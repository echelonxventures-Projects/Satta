package httpx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/services/httpx"
)

// steppingClock implements clock.Clock with deterministic steps, so measured
// durations in tests are exact.
type steppingClock struct {
	now   time.Time
	step  time.Duration
	reads int
}

var _ clock.Clock = (*steppingClock)(nil)

func (c *steppingClock) Now() time.Time {
	c.reads++
	// first call is t0, second t0+step (the "after" measurement)
	return c.now.Add(time.Duration(c.reads-1) * c.step)
}

func newSteppingClock() *steppingClock {
	return &steppingClock{
		now:  time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		step: 150 * time.Millisecond,
	}
}

func TestRequestLogCarriesCorrelationID(t *testing.T) {
	// The ordering bug found in the live template: RequestLog ran before
	// CorrelationID, so every line logged an empty correlation_id. The
	// production chain is Recover -> CorrelationID -> RequestLog; this test
	// pins the equivalent chain and asserts the ID lands in the log line.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	clk := newSteppingClock()

	handler := httpx.Chain(
		httpx.CorrelationID(),
		httpx.RequestLog(logger, clk),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/thing", nil)
	req.Header.Set("x-correlation-id", "corr-from-client")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("x-correlation-id"); got != "corr-from-client" {
		t.Errorf("response correlation header = %q, want the client's value echoed", got)
	}

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log was not valid JSON: %v\n%s", err, buf.String())
	}
	if line["correlation_id"] != "corr-from-client" {
		t.Fatalf("logged correlation_id = %v, want corr-from-client", line["correlation_id"])
	}
	status, okStatus := line["status"].(float64)
	if !okStatus || status != http.StatusTeapot {
		t.Errorf("logged status = %v, want 418", line["status"])
	}
	dur, okDur := line["duration_ms"].(float64)
	if !okDur || dur != 150 {
		t.Errorf("duration_ms = %v, want 150 from the stepping clock", line["duration_ms"])
	}
}

func TestCorrelationIDGeneratesWhenAbsentOrInvalid(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	for _, header := range []string{"", "has spaces", "bad;header"} {
		buf.Reset()
		handler := httpx.Chain(httpx.CorrelationID(), httpx.RequestLog(logger, newSteppingClock()))(
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("x-correlation-id", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		generated := rec.Header().Get("x-correlation-id")
		if generated == "" || generated == header && header != "" {
			t.Errorf("invalid header %q was not replaced with a fresh id (got %q)", header, generated)
		}
		if !isHex32(generated) {
			t.Errorf("generated id %q is not 32 hex chars", generated)
		}
	}
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		isHexDigit := r >= '0' && r <= '9' || r >= 'a' && r <= 'f'
		return !isHexDigit
	}) == -1
}

func TestRecoverTurnsPanicInto500AndKeepsProcessUp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := httpx.Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/boom", nil))
	}()
	<-done

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Fatalf("panic was not logged: %s", buf.String())
	}
	// The stack is logged for the on-call; the response body must not leak it.
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("panic text leaked into the response body")
	}
}

func TestStatusRecorderPassesThrough(t *testing.T) {
	handler := httpx.Recover(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestMetricsMiddlewarePassesTrafficThrough(t *testing.T) {
	// With no MeterProvider installed the OTel instruments are no-ops; what
	// this pins is that the metrics wrapper never alters the response and
	// never fails a request, whatever the meter state.
	mw, err := httpx.Metrics(newSteppingClock())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/thing", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("handler altered by metrics middleware: %d", rec.Code)
	}
}

func TestHealthCheckerReadinessAndDrain(t *testing.T) {
	c := httpx.NewChecker()
	c.Register("always-ok", func(context.Context) error { return nil })
	c.Register("broken", func(context.Context) error { return context.DeadlineExceeded })

	// A failing check must make readiness red, naming the failed check.
	rec := httptest.NewRecorder()
	c.ReadyHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "broken") {
		t.Fatalf("failed check not named in body: %s", rec.Body.String())
	}
	// "broken" is checked after "always-ok"; the first failure is reported.
	if strings.Contains(rec.Body.String(), "always-ok") {
		t.Fatal("healthy check reported as failed")
	}
}

func TestHealthCheckerAllGreenIsReady(t *testing.T) {
	c := httpx.NewChecker()
	c.Register("db", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	c.ReadyHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", rec.Code)
	}
}

func TestBeginDrainMakesReadinessFail(t *testing.T) {
	c := httpx.NewChecker()
	c.Register("db", func(context.Context) error { return nil })
	c.BeginDrain()
	rec := httptest.NewRecorder()
	c.ReadyHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "draining") {
		t.Fatalf("body = %s, want to mention draining", rec.Body.String())
	}
}

func TestLiveNeverConsultsDependencies(t *testing.T) {
	// Liveness must answer 200 with zero checks registered — a dependency
	// outage takes readiness, never liveness (no crash-stampede).
	rec := httptest.NewRecorder()
	httpx.LiveHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("live status = %d, want 200", rec.Code)
	}
}
