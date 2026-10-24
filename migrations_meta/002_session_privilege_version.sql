-- Additive migration for session rotation after authorization changes.
-- Apply after migrations_meta/001_initial_schema.sql.
-- Contract: every metadata mutation that changes a user's effective
-- authorization must increment users.privilege_version for affected users
-- in the same transaction. Sessions store the version captured at create/rotate.
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS privilege_version BIGINT NOT NULL DEFAULT 0;

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS privilege_version BIGINT NOT NULL DEFAULT 0;

COMMIT;
