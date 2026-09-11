//go:build integration

package metering_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/metering"
	"github.com/satta/platform/libs/go/metering/meteringtest"
	"github.com/satta/platform/libs/go/tenancy"
)

// TestPostgresEmitterContract runs the emitter contract against the durable
// PostgreSQL emitter, with migrations applied as the migrator role and event
// traffic as the RLS-bound application role — the same production-shaped
// paths the evidence integration suite uses.
func TestPostgresEmitterContract(t *testing.T) {
	migratorDSN := os.Getenv("PLATFORM_TEST_MIGRATOR_DATABASE_URL")
	if migratorDSN == "" {
		t.Skip("PLATFORM_TEST_MIGRATOR_DATABASE_URL not set; run via `task test:integration`")
	}
	appDSN := os.Getenv("PLATFORM_TEST_APP_DATABASE_URL")
	if appDSN == "" {
		t.Skip("PLATFORM_TEST_APP_DATABASE_URL not set; run via `task test:integration`")
	}

	// Fresh usage schema for the suite: the trigger functions live outside
	// the table and must be dropped with it, or the re-applied migration
	// collides on CREATE FUNCTION.
	admin, err := db.Open(context.Background(), db.Config{DSN: os.Getenv("PLATFORM_TEST_DATABASE_URL")})
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	defer admin.Close()
	resetStmts := []string{
		`DROP TABLE IF EXISTS usage_event CASCADE`,
		`DROP FUNCTION IF EXISTS usage_event_enforce_immutability() CASCADE`,
		`DROP FUNCTION IF EXISTS usage_event_deny_truncate() CASCADE`,
		`DELETE FROM schema_migration WHERE owner = 'metering'`,
	}
	for _, stmt := range resetStmts {
		if _, rerr := admin.Raw().Exec(context.Background(), stmt); rerr != nil {
			t.Fatalf("reset usage schema (%q): %v", stmt, rerr)
		}
	}

	migratorPool, err := db.Open(context.Background(), db.Config{DSN: migratorDSN})
	if err != nil {
		t.Fatalf("open migrator: %v", err)
	}
	defer migratorPool.Close()
	em := metering.NewPostgresEmitter(migratorPool, meteringtest.FixedClock())
	if err := em.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	meteringtest.RunContractTests(t, func(t *testing.T) metering.Emitter {
		t.Helper()
		appPool, err := db.Open(context.Background(), db.Config{DSN: appDSN})
		if err != nil {
			t.Fatalf("open app pool: %v", err)
		}
		t.Cleanup(func() { appPool.Close() })
		return metering.NewPostgresEmitter(appPool, meteringtest.FixedClock())
	})
}

var errDeliberateRollback = &rollbackError{}

type rollbackError struct{}

func (*rollbackError) Error() string { return "deliberate rollback for atomicity test" }

// TestPostgresEmitTxCommitsWithCallerTransaction pins the property that makes
// TxEmitter load-bearing: a usage event emitted inside a caller transaction
// commits with it and rolls back with it. Billing for work that never
// happened, or performing work that was never billed, are both unrecoverable.
func TestPostgresEmitTxCommitsWithCallerTransaction(t *testing.T) {
	appDSN := os.Getenv("PLATFORM_TEST_APP_DATABASE_URL")
	if appDSN == "" {
		t.Skip("PLATFORM_TEST_APP_DATABASE_URL not set; run via `task test:integration`")
	}
	ctx := context.Background()
	appPool, err := db.Open(ctx, db.Config{DSN: appDSN})
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()
	emitter := metering.NewPostgresEmitter(appPool, clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))

	scopeCtx, err := tenancy.WithScope(ctx, tenancy.Scope{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}
	event := metering.UsageEvent{
		EventID:       "tx-atomicity-1",
		Scope:         tenancy.Scope{TenantID: "tenant-a"},
		ProductCode:   "sportsbook",
		MeterCode:     "bet.placed",
		Quantity:      1,
		Unit:          "count",
		OccurredAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		SourceService: "sportsbook-service",
		SubjectType:   "bet",
		SubjectID:     "bet-1",
	}

	// Rollback: the caller's failure must take the usage event with it.
	err = appPool.InTenantTx(scopeCtx, func(ctx context.Context, tx pgx.Tx) error {
		if _, txErr := emitter.EmitTx(ctx, tx, event); txErr != nil {
			return txErr
		}
		return errDeliberateRollback
	})
	if err == nil {
		t.Fatal("rollback path did not fail as arranged")
	}
	if _, gerr := emitter.Get(scopeCtx, "tx-atomicity-1"); gerr == nil {
		t.Fatal("usage event survived a rolled-back transaction — EmitTx is not atomic with caller work")
	}

	// Commit: the event survives.
	err = appPool.InTenantTx(scopeCtx, func(ctx context.Context, tx pgx.Tx) error {
		_, txErr := emitter.EmitTx(ctx, tx, event)
		return txErr
	})
	if err != nil {
		t.Fatalf("commit path: %v", err)
	}
	got, gerr := emitter.Get(scopeCtx, "tx-atomicity-1")
	if gerr != nil {
		t.Fatalf("committed event not readable: %v", gerr)
	}
	if got.EventID != "tx-atomicity-1" {
		t.Fatalf("got %q, want tx-atomicity-1", got.EventID)
	}
}
