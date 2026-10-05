-- +goose Up
--
-- Normalized governance schema (docs/contracts/persistence.md, "Tables").
--
-- Stable text codes owned by this schema. They are the snake_case domain enum
-- names without the type prefix, never Go iota values:
--   principal kind       (contract.PrincipalKind)      human, agent, service
--   consent action       (contract.ConsentAction)      approve, revoke
--   consent outcome      (contract.ConsentOutcome)     approved, revoked, no_active_approval, rejected
--   consent reason       (contract.ConsentReason)      none, unauthorized, unknown_revision, superseded_revision,
--                                                      context_mismatch, policy_mismatch, obsolete_command
--                                                      (command_conflict is returned but never stored)
--   integrity outcome    (contract.IntegrityOutcome)   passed, failed, unavailable
--   operation kind       (governance.OperationKind)    promote, bootstrap, correct, consent
--   integration kind     (contract.IntegrationKind)    merged_change, existing_baseline; part of the operation
--                                                      receipt payload, so it has no column.

CREATE TABLE policies (
    project_id text NOT NULL CHECK (btrim(project_id) <> ''),
    revision_id text NOT NULL CHECK (btrim(revision_id) <> ''),
    owner_id text NOT NULL CHECK (btrim(owner_id) <> ''),
    owner_kind text NOT NULL,
    CONSTRAINT policies_pkey PRIMARY KEY (project_id, revision_id),
    CONSTRAINT policies_owner_kind_check CHECK (owner_kind IN ('human', 'agent', 'service'))
);

CREATE TABLE suites (
    project_id text NOT NULL CHECK (btrim(project_id) <> ''),
    suite_id text NOT NULL CHECK (btrim(suite_id) <> ''),
    revision bigint NOT NULL,
    current_version_id text CHECK (current_version_id IS NULL OR btrim(current_version_id) <> ''),
    target_id text NOT NULL CHECK (btrim(target_id) <> ''),
    policy_revision_id text NOT NULL,
    CONSTRAINT suites_pkey PRIMARY KEY (project_id, suite_id),
    CONSTRAINT suites_revision_check CHECK (revision >= 0),
    CONSTRAINT suites_policy_fkey FOREIGN KEY (project_id, policy_revision_id) REFERENCES policies(project_id, revision_id)
);

CREATE TABLE suite_versions (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    version_id text NOT NULL CHECK (btrim(version_id) <> ''),
    manifest_digest text NOT NULL,
    manifest jsonb NOT NULL,
    CONSTRAINT suite_versions_pkey PRIMARY KEY (project_id, suite_id, version_id),
    CONSTRAINT suite_versions_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites(project_id, suite_id),
    CONSTRAINT suite_versions_manifest_digest_check CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT suite_versions_manifest_check CHECK (jsonb_typeof(manifest) = 'object')
);

