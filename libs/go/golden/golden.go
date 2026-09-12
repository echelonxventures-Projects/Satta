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
	"strconv"
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
//
// Every int64 crosses the wire as a JSON string: values beyond 2^53 cannot
// survive a JavaScript JSON.parse through the IEEE-754 double a bare JSON
// number decodes to, and the max-int64 vector exists precisely to catch that
// class of bug. This mirrors the proto3 JSON mapping, where int64 is a string
// for the same reason.
type AllocationVector struct {
	ID      string      `json:"id"`
	Amount  int64       `json:"amount,string"`
	Weights BigIntSlice `json:"weights"`
	Parts   BigIntSlice `json:"parts,omitempty"`
	Error   string      `json:"error,omitempty"`
	Note    string      `json:"note,omitempty"`
}

// BigIntSlice is a []int64 whose elements marshal as JSON strings.
type BigIntSlice []int64

// MarshalJSON renders each element as a string, []int64{1,-2} to ["1","-2"].
// A nil slice renders as null, not as an empty array, so "no parts expected"
// and "zero parts" stay distinguishable in the corpus.
func (s BigIntSlice) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, v := range s {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "%q", strconv.FormatInt(v, 10))
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// UnmarshalJSON accepts string elements and rejects bare numbers: a consumer
// that emits numbers here has silently reintroduced the precision hazard.
func (s *BigIntSlice) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}
	var strs []string
	if err := json.Unmarshal(data, &strs); err != nil {
		return fmt.Errorf("golden: allocation int64s must be JSON strings: %w", err)
	}
	out := make(BigIntSlice, 0, len(strs))
	for _, str := range strs {
		v, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			return fmt.Errorf("golden: bad int64 %q: %w", str, err)
		}
		out = append(out, v)
	}
	*s = out
	return nil
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
