//go:build integration

package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/satta/platform/libs/go/db"
)

// The migration engine's contract: forward-only with verified checksums, and
// owner-scoped versions so two packages can each have a 0001. The engine is
// what makes every later schema change safe to reason about; these tests pin
// the properties the production discipline depends on.

func openAdmin(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("PLATFORM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PLATFORM_TEST_DATABASE_URL not set; run via `task test:integration`")
	}
	pool, err := db.Open(context.Background(), db.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func alphaMigrations() []db.Migration {
	return []db.Migration{{
		Version: 1, Name: "alpha",
		SQL:      `CREATE TABLE IF NOT EXISTS migration_engine_probe (id int)`,
		Checksum: "stable-checksum-alpha-v1",
	}}
}

func resetOwners(t *testing.T, pool *db.Pool, owners ...string) {
	t.Helper()
	for _, owner := range owners {
		if _, err := pool.Raw().Exec(context.Background(),
			`DELETE FROM schema_migration WHERE owner = $1`, owner); err != nil {
			t.Fatalf("reset owner %s: %v", owner, err)
		}
	}
}

func TestMigrationsAreOwnerScoped(t *testing.T) {
	pool := openAdmin(t)
	ctx := context.Background()
	resetOwners(t, pool, "owner-alpha", "owner-beta")

	// Two owners may each have a version 1 — the owner/version pair is the
	// key, not the version alone. The collision this prevents was a real
	// defect in the engine's first draft.
	if _, err := db.Migrate(ctx, pool, "owner-alpha", alphaMigrations()); err != nil {
		t.Fatalf("migrate owner-alpha: %v", err)
	}
	if _, err := db.Migrate(ctx, pool, "owner-beta", alphaMigrations()); err != nil {
		t.Fatalf("migrate owner-beta with its own version 1: %v", err)
	}

	// Re-running is a no-op for both.
	applied, err := db.Migrate(ctx, pool, "owner-alpha", alphaMigrations())
	if err != nil || len(applied) != 0 {
		t.Fatalf("idempotent re-run: applied=%v err=%v", applied, err)
	}
}

func TestEditedMigrationIsRejected(t *testing.T) {
	pool := openAdmin(t)
	ctx := context.Background()
	const owner = "owner-checksum"
	resetOwners(t, pool, owner)

	if _, err := db.Migrate(ctx, pool, owner, alphaMigrations()); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	// The same version, different text: the checksum guard must refuse —
	// environments that ran the old text and ones that would run the new
	// text have silently different schemas, which is exactly what the guard
	// exists to prevent.
	altered := []db.Migration{{
		Version: 1, Name: "alpha",
		SQL:      `CREATE TABLE IF NOT EXISTS checksum_probe (id int)`,
		Checksum: "not-the-recorded-checksum",
	}}
	_, err := db.Migrate(ctx, pool, owner, altered)
	if err == nil {
		t.Fatal("an edited migration was accepted — the checksum guard did not fire")
	}
	if !contains(err.Error(), "has been modified") {
		t.Fatalf("guard error = %v, want a modification report", err)
	}
}

func TestNilPoolIsRejected(t *testing.T) {
	if _, err := db.Migrate(context.Background(), nil, "x", nil); err == nil {
		t.Fatal("nil pool accepted")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
