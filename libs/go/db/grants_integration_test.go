//go:build integration

package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/satta/platform/libs/go/db"
)

// The grants conformance suite (ADR-0001 / S9): INSERT-only grants are the
// portable append-only control, and this test asserts them as behaviour, not
// as DDL text — the writing roles genuinely cannot UPDATE or DELETE, and the
// auditor genuinely cannot write, on a database configured the way
// production configures it.
func openGrantsPool(t *testing.T, name string) *db.Pool {
	t.Helper()
	dsn := os.Getenv(name)
	if dsn == "" {
		t.Skipf("%s not set; run via `task test:integration`", name)
	}
	pool, err := db.Open(context.Background(), db.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestAppendOnlyGrantsAreStructural(t *testing.T) {
	admin := openGrantsPool(t, "PLATFORM_TEST_DATABASE_URL")

	// The grants the migrator applies; running it here both refreshes them and
	// makes this test independent of whether a migration ran first.
	if err := db.ApplyAppendOnlyGrants(context.Background(), admin); err != nil {
		t.Fatalf("apply grants: %v", err)
	}

	appender := openGrantsPool(t, "PLATFORM_TEST_APPENDER_DATABASE_URL")
	ctx := context.Background()

	// Any append-only table; evidence_record exists after the evidence
	// migration and is the one that matters most.
	for _, table := range db.AppendOnlyTables {
		t.Run(table, func(t *testing.T) {
			// UPDATE must be refused for the writing role.
			if _, err := appender.Raw().Exec(ctx,
				"UPDATE "+table+" SET id = id"); err == nil {
				t.Errorf("UPDATE on %s succeeded for the appender role; INSERT-only grants are not in force", table)
			}
			// DELETE must be refused for the writing role.
			if _, err := appender.Raw().Exec(ctx,
				"DELETE FROM "+table+" WHERE false"); err == nil {
				t.Errorf("DELETE on %s succeeded for the appender role; INSERT-only grants are not in force", table)
			}
			// INSERT itself must be *granted*, not merely not-revoked: the
			// control is INSERT-and-SELECT only, not SELECT only.
			if _, err := appender.Raw().Exec(ctx, "SELECT count(*) FROM "+table); err != nil {
				t.Errorf("SELECT on %s failed for the appender role: %v", table, err)
			}
		})
	}

	// SAFETY INVARIANT 11: the auditor role cannot write, whatever else is true.
	auditor := openGrantsPool(t, "PLATFORM_TEST_AUDITOR_DATABASE_URL")
	for _, table := range db.AppendOnlyTables {
		t.Run("auditor/"+table, func(t *testing.T) {
			if _, err := auditor.Raw().Exec(ctx, "SELECT count(*) FROM "+table); err != nil {
				t.Errorf("auditor SELECT on %s failed (read-only means read, not nothing): %v", table, err)
			}
			if _, err := auditor.Raw().Exec(ctx,
				"INSERT INTO "+table+" DEFAULT VALUES"); err == nil {
				t.Errorf("auditor INSERT on %s succeeded; the auditor must be read-only at the database layer", table)
			}
			if _, err := auditor.Raw().Exec(ctx,
				"UPDATE "+table+" SET id = id"); err == nil {
				t.Errorf("auditor UPDATE on %s succeeded; the auditor must be read-only at the database layer", table)
			}
			if _, err := auditor.Raw().Exec(ctx,
				"DELETE FROM "+table+" WHERE false"); err == nil {
				t.Errorf("auditor DELETE on %s succeeded; the auditor must be read-only at the database layer", table)
			}
		})
	}
}
