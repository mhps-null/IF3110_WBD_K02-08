# Milestone 2 — Observability, Audit Log, Dataset Besar, & Stress Testing (k6)

Dokumen ini mencakup konfigurasi dataset pengujian besar (`> 1 detik`), format output structured JSON log & OpenTelemetry span, arsitektur Bonus Observability (OpenTelemetry Collector, Prometheus, Grafana), serta skenario Stress Testing `k6`.

---

## 1. Dataset Pengujian Besar (`UPDATE` Penuh > 1 Detik)

Berkas seed: [`docker/postgres-init/04-init-large-batch-dataset.sql`](../docker/postgres-init/04-init-large-batch-dataset.sql)

* **Database Target**: `test_ecommerce_pg` (PostgreSQL 16)
* **Tabel**: `large_migration_records`
* **Jumlah Row**: `500.000` baris (`id` `1` s.d. `500000`, primary key `id` + index `idx_large_migration_records_status`)

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

| Mode Eksekusi | Jumlah Row | Ukuran Batch | Catatan Perilaku & Durasi |
|---|---:|---:|---|
| `UPDATE` Penuh (1 transaksi) | `500.000` | — | Memakan waktu `> 1.000 ms` (`1,6 – 2,4 detik`), menahan row lock pada seluruh tabel hingga commit selesai. |
| `UPDATE` Per Batch (worker) | `500.000` | `10.000` | Setiap batch di-commit terpisah (`~30–45 ms` per batch) beserta checkpoint cursor sehingga progres dapat dipantau dan di-resume. |

---

## 2. Format Output Structured Log JSON & Span Tracing

Output dihasilkan oleh `observability.Logger` (`log/slog` JSON ke `stdout`) dan `observability.ConsoleSpanExporter` / `OTLPSpanExporter` pada alur `API → queue → worker → lock → batch/query`.

### A. Contoh Output JSON Log (`stdout`)

```json
{"time":"2026-11-22T05:40:12.114Z","level":"INFO","msg":"http.request","event":"http.request","actor":"admin","database":"","status":"success","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"3c8d19e2a4b70512","method":"POST","path":"/api/v2/databases/1/requests/14/execute","http_status":202}
{"time":"2026-11-22T05:40:12.129Z","level":"INFO","msg":"sql.start","event":"sql.start","actor":"leader_tim1","database":"test_ecommerce_pg","status":"started","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","query":"UPDATE large_migration_records SET status = $1 WHERE id > $2 AND id <= $3","params":["migrated",0,10000],"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:12.167Z","level":"INFO","msg":"sql.result","event":"sql.result","actor":"leader_tim1","database":"test_ecommerce_pg","status":"success","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","query":"UPDATE large_migration_records SET status = $1 WHERE id > $2 AND id <= $3","params":["migrated",0,10000],"duration_ms":38,"rows":10000,"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:12.171Z","level":"INFO","msg":"sql.commit","event":"sql.commit","actor":"leader_tim1","database":"test_ecommerce_pg","status":"committed","correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","duration_ms":4,"migration_id":14,"execution_id":29,"version":3,"job_id":"job-014-029","attempt":1,"batch":1}
{"time":"2026-11-22T05:40:15.403Z","level":"INFO","msg":"connection.create","event":"connection.create","actor":"admin","database":"test_ecommerce_pg","status":"success","correlation_id":"19b7e204a8c14d33b6f9012e4a7c5d90","password":"[REDACTED]","dsn":"postgres://test_user:[REDACTED]@localhost:5433/test_ecommerce_pg?sslmode=disable"}
```

### B. Contoh Output Span (`ConsoleSpanExporter`)

```json
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"3c8d19e2a4b70512","name":"POST /api/v2/databases/1/requests/14/execute","service":"api","start_time":"2026-11-22T05:40:12.108Z","end_time":"2026-11-22T05:40:12.114Z","duration_ms":6,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","http.method":"POST","http.path":"/api/v2/databases/1/requests/14/execute","http.status_code":202}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"5f20b81d4c9a6e31","parent_span_id":"3c8d19e2a4b70512","name":"worker.run_migration","service":"worker","start_time":"2026-11-22T05:40:12.121Z","end_time":"2026-11-22T05:40:12.174Z","duration_ms":53,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","job_id":"job-014-029","migration_id":14}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"84d2c10a7f3e9b54","parent_span_id":"5f20b81d4c9a6e31","name":"worker.acquire_db_lock","service":"worker","start_time":"2026-11-22T05:40:12.122Z","end_time":"2026-11-22T05:40:12.127Z","duration_ms":5,"status":"ok","attributes":{"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","database":"test_ecommerce_pg"}}
{"trace_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","span_id":"91a4f02c6e7b3d88","parent_span_id":"5f20b81d4c9a6e31","name":"db.batch_query","service":"worker","start_time":"2026-11-22T05:40:12.129Z","end_time":"2026-11-22T05:40:12.171Z","duration_ms":42,"status":"ok","attributes":{"batch":1,"correlation_id":"7e9a31c4d2b8401f9a0e6c2b1d4f8a10","rows":10000}}
```

