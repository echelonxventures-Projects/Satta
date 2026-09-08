package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"

	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// runVerifyEvidence recomputes evidence hash chains and reports integrity.
//
// SAFETY INVARIANT 4 makes the platform tamper-evident, not tamper-proof. That distinction is
// only worth anything if something performs the verification — a hash chain nobody checks is a
// hash chain that detects nothing. This command is that something, and it is meant to run on a
// schedule as well as by hand.
//
// It exits non-zero when any chain fails, so a scheduled run is a monitorable signal rather
// than output someone has to read.
func runVerifyEvidence(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("verify-evidence", flag.ExitOnError)
	common := registerCommon(flags)
	chainFilter := flags.String("chain", "", "verify only this chain (default: every chain)")
	tenantFilter := flags.String("tenant", "", "verify only chains belonging to this tenant")
	quiet := flags.Bool("quiet", false, "print only failures and the summary")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := common.validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, common.timeout)
	defer cancel()

	pool, err := common.openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	chains, err := listChains(ctx, pool, *chainFilter, *tenantFilter)
	if err != nil {
		return err
	}
	if len(chains) == 0 {
		fmt.Println("no evidence chains found")
		return nil
	}

	store := evidence.NewPostgresStore(pool, newClock())

	writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	if !*quiet {
		fmt.Fprintln(writer, "CHAIN\tTENANT\tRECORDS\tHEAD\tRESULT")
	}

	var broken, verified int
	for _, chain := range chains {
		// Each chain is verified under its owning tenant's scope, because the store enforces
		// tenant isolation on every read. Verification deliberately goes through the same
		// isolated read path as production traffic rather than around it.
		chainCtx, err := tenancy.WithScope(ctx, tenancy.Scope{TenantID: chain.tenantID})
		if err != nil {
			return fmt.Errorf("chain %s: %w", chain.chainID, err)
		}
		chainCtx, err = identity.WithActor(chainCtx, identity.ServiceActor("platformctl"))
		if err != nil {
			return fmt.Errorf("chain %s: %w", chain.chainID, err)
		}

		result, err := evidence.Verify(chainCtx, store, chain.chainID, 0, 0)
		if err != nil {
			return fmt.Errorf("verify chain %s: %w", chain.chainID, err)
		}

		outcome := "intact"
		if !result.Intact {
			outcome = fmt.Sprintf("BROKEN at %d: %s", result.FirstBadSequence, result.FailureReason)
			broken++
		} else {
			verified++
		}

		if !*quiet || !result.Intact {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%x\t%s\n",
				chain.chainID, chain.tenantID, result.RecordsChecked,
				truncateHash(result.HeadHash), outcome)
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}

	fmt.Printf("\n%d chain(s) verified, %d broken\n", verified, broken)
	if broken > 0 {
		// A broken chain is not a warning. It means either the storage layer corrupted data or
		// somebody altered an audit record, and both require investigation before the platform
		// is trusted to report on itself.
		return fmt.Errorf("%d evidence chain(s) failed integrity verification", broken)
	}
	return nil
}

type chainRef struct {
	chainID  string
	tenantID string
}

// listChains reads the chain registry outside tenant scope.
//
// This is one of the few legitimate uses of an unscoped transaction: enumerating every tenant's
// chains is inherently a platform-level operation, and a verifier that could only see one
// tenant's chains would not be able to answer "is the evidence store intact".
func listChains(ctx context.Context, pool interface {
	InTx(context.Context, func(context.Context, pgx.Tx) error) error
}, chainFilter, tenantFilter string) ([]chainRef, error) {
	var chains []chainRef
	err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT chain_id, tenant_id
			  FROM evidence_chain
			 WHERE ($1 = '' OR chain_id = $1)
			   AND ($2 = '' OR tenant_id = $2)
			 ORDER BY tenant_id, chain_id`, chainFilter, tenantFilter)
		if err != nil {
			if db.IsUndefinedTable(err) {
				// The schema has not been migrated. Reported as "no chains" rather than as an
				// error, so that `task up` on a fresh database does not fail here.
				return nil
			}
			return fmt.Errorf("list evidence chains: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ref chainRef
			if err := rows.Scan(&ref.chainID, &ref.tenantID); err != nil {
				return fmt.Errorf("scan chain row: %w", err)
			}
			chains = append(chains, ref)
		}
		return rows.Err()
	})
	return chains, err
}

func truncateHash(hash []byte) []byte {
	if len(hash) > 6 {
		return hash[:6]
	}
	return hash
}
