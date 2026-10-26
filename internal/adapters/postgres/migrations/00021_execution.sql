-- +goose Up
--
-- Canonical execution and the receipt (docs/contracts/persistence.md, "Execution (M2.1; normative)"; ADR 0028). The execution jobs,
-- their results and tests, the ledger of the SuiteWard / Receipt check, the owner's unblocks and the receipt state at an approve are
-- evidence: nothing here is read by consent or promotion. The insert-only tables reject UPDATE, DELETE and TRUNCATE by
-- reject_immutable_history(); execution_jobs changes only along the lifecycle of the runtime contract, by the guard trigger, and its
-- final rows never change; pr_receipt_checks is updated in place and never deleted. The mode of a command and the five new
-- dispositions are added to pr_commands, whose table is insert-only, so only its CHECK changes.

-- Every path of a list is at most max_bytes and the list has at most max_items. The result is never null, because a CHECK passes null.
-- +goose StatementBegin
CREATE FUNCTION execution_paths_valid(paths text[], max_items integer, max_bytes integer) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(cardinality(paths), 0) <= max_items
       AND coalesce((SELECT bool_and(coalesce(octet_length(p) <= max_bytes, false)) FROM unnest(paths) AS p), true)
$$;
-- +goose StatementEnd

-- The markers of a result: an array of at most 200 objects with a kind, a path and a line.
-- +goose StatementBegin
CREATE FUNCTION execution_markers_valid(markers jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN jsonb_typeof(markers) <> 'array' OR jsonb_array_length(markers) > 200 THEN false
        ELSE coalesce((SELECT bool_and(coalesce(jsonb_typeof(m) = 'object' AND jsonb_typeof(m -> 'kind') = 'string'
                                                AND jsonb_typeof(m -> 'path') = 'string' AND jsonb_typeof(m -> 'line') = 'number', false))
                       FROM jsonb_array_elements(markers) AS m), true) END
$$;
-- +goose StatementEnd

-- One row for each wanted run: a plan on a head, for one binding. The row changes while the job is open and never after.
CREATE TABLE execution_jobs (
    job_id bigint GENERATED ALWAYS AS IDENTITY,
    job_key text NOT NULL,
    repository_id bigint NOT NULL,
    pull_number integer NOT NULL,
    plan text NOT NULL,
    profile text NOT NULL,
    head_sha text NOT NULL,
    base_sha text,
    governing_digest text NOT NULL,
    governing_source text NOT NULL,
    declaration_digest text NOT NULL,
    canonical_level text NOT NULL,
    receipt_level text NOT NULL,
    unapproved_paths text[] NOT NULL DEFAULT '{}',
    changed_paths text[] NOT NULL DEFAULT '{}',
    changed_total integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'pending',
    reason text,
    attempts integer NOT NULL DEFAULT 0,
    failures integer NOT NULL DEFAULT 0,
    github_since timestamptz,
    not_before timestamptz NOT NULL,
    lease_id text,
    lease_expires_at timestamptz,
    leased_at timestamptz,
    deadline_at timestamptz,
    created_at timestamptz NOT NULL,
    finished_at timestamptz,
    CONSTRAINT execution_jobs_pkey PRIMARY KEY (job_id),
    CONSTRAINT execution_jobs_key_check CHECK (job_key ~ '^[0-9a-f]{64}$'),
    CONSTRAINT execution_jobs_repository_check CHECK (repository_id > 0),
    CONSTRAINT execution_jobs_pull_check CHECK (pull_number > 0),
    CONSTRAINT execution_jobs_plan_check CHECK (plan IN ('canonical', 'receipt')),
    CONSTRAINT execution_jobs_profile_check CHECK (profile = 'go'),
    CONSTRAINT execution_jobs_head_check CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT execution_jobs_base_check CHECK (base_sha IS NULL OR base_sha ~ '^[0-9a-f]{40}$'),
    -- A receipt job has a base, except one that was created already unavailable for no merge base, without an executor, or with GitHub
    -- unavailable; a canonical job has none.
    CONSTRAINT execution_jobs_base_plan_check CHECK (CASE plan
        WHEN 'canonical' THEN base_sha IS NULL
        WHEN 'receipt' THEN base_sha IS NOT NULL OR coalesce(state = 'unavailable' AND reason IN ('no-merge-base', 'executor-not-configured', 'github-unavailable'), false)
        ELSE true END), -- an unknown plan is the business of execution_jobs_plan_check
    CONSTRAINT execution_jobs_governing_digest_check CHECK (governing_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT execution_jobs_governing_source_check CHECK (governing_source ~ '^(V[1-9][0-9]{0,8}|P[1-9][0-9]{0,8}-R[1-9][0-9]{0,8})$'),
    CONSTRAINT execution_jobs_declaration_digest_check CHECK (declaration_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT execution_jobs_canonical_level_check CHECK (canonical_level IN ('off', 'advisory', 'required')),
    CONSTRAINT execution_jobs_receipt_level_check CHECK (receipt_level IN ('off', 'advisory', 'before-approval', 'required')),
    CONSTRAINT execution_jobs_plan_level_check CHECK ((plan <> 'canonical' OR canonical_level <> 'off') AND (plan <> 'receipt' OR receipt_level <> 'off')),
    CONSTRAINT execution_jobs_unapproved_paths_check CHECK (execution_paths_valid(unapproved_paths, 100, 1024)),
    CONSTRAINT execution_jobs_changed_paths_check CHECK (execution_paths_valid(changed_paths, 100, 1024)),
    CONSTRAINT execution_jobs_canonical_paths_check CHECK (plan <> 'canonical' OR (cardinality(unapproved_paths) = 0 AND cardinality(changed_paths) = 0)),
    CONSTRAINT execution_jobs_changed_total_check CHECK (changed_total >= 0),
    CONSTRAINT execution_jobs_state_check CHECK (state IN ('pending', 'leased', 'complete', 'unavailable', 'cancelled')),
    -- A complete job has no reason. An unavailable or cancelled job has one, from its own set. An open job holds the reason of its last
    -- failed attempt, or github-unavailable while a GitHub read is failing, and is null until something failed.
    CONSTRAINT execution_jobs_reason_check CHECK (CASE state
        WHEN 'complete' THEN reason IS NULL
        WHEN 'unavailable' THEN coalesce(reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'lease-expired', 'timeout', 'github-unavailable',
            'no-executor', 'fetch-failed', 'install-failed', 'resource-limit', 'toolchain-mismatch', 'source-mismatch', 'too-large', 'profile-unsupported',
            'executor-not-configured', 'no-merge-base'), false)
        WHEN 'cancelled' THEN coalesce(reason IN ('head-moved', 'closed', 'obsolete'), false)
        ELSE reason IS NULL OR reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'lease-expired', 'timeout', 'fetch-failed', 'github-unavailable') END),
    CONSTRAINT execution_jobs_attempts_check CHECK (attempts >= 0),
    CONSTRAINT execution_jobs_failures_check CHECK (failures >= 0),
    CONSTRAINT execution_jobs_lease_id_check CHECK (lease_id IS NULL OR lease_id ~ '^[0-9a-f]{32}$'),
    -- The latest lease stays when it ends, so that a result sent again for a finished job is recognized.
    CONSTRAINT execution_jobs_lease_check CHECK ((lease_id IS NOT NULL) = (attempts > 0) AND (leased_at IS NOT NULL) = (attempts > 0)),
    CONSTRAINT execution_jobs_leased_check CHECK ((lease_expires_at IS NOT NULL) = (state = 'leased') AND (deadline_at IS NOT NULL) = (state = 'leased')),
    CONSTRAINT execution_jobs_finished_check CHECK ((finished_at IS NOT NULL) = (state IN ('complete', 'unavailable', 'cancelled')))
);

-- The job key is unique among the current jobs: those that are not cancelled, and not unavailable only because no executor was configured
-- or seen, which a fresh job replaces once one is.
CREATE UNIQUE INDEX execution_jobs_key_current_idx ON execution_jobs (job_key)
    WHERE state <> 'cancelled' AND NOT (state = 'unavailable' AND reason IN ('executor-not-configured', 'no-executor'));
-- The lease query reads the open jobs oldest first.
CREATE INDEX execution_jobs_ready_idx ON execution_jobs (created_at, job_id) WHERE state IN ('pending', 'leased');
CREATE INDEX execution_jobs_head_idx ON execution_jobs (repository_id, pull_number, head_sha);

-- A job is inserted pending, or already unavailable for no-merge-base, executor-not-configured or github-unavailable, with no attempt.
-- +goose StatementBegin
CREATE FUNCTION guard_execution_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state IN ('leased', 'complete', 'cancelled')
        OR (NEW.state = 'unavailable' AND (NEW.reason IS NULL OR NEW.reason NOT IN ('no-merge-base', 'executor-not-configured', 'github-unavailable')))
        OR NEW.attempts > 0 OR NEW.failures > 0 THEN
        RAISE EXCEPTION 'a job is created pending, or already unavailable for a reason of the service'
            USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_initial_state';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A final job cannot change. The identity columns never change. The state moves only along the lifecycle: pending to leased,
-- unavailable or cancelled, and to pending itself only by its reason, its first GitHub failure and its wait; leased to pending,
-- complete, unavailable or cancelled, and to leased itself only by the expiry of the lease (a heartbeat).
-- +goose StatementBegin
CREATE FUNCTION guard_execution_job_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state IN ('complete', 'unavailable', 'cancelled') THEN
        RAISE EXCEPTION 'a finished job cannot change'
            USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_final';
    END IF;
    IF (NEW.job_id, NEW.job_key, NEW.repository_id, NEW.pull_number, NEW.plan, NEW.profile, NEW.head_sha, NEW.base_sha, NEW.governing_digest, NEW.governing_source,
        NEW.declaration_digest, NEW.canonical_level, NEW.receipt_level, NEW.unapproved_paths, NEW.changed_paths, NEW.changed_total, NEW.created_at)
        IS DISTINCT FROM (OLD.job_id, OLD.job_key, OLD.repository_id, OLD.pull_number, OLD.plan, OLD.profile, OLD.head_sha, OLD.base_sha, OLD.governing_digest, OLD.governing_source,
        OLD.declaration_digest, OLD.canonical_level, OLD.receipt_level, OLD.unapproved_paths, OLD.changed_paths, OLD.changed_total, OLD.created_at) THEN
        RAISE EXCEPTION 'the identity of a job cannot change'
            USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_identity';
    END IF;
    IF OLD.state = 'pending' AND NEW.state = 'pending' THEN
        IF (NEW.attempts, NEW.failures, NEW.lease_id, NEW.lease_expires_at, NEW.leased_at, NEW.deadline_at, NEW.finished_at)
            IS DISTINCT FROM (OLD.attempts, OLD.failures, OLD.lease_id, OLD.lease_expires_at, OLD.leased_at, OLD.deadline_at, OLD.finished_at) THEN
            RAISE EXCEPTION 'a pending job changes only its reason, its first GitHub failure and its wait'
                USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_transition';
        END IF;
    ELSIF OLD.state = 'leased' AND NEW.state = 'leased' THEN
        IF (NEW.reason, NEW.attempts, NEW.failures, NEW.github_since, NEW.not_before, NEW.lease_id, NEW.leased_at, NEW.deadline_at, NEW.finished_at)
            IS DISTINCT FROM (OLD.reason, OLD.attempts, OLD.failures, OLD.github_since, OLD.not_before, OLD.lease_id, OLD.leased_at, OLD.deadline_at, OLD.finished_at) THEN
            RAISE EXCEPTION 'a leased job changes only the expiry of its lease'
                USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_transition';
        END IF;
    ELSIF NOT ((OLD.state = 'pending' AND NEW.state IN ('leased', 'unavailable', 'cancelled'))
        OR (OLD.state = 'leased' AND NEW.state IN ('pending', 'complete', 'unavailable', 'cancelled'))) THEN
        RAISE EXCEPTION 'a job cannot move from % to %', OLD.state, NEW.state
            USING ERRCODE = '23514', CONSTRAINT = 'execution_jobs_transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER execution_jobs_insert_guard BEFORE INSERT ON execution_jobs
    FOR EACH ROW EXECUTE FUNCTION guard_execution_job_insert();
CREATE TRIGGER execution_jobs_update_guard BEFORE UPDATE ON execution_jobs
    FOR EACH ROW EXECUTE FUNCTION guard_execution_job_update();
CREATE TRIGGER execution_jobs_no_delete BEFORE DELETE ON execution_jobs
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER execution_jobs_no_truncate BEFORE TRUNCATE ON execution_jobs
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- Insert-only. One row for each job that ended with a result from the executor, complete or unavailable, and for a job that the
-- service ended on the GitHub bound while it was pending (origin service: no executor, image or runtime took part).
CREATE TABLE execution_results (
    job_id bigint NOT NULL,
    status text NOT NULL,
    origin text NOT NULL,
    reason text,
    executor_version text,
    executor_sha256 text,
    image_digest text,
    toolchain text,
    runtime text,
    started_at timestamptz NOT NULL,
    ended_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    body_digest text,
    markers jsonb NOT NULL DEFAULT '[]',
    submodules text[] NOT NULL DEFAULT '{}',
    CONSTRAINT execution_results_pkey PRIMARY KEY (job_id),
    CONSTRAINT execution_results_job_fkey FOREIGN KEY (job_id) REFERENCES execution_jobs (job_id),
    CONSTRAINT execution_results_status_check CHECK (status IN ('complete', 'unavailable')),
    CONSTRAINT execution_results_origin_check CHECK (origin IN ('executor', 'service')),
    CONSTRAINT execution_results_reason_check CHECK ((status = 'unavailable') = (reason IS NOT NULL)
        AND (reason IS NULL OR reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'github-unavailable', 'fetch-failed', 'install-failed',
            'resource-limit', 'toolchain-mismatch', 'source-mismatch', 'too-large', 'profile-unsupported'))),
    CONSTRAINT execution_results_origin_service_check CHECK (origin <> 'service' OR (status = 'unavailable' AND reason = 'github-unavailable')),
    CONSTRAINT execution_results_executor_check CHECK ((executor_version IS NULL) = (origin = 'service') AND (executor_sha256 IS NULL) = (origin = 'service')
        AND (body_digest IS NULL) = (origin = 'service')),
    CONSTRAINT execution_results_executor_version_check CHECK (executor_version IS NULL OR executor_version ~ '^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$'),
    CONSTRAINT execution_results_executor_sha256_check CHECK (executor_sha256 IS NULL OR executor_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT execution_results_image_digest_check CHECK (image_digest IS NULL OR image_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT execution_results_body_digest_check CHECK (body_digest IS NULL OR body_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT execution_results_complete_check CHECK (status <> 'complete' OR (image_digest IS NOT NULL AND toolchain IS NOT NULL AND runtime IS NOT NULL)),
    CONSTRAINT execution_results_toolchain_check CHECK (toolchain IS NULL OR octet_length(toolchain) <= 64),
    CONSTRAINT execution_results_runtime_check CHECK (runtime IS NULL OR (octet_length(runtime) <= 64 AND origin <> 'service')),
    CONSTRAINT execution_results_times_check CHECK (ended_at >= started_at),
    CONSTRAINT execution_results_markers_check CHECK (execution_markers_valid(markers)),
    CONSTRAINT execution_results_submodules_check CHECK (execution_paths_valid(submodules, 100, 4096))
);

CREATE TRIGGER execution_results_immutable BEFORE UPDATE OR DELETE ON execution_results
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER execution_results_no_truncate BEFORE TRUNCATE ON execution_results
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- Insert-only. One row for each test of a complete result. The head and base observations are the facts, and state is the decision that was
-- shown. The plan of the job decides which kinds, states and base runs belong; the store checks that, since it needs the job.
CREATE TABLE execution_tests (
    job_id bigint NOT NULL,
    package text NOT NULL,
    name text NOT NULL,
    file text NOT NULL,
    kind text NOT NULL,
    excluded_reason text,
    head_runs text[] NOT NULL,
    base_runs text[],
    state text NOT NULL,
    CONSTRAINT execution_tests_pkey PRIMARY KEY (job_id, package, name),
    CONSTRAINT execution_tests_result_fkey FOREIGN KEY (job_id) REFERENCES execution_results (job_id),
    CONSTRAINT execution_tests_package_check CHECK (octet_length(package) BETWEEN 1 AND 512),
    CONSTRAINT execution_tests_name_check CHECK (octet_length(name) BETWEEN 1 AND 200),
    CONSTRAINT execution_tests_file_check CHECK (octet_length(file) BETWEEN 1 AND 1024),
    CONSTRAINT execution_tests_kind_check CHECK (kind IN ('declared', 'added', 'changed')),
    CONSTRAINT execution_tests_excluded_check CHECK ((state = 'excluded') = (excluded_reason IS NOT NULL) AND (excluded_reason IS NULL OR excluded_reason IN ('build-constraint', 'nested-module'))),
    CONSTRAINT execution_tests_head_runs_check CHECK (head_runs <@ ARRAY['pass', 'fail', 'build-error', 'skip', 'absent']
        AND (CASE WHEN state = 'excluded' THEN cardinality(head_runs) = 0 ELSE cardinality(head_runs) BETWEEN 1 AND 3 END)),
    CONSTRAINT execution_tests_base_runs_check CHECK (base_runs IS NULL OR (base_runs <@ ARRAY['pass', 'fail', 'build-error', 'skip', 'absent']
        AND cardinality(base_runs) BETWEEN 1 AND 3 AND state <> 'excluded')),
    CONSTRAINT execution_tests_state_check CHECK (state IN ('pass', 'fail', 'skipped', 'missing', 'flaky', 'excluded', 'fails', 'new_api', 'detects', 'inert'))
);

CREATE TRIGGER execution_tests_immutable BEFORE UPDATE OR DELETE ON execution_tests
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER execution_tests_no_truncate BEFORE TRUNCATE ON execution_tests
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- The ledger of the SuiteWard / Receipt check. It mirrors pr_checks (00014): one row for each pull request with the current state and the
-- publication generation, updated in place and never deleted, and the publications themselves in the outbox. It has no foreign key to the
-- outbox, as pr_checks has none. The state also accepts neutral, and job_id and unblocked are added.
CREATE TABLE pr_receipt_checks (
    repository_id bigint NOT NULL,
    pull_number integer NOT NULL,
    head_sha text NOT NULL,
    head_since timestamptz NOT NULL,
    state text NOT NULL,
    reason text NOT NULL,
    reference text NOT NULL,
    job_id bigint NOT NULL,
    unblocked boolean NOT NULL DEFAULT false,
    generation bigint NOT NULL,
    message_key text NOT NULL,
    decided_at timestamptz NOT NULL,
    CONSTRAINT pr_receipt_checks_pkey PRIMARY KEY (repository_id, pull_number),
    CONSTRAINT pr_receipt_checks_repository_check CHECK (repository_id > 0),
    CONSTRAINT pr_receipt_checks_pull_check CHECK (pull_number > 0),
    CONSTRAINT pr_receipt_checks_head_check CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT pr_receipt_checks_reason_check CHECK (btrim(reason) <> ''),
    CONSTRAINT pr_receipt_checks_state_check CHECK (state IN ('success', 'in_progress', 'failure', 'neutral')),
    CONSTRAINT pr_receipt_checks_reference_check CHECK (reference ~ '^(P[0-9]+-R[0-9]+)?$'),
    CONSTRAINT pr_receipt_checks_job_check CHECK (job_id >= 0),
    CONSTRAINT pr_receipt_checks_generation_check CHECK (generation > 0),
    CONSTRAINT pr_receipt_checks_message_key_check CHECK (btrim(message_key) <> '')
);

CREATE TRIGGER pr_receipt_checks_no_delete BEFORE DELETE ON pr_receipt_checks
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER pr_receipt_checks_no_truncate BEFORE TRUNCATE ON pr_receipt_checks
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- Insert-only. The owner's unblock: one row for each job that an unblocked command lifted, written in the same transaction as its
-- pr_commands entry. The row binds to the job and not to the head.
CREATE TABLE execution_unblocks (
    repository_id bigint NOT NULL,
    comment_id bigint NOT NULL,
    job_id bigint NOT NULL,
    pull_number integer NOT NULL,
    head_sha text NOT NULL,
    decided_at timestamptz NOT NULL,
    CONSTRAINT execution_unblocks_pkey PRIMARY KEY (repository_id, comment_id, job_id),
    CONSTRAINT execution_unblocks_command_fkey FOREIGN KEY (repository_id, comment_id) REFERENCES pr_commands (repository_id, comment_id),
    CONSTRAINT execution_unblocks_job_fkey FOREIGN KEY (job_id) REFERENCES execution_jobs (job_id),
    CONSTRAINT execution_unblocks_pull_check CHECK (pull_number > 0),
    CONSTRAINT execution_unblocks_head_check CHECK (head_sha ~ '^[0-9a-f]{40}$')
);

CREATE TRIGGER execution_unblocks_immutable BEFORE UPDATE OR DELETE ON execution_unblocks
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER execution_unblocks_no_truncate BEFORE TRUNCATE ON execution_unblocks
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- Insert-only. The receipt state at the moment of an approve that reached the receipt gate, whether it was refused or not, written in the same
-- transaction as its pr_commands entry.
CREATE TABLE approval_receipts (
    repository_id bigint NOT NULL,
    comment_id bigint NOT NULL,
    head_sha text NOT NULL,
    receipt_level text NOT NULL,
    run_state text NOT NULL,
    job_id bigint,
    summary text,
    unblocked boolean NOT NULL DEFAULT false,
    decided_at timestamptz NOT NULL,
    CONSTRAINT approval_receipts_pkey PRIMARY KEY (repository_id, comment_id),
    CONSTRAINT approval_receipts_command_fkey FOREIGN KEY (repository_id, comment_id) REFERENCES pr_commands (repository_id, comment_id),
    CONSTRAINT approval_receipts_job_fkey FOREIGN KEY (job_id) REFERENCES execution_jobs (job_id),
    CONSTRAINT approval_receipts_head_check CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT approval_receipts_level_check CHECK (receipt_level IN ('advisory', 'before-approval', 'required')),
    CONSTRAINT approval_receipts_run_state_check CHECK (run_state IN ('none', 'pending', 'complete', 'unavailable', 'not-applicable')),
    CONSTRAINT approval_receipts_job_check CHECK ((run_state IN ('pending', 'complete', 'unavailable')) = (job_id IS NOT NULL)),
    CONSTRAINT approval_receipts_summary_check CHECK (summary IS NULL OR (summary IN ('met', 'not-met') AND run_state = 'complete'))
);

CREATE TRIGGER approval_receipts_immutable BEFORE UPDATE OR DELETE ON approval_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER approval_receipts_no_truncate BEFORE TRUNCATE ON approval_receipts
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

-- The mode of a command (the mode word of an approve, or the mode of /suiteward mode) and the five dispositions of the execution commands.
-- None of the five carries an operation id (the rule of 00013 still allows one only for 'processed'), and LatestOwnerCommand lists its
-- dispositions, so none counts as the owner's latest command. pr_commands is insert-only, so only the CHECK is replaced and a column added;
-- the existing rows already satisfy the wider list.
ALTER TABLE pr_commands ADD COLUMN mode text;
ALTER TABLE pr_commands
    DROP CONSTRAINT pr_commands_disposition_check,
    ADD CONSTRAINT pr_commands_disposition_check CHECK (disposition IN ('not-command', 'before-enrollment', 'no-suite', 'edited-first', 'failed',
        'ignored-author', 'malformed', 'invalid-code', 'replayed-code', 'unknown-proposal', 'processed',
        'proposed', 'propose-refused', 'skipped', 'skip-refused', 'suggested', 'suggest-refused', 'other-pull',
        'receipt-refused', 'mode-set', 'mode-refused', 'unblocked', 'unblock-refused')),
    ADD CONSTRAINT pr_commands_mode_check CHECK (mode IS NULL OR mode IN ('fix', 'feature', 'refactor')),
    ADD CONSTRAINT pr_commands_mode_set_check CHECK (disposition <> 'mode-set' OR mode IS NOT NULL);