---

## 3. [BONUS] Observability: OTel Collector, Prometheus, & Grafana

### A. Komponen & Berkas Konfigurasi
1. **OpenTelemetry Collector**: [`docker/observability/otel-collector-config.yaml`](../docker/observability/otel-collector-config.yaml)
   Menerima trace/log OTLP pada port `4317` (gRPC) dan `4318` (HTTP). Aplikasi mengirim span secara otomatis ketika environment variable `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` diatur (`observability.NewDefaultSpanExporter`).
2. **Prometheus Metrics (`/metrics`)**: [`internal/observability/metrics.go`](../internal/observability/metrics.go) & [`docker/observability/prometheus.yml`](../docker/observability/prometheus.yml)
   Mengekspos metrik berformat Prometheus dengan **kardinalitas label terbatas** (`status`, `pool`, `state` — tidak pernah menggunakan `user_id`, `job_id`, atau teks `SQL` sebagai label):
   * `dbplatform_migration_jobs{status="waiting|running|success|failed|..."}`
   * `dbplatform_migration_job_retries_total`
   * `dbplatform_migration_batches_processed_total` & `dbplatform_migration_rows_processed_total`
   * `dbplatform_migration_queue_wait_seconds_sum` / `_count` & `dbplatform_migration_lock_wait_seconds_sum` / `_count`
   * `dbplatform_migration_duration_seconds_sum{status="..."}` / `_count`
   * `dbplatform_db_pool_connections{pool="...",state="active|idle"}`
   * `dbplatform_db_pool_wait_total{pool="..."}` & `dbplatform_db_pool_wait_seconds_total{pool="..."}`
3. **Grafana Dashboard & Provisioning**:
   * Datasource provisioning: [`docker/observability/grafana/provisioning/datasources/datasources.yaml`](../docker/observability/grafana/provisioning/datasources/datasources.yaml)
   * Dashboard provider: [`docker/observability/grafana/provisioning/dashboards/dashboards.yaml`](../docker/observability/grafana/provisioning/dashboards/dashboards.yaml)
   * Dashboard JSON: [`docker/observability/grafana/dashboards/if3110-dbplatform.json`](../docker/observability/grafana/dashboards/if3110-dbplatform.json) (akses di `http://localhost:3000`, login `admin` / `admin`).

### B. Korelasi Log, Trace, dan Metrik
* Ketika grafik `dbplatform_migration_duration_seconds` atau `dbplatform_db_pool_wait_seconds_total` mengalami kenaikan pada jam tertentu di Grafana, operator memeriksa log JSON dan span pada rentang waktu yang sama.
* Log JSON, Span, dan record `audit_logs` dihubungkan oleh **`trace_id`** (`7e9a31c4d2b8401f9a0e6c2b1d4f8a10`) dan **`correlation_id`** yang identik dari request HTTP awal hingga eksekusi batch SQL di worker.

---

## 4. [BONUS] Stress Testing dengan `k6`

Script pengujian: [`docker/k6/stress_test.js`](../docker/k6/stress_test.js)

### Cara Menjalankan
```bash
k6 run -e BASE_URL=http://localhost:8080 \
       -e ADMIN_USER=admin \
       -e ADMIN_PASS=1234567891011121314 \
       -e DB_ID_A=1 -e DB_ID_B=2 \
       -e APPROVED_REQ_A=1 -e APPROVED_REQ_B=2 \
       docker/k6/stress_test.js
```

### Desain Beban & Threshold
* **5 Tahap Beban (Stages)**:
  1. **Baseline Normal (`0s–20s`, ramp ke `10 VUs`)**: Mengukur latensi normal pembacaan API dan pengiriman job.
  2. **Elevated Load (`20s–50s`, ramp ke `30 VUs`)**: Beban 3x di atas normal dengan pengiriman job paralel pada database yang sama (`DB_ID_A`) dan berbeda (`DB_ID_B`).
  3. **Peak Stress (`50s–80s`, ramp ke `60 VUs`)**: Menguji antrean lock per-database dan kejenuhan connection pool (`MaxOpenConns=10`).
  4. **Recovery (`80s–100s`, turun ke `10 VUs`)**: Mengamati pengosongan antrean job dan kembalinya latensi API ke baseline.
  5. **Cooldown (`100s–110s`, turun ke `0 VUs`)**.
* **Threshold**: `http_req_duration p(95) < 500ms`, `p(50) < 150ms`, dan `api_error_rate < 5%`.
* **Pemisahan Pengiriman vs Penyelesaian Job**: Respons `202 Accepted` hanya dicatat sebagai latensi pengiriman API; keberhasilan dan durasi penyelesaian job (`job_completion_duration_ms` & `jobs_completed_total`) baru dihitung setelah polling `GET /api/v2/jobs/{id}` mencapai status terminal `success`/`completed`.
