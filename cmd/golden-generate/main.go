// Command golden-generate produces the golden vector corpus from the current
// implementation. It exists so the corpus is generated, not hand-typed: a
// hand-typed corpus encodes what the author believed the code did, while a
// generated one encodes what it actually does.
//
// The generator is run once per corpus revision and its output committed. It
// is deliberately not part of CI — CI replays the committed corpus against
// the implementation; regeneration is a deliberate, reviewed act.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/golden"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

func main() {
	corpus := golden.Corpus{Version: 1}

	corpus.Money = moneyVectors()
	corpus.Ratio = ratioVectors()
	corpus.Allocation = allocationVectors()
	corpus.Preimage, corpus.RecordHash = hashVectors()

	out, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		failf("marshal corpus: %v", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile("libs/go/golden/testdata/corpus.json", out, 0o600); err != nil {
		failf("write corpus: %v", err)
	}
	fmt.Printf("corpus written: %d money, %d ratio, %d allocation, %d preimage, %d record hash\n",
		len(corpus.Money), len(corpus.Ratio), len(corpus.Allocation),
		len(corpus.Preimage), len(corpus.RecordHash))
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "golden-generate: "+format+"\n", args...)
	os.Exit(1)
}

func moneyVectors() []golden.MoneyVector {
	return []golden.MoneyVector{
		{ID: "money-add-basic", Op: "add", Inputs: []any{int64(1250), int64(-50)}, Result: int64(1200)},
		{ID: "money-add-overflow", Op: "add", Inputs: []any{"max_int64", int64(1)}, Error: "overflow",
			Note: "MaxInt64 + 1 must be detected, never wrapped"},
		{ID: "money-sub-underflow", Op: "sub", Inputs: []any{"min_int64", int64(1)}, Error: "overflow"},
		{ID: "money-mul-int", Op: "mul_int", Inputs: []any{int64(1250), int64(3)}, Result: int64(3750)},
		{ID: "money-mul-overflow", Op: "mul_int", Inputs: []any{"max_int64", int64(2)}, Error: "overflow"},
		{ID: "money-neg", Op: "neg", Inputs: []any{int64(-1250)}, Result: int64(1250)},
		{ID: "money-neg-minint", Op: "neg", Inputs: []any{"min_int64"}, Error: "overflow"},
		{ID: "money-abs", Op: "abs", Inputs: []any{int64(-1250)}, Result: int64(1250)},
		{ID: "money-parse-eur", Op: "parse", Inputs: []any{"12.34", "EUR", int64(2)}, Result: int64(1234)},
		{ID: "money-parse-jpy", Op: "parse", Inputs: []any{"1234", "JPY", int64(0)}, Result: int64(1234)},
		{ID: "money-parse-negative", Op: "parse", Inputs: []any{"-0.05", "EUR", int64(2)}, Result: int64(-5)},
		{ID: "money-parse-plus", Op: "parse", Inputs: []any{"+7.1", "EUR", int64(2)}, Result: int64(710)},
		{ID: "money-parse-dotfive", Op: "parse", Inputs: []any{".5", "EUR", int64(2)}, Result: int64(50)},
		{ID: "money-parse-too-precise", Op: "parse", Inputs: []any{"12.345", "EUR", int64(2)}, Error: "scale",
			Note: "input finer than the currency's scale is rejected, not silently rounded"},
		{ID: "money-parse-empty", Op: "parse", Inputs: []any{"", "EUR", int64(2)}, Error: "empty"},
		{ID: "money-decimal-eur", Op: "decimal", Inputs: []any{int64(1234), "EUR", int64(2)}, Result: "12.34"},
		{ID: "money-decimal-negative", Op: "decimal", Inputs: []any{int64(-5), "EUR", int64(2)}, Result: "-0.05"},
		{ID: "money-decimal-btc-satoshi", Op: "decimal", Inputs: []any{int64(1), "BTC", int64(8)}, Result: "0.00000001"},
		{ID: "money-decimal-jpy", Op: "decimal", Inputs: []any{int64(1234), "JPY", int64(0)}, Result: "1234"},
		{ID: "money-sum", Op: "sum", Inputs: []any{int64(1250), int64(-50), int64(800)}, Result: int64(2000)},
	}
}

