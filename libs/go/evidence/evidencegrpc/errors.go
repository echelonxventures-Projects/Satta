// Package evidencegrpc adapts the evidence.Store port to the EvidenceService
// gRPC contract (platform/audit/v1).
//
// DEFINITION OF DONE, GATE 2: every external dependency sits behind a port
// with contract tests. The gRPC boundary is one such dependency — this package
// is the adapter, and evidencetest.RunWireContractTests runs the same eleven
// scenarios through a real gRPC server and client so that the wire contract,
// not just the Go interface, is held to specification.
//
// Two adapters live here:
//
//   - Server wraps any evidence.Store as an EvidenceServiceServer.
//   - Client wraps any EvidenceServiceClient as an evidence.Store.
//
// Running the contract suite against Client(Server(MemoryStore)) exercises
// proto marshalling, the metadata convention, category and actor mapping,
// canonical payload bytes and error-code translation — everything a non-Go
// implementation must reproduce to conform.
package evidencegrpc

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// Metadata keys carrying the caller's tenancy scope and actor across the wire.
//
// These are the bridge until the IAM work (roadmap Task 6) replaces their
// source: today any caller can set them, which makes them identification, not
// authentication. When IAM lands, the interceptor binds them from a validated
// token instead and client-set values are ignored. The keys do not change —
// only who is allowed to set them.
//
// gRPC metadata keys are lowercased on the wire.
const (
	MetaTenantID      = "x-satta-scope-tenant"
	MetaBrandID       = "x-satta-scope-brand"
	MetaJurisdiction  = "x-satta-scope-jurisdiction"
	MetaRegion        = "x-satta-scope-region"
	MetaActorType     = "x-satta-actor-type"
	MetaActorID       = "x-satta-actor-id"
	MetaActorName     = "x-satta-actor-display-name"
	MetaActorSession  = "x-satta-actor-session-id"
	MetaActorSourceIP = "x-satta-actor-source-ip"
	MetaActorOnBehalf = "x-satta-actor-on-behalf-of"
)

// statusMapping pairs a domain sentinel with the status code the server emits
// for it and the client restores it from. The message carries the sentinel's
// own text, so the client matches on the sentinel text to recover identity
// through the code — errors.Is works across the wire exactly when the pair is
// in this table.
type statusMapping struct {
	sentinel error
	code     codes.Code
}

var mappings = []statusMapping{
	{evidence.ErrNotFound, codes.NotFound},
	{evidence.ErrInvalidRecord, codes.InvalidArgument},
	{evidence.ErrUnhashablePayload, codes.InvalidArgument},
	{evidence.ErrChainMismatch, codes.PermissionDenied},
	{evidence.ErrDuplicate, codes.AlreadyExists},
	{evidence.ErrMalformedPayload, codes.InvalidArgument},
	{tenancy.ErrNoScope, codes.PermissionDenied},
	{tenancy.ErrScopeMismatch, codes.PermissionDenied},
	{tenancy.ErrInvalidIdentifier, codes.InvalidArgument},
	{identity.ErrNoActor, codes.PermissionDenied},
}

// toStatus converts a domain error into a gRPC status. Errors wrapping a
// sentinel keep the sentinel's text in the message so the client side can
// restore the exact sentinel. Unknown errors become Internal without their
// detail — internal error strings can carry schema or driver internals that
// have no business on a wire response.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	for _, m := range mappings {
		if errors.Is(err, m.sentinel) {
			return status.Error(m.code, err.Error())
		}
	}
	return status.Error(codes.Internal, "evidence: internal error")
}

// fromStatus converts a wire error back into a domain error. A status whose
// code matches a mapping AND whose message contains the sentinel's text
// restores that sentinel (wrapped, so errors.Is still works); a code match
// with different text is returned as a plain status error — the same code can
// carry multiple causes and guessing would misreport.
func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	for _, m := range mappings {
		if st.Code() == m.code && contains(st.Message(), m.sentinel.Error()) {
			return fmt.Errorf("%w: wire: %s", m.sentinel, st.Message())
		}
	}
	return err
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
