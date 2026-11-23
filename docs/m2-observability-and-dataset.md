# Milestone 2 — Observability, Audit Log, & Dataset Pengujian Besar

Dokumen ini mencatat konfigurasi dataset pengujian berukuran besar untuk migration per batch serta contoh format output structured log JSON dan OpenTelemetry span dari package `internal/observability`.

---

## 1. Dataset Pengujian Besar (`UPDATE` Penuh > 1 Detik)

Berkas seed: [`docker/postgres-init/04-init-large-batch-dataset.sql`](../docker/postgres-init/04-init-large-batch-dataset.sql)

* **Database Target**: `test_ecommerce_pg` (PostgreSQL 16)
* **Tabel**: `large_migration_records`
* **Jumlah Row**: `500.000` baris (`id` `1` s.d. `500000`, memiliki primary key `id` dan index `idx_large_migration_records_status`)

### Skenario Pengukuran (`psql` dengan `\timing on`)

1. **Query `UPDATE` Penuh (Tanpa Batching)**:
   ```sql
   \timing on
   UPDATE large_migration_records
   SET status = 'migrated',
       amount = amount + 1.00,
       payload = md5(payload),
       updated_at = NOW();
   ```

2. **Query `UPDATE` Per Batch (Cursor-Based)**:
   ```sql
   UPDATE large_migration_records
   SET status = 'migrated',
       amount = amount + 1.00,
       payload = md5(payload),
       updated_at = NOW()
   WHERE id > $1 AND id <= $2;
   ```
   * **Atribut Batch**: `id` (unik dan tidak berubah)
   * **Ukuran Batch**: `10.000` row per transaksi (`50` batch)

### Catatan Hasil Eksekusi Lokal
*(Isi angka di bawah setelah menjalankan seed dan `UPDATE` di container PostgreSQL pengujian)*

| Mode Eksekusi | Jumlah Row | Ukuran Batch | Total Durasi |
|---|---:|---:|---:|
| `UPDATE` Penuh (1 transaksi) | `500.000` | — | `[isi hasil \timing, mis. 1842.31 ms]` |
| `UPDATE` Per Batch (worker) | `500.000` | `10.000` | `[isi total durasi dari job status]` |

---

## 2. Format Output Structured Log JSON & Span Tracing

Output dihasilkan oleh `observability.Logger` (`log/slog` JSON ke `stdout`) dan `observability.ConsoleSpanExporter` pada alur `API → queue → worker → lock → batch/query`.

### A. Contoh Output JSON Log (`stdout`)

```json
{"time":"2026-11-22T05:40:12.114Z","level":"INFO","msg":"http.request","event":"http.request","actor":"admin","database":"","status":"success","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"3c8d19e2a4b70512","method":"POST","path":"/api/migrations/14/execute","http_status":202}
{"time":"2026-11-22T05:40:12.129Z","level":"INFO","msg":"sql.start","event":"sql.start","actor":"leader_tim1","database":"test_ecommerce_pg","status":"started","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","query":"UPDATE large_migration_records SET status = $1 WHERE id > $2 AND id <= $3","params":["migrated",0,10000],"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:12.167Z","level":"INFO","msg":"sql.result","event":"sql.result","actor":"leader_tim1","database":"test_ecommerce_pg","status":"success","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","query":"UPDATE large_migration_records SET status = $1 WHERE id > $2 AND id <= $3","params":["migrated",0,10000],"duration_ms":38,"rows":10000,"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:12.171Z","level":"INFO","msg":"sql.commit","event":"sql.commit","actor":"leader_tim1","database":"test_ecommerce_pg","status":"committed","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","duration_ms":4,"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:15.403Z","level":"INFO","msg":"connection.create","event":"connection.create","actor":"admin","database":"test_ecommerce_pg","status":"success","correlation_id":"19b7e204a8c14d33b6f9012e4a7c5d90","password":"[REDACTED]","dsn":"postgres://test_user:[REDACTED]@localhost:5433/test_ecommerce_pg?sslmode=disable"}
```

### B. Contoh Output Span (`ConsoleSpanExporter`)

```json
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"3c8d19e2a4b70512","name":"POST /api/migrations/14/execute","service":"api","start_time":"2026-11-22T05:40:12.108Z","end_time":"2026-11-22T05:40:12.114Z","duration_ms":6,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","http.method":"POST","http.path":"/api/migrations/14/execute","http.status_code":202}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"5f20b81d4c9a6e31","parent_span_id":"3c8d19e2a4b70512","name":"worker.run_migration","service":"worker","start_time":"2026-11-22T05:40:12.121Z","end_time":"2026-11-22T05:40:12.174Z","duration_ms":53,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","job_id":"job-014-029","migration_id":14}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"84d2c10a7f3e9b54","parent_span_id":"5f20b81d4c9a6e31","name":"worker.acquire_db_lock","service":"worker","start_time":"2026-11-22T05:40:12.122Z","end_time":"2026-11-22T05:40:12.127Z","duration_ms":5,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","database":"test_ecommerce_pg"}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","parent_span_id":"5f20b81d4c9a6e31","name":"db.batch_query","service":"worker","start_time":"2026-11-22T05:40:12.129Z","end_time":"2026-11-22T05:40:12.171Z","duration_ms":42,"status":"ok","attributes":{"batch":1,"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","rows":10000}}
```
