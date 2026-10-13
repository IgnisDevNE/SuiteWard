-- +goose Up
--
-- Owner, TOTP and protection results (docs/contracts/persistence.md, "Owner,
-- TOTP and protection results"). Written by setup and the protection job. The
-- policies that Enroll appends are the governance policies; everything else
-- here is trusted local state outside the Suite lock. The TOTP secret is bytes
-- inside the instance (D-IDENTITY, ADR 0027).

CREATE TABLE owner_enrollment (
    singleton boolean NOT NULL DEFAULT true,
    generation bigint NOT NULL,
    account_id bigint NOT NULL,
    login text NOT NULL,
    secret bytea NOT NULL,
    tier text NOT NULL,
    policy_revision_id text NOT NULL,
    enrolled_at timestamptz NOT NULL,
    CONSTRAINT owner_enrollment_pkey PRIMARY KEY (singleton),
    CONSTRAINT owner_enrollment_singleton_check CHECK (singleton),
    CONSTRAINT owner_enrollment_generation_check CHECK (generation >= 1),
    CONSTRAINT owner_enrollment_account_check CHECK (account_id > 0),
    CONSTRAINT owner_enrollment_secret_check CHECK (octet_length(secret) = 20),
    CONSTRAINT owner_enrollment_tier_check CHECK (tier IN ('isolated', 'co-located')),
    CONSTRAINT owner_enrollment_revision_check CHECK (policy_revision_id = 'setup-' || generation)
);

-- One row per project and enrollment generation; the revision is the policy
-- revision Enroll appended for the project in the same transaction.
CREATE TABLE owner_test_checks (
    repository_id bigint NOT NULL,
    policy_revision_id text NOT NULL,
    project_id text NOT NULL,
    repo text NOT NULL,
    test_check text NOT NULL,
    CONSTRAINT owner_test_checks_pkey PRIMARY KEY (repository_id, policy_revision_id),
    CONSTRAINT owner_test_checks_policy_fkey FOREIGN KEY (project_id, policy_revision_id) REFERENCES policies (project_id, revision_id),
    CONSTRAINT owner_test_checks_repository_check CHECK (repository_id > 0),
    CONSTRAINT owner_test_checks_project_check CHECK (project_id = 'github-repo:' || repository_id),
    CONSTRAINT owner_test_checks_test_check_check CHECK (btrim(test_check) <> '' AND octet_length(test_check) <= 200)
);

-- A command is verified against its comment's created_at and may be processed
-- long after: no row is ever purged, or a code could be used twice.
CREATE TABLE totp_consumed_steps (
    account_id bigint NOT NULL,
    step bigint NOT NULL,
    consumed_at timestamptz NOT NULL,
    CONSTRAINT totp_consumed_steps_pkey PRIMARY KEY (account_id, step),
    CONSTRAINT totp_consumed_steps_account_check CHECK (account_id > 0)
);

-- Status, not evidence: the latest verification of each repository.
CREATE TABLE github_protection_results (
    repository_id bigint NOT NULL,
    result text NOT NULL,
    findings text[] NOT NULL DEFAULT '{}',
    caveats text[] NOT NULL DEFAULT '{}',
    checked_at timestamptz NOT NULL,
    CONSTRAINT github_protection_results_pkey PRIMARY KEY (repository_id),
    CONSTRAINT github_protection_results_repository_check CHECK (repository_id > 0),
    CONSTRAINT github_protection_results_result_check CHECK (result IN ('enforced', 'findings', 'enforcement-unavailable', 'unknown')),
    CONSTRAINT github_protection_results_findings_check CHECK ((result = 'findings') = (cardinality(findings) > 0)),
    CONSTRAINT github_protection_results_finding_codes_check CHECK (findings <@ ARRAY['missing-contract-check', 'missing-test-check', 'not-up-to-date', 'admin-bypass']),
    CONSTRAINT github_protection_results_caveat_codes_check CHECK (caveats <@ ARRAY['ruleset-bypass-unverified'])
);

CREATE TRIGGER owner_test_checks_immutable BEFORE UPDATE OR DELETE ON owner_test_checks
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER owner_test_checks_no_truncate BEFORE TRUNCATE ON owner_test_checks
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER totp_consumed_steps_immutable BEFORE UPDATE OR DELETE ON totp_consumed_steps
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER totp_consumed_steps_no_truncate BEFORE TRUNCATE ON totp_consumed_steps
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
