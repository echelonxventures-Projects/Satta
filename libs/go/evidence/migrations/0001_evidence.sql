-- Evidence store schema.
--
-- SAFETY INVARIANT 4: append-only, tamper-evident, no deletion path within the
-- retention period.
--
-- The Go port exposes no mutation method, but a port only constrains code that
-- goes through it. The triggers below constrain anything holding a psql session,
-- including platform staff and an attacker who has obtained database credentials.
-- Neither layer replaces the hash chain, which is what makes tampering detectable
-- after the fact rather than merely difficult.

-- Chain heads. One row per chain; appends serialise on this row, which is why
-- chains are partitioned per tenant rather than being global.
CREATE TABLE evidence_chain (
    chain_id      text        NOT NULL PRIMARY KEY,
    tenant_id     text        NOT NULL,
    head_sequence bigint      NOT NULL DEFAULT 0,
    head_hash     bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT evidence_chain_head_hash_length CHECK (octet_length(head_hash) = 32),
    CONSTRAINT evidence_chain_sequence_non_negative CHECK (head_sequence >= 0),
    CONSTRAINT evidence_chain_tenant_not_empty CHECK (tenant_id <> '')
);

COMMENT ON TABLE evidence_chain IS
    'Head pointer per evidence chain. head_hash is the anchor published to external notarisation.';

-- Records. Immutable once written.
CREATE TABLE evidence_record (
    chain_id           text        NOT NULL REFERENCES evidence_chain (chain_id),
    sequence           bigint      NOT NULL,

    tenant_id          text        NOT NULL,
    brand_id           text        NOT NULL DEFAULT '',
    jurisdiction_code  text        NOT NULL DEFAULT '',
    region_code        text        NOT NULL DEFAULT '',

    -- SAFETY INVARIANT 12: timestamptz stores a UTC instant. Display timezone is
    -- applied at the presentation edge and never persisted here.
    occurred_at        timestamptz NOT NULL,
    recorded_at        timestamptz NOT NULL,

    category           text        NOT NULL,
    action             text        NOT NULL,
    subject_type       text        NOT NULL,
    subject_id         text        NOT NULL,

    actor_type         text        NOT NULL,
    actor_id           text        NOT NULL,
    actor_display_name text        NOT NULL DEFAULT '',
    actor_session_id   text        NOT NULL DEFAULT '',
    actor_source_ip    text        NOT NULL DEFAULT '',
    actor_on_behalf_of text        NOT NULL DEFAULT '',

    payload            jsonb       NOT NULL,
    payload_hash       bytea       NOT NULL,
    prev_hash          bytea       NOT NULL,
    record_hash        bytea       NOT NULL,

    idempotency_key    text        NOT NULL,
    retention_class    text        NOT NULL,
    correlation_id     text        NOT NULL DEFAULT '',
    trace_id           text        NOT NULL DEFAULT '',

    PRIMARY KEY (chain_id, sequence),

    -- Idempotency is enforced by the database rather than only by a prior read,
    -- so that two concurrent retries of the same append cannot both succeed.
    CONSTRAINT evidence_record_idempotent UNIQUE (chain_id, idempotency_key),

    CONSTRAINT evidence_record_sequence_positive CHECK (sequence > 0),
    CONSTRAINT evidence_record_payload_hash_length CHECK (octet_length(payload_hash) = 32),
    CONSTRAINT evidence_record_prev_hash_length CHECK (octet_length(prev_hash) = 32),
    CONSTRAINT evidence_record_record_hash_length CHECK (octet_length(record_hash) = 32),
    CONSTRAINT evidence_record_tenant_not_empty CHECK (tenant_id <> ''),
    CONSTRAINT evidence_record_action_not_empty CHECK (action <> ''),
    CONSTRAINT evidence_record_retention_class_not_empty CHECK (retention_class <> ''),
    CONSTRAINT evidence_record_category_registered CHECK (category IN (
        'money', 'identity', 'compliance', 'responsible_gambling',
        'security', 'gameplay', 'configuration', 'commercial'
    ))
);

COMMENT ON TABLE evidence_record IS
    'Append-only hash-chained audit records. UPDATE, DELETE and TRUNCATE are blocked by trigger.';
COMMENT ON COLUMN evidence_record.retention_class IS
    'Part of the hashed pre-image, so retention cannot be shortened after the fact without breaking the chain.';

-- Query paths that compliance and investigation actually use. Deliberately few:
-- every index is also a copy of the data that has to be protected and retained.
CREATE INDEX evidence_record_tenant_occurred_idx
    ON evidence_record (tenant_id, occurred_at DESC);
CREATE INDEX evidence_record_subject_idx
    ON evidence_record (tenant_id, subject_type, subject_id, occurred_at DESC);
CREATE INDEX evidence_record_action_idx
    ON evidence_record (tenant_id, action, occurred_at DESC);
