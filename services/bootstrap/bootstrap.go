// Package bootstrap is the service lifecycle every backend embeds:
// configuration, observability, HTTP + gRPC listeners, health registration,
// and graceful shutdown with a drain phase.
//
// The shutdown sequence is deliberate and ordered:
//
//  1. mark unready  — readiness flips first so the load balancer stops
//     sending new work;
//  2. drain          — a short grace for registration propagation;
//  3. stop serving   — gRPC GracefulStop (in-flight RPCs finish), HTTP
//     Shutdown (stops accepting, waits for handlers);
//  4. close pools    — database and other infra last, only once no handler
//     can still be using them;
//  5. flush          — telemetry shutdown prints what it could not export.
//
// Skipping or reordering any step is how a deploy loses a request that had
// already been billed.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/services/httpx"
	"github.com/satta/platform/services/observability"
)

// Config is the service's runtime configuration, all of it environment:
// stage-0 runs with no config file and no cloud account (ADR-0002). Policy
// values (limits, rates, retention) resolve through the config engine, not
// here — this is infrastructure wiring only (DoD gate 1 distinction).
type Config struct {
	Name        string
	Version     string
	HTTPAddr    string        // e.g. :8080
	GRPCAddr    string        // e.g. :9090
	DatabaseDSN string        // service DATABASE_URL
	DrainGrace  time.Duration // LB propagation grace, default 5s
}

