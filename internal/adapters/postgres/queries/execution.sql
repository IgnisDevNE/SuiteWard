-- Execution (docs/contracts/persistence.md, "Execution (M2.1; normative)"). The lifecycle of a job is applied in Go, by the pure functions
-- of internal/application/execution, so that the in-memory store and this one cannot disagree: these statements read a job, lock it and
-- write the columns the lifecycle changes. No statement reads a clock; every time is a parameter.

-- Rows returned: 0 means a current job already has the key (the partial unique index), which GetCurrentExecutionJob reads.
-- name: InsertExecutionJob :one
INSERT INTO execution_jobs (job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest,
                            canonical_level, receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, not_before, created_at, finished_at)
VALUES (sqlc.arg(job_key), sqlc.arg(repository_id), sqlc.arg(pull_number), sqlc.arg(plan), sqlc.arg(profile), sqlc.arg(head_sha), sqlc.narg(base_sha),
        sqlc.arg(governing_digest), sqlc.arg(governing_source), sqlc.arg(declaration_digest), sqlc.arg(canonical_level), sqlc.arg(receipt_level),
        sqlc.arg(unapproved_paths), sqlc.arg(changed_paths), sqlc.arg(changed_total), sqlc.arg(state), sqlc.narg(reason), sqlc.arg(not_before),
        sqlc.arg(created_at), sqlc.narg(finished_at))
ON CONFLICT DO NOTHING
RETURNING job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
          receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
          leased_at, deadline_at, created_at, finished_at;

-- name: GetExecutionJob :one
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs WHERE job_id = sqlc.arg(job_id);

-- Holds the row until the transaction ends.
-- name: LockExecutionJob :one
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs WHERE job_id = sqlc.arg(job_id) FOR UPDATE;

-- The current job of a key: not cancelled, and not unavailable for executor-not-configured or no-executor (the predicate of the unique index).
-- name: GetCurrentExecutionJob :one
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs
WHERE job_key = sqlc.arg(job_key) AND state <> 'cancelled' AND NOT (state = 'unavailable' AND reason IN ('executor-not-configured', 'no-executor'));

-- The oldest job that a lease may take or must first expire: pending with its not_before reached, or leased with an expired lease. A job that
-- another lease holds is skipped, so two leases never take one job.
-- name: LeaseCandidate :one
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs
WHERE (state = 'pending' AND not_before <= sqlc.arg(now)::timestamptz) OR (state = 'leased' AND lease_expires_at <= sqlc.arg(now)::timestamptz)
ORDER BY created_at, job_id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- The open jobs for the sweep, locked, in id order. A job that a lease holds is left to the next sweep.
-- name: OpenExecutionJobs :many
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs
WHERE state IN ('pending', 'leased')
ORDER BY job_id
FOR UPDATE SKIP LOCKED;

-- The columns that the lifecycle changes; the guard trigger refuses any other move.
-- name: UpdateExecutionJob :execrows
UPDATE execution_jobs
SET state = sqlc.arg(state), reason = sqlc.narg(reason), attempts = sqlc.arg(attempts), failures = sqlc.arg(failures), github_since = sqlc.narg(github_since),
    not_before = sqlc.arg(not_before), lease_id = sqlc.narg(lease_id), lease_expires_at = sqlc.narg(lease_expires_at), leased_at = sqlc.narg(leased_at),
    deadline_at = sqlc.narg(deadline_at), finished_at = sqlc.narg(finished_at)
WHERE job_id = sqlc.arg(job_id);

-- name: HeadExecutionJobs :many
SELECT job_id, job_key, repository_id, pull_number, plan, profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level,
       receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at,
       leased_at, deadline_at, created_at, finished_at
FROM execution_jobs
WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number) AND head_sha = sqlc.arg(head_sha) AND plan = sqlc.arg(plan)
ORDER BY job_id;

-- name: CountExecutionJobs :many
SELECT state, count(*) AS jobs FROM execution_jobs GROUP BY state;

-- name: InsertExecutionResult :exec
INSERT INTO execution_results (job_id, status, origin, reason, executor_version, executor_sha256, image_digest, toolchain, runtime, started_at, ended_at, received_at,
                               body_digest, markers, submodules, unparsed)
VALUES (sqlc.arg(job_id), sqlc.arg(status), sqlc.arg(origin), sqlc.narg(reason), sqlc.narg(executor_version), sqlc.narg(executor_sha256), sqlc.narg(image_digest),
        sqlc.narg(toolchain), sqlc.narg(runtime), sqlc.arg(started_at), sqlc.arg(ended_at), sqlc.arg(received_at), sqlc.narg(body_digest), sqlc.arg(markers), sqlc.arg(submodules),
        sqlc.arg(unparsed));

