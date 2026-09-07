// Package db provides PostgreSQL access with tenant isolation enforced at the
// database layer.
//
// The isolation model is two-layered on purpose. Application code carries a
// tenancy scope and filters by it, and separately every tenant-scoped table has a
// row-level security policy that reads a per-transaction session variable set
// here. The database layer is the one that still holds when a new query forgets
// its WHERE clause, which is the failure mode that actually occurs.
//
// Consequently there is no way to obtain a connection through this package
// without declaring a tenancy scope. That is deliberate friction: a "just this
// once" unscoped query is how cross-tenant leaks are introduced.
package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/satta/platform/libs/go/tenancy"
)

// SessionVarTenant is the PostgreSQL session variable that row-level security
// policies read. It is set per transaction, never per connection: connections are
// pooled and reused across tenants, so a connection-level setting would leak
// between requests.
const SessionVarTenant = "app.tenant_id"

// SessionVarBrand carries the brand for policies that discriminate below tenant
// level.
const SessionVarBrand = "app.brand_id"

// ErrNoDSN is returned when no database URL is configured.
var ErrNoDSN = errors.New("db: no database URL configured")

// Config describes a connection pool.
type Config struct {
	// DSN is the PostgreSQL connection URL.
	DSN string
	// MaxConns bounds the pool. Left at zero, pgx's default applies.
	MaxConns int32
	// MinConns keeps a floor of warm connections.
	MinConns int32
	// MaxConnLifetime recycles connections so that a long-lived process does not
	// pin a connection across a database upgrade or a credential rotation.
	MaxConnLifetime time.Duration
	// MaxConnIdleTime closes idle connections.
	MaxConnIdleTime time.Duration
	// ConnectTimeout bounds the initial connection attempt.
	ConnectTimeout time.Duration
	// ApplicationName appears in pg_stat_activity, which is what makes a runaway
	// query attributable to a service during an incident.
	ApplicationName string
}

// ConfigFromEnv reads pool configuration from the environment. Infrastructure
// wiring — hostnames, pool sizes, timeouts — is environment configuration rather
// than business policy, so it is read here rather than from the rules engine.
func ConfigFromEnv(serviceName string) (Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return Config{}, fmt.Errorf("%w: set DATABASE_URL", ErrNoDSN)
	}
	cfg := Config{
		DSN:             dsn,
		MaxConns:        int32(envInt("DATABASE_MAX_CONNS", 10)),
		MinConns:        int32(envInt("DATABASE_MIN_CONNS", 1)),
		MaxConnLifetime: envDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
		MaxConnIdleTime: envDuration("DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		ConnectTimeout:  envDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
		ApplicationName: serviceName,
	}
	return cfg, nil
}

func envInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
	}
	return fallback
}

// Pool is a PostgreSQL connection pool.
type Pool struct {
	pool *pgxpool.Pool
}

// Open creates and verifies a connection pool.
func Open(ctx context.Context, cfg Config) (*Pool, error) {
	if cfg.DSN == "" {
		return nil, ErrNoDSN
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse DSN: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	if cfg.ApplicationName != "" {
		if poolCfg.ConnConfig.RuntimeParams == nil {
			poolCfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		poolCfg.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &Pool{pool: pool}, nil
}

// Close releases the pool.
func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

// Ping checks liveness. Used by the readiness probe.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return errors.New("db: pool is not open")
	}
	return p.pool.Ping(ctx)
}

// Raw exposes the underlying pool. It exists for migrations and for
// administrative tooling that legitimately operates outside tenant scope; it is
// not for request handling.
func (p *Pool) Raw() *pgxpool.Pool { return p.pool }

// InTenantTx runs fn inside a transaction with the tenancy session variables set
// from the scope in ctx, so that row-level security applies.
//
// The scope is read from context rather than accepted as an argument. An argument
// can be passed a different tenant than the request authenticated as, and that
// mistake is invisible at the call site; reading from context means the isolation
// boundary is the same object that authentication established.
func (p *Pool) InTenantTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return err
	}
	return p.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// InTx runs fn in a transaction without setting tenancy variables. Reserved for
// migrations, platform-global tables and cross-tenant administrative work; any
// use of it on a tenant-scoped table is a bug.
func (p *Pool) InTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return p.inTx(ctx, fn)
}

func (p *Pool) inTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if p == nil || p.pool == nil {
		return errors.New("db: pool is not open")
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op, so this is safe as an
		// unconditional cleanup and removes the need for a rollback call on every
		// error path.
		//nolint:errcheck // documented no-op cleanup; commit errors surface above
		_ = tx.Rollback(ctx)
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// applyScope sets the transaction-local session variables that RLS policies read.
//
// set_config with is_local=true is used rather than SET LOCAL because SET does not
// accept bind parameters, and building the statement by string concatenation would
// put a caller-influenced value into SQL text.
func applyScope(ctx context.Context, tx pgx.Tx, scope tenancy.Scope) error {
	if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, SessionVarTenant, scope.TenantID); err != nil {
		return fmt.Errorf("db: set %s: %w", SessionVarTenant, err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, SessionVarBrand, scope.BrandID); err != nil {
		return fmt.Errorf("db: set %s: %w", SessionVarBrand, err)
	}
	return nil
}
