# Orang 4 — Async Engine: Queue, Worker, Lock, Batch, Retry, Recovery, Job Monitoring

Branch `feat/m2-p4`, diturunkan dari `feat/m2-p3` (lifecycle/approval/history Orang 3),
yang diturunkan dari `main` (P1 SPA + P2 observability). Kode utama:

| Lokasi | Isi |
|---|---|
| `migrations_meta/007_job_queue_worker.sql` | Tabel queue `migration_jobs`, mirror checkpoint `migration_job_batches`, `migration_db_holds`, timeline append-only `migration_job_events` |
| `internal/jobs/` | Queue store, worker, lock lintas worker, ledger target, batch/kompensasi, retry/klasifikasi error, metrics Prometheus, service + JSON API |
| `cmd/worker/` | Service worker Go terpisah (composition root sendiri) + endpoint `/metrics` & `/healthz` |
| `cmd/server/main.go` | Registrasi job API (`jobs.Handler`) |
| `frontend/src/pages/JobsMonitorPage.tsx`, `frontend/src/components/jobs/ExecuteDialog.tsx`, `frontend/src/api/jobs.ts` | Job Monitoring SPA & dialog Execute |

## 1. Alur

```text
SPA "Execute" ──POST /api/v2/databases/{db}/requests/{id}/execute──► API (View + Edit)
   │  (1 transaksi metadata: lifecycle.QueueWith → outbox + request=pending + migration_jobs=queued + traceparent)
   ◄── 202 {job_id, status:"queued"}   (tidak menunggu SQL)

cmd/worker (N instance)
   poll ─► Dispatch outbox ─► RecoverExpired lease ─► ClaimNext (FOR UPDATE SKIP LOCKED + lease)
   ─► extract trace context dari job
   ─► AcquireDBLock (advisory lock sesi: metadata per-db + fencing di target)
   ─► lifecycle.Claim (pending→running, revalidasi di dalam lock) ─► lifecycle.Revalidate
   ─► LoadApprovedExecution (SQL immutable dari revisi; tidak pernah dari payload)
   ─► Execute normal (1 transaksi) / batch (1 transaksi per batch)
        tiap transaksi = statement + baris _dbplatform.migration_ledger, COMMIT bersama
   ─► lifecycle.RecordAttempt (success/failed, append-only, termasuk retry)
   ─► migration_jobs status + event + log JSON + span + metrics ─► release lock
SPA Job Monitoring ── polling GET /api/v2/jobs[/id] (hanya baca)
```

Approval dan enqueue adalah dua tindakan berbeda: job hanya dibuat setelah request
`approved` **dan** pengguna menekan Execute. Endpoint lama Orang 3
`POST .../queue` tetap berfungsi (mode normal); outbox-nya diambil oleh dispatcher.

## 2. Queue (brokerless, durable)

Queue = tabel PostgreSQL di metadata DB (diperbolehkan Q&A; tidak in-memory).

* **Job ID** = `migration_job_outbox.id` (kontrak Orang 3), stabil per request.
* **Payload minimum**: job ID, request/execution, `db_id`, `idempotency_key`
  (`job:<id>`), `enqueued_by` (user ID), `trace_parent`, `correlation_id`, opsi
  mode/batch. **Tidak ada** credential, session ID, atau SQL di payload: worker
  mengambil SQL dari revisi immutable dan credential dari `connections.PoolManager`.
* **Claim**: `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)`
  dengan lease (`lease_owner`, `lease_expires_at`, default 30 dtk) yang
  diperpanjang heartbeat tiap `lease/3`. Jika heartbeat mendapati lease sudah
  diambil worker lain, context job dibatalkan (fencing).
* **Restart**: job & checkpoint ada di database, sehingga tetap tersedia setelah
  restart API/worker. Lease kadaluarsa → job `uncertain` (apply) atau `queued`
  (kompensasi) dan diambil worker lain.

### Status job