func ratioVectors() []golden.RatioVector {
	return []golden.RatioVector{
		{ID: "ratio-parse-percent-int", Op: "parse_percent", Inputs: []any{"50"}, Result: "50/100",
			Note: "ParseRatioPercent takes a bare decimal: 50 means 50 percent"},
		{ID: "ratio-parse-percent-frac", Op: "parse_percent", Inputs: []any{"2.5"}, Result: "5/200",
			Note: "2.5 percent as an exact fraction; no float ever appears in the parse"},
		{ID: "ratio-parse-percent-fraction", Op: "parse_percent", Inputs: []any{"5/100"}, Result: "1/2000",
			Note: "big.Rat accepts fraction input; 5/100 as a percentage is 5/10000 = 1/2000"},
		{ID: "ratio-parse-percent-garbage", Op: "parse_percent", Inputs: []any{"five"}, Error: "exact decimal"},
		{ID: "ratio-parse-percent-zero-denominator", Op: "parse_percent", Inputs: []any{"5/0"}, Error: "not an exact decimal",
			Note: "big.Rat refuses a zero denominator before NewRatio sees it"},
		{ID: "ratio-apply-half-up", Op: "apply", Inputs: []any{int64(1001), "1/2", "half_up"}, Result: int64(501)},
		{ID: "ratio-apply-half-even", Op: "apply", Inputs: []any{int64(1000), "1/2", "half_even"}, Result: int64(500),
			Note: "banker's rounding: 500 is even"},
		{ID: "ratio-apply-half-even-odd", Op: "apply", Inputs: []any{int64(1002), "1/2", "half_even"}, Result: int64(501),
			Note: "1002/2 = 501 exactly, so no rounding occurs"},
		{ID: "ratio-apply-down", Op: "apply", Inputs: []any{int64(1001), "1/2", "down"}, Result: int64(500)},
		{ID: "ratio-apply-up", Op: "apply", Inputs: []any{int64(1001), "1/2", "up"}, Result: int64(501)},
		{ID: "ratio-apply-floor-negative", Op: "apply", Inputs: []any{int64(-1001), "1/2", "floor"}, Result: int64(-501)},
		{ID: "ratio-apply-ceil-negative", Op: "apply", Inputs: []any{int64(-1001), "1/2", "ceil"}, Result: int64(-500)},
		{ID: "ratio-apply-half-down", Op: "apply", Inputs: []any{int64(1001), "1/2", "half_down"}, Result: int64(500)},
		{ID: "ratio-apply-bonus-rate", Op: "apply", Inputs: []any{int64(1250), "5/100", "half_up"}, Result: int64(63),
			Note: "12.50 * 5% = 0.625 -> 0.63 half-up"},
		{ID: "ratio-div-int-inexact", Op: "div_int", Inputs: []any{int64(1000), int64(3), "down"}, Result: int64(333),
			Note: "inexact division rounds; Allocate is what preserves the total"},
		{ID: "ratio-rounding-name-roundtrip", Op: "parse_rounding", Inputs: []any{"half_even"}, Result: "half_even"},
	}
}

func allocationVectors() []golden.AllocationVector {
	return []golden.AllocationVector{
		{ID: "alloc-even", Amount: 1000, Weights: []int64{1, 1}, Parts: []int64{500, 500}},
		{ID: "alloc-weighted", Amount: 1000, Weights: []int64{1, 3}, Parts: []int64{250, 750}},
		{ID: "alloc-remainder-to-first", Amount: 1000, Weights: []int64{1, 1, 1}, Parts: []int64{334, 333, 333},
			Note: "1000/3: the waterfall gives the indivisible remainder to the first part deterministically"},
		{ID: "alloc-zero-weight", Amount: 1000, Weights: []int64{0, 1}, Parts: []int64{0, 1000}},
		{ID: "alloc-negative-amount", Amount: -1000, Weights: []int64{1, 1}, Parts: []int64{-500, -500}},
		{ID: "alloc-negative-weight", Amount: 1000, Weights: []int64{-1, 1}, Error: "non-negative"},
		{ID: "alloc-no-weights", Amount: 1000, Weights: []int64{}, Error: "at least one weight"},
		{ID: "alloc-maxint64", Amount: 9223372036854775807, Weights: []int64{1, 1},
			Parts: []int64{4611686018427387904, 4611686018427387903},
			Note:  "big.Int waterfall: MaxInt64 splits without overflow and the odd minor goes to the first part"},
	}
}

