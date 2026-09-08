package evidence

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the evidence store's schema migrations. The store owns its
// own schema rather than sharing a global migration directory, so that the
// append-only triggers and RLS policies travel with the code that depends on them.
func Migrations() (fs.FS, string) { return migrationsFS, "migrations" }

// PostgresStore is the durable evidence store.
type PostgresStore struct {
	pool  *db.Pool
	clock clock.Clock
}

// NewPostgresStore returns a store backed by pool.
func NewPostgresStore(pool *db.Pool, clk clock.Clock) *PostgresStore {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &PostgresStore{pool: pool, clock: clk}
}

var _ Store = (*PostgresStore)(nil)

// Migrate applies the evidence schema.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	fsys, dir := Migrations()
	_, err := db.MigrateFS(ctx, s.pool, "evidence", fsys, dir)
	return err
}

const recordColumns = `
    chain_id, sequence, tenant_id, brand_id, jurisdiction_code, region_code,
    occurred_at, recorded_at, category, action, subject_type, subject_id,
    actor_type, actor_id, actor_display_name, actor_session_id, actor_source_ip,
    actor_on_behalf_of, payload, payload_hash, prev_hash, record_hash,
    idempotency_key, retention_class, correlation_id, trace_id`

// Append implements Store.
func (s *PostgresStore) Append(ctx context.Context, record Record) (AppendResult, error) {
	results, err := s.AppendBatch(ctx, record.ChainID, []Record{record})
	if err != nil {
		return AppendResult{}, err
	}
	return results[0], nil
}

// AppendBatch implements Store.
//
// The whole batch runs in one transaction holding a row lock on the chain head.
// Appending to a hash chain is inherently sequential: the next record's PrevHash is
// the previous record's RecordHash, so concurrent appends to one chain must
// serialise. Taking the lock explicitly makes that serialisation visible and
// bounded, rather than surfacing as unique-violation retries under load.
func (s *PostgresStore) AppendBatch(ctx context.Context, chainID string, records []Record) ([]AppendResult, error) {
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
		if verr := records[i].Validate(); verr != nil {
			return nil, fmt.Errorf("evidence: record %d: %w", i, verr)
		}
		if aerr := tenancy.CheckAccess(ctx, records[i].Scope); aerr != nil {
			return nil, aerr
		}
		if werr := records[i].Actor.RequireWriteCapable(); werr != nil {
			return nil, fmt.Errorf("evidence: record %d: %w", i, werr)
		}
	}
	if iderr := tenancy.ValidateIdentifier("chain_id", chainID); iderr != nil {
		return nil, iderr
	}

	var results []AppendResult
	err = s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		head, headHash, lerr := s.lockChain(ctx, tx, chainID, callerScope.TenantID)
		if lerr != nil {
			return lerr
		}

		batchResults := make([]AppendResult, 0, len(records))
		nextSequence := head + 1
		prevHash := headHash
		now := s.clock.Now()
		seenKeys := make(map[string]struct{}, len(records))

		for i, in := range records {
			if existing, found, ferr := s.findByKey(ctx, tx, chainID, in.IdempotencyKey); ferr != nil {
				return ferr
			} else if found {
				batchResults = append(batchResults, AppendResult{
					Sequence:     existing.Sequence,
					RecordHash:   existing.RecordHash,
					Deduplicated: true,
				})
				continue
			}
			if _, dup := seenKeys[in.IdempotencyKey]; dup {
				return fmt.Errorf("%w: idempotency key %q appears twice in one batch",
					ErrInvalidRecord, in.IdempotencyKey)
			}
			seenKeys[in.IdempotencyKey] = struct{}{}

			rec := in.Normalize()
			rec.Sequence = nextSequence
			rec.RecordedAt = now
			rec.PrevHash = prevHash
			if rec.PayloadHash, err = HashPayload(rec.Payload); err != nil {
				return fmt.Errorf("evidence: record %d: %w", i, err)
			}
			if rec.RecordHash, err = rec.ComputeHash(); err != nil {
				return fmt.Errorf("evidence: record %d: %w", i, err)
			}
			if ierr := s.insertRecord(ctx, tx, rec); ierr != nil {
				return fmt.Errorf("evidence: record %d: %w", i, ierr)
			}

			batchResults = append(batchResults, AppendResult{Sequence: rec.Sequence, RecordHash: rec.RecordHash})
			prevHash = rec.RecordHash
			nextSequence++
		}

		if nextSequence-1 != head {
			if _, uerr := tx.Exec(ctx,
				`UPDATE evidence_chain SET head_sequence = $2, head_hash = $3 WHERE chain_id = $1`,
				chainID, nextSequence-1, prevHash); uerr != nil {
				return fmt.Errorf("evidence: advance chain head: %w", uerr)
			}
		}
		results = batchResults
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// lockChain returns the chain's current head, creating the chain if needed, and
// holds a row lock for the remainder of the transaction.
func (s *PostgresStore) lockChain(ctx context.Context, tx pgx.Tx, chainID, tenantID string) (uint64, []byte, error) {
	// ON CONFLICT DO NOTHING rather than a read-then-insert, so that two
	// concurrent first-appends to a new chain do not race.
	if _, err := tx.Exec(ctx,
		`INSERT INTO evidence_chain (chain_id, tenant_id, head_sequence, head_hash)
		 VALUES ($1, $2, 0, $3)
		 ON CONFLICT (chain_id) DO NOTHING`,
		chainID, tenantID, GenesisHash); err != nil {
		return 0, nil, fmt.Errorf("evidence: ensure chain %q: %w", chainID, err)
	}

	var head int64
	var headHash []byte
	var owner string
	err := tx.QueryRow(ctx,
		`SELECT head_sequence, head_hash, tenant_id FROM evidence_chain WHERE chain_id = $1 FOR UPDATE`,
		chainID).Scan(&head, &headHash, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		// The insert above did nothing and the row is invisible, which under
		// FORCE ROW LEVEL SECURITY means it belongs to another tenant.
		return 0, nil, fmt.Errorf("%w: chain %q is not visible to tenant %q", ErrChainMismatch, chainID, tenantID)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("evidence: lock chain %q: %w", chainID, err)
	}
	if owner != tenantID {
		return 0, nil, fmt.Errorf("%w: chain %q belongs to tenant %q, caller is %q",
			ErrChainMismatch, chainID, owner, tenantID)
	}
	return uint64(head), headHash, nil
}