| Status | Arti | Menahan DB? |
|---|---|---|
| `queued` | Pending (Queue), menunggu worker | – |
| `retrying` | Gagal sementara; dijadwalkan ulang dengan backoff | ya, bila sudah ada batch committed |
| `running` / `compensating` | Dipegang worker (lease + lock) | (request `running`) |
| `uncertain` | Hasil commit belum pasti / worker mati; wajib verifikasi ledger | **ya** |
| `success` | Committed & tercatat di history | – |
| `failed` | Di-rollback, tidak ada batch committed | – |
| `partial` | Sebagian batch committed, belum tuntas | **ya** |
| `partially_reverted` | Kompensasi baru sebagian | **ya** |
| `reverted` | Seluruh batch committed sudah dikompensasi | – |
| `cancelled` / `rejected` | Dibatalkan user / ditolak revalidasi sebelum SQL jalan | – |

Status request (Orang 3) tetap `Draft → Pending Review → Approved → Pending →
Running → Success/Failed`; status job memperinci keadaan worker. UI tidak pernah
menampilkan sukses hanya karena enqueue berhasil.

## 3. Lock lintas worker & pemulihan lock

`internal/jobs/lock.go` — dua advisory lock **session-level** pada koneksi khusus:

1. **Metadata DB**, key `0x4f34<<48 | db_id`: dipakai bersama semua instance
   worker → satu database hanya menjalankan satu migration; database berbeda
   berjalan paralel. Lock dipegang sepanjang job, **termasuk di antara commit batch**.
2. **Target DB**, key tetap: *fencing* di database data itu sendiri. Bila worker
   lama kehilangan koneksi metadata tetapi sesi target masih hidup, worker baru
   tidak bisa mulai sampai sesi lama berakhir (dan transaksinya ikut di-abort).

Namespace key berbeda dari lock transaksi Orang 3 (`db_id+2300000000`).
Lock sesi PostgreSQL otomatis lepas saat proses/koneksi mati, dan
`pg_advisory_unlock` hanya berhasil dari sesi pemiliknya, sehingga worker tidak
mungkin melepas lock milik worker lain. Sebelum tiap statement/batch worker
mem-ping kedua sesi lock; bila hilang, eksekusi dihentikan.

Selain lock, `lifecycle.eligibleTx` (Orang 3) menolak request lain pada database
yang sama selama ada request `pending/running` **atau** baris
`migration_db_holds` (diisi engine ini untuk `partial`, `partially_reverted`,
`compensating`, `uncertain`).

**Runbook pemulihan lock / worker gagal**

1. Worker mati → koneksinya putus → PostgreSQL melepas kedua lock. Tidak perlu tindakan.
2. Lease kadaluarsa (≤ 30 dtk) → `RecoverExpired` menandai job `uncertain`, event `job.lease_expired`.
3. Worker lain mengambil job, memverifikasi ledger target, lalu: mencatat attempt
   (commit terbukti → success; tidak → attempt `failed` + retry terbatas; batch →
   lanjut dari checkpoint).
4. Bila target tidak terjangkau, job tetap `uncertain`, database tetap ditahan,
   dan diverifikasi ulang berkala (tombol *Verifikasi sekarang* di SPA mempercepatnya).
5. Diagnosis manual: `SELECT pid, objid, granted FROM pg_locks WHERE locktype='advisory';`
   lalu `SELECT pg_terminate_backend(pid)` untuk sesi yang benar-benar zombie;
   bukti eksekusi ada di `_dbplatform.migration_ledger` pada database target.

## 4. Idempotency, retry, uncertain commit

* **Ledger target** `_dbplatform.migration_ledger (job_id, direction, batch_seq)`
  PK. Baris ledger di-INSERT **dalam transaksi yang sama** dengan statement/batch.
  Pesan duplikat yang mencoba commit ulang menemukan baris ledger (`ON CONFLICT DO
  NOTHING`) → transaksi di-rollback → **tidak ada double commit**.
* **Klasifikasi** (`retry.go`): SQLSTATE dari server = rollback pasti
  (`40001`, `40P01`, `55P03`, `57014`, `53xxx` → transient; lainnya permanent);
  error koneksi/EOF/timeout tanpa SQLSTATE atau `57P01`/`08xxx` = **uncertain**.
* **Uncertain** → tidak pernah retry buta: worker cek ledger (beberapa kali).
  Ada → dianggap committed. Tidak ada → attempt `failed` lalu retry. Tidak bisa
  dicek → job `uncertain`, DB ditahan.
