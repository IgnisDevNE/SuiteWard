-- +goose Up
CREATE TABLE suites (
    project_id text NOT NULL CHECK (btrim(project_id) <> ''),
    suite_id text NOT NULL CHECK (btrim(suite_id) <> ''),
    current_version_id text CHECK (current_version_id IS NULL OR btrim(current_version_id) <> ''),
    authority_revision numeric(20,0) NOT NULL,
    governance_payload jsonb NOT NULL,
    CONSTRAINT suites_pkey PRIMARY KEY (project_id, suite_id),
    CONSTRAINT suites_revision_check CHECK (authority_revision BETWEEN 0 AND 18446744073709551615),
    CONSTRAINT suites_payload_check CHECK (jsonb_typeof(governance_payload) = 'object')
);

CREATE TABLE suite_versions (
    project_id text NOT NULL,
    suite_id text NOT NULL,
    version_id text NOT NULL CHECK (btrim(version_id) <> ''),
    manifest_digest text NOT NULL,
    version_payload jsonb NOT NULL CHECK (jsonb_typeof(version_payload) = 'object'),
    CONSTRAINT suite_versions_pkey PRIMARY KEY (project_id, suite_id, version_id),
    CONSTRAINT suite_versions_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites(project_id, suite_id),
    CONSTRAINT suite_versions_digest_check CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE operation_receipts (
    operation_id text NOT NULL CHECK (btrim(operation_id) <> ''),
    project_id text NOT NULL,
    suite_id text NOT NULL,
    kind smallint NOT NULL CHECK (kind BETWEEN 1 AND 4),
    receipt_payload jsonb NOT NULL CHECK (jsonb_typeof(receipt_payload) = 'object'),
    CONSTRAINT operation_receipts_pkey PRIMARY KEY (operation_id),
    CONSTRAINT operation_receipts_scope_key UNIQUE (project_id, suite_id, operation_id),
    CONSTRAINT operation_receipts_scope_kind_key UNIQUE (project_id, suite_id, operation_id, kind),
    CONSTRAINT operation_receipts_suite_fkey FOREIGN KEY (project_id, suite_id) REFERENCES suites(project_id, suite_id)
);

CREATE TABLE consent_sources (
    source_command_id text NOT NULL CHECK (btrim(source_command_id) <> ''),
    project_id text NOT NULL,
    suite_id text NOT NULL,
    operation_id text NOT NULL,
    kind smallint NOT NULL DEFAULT 4 CHECK (kind = 4),
    CONSTRAINT consent_sources_pkey PRIMARY KEY (source_command_id),
    CONSTRAINT consent_sources_operation_fkey FOREIGN KEY (project_id, suite_id, operation_id, kind)
        REFERENCES operation_receipts(project_id, suite_id, operation_id, kind)
);

CREATE TABLE promotions (
    operation_id text NOT NULL,
    project_id text NOT NULL,
    suite_id text NOT NULL,
    operation_kind smallint NOT NULL CHECK (operation_kind BETWEEN 1 AND 3),
    version_id text NOT NULL,
    proposal_id text NOT NULL CHECK (btrim(proposal_id) <> ''),
    proposal_revision_id text NOT NULL CHECK (btrim(proposal_revision_id) <> ''),
    expected_version_id text CHECK (expected_version_id IS NULL OR btrim(expected_version_id) <> ''),
    corrects_version_id text CHECK (corrects_version_id IS NULL OR btrim(corrects_version_id) <> ''),
    carrier_id text NOT NULL CHECK (btrim(carrier_id) <> ''),
    source_revision text NOT NULL CHECK (btrim(source_revision) <> ''),
    target_id text NOT NULL CHECK (btrim(target_id) <> ''),
    recorded_at timestamptz NOT NULL,
    promotion_payload jsonb NOT NULL CHECK (jsonb_typeof(promotion_payload) = 'object'),
    CONSTRAINT promotions_pkey PRIMARY KEY (operation_id),
    CONSTRAINT promotions_scope_key UNIQUE (project_id, suite_id, operation_id),
    CONSTRAINT promotions_version_key UNIQUE (project_id, suite_id, version_id),
    CONSTRAINT promotions_reference_key UNIQUE (project_id, suite_id, proposal_id, proposal_revision_id),
    CONSTRAINT promotions_distinct_version_check CHECK (version_id IS DISTINCT FROM expected_version_id AND version_id IS DISTINCT FROM corrects_version_id),
    CONSTRAINT promotions_operation_fkey FOREIGN KEY (project_id, suite_id, operation_id, operation_kind)
        REFERENCES operation_receipts(project_id, suite_id, operation_id, kind),
    CONSTRAINT promotions_version_fkey FOREIGN KEY (project_id, suite_id, version_id) REFERENCES suite_versions(project_id, suite_id, version_id),
    CONSTRAINT promotions_expected_version_fkey FOREIGN KEY (project_id, suite_id, expected_version_id) REFERENCES suite_versions(project_id, suite_id, version_id),
    CONSTRAINT promotions_corrects_version_fkey FOREIGN KEY (project_id, suite_id, corrects_version_id) REFERENCES suite_versions(project_id, suite_id, version_id)
);

CREATE TABLE audit_events (
    operation_id text NOT NULL,
    project_id text NOT NULL,
    suite_id text NOT NULL,
    event_kind smallint NOT NULL,
    event_payload jsonb NOT NULL CHECK (jsonb_typeof(event_payload) = 'object'),
    CONSTRAINT audit_events_pkey PRIMARY KEY (operation_id),
    CONSTRAINT audit_events_scope_key UNIQUE (project_id, suite_id, operation_id),
    CONSTRAINT audit_events_operation_fkey FOREIGN KEY (project_id, suite_id, operation_id, event_kind)
        REFERENCES operation_receipts(project_id, suite_id, operation_id, kind)
);

CREATE TABLE publication_intents (
    operation_id text NOT NULL,
    project_id text NOT NULL,
    suite_id text NOT NULL,
    publication_payload jsonb NOT NULL CHECK (jsonb_typeof(publication_payload) = 'object'),
    CONSTRAINT publication_intents_pkey PRIMARY KEY (operation_id),
    CONSTRAINT publication_intents_scope_key UNIQUE (project_id, suite_id, operation_id),
    CONSTRAINT publication_intents_promotion_fkey FOREIGN KEY (project_id, suite_id, operation_id)
        REFERENCES promotions(project_id, suite_id, operation_id)
);

CREATE TABLE consent_acknowledgments (
    operation_id text NOT NULL,
    project_id text NOT NULL,
    suite_id text NOT NULL,
    kind smallint NOT NULL DEFAULT 4 CHECK (kind = 4),
    acknowledgment_payload jsonb NOT NULL CHECK (jsonb_typeof(acknowledgment_payload) = 'object'),
    CONSTRAINT consent_acknowledgments_pkey PRIMARY KEY (operation_id),
    CONSTRAINT consent_acknowledgments_operation_fkey FOREIGN KEY (project_id, suite_id, operation_id, kind)
        REFERENCES operation_receipts(project_id, suite_id, operation_id, kind)
);

ALTER TABLE suites ADD CONSTRAINT suites_current_version_fkey FOREIGN KEY (project_id, suite_id, current_version_id)
    REFERENCES suite_versions(project_id, suite_id, version_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE suites ADD CONSTRAINT suites_current_promotion_fkey FOREIGN KEY (project_id, suite_id, current_version_id)
    REFERENCES promotions(project_id, suite_id, version_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE promotions ADD CONSTRAINT promotions_audit_fkey FOREIGN KEY (project_id, suite_id, operation_id)
    REFERENCES audit_events(project_id, suite_id, operation_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE promotions ADD CONSTRAINT promotions_publication_fkey FOREIGN KEY (project_id, suite_id, operation_id)
    REFERENCES publication_intents(project_id, suite_id, operation_id) DEFERRABLE INITIALLY DEFERRED;
