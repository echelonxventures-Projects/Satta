//go:build integration

package evidence_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencetest"
)

// TestPostgresConcurrentAppendRaces exercises the optimistic concurrency path
// (ADR-0001 / S9): many writers appending to one chain concurrently must all
// land, exactly once each, with an unbroken chain — compare-and-swap on the
// head plus bounded retries is what makes that true without FOR UPDATE.
func TestPostgresConcurrentAppendRaces(t *testing.T) {
	admin := openTestPool(t, mustEnv(t, "PLATFORM_TEST_DATABASE_URL"))
	resetEvidenceSchema(t, admin)

	migratorPool, err := db.Open(context.Background(), db.Config{DSN: mustEnv(t, "PLATFORM_TEST_MIGRATOR_DATABASE_URL")})
	if err != nil {
		t.Fatalf("open migrator pool: %v", err)
	}
	defer migratorPool.Close()
	migrateStore := evidence.NewPostgresStore(migratorPool, clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	if merr := migrateStore.Migrate(context.Background()); merr != nil {
		t.Fatalf("migrate: %v", merr)
	}

	appPool, err := db.Open(context.Background(), db.Config{DSN: mustEnv(t, "PLATFORM_TEST_APP_DATABASE_URL")})
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer appPool.Close()
	store := evidence.NewPostgresStore(appPool, clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))

	const writers = 8
	const perWriter = 5
	ctx := evidencetest.CtxFor(t, evidencetest.TenantA)

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := "race-" + string(rune('a'+w)) + "-" + string(rune('0'+i))
				rec := evidencetest.NewRecord(evidencetest.TenantA, evidencetest.ChainA, "ledger.transfer.posted", key)
				if _, aerr := store.Append(ctx, rec); aerr != nil {
					errs <- aerr
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	// Every writer's append landed exactly once.
	head, err := store.Head(ctx, evidencetest.ChainA)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Sequence != writers*perWriter {
		t.Fatalf("head sequence = %d, want %d (lost or duplicated appends)", head.Sequence, writers*perWriter)
	}

	// And the chain that all those racing writers produced is intact.
	result, err := evidence.Verify(ctx, store, evidencetest.ChainA, 0, 0)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Intact {
		t.Fatalf("chain broken by concurrent appends at %d: %s", result.FirstBadSequence, result.FailureReason)
	}
}
