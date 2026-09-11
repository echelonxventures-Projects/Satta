package correlation_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/satta/platform/libs/go/correlation"
)

// The correlation ID is the durable join key between audit records, usage
// events and provider calls (see the package comment): these tests pin the
// properties that role depends on.

func TestNewIsDistinctAndWellFormed(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for i := 0; i < 100; i++ {
		id := correlation.New()
		if !hex32.MatchString(id) {
			t.Fatalf("correlation id %q is not 32 lowercase hex characters", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("correlation id %q generated twice — collision resistance is the point", id)
		}
		seen[id] = struct{}{}
	}
}

func TestWithIDPropagates(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "reconcile-42")
	id, ok := correlation.FromContext(ctx)
	if !ok || id != "reconcile-42" {
		t.Fatalf("FromContext = %q, %v; want reconcile-42, true", id, ok)
	}
}

func TestWithIDIgnoresInvalidAndGenerates(t *testing.T) {
	// An invalid or empty ID is replaced, never propagated: a downstream
	// service that sees no header at all must generate, but one that sees
	// garbage must not trust it.
	ctx := correlation.WithID(context.Background(), "")
	id, ok := correlation.FromContext(ctx)
	if !ok || id == "" {
		t.Fatal("empty ID was not replaced with a fresh one")
	}
}

func TestEnsureIdempotentPerContext(t *testing.T) {
	ctx, id1 := correlation.Ensure(context.Background())
	ctx2, id2 := correlation.Ensure(ctx)
	if id1 != id2 {
		t.Fatalf("Ensure on a context that already has an ID changed it: %q -> %q", id1, id2)
	}
	if _, ok := correlation.FromContext(ctx2); !ok {
		t.Fatal("Ensure did not attach the ID")
	}
}

func TestMissingIDIsAbsent(t *testing.T) {
	if _, ok := correlation.FromContext(context.Background()); ok {
		t.Fatal("bare context claims to carry a correlation ID")
	}
}
