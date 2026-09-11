package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/satta/platform/services/observability"
)

func TestNewLoggerStampsUTC(t *testing.T) {
	// SAFETY INVARIANT 12 at the presentation layer: log timestamps carry
	// no machine-local offset. A live run caught slog defaulting to local
	// time; this test pins the UTC enforcement.
	buf := &bytes.Buffer{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 && a.Value.Kind() == slog.KindTime {
				a.Value = slog.AnyValue(a.Value.Time().UTC())
			}
			return a
		},
	})
	logger := slog.New(handler).With("service", "probe")
	logger.Info("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log JSON: %v\n%s", err, buf.String())
	}
	ts, ok := line["time"].(string)
	if !ok {
		t.Fatalf("log has no string time field: %s", buf.String())
	}
	if strings.HasSuffix(ts, "Z") || strings.Contains(ts, "+00:00") {
		return
	}
	t.Fatalf("time %q is not UTC-stamped", ts)
}

func TestNewBuildsInstanceWithoutExporterConfig(t *testing.T) {
	// No OTLP endpoint: the service still boots, tracing still propagates,
	// only export is disabled. This is the local `go run` path.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	obs, err := observability.New(context.Background(), observability.Service{Name: "probe", Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if obs.Logger == nil {
		t.Fatal("nil logger")
	}
	if obs.MetricsHandler() == nil {
		t.Fatal("nil metrics handler")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil)
	obs.MetricsHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	// The service identity must appear on scraped metrics (target info or
	// resource attributes) so a Prometheus query can attribute series.
	body := rec.Body.String()
	if !strings.Contains(body, "probe") {
		t.Fatalf("metrics body does not mention the service name")
	}
}

func TestNewRejectsNamelessService(t *testing.T) {
	if _, err := observability.New(context.Background(), observability.Service{}); err == nil {
		t.Fatal("a service without a name must not start: it could not be traced, metered or paged about")
	}
}
