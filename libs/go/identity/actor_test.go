package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/satta/platform/libs/go/identity"
)

// The identity package is the attribution contract: every audit record names
// an actor, and the auditor type is structurally read-only (SAFETY INVARIANT
// 11). These tests pin both.

func TestActorValidate(t *testing.T) {
	if err := (identity.Actor{}).Validate(); err == nil {
		t.Fatal("empty actor accepted — no anonymous fallback exists")
	}
	if err := (identity.Actor{Type: identity.TypeService}).Validate(); err == nil {
		t.Fatal("actor without ID accepted")
	}
	if err := (identity.Actor{Type: identity.TypeService, ID: "ledger"}).Validate(); err != nil {
		t.Fatalf("valid actor rejected: %v", err)
	}
}

func TestAuditorIsStructurallyReadOnly(t *testing.T) {
	auditor := identity.Actor{Type: identity.TypeAuditor, ID: "audit-1"}
	if !auditor.IsReadOnly() {
		t.Fatal("auditor is not read-only")
	}
	if err := auditor.RequireWriteCapable(); err == nil {
		t.Fatal("auditor passed RequireWriteCapable — INVARIANT 11 broken")
	}
	// Every non-auditor type is write-capable (subject to authorisation
	// elsewhere); the structural rule is about the auditor only.
	for _, typ := range []identity.Type{
		identity.TypePlayer, identity.TypeOperatorUser, identity.TypePlatformUser,
		identity.TypeAgent, identity.TypePartnerAPIClient, identity.TypeService,
		identity.TypeRegulator,
	} {
		actor := identity.Actor{Type: typ, ID: "x"}
		if actor.IsReadOnly() {
			t.Fatalf("type %s marked read-only; only auditors are", typ)
		}
		if err := actor.RequireWriteCapable(); err != nil {
			t.Fatalf("type %s failed RequireWriteCapable: %v", typ, err)
		}
	}
	// A write-capable check on an invalid actor still fails on validity.
	if err := (identity.Actor{Type: identity.TypeAuditor}).RequireWriteCapable(); err == nil {
		t.Fatal("invalid auditor passed RequireWriteCapable")
	}
}

func TestContextRoundTripAndDenyByDefault(t *testing.T) {
	want := identity.Actor{Type: identity.TypeOperatorUser, ID: "op-7", SessionID: "sess-1"}
	ctx, err := identity.WithActor(context.Background(), want)
	if err != nil {
		t.Fatalf("WithActor: %v", err)
	}
	got, ok := identity.FromContext(ctx)
	if !ok || got != want {
		t.Fatalf("FromContext = %v, %v", got, ok)
	}

	_, err = identity.RequireFromContext(context.Background())
	if !errors.Is(err, identity.ErrNoActor) {
		t.Fatalf("RequireFromContext on bare context = %v, want ErrNoActor (deny-by-default)", err)
	}
}

func TestStringOmitsDisplayName(t *testing.T) {
	// DisplayName is personal data; String renders for logs, where it must not
	// appear (the package comment says so).
	a := identity.Actor{Type: identity.TypePlayer, ID: "p1", DisplayName: "Jane Doe"}
	if got := a.String(); got != "player:p1" {
		t.Fatalf("String = %q, want %q — display name leaked", got, "player:p1")
	}
	// OnBehalfOf is attribution, not personal data: it must appear.
	b := identity.Actor{Type: identity.TypeService, ID: "ledger", OnBehalfOf: "operator_user:op-7"}
	if got := b.String(); got != "service:ledger(on_behalf_of=operator_user:op-7)" {
		t.Fatalf("String = %q, want on_behalf_of attribution retained", got)
	}
}
