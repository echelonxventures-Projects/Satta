package golden_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/evidence"
	"github.com/satta/platform/libs/go/golden"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

const corpusPath = "testdata/corpus.json"

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

// TestGoldenMoney replays every money vector. This is the cross-implementation
// contract: a Rust or TypeScript money implementation replays the same corpus
// file and must produce the same results — overflow detection included, not
// just the happy paths.
func TestGoldenMoney(t *testing.T) {
	corpus := loadCorpus(t)
	for _, v := range corpus.Money {
		t.Run(v.ID, func(t *testing.T) {
			switch v.Op {
			case "add", "sub", "mul_int", "neg", "abs", "sum":
				replayArith(t, v)
			case "parse":
				replayParse(t, v)
			case "decimal":
				replayDecimal(t, v)
			default:
				t.Fatalf("unknown op %q in vector %q — corpus and replay disagree", v.Op, v.ID)
			}
		})
	}
}

func replayArith(t *testing.T, v golden.MoneyVector) {
	t.Helper()
	amounts := make([]money.Amount, 0, len(v.Inputs))
	for _, in := range v.Inputs {
		amounts = append(amounts, money.MustNew(inputInt(t, in), eur))
	}

	var got money.Amount
	var opErr error
	switch v.Op {
	case "add":
		got, opErr = amounts[0].Add(amounts[1])
	case "sub":
		got, opErr = amounts[0].Sub(amounts[1])
	case "mul_int":
		got, opErr = amounts[0].MulInt(inputInt(t, v.Inputs[1]))
	case "neg":
		got, opErr = amounts[0].Neg()
	case "abs":
		got, opErr = amounts[0].Abs()
	case "sum":
		got, opErr = money.Sum(amounts...)
	}
	gotMinor := wantResult(t, v.Error, opErr, got.Minor())
	if v.Error != "" {
		return // error expectation already asserted by wantResult
	}
	if gotMinor != expectedMinor(t, v.Result) {
		t.Fatalf("result = %d, want %v", gotMinor, v.Result)
	}
}