func (s *PostgresStore) insertRecord(ctx context.Context, tx pgx.Tx, rec Record) error {
	payload, err := json.Marshal(normalizePayload(rec.Payload))
	if err != nil {
		return fmt.Errorf("evidence: marshal payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO evidence_record (`+recordColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		rec.ChainID, int64(rec.Sequence), rec.Scope.TenantID, rec.Scope.BrandID,
		rec.Scope.JurisdictionCode, rec.Scope.RegionCode,
		rec.OccurredAt, rec.RecordedAt, string(rec.Category), rec.Action,
		rec.SubjectType, rec.SubjectID,
		string(rec.Actor.Type), rec.Actor.ID, rec.Actor.DisplayName, rec.Actor.SessionID,
		rec.Actor.SourceIP, rec.Actor.OnBehalfOf,
		payload, rec.PayloadHash, rec.PrevHash, rec.RecordHash,
		rec.IdempotencyKey, rec.RetentionClass, rec.CorrelationID, rec.TraceID)
	if err != nil {
		return fmt.Errorf("evidence: insert record: %w", err)
	}
	return nil
}

// normalizePayload converts a payload to a form json.Marshal handles predictably.
// A nil payload becomes an empty object so that the column is never NULL and the
// hash of "no detail" is a defined value.
func normalizePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func (s *PostgresStore) findByKey(ctx context.Context, tx pgx.Tx, chainID, key string) (Record, bool, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+recordColumns+` FROM evidence_record WHERE chain_id = $1 AND idempotency_key = $2`,
		chainID, key)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("evidence: lookup idempotency key: %w", err)
	}
	return rec, true, nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, chainID string, sequence uint64) (Record, error) {
	var out Record
	err := s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT `+recordColumns+` FROM evidence_record WHERE chain_id = $1 AND sequence = $2`,
			chainID, int64(sequence))
		rec, err := scanRecord(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: chain %q sequence %d", ErrNotFound, chainID, sequence)
		}
		if err != nil {
			return fmt.Errorf("evidence: get record: %w", err)
		}
		out = rec
		return nil
	})
	return out, err
}

// FindByIdempotencyKey implements Store.
func (s *PostgresStore) FindByIdempotencyKey(ctx context.Context, chainID, key string) (Record, error) {
	var out Record
	err := s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rec, found, err := s.findByKey(ctx, tx, chainID, key)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: chain %q idempotency key %q", ErrNotFound, chainID, key)
		}
		out = rec
		return nil
	})
	return out, err
}

// Range implements Store.
func (s *PostgresStore) Range(ctx context.Context, chainID string, from, to uint64) ([]Record, error) {
	if from == 0 {
		from = 1
	}
	var out []Record
	err := s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		upper := int64(to)
		if to == 0 {
			// A sentinel is used rather than branching the SQL, so that the query
			// plan is identical for both forms.
			upper = int64(1) << 62
		}
		rows, err := tx.Query(ctx,
			`SELECT `+recordColumns+` FROM evidence_record
			 WHERE chain_id = $1 AND sequence >= $2 AND sequence <= $3
			 ORDER BY sequence ASC`,
			chainID, int64(from), upper)
		if err != nil {
			return fmt.Errorf("evidence: range query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanRecord(rows)
			if err != nil {
				return fmt.Errorf("evidence: scan range row: %w", err)
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	return out, err
}

// Head implements Store.
func (s *PostgresStore) Head(ctx context.Context, chainID string) (ChainHead, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return ChainHead{}, err
	}
	out := ChainHead{ChainID: chainID, TenantID: scope.TenantID, HeadHash: GenesisHash}
	err = s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var sequence int64
		var hash []byte
		var owner string
		var updatedAt time.Time
		qerr := tx.QueryRow(ctx,
			`SELECT head_sequence, head_hash, tenant_id, updated_at FROM evidence_chain WHERE chain_id = $1`,
			chainID).Scan(&sequence, &hash, &owner, &updatedAt)
		if errors.Is(qerr, pgx.ErrNoRows) {
			// Either the chain does not exist, or row-level security is hiding
			// another tenant's chain. Both are reported as empty: distinguishing
			// them would confirm the existence of another tenant's chain.
			return nil
		}
		if qerr != nil {
			return fmt.Errorf("evidence: read chain head: %w", qerr)
		}
		out.Sequence = uint64(sequence)
		out.HeadHash = hash
		out.TenantID = owner
		out.UpdatedAt = updatedAt.UTC()
		return nil
	})
	return out, err
}

// Checkpoint records the current chain head for external notarisation and returns
// it. The notary reference is supplied by the caller, which calls the notary before
// recording, so that a recorded checkpoint always corresponds to a completed
// notarisation.
func (s *PostgresStore) Checkpoint(ctx context.Context, chainID, notaryKind, notaryReference string) (ChainHead, error) {
	head, err := s.Head(ctx, chainID)
	if err != nil {
		return ChainHead{}, err
	}
	if head.IsEmpty() {
		return ChainHead{}, fmt.Errorf("evidence: chain %q has no records to checkpoint", chainID)
	}
	err = s.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, xerr := tx.Exec(ctx,
			`INSERT INTO evidence_checkpoint (chain_id, sequence, tenant_id, head_hash, notary_kind, notary_reference)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (chain_id, sequence) DO NOTHING`,
			chainID, int64(head.Sequence), head.TenantID, head.HeadHash, notaryKind, notaryReference)
		if xerr != nil {
			return fmt.Errorf("evidence: record checkpoint: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return ChainHead{}, err
	}
	return head, nil
}

// rowScanner covers both pgx.Row and pgx.Rows so that one scan function serves
// single-row and multi-row queries.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var (
		rec          Record
		sequence     int64
		category     string
		actorType    string
		payloadBytes []byte
		occurredAt   time.Time
		recordedAt   time.Time
	)
	err := row.Scan(
		&rec.ChainID, &sequence, &rec.Scope.TenantID, &rec.Scope.BrandID,
		&rec.Scope.JurisdictionCode, &rec.Scope.RegionCode,
		&occurredAt, &recordedAt, &category, &rec.Action,
		&rec.SubjectType, &rec.SubjectID,
		&actorType, &rec.Actor.ID, &rec.Actor.DisplayName, &rec.Actor.SessionID,
		&rec.Actor.SourceIP, &rec.Actor.OnBehalfOf,
		&payloadBytes, &rec.PayloadHash, &rec.PrevHash, &rec.RecordHash,
		&rec.IdempotencyKey, &rec.RetentionClass, &rec.CorrelationID, &rec.TraceID,
	)
	if err != nil {
		return Record{}, err
	}
	rec.Sequence = uint64(sequence)
	rec.Category = Category(category)
	rec.Actor.Type = identity.Type(actorType)
	rec.OccurredAt = occurredAt.UTC()
	rec.RecordedAt = recordedAt.UTC()

	payload, err := decodePayload(payloadBytes)
	if err != nil {
		return Record{}, err
	}
	rec.Payload = payload
	return rec, nil
}

// decodePayload reads a JSONB payload back into a map.
//
// UseNumber is essential rather than stylistic: the default decoder turns every
// JSON number into a float64, which would silently change 9007199254740993 into
// 9007199254740992 and make the record fail its own integrity check. json.Number
// preserves the digits, and the canonical encoder then requires them to be an
// integer.
func decodePayload(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("evidence: decode payload: %w", err)
	}
	if payload == nil {
		return map[string]any{}, nil
	}
	return payload, nil
}
