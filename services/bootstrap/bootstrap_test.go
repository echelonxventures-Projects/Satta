package bootstrap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/satta/platform/services/bootstrap"
)

func TestConfigFromEnvRequiresName(t *testing.T) {
	t.Setenv("SATTA_SERVICE_NAME", "")
	if _, err := bootstrap.ConfigFromEnv(bootstrap.Config{}); err == nil {
		t.Fatal("missing name: ConfigFromEnv accepted an anonymous service")
	} else if !strings.Contains(err.Error(), "SATTA_SERVICE_NAME") {
		t.Fatalf("error %q must name the missing variable", err)
	}
}

func TestConfigFromEnvReadsAndDefaults(t *testing.T) {
	t.Setenv("SATTA_SERVICE_NAME", "sportsbook")
	t.Setenv("SATTA_SERVICE_VERSION", "v1.2.3")
	t.Setenv("SATTA_HTTP_ADDR", "127.0.0.1:18080")
	t.Setenv("SATTA_GRPC_ADDR", "127.0.0.1:19090")
	t.Setenv("DATABASE_URL", "postgres://satta_app:satta-app-dev@127.0.0.1:55432/satta?sslmode=disable")

	cfg, err := bootstrap.ConfigFromEnv(bootstrap.Config{})
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Name != "sportsbook" || cfg.Version != "v1.2.3" {
		t.Errorf("identity = %q/%q", cfg.Name, cfg.Version)
	}
	if cfg.HTTPAddr != "127.0.0.1:18080" || cfg.GRPCAddr != "127.0.0.1:19090" {
		t.Errorf("addrs = %q/%q", cfg.HTTPAddr, cfg.GRPCAddr)
	}
	if cfg.DrainGrace != 5*time.Second {
		t.Errorf("DrainGrace = %v, want the 5s default", cfg.DrainGrace)
	}
}

func TestRunRefusesWithoutHTTPHandlers(t *testing.T) {
	t.Setenv("SATTA_SERVICE_NAME", "unconfigured")
	// `task test` exports DATABASE_URL into the environment; these tests are
	// about lifecycle wiring, not a live database, so the DSN is pinned empty.
	t.Setenv("DATABASE_URL", "")
	cfg, err := bootstrap.ConfigFromEnv(bootstrap.Config{})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	app, err := bootstrap.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RegisterHTTP") {
		t.Fatalf("Run without RegisterHTTP = %v, want guidance naming RegisterHTTP", err)
	}
}

func TestConfigFromEnvParsesDrainSeconds(t *testing.T) {
	t.Setenv("SATTA_SERVICE_NAME", "drain-probe")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SATTA_DRAIN_SECONDS", "20")
	cfg, err := bootstrap.ConfigFromEnv(bootstrap.Config{})
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.DrainGrace != 20*time.Second {
		t.Fatalf("DrainGrace = %v, want 20s", cfg.DrainGrace)
	}

	t.Setenv("SATTA_DRAIN_SECONDS", "yesterday")
	if _, err := bootstrap.ConfigFromEnv(bootstrap.Config{}); err == nil {
		t.Fatal("garbage SATTA_DRAIN_SECONDS accepted — a mis-set drain budget fails silently at deploy time, not boot")
	}
}

func TestPoolNilWithoutDatabaseDSN(t *testing.T) {
	t.Setenv("SATTA_SERVICE_NAME", "dbless")
	t.Setenv("DATABASE_URL", "")
	cfg, err := bootstrap.ConfigFromEnv(bootstrap.Config{})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	app, err := bootstrap.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app.Pool() != nil {
		t.Fatal("Pool() non-nil without a DSN")
	}
	if app.Clock() == nil {
		t.Fatal("Clock() must always provide the injected time authority")
	}
}