-- The Suite points at its current version. The pointer and the version are
-- written in one transaction, so the constraint is checked at commit.
ALTER TABLE suites ADD CONSTRAINT suites_current_version_fkey
    FOREIGN KEY (project_id, suite_id, current_version_id) REFERENCES suite_versions(project_id, suite_id, version_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE proposals (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    proposal_id text NOT NULL CHECK (btrim(proposal_id) <> ''),
    carrier_id text NOT NULL CHECK (btrim(carrier_id) <> ''),
    CONSTRAINT proposals_pkey PRIMARY KEY (project_id, suite_id, proposal_id),
    CONSTRAINT proposals_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites(project_id, suite_id)
);

CREATE TABLE proposal_revisions (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    proposal_id text NOT NULL,
    revision_id text NOT NULL CHECK (btrim(revision_id) <> ''),
    seq bigint NOT NULL,
    origin text NOT NULL CHECK (btrim(origin) <> ''),
    carrier_id text NOT NULL CHECK (btrim(carrier_id) <> ''),
    manifest_digest text NOT NULL,
    scope_digest text NOT NULL,
    covered_inputs jsonb NOT NULL,
    expected_version_id text CHECK (expected_version_id IS NULL OR btrim(expected_version_id) <> ''),
    policy_revision_id text NOT NULL CHECK (btrim(policy_revision_id) <> ''),
    CONSTRAINT proposal_revisions_pkey PRIMARY KEY (project_id, suite_id, proposal_id, revision_id),
    CONSTRAINT proposal_revisions_seq_key UNIQUE (project_id, suite_id, proposal_id, seq),
    CONSTRAINT proposal_revisions_proposal_fkey FOREIGN KEY (project_id, suite_id, proposal_id) REFERENCES proposals(project_id, suite_id, proposal_id),
    CONSTRAINT proposal_revisions_seq_check CHECK (seq >= 1),
    CONSTRAINT proposal_revisions_manifest_digest_check CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT proposal_revisions_scope_digest_check CHECK (scope_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT proposal_revisions_covered_inputs_check CHECK (jsonb_typeof(covered_inputs) = 'object')
);

CREATE TABLE assessments (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    proposal_id text NOT NULL,
    revision_id text NOT NULL,
    source text NOT NULL CHECK (btrim(source) <> ''),
    evidence_emitter text,
    evidence_source text,
    evidence_revision_id text,
    outcome text,
    CONSTRAINT assessments_pkey PRIMARY KEY (project_id, suite_id, proposal_id, revision_id, source),
    CONSTRAINT assessments_revision_fkey FOREIGN KEY (project_id, suite_id, proposal_id, revision_id)
        REFERENCES proposal_revisions(project_id, suite_id, proposal_id, revision_id),
    -- The evidence binding is rebuilt from this revision of the same proposal; the key is skipped while evidence is missing.
    CONSTRAINT assessments_evidence_revision_fkey FOREIGN KEY (project_id, suite_id, proposal_id, evidence_revision_id)
        REFERENCES proposal_revisions(project_id, suite_id, proposal_id, revision_id),
    -- Evidence is entirely present or entirely absent; absent means missing evidence.
    CONSTRAINT assessments_evidence_check CHECK (
        (evidence_emitter IS NULL AND evidence_source IS NULL AND evidence_revision_id IS NULL AND outcome IS NULL)
        OR (evidence_emitter IS NOT NULL AND btrim(evidence_emitter) <> '' AND evidence_source IS NOT NULL AND btrim(evidence_source) <> '' AND evidence_revision_id IS NOT NULL AND btrim(evidence_revision_id) <> '' AND outcome IS NOT NULL)),
    CONSTRAINT assessments_outcome_check CHECK (outcome IS NULL OR outcome IN ('passed', 'failed', 'unavailable'))
);

CREATE TABLE consent_results (
    source_command_id text NOT NULL CHECK (btrim(source_command_id) <> ''),
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    project_id text NOT NULL,
    suite_id text NOT NULL,
    proposal_id text NOT NULL,
    revision_id text NOT NULL CHECK (btrim(revision_id) <> ''),
    actor_id text NOT NULL CHECK (btrim(actor_id) <> ''),
    actor_kind text NOT NULL,
    carrier_id text NOT NULL CHECK (btrim(carrier_id) <> ''),
    action text NOT NULL,
    command_order bigint NOT NULL,
    outcome text NOT NULL,
    reason text NOT NULL,
    seq bigint GENERATED ALWAYS AS IDENTITY NOT NULL,
    CONSTRAINT consent_results_pkey PRIMARY KEY (source_command_id),
    CONSTRAINT consent_results_operation_key UNIQUE (operation_id),
    CONSTRAINT consent_results_seq_key UNIQUE (seq),
    CONSTRAINT consent_results_scope_key UNIQUE (project_id, suite_id, source_command_id),
    CONSTRAINT consent_results_proposal_fkey FOREIGN KEY (project_id, suite_id, proposal_id) REFERENCES proposals(project_id, suite_id, proposal_id),
    CONSTRAINT consent_results_actor_kind_check CHECK (actor_kind IN ('human', 'agent', 'service')),
    CONSTRAINT consent_results_action_check CHECK (action IN ('approve', 'revoke')),
    CONSTRAINT consent_results_command_order_check CHECK (command_order >= 1),
    CONSTRAINT consent_results_outcome_check CHECK (outcome IN ('approved', 'revoked', 'no_active_approval', 'rejected')),
    CONSTRAINT consent_results_reason_check CHECK (reason IN ('none', 'unauthorized', 'unknown_revision', 'superseded_revision', 'context_mismatch', 'policy_mismatch', 'obsolete_command')),
    CONSTRAINT consent_results_reason_outcome_check CHECK ((outcome = 'rejected') = (reason <> 'none'))
);

CREATE INDEX consent_results_proposal_seq_idx ON consent_results (project_id, suite_id, proposal_id, seq);

CREATE TABLE operations (
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    project_id text NOT NULL,
    suite_id text NOT NULL,
    kind text NOT NULL,
    source_command_id text,
    receipt jsonb NOT NULL,
    CONSTRAINT operations_pkey PRIMARY KEY (operation_id),
    CONSTRAINT operations_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites(project_id, suite_id),
    CONSTRAINT operations_source_fkey FOREIGN KEY (project_id, suite_id, source_command_id) REFERENCES consent_results(project_id, suite_id, source_command_id),
    CONSTRAINT operations_kind_check CHECK (kind IN ('promote', 'bootstrap', 'correct', 'consent')),
    CONSTRAINT operations_source_check CHECK ((kind = 'consent') = (source_command_id IS NOT NULL)),
    CONSTRAINT operations_receipt_check CHECK (jsonb_typeof(receipt) = 'object')
);

CREATE INDEX operations_source_command_idx ON operations (project_id, suite_id, source_command_id) WHERE source_command_id IS NOT NULL;

-- operation_id has no foreign key because seeded history has no receipt.
CREATE TABLE promotions (
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    project_id text NOT NULL,
    suite_id text NOT NULL,
    version_id text NOT NULL,
    proposal_id text NOT NULL,
    revision_id text NOT NULL,
    carrier_id text NOT NULL CHECK (btrim(carrier_id) <> ''),
    source_revision text NOT NULL CHECK (btrim(source_revision) <> ''),
    target_id text NOT NULL CHECK (btrim(target_id) <> ''),
    recorded_at timestamptz NOT NULL,
    corrects_version_id text CHECK (corrects_version_id IS NULL OR btrim(corrects_version_id) <> ''),
    CONSTRAINT promotions_pkey PRIMARY KEY (project_id, suite_id, version_id),
    CONSTRAINT promotions_operation_key UNIQUE (operation_id),
    CONSTRAINT promotions_reference_key UNIQUE (project_id, suite_id, proposal_id, revision_id),
    CONSTRAINT promotions_version_fkey FOREIGN KEY (project_id, suite_id, version_id) REFERENCES suite_versions(project_id, suite_id, version_id),
    CONSTRAINT promotions_revision_fkey FOREIGN KEY (project_id, suite_id, proposal_id, revision_id)
        REFERENCES proposal_revisions(project_id, suite_id, proposal_id, revision_id),
    CONSTRAINT promotions_corrects_fkey FOREIGN KEY (project_id, suite_id, corrects_version_id) REFERENCES suite_versions(project_id, suite_id, version_id),
    CONSTRAINT promotions_corrects_check CHECK (corrects_version_id IS DISTINCT FROM version_id)
);

-- The applied schema version for readiness checks, read from the goose
-- bookkeeping that the migrator maintains.
-- +goose StatementBegin
CREATE FUNCTION schema_version() RETURNS bigint LANGUAGE sql STABLE AS $$
    SELECT COALESCE(max(version_id), 0)::bigint FROM goose_db_version WHERE is_applied
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION reject_immutable_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'governance history cannot be changed'
        USING ERRCODE = '23514', CONSTRAINT = 'immutable_history';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION require_next_suite_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.suite_id IS DISTINCT FROM OLD.suite_id THEN
        RAISE EXCEPTION 'Suite identity cannot be changed'
            USING ERRCODE = '23514', CONSTRAINT = 'suites_identity_immutable';
    END IF;
    IF NEW.target_id IS DISTINCT FROM OLD.target_id OR NEW.policy_revision_id IS DISTINCT FROM OLD.policy_revision_id THEN
        RAISE EXCEPTION 'Suite target and governing policy cannot be changed'
            USING ERRCODE = '23514', CONSTRAINT = 'suites_governance_immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'Suite writes require the exact next revision'
            USING ERRCODE = '23514', CONSTRAINT = 'suites_revision_advance';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER suites_revision_guard BEFORE UPDATE ON suites
    FOR EACH ROW EXECUTE FUNCTION require_next_suite_revision();
CREATE TRIGGER suites_no_delete BEFORE DELETE ON suites
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();

CREATE TRIGGER policies_immutable BEFORE UPDATE OR DELETE ON policies
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER suite_versions_immutable BEFORE UPDATE OR DELETE ON suite_versions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER proposals_immutable BEFORE UPDATE OR DELETE ON proposals
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER proposal_revisions_immutable BEFORE UPDATE OR DELETE ON proposal_revisions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER assessments_immutable BEFORE UPDATE OR DELETE ON assessments
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_results_immutable BEFORE UPDATE OR DELETE ON consent_results
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER operations_immutable BEFORE UPDATE OR DELETE ON operations
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER promotions_immutable BEFORE UPDATE OR DELETE ON promotions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();

CREATE TRIGGER suites_no_truncate BEFORE TRUNCATE ON suites
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER policies_no_truncate BEFORE TRUNCATE ON policies
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER suite_versions_no_truncate BEFORE TRUNCATE ON suite_versions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER proposals_no_truncate BEFORE TRUNCATE ON proposals
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER proposal_revisions_no_truncate BEFORE TRUNCATE ON proposal_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER assessments_no_truncate BEFORE TRUNCATE ON assessments
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_results_no_truncate BEFORE TRUNCATE ON consent_results
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER operations_no_truncate BEFORE TRUNCATE ON operations
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER promotions_no_truncate BEFORE TRUNCATE ON promotions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
