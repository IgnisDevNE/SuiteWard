-- name: LockSuite :one
SELECT * FROM suites
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id)
FOR UPDATE;

-- name: GetPolicy :one
SELECT * FROM policies
WHERE project_id = sqlc.arg(project_id) AND revision_id = sqlc.arg(revision_id);

-- name: ListScheduleEntries :many
SELECT * FROM schedule_entries
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id)
ORDER BY position;

-- name: GetProposal :one
SELECT * FROM proposals
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND proposal_id = sqlc.arg(proposal_id);

-- name: ListProposalRevisions :many
SELECT * FROM proposal_revisions
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND proposal_id = sqlc.arg(proposal_id)
ORDER BY seq;

-- name: ListConsentResults :many
SELECT * FROM consent_results
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND proposal_id = sqlc.arg(proposal_id)
ORDER BY seq;

-- Operation ids that replayed an already processed source command of the proposal.
-- name: ListConsentAliases :many
SELECT operation.operation_id, operation.source_command_id::text AS source_command_id
FROM operations AS operation
JOIN consent_results AS result ON result.project_id = operation.project_id
    AND result.suite_id = operation.suite_id
    AND result.source_command_id = operation.source_command_id
WHERE result.project_id = sqlc.arg(project_id) AND result.suite_id = sqlc.arg(suite_id) AND result.proposal_id = sqlc.arg(proposal_id)
  AND operation.operation_id <> result.operation_id
ORDER BY operation.operation_id;

-- name: GetAssessment :one
SELECT * FROM assessments
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND proposal_id = sqlc.arg(proposal_id)
  AND revision_id = sqlc.arg(revision_id) AND source = sqlc.arg(source);

-- A version with the promotion that created it and the proposal revision that
-- holds the approval binding.
-- name: GetVersion :one
SELECT sqlc.embed(suite_versions), sqlc.embed(promotions), sqlc.embed(proposal_revisions)
FROM suite_versions
JOIN promotions ON promotions.project_id = suite_versions.project_id
    AND promotions.suite_id = suite_versions.suite_id
    AND promotions.version_id = suite_versions.version_id
JOIN proposal_revisions ON proposal_revisions.project_id = promotions.project_id
    AND proposal_revisions.suite_id = promotions.suite_id
    AND proposal_revisions.proposal_id = promotions.proposal_id
    AND proposal_revisions.revision_id = promotions.revision_id
WHERE suite_versions.project_id = sqlc.arg(project_id) AND suite_versions.suite_id = sqlc.arg(suite_id)
  AND suite_versions.version_id = sqlc.arg(version_id);

-- name: GetPromotionByReference :one
SELECT sqlc.embed(promotions), sqlc.embed(proposal_revisions)
FROM promotions
JOIN proposal_revisions ON proposal_revisions.project_id = promotions.project_id
    AND proposal_revisions.suite_id = promotions.suite_id
    AND proposal_revisions.proposal_id = promotions.proposal_id
    AND proposal_revisions.revision_id = promotions.revision_id
WHERE promotions.project_id = sqlc.arg(project_id) AND promotions.suite_id = sqlc.arg(suite_id)
  AND promotions.proposal_id = sqlc.arg(proposal_id) AND promotions.revision_id = sqlc.arg(revision_id);

-- name: GetOperation :one
SELECT * FROM operations WHERE operation_id = sqlc.arg(operation_id);

-- The original operation that processed a source command, not a later alias.
-- name: GetOperationBySource :one
SELECT operation.* FROM operations AS operation
JOIN consent_results AS result ON result.operation_id = operation.operation_id
WHERE result.source_command_id = sqlc.arg(source_command_id);

-- name: InsertPolicy :exec
INSERT INTO policies (project_id, revision_id, owner_id, owner_kind)
VALUES (sqlc.arg(project_id), sqlc.arg(revision_id), sqlc.arg(owner_id), sqlc.arg(owner_kind));

