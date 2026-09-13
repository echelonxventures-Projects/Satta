//go:build integration

package ledger_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/ledger/ledgercontract"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// TestPostgresLedgerContract runs the same nine scenarios the memory
// binding passes — the point of the contract suite is that the durable
// binding inherits the invariants, including the database-level deferred
// balance trigger (a port bypass cannot post an unbalanced entry either).
func TestPostgresLedgerContract(t *testing.T) {
	admin := mustPool(t, mustEnv(t, "PLATFORM_TEST_DATABASE_URL"))

	ledgercontract.RunContractTests(t, func(t *testing.T) ledger.Ledger {
		t.Helper()
		resetLedgerSchema(t, admin)

		migrator := mustPool(t, mustEnv(t, "PLATFORM_TEST_MIGRATOR_DATABASE_URL"))
		migrateStore := ledger.NewPostgresLedger(migrator, clock.NewFixed(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)))
		if err := migrateStore.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		migrator.Close()

		app := mustPool(t, mustEnv(t, "PLATFORM_TEST_APP_DATABASE_URL"))
		t.Cleanup(func() { app.Close() })
		return ledger.NewPostgresLedger(app, clock.NewFixed(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)))
	})
}

// TestPostgresDBLevelBalanceGuard: the deferred constraint trigger must
// reject an unbalanced entry even when written through raw SQL, bypassing
// every Go check — the layering claim in the migration's header comment,
// tested rather than asserted.
func TestPostgresDBLevelBalanceGuard(t *testing.T) {
	app := mustPool(t, mustEnv(t, "PLATFORM_TEST_APP_DATABASE_URL"))
	ctx := context.Background()
	scopeCtx, err := tenancy.WithScope(ctx, tenancy.Scope{TenantID: "tenant-guard"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	l := ledger.NewPostgresLedger(app, clock.NewFixed(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)))
	eur := money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}
	if oerr := l.OpenAccount(scopeCtx, ledger.Account{ID: "guard-a", Kind: ledger.KindHouse, Currency: eur,
		Scope: tenancy.Scope{TenantID: "tenant-guard"}}); oerr != nil {
		t.Fatalf("open: %v", oerr)
	}
	// One posting alone is unbalanced; the Go Validate would refuse it, so
	// this goes in via raw SQL under the same tenant scope.
	err = app.InTenantTx(scopeCtx, func(ctx context.Context, tx pgx.Tx) error {
		var entryID int64
		if qerr := tx.QueryRow(ctx,
			`INSERT INTO ledger_entry (tenant_id, idempotency_key, occurred_at, recorded_at)
			 VALUES ($1, $2, now(), now()) RETURNING id`, "tenant-guard", "guard-1",
		).Scan(&entryID); qerr != nil {
			return qerr
		}
		if _, perr := tx.Exec(ctx,
			`INSERT INTO ledger_posting (entry_id, account_id, tenant_id, currency_code, minor_units)
			 VALUES ($1, $2, $3, $4, 500)`, entryID, "guard-a", "tenant-guard", "EUR",
		); perr != nil {
			return perr
		}
		// The deferred constraint trigger fires at COMMIT: returning nil here
		// lets InTenantTx attempt the commit, which must fail.
		return nil
	})
	if err == nil {
		t.Fatal("unbalanced entry committed through raw SQL — the database-level guard is not in force")
	}
	if !contains(err.Error(), "does not balance") {
		t.Fatalf("expected the balance trigger rejection, got: %v", err)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func mustPool(t *testing.T, dsn string) *db.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), db.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open pool (%s): %v", dsn, err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; run via `task test:integration`", name)
	}
	return v
}

func resetLedgerSchema(t *testing.T, admin *db.Pool) {
	t.Helper()
	stmts := []string{
		`DROP TABLE IF EXISTS ledger_posting, ledger_entry, ledger_account CASCADE`,
		`DROP FUNCTION IF EXISTS ledger_deny_mutation() CASCADE`,
		`DROP FUNCTION IF EXISTS ledger_posting_scope_matches_entry() CASCADE`,
		`DROP FUNCTION IF EXISTS ledger_entry_balanced() CASCADE`,
		`DELETE FROM schema_migration WHERE owner = 'ledger'`,
	}
	for _, stmt := range stmts {
		if _, err := admin.Raw().Exec(context.Background(), stmt); err != nil {
			t.Fatalf("reset ledger schema (%q): %v", stmt, err)
		}
	}
}
