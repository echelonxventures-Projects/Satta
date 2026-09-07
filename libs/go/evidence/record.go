// Package evidence implements the tamper-evident, append-only evidence store.
//
// SAFETY INVARIANT 4: records are hash-chained and there is no deletion or
// mutation path within the retention period. That is enforced at three levels,
// because any single level can be worked around:
//
//   - The Store interface exposes no Update or Delete method, so no caller can
//     express the intent in Go.
//   - The PostgreSQL schema carries BEFORE UPDATE OR DELETE triggers that raise,
//     so a direct SQL statement from a console fails too.
//   - Each record's hash covers its predecessor's hash, so a change made by
//     bypassing both of the above (editing files on disk, restoring a doctored
//     backup) is detectable by Verify and cannot be repaired without rewriting
//     every subsequent record.
//
// The third property is the one that actually matters. The first two prevent
// accidents; only the hash chain survives a determined insider, and it is why
// "tamper-evident" is the correct claim rather than "tamper-proof".
package evidence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// hashVersion prefixes every hash pre-image. If the canonical encoding ever has
// to change, the version distinguishes old records from new ones so that
// historical chains remain verifiable under the rules they were written with.
const hashVersion byte = 1

// HashSize is the length in bytes of a record or payload hash (SHA-256).
const HashSize = sha256.Size

// GenesisHash is the PrevHash of the first record in any chain.
var GenesisHash = make([]byte, HashSize)

// Errors returned by this package.
var (
	// ErrChainMismatch is returned when an append would put a record into a
	// chain belonging to a different tenant.
	ErrChainMismatch = errors.New("evidence: chain belongs to a different tenant")
	// ErrDuplicate is returned when the idempotency key has already been used in
	// the chain and the caller asked for strict behaviour.
	ErrDuplicate = errors.New("evidence: idempotency key already used in chain")
	// ErrNotFound is returned when no record exists at the requested position.
	ErrNotFound = errors.New("evidence: record not found")
	// ErrTampered is returned by Verify when the chain does not reproduce.
	ErrTampered = errors.New("evidence: chain integrity check failed")
	// ErrUnhashablePayload is returned when a payload contains a value that
	// cannot be canonically encoded.
	ErrUnhashablePayload = errors.New("evidence: payload value cannot be canonically encoded")
	// ErrInvalidRecord is returned when a record is missing required fields.
	ErrInvalidRecord = errors.New("evidence: invalid record")
)

// Category is the coarse classification used for retention and access control.
type Category string

const (
	CategoryUnspecified         Category = ""
	CategoryMoney               Category = "money"
	CategoryIdentity            Category = "identity"
	CategoryCompliance          Category = "compliance"
	CategoryResponsibleGambling Category = "responsible_gambling"
	CategorySecurity            Category = "security"
	CategoryGameplay            Category = "gameplay"
	CategoryConfiguration       Category = "configuration"
	CategoryCommercial          Category = "commercial"
)

var validCategories = map[Category]bool{
	CategoryMoney:               true,
	CategoryIdentity:            true,
	CategoryCompliance:          true,
	CategoryResponsibleGambling: true,
	CategorySecurity:            true,
	CategoryGameplay:            true,
	CategoryConfiguration:       true,
	CategoryCommercial:          true,
}

// Record is one immutable entry in a hash chain.
//
// Timestamps are stored and hashed at microsecond precision. PostgreSQL
// timestamptz resolves to microseconds, so hashing at nanosecond precision would
// produce records that verify in memory and fail after a database round-trip —
// an integrity alarm caused by the storage layer rather than by tampering.
type Record struct {
	// ChainID names the chain. Chains are partitioned per tenant so that one
	// tenant's write volume never serialises behind another's, since appending
	// to a chain is necessarily sequential.
	ChainID string
	// Sequence is the 1-based position within the chain, assigned on append.
	Sequence uint64
	// Scope is the tenancy scope the recorded event belongs to.
	Scope tenancy.Scope
	// OccurredAt is when the event happened, in UTC (SAFETY INVARIANT 12).
	OccurredAt time.Time
	// RecordedAt is when the platform durably stored it, assigned on append.
	RecordedAt time.Time
	// Category classifies the record.
	Category Category
	// Action is a dotted action name from the action registry, e.g.
	// "ledger.transfer.posted".
	Action string
	// SubjectType and SubjectID identify the entity acted upon.
	SubjectType string
	SubjectID   string
	// Actor is the principal responsible.
	Actor identity.Actor
	// Payload is structured detail. Permitted value types are string, bool,
	// integer, nil, slice and map. Floating-point values are rejected: an audit
	// record of a money movement must not contain a value that cannot be
	// reproduced exactly.
	//
	// Payloads must not contain secrets, raw payment instrument data, or
	// identity document contents; documents are referenced by handle.
	Payload map[string]any
	// PayloadHash is SHA-256 over the canonical encoding of Payload.
	PayloadHash []byte
	// PrevHash is the RecordHash of the preceding record, or GenesisHash.
	PrevHash []byte
	// RecordHash is SHA-256 over the canonical encoding of this record.
	RecordHash []byte
	// IdempotencyKey de-duplicates appends within the chain.
	IdempotencyKey string
	// RetentionClass is the retention policy key resolved at write time. It is
	// part of the hashed pre-image, so retention cannot be shortened after the
	// fact without breaking the chain.
	RetentionClass string
	// CorrelationID and TraceID link the record to the originating request.
	CorrelationID string
	TraceID       string
}

