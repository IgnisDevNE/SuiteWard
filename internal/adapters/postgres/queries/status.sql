-- The relay's periodic job (kind outbox_relay, owned by the River adapter) is the service's own bookkeeping, not work.
-- name: CountJobsByState :many
SELECT state::text AS state, count(*) AS total
FROM river_job
WHERE kind <> 'outbox_relay'
GROUP BY state;

-- name: CountOutboxByState :many
SELECT state, count(*) AS total
FROM outbox
GROUP BY state;
