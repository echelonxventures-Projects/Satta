package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/satta/platform/libs/go/tenancy"
)

// FileResolver is a Resolver backed by a YAML document of configuration layers.
// It is the local-development and contract-test implementation of the port; the
// served configuration engine implements the same interface.
//
// It is safe for concurrent use. Reload swaps the layer set atomically so that a
// running service picks up an edited file without a restart.
type FileResolver struct {
	path string

	mu      sync.RWMutex
	layers  []layer
	version string
}

// layer is one scoped block of key/value pairs.
type layer struct {
	Scope  scopeDoc          `yaml:"scope"`
	Values map[string]string `yaml:"values"`
}

// scopeDoc mirrors tenancy.Scope with snake_case YAML keys.
type scopeDoc struct {
	TenantID         string `yaml:"tenant_id"`
	BrandID          string `yaml:"brand_id"`
	JurisdictionCode string `yaml:"jurisdiction_code"`
	RegionCode       string `yaml:"region_code"`
}

func (s scopeDoc) toScope() tenancy.Scope {
	return tenancy.Scope{
		TenantID:         s.TenantID,
		BrandID:          s.BrandID,
		JurisdictionCode: s.JurisdictionCode,
		RegionCode:       s.RegionCode,
	}
}

type document struct {
	Layers []layer `yaml:"layers"`
}

// NewFileResolver loads configuration layers from path.
func NewFileResolver(path string) (*FileResolver, error) {
	r := &FileResolver{path: path}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// NewStaticResolver builds an in-memory resolver from explicit layers. Tests use
// it to state the exact policy under test rather than depending on a seed file.
func NewStaticResolver(version string, layers ...Layer) (*FileResolver, error) {
	internal := make([]layer, 0, len(layers))
	for _, l := range layers {
		if err := l.Scope.Validate(); err != nil {
			return nil, err
		}
		internal = append(internal, layer{
			Scope: scopeDoc{
				TenantID:         l.Scope.TenantID,
				BrandID:          l.Scope.BrandID,
				JurisdictionCode: l.Scope.JurisdictionCode,
				RegionCode:       l.Scope.RegionCode,
			},
			Values: l.Values,
		})
	}
	return &FileResolver{layers: internal, version: version}, nil
}

// Layer is the exported form of a configuration layer, used by NewStaticResolver.
type Layer struct {
	Scope  tenancy.Scope
	Values map[string]string
}

// Reload re-reads the backing file.
func (r *FileResolver) Reload() error {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", r.path, err)
	}
	var doc document
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("config: parse %s: %w", r.path, err)
	}
	for i, l := range doc.Layers {
		if err := l.Scope.toScope().Validate(); err != nil {
			return fmt.Errorf("config: %s layer %d: %w", r.path, i, err)
		}
	}

	// The version is the content hash. Deriving it from content rather than from
	// a hand-maintained field means a policy decision recorded against a version
	// can always be replayed against exactly the configuration that produced it.
	sum := sha256.Sum256(raw)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.layers = doc.Layers
	r.version = hex.EncodeToString(sum[:8])
	return nil
}

// Version implements Resolver.
func (r *FileResolver) Version(context.Context) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version, nil
}

// Resolve implements Resolver. Among the layers whose scope covers the requested
// scope and that define the key, the most specific wins.
//
// Two layers of equal specificity defining the same key is reported as an error
// rather than resolved by file order. Silent order-dependence in policy
// configuration is how a jurisdiction override gets shadowed by an unrelated
// edit, and it is not detectable after the fact.
func (r *FileResolver) Resolve(_ context.Context, scope tenancy.Scope, key string) (Value, error) {
	if err := scope.Validate(); err != nil {
		return Value{}, err
	}

	r.mu.RLock()
	layers, version := r.layers, r.version
	r.mu.RUnlock()

	type candidate struct {
		raw         string
		scope       tenancy.Scope
		specificity int
	}
	var candidates []candidate
	for _, l := range layers {
		layerScope := l.Scope.toScope()
		if !layerScope.Covers(scope) {
			continue
		}
		raw, ok := l.Values[key]
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{
			raw:         raw,
			scope:       layerScope,
			specificity: layerScope.Specificity(),
		})
	}
	if len(candidates) == 0 {
		return Value{}, fmt.Errorf("%w: key=%q scope=%s", ErrNotFound, key, scope.String())
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].specificity > candidates[j].specificity
	})
	if len(candidates) > 1 && candidates[0].specificity == candidates[1].specificity {
		return Value{}, fmt.Errorf(
			"config: key %q is defined ambiguously for scope %s by equally specific layers %s and %s",
			key, scope.String(), candidates[0].scope.String(), candidates[1].scope.String())
	}
	return NewValue(key, candidates[0].raw, candidates[0].scope, version), nil
}

// Keys returns every key defined by any layer. Used by the configuration
// coverage check that asserts a service's required keys are all present before
// it accepts traffic.
func (r *FileResolver) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, l := range r.layers {
		for k := range l.Values {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
