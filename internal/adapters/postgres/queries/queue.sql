-- name: InsertOutbox :exec
-- project_id and suite_id are both set (a governance message) or both null (a system message).
INSERT INTO outbox (key, project_id, suite_id, kind, payload)
VALUES (sqlc.arg(key), sqlc.narg(project_id), sqlc.narg(suite_id), sqlc.arg(kind), sqlc.arg(payload));

-- name: GetJobState :one
SELECT state::text AS state FROM river_job WHERE id = sqlc.arg(id);

-- name: GetOutboxState :one
SELECT state FROM outbox WHERE key = sqlc.arg(key);

-- Pending messages that were claimed on every attempt without an outcome stop here.
-- name: FailExhaustedOutbox :many
UPDATE outbox
SET state = 'failed',
    last_error = 'delivery abandoned: every attempt was claimed without an outcome',
    finished_at = sqlc.arg(now)::timestamptz
WHERE state = 'pending'
  AND next_attempt_at <= sqlc.arg(now)::timestamptz
  AND attempts >= sqlc.arg(max_attempts)::int
RETURNING key, kind;

-- Claims due messages: counts the attempt and hides them until the lease expires.
-- name: ClaimOutbox :many
WITH due AS (
    SELECT outbox.key FROM outbox
    WHERE state = 'pending'
      AND next_attempt_at <= sqlc.arg(now)::timestamptz
      AND attempts < sqlc.arg(max_attempts)::int
    ORDER BY next_attempt_at, outbox.key
    LIMIT sqlc.arg(batch)::int
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox
SET attempts = outbox.attempts + 1,
    next_attempt_at = sqlc.arg(lease_until)::timestamptz
FROM due
WHERE outbox.key = due.key
RETURNING outbox.key, outbox.kind, outbox.payload, outbox.attempts;

-- Records the outcome of a claim; updates nothing when the message was reclaimed since.
-- name: FinishOutbox :execrows
UPDATE outbox
SET state = sqlc.arg(state),
    last_error = COALESCE(sqlc.narg(last_error), last_error),
    next_attempt_at = COALESCE(sqlc.narg(next_attempt_at), next_attempt_at),
    finished_at = sqlc.narg(finished_at)
WHERE key = sqlc.arg(key) AND state = 'pending' AND attempts = sqlc.arg(attempts);

-- Gives back a claim that was never tried: restores the attempt and makes the message due.
-- name: ReleaseOutbox :execrows
UPDATE outbox
SET attempts = attempts - 1,
    next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz
WHERE key = sqlc.arg(key) AND state = 'pending' AND attempts = sqlc.arg(attempts);
