-- +goose Up
--
-- The other-pull disposition (docs/contracts/persistence.md, "Command ledger"): an owner approve or revoke whose reference names a
-- proposal that another pull request carries, refused before its step is used. It carries no operation id (the rule of 00013 still
-- allows one only for 'processed'), and LatestOwnerCommand lists its dispositions, so it never counts as the owner's latest command.
-- pr_commands is insert-only, so only the CHECK is replaced; the existing rows already satisfy the wider list.
ALTER TABLE pr_commands
    DROP CONSTRAINT pr_commands_disposition_check,
    ADD CONSTRAINT pr_commands_disposition_check CHECK (disposition IN ('not-command', 'before-enrollment', 'no-suite', 'edited-first', 'failed',
        'ignored-author', 'malformed', 'invalid-code', 'replayed-code', 'unknown-proposal', 'processed',
        'proposed', 'propose-refused', 'skipped', 'skip-refused', 'suggested', 'suggest-refused', 'other-pull'));
