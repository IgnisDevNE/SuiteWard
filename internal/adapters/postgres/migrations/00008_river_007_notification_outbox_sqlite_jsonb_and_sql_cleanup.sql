-- +goose Up
--
-- River schema migration 007 (line main), vendored unchanged from
-- github.com/riverqueue/river/riverdriver/riverpgxv5 v0.49.0 except that the
-- schema template markers are removed, so the connection's search_path selects
-- the schema. Goose owns the schema (ADR 0026); River's migrator would record
-- the version in river_migration, so the last statement does it here and
-- River's own validation accepts the database. Upgrading River means adding
-- the new River migrations as further goose migrations.

--
-- Notification outbox.
--

CREATE TABLE river_notification (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload text NOT NULL,
    topic text NOT NULL,
    CONSTRAINT topic_length CHECK (length(topic) > 0 AND length(topic) < 128)
);

CREATE INDEX river_notification_created_at_idx ON river_notification (created_at);
CREATE INDEX river_notification_topic_id_idx ON river_notification (topic, id);

--
-- SQLite JSONB conversion.
--
-- No-op. PostgreSQL already stores River JSON columns as jsonb.

--
-- SQL cleanup.
--

--
-- Drop unused tables `river_client` and `river_client_queue`.
--

DROP TABLE river_client_queue;
DROP TABLE river_client;

--
-- Adds `DEFAULT 25` to `river_job.max_attempts`.
--

ALTER TABLE river_job
    ALTER COLUMN max_attempts SET DEFAULT 25;

--
-- Changes `river_queue.updated_at` to have a default of `CURRENT_TIMESTAMP`.
--

ALTER TABLE river_queue
    ALTER COLUMN updated_at SET DEFAULT CURRENT_TIMESTAMP;

INSERT INTO river_migration (line, version) VALUES ('main', 7);
