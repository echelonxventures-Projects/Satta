// Package evidencetest holds the contract test suite that every evidence.Store
// implementation must pass.
//
// DEFINITION OF DONE, GATE 2: every external dependency sits behind a port with a
// local implementation and contract tests. The suite lives in its own package so
// that the in-memory double and the PostgreSQL store are held to one specification
// rather than to whatever each of their own test files happens to assert. When the
// two drift, isolation and idempotency bugs reach production through the path that
// was never tested.
package evidencetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// Factory builds a fresh, empty store for one test. Implementations that need
// cleanup register it with t.Cleanup.
type Factory func(t *testing.T) evidence.Store

// RunContractTests exercises the full Store contract.
func RunContractTests(t *testing.T, newStore Factory) {
	t.Helper()

	t.Run("AppendAssignsSequenceAndLinksChain", func(t *testing.T) {
		testAppendLinksChain(t, newStore(t))
	})
	t.Run("AppendIsIdempotent", func(t *testing.T) {
		testIdempotency(t, newStore(t))
	})
	t.Run("AppendBatchIsOrderedAndAtomic", func(t *testing.T) {
		testBatch(t, newStore(t))
	})
	t.Run("RejectsInvalidRecords", func(t *testing.T) {
		testValidation(t, newStore(t))
	})
	t.Run("RejectsFloatingPointPayloads", func(t *testing.T) {
		testNoFloatPayload(t, newStore(t))
	})
	t.Run("TenantIsolation", func(t *testing.T) {
		testTenantIsolation(t, newStore(t))
	})
	t.Run("RequiresTenantScope", func(t *testing.T) {
		testRequiresScope(t, newStore(t))
	})
	t.Run("HeadOfUnknownChainIsEmpty", func(t *testing.T) {
		testEmptyHead(t, newStore(t))
	})
	t.Run("VerifyIntactChain", func(t *testing.T) {
		testVerifyIntact(t, newStore(t))
	})
	t.Run("TimestampsAreUTCAndSurviveRoundTrip", func(t *testing.T) {
		testTimestamps(t, newStore(t))
	})
	t.Run("RangeIsInclusiveAndOrdered", func(t *testing.T) {
		testRange(t, newStore(t))
	})
}

// Fixtures. TenantA and TenantB exist so that isolation is checked against a
// store that actually holds another tenant's data — checking against an empty
// store proves nothing.
const (
	TenantA = "tenant-a"
	TenantB = "tenant-b"
	ChainA  = "tenant-a-money"
	ChainB  = "tenant-b-money"
)

// ScopeFor returns a tenant scope for the given tenant.
func ScopeFor(tenantID string) tenancy.Scope {
	return tenancy.Scope{TenantID: tenantID, BrandID: "", JurisdictionCode: "mt", RegionCode: "eu-west"}
}

// CtxFor returns a context carrying a tenant scope and a service actor.
func CtxFor(t *testing.T, tenantID string) context.Context {
	t.Helper()
	ctx, err := tenancy.WithScope(context.Background(), ScopeFor(tenantID))
	if err != nil {
		t.Fatalf("tenancy.WithScope: %v", err)
	}
	ctx, err = identity.WithActor(ctx, identity.ServiceActor("evidence-contract-test"))
	if err != nil {
		t.Fatalf("identity.WithActor: %v", err)
	}
	return ctx
}

// NewRecord builds a valid record for a tenant's chain.
func NewRecord(tenantID, chainID, action, idempotencyKey string) evidence.Record {
	return evidence.Record{
		ChainID:        chainID,
		Scope:          ScopeFor(tenantID),
		OccurredAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Category:       evidence.CategoryMoney,
		Action:         action,
		SubjectType:    "wallet",
		SubjectID:      "wallet-1",
		Actor:          identity.ServiceActor("ledger"),
		Payload:        map[string]any{"amount_minor_units": int64(1250), "currency_code": "EUR"},
		IdempotencyKey: idempotencyKey,
		RetentionClass: "financial-7y",
		CorrelationID:  "corr-" + idempotencyKey,
	}
}

