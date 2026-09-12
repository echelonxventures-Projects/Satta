// Package rules is the platform's policy-evaluation engine (roadmap Task 3).
//
// Deliberately NOT an expression language. Rules arrive as data through the
// config engine (they are business policy — gate 1: no policy literals in
// code) and evaluate against typed facts. The node set is closed and
// auditable, which is also the compliance property regulators ask for: a rule
// readable off a config diff, not a script hidden in a deployment artefact.
//
// Money facts are integers (minor units), times are UTC instants. A fact of
// the wrong kind against a comparison is an evaluation ERROR, never a silent
// false: an eligibility threshold that quietly evaluates false because a
// deposit arrived as a string is a compliance hole, not a business outcome.
package rules

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidRule marks a malformed rule tree or a fact-kind mismatch; both
// are configuration or programming errors, never business answers.
var ErrInvalidRule = errors.New("rules: invalid rule")

// Fact is one named, typed input. The constructors set the kind bit; kind
// checks during evaluation are what keep the "wrong kind is an error"
// property real rather than decorative.
type Fact struct {
	String  string
	Int     int64
	Bool    bool
	Time    time.Time
	Set     []string
	HasStr  bool
	HasInt  bool
	HasBool bool
	HasTime bool
	HasSet  bool
}

// StringFact builds a string-kind fact.
func StringFact(s string) Fact { return Fact{String: s, HasStr: true} }

// IntFact builds an integer-kind fact (money arrives in minor units).
func IntFact(i int64) Fact { return Fact{Int: i, HasInt: true} }

// BoolFact builds a boolean-kind fact.
func BoolFact(b bool) Fact { return Fact{Bool: b, HasBool: true} }

// TimeFact builds a UTC time-kind fact.
func TimeFact(t time.Time) Fact { return Fact{Time: t.UTC(), HasTime: true} }

// SetFact builds a set-kind fact (e.g. jurisdictions a principal is verified
// in; capabilities a tenant holds).
func SetFact(items ...string) Fact {
	return Fact{Set: append([]string(nil), items...), HasSet: true}
}

// Node is one rule-tree node: a composition (All/Any/Not), a constant, or a
// leaf. Validate enforces exactly one branch per node — a node that means two
// things means none of them reliably.
type Node struct {
	All []*Node `json:"all,omitempty"`
	Any []*Node `json:"any,omitempty"`
	Not *Node   `json:"not,omitempty"`
	// Const is a constant leaf, for disabled rules and test scaffolds.
	Const *bool `json:"const,omitempty"`

	// Leaf fields: Fact names the compared input; HasFact tests presence of
	// a different fact without reading its value.
	Fact    string `json:"fact,omitempty"`
	HasFact string `json:"has_fact,omitempty"`

	EqInt  *int64     `json:"eq_int,omitempty"`
	NeqInt *int64     `json:"neq_int,omitempty"`
	GtInt  *int64     `json:"gt_int,omitempty"`
	GteInt *int64     `json:"gte_int,omitempty"`
	LtInt  *int64     `json:"lt_int,omitempty"`
	LteInt *int64     `json:"lte_int,omitempty"`
	StrEq  *string    `json:"str_eq,omitempty"`
	BoolIs *bool      `json:"bool_is,omitempty"`
	Before *time.Time `json:"before,omitempty"`
	After  *time.Time `json:"after,omitempty"`
	// In / NotIn compare the fact's STRING value against a policy set.
	In    []string `json:"in,omitempty"`
	NotIn []string `json:"not_in,omitempty"`
	// CoversSet requires the fact's SET value to contain every listed item
	// (e.g. the player's verified jurisdictions cover this market's).
	CoversSet []string `json:"covers_set,omitempty"`
}

// LeafKind returns the single comparison performed, "" for non-leaves, or
// "mixed" when more than one leaf selector is set (a structural error).
func (n *Node) LeafKind() string {
	leaves := []struct {
		kind string
		on   bool
	}{
		{"eq_int", n.EqInt != nil}, {"neq_int", n.NeqInt != nil},
		{"gt_int", n.GtInt != nil}, {"gte_int", n.GteInt != nil},
		{"lt_int", n.LtInt != nil}, {"lte_int", n.LteInt != nil},
		{"str_eq", n.StrEq != nil}, {"bool_is", n.BoolIs != nil},
		{"before", n.Before != nil}, {"after", n.After != nil},
		{"in", n.In != nil}, {"not_in", n.NotIn != nil},
		{"covers_set", n.CoversSet != nil},
		{"has_fact", n.HasFact != ""},
	}
	kind := ""
	for _, l := range leaves {
		if l.on {
			if kind != "" {
				return "mixed"
			}
			kind = l.kind
		}
	}
	return kind
}

