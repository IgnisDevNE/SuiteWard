-- The latest content verdict of each Suite's canonical version (docs/contracts/persistence.md, "Canonical integrity").

-- name: GetCanonicalIntegrity :one
SELECT project_id, suite_id, version_id, verdict, checked_at
FROM canonical_integrity WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id);

-- Replaces the verdict in place. "unavailable" never replaces "failed" for the same version: the rule is in the one
-- statement, so concurrent writers cannot lose a confirmed failure. In DO UPDATE the table name is the existing row.
-- name: UpsertCanonicalIntegrity :exec
INSERT INTO canonical_integrity (project_id, suite_id, version_id, verdict, checked_at)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(version_id), sqlc.arg(verdict), sqlc.arg(checked_at))
ON CONFLICT (project_id, suite_id) DO UPDATE SET
    version_id = EXCLUDED.version_id,
    verdict = CASE WHEN canonical_integrity.version_id = EXCLUDED.version_id AND canonical_integrity.verdict = 'failed' AND EXCLUDED.verdict = 'unavailable'
                   THEN canonical_integrity.verdict ELSE EXCLUDED.verdict END,
    checked_at = EXCLUDED.checked_at;
