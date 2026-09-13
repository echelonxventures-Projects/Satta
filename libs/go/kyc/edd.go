package kyc

import (
	"errors"

	"github.com/satta/platform/libs/go/rules"
)

// EDDPolicy decides whether a transaction value requires enhanced due
// diligence, given the player's current assurance level. It is a rules-tree
// evaluation like every other business decision: the thresholds are policy
// (roadmap Task 12: "enhanced due diligence triggers"), the evaluation is
// data — and both come from config, never from a number in code (gate 1).
type EDDPolicy struct {
	triggers *rules.Node
}

// NewEDDPolicy compiles the trigger tree at construction; a malformed tree
// is a boot failure, not a silent "no EDD".
func NewEDDPolicy(triggers *rules.Node) (EDDPolicy, error) {
	if triggers == nil {
		return EDDPolicy{}, errors.New("kyc: EDD policy requires a rule tree")
	}
	if err := triggers.Validate(); err != nil {
		return EDDPolicy{}, err
	}
	return EDDPolicy{triggers: triggers}, nil
}

// Trigger evaluates: an unverified player crossing ANY threshold triggers;
// level>=2 players trigger only on the high bands (the exact semantics are
// whatever the tree says — this function adds none).
func (p EDDPolicy) Trigger(level int, valueMinor int64) (bool, error) {
	facts := map[string]rules.Fact{
		"kyc_level":   rules.IntFact(int64(level)),
		"value_minor": rules.IntFact(valueMinor),
	}
	return rules.Evaluate(p.triggers, facts)
}
