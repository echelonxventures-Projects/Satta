// Package kyc is identity verification and onboarding (roadmap Task 12):
// the case state machine, the provider port, enhanced-due-diligence
// triggers, and the fact the rest of the platform gates on (a player's KYC
// level is what withdrawals, deposit ceilings, and AML thresholds all read).
//
// Posture:
//
//   - The platform never stores identity document CONTENT. Documents are
//     referenced by opaque handles the provider issued; a KYC case is
//     metadata + decisions, because a breach of our database should never
//     hand out passports.
//   - Every decision (start, submit, verify, reject, escalate) is an
//     evidence-worthy transition: the package emits them on a port the
//     service wires to the evidence store — auditors reconstruct who was
//     verified to what level, when, on whose authority, forever.
//   - Provider truth is only ever observed via Query/reconcile, never
//     trusted from a push: Confirm requires the provider's own view of the
//     case to agree before a status moves.
package kyc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/tenancy"
)

// Errors.
var (
	ErrBadTransition = errors.New("kyc: illegal transition")
	ErrNotFound      = errors.New("kyc: unknown case")
	// ErrProviderContradiction: the push said verified, the pull says not.
	ErrProviderContradiction = errors.New("kyc: provider state contradicts signal")
)

// Status of a verification case.
type Status string

const (
	StatusNone      Status = "none"      // never started
	StatusSubmitted Status = "submitted" // documents with the provider
	StatusInReview  Status = "in_review" // provider needs manual/documentary follow-up
	StatusVerified  Status = "verified"  // passed at some Level
	StatusRejected  Status = "rejected"  // failed; may restart a fresh case
	StatusEscalated Status = "escalated" // EDD: higher assurance required (source-of-funds)
)

// Terminal reports closed states (a new case re-enters, never reopens).
func (s Status) Terminal() bool { return s == StatusVerified || s == StatusRejected }

// Case is one verification journey for a player.
type Case struct {
	ID           string
	TenantID     string
	PlayerID     string
	Jurisdiction string
	Status       Status
	Level        int // assurance level reached (policy defines what each unlocks)
	Provider     string
	ProviderRef  string
	DocHandles   []string // opaque provider handles; contents NEVER here
	Reason       string
	Reviewer     string // human decision attribution (who), never "the system"
	OpenedAt     time.Time
	UpdatedAt    time.Time
}

// Advance validates a transition; same semantics as payments/withdrawals.
func (c *Case) Advance(to Status, when time.Time) (bool, error) {
	edges := map[Status][]Status{
		StatusSubmitted: {StatusNone, StatusRejected}, // resubmission after rejection reopens the case
		StatusInReview:  {StatusSubmitted, StatusEscalated},
		StatusVerified:  {StatusSubmitted, StatusInReview, StatusEscalated},
		StatusRejected:  {StatusSubmitted, StatusInReview, StatusEscalated},
		StatusEscalated: {StatusSubmitted, StatusInReview},
	}
	if to == c.Status {
		return false, nil
	}
	if !contains(edges[to], c.Status) {
		return false, fmt.Errorf("%w: %s -> %s on case %s", ErrBadTransition, c.Status, to, c.ID)
	}
	c.Status = to
	c.UpdatedAt = when.UTC()
	return true, nil
}

func contains(hay []Status, needle Status) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// ProviderResult is what the verification vendor returns on submit or query.
type ProviderResult struct {
	ProviderRef string
	Status      Status
	Level       int
	Reason      string
}

// Provider is the KYC vendor port (DoD gate 2): document upload, checks,
// and EDD follow-up all live behind it. Vendor adapters land service-side.
type Provider interface {
	Name() string
	Submit(ctx context.Context, c Case, docHandles []string) (ProviderResult, error)
	Query(ctx context.Context, providerRef string) (ProviderResult, error)
}

// Store persists cases.
type Store interface {
	Save(ctx context.Context, c Case) error
	Load(ctx context.Context, id string) (Case, error)
	ByPlayer(ctx context.Context, tenantID, playerID string) (Case, bool)
}

// MemoryStore implements Store.
type MemoryStore struct {
	byID     map[string]Case
	byPlayer map[string]string
}

