package evidence

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/tenancy"
)

// MemoryStore is an in-memory Store used for local development, for unit tests,
// and as one side of the contract test suite that both implementations must pass.
//
// It mirrors the PostgreSQL implementation's isolation behaviour deliberately: it
// requires a tenancy scope in context and refuses cross-tenant reads, exactly as
// the database's row-level security policies do. A test double that is more
// permissive than production is a test double that lets isolation bugs through.
type MemoryStore struct {
	clock clock.Clock

	mu     sync.RWMutex
	chains map[string]*memChain
}

type memChain struct {
	tenantID string
	records  []Record
	byKey    map[string]uint64
	headHash []byte
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore(clk clock.Clock) *MemoryStore {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &MemoryStore{clock: clk, chains: make(map[string]*memChain)}
}

var _ Store = (*MemoryStore)(nil)

// Append implements Store.
func (s *MemoryStore) Append(ctx context.Context, record Record) (AppendResult, error) {
	results, err := s.AppendBatch(ctx, record.ChainID, []Record{record})
	if err != nil {
		return AppendResult{}, err
	}
	return results[0], nil
}

// AppendBatch implements Store.
func (s *MemoryStore) AppendBatch(ctx context.Context, chainID string, records []Record) ([]AppendResult, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("evidence: AppendBatch requires at least one record")
	}
	callerScope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return nil, err
	}

	for i := range records {
		if records[i].ChainID != chainID {
			return nil, fmt.Errorf("%w: record %d names chain %q but the batch targets %q",
				ErrInvalidRecord, i, records[i].ChainID, chainID)
		}
		if err := records[i].Validate(); err != nil {
			return nil, fmt.Errorf("evidence: record %d: %w", i, err)
		}
		if err := tenancy.CheckAccess(ctx, records[i].Scope); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	chain, ok := s.chains[chainID]
	if !ok {
		chain = &memChain{
			tenantID: callerScope.TenantID,
			byKey:    make(map[string]uint64),
			headHash: GenesisHash,
		}
		s.chains[chainID] = chain
	}
	if chain.tenantID != callerScope.TenantID {
		return nil, fmt.Errorf("%w: chain %q belongs to tenant %q, caller is %q",
			ErrChainMismatch, chainID, chain.tenantID, callerScope.TenantID)
	}

	// Stage the batch so that a failure part-way through leaves nothing behind.
	staged := make([]Record, 0, len(records))
	results := make([]AppendResult, 0, len(records))
	nextSequence := uint64(len(chain.records)) + 1
	prevHash := chain.headHash
	now := s.clock.Now()

	for i, in := range records {
		if existingSeq, dup := chain.byKey[in.IdempotencyKey]; dup {
			existing := chain.records[existingSeq-1]
			results = append(results, AppendResult{
				Sequence:     existing.Sequence,
				RecordHash:   existing.RecordHash,
				Deduplicated: true,
			})
			continue
		}
		// A key repeated inside the same batch is a caller bug, not a retry.
		for _, s := range staged {
			if s.IdempotencyKey == in.IdempotencyKey {
				return nil, fmt.Errorf("%w: idempotency key %q appears twice in one batch",
					ErrInvalidRecord, in.IdempotencyKey)
			}
		}

		rec := in.Normalize()
		rec.Sequence = nextSequence
		rec.RecordedAt = now
		rec.PrevHash = prevHash
		payloadHash, err := HashPayload(rec.Payload)
		if err != nil {
			return nil, fmt.Errorf("evidence: record %d: %w", i, err)
		}
		rec.PayloadHash = payloadHash
		recordHash, err := rec.ComputeHash()
		if err != nil {
			return nil, fmt.Errorf("evidence: record %d: %w", i, err)
		}
		rec.RecordHash = recordHash

		staged = append(staged, rec)
		results = append(results, AppendResult{Sequence: rec.Sequence, RecordHash: rec.RecordHash})
		prevHash = rec.RecordHash
		nextSequence++
	}

	for _, rec := range staged {
		chain.records = append(chain.records, rec)
		chain.byKey[rec.IdempotencyKey] = rec.Sequence
		chain.headHash = rec.RecordHash
	}
	return results, nil
}