func testAppendLinksChain(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)

	first, err := store.Append(ctx, NewRecord(TenantA, ChainA, "ledger.transfer.posted", "key-1"))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.Sequence)
	}
	if first.Deduplicated {
		t.Fatal("first append reported as deduplicated")
	}

	second, err := store.Append(ctx, NewRecord(TenantA, ChainA, "ledger.transfer.posted", "key-2"))
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}

	rec1, err := store.Get(ctx, ChainA, 1)
	if err != nil {
		t.Fatalf("get 1: %v", err)
	}
	if !bytes.Equal(rec1.PrevHash, evidence.GenesisHash) {
		t.Errorf("record 1 prev_hash = %x, want genesis", rec1.PrevHash)
	}
	if len(rec1.RecordHash) != evidence.HashSize {
		t.Errorf("record 1 hash length = %d, want %d", len(rec1.RecordHash), evidence.HashSize)
	}

	rec2, err := store.Get(ctx, ChainA, 2)
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if !bytes.Equal(rec2.PrevHash, rec1.RecordHash) {
		t.Errorf("record 2 prev_hash = %x, want record 1 hash %x", rec2.PrevHash, rec1.RecordHash)
	}

	head, err := store.Head(ctx, ChainA)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Sequence != 2 {
		t.Errorf("head sequence = %d, want 2", head.Sequence)
	}
	if !bytes.Equal(head.HeadHash, rec2.RecordHash) {
		t.Errorf("head hash = %x, want %x", head.HeadHash, rec2.RecordHash)
	}

	if _, err := store.Get(ctx, ChainA, 99); !errors.Is(err, evidence.ErrNotFound) {
		t.Errorf("get missing sequence: got %v, want ErrNotFound", err)
	}
}

func testIdempotency(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	record := NewRecord(TenantA, ChainA, "payment.deposit.captured", "same-key")

	first, err := store.Append(ctx, record)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	// A retried append after a lost response must not double-record the event.
	repeat, err := store.Append(ctx, record)
	if err != nil {
		t.Fatalf("repeat append: %v", err)
	}
	if !repeat.Deduplicated {
		t.Error("repeat append was not reported as deduplicated")
	}
	if repeat.Sequence != first.Sequence {
		t.Errorf("repeat sequence = %d, want %d", repeat.Sequence, first.Sequence)
	}
	if !bytes.Equal(repeat.RecordHash, first.RecordHash) {
		t.Error("repeat append returned a different record hash")
	}

	head, err := store.Head(ctx, ChainA)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Sequence != 1 {
		t.Fatalf("head sequence = %d after a duplicate append, want 1", head.Sequence)
	}

	found, err := store.FindByIdempotencyKey(ctx, ChainA, "same-key")
	if err != nil {
		t.Fatalf("FindByIdempotencyKey: %v", err)
	}
	if found.Sequence != first.Sequence {
		t.Errorf("found sequence = %d, want %d", found.Sequence, first.Sequence)
	}
	if _, err := store.FindByIdempotencyKey(ctx, ChainA, "never-used"); !errors.Is(err, evidence.ErrNotFound) {
		t.Errorf("FindByIdempotencyKey(unknown): got %v, want ErrNotFound", err)
	}
}

func testBatch(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	batch := []evidence.Record{
		NewRecord(TenantA, ChainA, "ledger.entry.debited", "b-1"),
		NewRecord(TenantA, ChainA, "ledger.entry.credited", "b-2"),
		NewRecord(TenantA, ChainA, "ledger.transaction.committed", "b-3"),
	}
	results, err := store.AppendBatch(ctx, ChainA, batch)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	for i, r := range results {
		if r.Sequence != uint64(i+1) {
			t.Errorf("result[%d].Sequence = %d, want %d", i, r.Sequence, i+1)
		}
	}

	// A batch containing an invalid record must commit nothing: a half-written
	// double-entry transaction in the audit trail is worse than none.
	badBatch := []evidence.Record{
		NewRecord(TenantA, ChainA, "ledger.entry.debited", "c-1"),
		func() evidence.Record {
			r := NewRecord(TenantA, ChainA, "ledger.entry.credited", "c-2")
			r.Action = "" // invalid
			return r
		}(),
	}
	if _, err := store.AppendBatch(ctx, ChainA, badBatch); err == nil {
		t.Fatal("expected AppendBatch with an invalid record to fail")
	}
	head, err := store.Head(ctx, ChainA)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Sequence != 3 {
		t.Fatalf("head sequence = %d after a rejected batch, want 3 — the batch was not atomic", head.Sequence)
	}
	if _, err := store.FindByIdempotencyKey(ctx, ChainA, "c-1"); !errors.Is(err, evidence.ErrNotFound) {
		t.Error("the valid record from a rejected batch was persisted")
	}

	if _, err := store.AppendBatch(ctx, ChainA, nil); err == nil {
		t.Error("expected empty batch to be rejected")
	}
}

