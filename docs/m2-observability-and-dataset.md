# Milestone 2 — Observability, Audit Log, Dataset Besar, & Stress Testing (k6)

Dokumen ini mencakup hasil pengukuran aktual dataset pengujian besar (`> 1 detik`), contoh nyata output structured JSON log & OpenTelemetry span lintas proses, arsitektur Bonus Observability (**OpenTelemetry Collector**, **Grafana Loki**, **Jaeger**, **Prometheus**, **Grafana**), serta hasil aktual Stress Testing **`k6`**.

---

## 1. Dataset Pengujian Besar (`UPDATE` Penuh > 1 Detik)

Berkas seed: [`docker/postgres-init/04-init-large-batch-dataset.sql`](../docker/postgres-init/04-init-large-batch-dataset.sql)

* **Lingkungan Pengujian**: Intel(R) Core(TM) i3-N305 (8 vCPU), 6,9 GiB RAM, Linux, container `postgres:16-alpine` (PostgreSQL 16.16)
* **Database Target**: `test_ecommerce_pg`
* **Tabel**: `large_migration_records`
* **Jumlah Row**: **`500.000` baris** (`id` `1` s.d. `500000`, primary key `id` + index `idx_large_migration_records_status`)

### Skenario & Hasil Pengukuran Aktual

1. **Query `UPDATE` Penuh Tanpa Batching (`psql \timing on`)**:
   ```sql
   BEGIN;
   UPDATE large_migration_records
   SET status = 'bench_full',
       updated_at = clock_timestamp();
   ROLLBACK;
   ```
   * **Durasi Terukur**: **`4.706,800 ms` (`4,71 detik`)** — melebihi batas minimum spesifikasi `> 1.000 ms` (`1 detik`), serta menahan *row-level lock* atas 500.000 baris sekaligus hingga satu transaksi selesai.

2. **Migration Per Batch Melalui Asynchronous Worker (`cmd/worker`) pada Dataset yang Sama**:
   ```sql
   -- SQL Up
   UPDATE large_migration_records SET status = 'migrated_m2', updated_at = now() WHERE id > $1 AND id <= $2
   -- SQL Down
   UPDATE large_migration_records SET status = 'pending', updated_at = now() WHERE id > $1 AND id <= $2
   ```
   * **Konfigurasi Job**: `mode = "batch"`, `batch_table = "public.large_migration_records"`, `batch_column = "id"`, `batch_size = 50.000` (`10` batch).

| Mode Eksekusi | Jumlah Row | Ukuran Batch | Jumlah Transaksi | Durasi Terukur Aktual | Perilaku Lock & Pemulihan |
|---|---:|---:|---:|---:|---|
| **`UPDATE` Penuh (Tanpa Batch)** | `500.000` | — | `1` | **`4.706,80 ms`** | Menahan lock seluruh 500.000 baris selama 4,71 detik; gagal di tengah me-rollback semuanya. |
| **`UPDATE` Per Batch (`cmd/worker`)** | `500.000` | `50.000` | `10` | **`4.611,96 ms`** (`~461 ms`/batch) | Setiap batch di-commit dalam transaksi terpisah bersama checkpoint `_dbplatform.migration_ledger`; progres live (`batches_done`, `rows_done`) terlihat di SPA dan dapat di-*resume* / dikompensasi terbalik bila gagal di tengah. |

---

## 2. Contoh Aktual Structured Log JSON, Distributed Trace, & Korelasi

Output dihasilkan oleh `observability.NewDefaultLogger` (`log/slog` JSON ke `stdout` + OTLP `/v1/logs`) dan `observability.NewDefaultSpanExporter` (`stderr` + OTLP `/v1/traces`) pada alur `if3110-api → queue → if3110-worker → lock → batch/query`.

### A. Contoh Nyata Korelasi Distributed Trace (Jaeger) & Log Terpusat (Loki)
Pada pengujian E2E dengan `trace_id = d90cba8d58fc7566b0ecf6f6ce5521bf`:

* **8 Span Terhubung dalam Satu Trace Lintas Service (`if3110-api` & `if3110-worker`) di Jaeger**:
  `POST /api/v2/databases/15/requests/3/execute` (`if3110-api`) → `job.process` (`if3110-worker`) → `job.lock.acquire` → `job.claim_request` → `job.revalidate` → `migration.execute` → `sql.exec` → `sql.commit`.
* **Log JSON Terkorelasi yang Diambil dari Grafana Loki (`{service_name=~"if3110-.*"} |= "d90cba8d58fc7566b0ecf6f6ce5521bf"`)**:
  ```json
  {"time":"2026-11-27T07:48:21.620655877+07:00","level":"INFO","msg":"sql.start","event":"sql.start","actor":"user:14","database":"pg_ecommerce_a_1795740498","status":"started","correlation_id":"d90cba8d58fc7566b0ecf6f6ce5521bf","trace_id":"d90cba8d58fc7566b0ecf6f6ce5521bf","span_id":"1ae8d6ee60a7e488","query":"DROP TABLE e2e_orders_ext2_1795740498","migration_id":2,"version":3,"job_id":"3","attempt":1}
  ```
* **Redaction Data Sensitif**: Seluruh field sensitif (`password`, `dsn`, `session_id`, `csrf_token`, `Authorization`, `Set-Cookie`) otomatis disamarkan menjadi `[REDACTED]` sebelum ditulis ke `stdout` maupun dikirim melalui OTLP.

