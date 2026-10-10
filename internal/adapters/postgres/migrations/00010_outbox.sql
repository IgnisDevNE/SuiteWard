-- +goose Up
--
-- Outbox (docs/contracts/persistence.md, "Jobs and outbox"). A row records an
-- external effect together with the governance fact that requires it. The row
-- owns its delivery state: it is the only part that may change, and only
-- while pending. A system message (for example the diagnostic probe) has no
-- Suite, so project_id and suite_id are both set or both null.

CREATE TABLE outbox (
    key text NOT NULL,
    project_id text,
    suite_id text,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT outbox_pkey PRIMARY KEY (key),
    CONSTRAINT outbox_key_check CHECK (btrim(key) <> '' AND octet_length(key) <= 200),
    CONSTRAINT outbox_scope_check CHECK ((project_id IS NULL) = (suite_id IS NULL)),
    CONSTRAINT outbox_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites (project_id, suite_id),
    CONSTRAINT outbox_kind_check CHECK (kind ~ '^[a-z][a-z0-9_.]*$' AND octet_length(kind) <= 64),
    CONSTRAINT outbox_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_state_check CHECK (state IN ('pending', 'delivered', 'failed')),
    CONSTRAINT outbox_attempts_check CHECK (attempts >= 0),
    CONSTRAINT outbox_finished_check CHECK ((state = 'pending') = (finished_at IS NULL))
);

-- The relay claims due pending rows in due order.
CREATE INDEX outbox_pending_idx ON outbox (next_attempt_at) WHERE state = 'pending';

-- +goose StatementBegin
CREATE FUNCTION guard_outbox_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state <> 'pending' THEN
        RAISE EXCEPTION 'a delivered or failed outbox message cannot change'
            USING ERRCODE = '23514', CONSTRAINT = 'outbox_terminal_state';
    END IF;
    IF NEW.key IS DISTINCT FROM OLD.key OR NEW.project_id IS DISTINCT FROM OLD.project_id
        OR NEW.suite_id IS DISTINCT FROM OLD.suite_id OR NEW.kind IS DISTINCT FROM OLD.kind
        OR NEW.payload IS DISTINCT FROM OLD.payload OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'only the delivery state of an outbox message can change'
            USING ERRCODE = '23514', CONSTRAINT = 'outbox_immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER outbox_update_guard BEFORE UPDATE ON outbox
    FOR EACH ROW EXECUTE FUNCTION guard_outbox_update();
CREATE TRIGGER outbox_no_delete BEFORE DELETE ON outbox
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER outbox_no_truncate BEFORE TRUNCATE ON outbox
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
