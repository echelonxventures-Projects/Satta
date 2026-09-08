package evidence_test

import (
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencetest"
)

// TestMemoryStoreWireContract runs the full Store contract through a real
// gRPC server and client over bufconn. This is the ADR-0001 conformance
// retrofit: in-process Go tests cannot validate the wire contract, and any
// future non-Go implementation is validated by pointing this same suite at
// its server.
func TestMemoryStoreWireContract(t *testing.T) {
	evidencetest.RunWireContractTests(t, evidencetest.WireOver(func(t *testing.T) evidence.Store {
		return evidence.NewMemoryStore(clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	}))
}

// TestWireOnlyBehaviour covers the checks that exist only because there is a
// wire: metadata denial, auditor enforcement through RPC, error identity and
// the generated surface's structural read/append-only shape.
func TestWireOnlyBehaviour(t *testing.T) {
	evidencetest.RunWireOnlyTests(t, evidencetest.WireOver(func(t *testing.T) evidence.Store {
		return evidence.NewMemoryStore(clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	}))
}