---

## 3. [BONUS] Observability: OTel Collector, Loki, Jaeger, Prometheus, & Grafana

### Komponen & Berkas Konfigurasi
1. **OpenTelemetry Collector**: [`docker/observability/otel-collector-config.yaml`](../docker/observability/otel-collector-config.yaml)
   Menerima OTLP pada port `4317` (gRPC) dan `4318` (HTTP), lalu meneruskan:
   * **Logs** (`otlphttp/loki`) ke **Grafana Loki** (`http://loki:3100/otlp`, konfigurasi [`docker/observability/loki-config.yaml`](../docker/observability/loki-config.yaml)).
   * **Traces** (`otlp/jaeger`) ke **Jaeger** (`jaeger:4317`, UI di `http://localhost:16686`).
   * **Metrics** (`prometheus`) pada `otel-collector:8889` di samping endpoint langsung `http://localhost:8080/metrics` dan `http://localhost:9091/metrics`.
2. **Prometheus Metrics**: [`internal/observability/metrics.go`](../internal/observability/metrics.go), [`internal/jobs/metrics.go`](../internal/jobs/metrics.go), & [`docker/observability/prometheus.yml`](../docker/observability/prometheus.yml)
   Seluruh label dibatasi pada himpunan nilai berbatas (`status`, `pool`, `state`); tidak ada `user_id`, `job_id`, atau teks `SQL` pada label:
   * `dbplatform_migration_jobs{status="queued|retrying|running|compensating|uncertain|success|failed|partial|partially_reverted|reverted|cancelled|rejected"}`
   * `dbplatform_migration_job_transitions_total{status="..."}` & `dbplatform_migration_job_retries_total`
   * `dbplatform_migration_batches_processed_total` & `dbplatform_migration_rows_processed_total`
   * `dbplatform_migration_queue_wait_seconds` & `dbplatform_migration_lock_wait_seconds` (histogram `_bucket`, `_sum`, `_count`)
   * `dbplatform_migration_duration_seconds{status="..."}` (histogram)
   * `dbplatform_db_pool_connections{pool="...",state="active|idle"}`, `dbplatform_db_pool_wait_total{pool="..."}`, & `dbplatform_db_pool_wait_seconds_total{pool="..."}`
3. **Grafana Provisioning & Dashboard**:
   * Datasources (`Prometheus`, `Loki` dengan *derived field* `TraceID` → Jaeger, serta `Jaeger` dengan *Traces-to-Logs* → Loki): [`docker/observability/grafana/provisioning/datasources/datasources.yaml`](../docker/observability/grafana/provisioning/datasources/datasources.yaml)
   * Dashboard provider: [`docker/observability/grafana/provisioning/dashboards/dashboards.yaml`](../docker/observability/grafana/provisioning/dashboards/dashboards.yaml)
   * Dashboard 7 Panel JSON: [`docker/observability/grafana/dashboards/if3110-dbplatform.json`](../docker/observability/grafana/dashboards/if3110-dbplatform.json)

---

## 4. [BONUS] Stress Testing dengan `k6`

Script pengujian: [`docker/k6/stress_test.js`](../docker/k6/stress_test.js)

### Cara Menjalankan
```bash
docker run --rm -i --network=host \
  -e BASE_URL=http://localhost:8080 \
  -e ADMIN_USER=admin \
  -e ADMIN_PASS=1234567891011121314 \
  -e DB_ID_A=1 -e DB_ID_B=2 \
  grafana/k6:0.54.0 run - < docker/k6/stress_test.js
```

### Ringkasan Hasil Aktual (`32.835` Request, `90` Migration Job Approved Selesai)
* **Request Throughput**: `289,48 req/s` (`32.835` HTTP requests dalam `113,4s`)
* **API Response Time**: `p50 = 7,51 ms` (`✓ < 150 ms`), `p95 = 40,58 ms` (`✓ < 500 ms`), `avg = 14,12 ms`
* **API Error Rate**: `0,00%` (`0 / 32.835` gagal, `✓ < 5%`)
* **Queue Wait (`job_queue_wait_ms`)**: `p50 = 95,50 ms`, `p95 = 219,39 ms`, `avg = 103,57 ms`
* **Job Completion (`job_completion_duration_ms` & `jobs_completed_total`)**: `90` job selesai `success` (`0,793 job/s`), `p50 = 137,50 ms`, `p95 = 252,30 ms`, `avg = 142,12 ms`
* **Per Tingkat Beban**:
  1. **Stage 1 — Baseline (`0 → 10 VUs`, `20s`)**: `p50 = 5,17 ms`, `p95 = 19,96 ms`
  2. **Stage 2 — Elevated (`10 → 30 VUs`, `30s`)**: `p50 = 6,66 ms`, `p95 = 28,81 ms`
  3. **Stage 3 — Peak Stress (`30 → 60 VUs`, `30s`)**: `p50 = 8,15 ms`, `p95 = 40,97 ms`
  4. **Stage 4–5 — Recovery & Cooldown (`60 → 10 → 0 VUs`, `30s`)**: `p50 = 7,34 ms`, `p95 = 57,21 ms` (median segera pulih ke level baseline seiring antrean terkuras ke nol).
