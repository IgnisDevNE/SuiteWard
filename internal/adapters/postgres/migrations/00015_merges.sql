-- +goose Up
--
-- Merges (docs/contracts/persistence.md, "Merges (M1.7)"; D-INTEGRATION). Three parts share this migration so the
-- later tasks add queries only:
--   * merge facts on the GitHub observations: the head repository, the resolution of each pull's absence (merged or
--     closed, the merge commit) and the closed-listing watermark. They are observation state, evidence and never
--     authority; GitHub's times (merged_at, updated_at) are stored as GitHub sent them, SuiteWard's (resolved_at,
--     resolved_for) from the injected clock.
--   * pr_merges, the governance outcome recorded once per merged PR (carrier), immutable like the other history.
--   * canonical_integrity, the latest content verdict of each Suite's canonical version, replaced in place.

ALTER TABLE github_repositories
    ADD COLUMN closed_watermark timestamptz;

-- NULL head_repo is "not observed" (a row from before this migration), which is not a fork. resolved_for is the
-- not_open_since the resolution answers: a pull is unresolved while it differs from not_open_since.
ALTER TABLE github_pulls
    ADD COLUMN head_repo text,
    ADD COLUMN resolved_for timestamptz,
    ADD COLUMN merged boolean NOT NULL DEFAULT false,
    ADD COLUMN merged_at timestamptz,
    ADD COLUMN merge_commit_sha text,
    ADD COLUMN resolved_at timestamptz,
    ADD COLUMN merge_processed boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT github_pulls_head_repo_check CHECK (head_repo IS NULL OR btrim(head_repo) <> ''),
    ADD CONSTRAINT github_pulls_merged_at_check CHECK (merged = (merged_at IS NOT NULL)),
    ADD CONSTRAINT github_pulls_merge_commit_check CHECK (merge_commit_sha IS NULL OR (merged AND merge_commit_sha ~ '^[0-9a-f]{40}$')),
    ADD CONSTRAINT github_pulls_resolution_check CHECK ((resolved_for IS NULL) = (resolved_at IS NULL));

-- The outcome of one merged PR. Reason is set exactly when the kind is blocked; corrects_version_id exactly when it is
-- corrected; proposal_id and revision_id are both set or both null (the PR's change proposal revision, when it has one).
-- source is the merge commit, empty when it could not be read.
CREATE TABLE pr_merges (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    carrier text NOT NULL,
    kind text NOT NULL,
    reason text NOT NULL,
    source text NOT NULL,
    version_id text NOT NULL,
    corrects_version_id text,
    proposal_id text,
    revision_id text,
    recorded_at timestamptz NOT NULL,
    CONSTRAINT pr_merges_pkey PRIMARY KEY (project_id, suite_id, carrier),
    CONSTRAINT pr_merges_carrier_check CHECK (btrim(carrier) <> ''),
    CONSTRAINT pr_merges_kind_check CHECK (kind IN ('promoted', 'corrected', 'no-change', 'not-target', 'before-canonical', 'blocked')),
    CONSTRAINT pr_merges_reason_check CHECK (reason IN ('', 'unapproved-change', 'invalid-source', 'assessment-conflict', 'approval-missing', 'differs-from-approved',
        'not-current', 'canonical-changed', 'policy-changed', 'integrity-not-passed', 'integration-mismatch', 'correction-context-reused', 'fork-unsupported',
        'shared-merge-commit', 'integration-unreadable', 'integrity-failed')),
    CONSTRAINT pr_merges_reason_blocked_check CHECK ((kind = 'blocked') = (reason <> '')),
    CONSTRAINT pr_merges_corrects_check CHECK ((kind = 'corrected') = (corrects_version_id IS NOT NULL)),
    CONSTRAINT pr_merges_reference_check CHECK ((proposal_id IS NULL) = (revision_id IS NULL)),
    CONSTRAINT pr_merges_version_fkey FOREIGN KEY (project_id, suite_id, version_id) REFERENCES suite_versions (project_id, suite_id, version_id),
    CONSTRAINT pr_merges_corrects_fkey FOREIGN KEY (project_id, suite_id, corrects_version_id) REFERENCES suite_versions (project_id, suite_id, version_id),
    CONSTRAINT pr_merges_revision_fkey FOREIGN KEY (project_id, suite_id, proposal_id, revision_id)
        REFERENCES proposal_revisions (project_id, suite_id, proposal_id, revision_id)
);

CREATE TRIGGER pr_merges_immutable BEFORE UPDATE OR DELETE ON pr_merges
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER pr_merges_no_truncate BEFORE TRUNCATE ON pr_merges
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- The latest content verdict of a Suite's canonical version, replaced in place by the integrity job. The rule that
-- "unavailable" never replaces "failed" for one version is the writer's, in one statement.
CREATE TABLE canonical_integrity (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    version_id text NOT NULL,
    verdict text NOT NULL,
    checked_at timestamptz NOT NULL,
    CONSTRAINT canonical_integrity_pkey PRIMARY KEY (project_id, suite_id),
    CONSTRAINT canonical_integrity_verdict_check CHECK (verdict IN ('passed', 'failed', 'unavailable')),
    CONSTRAINT canonical_integrity_version_fkey FOREIGN KEY (project_id, suite_id, version_id) REFERENCES suite_versions (project_id, suite_id, version_id)
);