// Validate checks the caller-supplied fields of a record before it is appended.
// Server-assigned fields (Sequence, RecordedAt, PrevHash, RecordHash) are not
// checked here because they are overwritten on append.
func (r Record) Validate() error {
	if r.ChainID == "" {
		return fmt.Errorf("%w: chain_id is empty", ErrInvalidRecord)
	}
	if err := tenancy.ValidateIdentifier("chain_id", r.ChainID); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRecord, err)
	}
	if err := r.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRecord, err)
	}
	if !validCategories[r.Category] {
		return fmt.Errorf("%w: category %q is not a registered category", ErrInvalidRecord, r.Category)
	}
	if r.Action == "" {
		return fmt.Errorf("%w: action is empty", ErrInvalidRecord)
	}
	if r.SubjectType == "" || r.SubjectID == "" {
		return fmt.Errorf("%w: subject_type and subject_id are both required", ErrInvalidRecord)
	}
	if err := r.Actor.Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRecord, err)
	}
	if r.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at is zero", ErrInvalidRecord)
	}
	if r.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotency_key is required", ErrInvalidRecord)
	}
	if r.RetentionClass == "" {
		return fmt.Errorf("%w: retention_class is required", ErrInvalidRecord)
	}
	// Reject unhashable payloads at validation time rather than at hash time so
	// that the caller gets the error before anything is written.
	if _, err := HashPayload(r.Payload); err != nil {
		return err
	}
	return nil
}

// Normalize returns a copy with timestamps coerced to UTC at microsecond
// precision. Append calls this; callers do not need to.
func (r Record) Normalize() Record {
	out := r
	out.OccurredAt = normalizeTime(r.OccurredAt)
	out.RecordedAt = normalizeTime(r.RecordedAt)
	return out
}

func normalizeTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Microsecond)
}

// HashPayload returns SHA-256 over the canonical encoding of payload. A nil or
// empty payload hashes the empty map, which is a defined value rather than a
// special case.
func HashPayload(payload map[string]any) ([]byte, error) {
	var buf []byte
	buf, err := encodeValue(buf, payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf)
	return sum[:], nil
}

// ComputeHash returns the record hash for r, given its payload hash and
// predecessor hash. It does not mutate r.
//
// The pre-image is a tagged, length-prefixed byte stream rather than JSON.
// Canonical JSON has too many ways to be almost-right — key ordering, number
// formatting, unicode escaping — and every one of them is a way for two parties
// to disagree about whether a chain is intact. Length prefixing makes the
// encoding injective: no two distinct records can produce the same bytes.
func (r Record) ComputeHash() ([]byte, error) {
	if len(r.PayloadHash) != HashSize {
		return nil, fmt.Errorf("%w: payload_hash must be %d bytes, got %d",
			ErrInvalidRecord, HashSize, len(r.PayloadHash))
	}
	if len(r.PrevHash) != HashSize {
		return nil, fmt.Errorf("%w: prev_hash must be %d bytes, got %d",
			ErrInvalidRecord, HashSize, len(r.PrevHash))
	}

	buf := []byte{hashVersion}
	buf = appendString(buf, r.ChainID)
	buf = appendUint(buf, r.Sequence)
	buf = appendString(buf, r.Scope.TenantID)
	buf = appendString(buf, r.Scope.BrandID)
	buf = appendString(buf, r.Scope.JurisdictionCode)
	buf = appendString(buf, r.Scope.RegionCode)
	buf = appendInt(buf, normalizeTime(r.OccurredAt).UnixMicro())
	buf = appendInt(buf, normalizeTime(r.RecordedAt).UnixMicro())
	buf = appendString(buf, string(r.Category))
	buf = appendString(buf, r.Action)
	buf = appendString(buf, r.SubjectType)
	buf = appendString(buf, r.SubjectID)
	buf = appendString(buf, string(r.Actor.Type))
	buf = appendString(buf, r.Actor.ID)
	buf = appendString(buf, r.Actor.DisplayName)
	buf = appendString(buf, r.Actor.SessionID)
	buf = appendString(buf, r.Actor.SourceIP)
	buf = appendString(buf, r.Actor.OnBehalfOf)
	buf = appendString(buf, r.RetentionClass)
	buf = appendString(buf, r.IdempotencyKey)
	buf = appendString(buf, r.CorrelationID)
	buf = appendString(buf, r.TraceID)
	buf = appendBytes(buf, r.PayloadHash)
	buf = appendBytes(buf, r.PrevHash)

	sum := sha256.Sum256(buf)
	return sum[:], nil
}