func testValidation(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	base := func() evidence.Record { return NewRecord(TenantA, ChainA, "ledger.transfer.posted", "v-key") }

	cases := map[string]func(*evidence.Record){
		"empty chain id":        func(r *evidence.Record) { r.ChainID = "" },
		"empty action":          func(r *evidence.Record) { r.Action = "" },
		"unregistered category": func(r *evidence.Record) { r.Category = evidence.Category("made-up") },
		"no category":           func(r *evidence.Record) { r.Category = evidence.CategoryUnspecified },
		"empty subject type":    func(r *evidence.Record) { r.SubjectType = "" },
		"empty subject id":      func(r *evidence.Record) { r.SubjectID = "" },
		"no actor":              func(r *evidence.Record) { r.Actor = identity.Actor{} },
		"zero occurred_at":      func(r *evidence.Record) { r.OccurredAt = time.Time{} },
		"no idempotency key":    func(r *evidence.Record) { r.IdempotencyKey = "" },
		"no retention class":    func(r *evidence.Record) { r.RetentionClass = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rec := base()
			mutate(&rec)
			chainID := rec.ChainID
			if chainID == "" {
				chainID = ChainA
			}
			if _, err := store.AppendBatch(ctx, chainID, []evidence.Record{rec}); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func testNoFloatPayload(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	rec := NewRecord(TenantA, ChainA, "ledger.transfer.posted", "float-key")
	// This is the mistake the guard exists to catch: money as a float.
	rec.Payload = map[string]any{"amount": 12.34}

	_, err := store.Append(ctx, rec)
	if !errors.Is(err, evidence.ErrUnhashablePayload) {
		t.Fatalf("float payload: got %v, want ErrUnhashablePayload", err)
	}
}

func testTenantIsolation(t *testing.T, store evidence.Store) {
	ctxA, ctxB := CtxFor(t, TenantA), CtxFor(t, TenantB)

	if _, err := store.Append(ctxA, NewRecord(TenantA, ChainA, "ledger.transfer.posted", "iso-a")); err != nil {
		t.Fatalf("tenant A append: %v", err)
	}
	if _, err := store.Append(ctxB, NewRecord(TenantB, ChainB, "ledger.transfer.posted", "iso-b")); err != nil {
		t.Fatalf("tenant B append: %v", err)
	}

	// Read paths: B must not reach A's chain by naming it.
	if _, err := store.Get(ctxB, ChainA, 1); err == nil {
		t.Error("tenant B read tenant A's record by sequence")
	}
	if recs, err := store.Range(ctxB, ChainA, 1, 0); err == nil && len(recs) > 0 {
		t.Errorf("tenant B read %d of tenant A's records via Range", len(recs))
	}
	if head, err := store.Head(ctxB, ChainA); err == nil && head.Sequence > 0 {
		t.Error("tenant B read tenant A's chain head")
	}
	if _, err := store.FindByIdempotencyKey(ctxB, ChainA, "iso-a"); err == nil {
		t.Error("tenant B resolved tenant A's idempotency key")
	}

	// Write path: B must not append into A's chain, and must not append a record
	// carrying A's scope into its own chain.
	if _, err := store.Append(ctxB, NewRecord(TenantB, ChainA, "ledger.transfer.posted", "iso-cross")); err == nil {
		t.Error("tenant B appended into tenant A's chain")
	}
	spoofed := NewRecord(TenantA, ChainB, "ledger.transfer.posted", "iso-spoof")
	if _, err := store.Append(ctxB, spoofed); !errors.Is(err, tenancy.ErrScopeMismatch) {
		t.Errorf("tenant B appending a record scoped to tenant A: got %v, want ErrScopeMismatch", err)
	}

	// A's own view is unaffected by any of the above.
	headA, err := store.Head(ctxA, ChainA)
	if err != nil {
		t.Fatalf("tenant A head: %v", err)
	}
	if headA.Sequence != 1 {
		t.Errorf("tenant A head sequence = %d, want 1", headA.Sequence)
	}
}

func testRequiresScope(t *testing.T, store evidence.Store) {
	// Deny-by-default: no scope in context means no access, not global access.
	bare := context.Background()
	rec := NewRecord(TenantA, ChainA, "ledger.transfer.posted", "no-scope")

	if _, err := store.Append(bare, rec); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("append without scope: got %v, want ErrNoScope", err)
	}
	if _, err := store.Get(bare, ChainA, 1); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("get without scope: got %v, want ErrNoScope", err)
	}
	if _, err := store.Head(bare, ChainA); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("head without scope: got %v, want ErrNoScope", err)
	}
	if _, err := store.Range(bare, ChainA, 1, 0); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("range without scope: got %v, want ErrNoScope", err)
	}
}

