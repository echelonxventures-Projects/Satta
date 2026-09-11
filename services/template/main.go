// Command template is the reference service every backend copies (S12):
// bootstrap lifecycle, observability stack, health endpoints, and one real
// gRPC service — EvidenceService — mounted through the wire adapter built
// for conformance (S8). Serving the evidence store is deliberately the demo
// workload: it exercises the port, the adapter, RLS-bound traffic and the
// drain path, so copying this template copies a working vertical slice.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"google.golang.org/grpc"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/evidence/evidencegrpc"
	"github.com/satta/platform/services/bootstrap"
)

func main() {
	ctx := context.Background()

	cfg, err := bootstrap.ConfigFromEnv(bootstrap.Config{
		Name:     "template",
		HTTPAddr: ":8080",
		GRPCAddr: ":9090",
	})
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	store := evidence.NewPostgresStore(app.Pool(), app.Clock())

	// The version body is static after boot; precomputing it keeps the
	// handler free of error branches worth ignoring.
	versionBody, err := json.Marshal(map[string]string{"service": cfg.Name, "version": cfg.Version})
	if err != nil {
		log.Fatalf("encode version body: %v", err)
	}

	app.RegisterHTTP(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // response write failure is reported by the client, not the server
			_, _ = w.Write(versionBody)
		})
	})
	app.RegisterGRPC(func(srv *grpc.Server) {
		evidencegrpc.NewServer(store).Register(srv)
	})

	if err := app.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}
