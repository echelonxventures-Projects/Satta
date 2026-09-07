package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/metering"
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
	return []schemaOwner{
		{name: "evidence", fsys: evidenceFS, dir: evidenceDir},
		{name: "metering", fsys: meteringFS, dir: meteringDir},
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
		applied, err := db.MigrateFS(ctx, pool, owner.fsys, owner.dir)
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

// appendOnlyTables are the tables where UPDATE and DELETE must be impossible for the writing
// role, not merely refused by a trigger.
//
// ADR-0001 moves append-only enforcement from PostgreSQL triggers to INSERT-only grants,
// because grants exist on every database engine and triggers do not. The triggers remain as
// optional hardening on PostgreSQL, but these grants are what upholds Invariant 4's "no
// deletion path" clause on any engine — so this function is not optional, and
// --skip-grants prints a warning rather than passing silently.
var appendOnlyTables = []string{
	"evidence_record",
	"evidence_checkpoint",
	"usage_event",
}

// mutableTables carry state that legitimately changes — a chain head pointer moves with every
// append — and so are granted UPDATE.
var mutableTables = []string{
	"evidence_chain",
	"schema_migration",
}

func applyGrants(ctx context.Context, pool *db.Pool) error {
	// Roles are created by the substrate's first-boot script. On a database where they are
	// absent — a bare PostgreSQL a developer pointed this at — granting would fail, so their
	// presence is checked first and reported as a clear instruction rather than a driver error.
	missing, err := missingRoles(ctx, pool)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"roles %v do not exist; apply deploy/local/postgres/00-init.sql, or re-run with --skip-grants "+
				"and understand that append-only enforcement is then trigger-only (ADR-0001)", missing)
	}

	err = pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range appendOnlyTables {
			// SELECT and INSERT only. The absence of UPDATE and DELETE here is the control.
			if err := execGrant(ctx, tx,
				`GRANT SELECT, INSERT ON `+table+` TO satta_appender`); err != nil {
				return err
			}
			// Revoke explicitly rather than relying on never having granted. A previous
			// migration or a hand-run GRANT ALL would otherwise leave the hole open.
			if err := execGrant(ctx, tx,
				`REVOKE UPDATE, DELETE, TRUNCATE ON `+table+` FROM satta_appender, satta_app`); err != nil {
				return err
			}
			if err := execGrant(ctx, tx,
				`GRANT SELECT, INSERT ON `+table+` TO satta_app`); err != nil {
				return err
			}
			if err := execGrant(ctx, tx,
				`GRANT SELECT ON `+table+` TO satta_auditor`); err != nil {
				return err
			}
			// SAFETY INVARIANT 11: the auditor is read-only at the database layer too.
			if err := execGrant(ctx, tx,
				`REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON `+table+` FROM satta_auditor`); err != nil {
				return err
			}
		}
		for _, table := range mutableTables {
			if err := execGrant(ctx, tx,
				`GRANT SELECT, INSERT, UPDATE ON `+table+` TO satta_app, satta_appender`); err != nil {
				return err
			}
			if err := execGrant(ctx, tx,
				`REVOKE DELETE, TRUNCATE ON `+table+` FROM satta_app, satta_appender`); err != nil {
				return err
			}
			if err := execGrant(ctx, tx,
				`GRANT SELECT ON `+table+` TO satta_auditor`); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply grants: %w", err)
	}

	fmt.Printf("grants applied: %d append-only table(s), %d mutable table(s)\n",
		len(appendOnlyTables), len(mutableTables))
	return nil
}

// execGrant runs one grant statement, tolerating a missing table.
//
// Tables are created by whichever migrations have run. A grant against a table that does not
// exist yet is not a failure worth aborting a migration for — the next run will apply it once
// the owning migration lands.
func execGrant(ctx context.Context, tx pgx.Tx, stmt string) error {
	if _, err := tx.Exec(ctx, stmt); err != nil {
		if isUndefinedTable(err) {
			return nil
		}
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01" // undefined_table
	}
	return false
}

func missingRoles(ctx context.Context, pool *db.Pool) ([]string, error) {
	want := []string{"satta_app", "satta_appender", "satta_auditor"}
	var missing []string
	err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, role := range want {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
				return fmt.Errorf("check role %s: %w", role, err)
			}
			if !exists {
				missing = append(missing, role)
			}
		}
		return nil
	})
	sort.Strings(missing)
	return missing, err
}

func reportStatus(ctx context.Context, pool *db.Pool) error {
	type record struct {
		version   int
		name      string
		appliedAt time.Time
	}

	var applied []record
	err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT version, name, applied_at FROM schema_migration ORDER BY version`)
		if err != nil {
			if isUndefinedTable(err) {
				return nil // Nothing has ever been applied.
			}
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r record
			if err := rows.Scan(&r.version, &r.name, &r.appliedAt); err != nil {
				return err
			}
			applied = append(applied, r)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}

	appliedByVersion := make(map[int]record, len(applied))
	for _, r := range applied {
		appliedByVersion[r.version] = r
	}

	fmt.Printf("%-10s  %-8s  %-28s  %s\n", "OWNER", "VERSION", "NAME", "APPLIED (UTC)")
	pending := 0
	for _, owner := range schemaOwners() {
		migrations, err := db.LoadMigrations(owner.fsys, owner.dir)
		if err != nil {
			return fmt.Errorf("%s schema: %w", owner.name, err)
		}
		for _, m := range migrations {
			if r, ok := appliedByVersion[m.Version]; ok {
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
