// Package golden holds the golden test vectors that pin money arithmetic and
// the evidence hash pre-image across implementations.
//
// ADR-0001 requires conformance to be checkable by any language that
// implements the platform contracts. Go's own tests prove the Go code is
// self-consistent; the golden corpus proves it matches a published,
// versioned set of input/output pairs that a Rust, Elixir or TypeScript
// implementation replays byte-for-byte. When two implementations disagree,
// the corpus is the referee — not whichever code shipped first.
//
// The corpus is append-only: vectors are added, never edited. A vector whose
// expected value needs to change is a new vector with a new ID, and the old
// one is marked deprecated with a reason, because a silently edited golden
// file is indistinguishable from one that was always wrong.
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Corpus is the parsed golden vector file.
type Corpus struct {
	// Version is the corpus format version. Breaking changes to the corpus
	// format bump this and regenerate consumers.
	Version int `json:"version"`

	Money      []MoneyVector      `json:"money"`
	Ratio      []RatioVector      `json:"ratio"`
	Allocation []AllocationVector `json:"allocation"`
	Preimage   []PreimageVector   `json:"preimage"`
	RecordHash []RecordHashVector `json:"record_hash"`
}

// MoneyVector pins one money operation. Error vectors carry a non-empty
// error field instead of a result.
type MoneyVector struct {
	ID     string `json:"id"`
	Op     string `json:"op"`     // add, sub, mul_int, neg, abs, parse, decimal
	Inputs []any  `json:"inputs"` // op-dependent; minor units unless the op says otherwise
	Result any    `json:"result"` // minor units, or a decimal string for parse/decimal
	Error  string `json:"error,omitempty"`
	Note   string `json:"note,omitempty"`
}

// RatioVector pins ratio construction, parsing and application.
type RatioVector struct {
	ID     string `json:"id"`
	Op     string `json:"op"` // parse_percent, parse_rounding, apply, div_int
	Inputs []any  `json:"inputs"`
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
	Note   string `json:"note,omitempty"`
}

// AllocationVector pins a full allocation waterfall, including the split
// remainder handling that double-entry payout code depends on.
type AllocationVector struct {
	ID      string  `json:"id"`
	Amount  int64   `json:"amount"`
	Weights []int64 `json:"weights"`
	Parts   []int64 `json:"parts"`
	Error   string  `json:"error,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// PreimageVector pins the canonical payload encoding: the exact bytes and
// their SHA-256. This is the format non-Go implementations must reproduce
// for payload_hash to agree.
type PreimageVector struct {
	ID       string         `json:"id"`
	Payload  map[string]any `json:"payload"`
	Encoding string         `json:"encoding_hex"`
	Sha256   string         `json:"sha256"`
	Note     string         `json:"note,omitempty"`
}

// RecordHashVector pins a full record hash: every pre-image field, the
// canonical payload hash, and the resulting record hash.
type RecordHashVector struct {
	ID         string         `json:"id"`
	Record     map[string]any `json:"record"`
	PayloadHex string         `json:"payload_encoding_hex"`
	RecordHash string         `json:"record_hash"`
	Note       string         `json:"note,omitempty"`
}

// Load reads and parses a corpus file. Numbers decode as json.Number rather
// than float64: the corpus pins integer payloads (money minor units among
// them), and a float64 that has been through JSON cannot distinguish 1250
// from 1250.0 — exactly the ambiguity SAFETY INVARIANT 1 forbids. The
// evidence encoder accepts json.Number for integers on purpose.
func Load(path string) (*Corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("golden: read corpus: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var corpus Corpus
	if err := dec.Decode(&corpus); err != nil {
		return nil, fmt.Errorf("golden: parse corpus: %w", err)
	}
	if corpus.Version != 1 {
		return nil, fmt.Errorf("golden: corpus version %d is not supported (want 1)", corpus.Version)
	}
	return &corpus, nil
}
