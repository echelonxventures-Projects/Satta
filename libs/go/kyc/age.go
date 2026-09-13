package kyc

import (
	"context"
	"errors"
	"fmt"
)

// AgeGate is the SEPARATE age-verification port (plan G4): "18+ (or 21+
// per jurisdiction) may play" is a distinct legal question from "this
// identity is verified" — different vendors, different evidence, different
// law. A KYC verification without a passing age decision is NOT an access
// grant; services must ask both and this package refuses to conflate them.
type AgeGate interface {
	Name() string
	// CheckAge verifies the player meets minimumAge. The provider sees the
	// document; the platform stores ONLY the answer (below), never the date
	// of birth — storing DOB here would replicate identity data the KYC
	// posture already excludes.
	CheckAge(ctx context.Context, tenantID, playerID string, minimumAge int) (AgeDecision, error)
}

// AgeDecision is the storable result: answer + provider reference, no
// personal data.
type AgeDecision struct {
	Adult       bool
	MinimumAge  int
	ProviderRef string
}

// AgeStore persists decisions per (tenant, player, minimumAge band).
type AgeStore interface {
	SaveAge(ctx context.Context, tenantID, playerID string, d AgeDecision) error
	GetAge(ctx context.Context, tenantID, playerID string, minimumAge int) (AgeDecision, bool)
}

// MemoryAgeStore implements AgeStore in-process.
type MemoryAgeStore struct {
	byKey map[string]AgeDecision
}

// NewMemoryAgeStore returns empty.
func NewMemoryAgeStore() *MemoryAgeStore {
	return &MemoryAgeStore{byKey: map[string]AgeDecision{}}
}

// SaveAge implements AgeStore (latest answer wins; providers re-check at
// their own cadence).
func (m *MemoryAgeStore) SaveAge(_ context.Context, tenant, player string, d AgeDecision) error {
	m.byKey[tenant+"|"+player+"|"+fmt.Sprint(d.MinimumAge)] = d
	return nil
}

// GetAge implements AgeStore.
func (m *MemoryAgeStore) GetAge(_ context.Context, tenant, player string, minimumAge int) (AgeDecision, bool) {
	d, ok := m.byKey[tenant+"|"+player+"|"+fmt.Sprint(minimumAge)]
	return d, ok
}

// VerifyAge runs (or reuses) the age decision for a jurisdiction's minimum.
func (s *Service) VerifyAge(ctx context.Context, gate AgeGate, ages AgeStore, player, jurisdiction string, minimumAge int) (AgeDecision, error) {
	if gate == nil || ages == nil {
		return AgeDecision{}, errors.New("kyc: VerifyAge requires a gate and a store")
	}
	if minimumAge <= 0 || minimumAge > 30 {
		return AgeDecision{}, fmt.Errorf("kyc: implausible minimum age %d", minimumAge)
	}
	scope, err := requireTenant(ctx)
	if err != nil {
		return AgeDecision{}, err
	}
	d, ok := ages.GetAge(ctx, scope, player, minimumAge)
	if ok && d.Adult {
		return d, nil // a passed adult check at this band does not re-run
	}
	d, err = gate.CheckAge(ctx, scope, player, minimumAge)
	if err != nil {
		return AgeDecision{}, fmt.Errorf("kyc: age check: %w", err)
	}
	d.MinimumAge = minimumAge
	if serr := ages.SaveAge(ctx, scope, player, d); serr != nil {
		return AgeDecision{}, serr
	}
	if !d.Adult {
		return d, fmt.Errorf("%w: minor for jurisdiction %s (minimum %d)", ErrMinor, jurisdiction, minimumAge)
	}
	return d, nil
}

// ErrMinor is the typed NO a session gate reads.
var ErrMinor = errors.New("kyc: age check says not adult")

func requireTenant(ctx context.Context) (string, error) {
	t := ctxTenant(ctx)
	if t == "" {
		return "", errors.New("kyc: tenant scope required")
	}
	return t, nil
}
