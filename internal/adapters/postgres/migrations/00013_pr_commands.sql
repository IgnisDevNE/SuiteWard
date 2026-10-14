-- +goose Up
--
-- PR command ledger, instance id and consumed steps bound to their command
-- (docs/contracts/persistence.md, "PR commands and bootstrap"; D-COMMANDS).
-- The ledger records the first observed version of every PR comment SuiteWard
-- considered and what it decided. It is the record of that decision, not the
-- authority: consent lives in the governance tables.

-- Insert-only. The operation is the one ProcessConsent recorded and is set for
-- exactly the disposition that reached it.
CREATE TABLE pr_commands (
    repository_id bigint NOT NULL,
    comment_id bigint NOT NULL,
    pull_number integer NOT NULL,
    author_id bigint NOT NULL,
    created_at timestamptz NOT NULL,
    body_digest text NOT NULL,
    disposition text NOT NULL,
    operation_id text,
    decided_at timestamptz NOT NULL,
    CONSTRAINT pr_commands_pkey PRIMARY KEY (repository_id, comment_id),
    CONSTRAINT pr_commands_repository_check CHECK (repository_id > 0),
    CONSTRAINT pr_commands_comment_check CHECK (comment_id > 0),
    CONSTRAINT pr_commands_pull_check CHECK (pull_number > 0),
    CONSTRAINT pr_commands_author_check CHECK (author_id > 0),
    CONSTRAINT pr_commands_body_digest_check CHECK (body_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT pr_commands_disposition_check CHECK (disposition IN ('not-command', 'before-enrollment', 'no-suite', 'edited-first', 'failed',
        'ignored-author', 'malformed', 'invalid-code', 'replayed-code', 'processed')),
    CONSTRAINT pr_commands_operation_check CHECK ((disposition = 'processed') = (operation_id IS NOT NULL) AND (operation_id IS NULL OR btrim(operation_id) <> ''))
);

CREATE TRIGGER pr_commands_immutable BEFORE UPDATE OR DELETE ON pr_commands
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER pr_commands_no_truncate BEFORE TRUNCATE ON pr_commands
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- This installation's random identifier: 128 bits as 32 lowercase hexadecimal digits, drawn once here and never changed.
-- gen_random_uuid() is built in; the digest spreads its 244 random bits over the full 128 that are kept.
CREATE TABLE suiteward_instance (
    singleton boolean NOT NULL DEFAULT true,
    instance_id text NOT NULL,
    CONSTRAINT suiteward_instance_pkey PRIMARY KEY (singleton),
    CONSTRAINT suiteward_instance_singleton_check CHECK (singleton),
    CONSTRAINT suiteward_instance_id_check CHECK (instance_id ~ '^[0-9a-f]{32}$')
);

INSERT INTO suiteward_instance (instance_id)
VALUES (left(encode(sha256(convert_to(gen_random_uuid()::text || gen_random_uuid()::text, 'UTF8')), 'hex'), 32));

CREATE TRIGGER suiteward_instance_immutable BEFORE UPDATE OR DELETE ON suiteward_instance
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER suiteward_instance_no_truncate BEFORE TRUNCATE ON suiteward_instance
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- A consumed step records the command that used it, so a retry of the same command is not a replay. Existing rows
-- were consumed by setup: they take the setup source of the current enrollment's generation. The backfill is the
-- one update the history trigger must allow; a step with no enrollment to explain it fails the migration on NOT NULL.
-- Older rows never recorded their generation, so they all take the current one; harmless, because only setup uses "setup:" sources.
ALTER TABLE totp_consumed_steps ADD COLUMN source_command_id text;
ALTER TABLE totp_consumed_steps DISABLE TRIGGER totp_consumed_steps_immutable;
UPDATE totp_consumed_steps SET source_command_id = 'setup:' || (SELECT generation FROM owner_enrollment WHERE singleton);
ALTER TABLE totp_consumed_steps ENABLE TRIGGER totp_consumed_steps_immutable;
ALTER TABLE totp_consumed_steps ALTER COLUMN source_command_id SET NOT NULL;
ALTER TABLE totp_consumed_steps ADD CONSTRAINT totp_consumed_steps_source_check CHECK (btrim(source_command_id) <> '');
