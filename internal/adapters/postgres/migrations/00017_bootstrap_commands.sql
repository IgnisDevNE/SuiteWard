-- +goose Up
--
-- The propose and skip dispositions of the explicit bootstrap (docs/contracts/persistence.md, "Explicit bootstrap (M1.7b rework,
-- 2026-10-16)"). None carries an operation id: the operation rule of 00013 is unchanged and still allows one only for 'processed'.
-- pr_commands is insert-only, so only the CHECK is replaced; the existing rows already satisfy the wider list.
ALTER TABLE pr_commands
    DROP CONSTRAINT pr_commands_disposition_check,
    ADD CONSTRAINT pr_commands_disposition_check CHECK (disposition IN ('not-command', 'before-enrollment', 'no-suite', 'edited-first', 'failed',
        'ignored-author', 'malformed', 'invalid-code', 'replayed-code', 'unknown-proposal', 'processed',
        'proposed', 'propose-refused', 'skipped', 'skip-refused'));
