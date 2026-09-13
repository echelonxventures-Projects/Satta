package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"strconv"
	"time"

	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/metering"

	"github.com/jackc/pgx/v5"
)

// schemaOwner names a package that owns a set of migrations.
//
// Each package embeds and owns its own migrations rather than contributing to a single global
// directory. That keeps the append-only triggers and row-level security policies in the same
// package as the code whose correctness depends on them — so a change to one is visible in
// review alongside the other.
type schemaOwner struct {
	name string
	fsys fs.FS
	dir  string
}

func schemaOwners() []schemaOwner {
	evidenceFS, evidenceDir := evidence.Migrations()
	meteringFS, meteringDir := metering.Migrations()
	ledgerFS, ledgerDir := ledger.Migrations()
	return []schemaOwner{
		{name: "evidence", fsys: evidenceFS, dir: evidenceDir},
		{name: "metering", fsys: meteringFS, dir: meteringDir},
		{name: "ledger", fsys: ledgerFS, dir: ledgerDir},
	}
}

func runMigrate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ExitOnError)
	common := registerCommon(flags)
	status := flags.Bool("status", false, "report applied migrations without applying anything")
	skipGrants := flags.Bool("skip-grants", false,
		"do not apply role grants (grants are mandatory per ADR-0001; use only when the roles do not exist)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := common.validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, common.timeout)
	defer cancel()

	pool, err := common.openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *status {
		return reportStatus(ctx, pool)
	}

	total := 0
	for _, owner := range schemaOwners() {
		applied, err := db.MigrateFS(ctx, pool, owner.name, owner.fsys, owner.dir)
		if err != nil {
			return fmt.Errorf("%s schema: %w", owner.name, err)
		}
		if len(applied) == 0 {
			fmt.Printf("  %-10s up to date\n", owner.name)
			continue
		}
		for _, version := range applied {
			fmt.Printf("  %-10s applied %04d\n", owner.name, version)
		}
		total += len(applied)
	}

	if total == 0 {
		fmt.Println("schema already up to date")
	} else {
		fmt.Printf("applied %d migration(s)\n", total)
	}

	if *skipGrants {
		fmt.Println("grants skipped — append-only enforcement is incomplete (ADR-0001)")
		return nil
	}
	return applyGrants(ctx, pool)
}

// Append-only enforcement is defined once, in the db package, so that
// platformctl and the conformance suites assert the same role model
// (ADR-0001: INSERT-only grants are the portable control; the triggers remain
// as optional hardening on PostgreSQL).

func applyGrants(ctx context.Context, pool *db.Pool) error {
	if err := db.ApplyAppendOnlyGrants(ctx, pool); err != nil {
		return err
	}
	fmt.Printf("grants applied: %d append-only table(s), %d mutable table(s)\n",
		len(db.AppendOnlyTables), len(db.MutableTables))
	return nil
}

// execGrant and the role/table grant model live in the db package
// (db.ApplyAppendOnlyGrants) so the conformance suites assert the same
// control platformctl applies.

func reportStatus(ctx context.Context, pool *db.Pool) error {
	type record struct {
		owner     string
		version   int
		name      string
		appliedAt time.Time
	}

	var applied []record
	err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT owner, version, name, applied_at FROM schema_migration ORDER BY owner, version`)
		if err != nil {
			if db.IsUndefinedTable(err) {
				return nil // Nothing has ever been applied.
			}
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r record
			if err := rows.Scan(&r.owner, &r.version, &r.name, &r.appliedAt); err != nil {
				return err
			}
			applied = append(applied, r)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}

	appliedByKey := make(map[string]record, len(applied))
	for _, r := range applied {
		appliedByKey[r.owner+"#"+strconv.Itoa(r.version)] = r
	}

	fmt.Printf("%-10s  %-8s  %-28s  %s\n", "OWNER", "VERSION", "NAME", "APPLIED (UTC)")
	pending := 0
	for _, owner := range schemaOwners() {
		migrations, err := db.LoadMigrations(owner.fsys, owner.dir)
		if err != nil {
			return fmt.Errorf("%s schema: %w", owner.name, err)
		}
		for _, m := range migrations {
			if r, ok := appliedByKey[owner.name+"#"+strconv.Itoa(m.Version)]; ok {
				fmt.Printf("%-10s  %04d      %-28s  %s\n",
					owner.name, m.Version, m.Name, r.appliedAt.UTC().Format(time.RFC3339))
				continue
			}
			fmt.Printf("%-10s  %04d      %-28s  %s\n", owner.name, m.Version, m.Name, "PENDING")
			pending++
		}
	}
	if pending > 0 {
		fmt.Printf("\n%d migration(s) pending — run 'platformctl migrate'\n", pending)
	}
	return nil
}
