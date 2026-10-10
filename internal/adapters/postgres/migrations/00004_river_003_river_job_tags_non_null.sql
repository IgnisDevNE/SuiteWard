-- +goose Up
--
-- River schema migration 003 (line main), vendored unchanged from
-- github.com/riverqueue/river/riverdriver/riverpgxv5 v0.49.0 except that the
-- schema template markers are removed, so the connection's search_path selects
-- the schema. Goose owns the schema (ADR 0026); River's migrator would record
-- the version in river_migration, so the last statement does it here and
-- River's own validation accepts the database. Upgrading River means adding
-- the new River migrations as further goose migrations.

ALTER TABLE river_job ALTER COLUMN tags SET DEFAULT '{}';
UPDATE river_job SET tags = '{}' WHERE tags IS NULL;
ALTER TABLE river_job ALTER COLUMN tags SET NOT NULL;

INSERT INTO river_migration (version) VALUES (3);
