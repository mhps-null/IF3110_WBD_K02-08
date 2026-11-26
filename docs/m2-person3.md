# Orang 3 — Migration Lifecycle, Approval, Rollback/Forward, History

## Baseline

Implemented on branch `feat/m2-p3`, which descends from the master-plan baseline
`feat/m2-p2` at `b4db4f1449e0451f9c077761d8579466cf1d6354`. Files owned by other
members are preserved except for additive SPA integration and registration in
`cmd/server/main.go`.

## Metadata migration (must apply before starting API)

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/005_migration_lifecycle.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/006_lifecycle_execution_runs.sql
```

Requires pre-existing `001` through `004`. The new schema intentionally does
**not** alter `groups.leader_user_id`, because that column is owned by Orang 1.
Migration `006` separates the immutable logical execution (`migration_execution_runs`)
from its append-only attempts (`migration_execution_attempts`). Retries retain the
same execution ID and add another attempt row; actor, status, timestamp, and error
are recorded per attempt. Migration `006` backfills these tables from the initial
`005` representation and moves rollback references to stable execution IDs. The old
M1 migration table and legacy lifecycle execution rows are left intact.
It also persists approval decisions in an append-only `migration_approval_decisions`
table so refreshing a review does not erase prior approvals or rejections.

## API contract

Routes have prefix `/api/v2` and are wrapped by the existing auth middleware
and global CSRF middleware, with backend access checks. All operations use JSON.

| Method | Endpoint | Purpose |
|---|---|---|
| POST | `/databases/{db}/migrations` | Create draft SQL Up/Down or irreversible |
| GET | `/databases/{db}/requests` | List request states |
| GET | `/databases/{db}/requests/{id}` | Detail + immutable SQL + current approvals and decision history |
| PATCH | `/databases/{db}/requests/{id}` | New immutable revision before execution |
| POST | `/databases/{db}/requests/{id}/validate` | PostgreSQL server-side Parse (no execute) |
| POST | `/databases/{db}/requests/{id}/submit` | Syntax validation, submit for all-group review |
| POST | `/databases/{db}/requests/{id}/refresh-review` | Refresh stakeholder/leader snapshot without changing SQL |
| POST | `/databases/{db}/requests/{id}/approve` | Leader decision (all their groups) |
| POST | `/databases/{db}/requests/{id}/reject` | Reject request |
| POST | `/databases/{db}/requests/{id}/queue` | User explicit Execute; durable enqueue, HTTP 202 |
| POST | `/databases/{db}/migrations/{migration}/actions` | Create rollback/forward draft |
| GET | `/databases/{db}/history?hide_rollback=true` | History filtered by execution ID pair |
| GET | `/review-inbox` | Current leader's pending review requests |

The `POST /actions` body includes `action` (`rollback` or `forward`) and
`execution_id` from the selected history row. The server returns HTTP 409 if
that execution is no longer eligible. Each action requires submit, approvals,
and explicit enqueue. After sequential rollbacks, forward restores one
migration at a time in reverse rollback order. A successful new Up replaces
the remaining forward branch. Failed rollback attempts stay visible and leave
the active migration order intact.

### Frontend

The SPA uses `frontend/src/api/client.ts` (`apiRequest()`), which handles
session cookies and CSRF refresh. Domain client: `frontend/src/api/lifecycle.ts`.
Pages: `MigrationsShellPage.tsx`, `ApprovalsInboxPage.tsx` via existing React
Router. The UI never displays enqueue as SQL execution success. Hide rollback
filters only a successful rollback and its referenced execution ID; a later
forward of the same migration remains visible.
History rows expose `available_action` for the reversible active tip and the
next rollback eligible for forward.

Both `/queue` and `/execute` require the selected `revision_id` in the JSON
body. A stale revision returns HTTP 409 without publishing a job.

## Approval logic

- Every group with target database access, **including View Only**, is an
  approval stakeholder.
- One active leader who belongs to multiple groups can approve once for all.
- Group without a leader stays Pending Review; there is no auto-denial.
- The request owner can refresh a pending review after leader/access changes;
  refreshing resets decisions so the current stakeholders must approve.
- Self-approval is allowed per QnA2.
- Revising an unexecuted request returns it to Draft. Current approvals are
  cleared and previous decisions remain in the decision history.
- Approve and reject require the `revision_id` shown to the reviewer. A decision
  for an outdated revision returns HTTP 409.
- Enqueue and worker claim reject approval snapshots when group membership or
  leader has changed; they do **not** silently reuse stale approvals. The
  request owner can refresh the stakeholder snapshot without changing SQL.
- Corrective SQL after a committed or failed execution attempt must be a **new
  migration**. Revisions are never modified or deleted.

## SQL validation limitations

`validateSQL` uses the PostgreSQL extended-protocol Parse step, without
executing any target query. To avoid executing DDL for validation, **one SQL
statement per Up and one per Down** are supported. A multi-statement script
must be decomposed into separate migrations; multi-statement parser errors are
reported as HTTP 400. Validation errors include the SQL field, SQLSTATE, and
character position when PostgreSQL provides it. Submit moves only the validated
revision into review and returns HTTP 409 if the draft changed during validation.
The check requires a reachable PostgreSQL target pool.
This is syntax validation, **not** semantic dry-run/permissions on every
affected object, and does not guarantee a successful execution.

## Worker hand-off contract (Orang 4)

`internal/lifecycle/worker_contract.go`:
- `LoadApprovedExecution(ctx, requestID)` returns immutable `ExecutionSpec`
  including server-side selected SQL; never accept SQL from queue payload.
- `Revalidate(ctx, requestID)` checks current actor permissions, all approval
  stakeholders, and linear rollback/forward order. Call after acquiring the
  **cross-worker target DB lock**, and before each attempt.
- `Claim(ctx, requestID)` atomically moves `pending` to `running`; a duplicate
  claim returns false.
- `RecordAttempt(ctx, requestID, actor, status, attemptNo, errorText)` records
  success **or failure** as a new immutable attempt under the same execution ID.
  The supplied actor is persisted on that attempt. Use only **after target
  commit outcome is known**.
- `RevisionDetails`, `Retry`, `ListHistory` are also available.

The job ID returned from `Queue` is the durable `migration_job_outbox.id`.
**The outbox is not yet consumed by a worker.** The worker must provide:
claim/ack semantics; target-DB lock across worker instances; execution and
batch transactions; attempt numbers; checkpointing; retry bounds/backoff;
idempotency; uncertain-commit reconciliation; cancellation and compensation.
Neither the API nor the domain package performs execution. The worker MUST NOT
mark an ambiguous target commit as Failed and immediately retry, nor release
a DB-level guard while an outcome is still uncertain. Asynchronous end-to-end
behavior cannot be claimed until Orang 4 connects these capabilities.

## Audit and observability

Controller mutations produce safe structured audit events via `internal/audit`.
If audit persistence fails after a mutation commits, the API logs the failure and
returns an explicit response directing the client to refresh before retrying.
Existing observability middleware and CSRF are reused. No SQL body, session
identifier, password, or credential is inserted in event details. Worker logs,
spans, and metrics remain owned by Orang 4 / shared infrastructure of Orang 2.

## Verification boundaries

`go test ./internal/lifecycle` passes with Go 1.26.2. Database integration tests
are in `internal/lifecycle/store_integration_test.go` and require
`TEST_METADATA_DSN`; they cover leader refresh, recorded rejection, stable
execution IDs across retries, and per-attempt actors. They have not been run
without a configured PostgreSQL test server. Frontend build and full repository
tests remain unverified.

### Before demo / merge

1. Run metadata migrations `005` and `006` on a disposable database.
2. Run `go test ./internal/lifecycle ./...` and
   `go test -tags=integration ./internal/lifecycle` with `TEST_METADATA_DSN`.
3. Run `cd frontend && npm ci && npm run build && npm test`.
4. Test revision editing after both successful and failed execution attempts;
   all-group approval including missing leader; stale approval; single active
   rollback; irreversible rollback rejection; forward order; failed rollback
   and correct execution-ID-based Hide rollback.
5. Integrate the Orang 4 worker and confirm job status progresses pending →
   running → success/failed, including duplicate delivery and restart recovery.
