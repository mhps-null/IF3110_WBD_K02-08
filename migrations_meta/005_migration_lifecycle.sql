-- Milestone 2: additive metadata; keep Milestone 1 records intact.
BEGIN;
CREATE TABLE IF NOT EXISTS migration_definitions (
 id BIGSERIAL PRIMARY KEY,
 db_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE RESTRICT,
 title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
 author_id BIGINT NOT NULL REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS migration_revisions (
 id BIGSERIAL PRIMARY KEY,
 migration_id BIGINT NOT NULL REFERENCES migration_definitions(id) ON DELETE RESTRICT,
 revision INT NOT NULL CHECK (revision > 0),
 sql_up TEXT NOT NULL CHECK (length(btrim(sql_up)) > 0),
 sql_down TEXT,
 irreversible_reason TEXT,
 checksum TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(migration_id, revision),
 CHECK ((sql_down IS NOT NULL AND length(btrim(sql_down)) > 0 AND irreversible_reason IS NULL)
     OR (sql_down IS NULL AND irreversible_reason IS NOT NULL AND length(btrim(irreversible_reason)) > 0))
);
CREATE TABLE IF NOT EXISTS migration_requests (
 id BIGSERIAL PRIMARY KEY,
 migration_id BIGINT NOT NULL REFERENCES migration_definitions(id),
 revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
 db_id BIGINT NOT NULL REFERENCES db_connections(id),
 action TEXT NOT NULL CHECK(action IN ('up','rollback','forward')),
 target_execution_id BIGINT,
 requested_by BIGINT NOT NULL REFERENCES users(id),
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','pending_review','approved','pending','running','success','failed','rejected')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_migration_requests_db_status ON migration_requests(db_id,status);
CREATE TABLE IF NOT EXISTS migration_approval_requirements (
 request_id BIGINT NOT NULL REFERENCES migration_requests(id) ON DELETE RESTRICT,
 group_id BIGINT NOT NULL REFERENCES groups(id),
 leader_id BIGINT REFERENCES users(id),
 approved_by BIGINT REFERENCES users(id),
 approved_at TIMESTAMPTZ,
 PRIMARY KEY(request_id,group_id)
);
CREATE TABLE IF NOT EXISTS migration_executions (
 id BIGSERIAL PRIMARY KEY,
 request_id BIGINT NOT NULL REFERENCES migration_requests(id),
 db_id BIGINT NOT NULL REFERENCES db_connections(id),
 migration_id BIGINT NOT NULL REFERENCES migration_definitions(id),
 revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
 action TEXT NOT NULL CHECK(action IN ('up','rollback','forward')),
 actor_id BIGINT NOT NULL REFERENCES users(id),
 status TEXT NOT NULL CHECK(status IN ('success','failed')),
 sql_snapshot TEXT NOT NULL,
 error_message TEXT,
 rollback_of_execution_id BIGINT REFERENCES migration_executions(id),
 attempt_number INTEGER NOT NULL DEFAULT 1 CHECK(attempt_number > 0),
 executed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE migration_requests ADD CONSTRAINT fk_migration_requests_target_exec FOREIGN KEY (target_execution_id) REFERENCES migration_executions(id);
CREATE INDEX IF NOT EXISTS idx_migration_executions_db_time ON migration_executions(db_id,id);
CREATE INDEX IF NOT EXISTS idx_request_revision_status ON migration_requests(revision_id,status);
CREATE TABLE IF NOT EXISTS migration_execution_attempts (
 id BIGSERIAL PRIMARY KEY,
 request_id BIGINT NOT NULL REFERENCES migration_requests(id),
 execution_id BIGINT REFERENCES migration_executions(id),
 attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
 status TEXT NOT NULL CHECK (status IN ('running','success','failed','uncertain')),
 error_message TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(request_id,attempt_number)
);
CREATE OR REPLACE FUNCTION reject_migration_attempt_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'migration attempt history is append-only'; END $$;
DROP TRIGGER IF EXISTS trg_migration_attempt_immutable ON migration_execution_attempts;
CREATE TRIGGER trg_migration_attempt_immutable BEFORE UPDATE OR DELETE ON migration_execution_attempts
 FOR EACH ROW EXECUTE FUNCTION reject_migration_attempt_mutation();
CREATE UNIQUE INDEX IF NOT EXISTS uq_success_execution_request ON migration_executions(request_id) WHERE status='success';
-- Durable outbox consumed by Orang 4. Insert in the same transaction as status=pending.
CREATE TABLE IF NOT EXISTS migration_job_outbox (
 id BIGSERIAL PRIMARY KEY,
 request_id BIGINT NOT NULL UNIQUE REFERENCES migration_requests(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 dispatched_at TIMESTAMPTZ
);
-- Append-only history even for privileged users of the application.
CREATE OR REPLACE FUNCTION reject_migration_execution_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'migration execution history is append-only'; END $$;
DROP TRIGGER IF EXISTS trg_execution_immutable ON migration_executions;
CREATE TRIGGER trg_execution_immutable BEFORE UPDATE OR DELETE ON migration_executions
 FOR EACH ROW EXECUTE FUNCTION reject_migration_execution_mutation();
CREATE OR REPLACE FUNCTION reject_migration_revision_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'migration revisions are append-only'; END $$;
DROP TRIGGER IF EXISTS trg_revision_immutable ON migration_revisions;
CREATE TRIGGER trg_revision_immutable BEFORE UPDATE OR DELETE ON migration_revisions
 FOR EACH ROW EXECUTE FUNCTION reject_migration_revision_mutation();
COMMIT;
