-- PR check ledger (docs/contracts/persistence.md, "PR checks (M1.6b)").

-- name: GetPRCheck :one
SELECT repository_id, pull_number, head_sha, head_since, state, reason, reference, change, revision, command_id, generation, message_key, decided_at, unblocked
FROM pr_checks WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number);

-- Holds the row until the transaction ends: Publish for one pull is serialized here.
-- name: LockPRCheck :one
SELECT repository_id, pull_number, head_sha, head_since, state, reason, reference, change, revision, command_id, generation, message_key, decided_at, unblocked
FROM pr_checks WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number) FOR UPDATE;

-- Rows affected: 0 means the pull already has a row, which LockPRCheck reads once the concurrent insert committed.
-- name: InsertPRCheck :execrows
INSERT INTO pr_checks (repository_id, pull_number, head_sha, head_since, state, reason, reference, change, revision, command_id, generation, message_key, decided_at, unblocked)
VALUES (sqlc.arg(repository_id), sqlc.arg(pull_number), sqlc.arg(head_sha), sqlc.arg(head_since), sqlc.arg(state), sqlc.arg(reason), sqlc.arg(reference),
        sqlc.arg(change), sqlc.arg(revision), sqlc.arg(command_id), sqlc.arg(generation), sqlc.arg(message_key), sqlc.arg(decided_at), sqlc.arg(unblocked))
ON CONFLICT (repository_id, pull_number) DO NOTHING;

-- name: UpdatePRCheck :exec
UPDATE pr_checks
SET head_sha = sqlc.arg(head_sha), head_since = sqlc.arg(head_since), state = sqlc.arg(state), reason = sqlc.arg(reason), reference = sqlc.arg(reference),
    change = sqlc.arg(change), revision = sqlc.arg(revision), command_id = sqlc.arg(command_id), generation = sqlc.arg(generation),
    message_key = sqlc.arg(message_key), decided_at = sqlc.arg(decided_at), unblocked = sqlc.arg(unblocked)
WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number);

-- The state of the message with the time it ended, which Publish needs to space the retries of a lost message.
-- name: GetOutboxOutcome :one
SELECT state, finished_at FROM outbox WHERE key = sqlc.arg(key);
