// Command platformctl is the operator CLI for the platform's own schema and evidence.
//
// It exists so that the operations a developer or an operator has to perform against the
// database are the same operations CI performs, expressed once. A migration applied by hand
// in one environment and by a script in another is how two environments end up with
// different schemas and nobody knows which is right.
//
// Subcommands:
//
//	migrate          apply owned schema migrations, then apply mandatory grants
//	seed             load local development tenants and policy configuration
//	verify-evidence  recompute every evidence hash chain and report integrity
//
// verify-evidence is not a development convenience. SAFETY INVARIANT 4 makes the platform
// tamper-evident rather than tamper-proof, which is only a meaningful property if something
// actually performs the verification. This is that something, and it is intended to run on a
// schedule as well as on demand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
)

const usage = `platformctl — platform schema and evidence operations

Usage:
  platformctl <command> [flags]

Commands:
  migrate           Apply owned schema migrations and mandatory grants
  seed              Load local development tenants and policy configuration
  verify-evidence   Recompute evidence hash chains and report integrity

Run 'platformctl <command> -h' for command flags.

Every command needs a database URL, from --database-url or DATABASE_URL.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Signal handling at the top level so that a long migration or a verification sweep over
	// a large chain stops cleanly rather than leaving a transaction to time out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "migrate":
		err = runMigrate(ctx, os.Args[2:])
	case "seed":
		err = runSeed(ctx, os.Args[2:])
	case "verify-evidence":
		err = runVerifyEvidence(ctx, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "platformctl: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "platformctl: cancelled")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "platformctl: %v\n", err)
		os.Exit(1)
	}
}

// commonFlags holds what every subcommand needs.
type commonFlags struct {
	databaseURL string
	timeout     time.Duration
}

func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.databaseURL, "database-url", os.Getenv("DATABASE_URL"),
		"PostgreSQL connection URL (defaults to DATABASE_URL)")
	fs.DurationVar(&c.timeout, "timeout", 5*time.Minute,
		"overall timeout for the operation")
	return c
}

func (c *commonFlags) validate() error {
	if strings.TrimSpace(c.databaseURL) == "" {
		return fmt.Errorf("no database URL: pass --database-url or set DATABASE_URL")
	}
	return nil
}

// openPool connects using the migrator identity.
//
// Pool configuration is read from flags rather than from the config engine on purpose:
// connection strings and pool sizes are infrastructure wiring, not business policy, and the
// config engine lives behind the database this command is about to migrate.
func (c *commonFlags) openPool(ctx context.Context) (*db.Pool, error) {
	pool, err := db.Open(ctx, db.Config{
		DSN:             c.databaseURL,
		MaxConns:        4,
		MinConns:        1,
		ConnectTimeout:  10 * time.Second,
		ApplicationName: "platformctl",
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return pool, nil
}

func newClock() clock.Clock { return clock.NewSystem() }
