-- +goose Up
--
-- Several profiles in one job (docs/contracts/persistence.md, "Migration 00023: several profiles (M2.2; normative)"; ADR 0030, section 4). The profile of a job
-- is a label of one or more ids joined by hyphens (cpp-go), a job can end unavailable for a missing image (image-missing-<id>), and a complete result keeps the image
-- and toolchain of each of its parts and the approved tests that a receipt removed. Which ids exist, and that the label is sorted and has no id twice, is the
-- business of the domain when it builds the job's Spec; what a column cannot say across tables is the store's rule. The execution tables keep their triggers, and
-- execution_results is insert-only, so only its two columns and its checks change: ALTER TABLE is no DML, and the rows from before carry the defaults.

ALTER TABLE execution_jobs
    DROP CONSTRAINT execution_jobs_profile_check,
    ADD CONSTRAINT execution_jobs_profile_check CHECK (profile ~ '^[a-z][a-z0-9]{0,15}(-[a-z][a-z0-9]{0,15})*$' AND octet_length(profile) <= 32),
    -- Same as 00021, and the unavailable job also takes the family image-missing-<id>. An open job never holds it, because it is not retried.
    DROP CONSTRAINT execution_jobs_reason_check,
    ADD CONSTRAINT execution_jobs_reason_check CHECK (CASE state
        WHEN 'complete' THEN reason IS NULL
        WHEN 'unavailable' THEN coalesce(reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'lease-expired', 'timeout', 'github-unavailable',
            'no-executor', 'fetch-failed', 'install-failed', 'resource-limit', 'toolchain-mismatch', 'source-mismatch', 'too-large', 'profile-unsupported',
            'executor-not-configured', 'no-merge-base') OR reason ~ '^image-missing-[a-z][a-z0-9]{0,15}$', false)
        WHEN 'cancelled' THEN coalesce(reason IN ('head-moved', 'closed', 'obsolete'), false)
        ELSE reason IS NULL OR reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'lease-expired', 'timeout', 'fetch-failed', 'github-unavailable') END);

-- The parts of a result: an array of at most 8 objects, one for each part that ran, each with the profile, the folder, the image digest and the toolchain as
-- strings. Other keys are not refused, as for the markers: the store writes the payload from typed values. A null is not an array, so the result is never null.
-- +goose StatementBegin
CREATE FUNCTION execution_parts_valid(parts jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN coalesce(jsonb_typeof(parts), '') <> 'array' THEN false
        WHEN jsonb_array_length(parts) > 8 THEN false
        ELSE coalesce((SELECT bool_and(coalesce(jsonb_typeof(p) = 'object'
                                                AND jsonb_typeof(p -> 'profile') = 'string' AND p ->> 'profile' ~ '^[a-z][a-z0-9]{0,15}$'
                                                AND jsonb_typeof(p -> 'folder') = 'string' AND octet_length(p ->> 'folder') BETWEEN 1 AND 512
                                                AND jsonb_typeof(p -> 'imageDigest') = 'string' AND p ->> 'imageDigest' ~ '^sha256:[0-9a-f]{64}$'
                                                AND jsonb_typeof(p -> 'toolchain') = 'string' AND octet_length(p ->> 'toolchain') BETWEEN 1 AND 64, false))
                       FROM jsonb_array_elements(parts) AS p), true) END
$$;
-- +goose StatementEnd

-- The approved tests that a receipt removed (SW-62): an array of at most 1000 objects, each with the package, the name and the file as strings, of the bounds of
-- the same columns of execution_tests. Other keys are not refused. A null is not an array, so the result is never null.
-- +goose StatementBegin
CREATE FUNCTION execution_removed_valid(removed jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN coalesce(jsonb_typeof(removed), '') <> 'array' THEN false
        WHEN jsonb_array_length(removed) > 1000 THEN false
        ELSE coalesce((SELECT bool_and(coalesce(jsonb_typeof(r) = 'object'
                                                AND jsonb_typeof(r -> 'package') = 'string' AND octet_length(r ->> 'package') BETWEEN 1 AND 512
                                                AND jsonb_typeof(r -> 'name') = 'string' AND octet_length(r ->> 'name') BETWEEN 1 AND 200
                                                AND jsonb_typeof(r -> 'file') = 'string' AND octet_length(r ->> 'file') BETWEEN 1 AND 1024, false))
                       FROM jsonb_array_elements(removed) AS r), true) END
$$;
-- +goose StatementEnd

-- The reasons of the executor are the same as in 00021, and the result of a job that ended unavailable for a missing image carries the family too. A result
-- that is not complete has no parts and removed no test.
ALTER TABLE execution_results
    DROP CONSTRAINT execution_results_reason_check,
    ADD CONSTRAINT execution_results_reason_check CHECK ((status = 'unavailable') = (reason IS NOT NULL)
        AND (reason IS NULL OR reason IN ('executor-error', 'sandbox-unavailable', 'archive-unavailable', 'github-unavailable', 'fetch-failed', 'install-failed',
            'resource-limit', 'toolchain-mismatch', 'source-mismatch', 'too-large', 'profile-unsupported') OR reason ~ '^image-missing-[a-z][a-z0-9]{0,15}$')),
    ADD COLUMN parts jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN removed jsonb NOT NULL DEFAULT '[]',
    ADD CONSTRAINT execution_results_parts_check CHECK (execution_parts_valid(parts)),
    ADD CONSTRAINT execution_results_parts_complete_check CHECK (status = 'complete' OR parts = '[]'::jsonb),
    ADD CONSTRAINT execution_results_removed_check CHECK (execution_removed_valid(removed)),
    ADD CONSTRAINT execution_results_removed_complete_check CHECK (status = 'complete' OR removed = '[]'::jsonb);