func testEmptyHead(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	head, err := store.Head(ctx, "tenant-a-unused")
	if err != nil {
		t.Fatalf("head of unknown chain: %v", err)
	}
	if !head.IsEmpty() {
		t.Errorf("head sequence = %d, want 0", head.Sequence)
	}
	if !bytes.Equal(head.HeadHash, evidence.GenesisHash) {
		t.Errorf("head hash = %x, want genesis", head.HeadHash)
	}
}

func testVerifyIntact(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	const count = 25
	for i := 1; i <= count; i++ {
		rec := NewRecord(TenantA, ChainA, "ledger.transfer.posted", fmt.Sprintf("verify-%d", i))
		rec.Payload = map[string]any{
			"amount_minor_units": int64(i * 100),
			"currency_code":      "EUR",
			"nested":             map[string]any{"line": int64(i), "flag": i%2 == 0, "note": nil},
			"tags":               []any{"a", "b"},
		}
		if _, err := store.Append(ctx, rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	result, err := evidence.Verify(ctx, store, ChainA, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Intact {
		t.Fatalf("chain not intact: %s (at %d)", result.FailureReason, result.FirstBadSequence)
	}
	if result.RecordsChecked != count {
		t.Errorf("checked %d records, want %d", result.RecordsChecked, count)
	}
	if err := result.Err(); err != nil {
		t.Errorf("Err() on an intact result: %v", err)
	}

	// Verifying a sub-range must anchor to the record before it.
	partial, err := evidence.Verify(ctx, store, ChainA, 10, 20)
	if err != nil {
		t.Fatalf("Verify sub-range: %v", err)
	}
	if !partial.Intact || partial.RecordsChecked != 11 {
		t.Errorf("sub-range: intact=%v checked=%d, want true/11", partial.Intact, partial.RecordsChecked)
	}
}

func testTimestamps(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	// A non-UTC input with sub-microsecond precision: both must be normalised, or
	// the record will verify in memory and fail after a database round-trip.
	zone := time.FixedZone("UTC+5:30", 5*3600+1800)
	rec := NewRecord(TenantA, ChainA, "ledger.transfer.posted", "ts-1")
	rec.OccurredAt = time.Date(2026, 8, 1, 17, 30, 0, 123456789, zone)

	if _, err := store.Append(ctx, rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	stored, err := store.Get(ctx, ChainA, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.OccurredAt.Location() != time.UTC {
		t.Errorf("occurred_at location = %v, want UTC (SAFETY INVARIANT 12)", stored.OccurredAt.Location())
	}
	if stored.RecordedAt.Location() != time.UTC {
		t.Errorf("recorded_at location = %v, want UTC (SAFETY INVARIANT 12)", stored.RecordedAt.Location())
	}
	if stored.OccurredAt.Nanosecond()%1000 != 0 {
		t.Errorf("occurred_at = %v retains sub-microsecond precision, which PostgreSQL cannot store",
			stored.OccurredAt)
	}
	wantInstant := time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC)
	if !stored.OccurredAt.Equal(wantInstant) {
		t.Errorf("occurred_at = %v, want %v", stored.OccurredAt, wantInstant)
	}

	// The stored record must still verify after the round-trip.
	result, err := evidence.Verify(ctx, store, ChainA, 0, 0)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Intact {
		t.Fatalf("chain broken after a timestamp round-trip: %s", result.FailureReason)
	}
}

func testRange(t *testing.T, store evidence.Store) {
	ctx := CtxFor(t, TenantA)
	for i := 1; i <= 5; i++ {
		if _, err := store.Append(ctx, NewRecord(TenantA, ChainA, "ledger.transfer.posted", fmt.Sprintf("r-%d", i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	page, err := store.Range(ctx, ChainA, 2, 4)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("Range(2,4) returned %d records, want 3 (inclusive bounds)", len(page))
	}
	for i, rec := range page {
		if rec.Sequence != uint64(i+2) {
			t.Errorf("page[%d].Sequence = %d, want %d", i, rec.Sequence, i+2)
		}
	}
	// to=0 means "up to head".
	all, err := store.Range(ctx, ChainA, 1, 0)
	if err != nil {
		t.Fatalf("Range(1,0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("Range(1,0) returned %d records, want 5", len(all))
	}
	// A range beyond the head is empty, not an error.
	beyond, err := store.Range(ctx, ChainA, 50, 60)
	if err != nil {
		t.Fatalf("Range beyond head: %v", err)
	}
	if len(beyond) != 0 {
		t.Fatalf("Range(50,60) returned %d records, want 0", len(beyond))
	}
}
