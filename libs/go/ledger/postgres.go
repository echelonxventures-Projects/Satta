package ledger

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/db"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations exposes the ledger-owned migration set to platformctl and tests,
// the same pattern the evidence and metering stores use.
func Migrations() (fs.FS, string) { return migrationsFS, "migrations" }

// PostgresLedger implements Ledger on PostgreSQL.
//
// Concurrency: an entry locks its accounts (deterministic order — unordered
// locking is a deadlock generator the first time two transfers share an
// account in opposite directions). The locking and the derived-balance read
// are in one transaction, so the overdraft decision cannot go stale under a
// concurrent post.
type PostgresLedger struct {
	pool *db.Pool
	clk  clock.Clock
}

// NewPostgresLedger wraps a pool with an injected clock (SAFETY INVARIANT 12).
func NewPostgresLedger(pool *db.Pool, clk clock.Clock) *PostgresLedger {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &PostgresLedger{pool: pool, clk: clk}
}

var _ Ledger = (*PostgresLedger)(nil)

// Migrate applies ledger-owned schema migrations.
func (l *PostgresLedger) Migrate(ctx context.Context) error {
	_, err := db.MigrateFS(ctx, l.pool, "ledger", migrationsFS, "migrations")
	return err
}

// OpenAccount implements Ledger. ON CONFLICT DO NOTHING makes re-opening an
// identical account a no-op; a conflicting definition is then detected by
// comparing the existing row.
func (l *PostgresLedger) OpenAccount(ctx context.Context, acct Account) error {
	if acct.ID == "" || acct.Currency.IsZero() {
		return errors.New("ledger: account needs an ID and a currency")
	}
	if err := acct.Scope.Validate(); err != nil {
		return err
	}
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return err
	}
	return l.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, ierr := tx.Exec(ctx,
			`INSERT INTO ledger_account (id, tenant_id, brand_id, jurisdiction_code, region_code, kind, currency_code, currency_scale, currency_kind)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 ON CONFLICT (id) DO NOTHING`,
			acct.ID, scope.TenantID, acct.Scope.BrandID, acct.Scope.JurisdictionCode, acct.Scope.RegionCode,
			string(acct.Kind), acct.Currency.Code, int16(acct.Currency.Scale), string(acct.Currency.Kind))
		if ierr != nil {
			return fmt.Errorf("ledger: open account %s: %w", acct.ID, ierr)
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		var kind, code, curKind string
		var scale int16
		if serr := tx.QueryRow(ctx,
			`SELECT kind, currency_code, currency_scale, currency_kind FROM ledger_account WHERE id = $1`, acct.ID,
		).Scan(&kind, &code, &scale, &curKind); serr != nil {
			return fmt.Errorf("ledger: read existing account %s: %w", acct.ID, serr)
		}
		if Kind(kind) != acct.Kind || code != acct.Currency.Code || int(scale) != int(acct.Currency.Scale) || money.Kind(curKind) != acct.Currency.Kind {
			return fmt.Errorf("ledger: account %s already opened with a different definition", acct.ID)
		}
		return nil
	})
}

