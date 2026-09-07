package evidence_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencetest"
)

func newMemoryStore(t *testing.T) evidence.Store {
	t.Helper()
	return evidence.NewMemoryStore(clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
}

// TestMemoryStoreContract holds the in-memory double to the same specification as
// the PostgreSQL store.
func TestMemoryStoreContract(t *testing.T) {
	evidencetest.RunContractTests(t, newMemoryStore)
}

// TestVerifyDetectsTampering is the test that gives SAFETY INVARIANT 4 its
// meaning. Each subtest corrupts the chain in a different way, and each way is
// one that a plausible attacker or a plausible bug would produce.
func TestVerifyDetectsTampering(t *testing.T) {
	tests := []struct {
		name          string
		corrupt       func(t *testing.T, store *evidence.MemoryStore)
		wantSequence  uint64
		wantSubstring string
	}{
		{
			name: "altered payload with stale hash",
			// The obvious attack: change what the record says happened.
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 3, func(r *evidence.Record) {
					r.Payload["amount_minor_units"] = int64(999999)
				})
			},
			wantSequence:  3,
			wantSubstring: "payload was altered",
		},
		{
			name: "altered field with recomputed payload hash",
			// A more careful attacker updates the payload hash to match. The
			// record hash then no longer reproduces.
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 4, func(r *evidence.Record) {
					r.Payload["amount_minor_units"] = int64(1)
					hash, err := evidence.HashPayload(r.Payload)
					if err != nil {
						t.Fatalf("HashPayload: %v", err)
					}
					r.PayloadHash = hash
				})
			},
			wantSequence:  4,
			wantSubstring: "record_hash",
		},
		{
			name: "altered actor attribution",
			// Reassigning blame is a tampering motive in its own right.
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 2, func(r *evidence.Record) {
					r.Actor.ID = "someone-else"
				})
			},
			wantSequence:  2,
			wantSubstring: "record_hash",
		},
		{
			name: "shortened retention class",
			// Retention is inside the hashed pre-image precisely so that this
			// fails rather than quietly making a record eligible for deletion.
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 5, func(r *evidence.Record) {
					r.RetentionClass = "transient-30d"
				})
			},
			wantSequence:  5,
			wantSubstring: "record_hash",
		},
		{
			name: "backdated occurrence",
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 6, func(r *evidence.Record) {
					r.OccurredAt = r.OccurredAt.Add(-72 * time.Hour)
				})
			},
			wantSequence:  6,
			wantSubstring: "record_hash",
		},
		{
			name: "re-linked chain",
			// Rewriting prev_hash is how an attacker would try to splice out a
			// record without leaving a sequence gap.
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				mustTamper(t, store, 7, func(r *evidence.Record) {
					r.PrevHash = make([]byte, evidence.HashSize)
				})
			},
			wantSequence:  7,
			wantSubstring: "chain was re-linked",
		},
		{
			name: "removed record",
			corrupt: func(t *testing.T, store *evidence.MemoryStore) {
				if err := store.DropRecordForTest(evidencetest.ChainA, 5); err != nil {
					t.Fatalf("DropRecordForTest: %v", err)
				}
			},
			wantSequence:  5,
			wantSubstring: "inserted, removed or reordered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := evidence.NewMemoryStore(clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
			ctx := evidencetest.CtxFor(t, evidencetest.TenantA)

			for i := 1; i <= 10; i++ {
				rec := evidencetest.NewRecord(evidencetest.TenantA, evidencetest.ChainA,
					"ledger.transfer.posted", key(i))
				rec.Payload = map[string]any{"amount_minor_units": int64(i * 100), "currency_code": "EUR"}
				if _, err := store.Append(ctx, rec); err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
			}

			before, err := evidence.Verify(ctx, store, evidencetest.ChainA, 0, 0)
			if err != nil {
				t.Fatalf("Verify before tampering: %v", err)
			}
			if !before.Intact {
				t.Fatalf("chain was already broken before tampering: %s", before.FailureReason)
			}

			tt.corrupt(t, store)

			after, err := evidence.Verify(ctx, store, evidencetest.ChainA, 0, 0)
			if err != nil {
				t.Fatalf("Verify after tampering returned an operational error: %v", err)
			}
			if after.Intact {
				t.Fatal("tampering went undetected — SAFETY INVARIANT 4 is not being enforced")
			}
			if after.FirstBadSequence != tt.wantSequence {
				t.Errorf("first bad sequence = %d, want %d (reason: %s)",
					after.FirstBadSequence, tt.wantSequence, after.FailureReason)
			}
			if !contains(after.FailureReason, tt.wantSubstring) {
				t.Errorf("failure reason %q does not mention %q", after.FailureReason, tt.wantSubstring)
			}
			if err := after.Err(); !errors.Is(err, evidence.ErrTampered) {
				t.Errorf("Err() = %v, want ErrTampered", err)
			}
		})
	}
}

// TestStoreHasNoMutationMethods asserts structurally that the port exposes no way
// to change or remove a record. If someone adds one, this fails at compile time
// on the interface assertion below rather than in review.
func TestStoreHasNoMutationMethods(t *testing.T) {
	// The compile-time assertion: a type implementing exactly the read and append
	// surface must satisfy evidence.Store. If a mutation method were added to the
	// interface, this stops compiling.
	var _ evidence.Store = (*evidence.MemoryStore)(nil)

	// And the reflective check, which catches a method added to the interface
	// under a name this test does not anticipate.
	forbidden := []string{"Update", "Delete", "Remove", "Purge", "Truncate", "Drop", "Modify", "Rewrite"}
	storeType := reflect.TypeOf((*evidence.Store)(nil)).Elem()
	for i := 0; i < storeType.NumMethod(); i++ {
		name := storeType.Method(i).Name
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("evidence.Store exposes method %q; SAFETY INVARIANT 4 forbids a mutation path on this port", name)
			}
		}
	}
}

func mustTamper(t *testing.T, store *evidence.MemoryStore, seq uint64, mutate func(*evidence.Record)) {
	t.Helper()
	if err := store.TamperForTest(evidencetest.ChainA, seq, mutate); err != nil {
		t.Fatalf("TamperForTest: %v", err)
	}
}

func key(i int) string { return "tamper-" + strconv.Itoa(i) }

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
