// Package catalog is the product/SKU registry and the entitlement gate
// (roadmap Task 3, Definition of Done gate 9: "capability registered as a
// product, with an entitlement check enforced before use — unentitled
// tenants get a clear denial, not a crash").
//
// Two halves, deliberately separate:
//
//   - The registry — what products, SKUs, capabilities and meters EXIST — is
//     build-time data compiled with the platform. Inventing a product code at
//     runtime would invent a metering and billing subject that no one
//     approved.
//   - Entitlements — which tenant/scope may use which product/SKU/capability,
//     and within what jurisdiction — are policy, resolved through the config
//     engine (gate 1), per layer, deny-by-default: no entitlement means no
//     access, never "no config means everything".
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Errors returned by this package.
var (
	// ErrUnknownProduct / ErrUnknownSKU / ErrUnknownCapability signal
	// registry gaps: asking about something that does not exist. Distinct
	// from ErrNotEntitled, which is a legitimate business answer.
	ErrUnknownProduct    = errors.New("catalog: unknown product")
	ErrUnknownSKU        = errors.New("catalog: unknown SKU")
	ErrUnknownCapability = errors.New("catalog: unknown capability")
	// ErrNotEntitled is the clear denial gate 9 demands.
	ErrNotEntitled = errors.New("catalog: not entitled")
	// ErrBadRegistry marks malformed registry or entitlement data.
	ErrBadRegistry = errors.New("catalog: malformed data")
)

// Meter is one billable usage meter inside a SKU. The metering SDK's
// product_code/meter_code/unit fields must name registry entries — this is
// the join that makes usage billing meaningful (DoD gate 10).
type Meter struct {
	Code       string   `json:"code"`
	Unit       string   `json:"unit"`
	Dimensions []string `json:"dimensions,omitempty"`
}

// SKU is a licensable variant of a product: a capability bundle plus the
// meters its usage is recorded against.
type SKU struct {
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Meters       []Meter  `json:"meters"`
}

// Product is a top-level sellable thing ("sportsbook", "poker", "wallet").
type Product struct {
	Code string `json:"code"`
	Name string `json:"name"`
	SKUs []SKU  `json:"skus"`
}

// Registry holds every known product. Validated once at load.
type Registry struct {
	products []Product
	byCode   map[string]Product
}

// NewRegistry compiles a product list, rejecting duplicates and empty
// capability/meter definitions before the first request can trip over them.
func NewRegistry(products []Product) (*Registry, error) {
	r := &Registry{products: append([]Product(nil), products...), byCode: map[string]Product{}}
	seenMeter := map[string]string{}
	for _, p := range products {
		if p.Code == "" || p.Name == "" {
			return nil, fmt.Errorf("%w: product needs code and name", ErrBadRegistry)
		}
		if _, dup := r.byCode[p.Code]; dup {
			return nil, fmt.Errorf("%w: duplicate product %q", ErrBadRegistry, p.Code)
		}
		if len(p.SKUs) == 0 {
			return nil, fmt.Errorf("%w: product %q has no SKUs", ErrBadRegistry, p.Code)
		}
		seenSKU := map[string]bool{}
		for _, s := range p.SKUs {
			if s.Code == "" || seenSKU[s.Code] {
				return nil, fmt.Errorf("%w: duplicate or empty SKU in %q", ErrBadRegistry, p.Code)
			}
			seenSKU[s.Code] = true
			if len(s.Capabilities) == 0 {
				return nil, fmt.Errorf("%w: SKU %s/%s has no capabilities — an empty entitlement is always-denied clutter at best, never-granted-by-accident at worst", ErrBadRegistry, p.Code, s.Code)
			}
			seenCap := map[string]bool{}
			for _, c := range s.Capabilities {
				if c == "" || seenCap[c] {
					return nil, fmt.Errorf("%w: duplicate/empty capability in SKU %s/%s", ErrBadRegistry, p.Code, s.Code)
				}
				seenCap[c] = true
			}
			for _, m := range s.Meters {
				if m.Code == "" || m.Unit == "" {
					return nil, fmt.Errorf("%w: meter %s/%s/%s needs code and unit", ErrBadRegistry, p.Code, s.Code, m.Code)
				}
				key := p.Code + "/" + m.Code
				if prev, dup := seenMeter[key]; dup {
					return nil, fmt.Errorf("%w: meter %q in %q already declared by SKU %q", ErrBadRegistry, key, p.Code, prev)
				}
				seenMeter[key] = s.Code
			}
		}
		r.byCode[p.Code] = p
	}
	return r, nil
}