* **Retry terbatas**: `max_attempts` (default 3, maks 10) dengan backoff eksponensial
  (`WORKER_RETRY_BASE` ×2 sampai `WORKER_RETRY_MAX`). Setiap attempt (gagal maupun
  berhasil) dicatat via `lifecycle.RecordAttempt` di bawah execution ID yang sama;
  retry memakai `lifecycle.Retry` sehingga approval entry yang sama tidak diulang,
  tetapi worker tetap merevalidasi approval & izin sebelum attempt berikutnya.
* Kegagalan **sebelum** claim (target down) tidak membuat attempt; dibatasi
  `MaxDeliveries` (20).

## 5. Batch processing

**Kontrak (cursor dikelola platform)** — opsi Execute `mode=batch`:

* `batch_table` (boleh `schema.table`), `batch_column` (integer, punya unique
  index/PK satu kolom, tidak berubah), `batch_size` (1..1.000.000, default 10.000).
* SQL Up/Down memakai `$1` = cursor bawah (eksklusif) dan `$2` = cursor atas
  (inklusif). Contoh untuk dataset 500.000 baris Orang 2:

```sql
-- SQL Up
UPDATE large_migration_records SET status = 'migrated', updated_at = now() WHERE id > $1 AND id <= $2
-- SQL Down
UPDATE large_migration_records SET status = 'pending' WHERE id > $1 AND id <= $2
```

`Parse` milik validasi Orang 3 menerima placeholder ini. API menolak (400) SQL
ber-placeholder pada mode normal dan SQL tanpa `$1/$2` pada mode batch; identifier
divalidasi regex lalu di-quote `pgx.Identifier`; tipe & keunikan kolom dicek di
katalog target.

**Eksekusi**: cakupan ditetapkan sekali di awal (`min-1 .. max`, `count`) dan
disimpan. Setiap batch: `SELECT max(c) FROM (SELECT col ... WHERE col > $1 AND
col <= scope_to ORDER BY col LIMIT n)` → hanya satu skalar ke worker (tidak memuat
dataset ke memory) → transaksi sendiri: `SQL($1,$2)` + ledger → COMMIT → mirror ke
`migration_job_batches` + progress (`rows_done`, `batches_done`, `last_cursor`).

* **Batch gagal** → hanya transaksi batch itu yang di-rollback; batch sebelumnya
  tetap committed → status `partial`, DB ditahan.
* **Resume** (`POST /jobs/{id}/resume`) → lanjut dari akhir prefix yang committed,
  batch selesai tidak diulang (dibuktikan test dengan `v = v + 1`).
* **Kompensasi** (`POST /jobs/{id}/compensate`) → SQL Down immutable yang sudah
  disetujui per batch, **urutan terbalik**, tiap batch dengan baris ledger
  `compensate`. Gagal di tengah → `partially_reverted` (dapat dilanjutkan). Semua
  selesai → `reverted`, hold dilepas. Ditolak bila migration irreversible atau SQL
  Down tidak mengikuti kontrak batch (buat migration perbaikan). Kompensasi tetap
  memerlukan izin View + Edit dan approval entry yang masih valid
  (`lifecycle.RevalidateApproval`).
* **Rollback** migration hasil batch (alur Orang 3) otomatis berjalan per batch atas
  rentang committed job sumber dalam urutan terbalik; **forward** mewarisi rencana
  batch terakhir migration tersebut bila mode tidak dipilih.
* **Cancel**: job antre dikeluarkan (request kembali `approved`); job batch berjalan
  berhenti setelah batch saat ini commit (batch committed tidak dihapus). Job normal
  yang sedang berjalan tidak dapat dihentikan di tengah statement (409).

## 6. Observability

**Log** (`log/slog` JSON ke stdout, logger shared Orang 2, redaction otomatis).
Contoh nyata dari uji end-to-end (200.000 baris, batch 20.000):

```json
{"level":"INFO","msg":"sql.commit","event":"sql.commit","actor":"user:1","database":"e2e_target","status":"committed","correlation_id":"3954daf251b972ee12281fde8826735b","trace_id":"3954daf251b972ee12281fde8826735b","span_id":"872efe33a885f8fe","query":"UPDATE large_migration_records SET status = 'migrated' WHERE id > $1 AND id <= $2","params":[0,20000],"duration_ms":112,"rows":20000,"migration_id":1,"version":1,"job_id":"1","attempt":1,"batch":1}
```