CREATE INDEX evidence_record_category_idx
    ON evidence_record (tenant_id, category, occurred_at DESC);
CREATE INDEX evidence_record_correlation_idx
    ON evidence_record (correlation_id) WHERE correlation_id <> '';

-- Periodic checkpoints, for external notarisation.
--
-- Hash chaining alone proves internal consistency. It does not stop an actor with
-- full write access from rewriting the entire chain coherently. Publishing the head
-- hash to an independent append-only location at intervals is what makes such a
-- rewrite detectable, because the republished chain cannot reproduce a hash that a
-- third party already holds.
CREATE TABLE evidence_checkpoint (
    chain_id         text        NOT NULL,
    sequence         bigint      NOT NULL,
    tenant_id        text        NOT NULL,
    head_hash        bytea       NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- Populated at insert time by the notarisation job, which calls the external
    -- notary first and records the outcome as a new row. The row is never updated,
    -- so a failed notarisation is visible as an absence rather than overwritten.
    notary_kind      text        NOT NULL DEFAULT '',
    notary_reference text        NOT NULL DEFAULT '',

    PRIMARY KEY (chain_id, sequence),
    CONSTRAINT evidence_checkpoint_hash_length CHECK (octet_length(head_hash) = 32)
);

COMMENT ON TABLE evidence_checkpoint IS
    'Append-only chain-head checkpoints anchored to external notarisation.';

-- Append-only enforcement.
CREATE FUNCTION evidence_deny_mutation() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'table % is append-only: % is not permitted (SAFETY INVARIANT 4: audit and evidence store has no deletion path within the retention period)',
        TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

COMMENT ON FUNCTION evidence_deny_mutation() IS
    'Blocks UPDATE, DELETE and TRUNCATE on evidence tables. Removing this function is a safety-invariant breach.';

CREATE TRIGGER evidence_record_deny_update
    BEFORE UPDATE ON evidence_record
    FOR EACH ROW EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_record_deny_delete
    BEFORE DELETE ON evidence_record
    FOR EACH ROW EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_record_deny_truncate
    BEFORE TRUNCATE ON evidence_record
    FOR EACH STATEMENT EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_checkpoint_deny_update
    BEFORE UPDATE ON evidence_checkpoint
    FOR EACH ROW EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_checkpoint_deny_delete
    BEFORE DELETE ON evidence_checkpoint
    FOR EACH ROW EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_checkpoint_deny_truncate
    BEFORE TRUNCATE ON evidence_checkpoint
    FOR EACH STATEMENT EXECUTE FUNCTION evidence_deny_mutation();

-- evidence_chain is not append-only: the head pointer moves with every append.
-- Deletion is still blocked, because dropping a chain row would orphan its records
-- and destroy the anchor they verify against.
CREATE TRIGGER evidence_chain_deny_delete
    BEFORE DELETE ON evidence_chain
    FOR EACH ROW EXECUTE FUNCTION evidence_deny_mutation();

CREATE TRIGGER evidence_chain_deny_truncate
    BEFORE TRUNCATE ON evidence_chain
    FOR EACH STATEMENT EXECUTE FUNCTION evidence_deny_mutation();

-- A head pointer may only move forward. Rewinding it is how a chain would be
-- rolled back to hide recent records, and it is not a legitimate operation.
CREATE FUNCTION evidence_chain_forward_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.head_sequence < OLD.head_sequence THEN
        RAISE EXCEPTION
            'evidence chain % head cannot move backwards from % to % (SAFETY INVARIANT 4)',
            OLD.chain_id, OLD.head_sequence, NEW.head_sequence
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.chain_id <> OLD.chain_id OR NEW.tenant_id <> OLD.tenant_id THEN
        RAISE EXCEPTION
            'evidence chain identity is immutable (chain_id %, tenant_id %)',
            OLD.chain_id, OLD.tenant_id
            USING ERRCODE = 'restrict_violation';
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER evidence_chain_forward_only
    BEFORE UPDATE ON evidence_chain
    FOR EACH ROW EXECUTE FUNCTION evidence_chain_forward_only();

-- Tenant isolation at the database layer.
--
-- FORCE ROW LEVEL SECURITY applies the policy to the table owner as well. Without
-- it, the migration role and any connection using it would bypass isolation
-- entirely, which is the usual reason RLS is present but ineffective.
ALTER TABLE evidence_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_chain FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_record FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_checkpoint ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_checkpoint FORCE ROW LEVEL SECURITY;

-- current_setting(..., true) returns NULL when the variable is unset, and
-- 'tenant' = NULL is NULL rather than true, so an unscoped connection sees
-- nothing. Deny-by-default falls out of the SQL semantics rather than relying on
-- the application remembering to set the variable.
CREATE POLICY evidence_chain_tenant_isolation ON evidence_chain
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY evidence_record_tenant_isolation ON evidence_record
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

CREATE POLICY evidence_checkpoint_tenant_isolation ON evidence_checkpoint
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