// NewMemoryStore returns empty.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]Case{}, byPlayer: map[string]string{}}
}

// Save upserts.
func (m *MemoryStore) Save(_ context.Context, c Case) error {
	m.byID[c.ID] = c
	m.byPlayer[c.TenantID+"|"+c.PlayerID] = c.ID
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context, id string) (Case, error) {
	c, ok := m.byID[id]
	if !ok {
		return Case{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return c, nil
}

// ByPlayer returns the current case for a player (if any).
func (m *MemoryStore) ByPlayer(_ context.Context, tenantID, playerID string) (Case, bool) {
	id, ok := m.byPlayer[tenantID+"|"+playerID]
	if !ok {
		return Case{}, false
	}
	return m.byID[id], true
}

// Event is the transition record the service appends to the evidence store
// (gate 5): identity state changes are audit events by definition.
type Event struct {
	CaseID   string
	TenantID string
	PlayerID string
	From, To Status
	Level    int
	Reason   string
	Reviewer string
	At       time.Time
}

// Recorder persists transitions (the service binds evidence.Append here).
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// Service is the onboarding pipeline.
type Service struct {
	store    Store
	provider Provider
	rec      Recorder
	clk      clock.Clock
	eddRules EDDPolicy
}

// NewService requires store, provider and recorder (audit is not optional);
// a silent no-op recorder must be passed explicitly, never defaulted.
func NewService(store Store, prov Provider, rec Recorder, clk clock.Clock, edd EDDPolicy) (*Service, error) {
	if store == nil || prov == nil || rec == nil || clk == nil {
		return nil, errors.New("kyc: Service needs store, provider, recorder (audit is not optional), clock, EDD policy")
	}
	return &Service{store: store, provider: prov, rec: rec, clk: clk, eddRules: edd}, nil
}

// Start opens (or reuses) the player's case.
func (s *Service) Start(ctx context.Context, id, player, jurisdiction string) (Case, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return Case{}, err
	}
	if prev, ok := s.store.ByPlayer(ctx, scope.TenantID, player); ok {
		return prev, nil // exactly one live journey per player
	}
	now := s.clk.Now().UTC()
	c := Case{
		ID: id, TenantID: scope.TenantID, PlayerID: player, Jurisdiction: jurisdiction,
		Status: StatusNone, Provider: s.provider.Name(), OpenedAt: now, UpdatedAt: now,
	}
	if serr := s.store.Save(ctx, c); serr != nil {
		return Case{}, serr
	}
	return c, nil
}

// Submit hands documents to the provider and records what it said.
func (s *Service) Submit(ctx context.Context, caseID string, docHandles []string) (Case, error) {
	c, err := s.store.Load(ctx, caseID)
	if err != nil {
		return Case{}, err
	}
	if len(docHandles) == 0 {
		return Case{}, errors.New("kyc: Submit requires document handles")
	}
	// Sort handles so the case content is deterministic in replay.
	sort.Strings(docHandles)
	c.DocHandles = appendUnique(c.DocHandles, docHandles)
	from := c.Status
	if _, aerr := c.Advance(StatusSubmitted, s.clk.Now()); aerr != nil {
		return Case{}, aerr
	}
	if serr := s.store.Save(ctx, c); serr != nil {
		return Case{}, serr
	}
	// The submission itself is an audit-worthy transition, like every other.
	if rerr := s.rec.Record(ctx, Event{
		CaseID: c.ID, TenantID: c.TenantID, PlayerID: c.PlayerID,
		From: from, To: c.Status, At: c.UpdatedAt,
	}); rerr != nil {
		return Case{}, rerr
	}
	res, perr := s.provider.Submit(ctx, c, docHandles)
	if perr != nil {
		return Case{}, fmt.Errorf("kyc: provider submit: %w", perr)
	}
	c.ProviderRef = res.ProviderRef
	if err := s.store.Save(ctx, c); err != nil {
		return Case{}, err
	}
	if res.Status == StatusVerified || res.Status == StatusInReview || res.Status == StatusRejected {
		return s.apply(ctx, caseID, res)
	}
	return s.store.Load(ctx, caseID)
}

// Confirm reconciles a push/webhook against the provider's own pull before
// applying it — provider truth is what Query says, never what a payload
// claims.
func (s *Service) Confirm(ctx context.Context, caseID string, push ProviderResult) (Case, error) {
	if _, err := s.store.Load(ctx, caseID); err != nil {
		return Case{}, err
	}
	q, qerr := s.provider.Query(ctx, push.ProviderRef)
	if qerr != nil {
		return Case{}, fmt.Errorf("kyc: confirm query: %w", qerr)
	}
	if (push.Status == StatusVerified || push.Status == StatusRejected) && q.Status != push.Status {
		return Case{}, fmt.Errorf("%w: push=%s query=%s on %s", ErrProviderContradiction, push.Status, q.Status, caseID)
	}
	return s.apply(ctx, caseID, q)
}

// HumanReview records a staff decision (rejections require the operator's
// attribution — the reviewer field is not optional).
func (s *Service) HumanReview(ctx context.Context, caseID, reviewer string, to Status, reason string, level int) (Case, error) {
	if reviewer == "" {
		return Case{}, errors.New("kyc: a human decision must name its author")
	}
	c, err := s.store.Load(ctx, caseID)
	if err != nil {
		return Case{}, err
	}
	if to != StatusVerified && to != StatusRejected && to != StatusEscalated {
		return Case{}, fmt.Errorf("%w: human may not set %q", ErrBadTransition, to)
	}
	res := ProviderResult{Status: to, Level: level, Reason: reason, ProviderRef: c.ProviderRef}
	c.Reviewer = reviewer
	if err := s.store.Save(ctx, c); err != nil {
		return Case{}, err
	}
	return s.apply(ctx, caseID, res)
}

// apply moves the case to a provider result's status, stamps the level,
// and records the transition as an evidence-worthy event.
func (s *Service) apply(ctx context.Context, caseID string, res ProviderResult) (Case, error) {
	c, err := s.store.Load(ctx, caseID)
	if err != nil {
		return Case{}, err
	}
	if res.ProviderRef != "" {
		c.ProviderRef = res.ProviderRef
	}
	from := c.Status
	changed, aerr := c.Advance(res.Status, s.clk.Now())
	if aerr != nil {
		return Case{}, aerr
	}
	c.Reason = res.Reason
	if res.Status == StatusVerified {
		c.Level = res.Level
	}
	if serr := s.store.Save(ctx, c); serr != nil {
		return Case{}, serr
	}
	if changed {
		if rerr := s.rec.Record(ctx, Event{
			CaseID: c.ID, TenantID: c.TenantID, PlayerID: c.PlayerID,
			From: from, To: c.Status, Level: c.Level, Reason: res.Reason,
			Reviewer: c.Reviewer, At: c.UpdatedAt,
		}); rerr != nil {
			return Case{}, rerr
		}
	}
	return c, nil
}

// RequiredLevel resolves the policy question the rest of the platform asks:
// what assurance must a player hold (withdrawals, deposit ceilings and AML
// all read this fact, never a cached guess).
func (s *Service) RequiredLevel(ctx context.Context, player string) (int, bool) {
	c, ok := s.store.ByPlayer(ctx, ctxTenant(ctx), player)
	if !ok {
		return 0, false
	}
	if c.Status != StatusVerified {
		return 0, true // known but not verified: level 0
	}
	return c.Level, true
}

// NeedsEDD asks the policy whether a value crosses the enhanced-diligence
// line for a player at their current level.
func (s *Service) NeedsEDD(ctx context.Context, player string, valueMinor int64) (bool, error) {
	level, _ := s.RequiredLevel(ctx, player)
	return s.eddRules.Trigger(level, valueMinor)
}

func ctxTenant(ctx context.Context) string {
	if scope, ok := tenancy.FromContext(ctx); ok {
		return scope.TenantID
	}
	return ""
}

func appendUnique(existing, add []string) []string {
	seen := map[string]bool{}
	out := append([]string(nil), existing...)
	for _, v := range add {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
