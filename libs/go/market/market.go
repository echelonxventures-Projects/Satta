// Package market is the jurisdiction→entity routing table and market
// activation gate (Master Plan D2: "everything configurable" — each tenant/
// brand binds to a licensed operator entity per jurisdiction; market
// activation = counsel sign-off + policy layer + entity binding, never code).
//
// The package encodes two hard rules:
//
//  1. Deny-by-default: a jurisdiction with no route is NOT served, and not
//     being served is not an error to hide — it is the answer. Serving a
//     jurisdiction without an explicit active route is the compliance
//     incident this table exists to make structurally impossible.
//  2. Activation is tri-state on purpose (active / staging / inactive):
//     stages 1-3 of ADR-0006 run real environments before money, and a
//     boolean would tempt someone to flip "false" to "true" without the
//     counsel-sign-off record an explicit activation demands.
package market

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Errors of the package.
var (
	// ErrNoRoute: the jurisdiction is simply not served by this tenant.
	ErrNoRoute = errors.New("market: no route for jurisdiction")
	// ErrInactive: a route exists but is not active (staging/disabled).
	ErrInactive = errors.New("market: jurisdiction not active")
	// ErrBadRoute marks malformed routing data.
	ErrBadRoute = errors.New("market: malformed routing table")
)

// State is a route's activation status.
type State string

const (
	StateActive   State = "active"
	StateStaging  State = "staging"
	StateInactive State = "inactive"
)

// Route binds a jurisdiction to the licensed entity that answers for it.
type Route struct {
	Jurisdiction string `json:"jurisdiction"`
	// EntityID names the licensed operator entity (e.g. "satta-mga-type1").
	EntityID string `json:"entity_id"`
	State    State  `json:"state"`
	// Verticals optionally narrows what may run in this market even when
	// active (e.g. a licence that covers casino but not sportsbook).
	Verticals []string `json:"verticals,omitempty"`
}

// Table is one tenant's routing configuration, loaded from the config engine
// under RoutingKey (the layering decides whose table applies; this type is
// the parsed result).
type Table struct {
	routes   map[string]Route
	order    []string
	vertical map[string]map[string]bool // jurisdiction -> vertical set (nil = all)
}

// RoutingKey is the config key holding the table as JSON ([Route, ...]).
const RoutingKey = "markets.routing"

// ParseTable compiles and validates routing data: jurisdiction codes must be
// non-empty and unique, entity IDs non-empty, states from the known set.
func ParseTable(raw string) (*Table, error) {
	var routes []Route
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadRoute, err)
	}
	t := &Table{routes: map[string]Route{}, vertical: map[string]map[string]bool{}}
	for _, r := range routes {
		if r.Jurisdiction == "" || r.EntityID == "" {
			return nil, fmt.Errorf("%w: route %q needs jurisdiction and entity_id", ErrBadRoute, r.Jurisdiction)
		}
		if _, dup := t.routes[r.Jurisdiction]; dup {
			return nil, fmt.Errorf("%w: duplicate jurisdiction %q", ErrBadRoute, r.Jurisdiction)
		}
		switch r.State {
		case StateActive, StateStaging, StateInactive:
		default:
			return nil, fmt.Errorf("%w: state %q on %q is unknown", ErrBadRoute, r.State, r.Jurisdiction)
		}
		t.routes[r.Jurisdiction] = r
		t.order = append(t.order, r.Jurisdiction)
		if len(r.Verticals) > 0 {
			set := map[string]bool{}
			for _, v := range r.Verticals {
				set[v] = true
			}
			t.vertical[r.Jurisdiction] = set
		}
	}
	return t, nil
}

// Resolve answers the question every money-path call must ask before serving
// a jurisdiction: which entity is responsible, and is it allowed. vertical
// may be "" to check the market as a whole.
func (t *Table) Resolve(jurisdiction, vertical string) (Route, error) {
	r, ok := t.routes[jurisdiction]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrNoRoute, jurisdiction)
	}
	if r.State != StateActive {
		return r, fmt.Errorf("%w: %s is %s", ErrInactive, jurisdiction, r.State)
	}
	if vertical != "" {
		if set, limited := t.vertical[jurisdiction]; limited && !set[vertical] {
			return r, fmt.Errorf("%w: %s does not license vertical %q", ErrNoRoute, jurisdiction, vertical)
		}
	}
	return r, nil
}

// Staging reports whether a jurisdiction is explicitly in a pre-production
// stage (play-money/social/sweepstakes under ADR-0006's ladder). Callers use
// this to route demo traffic without pretending it is active.
func (t *Table) Staging(jurisdiction string) bool {
	r, ok := t.routes[jurisdiction]
	return ok && r.State == StateStaging
}
