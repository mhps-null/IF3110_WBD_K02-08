# Observability, Benchmark Dataset Besar (> 1 Detik), & Stress Testing (`k6`)

Dokumen ini merangkum hasil pengukuran aktual dataset pengujian besar (`> 1 detik`), format & contoh nyata structured JSON log serta distributed trace lintas proses, konfigurasi **Bonus Observability** (**OpenTelemetry Collector**, **Grafana Loki**, **Jaeger**, **Prometheus**, **Grafana**), dan hasil aktual **Bonus Stress Testing `k6`**.

---

## 1. Dataset Pengujian Besar (`UPDATE` Penuh > 1 Detik vs Mode Per Batch)

Berkas seed: [`docker/postgres-init/04-init-large-batch-dataset.sql`](../docker/postgres-init/04-init-large-batch-dataset.sql)

* **Lingkungan Pengujian**: Intel(R) Core(TM) i3-N305 (8 vCPU), 6,9 GiB RAM, Linux, container `postgres:16-alpine` (PostgreSQL 16.16)
* **Database Target**: `test_ecommerce_pg`
* **Tabel & Volume Data**: `large_migration_records` — **`500.000` baris** (`id` `1` s.d. `500000`, `PRIMARY KEY (id)` + index `idx_large_migration_records_status`)

### Skenario & Hasil Pengukuran Aktual

1. **Query `UPDATE` Penuh Tanpa Batching (`psql \timing on`)**:
   ```sql
   BEGIN;
   UPDATE large_migration_records
   SET status = 'bench_full',
       updated_at = clock_timestamp();
   ROLLBACK;
   ```
   * **Durasi Terukur**: **`4.706,80 ms` (`4,71 detik`)** — melampaui syarat spesifikasi `> 1.000 ms` (`1 detik`) serta menahan *row-level lock* pada seluruh 500.000 baris hingga transaksi selesai.

2. **Migration Per Batch Melalui Asynchronous Worker (`cmd/worker`) pada Dataset yang Sama**:
   ```sql
   -- SQL Up
   UPDATE large_migration_records SET status = 'migrated_m2', updated_at = now() WHERE id > $1 AND id <= $2
   -- SQL Down
   UPDATE large_migration_records SET status = 'pending', updated_at = now() WHERE id > $1 AND id <= $2
   ```
   * **Konfigurasi Job**: `mode = "batch"`, `batch_table = "public.large_migration_records"`, `batch_column = "id"`, `batch_size = 50.000` (`10` transaksi batch).

| Mode Eksekusi | Jumlah Row | Ukuran Batch | Jumlah Transaksi | Durasi Terukur Aktual | Perilaku Lock & Pemulihan |
|---|---:|---:|---:|---:|---|
| **`UPDATE` Penuh (Tanpa Batch)** | `500.000` | — | `1` | **`4.706,80 ms`** | Menahan lock seluruh 500.000 baris selama 4,71 detik; kegagalan di akhir me-rollback seluruh perubahan. |
| **`UPDATE` Per Batch (`cmd/worker`)** | `500.000` | `50.000` | `10` | **`4.611,96 ms`** (`~461 ms`/batch) | Setiap batch di-commit dalam transaksi tersendiri bersama checkpoint `_dbplatform.migration_ledger`; progres live (`batches_done`, `rows_done`) terpantau di SPA dan mendukung *resume* / kompensasi terbalik. |

---

## 2. Activity/SQL Structured Logging & OpenTelemetry Distributed Tracing

Diimplementasikan pada `internal/observability`:

* **Structured Log JSON (`log/slog` ke `stdout`)**: Mencatat waktu, `level`, `event`, `actor`, `database`, `status`, `correlation_id`, `trace_id`, dan `span_id` (tidak disimpan di database platform). Untuk seluruh operasi SQL (termasuk query *read-only* pada viewer, eksekusi normal, batch, kompensasi, retry, serta kegagalan), dicatat secara terpisah `sql.start`, `sql.result`, `sql.commit` / `sql.rollback`, dan `migration.attempt` lengkap dengan salinan `query`, `params`, `duration_ms`, `rows`, `sqlstate`, `migration_id`, `execution_id`, `version`, `job_id`, `attempt`, dan `batch`.
* **Redaction & Sanitasi Otomatis**: Seluruh atribut sensitif (`password`, `dsn`, `session_id`, `csrf_token`, `Authorization`, `Set-Cookie`, `secret`, `api_key`) disamarkan menjadi `[REDACTED]` pada log maupun atribut span (`RedactAttr`, `RedactString`), serta dibersihkan dari karakter kontrol `\r`/`\n` (`SanitizeLogString`).

