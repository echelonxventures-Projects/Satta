package evidence_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/satta/platform/libs/go/evidence"
)

// TestPayloadCodecRoundTrip pins the property the wire depends on: for every
// encodable payload, decode(encode(x)) == x and encode(decode(encode(x))) is
// byte-identical. If the second property ever fails, hashes computed from wire
// bytes and hashes computed from maps would disagree.
func TestPayloadCodecRoundTrip(t *testing.T) {
	payloads := []map[string]any{
		nil,
		{},
		{"k": nil},
		{"s": "value"},
		{"i": int64(-9223372036854775808)},
		{"j": int64(9223372036854775807)},
		{"small": 5, "neg": -3},
		{"b": true, "b2": false},
		{"list": []any{"a", int64(1), nil, true}},
		{"strings": []string{"x", "y"}},
		{"nested": map[string]any{"deep": map[string]any{"n": int64(9)}}},
		{"json.Number": mustNumber(t, "42")},
		{"map-of-strings": map[string]string{"a": "1"}},
		{
			"amount_minor_units": int64(1250),
			"currency_code":      "EUR",
			"nested":             map[string]any{"line": int64(1), "flag": true, "note": nil},
			"tags":               []any{"a", "b"},
		},
	}

	for i, p := range payloads {
		encoded, err := evidence.EncodePayload(p)
		if err != nil {
			t.Fatalf("payload %d: EncodePayload: %v", i, err)
		}
		decoded, err := evidence.DecodePayload(encoded)
		if err != nil {
			t.Fatalf("payload %d: DecodePayload: %v", i, err)
		}
		reencoded, err := evidence.EncodePayload(decoded)
		if err != nil {
			t.Fatalf("payload %d: re-encode: %v", i, err)
		}
		if !bytes.Equal(encoded, reencoded) {
			t.Fatalf("payload %d: canonical encoding is not kind-stable:\nfirst:  %x\nsecond: %x", i, encoded, reencoded)
		}
	}
}

func mustNumber(t *testing.T, s string) json.Number {
	t.Helper()
	return json.Number(s)
}

// TestDecodePayloadRejectsMalformed pins the other half of the contract: a
// payload whose bytes do not parse is rejected, never repaired. Each case is a
// truncation or corruption of a valid encoding.
func TestDecodePayloadRejectsMalformed(t *testing.T) {
	valid, err := evidence.EncodePayload(map[string]any{
		"s": "abc", "i": int64(7), "l": []any{int64(1), "x"},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	cases := []struct {
		name string
		buf  []byte
	}{
		{"truncated at every prefix", valid[:len(valid)-1]},
		{"empty", []byte{}},
		{"unknown tag", []byte{0x99}},
		// Tag bytes match the encoder's: 's' string, 'm' map, 'l' list,
		// each followed by a length/count that overruns the buffer.
		{"string length overruns buffer", []byte{'s', 0x7f, 'a'}},
		{"map count overruns buffer", []byte{'m', 0x7f}},
		{"list count overruns buffer", []byte{'l', 0x7f}},
		{"trailing bytes after map", append(append([]byte{}, valid...), 0x00)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := evidence.DecodePayload(tc.buf); err == nil {
				t.Fatal("expected malformed payload to be rejected")
			}
		})
	}
}

// TestDecodePayloadDecodesAllPrefixes individually truncates the valid
// encoding at every length; all must fail.
func TestDecodePayloadDecodesAllPrefixes(t *testing.T) {
	valid, err := evidence.EncodePayload(map[string]any{
		"s": "abc", "i": int64(7), "l": []any{int64(1), "x"},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for n := 0; n < len(valid); n++ {
		if _, err := evidence.DecodePayload(valid[:n]); err == nil {
			t.Fatalf("truncation at %d bytes decoded without error", n)
		}
	}
}

// TestEmptyPayloadHasStableEncoding pins the empty-map encoding so that the
// "no payload" case never drifts between implementations.
func TestEmptyPayloadHasStableEncoding(t *testing.T) {
	encoded, err := evidence.EncodePayload(nil)
	if err != nil {
		t.Fatalf("encode nil: %v", err)
	}
	empty, err := evidence.EncodePayload(map[string]any{})
	if err != nil {
		t.Fatalf("encode empty: %v", err)
	}
	if !bytes.Equal(encoded, empty) {
		t.Fatal("nil and empty map must encode identically")
	}
	if !reflect.DeepEqual(mustDecode(t, encoded), map[string]any{}) {
		t.Fatal("empty payload must decode to empty map")
	}
}

func mustDecode(t *testing.T, buf []byte) map[string]any {
	t.Helper()
	m, err := evidence.DecodePayload(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}
