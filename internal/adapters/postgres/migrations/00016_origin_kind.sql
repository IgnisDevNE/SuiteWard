-- +goose Up
--
-- The origin kind and outcomes without a canonical (docs/contracts/persistence.md, "Bootstrap from a PR, and the revision
-- comment (M1.7b)"; ADR 0010 amendment of 2026-10-15).
--   * proposal_revisions.origin_kind says where a revision's source comes from: a pinned default-branch commit
--     ('branch', the existing-baseline bootstrap) or a PR head ('pull', the first-test bootstrap and every change). A revision
--     that expected no canonical was a baseline, so it becomes 'branch'; one that expected a version was a change, so 'pull'.
--   * pr_merges.version_id becomes nullable: a merge decided without a canonical names no version. It stays required for
--     every kind but 'blocked' and 'before-canonical'.

-- proposal_revisions is immutable by trigger, and the back-fill is the one UPDATE it will ever see. The trigger is turned
-- off for this statement only and on again before the migration ends (goose runs the file in one transaction, so a failure
-- leaves it enabled).
ALTER TABLE proposal_revisions DISABLE TRIGGER proposal_revisions_immutable;
ALTER TABLE proposal_revisions ADD COLUMN origin_kind text;
UPDATE proposal_revisions SET origin_kind = CASE WHEN expected_version_id IS NULL THEN 'branch' ELSE 'pull' END;
-- A proposal never changes kind. Production never revised a baseline, so no deployed database holds a proposal of both
-- kinds; if one does, refuse loudly (this rolls the migration back) rather than leave a proposal that cannot be read.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM proposal_revisions GROUP BY project_id, suite_id, proposal_id HAVING count(DISTINCT origin_kind) > 1) THEN
        RAISE EXCEPTION 'a proposal has revisions of both kinds (a baseline rebound onto a canonical); resolve it before migrating to 00016';
    END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE proposal_revisions
    ALTER COLUMN origin_kind SET NOT NULL,
    ADD CONSTRAINT proposal_revisions_origin_kind_check CHECK (origin_kind IN ('branch', 'pull'));
ALTER TABLE proposal_revisions ENABLE TRIGGER proposal_revisions_immutable;

ALTER TABLE pr_merges
    ALTER COLUMN version_id DROP NOT NULL,
    ADD CONSTRAINT pr_merges_version_check CHECK (version_id IS NOT NULL OR kind IN ('blocked', 'before-canonical'));
