-- Ledger store schema (roadmap Task 7).
--
-- SAFETY INVARIANT 1: money is bigint minor units, never a floating-point
-- column — enforced structurally by the types below and statically by
-- scripts/check-invariants.sh.
--
-- Double-entry posture, three layers as everywhere else:
--   1. The Go port validates balance per currency before storage.
--   2. The deferred constraint trigger re-derives the net of every entry at
--      commit — a psql session that bypassed the port cannot post an
--      unbalanced entry.
--   3. INSERT-only grants make the journal append-only for the runtime roles;
--      the deny triggers below harden it further (ADR-0001: grants are the
--      portable control, triggers are PostgreSQL-specific defense-in-depth).
--
-- Balances are DERIVED (SUM over postings). There is deliberately no balance
-- column to drift from the journal.

-- Accounts: opened once, never altered. A changed kind or currency would
-- silently re-classify every past posting under it.
CREATE TABLE ledger_account (
    id                text        NOT NULL PRIMARY KEY,
    tenant_id         text        NOT NULL,
    brand_id          text        NOT NULL DEFAULT '',
    jurisdiction_code text        NOT NULL DEFAULT '',
    region_code       text        NOT NULL DEFAULT '',
    kind              text        NOT NULL,
    currency_code     text        NOT NULL,
    currency_scale    smallint    NOT NULL,
    -- Registry kind of the currency (fiat/crypto/bonus/...), stored so the
    -- full money.Currency can be reconstructed from a row — never for
    -- equality (that is code+scale), always for routing (which rail a payout
    -- uses is a kind question).
    currency_kind     text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ledger_account_tenant_not_empty CHECK (tenant_id <> ''),
    CONSTRAINT ledger_account_kind_not_empty CHECK (kind <> ''),
    CONSTRAINT ledger_account_currency_not_empty CHECK (currency_code <> ''),
    CONSTRAINT ledger_account_scale_range CHECK (currency_scale BETWEEN 0 AND 18)
);

COMMENT ON TABLE ledger_account IS
    'Ledger account registry. Immutable by trigger and by INSERT-only grants; a "changed" account is a new account.';

-- Journal entries.
CREATE TABLE ledger_entry (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id        text        NOT NULL,
    brand_id         text        NOT NULL DEFAULT '',
    jurisdiction_code text       NOT NULL DEFAULT '',
    region_code      text        NOT NULL DEFAULT '',
    idempotency_key  text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    reason           text        NOT NULL DEFAULT '',
    occurred_at      timestamptz NOT NULL,
    recorded_at      timestamptz NOT NULL,

    CONSTRAINT ledger_entry_idempotency_unique UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT ledger_entry_tenant_not_empty CHECK (tenant_id <> ''),
    CONSTRAINT ledger_entry_key_not_empty CHECK (idempotency_key <> '')
);

COMMENT ON TABLE ledger_entry IS
    'One balanced group of postings. The public ID is ''e'' || id.';

-- Postings: signed minor units.
CREATE TABLE ledger_posting (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id     bigint      NOT NULL REFERENCES ledger_entry (id),
    account_id   text        NOT NULL REFERENCES ledger_account (id),
    tenant_id    text        NOT NULL,
    currency_code text       NOT NULL,
    minor_units  bigint      NOT NULL,

    -- A zero posting is a no-op that would pollute derived balances with
    -- noise every reader has to strip out.
    CONSTRAINT ledger_posting_nonzero CHECK (minor_units <> 0)
);

COMMENT ON TABLE ledger_posting IS
    'Signed movement between one account and an entry''s other legs. Append-only.';

CREATE INDEX ledger_posting_entry_idx    ON ledger_posting (entry_id);
CREATE INDEX ledger_posting_account_idx  ON ledger_posting (account_id);
CREATE INDEX ledger_posting_tenant_time  ON ledger_posting (tenant_id, entry_id);

