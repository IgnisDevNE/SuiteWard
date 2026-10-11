-- +goose Up
--
-- GitHub observations (docs/contracts/persistence.md, "GitHub observations").
-- What synchronization saw on GitHub: evidence, never authority. These rows
-- are not governance facts: they reference no Suite and no governance table.
-- Observation times come from the injected clock; GitHub's times
-- (updated_at, created_at) are stored as GitHub sent them.

CREATE TABLE github_repositories (
    repo text NOT NULL,
    installation_id bigint NOT NULL,
    last_success timestamptz,
    last_error text NOT NULL DEFAULT '',
    last_status integer NOT NULL DEFAULT 0,
    wait_until timestamptz,
    CONSTRAINT github_repositories_pkey PRIMARY KEY (repo),
    CONSTRAINT github_repositories_repo_check CHECK (btrim(repo) <> ''),
    CONSTRAINT github_repositories_installation_check CHECK (installation_id > 0),
    CONSTRAINT github_repositories_last_error_check CHECK (last_error IN ('', 'rate_limited', 'budget_exhausted', 'unauthorized', 'forbidden', 'not_found', 'unprocessable', 'status', 'transport', 'decode')),
    CONSTRAINT github_repositories_last_status_check CHECK (last_status = 0 OR last_status BETWEEN 100 AND 599)
);

CREATE TABLE github_pulls (
    repo text NOT NULL,
    number integer NOT NULL,
    head_sha text NOT NULL,
    head_ref text NOT NULL,
    base_ref text NOT NULL,
    base_sha text NOT NULL,
    updated_at timestamptz NOT NULL,
    first_seen timestamptz NOT NULL,
    last_seen timestamptz NOT NULL,
    not_open_since timestamptz,
    CONSTRAINT github_pulls_pkey PRIMARY KEY (repo, number),
    CONSTRAINT github_pulls_repo_fkey FOREIGN KEY (repo) REFERENCES github_repositories (repo),
    CONSTRAINT github_pulls_number_check CHECK (number > 0)
);

-- A comment version is keyed by (repo, comment_id, updated_at, body_digest): an edit is a
-- new row and the earlier body stays, even for two edits within one second of
-- updated_at. The body is raw bytes. Only not_seen_since may change, and the
-- stored digest must be the digest of the stored body.
CREATE TABLE github_comment_versions (
    repo text NOT NULL,
    comment_id bigint NOT NULL,
    updated_at timestamptz NOT NULL,
    pull_number integer NOT NULL,
    author_id bigint NOT NULL,
    author_login text NOT NULL,
    created_at timestamptz NOT NULL,
    body bytea NOT NULL,
    body_digest text NOT NULL,
    first_seen timestamptz NOT NULL,
    not_seen_since timestamptz,
    CONSTRAINT github_comment_versions_pkey PRIMARY KEY (repo, comment_id, updated_at, body_digest),
    CONSTRAINT github_comment_versions_pull_fkey FOREIGN KEY (repo, pull_number) REFERENCES github_pulls (repo, number),
    CONSTRAINT github_comment_versions_comment_id_check CHECK (comment_id > 0),
    CONSTRAINT github_comment_versions_body_digest_check CHECK (body_digest = 'sha256:' || encode(sha256(body), 'hex'))
);

-- Comments are read and marked per pull.
CREATE INDEX github_comment_versions_pull_idx ON github_comment_versions (repo, pull_number);

-- +goose StatementBegin
CREATE FUNCTION guard_github_comment_versions_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF to_jsonb(NEW) - 'not_seen_since' IS DISTINCT FROM to_jsonb(OLD) - 'not_seen_since' THEN
        RAISE EXCEPTION 'only not_seen_since of a GitHub comment version can change'
            USING ERRCODE = '23514', CONSTRAINT = 'github_comment_versions_immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER github_comment_versions_update_guard BEFORE UPDATE ON github_comment_versions
    FOR EACH ROW EXECUTE FUNCTION guard_github_comment_versions_update();
CREATE TRIGGER github_comment_versions_no_delete BEFORE DELETE ON github_comment_versions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER github_comment_versions_no_truncate BEFORE TRUNCATE ON github_comment_versions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
