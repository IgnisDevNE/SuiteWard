-- +goose Up
-- Stub (RED): the column exists so the tests below fail on behavior, not on a missing column.
ALTER TABLE proposal_revisions ADD COLUMN origin_kind text NOT NULL DEFAULT 'pull';