Event yang dicatat: `worker.start/stop`, `job.lock.acquired`, `sql.start`,
`sql.result`, `sql.commit`, `sql.rollback`, `batch.scope`, `batch.committed`,
`batch.compensated`, `migration.attempt` (dengan salinan SQL), `job.recover`,
`job.finished`, `job.lease_lost`.

**Trace** — context W3C disimpan di job saat Execute dan di-extract worker. Satu
trace mencakup `POST .../execute` (api) → `job.process` → `job.lock.acquire` →
`job.claim_request` → `job.revalidate` → `migration.execute` → `migration.batch` ×N
→ `sql.exec` / `sql.commit` (+ `job.retry`, `job.recover`, `job.verify_commit`,
`job.compensate`). Contoh span (exporter console, stderr):

```json
{"trace_id":"3954daf251b972ee12281fde8826735b","span_id":"872efe33a885f8fe","parent_span_id":"f86e2000bfbded3e","name":"migration.batch","service":"worker","duration_ms":115,"status":"ok","attributes":{"batch":1,"cursor_from":0,"cursor_to":20000}}
```

**Metrics (bonus)** — `GET :9091/metrics` (format Prometheus, label terbatas; ID dan
SQL tidak pernah menjadi label):

| Metric | Tipe |
|---|---|
| `dbplatform_migration_jobs{status}` | gauge (snapshot antrean dari DB, semua worker) |
| `dbplatform_migration_job_transitions_total{status}` | counter |
| `dbplatform_migration_job_retries_total` | counter |
| `dbplatform_migration_queue_wait_seconds` | histogram (enqueue → claim pertama) |
| `dbplatform_migration_lock_wait_seconds` | histogram |
| `dbplatform_migration_duration_seconds{status}` | histogram |
| `dbplatform_migration_batches_processed_total`, `dbplatform_migration_rows_processed_total` | counter |
| `dbplatform_db_pool_connections{pool,state}`, `dbplatform_db_pool_wait_total{pool}`, `dbplatform_db_pool_wait_seconds_total{pool}` | gauge / counter |

Scrape config Prometheus, OTel Collector, dan dashboard Grafana tetap milik Orang 2;
nama metric di atas adalah kontraknya.

## 7. Keamanan

* Semua route job di belakang middleware session + CSRF global; otorisasi backend
  berbasis group access: list/detail = View Only (job pada DB tanpa akses → 404),
  execute/resume/cancel/compensate = View + Edit. SPA hanya menampilkan tombol dari
  `actions` yang dihitung backend.
* Body JSON dibatasi 16 KiB, `DisallowUnknownFields` (field `sql` di payload ditolak).
* Pesan error 4xx ditulis oleh paket ini; error driver disanitasi
  (`migration.SanitizeErrorText`) sebelum disimpan/ditampilkan; 5xx generik.
* Identifier batch divalidasi + di-quote; nilai cursor selalu parameter `$1/$2`.
* Tidak ada credential/session ID di payload, log, event, maupun audit
  (diverifikasi grep pada log E2E).

## 8. Integrasi dengan Orang 3 (perubahan aditif di `internal/lifecycle`)

* `QueueWith(ctx, id, actor, hook)` — `Queue` dengan hook transaksional; outbox
  memakai `ON CONFLICT (request_id)` agar request yang dilepas dapat di-Execute
  ulang dengan job ID yang sama. `Queue` lama tetap ada.
* `eligibleTx` juga menolak database yang memiliki `migration_db_holds` milik request lain.
* `RevalidateApproval` (izin + snapshot approval, untuk kompensasi) dan
  `ReleaseQueued` (pending yang belum di-claim → approved/failed).
* Test integrasi lifecycle memuat `007`; ekspektasi riwayat keputusan pada
  `TestRefreshReviewAfterLeaderAssignmentAndRecordRejection` diperbaiki (satu
  leader dua group menghasilkan dua baris keputusan, konsisten dengan assert di atasnya).