// Post implements Ledger: validate, lock accounts in sorted order, derive
// current balances, check business rules, write.
func (l *PostgresLedger) Post(ctx context.Context, entry Entry) (Entry, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return Entry{}, err
	}
	if verr := entry.Validate(); verr != nil {
		return Entry{}, verr
	}

	var out Entry
	err = l.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Structural pass first (accounts, currency, tenant) in deterministic
		// lock order — same ordering rule as any two-phase resource: lock
		// order must be a function of the data, never of the caller's loop.
		ids := make([]string, 0, len(entry.Postings))
		for _, p := range entry.Postings {
			ids = append(ids, p.AccountID)
		}
		sort.Strings(ids)
		accounts := map[string]Account{}
		var prev string
		for _, id := range ids {
			if id == prev {
				continue // one lock per distinct account
			}
			prev = id
			acct, ferr := lockAccount(ctx, tx, id)
			if ferr != nil {
				return ferr
			}
			accounts[id] = acct
		}
		for _, p := range entry.Postings {
			acct := accounts[p.AccountID]
			if acct.Currency.Code != p.Amount.Currency().Code || acct.Currency.Scale != p.Amount.Currency().Scale {
				return fmt.Errorf("ledger: posting %s currency %s does not match account %s",
					p.AccountID, p.Amount.Currency().Code, acct.Currency.Code)
			}
			if acct.Scope.TenantID != scope.TenantID {
				return fmt.Errorf("%w: posting to account of tenant %q by tenant %q",
					tenancy.ErrScopeMismatch, acct.Scope.TenantID, scope.TenantID)
			}
		}

		// Idempotency check before any write (same rule as memory: a retry
		// must observe the committed original, never race ahead of it).
		var existingID int64
		derr := tx.QueryRow(ctx,
			`SELECT id FROM ledger_entry WHERE tenant_id = $1 AND idempotency_key = $2`,
			scope.TenantID, entry.IdempotencyKey).Scan(&existingID)
		if derr == nil {
			existing, gerr := readEntry(ctx, tx, existingID)
			if gerr != nil {
				return gerr
			}
			if !samePostings(existing.Postings, entry.Postings) {
				return fmt.Errorf("%w: key %q re-posted with different postings", ErrDuplicateKey, entry.IdempotencyKey)
			}
			out = existing
			return nil
		}
		if !errors.Is(derr, pgx.ErrNoRows) {
			return fmt.Errorf("ledger: idempotency lookup: %w", derr)
		}

		// Derived balances + overdraft, inside the lock.
		balances := map[string]money.Amount{} // account|currency
		for _, p := range entry.Postings {
			acct := accounts[p.AccountID]
			key := p.AccountID + "|" + p.Amount.Currency().Code
			if _, done := balances[key]; done {
				continue
			}
			var minor int64
			if berr := tx.QueryRow(ctx,
				`SELECT COALESCE(SUM(minor_units), 0) FROM ledger_posting WHERE account_id = $1 AND currency_code = $2`,
				p.AccountID, p.Amount.Currency().Code).Scan(&minor); berr != nil {
				return fmt.Errorf("ledger: derive balance %s: %w", p.AccountID, berr)
			}
			balances[key] = money.MustNew(minor, acct.Currency)
		}
		projected := map[string]money.Amount{}
		for _, p := range entry.Postings {
			acct := accounts[p.AccountID]
			key := p.AccountID + "|" + p.Amount.Currency().Code
			base, seen := projected[key]
			if !seen {
				base = balances[key]
			}
			next, aerr := base.Add(p.Amount)
			if aerr != nil {
				return aerr
			}
			if next.IsNegative() && refusesNegative(acct.Kind) {
				return fmt.Errorf("%w: %s on %s would go negative", ErrOverdraft, p.AccountID, acct.Kind)
			}
			projected[key] = next
		}

		recordedAt := l.clk.Now().UTC()
		// ON CONFLICT rather than a caught 23505: a unique violation aborts
		// the whole transaction in PostgreSQL, and re-reading the winner
		// inside that aborted tx is impossible. A clean no-op insert leaves
		// the transaction alive and the winner visible.
		var id int64
		ierr := tx.QueryRow(ctx,
			`INSERT INTO ledger_entry (tenant_id, brand_id, jurisdiction_code, region_code, idempotency_key, description, reason, occurred_at, recorded_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
			 RETURNING id`,
			scope.TenantID, scope.BrandID, scope.JurisdictionCode, scope.RegionCode,
			entry.IdempotencyKey, entry.Description, entry.Reason, entry.OccurredAt.UTC(), recordedAt,
		).Scan(&id)
		if errors.Is(ierr, pgx.ErrNoRows) {
			// Concurrent identical post won; apply the idempotency rule.
			var winnerID int64
			if lerr := tx.QueryRow(ctx,
				`SELECT id FROM ledger_entry WHERE tenant_id = $1 AND idempotency_key = $2`,
				scope.TenantID, entry.IdempotencyKey).Scan(&winnerID); lerr != nil {
				return fmt.Errorf("ledger: post race lookup %s: %w", entry.IdempotencyKey, lerr)
			}
			existing, gerr := readEntry(ctx, tx, winnerID)
			if gerr != nil {
				return gerr
			}
			if !samePostings(existing.Postings, entry.Postings) {
				return fmt.Errorf("%w: key %q re-posted with different postings", ErrDuplicateKey, entry.IdempotencyKey)
			}
			out = existing
			return nil
		}
		if ierr != nil {
			return fmt.Errorf("ledger: post entry %s: %w", entry.IdempotencyKey, ierr)
		}

		for _, p := range entry.Postings {
			if _, perr := tx.Exec(ctx,
				`INSERT INTO ledger_posting (entry_id, account_id, tenant_id, currency_code, minor_units)
				 VALUES ($1, $2, $3, $4, $5)`,
				id, p.AccountID, scope.TenantID, p.Amount.Currency().Code, p.Amount.Minor()); perr != nil {
				return fmt.Errorf("ledger: post %s leg %s: %w", entry.IdempotencyKey, p.AccountID, perr)
			}
		}

		stamped, rerr := readEntry(ctx, tx, id)
		if rerr != nil {
			return rerr
		}
		out = stamped
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}