// wantResult reconciles an operation error with the vector's expectation:
// vectors with an Error field must fail with that keyword in the message, and
// vectors without must succeed.
func wantResult(t *testing.T, wantErr string, err error, got int64) int64 {
	t.Helper()
	if wantErr != "" {
		if err == nil {
			t.Fatalf("expected error %q, got result %d", wantErr, got)
		}
		if !contains(err.Error(), wantErr) {
			t.Fatalf("error %q does not match expected keyword %q", err.Error(), wantErr)
		}
		return 0
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return got
}

func replayParse(t *testing.T, v golden.MoneyVector) {
	t.Helper()
	s := asString(t, v.Inputs[0])
	cur := money.Currency{Code: "EUR", Scale: moneyScale(t, inputInt(t, v.Inputs[2])), Kind: money.KindFiat}
	amount, err := money.Parse(s, cur)
	if v.Error != "" {
		wantResult(t, v.Error, err, 0)
		return
	}
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	if amount.Minor() != expectedMinor(t, v.Result) {
		t.Fatalf("parse %q = %d, want %v", s, amount.Minor(), v.Result)
	}
}

func replayDecimal(t *testing.T, v golden.MoneyVector) {
	t.Helper()
	cur := money.Currency{Code: "EUR", Scale: moneyScale(t, inputInt(t, v.Inputs[2])), Kind: money.KindFiat}
	amount := money.MustNew(inputInt(t, v.Inputs[0]), cur)
	want := asString(t, v.Result)
	if amount.Decimal() != want {
		t.Fatalf("decimal = %q, want %q", amount.Decimal(), want)
	}
}

// TestGoldenRatio replays ratio construction, percent parsing and the rounding
// modes.
func TestGoldenRatio(t *testing.T) {
	corpus := loadCorpus(t)
	for _, v := range corpus.Ratio {
		t.Run(v.ID, func(t *testing.T) {
			switch v.Op {
			case "parse_percent":
				s := asString(t, v.Inputs[0])
				ratio, err := money.ParseRatioPercent(s)
				if v.Error != "" {
					wantResult(t, v.Error, err, 0)
					return
				}
				if err != nil {
					t.Fatalf("parse %q: %v", s, err)
				}
				if want := asString(t, v.Result); ratio.String() != want {
					t.Fatalf("ratio = %s, want %s", ratio.String(), want)
				}
			case "parse_rounding":
				mode, err := money.ParseRoundingMode(asString(t, v.Inputs[0]))
				if err != nil {
					t.Fatalf("parse rounding: %v", err)
				}
				if want := asString(t, v.Result); mode.String() != want {
					t.Fatalf("rounding = %s, want %s", mode.String(), want)
				}
			case "apply":
				amount := money.MustNew(inputInt(t, v.Inputs[0]), eur)
				ratio := mustRatio(t, asString(t, v.Inputs[1]))
				mode := mustRounding(t, asString(t, v.Inputs[2]))
				got, err := amount.ApplyRatio(ratio, mode)
				if err != nil {
					t.Fatalf("apply: %v", err)
				}
				if got.Minor() != expectedMinor(t, v.Result) {
					t.Fatalf("apply = %d, want %v", got.Minor(), v.Result)
				}
			case "div_int":
				amount := money.MustNew(inputInt(t, v.Inputs[0]), eur)
				mode := mustRounding(t, asString(t, v.Inputs[2]))
				got, err := amount.DivInt(inputInt(t, v.Inputs[1]), mode)
				if err != nil {
					t.Fatalf("div: %v", err)
				}
				if got.Minor() != expectedMinor(t, v.Result) {
					t.Fatalf("div = %d, want %v", got.Minor(), v.Result)
				}
			default:
				t.Fatalf("unknown ratio op %q", v.Op)
			}
		})
	}
}

// TestGoldenAllocation replays the allocation waterfall, the payout code's
// most load-bearing primitive.
func TestGoldenAllocation(t *testing.T) {
	corpus := loadCorpus(t)
	for _, v := range corpus.Allocation {
		t.Run(v.ID, func(t *testing.T) {
			amount := money.MustNew(v.Amount, eur)
			parts, err := amount.Allocate(v.Weights)
			if v.Error != "" {
				wantResult(t, v.Error, err, 0)
				return
			}
			if err != nil {
				t.Fatalf("allocate: %v", err)
			}
			if len(parts) != len(v.Parts) {
				t.Fatalf("got %d parts, want %d", len(parts), len(v.Parts))
			}
			for i := range parts {
				if parts[i].Minor() != v.Parts[i] {
					t.Fatalf("part[%d] = %d, want %d", i, parts[i].Minor(), v.Parts[i])
				}
			}
			// The invariant the corpus exists for: parts always sum to the
			// original amount, exactly.
			var sum int64
			for _, p := range parts {
				sum += p.Minor()
			}
			if sum != v.Amount {
				t.Fatalf("parts sum to %d, want %d — allocation is not value-preserving", sum, v.Amount)
			}
		})
	}
}

// TestGoldenPreimage replays the canonical payload encoding and its hash.
// This is what a non-Go implementation reproduces for payload_hash to agree.
func TestGoldenPreimage(t *testing.T) {
	corpus := loadCorpus(t)
	for _, v := range corpus.Preimage {
		t.Run(v.ID, func(t *testing.T) {
			encoded, err := evidence.EncodePayload(v.Payload)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if hex.EncodeToString(encoded) != v.Encoding {
				t.Fatalf("encoding = %x, want %s", encoded, v.Encoding)
			}
			hash, err := evidence.HashPayload(v.Payload)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if hex.EncodeToString(hash) != v.Sha256 {
				t.Fatalf("sha256 = %x, want %s", hash, v.Sha256)
			}
			decoded, err := evidence.DecodePayload(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			reencoded, err := evidence.EncodePayload(decoded)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if !bytes.Equal(reencoded, encoded) {
				t.Fatal("decode/encode round-trip is not byte-stable")
			}
		})
	}
}

// TestGoldenRecordHash replays the full record-hash pre-image.
func TestGoldenRecordHash(t *testing.T) {
	corpus := loadCorpus(t)
	for _, v := range corpus.RecordHash {
		t.Run(v.ID, func(t *testing.T) {
			record := recordFromVector(t, v)
			got, err := record.ComputeHash()
			if err != nil {
				t.Fatalf("compute: %v", err)
			}
			if hex.EncodeToString(got) != v.RecordHash {
				t.Fatalf("record hash = %x, want %s", got, v.RecordHash)
			}
		})
	}
}

func recordFromVector(t *testing.T, v golden.RecordHashVector) evidence.Record {
	t.Helper()
	fields := v.Record
	occurred, err := timeParseUTC(asString(t, fields["occurred_at_utc"]))
	if err != nil {
		t.Fatalf("occurred_at: %v", err)
	}
	recorded, err := timeParseUTC(asString(t, fields["recorded_at_utc"]))
	if err != nil {
		t.Fatalf("recorded_at: %v", err)
	}
	payloadHash, err := hex.DecodeString(asString(t, fields["payload_hash"]))
	if err != nil {
		t.Fatalf("payload_hash hex: %v", err)
	}
	prevHash, err := hex.DecodeString(asString(t, fields["prev_hash"]))
	if err != nil {
		t.Fatalf("prev_hash hex: %v", err)
	}
	return evidence.Record{
		ChainID:  asString(t, fields["chain_id"]),
		Sequence: uint64(expectedMinor(t, fields["sequence"])),
		Scope: tenancy.Scope{
			TenantID:         asString(t, fields["tenant_id"]),
			BrandID:          strOrEmpty(fields["brand_id"]),
			JurisdictionCode: asString(t, fields["jurisdiction_code"]),
			RegionCode:       asString(t, fields["region_code"]),
		},
		OccurredAt:  occurred,
		RecordedAt:  recorded,
		Category:    evidence.Category(asString(t, fields["category"])),
		Action:      asString(t, fields["action"]),
		SubjectType: asString(t, fields["subject_type"]),
		SubjectID:   asString(t, fields["subject_id"]),
		Actor: identity.Actor{
			Type:        identity.Type(asString(t, fields["actor_type"])),
			ID:          asString(t, fields["actor_id"]),
			DisplayName: strOrEmpty(fields["actor_display"]),
			SessionID:   strOrEmpty(fields["actor_session"]),
			SourceIP:    strOrEmpty(fields["actor_source_ip"]),
			OnBehalfOf:  strOrEmpty(fields["actor_on_behalf"]),
		},
		PayloadHash:    payloadHash,
		PrevHash:       prevHash,
		IdempotencyKey: asString(t, fields["idempotency_key"]),
		RetentionClass: asString(t, fields["retention_class"]),
		CorrelationID:  asString(t, fields["correlation_id"]),
		TraceID:        strOrEmpty(fields["trace_id"]),
	}
}

// helpers

// asString type-asserts a corpus value to string, failing the test rather
// than panicking the suite when the corpus and the replay disagree on shape.
func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("corpus value %v has type %T, want string", v, v)
	}
	return s
}

