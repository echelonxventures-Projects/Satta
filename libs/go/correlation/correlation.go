// Package correlation propagates a request-scoped correlation identifier.
//
// A correlation ID differs from a trace ID: traces are sampled and have a
// retention window measured in days, whereas a correlation ID is written into
// audit records, usage events and outbound provider calls, and must survive for
// as long as those do. Reconciling an invoice line against the bet that caused
// it, years later, depends on this identifier existing independently of the
// tracing backend.
package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// HeaderName is the wire header carrying the correlation ID across process
// boundaries, in both HTTP and gRPC metadata.
const HeaderName = "x-correlation-id"

type contextKey struct{}

// New generates a fresh correlation ID. It uses crypto/rand rather than a
// counter or timestamp so that IDs from different regions and processes never
// collide, and so that they leak no information about volume or ordering.
func New() string {
	buf := make([]byte, 16)
	// crypto/rand.Read is documented never to return an error on supported
	// platforms; it panics internally on failure. No error branch is needed.
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// WithID returns a context carrying id. An empty id is replaced with a fresh
// one, because a missing correlation ID downstream is worse than an
// unrecognised one.
func WithID(ctx context.Context, id string) context.Context {
	if !valid(id) {
		id = New()
	}
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the correlation ID in ctx, if any.
func FromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	return id, ok && id != ""
}

// Ensure returns ctx with a correlation ID guaranteed to be present, along with
// the ID. Entry points call this once.
func Ensure(ctx context.Context) (context.Context, string) {
	if id, ok := FromContext(ctx); ok {
		return ctx, id
	}
	id := New()
	return context.WithValue(ctx, contextKey{}, id), id
}

// valid bounds what will be accepted from an untrusted caller. Correlation IDs
// arrive over the network and end up in log lines and database columns, so
// length and character set are constrained.
func valid(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
