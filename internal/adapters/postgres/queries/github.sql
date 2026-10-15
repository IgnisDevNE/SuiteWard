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
INSERT INTO github_pulls (repo, number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, head_repo)
SELECT sqlc.arg(repo)::text, (p->>'number')::int, p->>'head_sha', p->>'head_ref', p->>'base_ref', p->>'base_sha', (p->>'updated_at')::timestamptz,
       sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz, NULLIF(p->>'head_repo', '')
FROM jsonb_array_elements(sqlc.arg(pulls)::jsonb) AS p
ON CONFLICT (repo, number) DO UPDATE
SET head_sha = excluded.head_sha,
    head_ref = excluded.head_ref,
    base_ref = excluded.base_ref,
    base_sha = excluded.base_sha,
    updated_at = excluded.updated_at,
    last_seen = excluded.last_seen,
    head_repo = excluded.head_repo,
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

-- A repeated version (same id, updated_at and body) changes nothing. The body travels base64-encoded: it is raw bytes.
-- name: InsertCommentVersions :exec
INSERT INTO github_comment_versions (repo, comment_id, updated_at, pull_number, author_id, author_login, created_at, body, body_digest, first_seen)
SELECT sqlc.arg(repo)::text, (c->>'comment_id')::bigint, (c->>'updated_at')::timestamptz, (c->>'pull_number')::int, (c->>'author_id')::bigint,
       c->>'author_login', (c->>'created_at')::timestamptz, decode(c->>'body', 'base64'), c->>'body_digest', sqlc.arg(at)::timestamptz
FROM jsonb_array_elements(sqlc.arg(comments)::jsonb) AS c
ON CONFLICT (repo, comment_id, updated_at, body_digest) DO NOTHING;

-- A comment of a pull listed in the cycle that the pull's full listing lacks:
-- the first absence is marked on its latest version: the largest updated_at, then first_seen, then body_digest.
-- name: MarkCommentsNotSeen :exec
UPDATE github_comment_versions v
SET not_seen_since = sqlc.arg(at)::timestamptz
WHERE v.repo = sqlc.arg(repo)
  AND v.pull_number = ANY(sqlc.arg(listed_pulls)::int[])
  AND v.not_seen_since IS NULL
  AND v.comment_id <> ALL(sqlc.arg(listed_comments)::bigint[])
  AND NOT EXISTS (SELECT 1 FROM github_comment_versions later WHERE later.repo = v.repo AND later.comment_id = v.comment_id
                  AND (later.updated_at, later.first_seen, later.body_digest COLLATE "C") > (v.updated_at, v.first_seen, v.body_digest COLLATE "C"));

-- name: GetGitHubRepository :one
SELECT repo, installation_id, last_success, last_error, last_status, wait_until
FROM github_repositories WHERE repo = sqlc.arg(repo);

-- name: ListGitHubPulls :many
SELECT number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since, head_repo
FROM github_pulls WHERE repo = sqlc.arg(repo) ORDER BY number;

-- name: ListGitHubComments :many
SELECT pull_number, comment_id, author_id, author_login, created_at, updated_at, body, body_digest, first_seen, not_seen_since
FROM github_comment_versions WHERE repo = sqlc.arg(repo) AND pull_number = sqlc.arg(pull_number)
ORDER BY comment_id, updated_at, first_seen, body_digest COLLATE "C";

-- Merge facts (docs/contracts/persistence.md, "Merges (M1.7)"). The writes below lock the repository row first,
-- like RecordCycle, so a cycle and a resolution of one repository never interleave.

-- name: LockGitHubRepository :one
SELECT closed_watermark FROM github_repositories WHERE repo = sqlc.arg(repo) FOR UPDATE;

-- Not open, and the current absence has no resolution.
-- name: ListAwaitingResolution :many
SELECT number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since, head_repo
FROM github_pulls
WHERE repo = sqlc.arg(repo) AND not_open_since IS NOT NULL AND resolved_for IS DISTINCT FROM not_open_since
ORDER BY number;

-- Writes only for the absence the resolution was read for; no row is a stale resolution. The head and base are
-- refreshed because a push between the last cycle and the merge is not otherwise seen.
-- name: RecordResolution :execrows
UPDATE github_pulls
SET head_sha = sqlc.arg(head_sha),
    head_ref = sqlc.arg(head_ref),
    base_ref = sqlc.arg(base_ref),
    base_sha = sqlc.arg(base_sha),
    head_repo = NULLIF(sqlc.arg(head_repo)::text, ''),
    resolved_for = not_open_since,
    merged = sqlc.arg(merged),
    merged_at = sqlc.narg(merged_at),
    merge_commit_sha = NULLIF(sqlc.arg(merge_commit_sha)::text, ''),
    resolved_at = sqlc.arg(resolved_at)
WHERE repo = sqlc.arg(repo) AND number = sqlc.arg(number) AND not_open_since = sqlc.arg(not_open_since);

-- name: SetClosedWatermark :exec
UPDATE github_repositories SET closed_watermark = sqlc.arg(watermark) WHERE repo = sqlc.arg(repo);

-- A merged pull seen only in the closed listing: absent since the listing, unresolved. A stored pull is left alone.
-- name: InsertClosedPulls :exec
INSERT INTO github_pulls (repo, number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since, head_repo)
SELECT sqlc.arg(repo)::text, (p->>'number')::int, p->>'head_sha', p->>'head_ref', p->>'base_ref', p->>'base_sha', (p->>'updated_at')::timestamptz,
       sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz, NULLIF(p->>'head_repo', '')
FROM jsonb_array_elements(sqlc.arg(pulls)::jsonb) AS p
ON CONFLICT (repo, number) DO NOTHING;

-- name: ListMergedPulls :many
SELECT number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since, head_repo,
       resolved_for, merged_at, merge_commit_sha, resolved_at, merge_processed
FROM github_pulls
WHERE repo = sqlc.arg(repo) AND merged
ORDER BY merged_at, number;

-- name: MarkMergeProcessed :execrows
UPDATE github_pulls SET merge_processed = true WHERE repo = sqlc.arg(repo) AND number = sqlc.arg(number);