func hashVectors() (pre []golden.PreimageVector, rec []golden.RecordHashVector) {
	payloads := []struct {
		id      string
		payload map[string]any
		note    string
	}{
		{"pre-empty", map[string]any{}, "empty payload has a defined value, not a special case"},
		{"pre-scalars", map[string]any{"s": "value", "i": int64(42), "b": true, "n": nil}, ""},
		{"pre-money", map[string]any{"amount_minor_units": int64(1250), "currency_code": "EUR"}, ""},
		{"pre-nested", map[string]any{"nested": map[string]any{"deep": int64(9)}}, ""},
		{"pre-list", map[string]any{"tags": []any{"a", "b"}}, ""},
		{"pre-int64-extremes", map[string]any{"min": int64(-9223372036854775808), "max": int64(9223372036854775807)},
			"the extremes must hash identically in every language: two's-complement varints"},
	}
	for _, p := range payloads {
		enc, err := evidence.EncodePayload(p.payload)
		if err != nil {
			failf("%s: encode: %v", p.id, err)
		}
		sum := sha256.Sum256(enc)
		pre = append(pre, golden.PreimageVector{
			ID: p.id, Payload: p.payload, Encoding: hex.EncodeToString(enc),
			Sha256: hex.EncodeToString(sum[:]), Note: p.note,
		})
	}

	// One full record hash: the fields a real ledger.transfer.posted carries.
	base := evidence.Record{
		ChainID:        "tenant-a-money",
		Sequence:       1,
		Scope:          tenancy.Scope{TenantID: "tenant-a", JurisdictionCode: "mt", RegionCode: "eu-west"},
		OccurredAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		RecordedAt:     time.Date(2026, 8, 1, 12, 0, 0, 123000000, time.UTC),
		Category:       evidence.CategoryMoney,
		Action:         "ledger.transfer.posted",
		SubjectType:    "wallet",
		SubjectID:      "wallet-1",
		Actor:          identity.ServiceActor("ledger"),
		Payload:        map[string]any{"amount_minor_units": int64(1250), "currency_code": "EUR"},
		IdempotencyKey: "vec-1",
		RetentionClass: "financial-7y",
		CorrelationID:  "corr-vec-1",
	}
	payloadHash, err := evidence.HashPayload(base.Payload)
	if err != nil {
		failf("payload hash: %v", err)
	}
	base.PayloadHash = payloadHash
	base.PrevHash = evidence.GenesisHash
	recordHash, err := base.ComputeHash()
	if err != nil {
		failf("record hash: %v", err)
	}
	enc, err := evidence.EncodePayload(base.Payload)
	if err != nil {
		failf("encode: %v", err)
	}
	rec = append(rec, golden.RecordHashVector{
		ID:         "record-genesis-ledger-transfer",
		Record:     recordToMap(base),
		PayloadHex: hex.EncodeToString(enc),
		RecordHash: hex.EncodeToString(recordHash),
	})
	return pre, rec
}

func recordToMap(r evidence.Record) map[string]any {
	return map[string]any{
		"chain_id":          r.ChainID,
		"sequence":          r.Sequence,
		"tenant_id":         r.Scope.TenantID,
		"brand_id":          r.Scope.BrandID,
		"jurisdiction_code": r.Scope.JurisdictionCode,
		"region_code":       r.Scope.RegionCode,
		"occurred_at_utc":   r.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999"),
		"recorded_at_utc":   r.RecordedAt.UTC().Format("2006-01-02T15:04:05.999999"),
		"category":          string(r.Category),
		"action":            r.Action,
		"subject_type":      r.SubjectType,
		"subject_id":        r.SubjectID,
		"actor_type":        string(r.Actor.Type),
		"actor_id":          r.Actor.ID,
		"actor_display":     r.Actor.DisplayName,
		"actor_session":     r.Actor.SessionID,
		"actor_source_ip":   r.Actor.SourceIP,
		"actor_on_behalf":   r.Actor.OnBehalfOf,
		"payload_hash":      hex.EncodeToString(r.PayloadHash),
		"prev_hash":         hex.EncodeToString(r.PrevHash),
		"idempotency_key":   r.IdempotencyKey,
		"retention_class":   r.RetentionClass,
		"correlation_id":    r.CorrelationID,
		"trace_id":          r.TraceID,
	}
}
