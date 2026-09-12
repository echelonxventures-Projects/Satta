package rules_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/rules"
)

// The rule under test mirrors a real policy shape: "deposits above the KYC
// threshold require a verified level-2 identity, and only inside a covered
// jurisdiction." Config-defined, auditable, and typed end to end.
func int64p(v int64) *int64     { return &v }
func strp(s string) *string     { return &s }
func boolp(b bool) *bool        { return &b }
func tp(t time.Time) *time.Time { return &t }

func depositGate() *rules.Node {
	return &rules.Node{All: []*rules.Node{
		{Any: []*rules.Node{
			{LtInt: int64p(10000), Fact: "amount_minor"}, // under threshold: pass
			{All: []*rules.Node{
				{GteInt: int64p(10000), Fact: "amount_minor"},
				{EqInt: int64p(2), Fact: "kyc_level"},
			}},
		}},
		{CoversSet: []string{"mt"}, Fact: "verified_jurisdictions"},
	}}
}

func facts(amount, kyc int64, jurisdictions ...string) map[string]rules.Fact {
	return map[string]rules.Fact{
		"amount_minor":           rules.IntFact(amount),
		"kyc_level":              rules.IntFact(kyc),
		"verified_jurisdictions": rules.SetFact(jurisdictions...),
	}
}

func TestDepositGate(t *testing.T) {
	gate := depositGate()
	cases := []struct {
		name  string
		facts map[string]rules.Fact
		want  bool
	}{
		{"small deposit passes", facts(9999, 0, "mt"), true},
		{"large deposit with KYC2 passes", facts(10000, 2, "mt"), true},
		{"large deposit without KYC fails", facts(50000, 1, "mt"), false},
		{"KYC2 but wrong jurisdiction fails", facts(50000, 2, "de"), false},
		{"KYC2 but no jurisdictions fails", facts(50000, 2), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rules.Evaluate(gate, tc.facts)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got != tc.want {
				t.Fatalf("gate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKindMismatchIsErrorNotSilentFalse(t *testing.T) {
	// The compliance property: a string fact compared as int must ERROR,
	// never evaluate false — a silent false is a business denial made of a
	// type bug.
	n := &rules.Node{GteInt: int64p(10), Fact: "amount_minor"}
	_, err := rules.Evaluate(n, map[string]rules.Fact{
		"amount_minor": rules.StringFact("500"),
	})
	if err == nil {
		t.Fatal("string fact compared as int produced no error")
	}
	if _, err := rules.Evaluate(n, map[string]rules.Fact{}); err == nil {
		t.Fatal("missing fact produced no error")
	}
}

func TestTimeComparisons(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := &rules.Node{Before: tp(cutoff), Fact: "occurred_at"}
	ok, err := rules.Evaluate(n, map[string]rules.Fact{
		"occurred_at": rules.TimeFact(cutoff.Add(-time.Hour)),
	})
	if err != nil || !ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	ok, err = rules.Evaluate(n, map[string]rules.Fact{
		"occurred_at": rules.TimeFact(cutoff.Add(time.Hour)),
	})
	if err != nil || ok {
		t.Fatalf("after cutoff: %v %v", ok, err)
	}
}

func TestNotAndConstAndHasFact(t *testing.T) {
	n := &rules.Node{All: []*rules.Node{
		{Not: &rules.Node{StrEq: strp("blocked"), Fact: "state"}},
		{HasFact: "session_id"},
		{Const: boolp(true)},
	}}
	ok, err := rules.Evaluate(n, map[string]rules.Fact{
		"state":      rules.StringFact("active"),
		"session_id": rules.StringFact("s-1"),
	})
	if err != nil || !ok {
		t.Fatalf("composed: %v %v", ok, err)
	}
	ok, err = rules.Evaluate(n, map[string]rules.Fact{"state": rules.StringFact("active")})
	if err == nil && ok {
		t.Fatal("missing has_fact passed")
	}
}

func TestValidateRejectsMalformedTrees(t *testing.T) {
	cases := map[string]*rules.Node{
		"empty node":        {},
		"mixed leaves":      {Fact: "f", EqInt: int64p(1), StrEq: strp("x")},
		"two branches":      {All: []*rules.Node{{Const: boolp(true)}}, Not: &rules.Node{Const: boolp(false)}},
		"leaf without fact": {EqInt: int64p(1)},
		"nil child":         {All: []*rules.Node{nil}},
	}
	for name, n := range cases {
		if err := n.Validate(); err == nil {
			t.Errorf("%s: malformed tree accepted", name)
		}
	}
}

func TestRuleRoundTripsAsJSON(t *testing.T) {
	// Rules are loaded from config as JSON; the round-trip must be faithful.
	raw, err := json.Marshal(depositGate())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back rules.Node
	if uerr := json.Unmarshal(raw, &back); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	ok, err := rules.Evaluate(&back, facts(10000, 2, "mt"))
	if err != nil || !ok {
		t.Fatalf("rebuilt rule: %v %v", ok, err)
	}
}