// Product returns a registered product.
func (r *Registry) Product(code string) (Product, error) {
	p, ok := r.byCode[code]
	if !ok {
		return Product{}, fmt.Errorf("%w: %s", ErrUnknownProduct, code)
	}
	return p, nil
}

// SKU returns a registered SKU within a product.
func (r *Registry) SKU(productCode, skuCode string) (SKU, error) {
	p, err := r.Product(productCode)
	if err != nil {
		return SKU{}, err
	}
	for _, s := range p.SKUs {
		if s.Code == skuCode {
			return s, nil
		}
	}
	return SKU{}, fmt.Errorf("%w: %s/%s", ErrUnknownSKU, productCode, skuCode)
}

// ValidateUsage checks metering coordinates against the registry: a usage
// event naming an unregistered product/meter cannot be billed correctly and
// must be rejected at the door.
func (r *Registry) ValidateUsage(productCode, meterCode string) error {
	p, err := r.Product(productCode)
	if err != nil {
		return err
	}
	for _, s := range p.SKUs {
		for _, m := range s.Meters {
			if m.Code == meterCode {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrBadRegistry, productCode, meterCode)
}

// Entitlement is one tenant's permission grant. Capability must be one of the
// SKU's registered capabilities; Jurisdictions bounds where the grant applies
// (empty = the resolver's scope was already jurisdiction-checked upstream, and
// the explicit form is always preferred).
type Entitlement struct {
	ProductCode   string   `json:"product_code"`
	SKUCode       string   `json:"sku_code"`
	Capability    string   `json:"capability"`
	Jurisdictions []string `json:"jurisdictions,omitempty"`
}

// Entitlements resolve from a config value (key "catalog.entitlements", JSON)
// through the layering engine: a brand layer may add grants the tenant layer
// lacks, and a narrower layer overrides the broader one wholesale —
// merge-across-layers invites a jurisdiction grant accidentally surviving a
// narrower "off" switch.
type Entitlements struct {
	reg   *Registry
	items []Entitlement
}

// ParseEntitlements decodes and cross-checks entitlement data against the
// registry: an entitlement naming a capability that does not exist is a typo
// waiting to deny someone real money, so it fails loudly at load.
func ParseEntitlements(reg *Registry, raw string) (*Entitlements, error) {
	var items []Entitlement
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("%w: entitlements JSON: %w", ErrBadRegistry, err)
	}
	for _, e := range items {
		sku, err := reg.SKU(e.ProductCode, e.SKUCode)
		if err != nil {
			return nil, err
		}
		found := false
		for _, c := range sku.Capabilities {
			if c == e.Capability {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s/%s has no capability %q", ErrBadRegistry, e.ProductCode, e.SKUCode, e.Capability)
		}
	}
	return &Entitlements{reg: reg, items: items}, nil
}

// EntitleKey is the config key policy authors define entitlements under;
// services load it through the config engine and hand the JSON to
// ParseEntitlements (registry data is build-time, grants are policy).
const EntitleKey = "catalog.entitlements"

// Check enforces gate 9: before any capability use, an unentitled tenant
// gets ErrNotEntitled — clear, typed, and not a crash.
func (e *Entitlements) Check(productCode, capability, jurisdiction string) error {
	for _, item := range e.items {
		if item.ProductCode != productCode || item.Capability != capability {
			continue
		}
		if len(item.Jurisdictions) == 0 || contains(item.Jurisdictions, jurisdiction) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s capability %q in %q", ErrNotEntitled, productCode, capability, jurisdiction)
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