### Contoh Aktual Korelasi Log JSON (Loki / `stdout`) & Span (Jaeger / `stderr`)
Pada eksekusi E2E dengan `trace_id = d90cba8d58fc7566b0ecf6f6ce5521bf`:

* **Rantai Span Lintas Proses (`if3110-api` → Queue → `if3110-worker`)**:
  `POST /api/v2/databases/15/requests/3/execute` (`if3110-api`) → `job.process` (`if3110-worker`) → `job.lock.acquire` → `job.claim_request` → `job.revalidate` → `migration.execute` → `sql.exec` → `sql.commit`.
* **Output Span (`ConsoleSpanExporter` / OTLP)**:
  ```json
  {"trace_id":"3954daf251b972ee12281fde8826735b","span_id":"872efe33a885f8fe","parent_span_id":"f86e2000bfbded3e","name":"migration.batch","service":"if3110-worker","duration_ms":115,"status":"ok","attributes":{"batch":1,"cursor_from":0,"cursor_to":20000}}
  ```
* **Output Structured JSON Log Terkorelasi (diambil langsung dari Grafana Loki melalui OTel Collector)**:
  ```json
  {"time":"2026-11-27T07:48:21.620655877+07:00","level":"INFO","msg":"sql.start","event":"sql.start","actor":"user:14","database":"pg_ecommerce_a_1795740498","status":"started","correlation_id":"d90cba8d58fc7566b0ecf6f6ce5521bf","trace_id":"d90cba8d58fc7566b0ecf6f6ce5521bf","span_id":"1ae8d6ee60a7e488","query":"DROP TABLE e2e_orders_ext2_1795740498","migration_id":2,"version":3,"job_id":"3","attempt":1}
  ```

---

## 3. [BONUS] Observability: OTel Collector, Loki, Jaeger, Prometheus, & Grafana

### Komponen & Berkas Konfigurasi
1. **OpenTelemetry Collector**: [`docker/observability/otel-collector-config.yaml`](../docker/observability/otel-collector-config.yaml)
   Menerima telemetri OTLP pada port `4317` (gRPC) dan `4318` (HTTP) ketika `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` disetel pada API dan Worker, lalu meneruskan:
   * **Logs (`/v1/logs`)** → **Grafana Loki** (`http://loki:3100/otlp`, konfigurasi [`docker/observability/loki-config.yaml`](../docker/observability/loki-config.yaml)).
   * **Traces (`/v1/traces`)** → **Jaeger** (`jaeger:4317`, UI pada `http://localhost:16686`).
   * **Metrics (`/v1/metrics` & `/metrics`)** → diekspos langsung pada `http://localhost:8080/metrics` (API), `http://localhost:9091/metrics` (Worker), serta `otel-collector:8889` yang di-scrape oleh **Prometheus** ([`docker/observability/prometheus.yml`](../docker/observability/prometheus.yml)).
2. **Daftar Metrik Prometheus (Kardinalitas Label Terbatas)**:
   Hanya memakai label berbatas `status`, `pool`, dan `state` (**tidak pernah** menggunakan `user_id`, `job_id`, atau teks `SQL` sebagai label):
   * `dbplatform_migration_jobs{status="queued|retrying|running|compensating|uncertain|success|failed|partial|partially_reverted|reverted|cancelled|rejected"}` (gauge)
   * `dbplatform_migration_job_transitions_total{status="..."}` & `dbplatform_migration_job_retries_total` (counter)
   * `dbplatform_migration_batches_processed_total` & `dbplatform_migration_rows_processed_total` (counter)
   * `dbplatform_migration_queue_wait_seconds` & `dbplatform_migration_lock_wait_seconds` (histogram `_bucket`, `_sum`, `_count`)
   * `dbplatform_migration_duration_seconds{status="..."}` (histogram)
   * `dbplatform_db_pool_connections{pool="...",state="active|idle"}`, `dbplatform_db_pool_wait_total{pool="..."}`, & `dbplatform_db_pool_wait_seconds_total{pool="..."}` (gauge/counter)
