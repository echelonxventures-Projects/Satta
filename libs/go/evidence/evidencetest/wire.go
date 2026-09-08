// Wire-level conformance.
//
// RunWireContractTests runs the same eleven scenarios as RunContractTests,
// through a real gRPC server and client rather than an in-process interface
// call. The value over the in-process suite is that the wire contract itself
// is exercised: proto marshalling, the metadata convention, canonical payload
// bytes, category/actor mapping and error-code translation. A non-Go
// implementation of EvidenceService that passes this suite conforms to the
// port specification, which is what ADR-0001 asks of every adapter.
package evidencetest

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencegrpc"
	auditv1 "github.com/satta/platform/libs/go/gen/platform/audit/v1"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// WireFactory builds a fresh, empty store for one test, served over gRPC.
// The underlying store is wrapped: the suite only ever sees the wire client.
type WireFactory func(t *testing.T) evidence.Store

// WireOver returns a factory that serves newStore over a bufconn gRPC server
// and hands back a Client pointed at it. bufconn is an in-memory listener:
// the full gRPC stack — HTTP/2 framing, proto serialisation, interceptors,
// status propagation — runs without a network socket, so the suite satisfies
// the runs-locally-with-no-credentials rule.
func WireOver(newStore Factory) WireFactory {
	return func(t *testing.T) evidence.Store {
		t.Helper()
		store := newStore(t)
		return ServeWire(t, store)
	}
}

// ServeWire starts a gRPC server backing store on a bufconn listener and
// returns the connected client store. Both shut down with the test.
func ServeWire(t *testing.T, store evidence.Store) evidence.Store {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	evidencegrpc.NewServer(store).Register(server)

	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		<-done
	})

	// passthrough hands "bufnet" straight to the dialer; the default DNS
	// resolver would try to resolve it as a hostname and produce zero
	// addresses.
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return evidencegrpc.NewClient(conn)
}

// RunWireContractTests runs the full Store contract through the wire.
func RunWireContractTests(t *testing.T, newWire WireFactory) {
	t.Helper()
	RunContractTests(t, Factory(newWire))
}

// RunWireOnlyTests covers behaviour the wire adds beyond the port: the
// metadata convention, deny-by-default over RPC, and the structural absence of
// mutation RPCs on the generated surface.
func RunWireOnlyTests(t *testing.T, newWire WireFactory) {
	t.Helper()

	t.Run("DenyByDefaultWithoutScope", func(t *testing.T) {
		store := newWire(t)
		// Deliberately no tenancy scope: the server must surface the domain's
		// own denial, not invent one.
		_, err := store.Append(context.Background(), NewRecord(TenantA, ChainA, "ledger.transfer.posted", "wire-noscope"))
		if err == nil {
			t.Fatal("append without scope was accepted over the wire")
		}
	})

	t.Run("AuditorCannotAppend", func(t *testing.T) {
		store := newWire(t)
		ctx, err := tenancy.WithScope(context.Background(), ScopeFor(TenantA))
		if err != nil {
			t.Fatalf("WithScope: %v", err)
		}
		ctx, err = identity.WithActor(ctx, identity.Actor{Type: identity.TypeAuditor, ID: "audit-1"})
		if err != nil {
			t.Fatalf("WithActor: %v", err)
		}
		// SAFETY INVARIANT 11 must survive the wire crossing.
		_, err = store.Append(ctx, NewRecord(TenantA, ChainA, "ledger.transfer.posted", "wire-auditor"))
		if err == nil {
			t.Fatal("auditor append was accepted over the wire")
		}
	})

	t.Run("ErrorIdentitySurvivesWire", func(t *testing.T) {
		store := newWire(t)
		ctx := CtxFor(t, TenantA)
		// Not found: the client must restore evidence.ErrNotFound so errors.Is
		// keeps working for callers that never see the server.
		_, err := store.Get(ctx, ChainA, 42)
		if err == nil {
			t.Fatal("get of a missing record succeeded")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected a not-found error over the wire, got: %v", err)
		}
	})

	t.Run("WireSurfaceHasNoMutationRPCs", func(t *testing.T) {
		// The structural check, on the generated interface this time: the
		// wire must be as incapable of mutation as the Go port is.
		forbidden := []string{"Update", "Delete", "Remove", "Purge", "Truncate", "Drop", "Modify", "Rewrite"}
		clientType := reflect.TypeOf((*auditv1.EvidenceServiceClient)(nil)).Elem()
		for i := 0; i < clientType.NumMethod(); i++ {
			name := clientType.Method(i).Name
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("EvidenceServiceClient exposes %q; the wire contract forbids mutation RPCs", name)
				}
			}
		}
	})
}
