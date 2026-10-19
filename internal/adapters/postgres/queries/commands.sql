-- PR command ledger (docs/contracts/persistence.md, "PR commands and bootstrap").

-- The first observed version of every comment of an enrolled repository that has no ledger entry. Observations are
-- keyed by owner/name and the ledger by repository id: the enrolled projects (owner_test_checks of the current
-- enrollment's revision) join them. A comment's created_at never changes (the cycle refuses a change), so the
-- horizon filter may run before the first version is picked: the smallest first_seen, then updated_at, then digest.
-- name: PendingCommands :many
WITH enrolled AS (
    SELECT c.repository_id, c.repo
    FROM owner_enrollment e
    JOIN owner_test_checks c ON c.policy_revision_id = e.policy_revision_id
    WHERE e.singleton
), first_version AS (
    SELECT DISTINCT ON (v.repo, v.comment_id)
           enrolled.repository_id, v.repo, v.pull_number, v.comment_id, v.author_id, v.author_login, v.created_at, v.updated_at,
           v.body, v.body_digest, v.first_seen, v.not_seen_since
    FROM github_comment_versions v
    JOIN enrolled ON enrolled.repo = v.repo
    WHERE v.created_at < sqlc.arg(horizon)::timestamptz
    ORDER BY v.repo, v.comment_id, v.first_seen, v.updated_at, v.body_digest COLLATE "C"
)
SELECT f.repository_id, f.repo, f.pull_number, f.comment_id, f.author_id, f.author_login, f.created_at, f.updated_at,
       f.body, f.body_digest, f.first_seen, f.not_seen_since
FROM first_version f
WHERE NOT EXISTS (SELECT 1 FROM pr_commands p WHERE p.repository_id = f.repository_id AND p.comment_id = f.comment_id)
ORDER BY f.created_at, f.comment_id, f.repository_id
LIMIT sqlc.arg(max_rows)::int;

-- Rows affected: 0 means the comment already has an entry, which GetPRCommand reads.
-- name: InsertPRCommand :execrows
INSERT INTO pr_commands (repository_id, comment_id, pull_number, author_id, created_at, body_digest, disposition, operation_id, decided_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(comment_id), sqlc.arg(pull_number), sqlc.arg(author_id), sqlc.arg(created_at), sqlc.arg(body_digest),
        sqlc.arg(disposition), sqlc.narg(operation_id), sqlc.arg(decided_at))
ON CONFLICT (repository_id, comment_id) DO NOTHING;

-- name: GetPRCommand :one
SELECT repository_id, comment_id, pull_number, author_id, created_at, body_digest, disposition, operation_id, decided_at
FROM pr_commands WHERE repository_id = sqlc.arg(repository_id) AND comment_id = sqlc.arg(comment_id);

-- name: GetInstanceID :one
SELECT instance_id FROM suiteward_instance WHERE singleton;

-- The owner's latest command on a pull that can set or clear the check: the greatest comment id among the wrong codes and the processed
-- commands, below before when it is not 0. The other rejected dispositions have no effect on the check (D-COMMANDS, "A wrong command has no effect").
-- name: LatestOwnerCommand :one
SELECT repository_id, comment_id, pull_number, author_id, created_at, body_digest, disposition, operation_id, decided_at
FROM pr_commands
WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number) AND author_id = sqlc.arg(author_id)
  AND disposition IN ('invalid-code', 'replayed-code', 'processed')
  AND (sqlc.arg(before)::bigint = 0 OR comment_id < sqlc.arg(before)::bigint)
ORDER BY comment_id DESC
LIMIT 1;

-- Whether the pull has a skipped entry whose comment was created at or after the current enrollment's enrolled_at.
-- name: PullSkipped :one
SELECT EXISTS (
    SELECT 1 FROM pr_commands
    WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number) AND disposition = 'skipped' AND created_at >= sqlc.arg(since)::timestamptz
);
