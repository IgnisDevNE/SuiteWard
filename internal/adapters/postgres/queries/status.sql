-- The service's own bookkeeping jobs are not work: the relay's periodic job (outbox_relay, owned by the River adapter),
-- the GitHub synchronization tick (github_sync_tick), the protection verification (github_protection_check), the
-- bootstrap (github_bootstrap), the command job (github_commands) and the check job (github_checks).
-- name: CountJobsByState :many
SELECT state::text AS state, count(*) AS total
FROM river_job
WHERE kind NOT IN ('outbox_relay', 'github_sync_tick', 'github_protection_check', 'github_bootstrap', 'github_commands', 'github_checks')
GROUP BY state;

-- name: CountOutboxByState :many
SELECT state, count(*) AS total
FROM outbox
GROUP BY state;
