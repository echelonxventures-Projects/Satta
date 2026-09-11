// Package observability wires the three signals every service must emit
// (Definition of Done gate 4): structured logs, Prometheus metrics,
// OpenTelemetry traces.
//
// The contract is:
//
//   - Logs go to stdout as structured JSON. The substrate ships logs from
//     stdout to Loki; services never talk to a log backend directly.
//   - Traces go to the OTLP collector (Tempo in the local substrate),
//     addressed by OTEL_EXPORTER_OTLP_ENDPOINT, with context propagation
//     via traceparent on HTTP and gRPC metadata.
//   - Metrics are served by the service itself on /metrics in Prometheus
//     text format; the scraper pulls, it is never pushed.
//
// A service that cannot reach its trace endpoint must still serve traffic:
// export failures are logged and dropped, never returned to a caller.
// Observability is a diagnostic plane, not a data plane dependency. The one
// exception is a malformed endpoint: that is a deployment defect, and a
// service that silently stops tracing in production is worse than one that
// refuses to start with a clear error.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

// Service describes what a service announces about itself everywhere: in
// logs, in trace resource attributes, in metric identity.
type Service struct {
	Name    string
	Version string
}

// Instance bundles the three signals plus the lifecycle they require.
// Shutdown flushes pending telemetry and must be deferred by every main.
type Instance struct {
	Logger *slog.Logger
	// MetricsHandler is the /metrics handler; httpx mounts it.
	instance *providerSet
}

// New builds the observability stack for a service.
func New(ctx context.Context, svc Service) (*Instance, error) {
	if svc.Name == "" {
		return nil, errors.New("observability: service name is required")
	}
	logger := NewLogger(svc)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		// Telemetry must never fail a request; it is loud enough once.
		logger.Warn("otel export failure (service unaffected)", "error", err)
	}))

	providers, err := newProviderSet(ctx, svc, logger)
	if err != nil {
		return nil, err
	}

	// Correlation propagates through trace context; the platform's own
	// long-lived correlation id rides alongside it (libs/go/correlation).
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Instance{Logger: logger, instance: providers}, nil
}

// Shutdown flushes and stops the telemetry providers. Errors are returned
// because the process is exiting and a lost span or metric batch is worth
// printing.
func (i *Instance) Shutdown(ctx context.Context) error {
	return i.instance.shutdown(ctx)
}

// NewLogger returns the JSON logger every service shares. The default slog
// handler stamps local time; an explicit ReplaceAttr forces UTC so the log
// line cannot carry the machine's timezone (SAFETY INVARIANT 12's
// presentation — the domain still may not read the wall clock itself).
func NewLogger(svc Service) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: LevelFromEnv(),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 && a.Value.Kind() == slog.KindTime {
				a.Value = slog.AnyValue(a.Value.Time().UTC())
			}
			return a
		},
	})
	return slog.New(handler).With("service", svc.Name, "version", svc.Version)
}

// LevelFromEnv reads SATTA_LOG_LEVEL (debug, info, warn, error; default info).
func LevelFromEnv() slog.Level {
	switch strings.ToLower(os.Getenv("SATTA_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func resourceFor(svc Service) *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(svc.Name),
		semconv.ServiceVersion(svc.Version),
	)
}

// joinErrors keeps shutdown reporting honest without extra dependencies.
func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%v", errs)
	}
}
