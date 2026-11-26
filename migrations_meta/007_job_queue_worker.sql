-- Milestone 2 (Orang 4): durable job queue, worker lease, batch checkpoint mirror,
-- database holds, and append-only job events. Additive only; requires 001-006.
--
-- Source of truth split:
--   * migration_job_outbox (Orang 3)  : durable "Execute pressed" event, id == job ID.
--   * migration_jobs                  : queue state, lease, retry/backoff, progress.
--   * _dbplatform.migration_ledger    : lives in the TARGET database and is written
--                                       in the same transaction as each migration /
--                                       batch change. It is the evidence used to
--                                       resolve uncertain commits and to resume.
--   * migration_job_batches           : metadata mirror of the target ledger for SPA.
BEGIN;

CREATE TABLE IF NOT EXISTS migration_jobs (
    id                BIGINT PRIMARY KEY REFERENCES migration_job_outbox(id) ON DELETE RESTRICT,
    request_id        BIGINT NOT NULL UNIQUE REFERENCES migration_requests(id) ON DELETE RESTRICT,
    db_id             BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE RESTRICT,
    mode              TEXT NOT NULL DEFAULT 'normal' CHECK (mode IN ('normal', 'batch')),
    op                TEXT NOT NULL DEFAULT 'apply' CHECK (op IN ('apply', 'compensate')),
    status            TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
                          'queued', 'retrying', 'running', 'compensating', 'uncertain',
                          'success', 'failed', 'partial', 'partially_reverted', 'reverted',
                          'cancelled', 'rejected')),
    -- Batch contract (platform-managed cursor). Identifiers are validated and quoted.
    batch_table       TEXT,
    batch_column      TEXT,
    batch_size        INTEGER CHECK (batch_size IS NULL OR batch_size BETWEEN 1 AND 1000000),
    source_job_id     BIGINT REFERENCES migration_jobs(id),
    scope_from        BIGINT,
    scope_to          BIGINT,
    total_rows        BIGINT,
    rows_done         BIGINT NOT NULL DEFAULT 0,
    batches_done      INTEGER NOT NULL DEFAULT 0,
    rows_reverted     BIGINT NOT NULL DEFAULT 0,
    batches_reverted  INTEGER NOT NULL DEFAULT 0,
    last_cursor       BIGINT,
    -- Retry / delivery bookkeeping.
    max_attempts      INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 10),
    retry_count       INTEGER NOT NULL DEFAULT 0,
    deliveries        INTEGER NOT NULL DEFAULT 0,
    recoveries        INTEGER NOT NULL DEFAULT 0,
    next_run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Lease: a worker owns a running job only while lease_expires_at is in the future.
    lease_owner       TEXT,
    lease_expires_at  TIMESTAMPTZ,
    heartbeat_at      TIMESTAMPTZ,
    cancel_requested  BOOLEAN NOT NULL DEFAULT false,
    last_error        TEXT,
    -- Safe request metadata: trace context and actor ID only. Never credentials/session IDs.
    trace_parent      TEXT,
    correlation_id    TEXT,
    enqueued_by       BIGINT REFERENCES users(id),
    idempotency_key   TEXT NOT NULL UNIQUE,
    queued_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (mode = 'normal' OR (batch_size IS NOT NULL AND
           ((batch_table IS NOT NULL AND batch_column IS NOT NULL) OR source_job_id IS NOT NULL)))
);

CREATE INDEX IF NOT EXISTS idx_migration_jobs_claimable
    ON migration_jobs (next_run_at, id) WHERE status IN ('queued', 'retrying', 'uncertain');
CREATE INDEX IF NOT EXISTS idx_migration_jobs_lease
    ON migration_jobs (lease_expires_at) WHERE status IN ('running', 'compensating');
CREATE INDEX IF NOT EXISTS idx_migration_jobs_db ON migration_jobs (db_id, id DESC);

-- Metadata mirror of committed batches (target ledger remains the evidence).
CREATE TABLE IF NOT EXISTS migration_job_batches (
    job_id        BIGINT NOT NULL REFERENCES migration_jobs(id) ON DELETE RESTRICT,
    batch_seq     INTEGER NOT NULL CHECK (batch_seq > 0),
    cursor_from   BIGINT NOT NULL,
    cursor_to     BIGINT NOT NULL,
    rows_affected BIGINT NOT NULL DEFAULT 0,
    status        TEXT NOT NULL CHECK (status IN ('committed', 'reverted')),
    committed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    reverted_at   TIMESTAMPTZ,
    PRIMARY KEY (job_id, batch_seq),
    CHECK (cursor_to > cursor_from)
);

-- A hold keeps every OTHER migration on the same database blocked while a job is
-- partial, partially reverted, compensating, or has an unverified commit outcome.
CREATE TABLE IF NOT EXISTS migration_db_holds (
    job_id     BIGINT PRIMARY KEY REFERENCES migration_jobs(id) ON DELETE RESTRICT,
    db_id      BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE RESTRICT,
    request_id BIGINT NOT NULL REFERENCES migration_requests(id) ON DELETE RESTRICT,
    reason     TEXT NOT NULL CHECK (reason IN ('partial', 'partially_reverted', 'compensating', 'uncertain')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_migration_db_holds_db ON migration_db_holds (db_id);

-- Append-only operational timeline of a job (queued, claimed, lock, batch, retry, ...).
CREATE TABLE IF NOT EXISTS migration_job_events (
    id         BIGSERIAL PRIMARY KEY,
    job_id     BIGINT NOT NULL REFERENCES migration_jobs(id) ON DELETE RESTRICT,
    event      TEXT NOT NULL CHECK (length(event) BETWEEN 1 AND 64),
    status     TEXT NOT NULL,
    message    TEXT,
    worker_id  TEXT,
    attempt    INTEGER,
    batch_seq  INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_migration_job_events_job ON migration_job_events (job_id, id);

CREATE OR REPLACE FUNCTION reject_migration_job_event_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'migration job events are append-only';
END
$$;
DROP TRIGGER IF EXISTS trg_migration_job_event_immutable ON migration_job_events;
CREATE TRIGGER trg_migration_job_event_immutable
    BEFORE UPDATE OR DELETE ON migration_job_events
    FOR EACH ROW EXECUTE FUNCTION reject_migration_job_event_mutation();

-- Backfill: outbox rows created before this migration become normal-mode jobs.
INSERT INTO migration_jobs (id, request_id, db_id, idempotency_key, status, queued_at, created_at)
SELECT o.id, o.request_id, r.db_id, 'job:' || o.id,
       CASE WHEN r.status = 'pending' THEN 'queued'
            WHEN r.status = 'running' THEN 'uncertain'
            WHEN r.status = 'success' THEN 'success'
            ELSE 'failed' END,
       o.created_at, o.created_at
FROM migration_job_outbox o
JOIN migration_requests r ON r.id = o.request_id
ON CONFLICT (id) DO NOTHING;
UPDATE migration_job_outbox SET dispatched_at = now()
WHERE dispatched_at IS NULL AND id IN (SELECT id FROM migration_jobs);

COMMIT;