// Canonical encoding primitives. Every field is length-prefixed so that the
// concatenation is unambiguous.

func appendString(buf []byte, s string) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}

func appendBytes(buf, b []byte) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(b)))
	return append(buf, b...)
}

func appendUint(buf []byte, n uint64) []byte { return binary.AppendUvarint(buf, n) }

func appendInt(buf []byte, n int64) []byte { return binary.AppendVarint(buf, n) }

// Type tags for payload values.
const (
	tagNil    byte = 'n'
	tagString byte = 's'
	tagInt    byte = 'i'
	tagBool   byte = 'b'
	tagList   byte = 'l'
	tagMap    byte = 'm'
)

// encodeValue canonically encodes a payload value.
//
// Floating-point values are refused rather than encoded. Two justifications, and
// the second is the important one: floats have multiple bit patterns for
// equivalent values, so hashing them is not reliably reproducible across
// languages; and a float in an audit payload usually means money or a rate was
// computed as a float somewhere upstream, which SAFETY INVARIANT 1 forbids. The
// error message points the caller at that.
func encodeValue(buf []byte, v any) ([]byte, error) {
	switch val := v.(type) {
	case nil:
		return append(buf, tagNil), nil
	case string:
		buf = append(buf, tagString)
		return appendString(buf, val), nil
	case bool:
		buf = append(buf, tagBool)
		if val {
			return append(buf, 1), nil
		}
		return append(buf, 0), nil
	case int:
		buf = append(buf, tagInt)
		return appendInt(buf, int64(val)), nil
	case int32:
		buf = append(buf, tagInt)
		return appendInt(buf, int64(val)), nil
	case int64:
		buf = append(buf, tagInt)
		return appendInt(buf, val), nil
	case uint64:
		if val > 1<<63-1 {
			return nil, fmt.Errorf("%w: uint64 %d exceeds int64 range", ErrUnhashablePayload, val)
		}
		buf = append(buf, tagInt)
		return appendInt(buf, int64(val)), nil
	case json.Number:
		// Payloads read back from JSONB arrive as json.Number when decoded with
		// UseNumber, which is how verification reproduces integers exactly.
		n, err := val.Int64()
		if err != nil {
			return nil, fmt.Errorf("%w: number %q is not an integer; represent money as minor units and rates as an explicit numerator/denominator pair",
				ErrUnhashablePayload, val.String())
		}
		buf = append(buf, tagInt)
		return appendInt(buf, n), nil
	case float32, float64:
		return nil, fmt.Errorf("%w: floating-point values are not permitted in evidence payloads (SAFETY INVARIANT 1); use integer minor units for money and a numerator/denominator pair for rates",
			ErrUnhashablePayload)
	case []any:
		buf = append(buf, tagList)
		buf = binary.AppendUvarint(buf, uint64(len(val)))
		for _, item := range val {
			var err error
			if buf, err = encodeValue(buf, item); err != nil {
				return nil, err
			}
		}
		return buf, nil
	case []string:
		buf = append(buf, tagList)
		buf = binary.AppendUvarint(buf, uint64(len(val)))
		for _, item := range val {
			buf = append(buf, tagString)
			buf = appendString(buf, item)
		}
		return buf, nil
	case map[string]any:
		buf = append(buf, tagMap)
		buf = binary.AppendUvarint(buf, uint64(len(val)))
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			buf = appendString(buf, k)
			var err error
			if buf, err = encodeValue(buf, val[k]); err != nil {
				return nil, fmt.Errorf("payload key %q: %w", k, err)
			}
		}
		return buf, nil
	case map[string]string:
		buf = append(buf, tagMap)
		buf = binary.AppendUvarint(buf, uint64(len(val)))
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			buf = appendString(buf, k)
			buf = append(buf, tagString)
			buf = appendString(buf, val[k])
		}
		return buf, nil
	default:
		return nil, fmt.Errorf("%w: unsupported type %T", ErrUnhashablePayload, v)
	}
}
