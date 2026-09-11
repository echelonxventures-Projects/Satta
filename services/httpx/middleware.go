// Package httpx is the HTTP surface every service shares: the middleware
// chain, health and readiness endpoints, and graceful drain. It exists so
// "no service ships without observability" is a property of the template
// rather than of each author's memory.
package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/correlation"
)

// Middleware wraps one handler into another.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares outermost-first: Chain(a, b)(h) runs a, then b,
// then h.
func Chain(mws ...Middleware) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
}

// HeaderCorrelationID is the wire header carrying the long-lived correlation
// id; it matches libs/go/correlation.HeaderName, asserted by test.
const HeaderCorrelationID = correlation.HeaderName

// CorrelationID attaches the request's correlation id — taken from the
// x-correlation-id header or generated — to the request context. It runs
// before logging so every log line and every downstream call carries it.
//
// The header is identification, not authentication; IAM (roadmap Task 6)
// will stamp trusted values at the gateway and strip client-supplied ones.
func CorrelationID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(HeaderCorrelationID))
			if !validCorrelationID(id) {
				id = correlation.New()
			}
			ctx := correlation.WithID(r.Context(), id)
			w.Header().Set(HeaderCorrelationID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func validCorrelationID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		isAlphaNum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !isAlphaNum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// RequestLog logs one structured line per request with latency, status and
// correlation id. Status comes from a wrapping ResponseWriter because
// handlers write directly. Latency is measured against the injected clock
// (SAFETY INVARIANT 12): with a Fixed clock in tests, request timing is
// deterministic rather than flaky.
func RequestLog(logger *slog.Logger, clk clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			ctx := r.Context()
			corrID, _ := correlation.FromContext(ctx)
			logger.InfoContext(ctx, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", clk.Now().Sub(start).Milliseconds(),
				"correlation_id", corrID,
				"remote", r.RemoteAddr,
			)
		})
	}
}

// Recover turns a handler panic into a 500, logs the stack, and keeps the
// process alive. A panic in a request path is a bug; killing every in-flight
// request because of it is a bigger one.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			defer func() {
				if rec := recover(); rec != nil {
					logger.ErrorContext(ctx, "panic recovered",
						"panic", rec,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status code written by downstream handlers.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Metrics records request count (by status class), in-flight gauge and
// latency histogram. Cardinality is bounded on purpose: status class, not
// status code; no raw paths as labels. Instrument construction errors are
// returned rather than swallowed so a misconfigured meter fails the service
// at boot, not silently in production.
func Metrics(clk clock.Clock) (Middleware, error) {
	meter := otel.Meter("satta.http")
	requests, err := meter.Int64Counter("http.server.requests",
		metric.WithDescription("Requests by status class"))
	if err != nil {
		return nil, fmt.Errorf("httpx: instrument requests: %w", err)
	}
	inflight, err := meter.Int64UpDownCounter("http.server.inflight",
		metric.WithDescription("Requests currently being served"))
	if err != nil {
		return nil, fmt.Errorf("httpx: instrument inflight: %w", err)
	}
	duration, err := meter.Float64Histogram("http.server.request.duration",
		metric.WithDescription("Request latency"), metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("httpx: instrument duration: %w", err)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			start := clk.Now()
			inflight.Add(ctx, 1)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				inflight.Add(ctx, -1)
				duration.Record(ctx, clk.Now().Sub(start).Seconds())
				requests.Add(ctx, 1, metric.WithAttributes(attribute.String("status_class", statusClass(rec.status))))
			}()
			next.ServeHTTP(rec, r)
		})
	}, nil
}

func statusClass(code int) string {
	switch {
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// NewMux wraps otelhttp around the handler so every request gets a server
// span with the given operation name.
func NewMux(handler http.Handler, operation string) http.Handler {
	return otelhttp.NewHandler(handler, operation,
		otelhttp.WithFilter(func(req *http.Request) bool {
			// Health checks and scrapes are frequent and meaningless as
			// traces; they flood the collector and skew latency histograms.
			return !strings.HasPrefix(req.URL.Path, "/healthz") &&
				!strings.HasPrefix(req.URL.Path, "/readyz") &&
				req.URL.Path != "/metrics"
		}),
	)
}
