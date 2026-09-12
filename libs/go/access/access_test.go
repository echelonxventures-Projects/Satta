package access_test

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/satta/platform/libs/go/access"
	"github.com/satta/platform/libs/go/rules"
)

const policyJSON = `[
	{"role":"support-agent","permission":"player.read"},
	{"role":"support-agent","permission":"player.write","condition":{"fact":"market","in":["mt","se"]}},
	{"role":"trader","permission":"market.*"},
	{"role":"trader","permission":"order.book","condition":{"fact":"kyc_level","gte_int":2}},
	{"role":"auditor","permission":"audit.read"}
]`

func policy(t *testing.T) *access.Policy {
	t.Helper()
	p, err := access.ParsePolicy(policyJSON)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return p
}

func TestRBACMatrix(t *testing.T) {
	p := policy(t)
	if err := p.Check("support-agent", "player.read", nil); err != nil {
		t.Fatalf("granted: %v", err)
	}
	if err := p.Check("support-agent", "player.delete", nil); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("unlisted permission: %v, want ErrDenied", err)
	}
}

func TestUnknownRoleDenies(t *testing.T) {
	// Deny-by-default is a TYPE of answer here: unbound principals get
	// ErrUnknownRole, never an allow, never a silent pass.
	if err := policy(t).Check("ghost", "anything", nil); !errors.Is(err, access.ErrUnknownRole) {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestABACConditionOnFacts(t *testing.T) {
	p := policy(t)
	if err := p.Check("support-agent", "player.write", map[string]rules.Fact{
		"market": rules.StringFact("mt"),
	}); err != nil {
		t.Fatalf("permitted market: %v", err)
	}
	if err := p.Check("support-agent", "player.write", map[string]rules.Fact{
		"market": rules.StringFact("us"),
	}); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("forbidden market: %v, want denial", err)
	}
	// Missing facts deny: "could not evaluate" is never "allowed".
	err := p.Check("support-agent", "player.write", nil)
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("missing fact: %v, want denial", err)
	}
	if !errors.Is(err, rules.ErrInvalidRule) {
		t.Fatalf("missing fact should also expose the evaluation error for operators: %v", err)
	}
}

func TestWildcardPermission(t *testing.T) {
	if err := policy(t).Check("trader", "market.close", nil); err != nil {
		t.Fatalf("wildcard grant: %v", err)
	}
}

func TestParseRejectsMalformedPolicy(t *testing.T) {
	cases := map[string]string{
		"empty role":    `[{"permission":"x"}]`,
		"bad json":      `{"nope":1}`,
		"bad condition": `[{"role":"r","permission":"p","condition":{"eq_int":5}}]`, // leaf without fact
	}
	for name, raw := range cases {
		if _, err := access.ParsePolicy(raw); err == nil {
			t.Errorf("%s: malformed policy accepted", name)
		}
	}
}

func TestAPIKeyHashDiscriminates(t *testing.T) {
	key := access.NewAPIKey()
	pepper := []byte("server-pepper")
	other := []byte("different-pepper")

	h := key.Hash(pepper)
	if !access.ConstantTimeEqual(h, key.Hash(pepper)) {
		t.Fatal("same key+pepper must hash identically")
	}
	if access.ConstantTimeEqual(h, key.Hash(other)) {
		t.Fatal("different pepper produced the same digest — storage leak would be usable")
	}
	second := access.NewAPIKey()
	if second.ID == key.ID || second.Secret == key.Secret {
		t.Fatal("two minted keys collided")
	}
	if _, err := hex.DecodeString(key.Secret); err != nil {
		t.Fatalf("secret not hex: %v", err)
	}
}

func TestAPIKeyShapeValidation(t *testing.T) {
	key := access.NewAPIKey()
	id, secret, err := access.ValidateAPIKeyShape(key.ID + "." + key.Secret)
	if err != nil {
		t.Fatalf("valid shape rejected: %v", err)
	}
	if id != key.ID || secret != key.Secret {
		t.Fatalf("roundtrip mismatch: %q %q", id, secret)
	}
	for _, bad := range []string{"", "noseparator", "a.b", "a." + "zz", "." + key.Secret, key.ID + "."} {
		if _, _, err := access.ValidateAPIKeyShape(bad); err == nil {
			t.Errorf("malformed key accepted: %q", bad)
		}
	}
}