-- Deny mutation on journal structures.
CREATE FUNCTION ledger_deny_mutation() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'table % is append-only: % is not permitted (ledger corrections are reversing entries, SAFETY INVARIANT 1)',
        TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

COMMENT ON FUNCTION ledger_deny_mutation() IS
    'Blocks UPDATE/DELETE/TRUNCATE on ledger tables. Removing it is a safety-invariant breach.';

CREATE TRIGGER ledger_account_deny_mutation
    BEFORE UPDATE OR DELETE ON ledger_account
    FOR EACH ROW EXECUTE FUNCTION ledger_deny_mutation();
CREATE TRIGGER ledger_account_deny_truncate
    BEFORE TRUNCATE ON ledger_account
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_deny_mutation();

CREATE TRIGGER ledger_entry_deny_mutation
    BEFORE UPDATE OR DELETE ON ledger_entry
    FOR EACH ROW EXECUTE FUNCTION ledger_deny_mutation();
CREATE TRIGGER ledger_entry_deny_truncate
    BEFORE TRUNCATE ON ledger_entry
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_deny_mutation();

CREATE TRIGGER ledger_posting_deny_mutation
    BEFORE UPDATE OR DELETE ON ledger_posting
    FOR EACH ROW EXECUTE FUNCTION ledger_deny_mutation();
CREATE TRIGGER ledger_posting_deny_truncate
    BEFORE TRUNCATE ON ledger_posting
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_deny_mutation();

-- Posting attributes must match its entry's scope: the RLS policies key on the
-- posting's own tenant_id, and drift there would make a row visible to a
-- tenant that never owned it.
CREATE FUNCTION ledger_posting_scope_matches_entry() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE entry_tenant text;
BEGIN
    SELECT tenant_id INTO entry_tenant FROM ledger_entry WHERE id = NEW.entry_id;
    IF NEW.tenant_id IS DISTINCT FROM entry_tenant THEN
        RAISE EXCEPTION 'ledger posting tenant % does not match entry tenant %',
            NEW.tenant_id, entry_tenant
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER ledger_posting_scope_match
    BEFORE INSERT ON ledger_posting
    FOR EACH ROW EXECUTE FUNCTION ledger_posting_scope_matches_entry();

-- The balance invariant at the database level: deferred so multi-row inserts
-- commit atomically, evaluated at COMMIT, rejecting any entry whose postings
-- net non-zero for any currency — regardless of which client attempted it.
CREATE FUNCTION ledger_entry_balanced() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    net   bigint;
    code  text;
BEGIN
    FOR code IN
        SELECT DISTINCT currency_code FROM ledger_posting WHERE entry_id = NEW.entry_id
    LOOP
        SELECT COALESCE(SUM(minor_units), 0) INTO net
            FROM ledger_posting WHERE entry_id = NEW.entry_id AND currency_code = code;
        IF net <> 0 THEN
            RAISE EXCEPTION 'ledger entry % does not balance in % (net % minor units, want 0)',
                NEW.entry_id, code, net
                USING ERRCODE = 'check_violation';
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;

COMMENT ON FUNCTION ledger_entry_balanced() IS
    'Deferred constraint: every ledger entry must net to zero per currency at COMMIT. SAFETY INVARIANT 1''s database-level enforcement.';

CREATE CONSTRAINT TRIGGER ledger_entry_balance_check
    AFTER INSERT ON ledger_posting
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_entry_balanced();

-- Tenant isolation at the database layer, same posture as the evidence store:
-- FORCE so the table owner is subject, and deny-by-default through the SQL
-- semantics of current_setting(..., true) returning NULL when unset.
ALTER TABLE ledger_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_account FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_entry FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_posting ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_posting FORCE ROW LEVEL SECURITY;

CREATE POLICY ledger_account_tenant_isolation ON ledger_account
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY ledger_entry_tenant_isolation ON ledger_entry
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY ledger_posting_tenant_isolation ON ledger_posting
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
