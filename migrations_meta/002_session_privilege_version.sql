-- Additive migration: track the authorization version captured by each session.
-- Apply after migrations_meta/001_initial_schema.sql.
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS privilege_version BIGINT NOT NULL DEFAULT 0;

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS privilege_version BIGINT NOT NULL DEFAULT 0;

COMMIT;
