package kyc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/kyc"
	"github.com/satta/platform/libs/go/rules"
	"github.com/satta/platform/libs/go/tenancy"
)

type fakeProvider struct {
	name   string
	state  kyc.ProviderResult
	submit kyc.ProviderResult
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Submit(context.Context, kyc.Case, []string) (kyc.ProviderResult, error) {
	return f.submit, nil
}

func (f *fakeProvider) Query(context.Context, string) (kyc.ProviderResult, error) {
	return f.state, nil
}

type recording struct {
	events []kyc.Event
}

func (r *recording) Record(_ context.Context, ev kyc.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func int64p(v int64) *int64 { return &v }

func edd(t *testing.T) kyc.EDDPolicy {
	t.Helper()
	p, err := kyc.NewEDDPolicy(&rules.Node{All: []*rules.Node{
		{Fact: "kyc_level", LtInt: int64p(2)},
		{Fact: "value_minor", GteInt: int64p(200000)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func harness(t *testing.T) (*kyc.Service, *recording, context.Context) {
	t.Helper()
	st := kyc.NewMemoryStore()
	prov := &fakeProvider{
		name:   "verify-co",
		submit: kyc.ProviderResult{ProviderRef: "vc_1", Status: kyc.StatusInReview},
		state:  kyc.ProviderResult{ProviderRef: "vc_1", Status: kyc.StatusInReview},
	}
	rec := &recording{}
	svc, err := kyc.NewService(st, prov, rec, clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)), edd(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cerr := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	return svc, rec, ctx
}

func TestVerificationHappyPath(t *testing.T) {
	svc, rec, ctx := harness(t)
	if _, err := svc.Start(ctx, "case-1", "alice", "mt"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "case-1", []string{"doc://passport-1", "doc://selfie-1"}); err != nil {
		t.Fatal(err)
	}
	c, err := svc.HumanReview(ctx, "case-1", "officer-7", kyc.StatusVerified, "documents matched", 2)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if c.Status != kyc.StatusVerified || c.Level != 2 {
		t.Fatalf("case = %+v, want verified level 2", c)
	}
	// Transitions are recorded in order with attribution: submitted,
	// provider in_review, verified.
	if len(rec.events) != 3 {
		t.Fatalf("events = %d, want submitted, in_review, verified", len(rec.events))
	}
	if rec.events[0].To != kyc.StatusSubmitted {
		t.Fatalf("first event = %+v", rec.events[0])
	}
	if rec.events[2].To != kyc.StatusVerified || rec.events[2].Reviewer != "officer-7" {
		t.Fatalf("verified event = %+v", rec.events[2])
	}
	lvl, ok := svc.RequiredLevel(ctx, "alice")
	if !ok || lvl != 2 {
		t.Fatalf("RequiredLevel = %d %v, want 2 true", lvl, ok)
	}
}

func TestPushContradictingPullIsRejected(t *testing.T) {
	svc, _, ctx := harness(t)
	if _, err := svc.Start(ctx, "case-2", "bob", "mt"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "case-2", []string{"doc://x"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Confirm(ctx, "case-2", kyc.ProviderResult{ProviderRef: "vc_1", Status: kyc.StatusVerified})
	if !errors.Is(err, kyc.ErrProviderContradiction) {
		t.Fatalf("contradiction: %v, want ErrProviderContradiction", err)
	}
}

func TestRejectionThenResubmit(t *testing.T) {
	svc, _, ctx := harness(t)
	if _, err := svc.Start(ctx, "case-3", "eve", "mt"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "case-3", []string{"doc://y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HumanReview(ctx, "case-3", "officer-1", kyc.StatusRejected, "mismatch", 0); err != nil {
		t.Fatal(err)
	}
	// A rejected player may restart: resubmission reopens the same case.
	c, err := svc.Submit(ctx, "case-3", []string{"doc://z"})
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if c.Status != kyc.StatusInReview && c.Status != kyc.StatusSubmitted {
		t.Fatalf("status after resubmit = %s", c.Status)
	}
}

func TestHumanDecisionRequiresAttribution(t *testing.T) {
	svc, _, ctx := harness(t)
	if _, err := svc.Start(ctx, "case-4", "mallory", "mt"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "case-4", []string{"doc://w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HumanReview(ctx, "case-4", "", kyc.StatusVerified, "", 2); err == nil {
		t.Fatal("anonymous verification accepted — no state change goes unattributed")
	}
}

func TestEDDTrigger(t *testing.T) {
	svc, _, ctx := harness(t)
	if _, err := svc.Start(ctx, "case-5", "pat", "mt"); err != nil {
		t.Fatal(err)
	}
	// Unverified (level 0) crossing 2000.00: EDD.
	hit, err := svc.NeedsEDD(ctx, "pat", 200000)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("EDD did not trigger for level-0 large value")
	}
	// Under the threshold: no trigger.
	hit, nerr := svc.NeedsEDD(ctx, "pat", 100)
	if nerr != nil {
		t.Fatal(nerr)
	}
	if hit {
		t.Fatal("EDD triggered below threshold")
	}
}

func TestSingleLiveJourneyPerPlayer(t *testing.T) {
	svc, _, ctx := harness(t)
	first, err := svc.Start(ctx, "case-a", "sam", "mt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Start(ctx, "case-b", "sam", "mt")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("second Start minted a new case: %s vs %s", second.ID, first.ID)
	}
}

type fakeAgeGate struct{ adult bool }

func (f *fakeAgeGate) Name() string { return "age-co" }

func (f *fakeAgeGate) CheckAge(context.Context, string, string, int) (kyc.AgeDecision, error) {
	return kyc.AgeDecision{Adult: f.adult}, nil
}

func TestAgeGateSeparateFromVerification(t *testing.T) {
	svc, _, ctx := harness(t)
	st := kyc.NewMemoryAgeStore()

	// Minor refused at 18 even though nothing about KYC says so.
	d, err := svc.VerifyAge(ctx, &fakeAgeGate{adult: false}, st, "kid", "mt", 18)
	if !errors.Is(err, kyc.ErrMinor) || d.Adult {
		t.Fatalf("minor: %v %+v, want ErrMinor", err, d)
	}
	// Adult passes and the decision is reused (gate called once).
	calls := 0
	gate := ageCounter{adult: true, calls: &calls}
	d, err = svc.VerifyAge(ctx, &gate, st, "grown", "mt", 18)
	if err != nil || !d.Adult {
		t.Fatalf("adult: %v %+v", err, d)
	}
	if _, err := svc.VerifyAge(ctx, &gate, st, "grown", "mt", 18); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("gate called %d times, want 1 (passed check reused)", calls)
	}
	// A different jurisdiction band is its own decision.
	if _, err := svc.VerifyAge(ctx, &gate, st, "grown", "us", 21); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("band change must re-check: %d", calls)
	}
}

type ageCounter struct {
	adult bool
	calls *int
}

func (a *ageCounter) Name() string { return "age-co" }

func (a *ageCounter) CheckAge(context.Context, string, string, int) (kyc.AgeDecision, error) {
	*a.calls++
	return kyc.AgeDecision{Adult: a.adult}, nil
}
