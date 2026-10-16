-- The outcome of one merged pull request (M1.7). Written once, after the promotion it may name, in the same transaction.

-- name: GetMergeOutcome :one
SELECT * FROM pr_merges
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND carrier = sqlc.arg(carrier);

-- name: InsertMergeOutcome :exec
INSERT INTO pr_merges (project_id, suite_id, carrier, kind, reason, source, version_id, corrects_version_id, proposal_id, revision_id, recorded_at)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(carrier), sqlc.arg(kind), sqlc.arg(reason), sqlc.arg(source), sqlc.narg(version_id),
    sqlc.narg(corrects_version_id), sqlc.narg(proposal_id), sqlc.narg(revision_id), sqlc.arg(recorded_at));