// ConfigFromEnv builds Config from the standard environment. The service
// name is mandatory: an anonymous service cannot be traced, metered or
// paged about.
func ConfigFromEnv(defaults Config) (Config, error) {
	cfg := defaults
	if v := os.Getenv("SATTA_SERVICE_NAME"); v != "" {
		cfg.Name = v
	}
	if v := os.Getenv("SATTA_SERVICE_VERSION"); v != "" {
		cfg.Version = v
	}
	if cfg.Name == "" {
		return Config{}, errors.New("bootstrap: SATTA_SERVICE_NAME is required")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if v := os.Getenv("SATTA_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("SATTA_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseDSN = v
	}
	if v := os.Getenv("SATTA_DRAIN_SECONDS"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 0 {
			return Config{}, fmt.Errorf("bootstrap: SATTA_DRAIN_SECONDS %q is not a non-negative integer", v)
		}
		cfg.DrainGrace = time.Duration(secs) * time.Second
	}
	if cfg.DrainGrace <= 0 {
		cfg.DrainGrace = 5 * time.Second
	}
	return cfg, nil
}

// App is a running service: observability, health, pools and servers, with
// one lifecycle method.
type App struct {
	cfg   Config
	obs   *observability.Instance
	check *httpx.Checker
	pool  *db.Pool
	clock clock.Clock

	httpSrv *http.Server
	grpcSrv *grpc.Server
	httpLn  net.Listener
	grpcLn  net.Listener
	regErr  error
}

// New prepares the app: telemetry up, pool open (with a readiness check
// registered against it), servers not yet listening. Build handlers and
// call RegisterHTTP / RegisterGRPC before Run.
func New(ctx context.Context, cfg Config) (*App, error) {
	obs, err := observability.New(ctx, observability.Service{Name: cfg.Name, Version: cfg.Version})
	if err != nil {
		return nil, err
	}
	app := &App{
		cfg:   cfg,
		obs:   obs,
		check: httpx.NewChecker(),
		clock: clock.NewSystem(),
	}

	if cfg.DatabaseDSN != "" {
		pool, err := db.Open(ctx, db.Config{DSN: cfg.DatabaseDSN})
		if err != nil {
			obs.Logger.Error("database pool open failed", "error", err)
			if serr := obs.Shutdown(context.WithoutCancel(ctx)); serr != nil {
				obs.Logger.Error("telemetry shutdown after failed boot", "error", serr)
			}
			return nil, err
		}
		app.pool = pool
		app.check.Register("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		})
	}
	return app, nil
}

// Pool exposes the database pool to the service's own constructors. It is
// nil for services configured without a database.
func (a *App) Pool() *db.Pool { return a.pool }

// Clock is the service's injected time authority (SAFETY INVARIANT 12).
func (a *App) Clock() clock.Clock { return a.clock }

// Logger is the service logger.
func (a *App) Logger() *slog.Logger { return a.obs.Logger }

// Checker lets services register additional readiness probes.
func (a *App) Checker() *httpx.Checker { return a.check }

// RegisterHTTP installs the service's business routes on the shared,
// middleware-wrapped mux. Health endpoints are added here and must not be
// replaced by services: they are the contract the load balancer reads.
//
// The Metrics middleware can fail at construction (a broken instrument is a
// bug worth surfacing); RegisterHTTP records that as a sticky error which
// Run returns before serving anything.
func (a *App) RegisterHTTP(business func(mux *http.ServeMux)) {
	if a.regErr != nil {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpx.LiveHandler())
	mux.Handle("GET /readyz", a.check.ReadyHandler())
	mux.Handle("GET /metrics", a.obs.MetricsHandler())
	if business != nil {
		business(mux)
	}

	metricsMW, err := httpx.Metrics(a.clock)
	if err != nil {
		a.regErr = err
		return
	}
	handler := httpx.Chain(
		httpx.Recover(a.obs.Logger),
		// Correlation must be established before the request log reads it;
		// the live template logs proved the wrong order silently emptied the
		// correlation_id field on every line.
		httpx.CorrelationID(),
		httpx.RequestLog(a.obs.Logger, a.clock),
		metricsMW,
	)(httpx.NewMux(mux, a.cfg.Name))

	a.httpSrv = &http.Server{
		Handler: handler,
		// Slowloris protection: headers must arrive promptly; bodies may
		// stream as long as the handler's own deadlines allow.
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// RegisterGRPC builds the gRPC server with the shared interceptors and hands
// it to the service for registering its own services, then starts it in Run.
func (a *App) RegisterGRPC(register func(srv *grpc.Server)) {
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(recoverUnary(a.obs.Logger)),
		grpc.ChainStreamInterceptor(recoverStream(a.obs.Logger)),
	)
	register(srv)
	a.grpcSrv = srv
}

// Run starts both listeners and blocks until an interrupt/TERM signal or a
// fatal listen error, then executes the ordered drain. It returns once the
// process is finished shutting down.
func (a *App) Run(ctx context.Context) error {
	if a.regErr != nil {
		return a.regErr
	}
	if err := a.listen(ctx); err != nil {
		return err
	}
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	errCh := make(chan error, 2)
	go a.serveHTTP(errCh)
	go a.serveGRPC(errCh)

	a.obs.Logger.Info("service started",
		"http_addr", a.cfg.HTTPAddr, "grpc_addr", a.cfg.GRPCAddr)

	select {
	case <-sigCtx.Done():
		a.obs.Logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			a.obs.Logger.Error("listener failed", "error", err)
			// The listener already failed; drain anyway so pools close and
			// telemetry flushes, then report whichever error matters more:
			// the original listener failure.
			_ = a.shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // shutdown failure must not mask the listener error below
			return err
		}
	}
	return a.shutdown(context.WithoutCancel(ctx))
}

func (a *App) listen(ctx context.Context) error {
	if a.httpSrv == nil {
		return errors.New("bootstrap: RegisterHTTP must be called before Run")
	}
	lc := &net.ListenConfig{}
	httpLn, err := lc.Listen(ctx, "tcp", a.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen http %s: %w", a.cfg.HTTPAddr, err)
	}
	a.httpLn = httpLn

	if a.grpcSrv != nil {
		grpcLn, err := lc.Listen(ctx, "tcp", a.cfg.GRPCAddr)
		if err != nil {
			_ = httpLn.Close()
			return fmt.Errorf("listen grpc %s: %w", a.cfg.GRPCAddr, err)
		}
		a.grpcLn = grpcLn
	}
	return nil
}

func (a *App) serveHTTP(errCh chan<- error) {
	if err := a.httpSrv.Serve(a.httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}

func (a *App) serveGRPC(errCh chan<- error) {
	if a.grpcSrv == nil {
		return
	}
	if err := a.grpcSrv.Serve(a.grpcLn); err != nil {
		errCh <- err
		return
	}
	errCh <- nil
}

// shutdown runs the ordered drain. Each phase shares one overall budget; the
// cap exists so a wedged dependency cannot hang the pod forever — Kubernetes
// kills it at terminationGracePeriodSeconds regardless, and a logged reason
// beats an unexplained SIGKILL.
func (a *App) shutdown(base context.Context) error {
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()

	// 1-2: unready + drain.
	a.check.BeginDrain()
	select {
	case <-ctx.Done():
	case <-time.After(a.cfg.DrainGrace):
	}

	// 3: stop accepting, finish in flight.
	if a.grpcSrv != nil {
		finished := make(chan struct{})
		go func() { a.grpcSrv.GracefulStop(); close(finished) }()
		select {
		case <-finished:
		case <-ctx.Done():
			a.grpcSrv.Stop()
		}
	}
	if err := a.httpSrv.Shutdown(ctx); err != nil {
		a.obs.Logger.Error("http shutdown", "error", err)
	}

	// 4: close pools.
	if a.pool != nil {
		a.pool.Close()
	}

	// 5: flush telemetry.
	return a.obs.Shutdown(ctx)
}

// recoverUnary converts a handler panic into an Internal status instead of
// a crash loop; the process survives, the caller gets a defined code.
func recoverUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.ErrorContext(ctx, "grpc panic recovered",
					"method", info.FullMethod, "panic", rec, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// recoverStream is the streaming equivalent.
func recoverStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.ErrorContext(ss.Context(), "grpc stream panic recovered",
					"method", info.FullMethod, "panic", rec, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}
