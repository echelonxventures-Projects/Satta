//go:build integration

// Integration tests run against a real PostgreSQL substrate, gated on
// PLATFORM_TEST_DATABASE_URL (a separate variable from DATABASE_URL on
// purpose: a suite that truncates tables must never reach a database just
// because a service connection string happened to be exported).
package evidence_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencetest"
)

// TestPostgresStoreWireContract runs the full wire contract against the
// durable store: the same eleven scenarios plus the wire-only checks, with
// the PostgreSQL adapter behind the gRPC boundary.
//
// The store connects as satta_app — the ordinary request role, subject to
// row-level security — while schema reset and migration use an admin pool.
// Running traffic as the RLS-bound role is the point: tenant isolation is one
// of the eleven scenarios, and a superuser connection would bypass the very
// control the scenario exists to prove.
func TestPostgresStoreWireContract(t *testing.T) {
	admin := openTestPool(t, mustEnv(t, "PLATFORM_TEST_DATABASE_URL"))

	factory := evidencetest.WireOver(func(t *testing.T) evidence.Store {
		t.Helper()
		resetEvidenceSchema(t, admin)

		// Migrations run as the migrator role, exactly as platformctl does in
		// production: its ALTER DEFAULT PRIVILEGES grant the application
		// roles their access at table-creation time, so a schema created by
		// any other role would silently leave satta_app without grants.
		migratorDSN := mustEnv(t, "PLATFORM_TEST_MIGRATOR_DATABASE_URL")
		migratorPool, err := db.Open(context.Background(), db.Config{DSN: migratorDSN})
		if err != nil {
			t.Fatalf("open migrator pool: %v", err)
		}
		defer migratorPool.Close()

		migrateStore := evidence.NewPostgresStore(migratorPool, clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
		if merr := migrateStore.Migrate(context.Background()); merr != nil {
			t.Fatalf("migrate evidence schema: %v", merr)
		}

		appDSN := mustEnv(t, "PLATFORM_TEST_APP_DATABASE_URL")
		appPool, err := db.Open(context.Background(), db.Config{DSN: appDSN})
		if err != nil {
			t.Fatalf("open app pool: %v", err)
		}
		t.Cleanup(func() { appPool.Close() })

		return evidence.NewPostgresStore(appPool, clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	})

	evidencetest.RunWireContractTests(t, factory)
	evidencetest.RunWireOnlyTests(t, factory)
}

// mustEnv reads an environment variable or fails with a clear instruction.
func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; run via `task test:integration`", name)
	}
	return v
}

// openTestPool opens a pool described by dsn.
func openTestPool(t *testing.T, dsn string) *db.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), db.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// resetEvidenceSchema clears evidence-owned objects between tests. The evidence
// tables carry immutability triggers for the application roles; the test runs
// as the schema-owning migration role, for which objects are droppable — which
// is the governed way to reset an environment (the same authority the
// migrator role has, used for the same purpose). Trigger functions and
// policies live outside the tables, so they are dropped explicitly.
func resetEvidenceSchema(t *testing.T, pool *db.Pool) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP TABLE IF EXISTS evidence_checkpoint, evidence_record, evidence_chain CASCADE`,
		`DROP FUNCTION IF EXISTS evidence_deny_mutation() CASCADE`,
		`DROP FUNCTION IF EXISTS evidence_chain_forward_only() CASCADE`,
		// Hand the bookkeeping table to the migrator role, mirroring a fresh
		// database where the migrator's own first run creates it. The table
		// holds no evidence — it records which migration files have run — so
		// ownership is infrastructure state, not audit history.
		`ALTER TABLE schema_migration OWNER TO satta_migrator`,
		// schema_migration is mutable infrastructure state (not evidence);
		// remove the evidence owner's rows so Migrate re-applies cleanly.
		`DELETE FROM schema_migration WHERE owner = 'evidence'`,
	}
	for _, stmt := range stmts {
		if _, err := pool.Raw().Exec(ctx, stmt); err != nil {
			t.Fatalf("reset evidence schema (%q): %v", stmt, err)
		}
	}
}
