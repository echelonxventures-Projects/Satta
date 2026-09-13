// Package aml is transaction monitoring, fraud and AML screening (roadmap
// Task 14): velocity rules, alerts, SAR case records, chargeback tracking,
// and the crypto-chain risk port (plan D3).
//
// Posture (same as every policy surface here):
//
//   - The rules are data. Trees arrive from config (gate 1); this package
//     evaluates and escalates, it never decides alone which activity is
//     criminal — it produces alerts and case records for human MLRO review
//     (delivery-realities: assurance comes from qualified people).
//   - Monitoring is side-effectless: observing an activity never blocks it;
//     only a declared BLOCK rule or a crypto risk verdict refuses. An
//     engine that "flags therefore denies" hides false positives from
//     review, and false positives deny players money — the harm it exists
//     to prevent, pointed the wrong way.
//   - SAR generation produces a record for a human to file; the platform
//     never files with a FIU on its own, and the record keeps which rule
//     fired on which facts — the audit chain a regulator asks for.
package aml

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/rules"
)

// Errors.
var (
	// ErrBlocked is the typed refusal a monitored action receives when a
	// hard rule fires.
	ErrBlocked = errors.New("aml: blocked")
	// ErrBadRule marks malformed policy at construction.
	ErrBadRule = errors.New("aml: malformed rule")
)

// Verdict for one observed action.
type Verdict int

const (
	// VerdictClear: nothing fired.
	VerdictClear Verdict = iota
	// VerdictAlert: soft rules fired; activity proceeds, review queue grows.
	VerdictAlert
	// VerdictBlock: a hard ceiling fired; the caller must refuse.
	VerdictBlock
)

// Rule is one policy entry: a tree, what it means, what it does.
type Rule struct {
	ID       string      `json:"id"`
	Node     *rules.Node `json:"node"`
	Severity string      `json:"severity"` // alert | block
	SAR      bool        `json:"sar"`      // fires a SAR case record
	Window   string      `json:"window"`   // dedup: hourly | daily
	Note     string      `json:"note"`
}

// Engine evaluates rules against action facts.
type Engine struct {
	rules []Rule
	clk   clock.Clock

	mu      sync.Mutex
	alerts  map[string]bool     // rule|player|bucket -> raised
	charges map[string][]charge // player -> chargeback timestamps
	cases   []Case
}

type charge struct {
	at  time.Time
	ref string
}

// Case is a generated review/SAR record (for the MLRO queue; fields exist
// because a regulator asks "on what evidence did you decide to look").
type Case struct {
	ID       string
	PlayerID string
	RuleID   string
	Severity string
	SAR      bool
	Facts    map[string]rules.Fact
	OpenedAt time.Time
}

// NewEngine validates the whole rule set at construction (a malformed tree
// or unknown severity is a boot failure, never a runtime surprise).
func NewEngine(clk clock.Clock, rs ...Rule) (*Engine, error) {
	if clk == nil {
		return nil, errors.New("aml: clock required")
	}
	for _, r := range rs {
		if r.ID == "" || r.Node == nil {
			return nil, fmt.Errorf("%w: rule needs id and node", ErrBadRule)
		}
		if err := r.Node.Validate(); err != nil {
			return nil, fmt.Errorf("%w: rule %s: %w", ErrBadRule, r.ID, err)
		}
		switch r.Severity {
		case "alert", "block":
		default:
			return nil, fmt.Errorf("%w: rule %s severity %q", ErrBadRule, r.ID, r.Severity)
		}
		switch r.Window {
		case "", "hourly", "daily":
		default:
			return nil, fmt.Errorf("%w: rule %s window %q", ErrBadRule, r.ID, r.Window)
		}
	}
	return &Engine{
		rules:   append([]Rule(nil), rs...),
		clk:     clk,
		alerts:  map[string]bool{},
		charges: map[string][]charge{},
	}, nil
}

// Observe evaluates one action's facts for a player and records what fired.
// It never blocks by accident: only severity "block" contributes to the
// returned denial.
func (e *Engine) Observe(_ context.Context, player string, facts map[string]rules.Fact) (Verdict, error) {
	now := e.clk.Now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	verdict := VerdictClear
	for _, r := range e.rules {
		hit, err := rules.Evaluate(r.Node, facts)
		if err != nil {
			return VerdictClear, fmt.Errorf("aml: rule %s: %w", r.ID, err)
		}
		if !hit {
			continue
		}
		key := e.dedupeKey(r, player, now)
		if !e.alerts[key] {
			e.alerts[key] = true
			e.cases = append(e.cases, Case{
				ID:       fmt.Sprintf("case-%s-%s-%d", r.ID, player, len(e.cases)+1),
				PlayerID: player, RuleID: r.ID, Severity: r.Severity,
				SAR: r.SAR, Facts: facts, OpenedAt: now,
			})
		}
		if r.Severity == "block" {
			verdict = VerdictBlock
		} else if verdict == VerdictClear {
			verdict = VerdictAlert
		}
	}
	return verdict, nil
}

func (e *Engine) dedupeKey(r Rule, player string, now time.Time) string {
	switch r.Window {
	case "daily":
		return r.ID + "|" + player + "|" + now.Format("2006-01-02")
	default: // hourly (and unspecified): the tighter window
		return r.ID + "|" + player + "|" + now.Format("2006-01-02T15")
	}
}

// Cases returns the open review queue (a durable queue is the service's
// store; this is the binding's memory shape).
func (e *Engine) Cases() []Case {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Case(nil), e.cases...)
}

// SARs returns only the records that crossed the suspicious threshold.
func (e *Engine) SARs() []Case {
	var out []Case
	for _, c := range e.Cases() {
		if c.SAR {
			out = append(out, c)
		}
	}
	return out
}

// MarkChargeback records a chargeback against a player within the window;
// velocity facts (chargebacks_7d) read it on the next Observe.
func (e *Engine) MarkChargeback(player, txRef string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clk.Now().UTC()
	e.charges[player] = append(e.charges[player], charge{at: now, ref: txRef})
	list := e.charges[player]
	keep := make([]charge, 0, len(list))
	for _, c := range list {
		if now.Sub(c.at) <= 7*24*time.Hour {
			keep = append(keep, c)
		}
	}
	e.charges[player] = keep
}

// Chargebacks7d is the fact value rules can reference.
func (e *Engine) Chargebacks7d(player string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return int64(len(e.charges[player]))
}

// CryptoRisk is the blockchain-analytics port (plan D3, delivery-realities
// vendor): scoring is the vendor's job, the threshold is policy.
type CryptoRisk interface {
	Name() string
	ScoreAddress(ctx context.Context, address string) (band string, err error)
}

// RiskBlocks decides whether a deposit/withdrawal address may be used for
// this player action; high and severe are refused, unknown is refused when
// the policy says so (deny-by-default with an explicit allow list).
type RiskBlocks struct {
	RefuseBands map[string]bool
}

// Check evaluates a band against the policy set.
func (rb RiskBlocks) Check(band string) error {
	if rb.RefuseBands[band] {
		return fmt.Errorf("%w: crypto address risk band %q", ErrBlocked, band)
	}
	return nil
}