// moneyScale clamps an integer scale into the currency type's uint8 field,
// refusing anything out of range rather than truncating it.
func moneyScale(t *testing.T, scale int64) uint8 {
	t.Helper()
	if scale < 0 || scale > 18 {
		t.Fatalf("scale %d out of the representable range", scale)
	}
	return uint8(scale)
}

func loadCorpus(t *testing.T) *golden.Corpus {
	t.Helper()
	corpus, err := golden.Load(corpusPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return corpus
}

func inputInt(t *testing.T, in any) int64 {
	t.Helper()
	switch v := in.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			t.Fatalf("corpus number %q: %v", v.String(), err)
		}
		return n
	case string:
		switch v {
		case "max_int64":
			return int64(9223372036854775807)
		case "min_int64":
			return int64(-9223372036854775808)
		}
		return mustParseInt64(t, v)
	default:
		t.Fatalf("corpus input %v has unsupported type %T", in, in)
		return 0
	}
}

func expectedMinor(t *testing.T, in any) int64 {
	t.Helper()
	return inputInt(t, in)
}

func mustParseInt64(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("parse int64 %q: %v", s, err)
	}
	return n
}

func mustRatio(t *testing.T, s string) money.Ratio {
	t.Helper()
	var num, den int64
	if _, err := fmt.Sscanf(s, "%d/%d", &num, &den); err != nil {
		t.Fatalf("parse ratio %q: %v", s, err)
	}
	return money.MustRatio(num, den)
}

func mustRounding(t *testing.T, s string) money.RoundingMode {
	t.Helper()
	mode, err := money.ParseRoundingMode(s)
	if err != nil {
		t.Fatalf("parse rounding %q: %v", s, err)
	}
	return mode
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func strOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// timeParseUTC parses the corpus' UTC timestamp format. Microsecond precision
// is preserved exactly; sub-microsecond digits never appear in the corpus
// because the storage layer could not round-trip them anyway.
func timeParseUTC(s string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05.999999", s)
}
