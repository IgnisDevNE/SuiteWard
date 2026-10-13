-- Owner, TOTP and protection results (docs/contracts/persistence.md, "Owner,
-- TOTP and protection results"). Enroll starts by locking the enrollment table
-- so the generation is computed under the lock.

-- name: LockOwnerEnrollment :exec
LOCK TABLE owner_enrollment IN SHARE ROW EXCLUSIVE MODE;

-- name: GetOwnerEnrollment :one
SELECT generation, account_id, login, secret, tier, policy_revision_id, enrolled_at
FROM owner_enrollment WHERE singleton;

-- name: AnyProjectHasSuite :one
SELECT EXISTS (SELECT 1 FROM suites WHERE project_id = ANY(sqlc.arg(project_ids)::text[]));

-- name: UpsertOwnerEnrollment :exec
INSERT INTO owner_enrollment (singleton, generation, account_id, login, secret, tier, policy_revision_id, enrolled_at)
VALUES (true, sqlc.arg(generation), sqlc.arg(account_id), sqlc.arg(login), sqlc.arg(secret), sqlc.arg(tier), sqlc.arg(policy_revision_id), sqlc.arg(enrolled_at))
ON CONFLICT (singleton) DO UPDATE
SET generation = excluded.generation,
    account_id = excluded.account_id,
    login = excluded.login,
    secret = excluded.secret,
    tier = excluded.tier,
    policy_revision_id = excluded.policy_revision_id,
    enrolled_at = excluded.enrolled_at;

-- name: InsertOwnerTestCheck :exec
INSERT INTO owner_test_checks (repository_id, policy_revision_id, project_id, repo, test_check)
VALUES (sqlc.arg(repository_id), sqlc.arg(policy_revision_id), sqlc.arg(project_id), sqlc.arg(repo), sqlc.arg(test_check));

-- name: ListOwnerTestChecks :many
SELECT repository_id, repo, test_check FROM owner_test_checks
WHERE policy_revision_id = sqlc.arg(policy_revision_id)
ORDER BY repository_id;

-- Rows affected: 0 means the step was already used.
-- name: InsertConsumedStep :execrows
INSERT INTO totp_consumed_steps (account_id, step, consumed_at)
VALUES (sqlc.arg(account_id), sqlc.arg(step), sqlc.arg(consumed_at))
ON CONFLICT (account_id, step) DO NOTHING;

-- One statement, one snapshot: whether the account is the enrolled owner, and whether the step was new.
-- A concurrent insert of the same step waits for the first and then does nothing.
-- name: ConsumeOwnerStep :one
WITH enrolled AS (
    SELECT account_id FROM owner_enrollment WHERE singleton AND account_id = sqlc.arg(account_id)::bigint
), consumed AS (
    INSERT INTO totp_consumed_steps (account_id, step, consumed_at)
    SELECT account_id, sqlc.arg(step)::bigint, sqlc.arg(consumed_at)::timestamptz FROM enrolled
    ON CONFLICT (account_id, step) DO NOTHING
    RETURNING 1
)
SELECT (SELECT count(*) FROM enrolled)::bigint AS enrolled, (SELECT count(*) FROM consumed)::bigint AS consumed;

-- name: UpsertProtectionResult :exec
INSERT INTO github_protection_results (repository_id, result, findings, caveats, checked_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(result), sqlc.arg(findings)::text[], sqlc.arg(caveats)::text[], sqlc.arg(checked_at))
ON CONFLICT (repository_id) DO UPDATE
SET result = excluded.result,
    findings = excluded.findings,
    caveats = excluded.caveats,
    checked_at = excluded.checked_at;

-- name: ListProtectionResults :many
SELECT repository_id, result, findings, caveats, checked_at FROM github_protection_results ORDER BY repository_id;
