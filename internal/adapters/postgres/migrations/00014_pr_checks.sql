-- +goose Up
--
-- PR check ledger (docs/contracts/persistence.md, "PR checks (M1.6b)"; D-CHECKS).
-- One row per pull request holds the current state of its SuiteWard / Contract
-- check and the publication generation. The row is updated in place and never
-- deleted; the publications themselves are the outbox rows, and message_key
-- names the one of the current generation (check:<repository id>:<pull>:<generation>). It has no foreign key to the
-- outbox: that would make the outbox's truncate guard answer with another error.
-- The ledger records what was published, never the authority.

CREATE TABLE pr_checks (
    repository_id bigint NOT NULL,
    pull_number integer NOT NULL,
    head_sha text NOT NULL,
    head_since timestamptz NOT NULL,
    state text NOT NULL,
    reason text NOT NULL,
    reference text NOT NULL,
    change text NOT NULL,
    revision bigint NOT NULL,
    command_id bigint NOT NULL,
    generation bigint NOT NULL,
    message_key text NOT NULL,
    decided_at timestamptz NOT NULL,
    CONSTRAINT pr_checks_pkey PRIMARY KEY (repository_id, pull_number),
    CONSTRAINT pr_checks_repository_check CHECK (repository_id > 0),
    CONSTRAINT pr_checks_pull_check CHECK (pull_number > 0),
    CONSTRAINT pr_checks_head_check CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT pr_checks_reason_check CHECK (btrim(reason) <> ''),
    CONSTRAINT pr_checks_state_check CHECK (state IN ('success', 'in_progress', 'failure')),
    CONSTRAINT pr_checks_reference_check CHECK (reference ~ '^(P[0-9]+-R[0-9]+)?$'),
    CONSTRAINT pr_checks_revision_check CHECK (revision >= 0),
    CONSTRAINT pr_checks_command_check CHECK (command_id >= 0),
    CONSTRAINT pr_checks_generation_check CHECK (generation > 0),
    CONSTRAINT pr_checks_message_key_check CHECK (btrim(message_key) <> '')
);

CREATE TRIGGER pr_checks_no_delete BEFORE DELETE ON pr_checks
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER pr_checks_no_truncate BEFORE TRUNCATE ON pr_checks
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
