package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexport "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// providerSet owns the tracer and meter providers plus the HTTP handler
// that exposes metrics in Prometheus text format.
type providerSet struct {
	tracer         *sdktrace.TracerProvider
	meter          *sdkmetric.MeterProvider
	metricsHandler http.Handler
}

func newProviderSet(ctx context.Context, svc Service, logger *slog.Logger) (*providerSet, error) {
	set := &providerSet{}

	tracer, err := initTracing(ctx, svc, logger)
	if err != nil {
		return nil, err
	}
	set.tracer = tracer

	meter, handler, err := initMetrics(svc)
	if err != nil {
		return nil, err
	}
	set.meter = meter
	set.metricsHandler = handler

	return set, nil
}

// initTracing builds the tracer provider. When OTEL_EXPORTER_OTLP_ENDPOINT
// is unset (plain go run, unit tests), spans are still created and
// propagated — the sampling and context contract stays exercised — but
// nothing is exported.
func initTracing(ctx context.Context, svc Service, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resourceFor(svc)),
	}
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		exporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(), // loopback substrate; TLS is a deployment concern
		)
		if err != nil {
			// A configured-but-unbuildable exporter is a deployment defect:
			// refuse to start rather than run without tracing.
			return nil, fmt.Errorf("traces exporter for %s: %w", endpoint, err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	} else {
		logger.Warn("OTEL_EXPORTER_OTLP_ENDPOINT unset; spans will not be exported")
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// initMetrics builds the meter provider on a dedicated Prometheus registry.
// The registry (not the default one) is exposed so a service's /metrics
// output is exactly what this SDK produced: no stdlib collector noise, no
// collisions between services' metrics.
func initMetrics(svc Service) (*sdkmetric.MeterProvider, http.Handler, error) {
	registry := prometheus.NewRegistry()
	exporter, err := promexport.New(promexport.WithRegisterer(registry))
	if err != nil {
		return nil, nil, fmt.Errorf("prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(resourceFor(svc)),
	)
	otel.SetMeterProvider(mp)
	return mp, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
}

// MetricsHandler serves the process metrics in Prometheus text format.
func (i *Instance) MetricsHandler() http.Handler { return i.instance.metricsHandler }

func (p *providerSet) shutdown(ctx context.Context) error {
	var errs []error
	if p.tracer != nil {
		if err := p.tracer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer shutdown: %w", err))
		}
	}
	if p.meter != nil {
		if err := p.meter.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter shutdown: %w", err))
		}
	}
	return joinErrors(errs)
}
