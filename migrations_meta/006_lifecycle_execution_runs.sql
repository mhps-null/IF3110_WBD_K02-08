-- Separate one logical execution from its append-only retry attempts.
BEGIN;

ALTER TABLE migration_approval_requirements
    ADD COLUMN IF NOT EXISTS decision TEXT,
    ADD COLUMN IF NOT EXISTS decided_by BIGINT REFERENCES users(id),
    ADD COLUMN IF NOT EXISTS decided_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'ck_migration_approval_decision'
          AND conrelid = 'migration_approval_requirements'::regclass
    ) THEN
        ALTER TABLE migration_approval_requirements
            ADD CONSTRAINT ck_migration_approval_decision
            CHECK (decision IS NULL OR decision IN ('approved', 'rejected'));
    END IF;
END
$$;

UPDATE migration_approval_requirements
SET decision = 'approved',
    decided_by = approved_by,
    decided_at = approved_at
WHERE approved_by IS NOT NULL AND decision IS NULL;

CREATE TABLE IF NOT EXISTS migration_approval_decisions (
    id BIGSERIAL PRIMARY KEY,
    request_id BIGINT NOT NULL REFERENCES migration_requests(id),
    revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    leader_id BIGINT REFERENCES users(id),
    actor_id BIGINT NOT NULL REFERENCES users(id),
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO migration_approval_decisions
    (request_id,revision_id,group_id,leader_id,actor_id,decision,decided_at)
SELECT a.request_id,r.revision_id,a.group_id,a.leader_id,a.approved_by,'approved',COALESCE(a.approved_at,now())
FROM migration_approval_requirements a
JOIN migration_requests r ON r.id=a.request_id
WHERE a.approved_by IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM migration_approval_decisions d
      WHERE d.request_id=a.request_id AND d.group_id=a.group_id
        AND d.actor_id=a.approved_by AND d.decision='approved'
  );

CREATE TABLE IF NOT EXISTS migration_execution_runs (
    id BIGSERIAL PRIMARY KEY,
    request_id BIGINT NOT NULL UNIQUE REFERENCES migration_requests(id),
    db_id BIGINT NOT NULL REFERENCES db_connections(id),
    migration_id BIGINT NOT NULL REFERENCES migration_definitions(id),
    revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
    action TEXT NOT NULL CHECK (action IN ('up', 'rollback', 'forward')),
    actor_id BIGINT NOT NULL REFERENCES users(id),
    sql_snapshot TEXT NOT NULL,
    rollback_of_execution_id BIGINT REFERENCES migration_execution_runs(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_migration_run_immutable ON migration_execution_runs;
DROP TRIGGER IF EXISTS trg_migration_attempt_immutable ON migration_execution_attempts;
DROP FUNCTION IF EXISTS reject_migration_attempt_mutation();

ALTER TABLE migration_execution_attempts
    ADD COLUMN IF NOT EXISTS run_id BIGINT REFERENCES migration_execution_runs(id),
    ADD COLUMN IF NOT EXISTS actor_id BIGINT REFERENCES users(id);

-- Preserve execution IDs where possible so existing rollback references remain stable.
INSERT INTO migration_execution_runs
    (id, request_id, db_id, migration_id, revision_id, action, actor_id,
     sql_snapshot, rollback_of_execution_id, created_at)
SELECT e.id,
       e.request_id,
       e.db_id,
       e.migration_id,
       e.revision_id,
       e.action,
       e.actor_id,
       e.sql_snapshot,
       NULL,
       e.executed_at
FROM migration_executions e
JOIN (
    SELECT request_id, MIN(id) AS first_execution_id
    FROM migration_executions
    GROUP BY request_id
) first_attempt ON first_attempt.first_execution_id = e.id
ON CONFLICT (request_id) DO NOTHING;

UPDATE migration_execution_attempts a
SET run_id = (
        SELECT r.id FROM migration_execution_runs r WHERE r.request_id = a.request_id
    ),
    actor_id = COALESCE(
        a.actor_id,
        (SELECT e.actor_id FROM migration_executions e WHERE e.id = a.execution_id),
        (SELECT q.requested_by FROM migration_requests q WHERE q.id = a.request_id)
    )
WHERE (a.run_id IS NULL OR a.actor_id IS NULL)
  AND EXISTS (
      SELECT 1 FROM migration_execution_runs r WHERE r.request_id = a.request_id
  );

INSERT INTO migration_execution_attempts
    (request_id, execution_id, run_id, attempt_number, actor_id, status, error_message, created_at)
SELECT e.request_id,
       e.id,
       r.id,
       e.attempt_number,
       e.actor_id,
       e.status,
       e.error_message,
       e.executed_at
FROM migration_executions e
JOIN migration_execution_runs r ON r.request_id = e.request_id
WHERE NOT EXISTS (
    SELECT 1
    FROM migration_execution_attempts a
    WHERE a.request_id = e.request_id
      AND a.attempt_number = e.attempt_number
);

UPDATE migration_execution_runs run
SET rollback_of_execution_id = source_run.id
FROM migration_requests q
JOIN migration_executions source_attempt
    ON source_attempt.id = q.target_execution_id
JOIN migration_execution_runs source_run
    ON source_run.request_id = source_attempt.request_id
WHERE run.request_id = q.id
  AND q.action IN ('rollback', 'forward')
  AND run.rollback_of_execution_id IS NULL;

ALTER TABLE migration_execution_attempts
    ALTER COLUMN run_id SET NOT NULL,
    ALTER COLUMN actor_id SET NOT NULL;

UPDATE migration_requests r
SET target_execution_id = run.id
FROM migration_executions old_target
JOIN migration_execution_runs run ON run.request_id = old_target.request_id
WHERE r.target_execution_id = old_target.id;

ALTER TABLE migration_requests
    DROP CONSTRAINT IF EXISTS fk_migration_requests_target_exec;
ALTER TABLE migration_requests
    ADD CONSTRAINT fk_migration_requests_target_exec
    FOREIGN KEY (target_execution_id) REFERENCES migration_execution_runs(id);

SELECT setval(
    pg_get_serial_sequence('migration_execution_runs', 'id'),
    GREATEST(COALESCE((SELECT MAX(id) FROM migration_execution_runs), 0), 1),
    EXISTS (SELECT 1 FROM migration_execution_runs)
);

CREATE OR REPLACE FUNCTION reject_migration_run_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'migration execution runs are append-only';
END
$$;
CREATE TRIGGER trg_migration_run_immutable
    BEFORE UPDATE OR DELETE ON migration_execution_runs
    FOR EACH ROW EXECUTE FUNCTION reject_migration_run_mutation();

CREATE OR REPLACE FUNCTION reject_migration_attempt_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'migration attempt history is append-only';
END
$$;
DROP TRIGGER IF EXISTS trg_migration_attempt_immutable ON migration_execution_attempts;
CREATE TRIGGER trg_migration_attempt_immutable
    BEFORE UPDATE OR DELETE ON migration_execution_attempts
    FOR EACH ROW EXECUTE FUNCTION reject_migration_attempt_mutation();

CREATE OR REPLACE FUNCTION reject_migration_approval_decision_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'migration approval decisions are append-only';
END
$$;
DROP TRIGGER IF EXISTS trg_migration_approval_decision_immutable ON migration_approval_decisions;
CREATE TRIGGER trg_migration_approval_decision_immutable
    BEFORE UPDATE OR DELETE ON migration_approval_decisions
    FOR EACH ROW EXECUTE FUNCTION reject_migration_approval_decision_mutation();

COMMIT;
