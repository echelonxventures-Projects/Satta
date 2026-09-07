package config

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/satta/platform/libs/go/tenancy"
)

// Require verifies that every key resolves for the given scope, reporting all
// missing keys at once.
//
// Services call this during startup with the keys they will need. A policy key
// that is absent is then a boot failure with a complete list, rather than a
// runtime failure discovered when the first player places the first bet on the
// affected market. That ordering matters: an operator can fix configuration
// before opening; they cannot un-take a bet that was priced wrongly.
func Require(ctx context.Context, r Resolver, scope tenancy.Scope, keys ...string) error {
	var missing []string
	for _, key := range keys {
		if _, err := r.Resolve(ctx, scope, key); err != nil {
			if errors.Is(err, ErrNotFound) {
				missing = append(missing, key)
				continue
			}
			return fmt.Errorf("config: resolving required key %q for %s: %w", key, scope.String(), err)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("config: %d required key(s) not defined for scope %s: %s",
			len(missing), scope.String(), strings.Join(missing, ", "))
	}
	return nil
}

// Typed helpers. These exist so that call sites read as one line rather than
// three, and so that the error consistently names the key and scope.

// String resolves key as a string.
func String(ctx context.Context, r Resolver, scope tenancy.Scope, key string) (string, error) {
	v, err := r.Resolve(ctx, scope, key)
	if err != nil {
		return "", err
	}
	return v.String()
}

// Int resolves key as an integer.
func Int(ctx context.Context, r Resolver, scope tenancy.Scope, key string) (int64, error) {
	v, err := r.Resolve(ctx, scope, key)
	if err != nil {
		return 0, err
	}
	return v.Int()
}

// Bool resolves key as a boolean.
func Bool(ctx context.Context, r Resolver, scope tenancy.Scope, key string) (bool, error) {
	v, err := r.Resolve(ctx, scope, key)
	if err != nil {
		return false, err
	}
	return v.Bool()
}