// Balance implements Ledger: derived at read time, never cached as truth.
func (l *PostgresLedger) Balance(ctx context.Context, accountID string) (money.Amount, error) {
	if _, err := tenancy.RequireFromContext(ctx); err != nil {
		return money.Amount{}, err
	}
	var out money.Amount
	err := l.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var code, curKind string
		var scale int16
		if err := tx.QueryRow(ctx,
			`SELECT currency_code, currency_scale, currency_kind FROM ledger_account WHERE id = $1`, accountID,
		).Scan(&code, &scale, &curKind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: account %s", ErrNoSuchAccount, accountID)
			}
			return err
		}
		var minor int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(minor_units), 0) FROM ledger_posting WHERE account_id = $1 AND currency_code = $2`,
			accountID, code).Scan(&minor); err != nil {
			return err
		}
		cur := money.Currency{Code: code, Scale: uint8(scale), Kind: money.Kind(curKind)}
		amt, err := money.New(minor, cur)
		if err != nil {
			return err
		}
		out = amt
		return nil
	})
	if err != nil {
		return money.Amount{}, err
	}
	return out, nil
}

// Entry implements Ledger.
func (l *PostgresLedger) Entry(ctx context.Context, id string) (Entry, error) {
	if _, err := tenancy.RequireFromContext(ctx); err != nil {
		return Entry{}, err
	}
	num, err := parseEntryID(id)
	if err != nil {
		return Entry{}, err
	}
	var out Entry
	err = l.pool.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var gerr error
		out, gerr = readEntry(ctx, tx, num)
		return gerr
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}

// ---- internals ---------------------------------------------------------------

func lockAccount(ctx context.Context, tx pgx.Tx, id string) (Account, error) {
	var (
		tenant, brand, jur, region, kind, code, curKind string
		scale                                           int16
	)
	err := tx.QueryRow(ctx,
		`SELECT tenant_id, brand_id, jurisdiction_code, region_code, kind, currency_code, currency_scale, currency_kind
		 FROM ledger_account WHERE id = $1 FOR UPDATE`, id,
	).Scan(&tenant, &brand, &jur, &region, &kind, &code, &scale, &curKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, fmt.Errorf("%w: %s", ErrNoSuchAccount, id)
	}
	if err != nil {
		return Account{}, fmt.Errorf("ledger: lock account %s: %w", id, err)
	}
	return Account{
		ID: id, Kind: Kind(kind),
		Currency: money.Currency{Code: code, Scale: uint8(scale), Kind: money.Kind(curKind)},
		Scope:    tenancy.Scope{TenantID: tenant, BrandID: brand, JurisdictionCode: jur, RegionCode: region},
	}, nil
}

func readEntry(ctx context.Context, tx pgx.Tx, id int64) (Entry, error) {
	var (
		e                          Entry
		tenant, brand, jur, region string
		occurred, rec              time.Time
	)
	row := tx.QueryRow(ctx,
		`SELECT tenant_id, brand_id, jurisdiction_code, region_code, idempotency_key, description, reason, occurred_at, recorded_at
		 FROM ledger_entry WHERE id = $1`, id)
	if err := row.Scan(&tenant, &brand, &jur, &region, &e.IdempotencyKey, &e.Description, &e.Reason, &occurred, &rec); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Entry{}, fmt.Errorf("%w: entry %d", ErrNotFound, id)
		}
		return Entry{}, fmt.Errorf("ledger: read entry %d: %w", id, err)
	}
	e.ID = fmt.Sprintf("e%d", id)
	e.OccurredAt = occurred.UTC()
	e.RecordedAt = rec.UTC()
	rows, err := tx.Query(ctx,
		`SELECT p.account_id, p.currency_code, p.minor_units, a.currency_scale, a.currency_kind
		 FROM ledger_posting p JOIN ledger_account a ON a.id = p.account_id
		 WHERE p.entry_id = $1 ORDER BY p.id`, id)
	if err != nil {
		return Entry{}, fmt.Errorf("ledger: postings of %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			account, code, curKind string
			minor                  int64
			scale                  int16
		)
		if err := rows.Scan(&account, &code, &minor, &scale, &curKind); err != nil {
			return Entry{}, err
		}
		amt, err := money.New(minor, money.Currency{Code: code, Scale: uint8(scale), Kind: money.Kind(curKind)})
		if err != nil {
			return Entry{}, err
		}
		e.Postings = append(e.Postings, Posting{AccountID: account, Amount: amt})
	}
	if err := rows.Err(); err != nil {
		return Entry{}, err
	}
	// A second statement on the same tx would leave this connection busy
	// mid-iteration if anything below ever queried again; drain first.
	return e, nil
}

func parseEntryID(id string) (int64, error) {
	rest, found := strings.CutPrefix(id, "e")
	if !found || rest == "" {
		return 0, fmt.Errorf("%w: entry id %q", ErrNotFound, id)
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: entry id %q", ErrNotFound, id)
	}
	return n, nil
}