-- name: InsertSuite :exec
INSERT INTO suites (project_id, suite_id, revision, current_version_id, target_id, policy_revision_id, schedule_generation)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(revision), sqlc.narg(current_version_id), sqlc.arg(target_id),
    sqlc.arg(policy_revision_id), sqlc.arg(schedule_generation));

-- name: InsertSuiteVersion :exec
INSERT INTO suite_versions (project_id, suite_id, version_id, manifest_digest, manifest)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(version_id), sqlc.arg(manifest_digest), sqlc.arg(manifest));

-- name: InsertProposal :exec
INSERT INTO proposals (project_id, suite_id, proposal_id, carrier_id)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(proposal_id), sqlc.arg(carrier_id));

-- name: InsertProposalRevision :exec
INSERT INTO proposal_revisions (project_id, suite_id, proposal_id, revision_id, seq, origin, carrier_id,
    manifest_digest, scope_digest, covered_inputs, expected_version_id, policy_revision_id)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(proposal_id), sqlc.arg(revision_id), sqlc.arg(seq), sqlc.arg(origin),
    sqlc.arg(carrier_id), sqlc.arg(manifest_digest), sqlc.arg(scope_digest), sqlc.arg(covered_inputs),
    sqlc.narg(expected_version_id), sqlc.arg(policy_revision_id));

-- name: InsertAssessment :exec
INSERT INTO assessments (project_id, suite_id, proposal_id, revision_id, source, evidence_emitter, evidence_source, evidence_revision_id, outcome)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(proposal_id), sqlc.arg(revision_id), sqlc.arg(source),
    sqlc.narg(evidence_emitter), sqlc.narg(evidence_source), sqlc.narg(evidence_revision_id), sqlc.narg(outcome));

-- name: InsertScheduleEntry :exec
INSERT INTO schedule_entries (project_id, suite_id, proposal_id, carrier_id, position, state)
VALUES (sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(proposal_id), sqlc.arg(carrier_id), sqlc.arg(position), sqlc.arg(state));

-- name: InsertConsentResult :exec
INSERT INTO consent_results (source_command_id, operation_id, project_id, suite_id, proposal_id, revision_id,
    actor_id, actor_kind, carrier_id, action, command_order, outcome, reason)
VALUES (sqlc.arg(source_command_id), sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(proposal_id),
    sqlc.arg(revision_id), sqlc.arg(actor_id), sqlc.arg(actor_kind), sqlc.arg(carrier_id), sqlc.arg(action),
    sqlc.arg(command_order), sqlc.arg(outcome), sqlc.arg(reason));

-- name: InsertOperation :exec
INSERT INTO operations (operation_id, project_id, suite_id, kind, source_command_id, receipt)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(kind), sqlc.narg(source_command_id), sqlc.arg(receipt));

-- name: InsertPromotion :exec
INSERT INTO promotions (operation_id, project_id, suite_id, version_id, proposal_id, revision_id, carrier_id,
    source_revision, target_id, recorded_at, corrects_version_id)
VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(suite_id), sqlc.arg(version_id), sqlc.arg(proposal_id),
    sqlc.arg(revision_id), sqlc.arg(carrier_id), sqlc.arg(source_revision), sqlc.arg(target_id), sqlc.arg(recorded_at),
    sqlc.narg(corrects_version_id));

-- Advances the revision by exactly one, optionally moving the current version
-- and the schedule generation, and returns the new revision.
-- name: BumpSuiteRevision :one
UPDATE suites
SET revision = revision + 1,
    current_version_id = COALESCE(sqlc.narg(current_version_id)::text, current_version_id),
    schedule_generation = COALESCE(sqlc.narg(schedule_generation)::bigint, schedule_generation)
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id)
RETURNING revision;

-- name: UpdateScheduleEntryState :execrows
UPDATE schedule_entries SET state = sqlc.arg(state)
WHERE project_id = sqlc.arg(project_id) AND suite_id = sqlc.arg(suite_id) AND proposal_id = sqlc.arg(proposal_id);

-- name: GetSchemaVersion :one
SELECT schema_version()::bigint AS version;
