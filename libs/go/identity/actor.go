// Package identity carries the authenticated principal responsible for an
// action through the call stack.
//
// Every audit record names an actor. There is no "system" actor that stands in
// for an unattributed change: a background job runs as a named service
// principal, and a job triggered by a human records the human as the initiator.
// This package therefore has no anonymous fallback.
//
// The full authentication and authorisation implementation belongs to the IAM
// work; this package defines the shape that the rest of the platform depends on
// so that services do not each invent their own.
package identity

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoActor is returned when an action requiring attribution has no actor in
// context. Deny-by-default.
var ErrNoActor = errors.New("identity: no actor in context")

// Type classifies a principal.
type Type string

const (
	TypeUnspecified      Type = ""
	TypePlayer           Type = "player"
	TypeOperatorUser     Type = "operator_user"
	TypePlatformUser     Type = "platform_user"
	TypeAgent            Type = "agent"
	TypePartnerAPIClient Type = "partner_api_client"
	TypeService          Type = "service"
	TypeRegulator        Type = "regulator"
	// TypeAuditor is structurally read-only.
	//
	// SAFETY INVARIANT 11: no write path in the platform accepts an auditor
	// actor. IsReadOnly below is what enforcement checks, so the constraint
	// lives with the type rather than being restated per service.
	TypeAuditor Type = "auditor"
)

// Actor is an authenticated principal.
type Actor struct {
	Type Type
	// ID is the stable identifier of the principal within Type.
	ID string
	// DisplayName is captured at action time for evidentiary value. It may
	// become stale; it is never used for authorisation.
	DisplayName string
	// SessionID correlates the action to an authenticated session, where one
	// exists.
	SessionID string
	// SourceIP is the observed client address, where known.
	SourceIP string
	// OnBehalfOf names the principal a service is acting for, when a service
	// principal performs work initiated by a human or another tenant. It keeps
	// the human attributable through asynchronous hops.
	OnBehalfOf string
}

// Validate reports whether the actor is usable for attribution.
func (a Actor) Validate() error {
	if a.Type == TypeUnspecified {
		return fmt.Errorf("identity: actor has no type")
	}
	if a.ID == "" {
		return fmt.Errorf("identity: actor of type %s has no id", a.Type)
	}
	return nil
}

// IsReadOnly reports whether the actor may never perform a state change.
func (a Actor) IsReadOnly() bool {
	return a.Type == TypeAuditor
}

// RequireWriteCapable returns an error if the actor is structurally read-only.
// Write paths call this before mutating anything.
func (a Actor) RequireWriteCapable() error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.IsReadOnly() {
		return fmt.Errorf("identity: actor type %s is read-only and cannot perform state changes", a.Type)
	}
	return nil
}

// String renders the actor for logs. DisplayName is omitted deliberately: it can
// contain a player's name, and logs are a wider audience than audit records.
func (a Actor) String() string {
	if a.OnBehalfOf != "" {
		return fmt.Sprintf("%s:%s(on_behalf_of=%s)", a.Type, a.ID, a.OnBehalfOf)
	}
	return fmt.Sprintf("%s:%s", a.Type, a.ID)
}

// LogAttrs returns structured-logging key/value pairs. Personal data is
// excluded.
func (a Actor) LogAttrs() []any {
	attrs := []any{"actor_type", string(a.Type), "actor_id", a.ID}
	if a.SessionID != "" {
		attrs = append(attrs, "session_id", a.SessionID)
	}
	if a.OnBehalfOf != "" {
		attrs = append(attrs, "actor_on_behalf_of", a.OnBehalfOf)
	}
	return attrs
}

type actorContextKey struct{}

// WithActor returns a context carrying the actor, validating it first.
func WithActor(ctx context.Context, a Actor) (context.Context, error) {
	if err := a.Validate(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, actorContextKey{}, a), nil
}

// MustWithActor is WithActor for tests and fixtures.
func MustWithActor(ctx context.Context, a Actor) context.Context {
	out, err := WithActor(ctx, a)
	if err != nil {
		panic(err)
	}
	return out
}

// FromContext returns the actor in ctx, if any.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorContextKey{}).(Actor)
	return a, ok
}

// RequireFromContext returns the actor in ctx or ErrNoActor.
func RequireFromContext(ctx context.Context) (Actor, error) {
	a, ok := FromContext(ctx)
	if !ok {
		return Actor{}, ErrNoActor
	}
	return a, nil
}

// ServiceActor builds the principal for a service acting on its own behalf,
// such as a scheduled reconciliation run.
func ServiceActor(serviceName string) Actor {
	return Actor{Type: TypeService, ID: serviceName, DisplayName: serviceName}
}
