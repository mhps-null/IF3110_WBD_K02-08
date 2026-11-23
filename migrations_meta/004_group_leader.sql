-- Additive migration for group leader management (Milestone 2).
-- Apply after migrations_meta/003_audit_logs.sql.
-- Policy notes (per Milestone 2 Spec & Q&A):
-- 1. leader_user_id is nullable: a group with DB access but no leader keeps
--    approval workflows in Pending state rather than rejecting them.
-- 2. No UNIQUE constraint on leader_user_id: one user may lead multiple groups.
-- 3. Application logic ensures a leader is an active member of the group;
--    removing a member clears leader_user_id if that member was the leader.
BEGIN;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS leader_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_groups_leader_user_id ON groups (leader_user_id);

COMMIT;
