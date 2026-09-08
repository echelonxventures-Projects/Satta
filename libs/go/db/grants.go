// Grants are the portable append-only control (ADR-0001): INSERT-and-SELECT
// role grants exist on every SQL engine, while PostgreSQL triggers do not.
// The triggers in the migrations remain as optional hardening on PostgreSQL;
// these grants are what upholds Invariant 4's "no deletion path" clause on any
// engine. This file is the single definition of the role model so that
// platformctl, the integration suites and any future tooling assert the same
// thing.
package db

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AppendOnlyTables are the tables where UPDATE and DELETE must be impossible
// for the writing roles, not merely refused by a trigger.
var AppendOnlyTables = []string{
	"evidence_record",
	"evidence_checkpoint",
	"usage_event",
}

// MutableTables carry state that legitimately changes — a chain head pointer
// moves with every append — and are granted UPDATE.
var MutableTables = []string{
	"evidence_chain",
	"schema_migration",
}

// Roles of the three-role model (ADR-0001). satta_migrator owns DDL and is
// granted nothing here — it is not a runtime role.
const (
	RoleApp      = "satta_app"
	RoleAppender = "satta_appender"
	RoleAuditor  = "satta_auditor"
)

// RequiredRoles lists the runtime roles grants depend on. Their absence is a
// substrate configuration error, reported distinctly from grant failures.
var RequiredRoles = []string{RoleApp, RoleAppender, RoleAuditor}

// ApplyAppendOnlyGrants applies the role grants that make append-only a
// structural property of the database rather than a convention of the code.
//
// Not optional: this function is what enforces Invariant 4 on engines without
// triggers. --skip-grants in platformctl prints a warning precisely because
// skipping it leaves enforcement trigger-only.
func ApplyAppendOnlyGrants(ctx context.Context, pool *Pool) error {
	missing, err := MissingRoles(ctx, pool)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"roles %v do not exist; apply deploy/local/postgres/00-init.sql, or run with --skip-grants "+
				"and understand that append-only enforcement is then trigger-only (ADR-0001)", missing)
	}

	err = pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range AppendOnlyTables {
			// SELECT and INSERT only. The absence of UPDATE and DELETE here is the control.
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`GRANT SELECT, INSERT ON %s TO %s`, table, RoleAppender)); gerr != nil {
				return gerr
			}
			// Revoke explicitly rather than relying on never having granted. A previous
			// migration or a hand-run GRANT ALL would otherwise leave the hole open.
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`REVOKE UPDATE, DELETE, TRUNCATE ON %s FROM %s, %s`, table, RoleAppender, RoleApp)); gerr != nil {
				return gerr
			}
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`GRANT SELECT, INSERT ON %s TO %s`, table, RoleApp)); gerr != nil {
				return gerr
			}
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`GRANT SELECT ON %s TO %s`, table, RoleAuditor)); gerr != nil {
				return gerr
			}
			// SAFETY INVARIANT 11: the auditor is read-only at the database layer too.
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %s FROM %s`, table, RoleAuditor)); gerr != nil {
				return gerr
			}
		}
		for _, table := range MutableTables {
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE ON %s TO %s, %s`, table, RoleApp, RoleAppender)); gerr != nil {
				return gerr
			}
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`REVOKE DELETE, TRUNCATE ON %s FROM %s, %s`, table, RoleApp, RoleAppender)); gerr != nil {
				return gerr
			}
			if gerr := execGrant(ctx, tx,
				fmt.Sprintf(`GRANT SELECT ON %s TO %s`, table, RoleAuditor)); gerr != nil {
				return gerr
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply grants: %w", err)
	}
	return nil
}

// MissingRoles reports which of RequiredRoles do not exist on the server.
func MissingRoles(ctx context.Context, pool *Pool) ([]string, error) {
	var missing []string
	err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, role := range RequiredRoles {
			var exists bool
			if rerr := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); rerr != nil {
				return fmt.Errorf("check role %s: %w", role, rerr)
			}
			if !exists {
				missing = append(missing, role)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(missing)
	return missing, nil
}

// execGrant runs one grant statement, tolerating a missing table: tables are
// created by whichever migrations have run, and a grant against a table that
// does not exist yet is not a failure — the next run applies it once the
// owning migration lands.
func execGrant(ctx context.Context, tx pgx.Tx, stmt string) error {
	if _, err := tx.Exec(ctx, stmt); err != nil {
		if IsUndefinedTable(err) {
			return nil
		}
		return fmt.Errorf("%s: %w", stmt, err)
	}
	return nil
}

// IsUndefinedTable reports whether err is PostgreSQL's undefined_table error.
func IsUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}
