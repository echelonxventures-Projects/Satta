package aml_test

import (
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/aml"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/rules"
)

func int64p(v int64) *int64 { return &v }

var fixed = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func velocityRules() []aml.Rule {
	return []aml.Rule{
		{
			ID: "rapid-deposits", Severity: "alert", SAR: true, Window: "hourly",
			Node: &rules.Node{All: []*rules.Node{
				{Fact: "deposits_24h", GteInt: int64p(5)},
			}},
		},
		{
			ID: "ceiling", Severity: "block", Window: "daily",
			Node: &rules.Node{Any: []*rules.Node{
				{Fact: "deposit_minor", GteInt: int64p(1000000)},
				{Fact: "chargebacks_7d", GteInt: int64p(2)},
			}},
		},
	}
}

func engine(t *testing.T) *aml.Engine {
	t.Helper()
	e, err := aml.NewEngine(clock.NewFixed(fixed), velocityRules()...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func facts(deposits, deposit, chargebacks int64) map[string]rules.Fact {
	return map[string]rules.Fact{
		"deposits_24h":   rules.IntFact(deposits),
		"deposit_minor":  rules.IntFact(deposit),
		"chargebacks_7d": rules.IntFact(chargebacks),
	}
}

func TestAlertBlockAndDedupe(t *testing.T) {
	e := engine(t)
	// Rapid deposits: alert + SAR case, first time only.
	v, err := e.Observe(t.Context(), "alice", facts(6, 5000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if v != aml.VerdictAlert {
		t.Fatalf("verdict = %v, want alert", v)
	}
	// Repeat in the same hour: no duplicate case (escalating signal).
	if _, err := e.Observe(t.Context(), "alice", facts(6, 5000, 0)); err != nil {
		t.Fatal(err)
	}
	cases := e.Cases()
	if len(cases) != 1 {
		t.Fatalf("cases = %d, want 1 (deduped)", len(cases))
	}
	if len(e.SARs()) != 1 {
		t.Fatalf("SARs = %d, want 1", len(e.SARs()))
	}
}

func TestBlockCeiling(t *testing.T) {
	e := engine(t)
	v, err := e.Observe(t.Context(), "bob", facts(0, 1000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if v != aml.VerdictBlock {
		t.Fatalf("verdict = %v, want block", v)
	}
	if _, err := e.Observe(t.Context(), "carol", facts(0, 999999, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestChargebackWindow(t *testing.T) {
	e := engine(t)
	e.MarkChargeback("dave", "cb-1")
	e.MarkChargeback("dave", "cb-2")
	if got := e.Chargebacks7d("dave"); got != 2 {
		t.Fatalf("chargebacks = %d, want 2", got)
	}
	// Two chargebacks make the block ceiling fire via the OR.
	v, err := e.Observe(t.Context(), "dave", facts(0, 10, 2))
	if err != nil {
		t.Fatal(err)
	}
	if v != aml.VerdictBlock {
		t.Fatalf("verdict = %v, want block", v)
	}
}

func TestEngineValidationAtBoot(t *testing.T) {
	if _, err := aml.NewEngine(nil); err == nil {
		t.Fatal("nil clock accepted")
	}
	if _, err := aml.NewEngine(clock.NewFixed(fixed),
		aml.Rule{ID: "x", Severity: "explode", Node: &rules.Node{Const: boolp(true)}}); err == nil {
		t.Fatal("unknown severity accepted")
	}
	if _, err := aml.NewEngine(clock.NewFixed(fixed), aml.Rule{ID: "y"}); err == nil {
		t.Fatal("rule without node accepted")
	}
}

func TestCryptoRiskBands(t *testing.T) {
	rb := aml.RiskBlocks{RefuseBands: map[string]bool{"high": true, "severe": true, "unknown": true}}
	if err := rb.Check("low"); err != nil {
		t.Fatalf("low band blocked: %v", err)
	}
	err := rb.Check("severe")
	if !errors.Is(err, aml.ErrBlocked) {
		t.Fatalf("severe: %v, want ErrBlocked", err)
	}
	if err := rb.Check("unknown"); err == nil {
		t.Fatal("unknown band allowed — deny-by-default is the point")
	}
}

func boolp(v bool) *bool { return &v }