-- name: GetExecutionResult :one
SELECT job_id, status, origin, reason, executor_version, executor_sha256, image_digest, toolchain, runtime, started_at, ended_at, received_at, body_digest, markers, submodules, unparsed
FROM execution_results WHERE job_id = sqlc.arg(job_id);

-- name: InsertExecutionTests :copyfrom
INSERT INTO execution_tests (job_id, package, name, file, kind, excluded_reason, head_runs, base_runs, state)
VALUES (sqlc.arg(job_id), sqlc.arg(package), sqlc.arg(name), sqlc.arg(file), sqlc.arg(kind), sqlc.narg(excluded_reason), sqlc.arg(head_runs), sqlc.narg(base_runs), sqlc.arg(state));

-- name: ListExecutionTests :many
SELECT job_id, package, name, file, kind, excluded_reason, head_runs, base_runs, state
FROM execution_tests WHERE job_id = sqlc.arg(job_id)
ORDER BY package COLLATE "C", name COLLATE "C";

-- True when a row names the job and its command entry was created at or after since.
-- name: ExecutionJobUnblocked :one
SELECT EXISTS (
    SELECT 1 FROM execution_unblocks u
    JOIN pr_commands c ON c.repository_id = u.repository_id AND c.comment_id = u.comment_id
    WHERE u.job_id = sqlc.arg(job_id) AND c.created_at >= sqlc.arg(since)::timestamptz
);

-- Rows affected: 0 means the command already unblocked the job.
-- name: InsertExecutionUnblock :execrows
INSERT INTO execution_unblocks (repository_id, comment_id, job_id, pull_number, head_sha, decided_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(comment_id), sqlc.arg(job_id), sqlc.arg(pull_number), sqlc.arg(head_sha), sqlc.arg(decided_at))
ON CONFLICT (repository_id, comment_id, job_id) DO NOTHING;

-- name: ListExecutionUnblocks :many
SELECT job_id FROM execution_unblocks WHERE repository_id = sqlc.arg(repository_id) AND comment_id = sqlc.arg(comment_id) ORDER BY job_id;

-- Rows affected: 0 means the comment already has a receipt state, which GetApprovalReceipt reads.
-- name: InsertApprovalReceipt :execrows
INSERT INTO approval_receipts (repository_id, comment_id, head_sha, receipt_level, run_state, job_id, summary, unblocked, decided_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(comment_id), sqlc.arg(head_sha), sqlc.arg(receipt_level), sqlc.arg(run_state), sqlc.narg(job_id), sqlc.narg(summary),
        sqlc.arg(unblocked), sqlc.arg(decided_at))
ON CONFLICT (repository_id, comment_id) DO NOTHING;

-- name: GetApprovalReceipt :one
SELECT repository_id, comment_id, head_sha, receipt_level, run_state, job_id, summary, unblocked, decided_at
FROM approval_receipts WHERE repository_id = sqlc.arg(repository_id) AND comment_id = sqlc.arg(comment_id);

-- The ledger of the Receipt check: the statements of the check ledger (checks.sql) over pr_receipt_checks.

-- name: GetPRReceiptCheck :one
SELECT repository_id, pull_number, head_sha, head_since, state, reason, reference, job_id, unblocked, generation, message_key, decided_at
FROM pr_receipt_checks WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number);

-- Holds the row until the transaction ends: Publish for one pull is serialized here.
-- name: LockPRReceiptCheck :one
SELECT repository_id, pull_number, head_sha, head_since, state, reason, reference, job_id, unblocked, generation, message_key, decided_at
FROM pr_receipt_checks WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number) FOR UPDATE;

-- Rows affected: 0 means the pull already has a row, which LockPRReceiptCheck reads once the concurrent insert committed.
-- name: InsertPRReceiptCheck :execrows
INSERT INTO pr_receipt_checks (repository_id, pull_number, head_sha, head_since, state, reason, reference, job_id, unblocked, generation, message_key, decided_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(pull_number), sqlc.arg(head_sha), sqlc.arg(head_since), sqlc.arg(state), sqlc.arg(reason), sqlc.arg(reference),
        sqlc.arg(job_id), sqlc.arg(unblocked), sqlc.arg(generation), sqlc.arg(message_key), sqlc.arg(decided_at))
ON CONFLICT (repository_id, pull_number) DO NOTHING;

-- name: UpdatePRReceiptCheck :exec
UPDATE pr_receipt_checks
SET head_sha = sqlc.arg(head_sha), head_since = sqlc.arg(head_since), state = sqlc.arg(state), reason = sqlc.arg(reason), reference = sqlc.arg(reference),
    job_id = sqlc.arg(job_id), unblocked = sqlc.arg(unblocked), generation = sqlc.arg(generation), message_key = sqlc.arg(message_key), decided_at = sqlc.arg(decided_at)
WHERE repository_id = sqlc.arg(repository_id) AND pull_number = sqlc.arg(pull_number);