// Validate checks structural well-formedness recursively; run at policy load
// time so a broken rule fails boot, not the first bet it evaluates.
func (n *Node) Validate() error {
	if n == nil {
		return fmt.Errorf("%w: nil node", ErrInvalidRule)
	}
	branches := 0
	if len(n.All) > 0 {
		branches++
	}
	if len(n.Any) > 0 {
		branches++
	}
	if n.Not != nil {
		branches++
	}
	if n.Const != nil {
		branches++
	}
	kind := n.LeafKind()
	if kind == "mixed" {
		return fmt.Errorf("%w: leaf %q mixes comparison selectors", ErrInvalidRule, n.Fact)
	}
	if kind != "" {
		branches++
		if kind != "has_fact" && n.Fact == "" {
			return fmt.Errorf("%w: leaf %s names no fact", ErrInvalidRule, kind)
		}
		if kind == "has_fact" && n.HasFact == "" {
			return fmt.Errorf("%w: has_fact names no fact", ErrInvalidRule)
		}
	}
	if branches != 1 {
		return fmt.Errorf("%w: node with %d branches (want exactly 1)", ErrInvalidRule, branches)
	}
	for _, c := range n.All {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	for _, c := range n.Any {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	if n.Not != nil {
		return n.Not.Validate()
	}
	return nil
}

// Evaluate validates then runs the tree against facts.
func Evaluate(n *Node, facts map[string]Fact) (bool, error) {
	if err := n.Validate(); err != nil {
		return false, err
	}
	return evaluate(n, facts)
}

func evaluate(n *Node, facts map[string]Fact) (bool, error) {
	switch {
	case n.Const != nil:
		return *n.Const, nil
	case len(n.All) > 0:
		for _, c := range n.All {
			ok, err := evaluate(c, facts)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	case len(n.Any) > 0:
		for _, c := range n.Any {
			ok, err := evaluate(c, facts)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case n.Not != nil:
		ok, err := evaluate(n.Not, facts)
		return !ok, err
	}
	return evaluateLeaf(n, facts)
}

func evaluateLeaf(n *Node, facts map[string]Fact) (bool, error) {
	kind := n.LeafKind()
	if kind == "has_fact" {
		_, ok := facts[n.HasFact]
		return ok, nil
	}
	fact, ok := facts[n.Fact]
	if !ok {
		return false, fmt.Errorf("%w: fact %q not supplied", ErrInvalidRule, n.Fact)
	}
	switch kind {
	case "eq_int", "neq_int", "gt_int", "gte_int", "lt_int", "lte_int":
		if !fact.HasInt {
			return false, fmt.Errorf("%w: fact %q is not an integer", ErrInvalidRule, n.Fact)
		}
		var want int64
		switch kind {
		case "eq_int":
			want = *n.EqInt
		case "neq_int":
			want = *n.NeqInt
		case "gt_int":
			want = *n.GtInt
		case "gte_int":
			want = *n.GteInt
		case "lt_int":
			want = *n.LtInt
		case "lte_int":
			want = *n.LteInt
		}
		switch kind {
		case "eq_int":
			return fact.Int == want, nil
		case "neq_int":
			return fact.Int != want, nil
		case "gt_int":
			return fact.Int > want, nil
		case "gte_int":
			return fact.Int >= want, nil
		case "lt_int":
			return fact.Int < want, nil
		default: // lte_int
			return fact.Int <= want, nil
		}
	case "str_eq":
		if !fact.HasStr {
			return false, fmt.Errorf("%w: fact %q is not a string", ErrInvalidRule, n.Fact)
		}
		return fact.String == *n.StrEq, nil
	case "bool_is":
		if !fact.HasBool {
			return false, fmt.Errorf("%w: fact %q is not a bool", ErrInvalidRule, n.Fact)
		}
		return fact.Bool == *n.BoolIs, nil
	case "before":
		if !fact.HasTime {
			return false, fmt.Errorf("%w: fact %q is not a time", ErrInvalidRule, n.Fact)
		}
		return fact.Time.Before(n.Before.UTC()), nil
	case "after":
		if !fact.HasTime {
			return false, fmt.Errorf("%w: fact %q is not a time", ErrInvalidRule, n.Fact)
		}
		return fact.Time.After(n.After.UTC()), nil
	case "in":
		if !fact.HasStr {
			return false, fmt.Errorf("%w: fact %q is not a string", ErrInvalidRule, n.Fact)
		}
		return contains(n.In, fact.String), nil
	case "not_in":
		if !fact.HasStr {
			return false, fmt.Errorf("%w: fact %q is not a string", ErrInvalidRule, n.Fact)
		}
		return !contains(n.NotIn, fact.String), nil
	case "covers_set":
		if !fact.HasSet {
			return false, fmt.Errorf("%w: fact %q is not a set", ErrInvalidRule, n.Fact)
		}
		for _, want := range n.CoversSet {
			if !contains(fact.Set, want) {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("%w: leaf %q carries no comparison", ErrInvalidRule, n.Fact)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
