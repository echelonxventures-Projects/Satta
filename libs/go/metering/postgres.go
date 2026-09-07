package metering

import (
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
	"github.com/satta/platform/libs/go/tenancy"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the metering schema's migrations. The package owns its own schema so
// that the immutability triggers travel with the code whose guarantees depend on them.
func Migrations() (fs.FS, string) { return migrationsFS, "migrations" }

// PostgresEmitter is the durable usage emitter.
//
// It implements both Emitter and TxEmitter. TxEmitter is the interface services doing billable
// work should reach for: a billable action and its usage event must commit or fail together.
// Emitting over the network after the action commits means a crash in between produces unbilled
// usage; emitting before means billing for work that never happened. Neither is recoverable by
// retry, because neither leaves a record of what was missed.
type PostgresEmitter struct {
	pool  *db.Pool
	clock clock.Clock
}

// NewPostgresEmitter returns an emitter backed by pool.
func NewPostgresEmitter(pool *db.Pool, clk clock.Clock) *PostgresEmitter {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &PostgresEmitter{pool: pool, clock: clk}
}

var (
	_ Emitter   = (*PostgresEmitter)(nil)
	_ TxEmitter = (*PostgresEmitter)(nil)
)

// Migrate applies the metering schema.
func (e *PostgresEmitter) Migrate(ctx context.Context) error {
	fsys, dir := Migrations()
	_, err := db.MigrateFS(ctx, e.pool, fsys, dir)
	return err
}

const insertUsageSQL = `
INSERT INTO usage_event (
    event_id, tenant_id, brand_id, jurisdiction_code, region_code,
    product_code, meter_code, quantity, unit,
    occurred_at, recorded_at, dimensions,
    source_service, source_version, correlation_id, subject_type, subject_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (event_id) DO NOTHING`

// Emit implements Emitter.
func (e *PostgresEmitter) Emit(ctx context.Context, event UsageEvent) (EmitResult, error) {
	results, err := e.EmitBatch(ctx, []UsageEvent{event})
	if err != nil {
		return EmitResult{}, err
	}
	return results[0], nil
}

// EmitBatch implements Emitter. The whole batch commits or none of it does.
func (e *PostgresEmitter) EmitBatch(ctx context.Context, events []UsageEvent) ([]EmitResult, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("metering: EmitBatch requires at least one event")
	}
	if err := e.validateBatch(ctx, events); err != nil {
		return nil, err
	}

	var results []EmitResult
	err := e.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		out := make([]EmitResult, 0, len(events))
		for i := range events {
			result, err := e.insert(ctx, tx, events[i])
			if err != nil {
				return fmt.Errorf("metering: event %d: %w", i, err)
			}
			out = append(out, result)
		}
		results = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// EmitTx implements TxEmitter, recording usage inside a transaction the caller owns.
//
// The caller is responsible for having established tenancy on that transaction — normally by
// obtaining it through Pool.InTenantTx. The scope is still validated here against the context,
// because a mismatch between the transaction's tenant and the event's tenant would otherwise be
// caught only by row-level security, and only if RLS happens to be enabled.
func (e *PostgresEmitter) EmitTx(ctx context.Context, tx pgx.Tx, event UsageEvent) (EmitResult, error) {
	if err := e.validateBatch(ctx, []UsageEvent{event}); err != nil {
		return EmitResult{}, err
	}
	return e.insert(ctx, tx, event)
}

// validateBatch applies the same checks the in-memory emitter applies, in the same order, so
// that a test passing against the double is not passing for a reason the database would reject.
func (e *PostgresEmitter) validateBatch(ctx context.Context, events []UsageEvent) error {
	if _, err := tenancy.RequireFromContext(ctx); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(events))
	for i := range events {
		if err := events[i].Validate(); err != nil {
			return fmt.Errorf("metering: event %d: %w", i, err)
		}
		if err := tenancy.CheckAccess(ctx, events[i].Scope); err != nil {
			return err
		}
		if _, dup := seen[events[i].EventID]; dup {
			// A repeated ID inside one batch is a caller bug, not a retry. Left to the
			// database it would silently collapse into one row via ON CONFLICT, and the
			// caller would believe both were recorded.
			return fmt.Errorf("%w: event_id %q appears twice in one batch",
				ErrInvalidEvent, events[i].EventID)
		}
		seen[events[i].EventID] = struct{}{}
	}
	return nil
}

func (e *PostgresEmitter) insert(ctx context.Context, tx pgx.Tx, event UsageEvent) (EmitResult, error) {
	normalized := event.Normalize()

	dimensions, err := json.Marshal(normalizeDimensions(normalized.Dimensions))
	if err != nil {
		return EmitResult{}, fmt.Errorf("marshal dimensions: %w", err)
	}

	tag, err := tx.Exec(ctx, insertUsageSQL,
		normalized.EventID,
		normalized.Scope.TenantID, normalized.Scope.BrandID,
		normalized.Scope.JurisdictionCode, normalized.Scope.RegionCode,
		normalized.ProductCode, normalized.MeterCode,
		normalized.Quantity, normalized.Unit,
		normalized.OccurredAt, e.clock.Now(),
		dimensions,
		normalized.SourceService, normalized.SourceVersion,
		normalized.CorrelationID, normalized.SubjectType, normalized.SubjectID,
	)
	if err != nil {
		return EmitResult{}, fmt.Errorf("insert usage event: %w", err)
	}

	// Zero rows affected means ON CONFLICT DO NOTHING suppressed the insert, so the event ID
	// was already present. That is the idempotent path, not an error: the stored event stands.
	if tag.RowsAffected() == 0 {
		return EmitResult{EventID: normalized.EventID, Outcome: OutcomeDuplicate}, nil
	}
	return EmitResult{EventID: normalized.EventID, Outcome: OutcomeAccepted}, nil
}

