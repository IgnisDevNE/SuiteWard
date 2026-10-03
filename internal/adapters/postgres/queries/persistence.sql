-- name: GetAuthority :one
SELECT project_id, suite_id, current_version_id, authority_revision::text AS authority_revision, governance_payload
FROM suites WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id);

-- name: LockAuthority :one
SELECT project_id, suite_id, current_version_id, authority_revision::text AS authority_revision, governance_payload
FROM suites WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) FOR UPDATE;

-- name: InsertInitialAuthority :exec
INSERT INTO suites (project_id, suite_id, authority_revision, governance_payload)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(authority_revision)::text::numeric, sqlc.arg(governance_payload));

-- name: CASAuthority :execrows
UPDATE suites
SET authority_revision = sqlc.arg(new_revision)::text::numeric,
    current_version_id = sqlc.narg(new_current)::text,
    governance_payload = sqlc.arg(governance_payload)
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id)
  AND authority_revision = sqlc.arg(expected_revision)::text::numeric
  AND current_version_id IS NOT DISTINCT FROM sqlc.narg(expected_current)::text;

-- name: InsertVersion :exec
INSERT INTO suite_versions (project_id, suite_id, version_id, manifest_digest, version_payload)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(version_id), sqlc.arg(manifest_digest), sqlc.arg(version_payload));

-- name: GetVersion :one
SELECT * FROM suite_versions
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND version_id = sqlc.arg(version_id);

-- name: InsertOperation :exec
INSERT INTO operation_receipts (operation_id, project_id, suite_id, kind, receipt_payload)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(kind), sqlc.arg(receipt_payload));

-- name: FindOperation :one
SELECT * FROM operation_receipts WHERE operation_id = sqlc.arg(operation_id);

-- name: InsertConsentSource :exec
INSERT INTO consent_sources (source_command_id, project_id, suite_id, operation_id)
VALUES (sqlc.arg(source_command_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(operation_id));

-- name: FindConsentSource :one
SELECT receipt.* FROM consent_sources AS source
JOIN operation_receipts AS receipt ON receipt.operation_id = source.operation_id
WHERE source.source_command_id = sqlc.arg(source_command_id);

-- name: InsertPromotion :exec
INSERT INTO promotions (operation_id, project_id, suite_id, operation_kind, version_id, proposal_id, proposal_revision_id,
    expected_version_id, corrects_version_id, carrier_id, source_revision, target_id, recorded_at, promotion_payload)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(operation_kind), sqlc.arg(version_id),
    sqlc.arg(proposal_id), sqlc.arg(proposal_revision_id), sqlc.narg(expected_version_id), sqlc.narg(corrects_version_id),
    sqlc.arg(carrier_id), sqlc.arg(source_revision), sqlc.arg(target_id), sqlc.arg(recorded_at), sqlc.arg(promotion_payload));

-- name: FindPromotionByReference :one
SELECT * FROM promotions
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id)
  AND proposal_id = sqlc.arg(proposal_id) AND proposal_revision_id = sqlc.arg(proposal_revision_id);

-- name: InsertAudit :exec
INSERT INTO audit_events (operation_id, project_id, suite_id, event_kind, event_payload)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(event_kind), sqlc.arg(event_payload));

-- name: InsertPublication :exec
INSERT INTO publication_intents (operation_id, project_id, suite_id, publication_payload)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(publication_payload));

-- name: InsertAcknowledgment :exec
INSERT INTO consent_acknowledgments (operation_id, project_id, suite_id, acknowledgment_payload)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(acknowledgment_payload));
