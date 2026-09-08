package evidence

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// DecodePayload is the inverse of the canonical payload encoder: it parses the
// tagged, length-prefixed byte stream back into the map form the port uses.
//
// The decoder exists because the wire transports payloads as their canonical
// bytes (payload_encoded) — Struct is lossy for int64, so bytes are the only
// form that reproduces hashes exactly. A decoder is also what turns the
// encoding into a cross-language contract rather than a Go-private format:
// non-Go implementations parse these same bytes, and golden test vectors pin
// the format for them.
//
// Decoded values are exactly the encodable kinds: nil, string, int64, bool,
// []any, map[string]any. Re-encoding a decoded value reproduces the input
// bytes, because the encoding is injective and kind-stable (every integer
// shape encodes to the same tagInt bytes).
//
// Malformed input is rejected rather than repaired: a payload whose encoding
// does not parse cannot have been the encoding that produced its hash, so
// treating a best-effort parse as equivalent would silently accept a
// tampered record.
func DecodePayload(buf []byte) (map[string]any, error) {
	d := &payloadDecoder{buf: buf}
	val, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.buf) {
		return nil, fmt.Errorf("%w: %d trailing bytes after payload", ErrInvalidRecord, len(d.buf)-d.pos)
	}
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: payload does not encode a map", ErrInvalidRecord)
	}
	return m, nil
}

// EncodePayload returns the canonical bytes for payload. Append paths hash
// these bytes; the wire adapters carry them.
func EncodePayload(payload map[string]any) ([]byte, error) {
	var buf []byte
	buf, err := encodeValue(buf, payload)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

type payloadDecoder struct {
	buf []byte
	pos int
}

var ErrMalformedPayload = errors.New("evidence: malformed canonical payload encoding")

func (d *payloadDecoder) value() (any, error) {
	if d.pos >= len(d.buf) {
		return nil, ErrMalformedPayload
	}
	tag := d.buf[d.pos]
	d.pos++

	switch tag {
	case tagNil:
		return nil, nil
	case tagString:
		s, err := d.str()
		if err != nil {
			return nil, err
		}
		return s, nil
	case tagBool:
		if d.pos >= len(d.buf) {
			return nil, ErrMalformedPayload
		}
		b := d.buf[d.pos]
		d.pos++
		return b != 0, nil
	case tagInt:
		n, err := d.varint()
		if err != nil {
			return nil, err
		}
		return n, nil
	case tagList:
		count, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		// A count larger than the remaining bytes can never decode; refusing
		// it here rejects truncated garbage without allocating for it.
		if count > uint64(len(d.buf)-d.pos) {
			return nil, ErrMalformedPayload
		}
		out := make([]any, 0, count)
		for i := uint64(0); i < count; i++ {
			item, err := d.value()
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case tagMap:
		count, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		if count > uint64(len(d.buf)-d.pos) {
			return nil, ErrMalformedPayload
		}
		out := make(map[string]any, count)
		for i := uint64(0); i < count; i++ {
			key, err := d.str()
			if err != nil {
				return nil, err
			}
			val, err := d.value()
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: unknown payload value tag %q", ErrMalformedPayload, tag)
	}
}

func (d *payloadDecoder) str() (string, error) {
	n, err := d.uvarint()
	if err != nil {
		return "", err
	}
	if n > uint64(len(d.buf)-d.pos) {
		return "", ErrMalformedPayload
	}
	s := string(d.buf[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s, nil
}

func (d *payloadDecoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n <= 0 {
		return 0, ErrMalformedPayload
	}
	d.pos += n
	return v, nil
}

// varint reads a signed varint. The encoder writes signed varints for integers
// because money minor units are signed; the wire shape must round-trip that
// exactly.
func (d *payloadDecoder) varint() (int64, error) {
	v, n := binary.Varint(d.buf[d.pos:])
	if n <= 0 {
		return 0, ErrMalformedPayload
	}
	d.pos += n
	return v, nil
}