// Get implements Store.
func (s *MemoryStore) Get(ctx context.Context, chainID string, sequence uint64) (Record, error) {
	chain, err := s.readableChain(ctx, chainID)
	if err != nil {
		return Record{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sequence == 0 || sequence > uint64(len(chain.records)) {
		return Record{}, fmt.Errorf("%w: chain %q sequence %d", ErrNotFound, chainID, sequence)
	}
	return cloneRecord(chain.records[sequence-1]), nil
}

// Range implements Store.
func (s *MemoryStore) Range(ctx context.Context, chainID string, from, to uint64) ([]Record, error) {
	chain, err := s.readableChain(ctx, chainID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if from == 0 {
		from = 1
	}
	length := uint64(len(chain.records))
	if to == 0 || to > length {
		to = length
	}
	if from > to {
		return nil, nil
	}
	out := make([]Record, 0, to-from+1)
	for seq := from; seq <= to; seq++ {
		out = append(out, cloneRecord(chain.records[seq-1]))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

// Head implements Store.
func (s *MemoryStore) Head(ctx context.Context, chainID string) (ChainHead, error) {
	callerScope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return ChainHead{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	chain, ok := s.chains[chainID]
	if !ok {
		// A chain that does not exist yet reports as empty rather than missing,
		// so that first-append and subsequent-append paths are identical.
		return ChainHead{ChainID: chainID, TenantID: callerScope.TenantID, HeadHash: GenesisHash}, nil
	}
	if chain.tenantID != callerScope.TenantID {
		return ChainHead{}, fmt.Errorf("%w: chain %q belongs to tenant %q, caller is %q",
			ErrChainMismatch, chainID, chain.tenantID, callerScope.TenantID)
	}
	var updatedAt = s.clock.Now()
	if n := len(chain.records); n > 0 {
		updatedAt = chain.records[n-1].RecordedAt
	}
	return ChainHead{
		ChainID:   chainID,
		TenantID:  chain.tenantID,
		Sequence:  uint64(len(chain.records)),
		HeadHash:  append([]byte(nil), chain.headHash...),
		UpdatedAt: updatedAt,
	}, nil
}

// FindByIdempotencyKey implements Store.
func (s *MemoryStore) FindByIdempotencyKey(ctx context.Context, chainID, key string) (Record, error) {
	chain, err := s.readableChain(ctx, chainID)
	if err != nil {
		return Record{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	seq, ok := chain.byKey[key]
	if !ok {
		return Record{}, fmt.Errorf("%w: chain %q idempotency key %q", ErrNotFound, chainID, key)
	}
	return cloneRecord(chain.records[seq-1]), nil
}

// readableChain resolves a chain after checking that the caller's tenant owns it.
func (s *MemoryStore) readableChain(ctx context.Context, chainID string) (*memChain, error) {
	callerScope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	chain, ok := s.chains[chainID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: chain %q", ErrNotFound, chainID)
	}
	if chain.tenantID != callerScope.TenantID {
		// Reported as a mismatch rather than as "not found" because both the
		// caller and the chain are known here; the security-relevant behaviour is
		// that no record content crosses the boundary.
		return nil, fmt.Errorf("%w: chain %q belongs to tenant %q, caller is %q",
			ErrChainMismatch, chainID, chain.tenantID, callerScope.TenantID)
	}
	return chain, nil
}

// cloneRecord returns a defensive copy so that a caller mutating a returned
// record cannot alter stored state — which in a store whose entire purpose is
// immutability would be an obvious hole.
func cloneRecord(r Record) Record {
	out := r
	out.PayloadHash = append([]byte(nil), r.PayloadHash...)
	out.PrevHash = append([]byte(nil), r.PrevHash...)
	out.RecordHash = append([]byte(nil), r.RecordHash...)
	if r.Payload != nil {
		out.Payload = make(map[string]any, len(r.Payload))
		for k, v := range r.Payload {
			out.Payload[k] = v
		}
	}
	return out
}

// TamperForTest mutates a stored record in place, bypassing every integrity
// guarantee. It exists so that the verifier can be tested against an actually
// corrupted chain — a verifier that has only ever been run against intact data
// has not been tested.
//
// It is defined on the in-memory double only. There is no equivalent on the
// PostgreSQL store, and there must not be.
func (s *MemoryStore) TamperForTest(chainID string, sequence uint64, mutate func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.chains[chainID]
	if !ok {
		return fmt.Errorf("%w: chain %q", ErrNotFound, chainID)
	}
	if sequence == 0 || sequence > uint64(len(chain.records)) {
		return fmt.Errorf("%w: chain %q sequence %d", ErrNotFound, chainID, sequence)
	}
	mutate(&chain.records[sequence-1])
	return nil
}

// DropRecordForTest removes a record, leaving a gap. Test-only, for the same
// reason as TamperForTest.
func (s *MemoryStore) DropRecordForTest(chainID string, sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.chains[chainID]
	if !ok {
		return fmt.Errorf("%w: chain %q", ErrNotFound, chainID)
	}
	if sequence == 0 || sequence > uint64(len(chain.records)) {
		return fmt.Errorf("%w: chain %q sequence %d", ErrNotFound, chainID, sequence)
	}
	idx := sequence - 1
	removed := chain.records[idx]
	chain.records = append(chain.records[:idx], chain.records[idx+1:]...)
	delete(chain.byKey, removed.IdempotencyKey)
	return nil
}
