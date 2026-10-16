-- +goose Up
--
-- The start of a repository's last successful synchronization cycle in GitHub's clock (docs/contracts/persistence.md,
-- "Cycle start (M1.7c)"): the Date of the cycle's first response. It is zero (NULL) for a repository that has not completed
-- a cycle since this migration. Forward-only.
ALTER TABLE github_repositories ADD COLUMN cycle_started_at timestamptz;
