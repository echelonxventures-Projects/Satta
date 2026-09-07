package evidence

import (
	"context"
	"time"
)

// Store is the evidence store port.
//
// SAFETY INVARIANT 4: this interface intentionally has no Update, Delete, Purge
// or Truncate method, and none may be added. Retention lifecycle work operates
// on whole chains that have passed their retention horizon, through a separate
// governed process, and is not expressible through this port.
type Store interface {
	// Append adds a record to its chain and returns the assigned position.
	// Server-assigned fields on the input record are ignored.
	//
	// Append is idempotent on (ChainID, IdempotencyKey): a repeat returns the
	// original result with Deduplicated set. Idempotency is not a convenience
	// here — an audit trail that double-records a money movement on a retry is
	// as unusable as one that misses it.
	Append(ctx context.Context, record Record) (AppendResult, error)

	// AppendBatch appends records to one chain atomically, in the given order.
	// Either every record is committed or none is, so a partially written
	// multi-part event cannot appear in the chain.
	AppendBatch(ctx context.Context, chainID string, records []Record) ([]AppendResult, error)

	// Get returns the record at sequence in chainID, or ErrNotFound.
	Get(ctx context.Context, chainID string, sequence uint64) (Record, error)

	// Range returns records in [from, to] inclusive, ordered by sequence
	// ascending. A to of 0 means "up to the current head".
	Range(ctx context.Context, chainID string, from, to uint64) ([]Record, error)

	// Head returns the current head of chainID. For a chain that does not exist
	// yet it returns a zero-sequence head with GenesisHash rather than an error,
	// so that callers can treat "empty" and "new" identically.
	Head(ctx context.Context, chainID string) (ChainHead, error)

	// FindByIdempotencyKey returns the existing record for a key, or ErrNotFound.
	// Callers use it to recover the outcome of an append whose response was lost.
	FindByIdempotencyKey(ctx context.Context, chainID, idempotencyKey string) (Record, error)
}

// AppendResult reports the outcome of an append.
type AppendResult struct {
	// Sequence is the position the record occupies in the chain.
	Sequence uint64
	// RecordHash is the record's hash, which becomes the next record's PrevHash.
	RecordHash []byte
	// Deduplicated is true when the idempotency key had already been used and an
	// existing record was returned instead of a new one being written.
	Deduplicated bool
}

// ChainHead is the current tip of a chain. External notarisation and periodic
// checkpointing anchor to this value: publishing the head hash to an
// independent, append-only location is what turns "we can detect tampering" into
// "we can prove to a third party that we detected it", because otherwise an
// attacker with write access could rewrite the whole chain consistently.
type ChainHead struct {
	ChainID  string
	TenantID string
	Sequence uint64
	HeadHash []byte
	// UpdatedAt is when the head last moved, in UTC.
	UpdatedAt time.Time
}

// IsEmpty reports whether the chain has no records.
func (h ChainHead) IsEmpty() bool { return h.Sequence == 0 }
