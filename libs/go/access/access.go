// Package access is the authorization core of roadmap Task 6. Scope here is
// the part that must be a library the whole platform shares: the role
// matrix, its ABAC overlay, and API-key primitives. The transport halves
// (OIDC provider adapters, session stores, MFA flows) are service-level and
// land with the services that expose them, on top of these checks.
//
// SAFETY INVARIANT 3 (deny-by-default) is the shape of every call: an
// unknown role, an unparseable matrix or a missing condition context is a
// refusal, never an allow. A permission system's failure mode must be "the
// deployment broke visibly", because the alternative — "it quietly worked" —
// is a breach that reports no symptoms.
//
// The role→permission matrix is policy (gate 1): it arrives as parsed JSON
// (from the config engine in services, inline in tests). ABAC conditions
// reuse the rules engine, so a jurisdiction/segment/market predicate in an
// authorization decision is the same auditable data as one in a business
// rule — one evaluator, one audit story.
package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/satta/platform/libs/go/rules"
)

// Errors of the package.
var (
	// ErrDenied is the typed refusal: distinct from "unknown caller" and
	// from a system error so callers can tell a policy NO from a broken
	// deployment without parsing strings.
	ErrDenied = errors.New("access: denied")
	// ErrBadPolicy marks malformed matrix data (a deployment defect).
	ErrBadPolicy = errors.New("access: malformed policy")
	// ErrUnknownRole is the deny-by-default answer for an unbound principal.
	ErrUnknownRole = errors.New("access: unknown role")
)

// Grant is one matrix entry: a permission for a role, optionally narrowed
// by conditions evaluated against request facts.
type Grant struct {
	Role        string      `json:"role"`
	Permission  string      `json:"permission"`
	Condition   *rules.Node `json:"condition,omitempty"`
	Description string      `json:"description,omitempty"`
}

// Policy is the parsed role matrix.
type Policy struct {
	grants []Grant
	byRole map[string][]int // role -> grant indexes
}

// ParsePolicy compiles and validates the matrix at load: empty roles, empty
// permissions and malformed condition trees are rejected here, not at the
// first check that needs them.
func ParsePolicy(raw string) (*Policy, error) {
	var grants []Grant
	if err := json.Unmarshal([]byte(raw), &grants); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadPolicy, err)
	}
	p := &Policy{grants: grants, byRole: map[string][]int{}}
	for i, g := range grants {
		if g.Role == "" || g.Permission == "" {
			return nil, fmt.Errorf("%w: grant %d needs role and permission", ErrBadPolicy, i)
		}
		if g.Condition != nil {
			if err := g.Condition.Validate(); err != nil {
				return nil, fmt.Errorf("%w: grant %d condition: %w", ErrBadPolicy, i, err)
			}
		}
		p.byRole[g.Role] = append(p.byRole[g.Role], i)
	}
	return p, nil
}

// Check answers: may this role hold this permission, given the facts the
// request brought? Facts missing for a condition is a DENY (the condition
// could not be satisfied — and the alternative, allowing on missing data, is
// the bug every breach postmortem names).
func (p *Policy) Check(role, permission string, facts map[string]rules.Fact) error {
	idxs, ok := p.byRole[role]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownRole, role)
	}
	for _, i := range idxs {
		g := p.grants[i]
		if !permissionMatches(g.Permission, permission) {
			continue
		}
		if g.Condition == nil {
			return nil
		}
		ok, err := rules.Evaluate(g.Condition, facts)
		if err != nil {
			// Two wrapped sentinels, deliberately: callers match ErrDenied
			// (the answer), operators and tests can match ErrInvalidRule
			// (the broken deployment behind the answer). Both facts survive.
			return fmt.Errorf("%w: %w", ErrDenied, err)
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("%w: role %q permission %q", ErrDenied, role, permission)
}

// permissionMatches is the whole of the wildcard grammar, kept deliberately
// small: exact, "*" (everything), or "<ns>.*" (one namespace prefix). A
// richer glob is where authorization bugs go to become unreadable.
func permissionMatches(granted, wanted string) bool {
	switch {
	case granted == wanted, granted == "*":
		return true
	case strings.HasSuffix(granted, ".*"):
		prefix := strings.TrimSuffix(granted, "*") // keeps the trailing dot
		return strings.HasPrefix(wanted, prefix) && len(wanted) > len(prefix)
	default:
		return false
	}
}

// Roles lists the bound roles (audit/console surface, not authorization).
func (p *Policy) Roles() []string {
	out := make([]string, 0, len(p.byRole))
	for r := range p.byRole {
		out = append(out, r)
	}
	return out
}

// ---- API keys ---------------------------------------------------------------

// APIKey is an issued credential. The secret exists exactly once, in
// Presented; storage keeps only the derivation secret (pepper) and the
// identifier.
type APIKey struct {
	ID     string // short public identifier, in logs and authn events
	Secret string // caller-provided, hex; never logged
}

// NewAPIKey mints a key with crypto/rand secret bytes; 32 bytes of entropy
// is the platform floor — an API key that can be brute-forced is a backdoor
// with a support ticket attached. crypto/rand.Read is documented never to
// return on supported platforms (it panics internally on failure), so no
// error branch is needed — the same reasoning libs/go/correlation records.
func NewAPIKey() APIKey {
	var id [6]byte
	var secret [32]byte
	_, _ = rand.Read(id[:])
	_, _ = rand.Read(secret[:])
	return APIKey{
		ID:     strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(id[:])),
		Secret: hex.EncodeToString(secret[:]),
	}
}

// Hash returns the keyed digest to store: the pepper is a server-side secret
// so a database leak alone never yields usable credentials.
func (k APIKey) Hash(pepper []byte) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(k.ID))
	mac.Write([]byte{0})
	mac.Write([]byte(k.Secret))
	return mac.Sum(nil)
}

// ConstantTimeEqual compares two HMAC digests without leaking timing.
func ConstantTimeEqual(a, b []byte) bool {
	return hmac.Equal(a, b)
}

// ValidateAPIKeyShape checks a presented "id.secret" pair structurally
// (length/encoding only — the authorization decision is the hash compare
// against stored records, which lives in the auth service).
func ValidateAPIKeyShape(presented string) (id, secret string, err error) {
	idPart, secretPart, found := strings.Cut(presented, ".")
	if !found || idPart == "" || len(secretPart) != 64 {
		return "", "", fmt.Errorf("%w: api key malformed", ErrDenied)
	}
	if _, err := hex.DecodeString(secretPart); err != nil {
		return "", "", fmt.Errorf("%w: api key secret not hex", ErrDenied)
	}
	return idPart, secretPart, nil
}
