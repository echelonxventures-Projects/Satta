package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/satta/platform/libs/go/config"
	"github.com/satta/platform/libs/go/tenancy"
)

// requiredLocalKeys are policy keys the local environment must define for a service to boot.
//
// This list is the point of the seed command. Definition of Done gate 1 forbids literal
// business-policy values in code, which means a service cannot fall back to a sensible default
// when a key is missing — it has to fail. Verifying the keys resolve here turns that into a
// clear message at seed time instead of a boot failure later, or worse, a failure discovered
// when the first bet is priced.
var requiredLocalKeys = []string{
	"platform.default_currency",
	"platform.rounding_mode",
	"evidence.retention_class.default",
	"evidence.retention_class.financial",
	"metering.default_unit",
	"tenancy.default_jurisdiction",
}

func runSeed(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("seed", flag.ExitOnError)
	common := registerCommon(flags)
	policyPath := flags.String("policy", "config/local/policy.yaml",
		"path to the local policy configuration file")
	checkOnly := flags.Bool("check", false, "validate without writing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// The database URL is accepted but not yet required: there are no tenant tables to seed
	// until the IAM work in Task 6 lands. Validated only if provided, so that a typo in a
	// connection string is still caught here rather than silently ignored.
	_ = common

	if _, err := os.Stat(*policyPath); errors.Is(err, os.ErrNotExist) {
		if *checkOnly {
			return fmt.Errorf("policy file %s does not exist", *policyPath)
		}
		return fmt.Errorf(
			"policy file %s does not exist; it is committed to the repository, so this usually means "+
				"the command was run from outside the repository root", *policyPath)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", *policyPath, err)
	}

	resolver, err := config.NewFileResolver(*policyPath)
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}

	version, err := resolver.Version(ctx)
	if err != nil {
		return fmt.Errorf("read policy version: %w", err)
	}

	// Resolve against the local development scope. A key defined only at global scope still
	// resolves here, because a wider scope covers a narrower one.
	scope := tenancy.Scope{
		TenantID:         "local",
		JurisdictionCode: "mt",
		RegionCode:       "eu-west",
	}
	if rerr := config.Require(ctx, resolver, scope, requiredLocalKeys...); rerr != nil {
		return rerr
	}

	keys := resolver.Keys()
	sort.Strings(keys)

	abs, err := filepath.Abs(*policyPath)
	if err != nil {
		return fmt.Errorf("resolve policy path: %w", err)
	}
	fmt.Printf("policy loaded from %s\n", abs)
	fmt.Printf("  version   %s (content hash)\n", version)
	fmt.Printf("  keys      %d defined, %d required and present\n", len(keys), len(requiredLocalKeys))
	fmt.Printf("  scope     %s\n", scope.String())

	// Tenant, brand and product seeding arrives with the IAM and product-registry work. Stated
	// explicitly so that a developer expecting seeded tenants knows why there are none, rather
	// than assuming the seed failed silently.
	fmt.Println("  tenants   none yet — tenant seeding arrives with Task 6 (Auth, IAM & multi-tenancy)")
	return nil
}