// normalizeDimensions ensures the column is never NULL, so that a query filtering on a
// dimension key does not have to special-case absence.
func normalizeDimensions(dimensions map[string]string) map[string]string {
	if dimensions == nil {
		return map[string]string{}
	}
	return dimensions
}

// Get returns a recorded usage event by ID.
func (e *PostgresEmitter) Get(ctx context.Context, eventID string) (UsageEvent, error) {
	var out UsageEvent
	err := e.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var (
			dimensions []byte
			occurredAt time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT event_id, tenant_id, brand_id, jurisdiction_code, region_code,
			       product_code, meter_code, quantity, unit, occurred_at, dimensions,
			       source_service, source_version, correlation_id, subject_type, subject_id
			  FROM usage_event WHERE event_id = $1`, eventID).Scan(
			&out.EventID, &out.Scope.TenantID, &out.Scope.BrandID,
			&out.Scope.JurisdictionCode, &out.Scope.RegionCode,
			&out.ProductCode, &out.MeterCode, &out.Quantity, &out.Unit,
			&occurredAt, &dimensions,
			&out.SourceService, &out.SourceVersion, &out.CorrelationID,
			&out.SubjectType, &out.SubjectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: event_id %q", ErrNotFound, eventID)
		}
		if err != nil {
			return fmt.Errorf("metering: get usage event: %w", err)
		}
		out.OccurredAt = occurredAt.UTC()
		if len(dimensions) > 0 {
			if err := json.Unmarshal(dimensions, &out.Dimensions); err != nil {
				return fmt.Errorf("metering: decode dimensions: %w", err)
			}
		}
		return nil
	})
	return out, err
}

// MarkRated records that an event has been rated, after which it is immutable.
//
// The rating engine owns this transition. It is a single UPDATE guarded by
// `rated_at IS NULL`, so a concurrent second rating affects zero rows and is reported as an
// immutability error rather than silently overwriting the first rating's reference
// (SAFETY INVARIANT 7).
func (e *PostgresEmitter) MarkRated(ctx context.Context, eventID, ratingReference string) error {
	return e.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE usage_event
			   SET rated_at = $2, rating_reference = $3
			 WHERE event_id = $1 AND rated_at IS NULL`,
			eventID, e.clock.Now(), ratingReference)
		if err != nil {
			return fmt.Errorf("metering: mark rated: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Either the event does not exist or it is already rated. Distinguished with a
			// follow-up read so the caller gets an actionable error.
			var alreadyRated bool
			err := tx.QueryRow(ctx,
				`SELECT rated_at IS NOT NULL FROM usage_event WHERE event_id = $1`,
				eventID).Scan(&alreadyRated)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: event_id %q", ErrNotFound, eventID)
			}
			if err != nil {
				return fmt.Errorf("metering: mark rated: %w", err)
			}
			return fmt.Errorf("%w: event_id %q is already rated", ErrImmutable, eventID)
		}
		return nil
	})
}

// UnpublishedBatch returns events awaiting publication to the event bus, oldest first.
//
// This is the outbox drain. Usage is written in the same transaction as the billable action and
// published afterwards, rather than published inline — an inline publish inside the business
// transaction couples committing a bet to the availability of the message broker.
func (e *PostgresEmitter) UnpublishedBatch(ctx context.Context, limit int) ([]UsageEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []UsageEvent
	err := e.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT event_id, tenant_id, brand_id, jurisdiction_code, region_code,
			       product_code, meter_code, quantity, unit, occurred_at,
			       source_service, source_version, correlation_id, subject_type, subject_id
			  FROM usage_event
			 WHERE published_at IS NULL
			 ORDER BY recorded_at
			 LIMIT $1`, limit)
		if err != nil {
			return fmt.Errorf("metering: read outbox: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				event      UsageEvent
				occurredAt time.Time
			)
			if err := rows.Scan(
				&event.EventID, &event.Scope.TenantID, &event.Scope.BrandID,
				&event.Scope.JurisdictionCode, &event.Scope.RegionCode,
				&event.ProductCode, &event.MeterCode, &event.Quantity, &event.Unit,
				&occurredAt, &event.SourceService, &event.SourceVersion,
				&event.CorrelationID, &event.SubjectType, &event.SubjectID); err != nil {
				return fmt.Errorf("metering: scan outbox row: %w", err)
			}
			event.OccurredAt = occurredAt.UTC()
			out = append(out, event)
		}
		return rows.Err()
	})
	return out, err
}

// MarkPublished records that events have reached the event bus.
func (e *PostgresEmitter) MarkPublished(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	return e.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE usage_event SET published_at = $2 WHERE event_id = ANY($1) AND published_at IS NULL`,
			eventIDs, e.clock.Now())
		if err != nil {
			return fmt.Errorf("metering: mark published: %w", err)
		}
		return nil
	})
}