3. **Grafana Dashboard & Provisioning Otomatis (`http://localhost:3000`, `admin`/`admin`)**:
   * Datasource provisioning (`Prometheus`, `Loki` dengan *derived field* `TraceID` → Jaeger, serta `Jaeger` dengan *Traces-to-Logs* → Loki): [`docker/observability/grafana/provisioning/datasources/datasources.yaml`](../docker/observability/grafana/provisioning/datasources/datasources.yaml)
   * Dashboard provider: [`docker/observability/grafana/provisioning/dashboards/dashboards.yaml`](../docker/observability/grafana/provisioning/dashboards/dashboards.yaml)
   * Dashboard JSON 7 panel: [`docker/observability/grafana/dashboards/if3110-dbplatform.json`](../docker/observability/grafana/dashboards/if3110-dbplatform.json)

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

### Hasil Pengukuran Aktual & Analisis per Tingkat Beban
* **Total Request & Throughput**: **`32.835` HTTP requests** (**`289,48 req/s`**), `6.438` iterasi (`56,76 iter/s`), checks lulus **`100,00%`** (`12.876 / 12.876`).
* **API Response Time Keseluruhan**: **`p50 = 7,51 ms`** (`✓ < 150 ms`), **`p95 = 40,58 ms`** (`http_req_duration p95 = 41,23 ms`, `✓ < 500 ms`), `avg = 14,12 ms`.
* **API Error Rate**: **`0,00%`** (`0 / 32.835` gagal, `✓ < 5%`).
* **Queue Wait (`job_queue_wait_ms`)**: `p50 = 95,50 ms`, `p95 = 219,39 ms`, `avg = 103,57 ms`, `max = 287,00 ms`.
* **Job Completion (`job_completion_duration_ms` & `jobs_completed_total`)**: **`90` migration job Approved selesai `success`** (**`0,793 job/detik`**; respons `202 Accepted` tidak dihitung sebagai keberhasilan migrasi), dengan durasi penyelesaian `p50 = 137,50 ms`, `p95 = 252,30 ms`, `avg = 142,12 ms`.

| Tingkat Beban | Durasi | Target VU | `p50` API | `p95` API | `avg` API | Analisis Perilaku Sistem |
|---|---:|---:|---:|---:|---:|---|
| **1. Baseline Normal** | `20s` | `0 → 10 VUs` | `5,17 ms` | `19,96 ms` | `7,99 ms` | Latensi sangat rendah dan stabil; koneksi pool metadata (`MaxOpenConns=25`) dan target DB memiliki cadangan koneksi idle tanpa antrean. |
| **2. Elevated Load** | `30s` | `10 → 30 VUs` | `6,66 ms` | `28,81 ms` | `10,52 ms` | Beban 3× baseline; job pada `DB_ID_A` dan `DB_ID_B` berjalan paralel di `cmd/worker`, sementara *paired burst* pada `DB_ID_A` diserialisasi aman oleh advisory lock per-DB (`queue_wait ~95 ms`). |
| **3. Peak Stress** | `30s` | `30 → 60 VUs` | `8,15 ms` | `40,97 ms` | `14,12 ms` | Beban puncak 6× baseline (~290 req/s); `p95` hanya naik ke `40,97 ms` (jauh di bawah batas `500 ms`) karena API mempublikasikan job secara non-blocking ke antrean tanpa menjalankan DDL/DML target di HTTP handler (`error_rate = 0,00%`). |
| **4–5. Recovery & Cooldown** | `30s` | `60 → 10 → 0 VUs` | `7,34 ms` | `57,21 ms` | `16,59 ms` | Begitu VU diturunkan ke `10` lalu `0`, median (`p50`) segera pulih turun dari `8,15 ms` ke `7,34 ms`; sisa antrean dikuras habis hingga `queued = 0` dan seluruh koneksi kembali `idle` tanpa deadlock maupun kebocoran resource. |
