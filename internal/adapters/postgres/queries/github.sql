-- GitHub observations (docs/contracts/persistence.md, "GitHub observations").
-- RecordCycle and RecordFailure start with an upsert of the repository row,
-- which locks it until the transaction ends; the statements after it compute
-- their marks from the state that lock protects.

-- name: UpsertRepositorySuccess :exec
INSERT INTO github_repositories (repo, installation_id, last_success)
VALUES (sqlc.arg(repo), sqlc.arg(installation_id), sqlc.arg(at)::timestamptz)
ON CONFLICT (repo) DO UPDATE
SET installation_id = excluded.installation_id,
    last_success = excluded.last_success,
    last_error = '',
    last_status = 0,
    wait_until = NULL;

-- name: UpsertRepositoryFailure :exec
INSERT INTO github_repositories (repo, installation_id, last_error, last_status, wait_until)
VALUES (sqlc.arg(repo), sqlc.arg(installation_id), sqlc.arg(last_error), sqlc.arg(last_status), sqlc.narg(wait_until))
ON CONFLICT (repo) DO UPDATE
SET installation_id = excluded.installation_id,
    last_error = excluded.last_error,
    last_status = excluded.last_status,
    wait_until = excluded.wait_until;

-- name: UpsertPulls :exec
-- Reopens a pull that reappears; first_seen never changes.
INSERT INTO github_pulls (repo, number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen)
SELECT sqlc.arg(repo)::text, (p->>'number')::int, p->>'head_sha', p->>'head_ref', p->>'base_ref', p->>'base_sha', (p->>'updated_at')::timestamptz,
       sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz
FROM jsonb_array_elements(sqlc.arg(pulls)::jsonb) AS p
ON CONFLICT (repo, number) DO UPDATE
SET head_sha = excluded.head_sha,
    head_ref = excluded.head_ref,
    base_ref = excluded.base_ref,
    base_sha = excluded.base_sha,
    updated_at = excluded.updated_at,
    last_seen = excluded.last_seen,
    not_open_since = NULL;

-- Marks the first cycle a pull was absent from the complete open listing.
-- name: MarkPullsNotOpen :exec
UPDATE github_pulls
SET not_open_since = sqlc.arg(at)::timestamptz
WHERE repo = sqlc.arg(repo)
  AND not_open_since IS NULL
  AND number <> ALL(sqlc.arg(open_numbers)::int[]);

-- Comment ids of the listing whose stored creation time differs: GitHub never changes it.
-- name: CommentsWithChangedCreation :many
SELECT v.comment_id
FROM github_comment_versions v
JOIN jsonb_array_elements(sqlc.arg(comments)::jsonb) AS c
    ON (c->>'comment_id')::bigint = v.comment_id
WHERE v.repo = sqlc.arg(repo) AND v.created_at <> (c->>'created_at')::timestamptz
ORDER BY v.comment_id
LIMIT 1;

-- A comment listed again is seen: its mark goes, on every version.
-- name: ClearCommentsNotSeen :exec
UPDATE github_comment_versions
SET not_seen_since = NULL
WHERE repo = sqlc.arg(repo)
  AND comment_id = ANY(sqlc.arg(comment_ids)::bigint[])
  AND not_seen_since IS NOT NULL;

-- A repeated version, same id and same updated_at, changes nothing.
-- name: InsertCommentVersions :exec
INSERT INTO github_comment_versions (repo, comment_id, updated_at, pull_number, author_id, author_login, created_at, body, body_digest, first_seen)
SELECT sqlc.arg(repo)::text, (c->>'comment_id')::bigint, (c->>'updated_at')::timestamptz, (c->>'pull_number')::int, (c->>'author_id')::bigint,
       c->>'author_login', (c->>'created_at')::timestamptz, c->>'body', c->>'body_digest', sqlc.arg(at)::timestamptz
FROM jsonb_array_elements(sqlc.arg(comments)::jsonb) AS c
ON CONFLICT (repo, comment_id, updated_at) DO NOTHING;

-- A comment of a pull listed in the cycle that the pull's full listing lacks:
-- the first absence is marked on its latest version.
-- name: MarkCommentsNotSeen :exec
UPDATE github_comment_versions v
SET not_seen_since = sqlc.arg(at)::timestamptz
WHERE v.repo = sqlc.arg(repo)
  AND v.pull_number = ANY(sqlc.arg(listed_pulls)::int[])
  AND v.not_seen_since IS NULL
  AND v.comment_id <> ALL(sqlc.arg(listed_comments)::bigint[])
  AND v.updated_at = (SELECT max(latest.updated_at) FROM github_comment_versions latest WHERE latest.repo = v.repo AND latest.comment_id = v.comment_id);

-- name: GetGitHubRepository :one
SELECT repo, installation_id, last_success, last_error, last_status, wait_until
FROM github_repositories WHERE repo = sqlc.arg(repo);

-- name: ListGitHubPulls :many
SELECT number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since
FROM github_pulls WHERE repo = sqlc.arg(repo) ORDER BY number;

-- name: ListGitHubComments :many
SELECT pull_number, comment_id, author_id, author_login, created_at, updated_at, body, body_digest, first_seen, not_seen_since
FROM github_comment_versions WHERE repo = sqlc.arg(repo) AND pull_number = sqlc.arg(pull_number)
ORDER BY comment_id, updated_at;
