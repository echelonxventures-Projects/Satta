-- Metered usage storage.
--
-- SAFETY INVARIANT 7: once rated, usage records are immutable; entitlements are
-- not tenant-bypassable; billing cannot mutate the gaming ledger.
--
-- The third clause is enforced by absence: there is no foreign key, column or view
-- here that references a ledger account, and the rating engine's write path is a
-- separate schema. Nothing in this file can post to the ledger.

CREATE TABLE usage_event (
    -- Also the idempotency key. A primary key rather than a unique index so that a
    -- duplicate emission is rejected by the most fundamental constraint the table
    -- has.
    event_id          text        NOT NULL PRIMARY KEY,

    tenant_id         text        NOT NULL,
    brand_id          text        NOT NULL DEFAULT '',
    jurisdiction_code text        NOT NULL DEFAULT '',
    region_code       text        NOT NULL DEFAULT '',

    product_code      text        NOT NULL,
    meter_code        text        NOT NULL,

    -- Integer quantity. There is deliberately no numeric or double precision
    -- column here: finer granularity is expressed by choosing a finer unit.
    quantity          bigint      NOT NULL,
    unit              text        NOT NULL,

    -- SAFETY INVARIANT 12: UTC instants. occurred_at determines the billing
    -- period, so it is the action's time, not the emission's.
    occurred_at       timestamptz NOT NULL,
    recorded_at       timestamptz NOT NULL DEFAULT now(),

    dimensions        jsonb       NOT NULL DEFAULT '{}'::jsonb,

    source_service    text        NOT NULL,
    source_version    text        NOT NULL DEFAULT '',
    correlation_id    text        NOT NULL DEFAULT '',
    subject_type      text        NOT NULL DEFAULT '',
    subject_id        text        NOT NULL DEFAULT '',

    -- Rating state. Set once, by the rating engine. Non-null rated_at is the point
    -- after which the row is frozen.
    rated_at          timestamptz,
    rating_reference  text        NOT NULL DEFAULT '',

    -- Outbox publication state for the downstream event bus.
    published_at      timestamptz,

    CONSTRAINT usage_event_quantity_non_negative CHECK (quantity >= 0),
    CONSTRAINT usage_event_tenant_not_empty CHECK (tenant_id <> ''),
    CONSTRAINT usage_event_product_not_empty CHECK (product_code <> ''),
    CONSTRAINT usage_event_meter_not_empty CHECK (meter_code <> ''),
    CONSTRAINT usage_event_unit_not_empty CHECK (unit <> ''),
    CONSTRAINT usage_event_source_not_empty CHECK (source_service <> ''),
    CONSTRAINT usage_event_rating_reference_requires_rated
        CHECK (rating_reference = '' OR rated_at IS NOT NULL)
);

COMMENT ON TABLE usage_event IS
    'Metered billable usage. Immutable once rated_at is set. Carries no monetary amount and no ledger reference.';
COMMENT ON COLUMN usage_event.event_id IS
    'Idempotency key, derived deterministically from the metered action so retries do not double-bill.';

-- Billing period aggregation, and the outbox drain.
CREATE INDEX usage_event_billing_idx
    ON usage_event (tenant_id, product_code, meter_code, occurred_at);
CREATE INDEX usage_event_unrated_idx
    ON usage_event (occurred_at) WHERE rated_at IS NULL;
CREATE INDEX usage_event_unpublished_idx
    ON usage_event (recorded_at) WHERE published_at IS NULL;
CREATE INDEX usage_event_subject_idx
    ON usage_event (tenant_id, subject_type, subject_id) WHERE subject_id <> '';

-- Immutability enforcement.
CREATE FUNCTION usage_event_enforce_immutability() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION
            'usage_event is append-only: DELETE is not permitted (SAFETY INVARIANT 7). Issue a billing adjustment instead.'
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- Once rated, nothing may change. Rated usage has been priced and may already
    -- appear on an issued invoice; changing it would silently desynchronise the
    -- invoice from the usage it was computed from.
    IF OLD.rated_at IS NOT NULL THEN
        RAISE EXCEPTION
            'usage_event % was rated at % and is immutable (SAFETY INVARIANT 7)',
            OLD.event_id, OLD.rated_at
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- Before rating, only the rating and publication columns may move. Everything
    -- describing what happened is fixed at emission.
    IF NEW.event_id          <> OLD.event_id
       OR NEW.tenant_id      <> OLD.tenant_id
       OR NEW.brand_id       <> OLD.brand_id
       OR NEW.jurisdiction_code <> OLD.jurisdiction_code
       OR NEW.region_code    <> OLD.region_code
       OR NEW.product_code   <> OLD.product_code
       OR NEW.meter_code     <> OLD.meter_code
       OR NEW.quantity       <> OLD.quantity
       OR NEW.unit           <> OLD.unit
       OR NEW.occurred_at    <> OLD.occurred_at
       OR NEW.recorded_at    <> OLD.recorded_at
       OR NEW.dimensions     <> OLD.dimensions
       OR NEW.source_service <> OLD.source_service
       OR NEW.source_version <> OLD.source_version
       OR NEW.correlation_id <> OLD.correlation_id
       OR NEW.subject_type   <> OLD.subject_type
       OR NEW.subject_id     <> OLD.subject_id
    THEN
        RAISE EXCEPTION
            'usage_event % describes a recorded action and cannot be edited; only rated_at, rating_reference and published_at may be set (SAFETY INVARIANT 7)',
            OLD.event_id
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- Rating happens once.
    IF NEW.rated_at IS NULL AND OLD.rated_at IS NOT NULL THEN
        RAISE EXCEPTION 'usage_event % cannot be un-rated', OLD.event_id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION usage_event_enforce_immutability() IS
    'Enforces SAFETY INVARIANT 7. Removing or weakening this function is a safety-invariant breach.';

CREATE TRIGGER usage_event_immutable
    BEFORE UPDATE OR DELETE ON usage_event
    FOR EACH ROW EXECUTE FUNCTION usage_event_enforce_immutability();

CREATE FUNCTION usage_event_deny_truncate() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'usage_event cannot be truncated (SAFETY INVARIANT 7)'
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER usage_event_deny_truncate
    BEFORE TRUNCATE ON usage_event
    FOR EACH STATEMENT EXECUTE FUNCTION usage_event_deny_truncate();

-- Tenant isolation.
ALTER TABLE usage_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_event FORCE ROW LEVEL SECURITY;

CREATE POLICY usage_event_tenant_isolation ON usage_event
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
