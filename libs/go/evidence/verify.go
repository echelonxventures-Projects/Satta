package evidence

import (
	"bytes"
	"context"
	"fmt"
)

// verifyPageSize bounds how many records are held in memory at once during
// verification. Chains are expected to reach billions of records, so verification
// streams rather than loading a range.
const verifyPageSize = 1000

// VerifyResult reports the outcome of a chain integrity check.
type VerifyResult struct {
	// Intact is true when every record in the range reproduced.
	Intact bool
	// RecordsChecked counts the records examined.
	RecordsChecked uint64
	// FirstBadSequence is the earliest position that failed, when Intact is
	// false. Reporting the earliest rather than any failure matters: the first
	// break localises the tampering, and everything after it fails as a
	// consequence.
	FirstBadSequence uint64
	// FailureReason describes what did not reproduce.
	FailureReason string
	// HeadHash is the record hash at the end of the verified range.
	HeadHash []byte
	// From and To are the bounds actually verified.
	From uint64
	To   uint64
}

// Err returns ErrTampered wrapped with detail when the chain did not verify, and
// nil when it did. Callers that treat a broken chain as a hard failure use this;
// callers that report it (a compliance dashboard) read the fields.
func (v VerifyResult) Err() error {
	if v.Intact {
		return nil
	}
	return fmt.Errorf("%w: chain broken at sequence %d: %s",
		ErrTampered, v.FirstBadSequence, v.FailureReason)
}

// Verify recomputes the hash chain over [from, to] and reports the first
// inconsistency. A from of 0 or 1 starts at the beginning; a to of 0 means the
// current head.
//
// Verification is implemented once, here, against the Store port rather than
// inside each implementation. A verifier that lives inside the storage engine it
// is checking is a verifier that can be defeated by the same access that defeated
// the storage engine, and it also drifts between implementations.
//
// Three separate properties are checked per record, because each catches a
// different class of tampering:
//
//  1. The record hash reproduces from the record's own contents. Catches an
//     edited field.
//  2. The payload hash reproduces from the stored payload. Catches an edited
//     payload where the hash column was left alone.
//  3. The record's PrevHash equals the previous record's RecordHash. Catches
//     insertion, deletion and reordering, which the first two would not.
func Verify(ctx context.Context, store Store, chainID string, from, to uint64) (VerifyResult, error) {
	head, err := store.Head(ctx, chainID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("evidence: read head of chain %q: %w", chainID, err)
	}
	if from == 0 {
		from = 1
	}
	if to == 0 || to > head.Sequence {
		to = head.Sequence
	}

	result := VerifyResult{Intact: true, From: from, To: to}
	if head.Sequence == 0 || from > to {
		// An empty chain is trivially intact, and its head is the genesis value.
		result.HeadHash = GenesisHash
		if head.Sequence > 0 {
			result.HeadHash = head.HeadHash
		}
		return result, nil
	}

	// Establish the expected PrevHash for the first record in the range. When
	// starting mid-chain, that means reading the record immediately before it;
	// otherwise a caller could "verify" a doctored sub-range that does not attach
	// to the chain it claims to be part of.
	expectedPrev := GenesisHash
	if from > 1 {
		prev, err := store.Get(ctx, chainID, from-1)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("evidence: read anchor record %d of chain %q: %w", from-1, chainID, err)
		}
		expectedPrev = prev.RecordHash
	}

	nextSequence := from
	for cursor := from; cursor <= to; {
		pageEnd := cursor + verifyPageSize - 1
		if pageEnd > to {
			pageEnd = to
		}
		page, err := store.Range(ctx, chainID, cursor, pageEnd)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("evidence: read chain %q [%d,%d]: %w", chainID, cursor, pageEnd, err)
		}
		if len(page) == 0 {
			return fail(result, nextSequence, fmt.Sprintf(
				"record %d is missing: the chain has a gap where a record was removed", nextSequence)), nil
		}

		for _, record := range page {
			if record.Sequence != nextSequence {
				return fail(result, nextSequence, fmt.Sprintf(
					"expected sequence %d but found %d: records were inserted, removed or reordered",
					nextSequence, record.Sequence)), nil
			}

			payloadHash, err := HashPayload(record.Payload)
			if err != nil {
				return fail(result, record.Sequence, fmt.Sprintf(
					"stored payload cannot be canonically encoded: %v", err)), nil
			}
			if !bytes.Equal(payloadHash, record.PayloadHash) {
				return fail(result, record.Sequence,
					"payload does not match its stored payload_hash: the payload was altered"), nil
			}
			if !bytes.Equal(record.PrevHash, expectedPrev) {
				return fail(result, record.Sequence, fmt.Sprintf(
					"prev_hash %x does not match the previous record hash %x: the chain was re-linked",
					truncate(record.PrevHash), truncate(expectedPrev))), nil
			}
			computed, err := record.ComputeHash()
			if err != nil {
				return fail(result, record.Sequence, fmt.Sprintf("cannot recompute record hash: %v", err)), nil
			}
			if !bytes.Equal(computed, record.RecordHash) {
				return fail(result, record.Sequence, fmt.Sprintf(
					"record_hash %x does not match recomputed %x: a field was altered",
					truncate(record.RecordHash), truncate(computed))), nil
			}

			expectedPrev = record.RecordHash
			result.RecordsChecked++
			result.HeadHash = record.RecordHash
			nextSequence++
		}
		cursor = page[len(page)-1].Sequence + 1
	}

	// When the whole chain was verified, the recomputed head must also match the
	// head the store advertises. A mismatch means the chain body and the head
	// pointer disagree, which is what a rollback to an earlier state looks like.
	if from == 1 && to == head.Sequence && !bytes.Equal(result.HeadHash, head.HeadHash) {
		return fail(result, head.Sequence, fmt.Sprintf(
			"recomputed head %x does not match stored chain head %x",
			truncate(result.HeadHash), truncate(head.HeadHash))), nil
	}
	return result, nil
}

func fail(result VerifyResult, sequence uint64, reason string) VerifyResult {
	result.Intact = false
	result.FirstBadSequence = sequence
	result.FailureReason = reason
	return result
}

// truncate shortens a hash for error messages. Full hashes make failures hard to
// read and the prefix is sufficient to distinguish them.
func truncate(h []byte) []byte {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
