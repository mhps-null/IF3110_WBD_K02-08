-- Additive migration for platform activity audit logs (Milestone 2).
-- Apply after migrations_meta/002_session_privilege_version.sql.
BEGIN;

CREATE TABLE IF NOT EXISTS audit_logs (
    id             BIGSERIAL PRIMARY KEY,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor          VARCHAR(150) NOT NULL,
    event          VARCHAR(150) NOT NULL,
    database_name  VARCHAR(150) NOT NULL DEFAULT '',
    status         VARCHAR(64)  NOT NULL,
    correlation_id VARCHAR(128) NOT NULL DEFAULT '',
    trace_id       VARCHAR(64)  NOT NULL DEFAULT '',
    span_id        VARCHAR(32)  NOT NULL DEFAULT '',
    details        JSONB        NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor      ON audit_logs (actor);
CREATE INDEX IF NOT EXISTS idx_audit_logs_event      ON audit_logs (event);
CREATE INDEX IF NOT EXISTS idx_audit_logs_database   ON audit_logs (database_name);

COMMIT;