## 9. Pengujian

```bash
go test ./internal/jobs                     # unit: opsi, kontrak SQL batch, klasifikasi error, backoff, aksi, metrics, HTTP authz
TEST_METADATA_DSN=postgres://... go test -tags=integration -race ./internal/jobs ./internal/lifecycle
cd frontend && npm test                     # Job Monitoring: progress/hold/aksi, polling hanya GET, konfirmasi cancel
```

Integration test (PostgreSQL nyata, database metadata & target dibuat per test):

| Test | Membuktikan |
|---|---|
| `TestPublishReturnsJobImmediatelyAndWorkerExecutesAsync` | publish → job ID, SQL belum jalan; worker commit + ledger + history |
| `TestDuplicateDeliveryDoesNotDoubleCommit` | pesan duplikat (termasuk saat request tampak running) tidak commit dua kali |
| `TestUncertainCommitIsVerifiedNotBlindlyRetried` | ack COMMIT hilang → diverifikasi sukses tanpa attempt ekstra; putus sebelum commit → attempt gagal tercatat lalu retry |
| `TestUncertainWithUnreachableTargetHoldsDatabase` | hasil tak terverifikasi → tetap `uncertain`, DB ditahan, tidak dieksekusi ulang; DB lain tidak terblokir |
| `TestPermanentFailureIsRecordedWithoutRetry` / `TestTransientRetryIsBoundedWithBackoff` | kegagalan permanen tanpa retry; transient dibatasi `max_attempts` dengan backoff, semua attempt tercatat |
| `TestBatchSuccessCheckpointsEveryBatch` | 1 checkpoint per batch, tiap baris tepat sekali |
| `TestBatchFailurePartialHoldResumeWithoutRepeating` | batch gagal → partial + hold memblokir migration lain → resume tanpa mengulang |
| `TestCompensationRevertsInReverseAndSupportsPartiallyReverted` | kompensasi terbalik, gagal sebagian → partially reverted → dilanjutkan → reverted; view-only ditolak |
| `TestCompensationRejectedForIrreversibleMigration` | pembatalan tidak aman ditolak |
| `TestWorkerCrashRecoveryResumesFromCheckpoint` | worker crash → lease kadaluarsa → worker lain melanjutkan dari checkpoint |
| `TestCrossWorkerLockSameAndDifferentDatabases` | lock DB sama eksklusif, DB beda paralel, job menunggu lock, lock pulih saat sesi pemegang mati |
| `TestTwoWorkersRunDifferentDatabasesInParallel` | dua worker, dua DB, `pg_sleep(1)` masing-masing selesai < 1,9 dtk |
| `TestRevalidationRejectsStaleApprovalBeforeExecution` | leader berubah setelah approval → ditolak sebelum SQL jalan, request kembali approved |
| `TestCancelQueuedAndAuthorization` | izin execute/list/detail/cancel, cancel antrean, re-execute job ID sama |
| `TestExecuteValidatesBatchContract` | validasi mode/identifier/kolom unik/ukuran batch |
| `TestBatchRollbackAndForwardThroughQueue` | rollback batch atas rentang sumber, forward mewarisi rencana, Hide rollback |
| `TestTraceContextPropagatesThroughQueue` | satu trace API → queue → worker → lock → query |
| `TestDispatchPicksUpLifecycleQueueEndpoint` | endpoint `/queue` lama Orang 3 tetap diproses |

Uji end-to-end manual (server + worker biner nyata, HTTP + CSRF): batch 200.000 baris
(10 × 20.000) selesai `success`; `kill -9` worker di tengah rollback batch setelah 1
batch commit → lease kadaluarsa → worker kedua melanjutkan dari checkpoint → `success`,
seluruh baris kembali `pending`.

## 10. Batasan yang diketahui

* Satu statement per SQL Up/Down (konsekuensi validasi `Parse` Orang 3).
* Kursor batch harus kolom integer unik; kursor komposit/teks tidak didukung.
* Ledger membutuhkan hak `CREATE` pada database target untuk schema `_dbplatform`.
* Statement non-transaksional (mis. `CREATE INDEX CONCURRENTLY`) tidak didukung karena
  bukti commit harus satu transaksi dengan perubahan.
