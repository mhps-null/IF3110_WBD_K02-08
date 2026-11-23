# Integrated Database Management Platform

> Tugas Besar IF3110 Web-Based Development — Milestone 1 & Milestone 2

<img width="1920" height="1080" alt="preview" src="./docs/preview.png" />

Platform manajemen database PostgreSQL terintegrasi berbasis **Go JSON API** dan **React + TypeScript Single-Page Application (SPA)** yang responsif (desktop & mobile) dengan kontrol akses berbasis grup & **Group Leader**, eksplorasi skema dan data tabel (*read-only*), manajemen riwayat migrasi, **structured JSON logging & W3C distributed tracing (`internal/observability`)**, **platform audit log (`internal/audit`)**, serta **Static Application Security Testing (SAST) menggunakan GitHub CodeQL**.

---

## Fitur Utama

- **Single-Page Application (SPA) Responsif (`frontend/`)**: Seluruh alur autentikasi, dashboard, administrasi (users, groups, group leaders, database connections, access matrix), database schema/data viewer, audit log viewer, serta shell integrasi migration/approval dan job monitoring diimplementasikan menggunakan **React 18 + TypeScript + Vite + Tailwind CSS** yang responsif di desktop maupun perangkat mobile.
- **Autentikasi, Session, & CSRF JSON API (`internal/auth`, `internal/session`, `internal/web`)**: Hash password aman (**PBKDF2-SHA256** 600.000 iterasi), opaque session identifier acak (CSPRNG 32-byte, disimpan sebagai SHA-256 hash di database) melalui cookie `HttpOnly; SameSite=Lax` (`Secure` di produksi), rotasi session berkala dan saat `privilege_version` berubah, grace period, idle timeout (30 menit), absolute expiration (8 jam), pencabutan sesi instan, serta proteksi CSRF (`HttpOnly; SameSite=Strict` cookie + header `X-CSRF-Token`).
- **Access Control Berbasis Grup & Group Leader (`internal/access`, `internal/admin`)**:
  - Pemisahan tegas antara **Platform Admin** (administrasi platform) dan hak akses database per grup (`View Only` vs `View + Edit`) dengan resolusi deterministik *Max Privilege* untuk anggota multi-grup.
  - Pengelolaan **Group Leader** (`migrations_meta/004_group_leader.sql`): validasi leader harus anggota aktif grup, dukungan satu user memimpin beberapa grup sekaligus, auto-clear leader bila anggota dikeluarkan, serta endpoint inspeksi approver (`GET /api/databases/{id}/access-groups`) yang menandai grup tanpa leader sebagai `pending_leader_assignment` (tetap *Pending*, bukan ditolak).
- **Database Management, Pool Telemetry, & Read-Only Viewer (`internal/connections`, `internal/viewer`, `internal/dbdriver`)**:
  - Enkripsi password koneksi database target menggunakan **AES-256-GCM**, connection pooling dengan health check, serta eksposur statistik pool live (`contract.PoolStatsProvider` / `GET /api/admin/connections/pool-stats`) yang terhubung ke `observability.MetricsRecorder`.
  - Introspeksi skema lengkap (tabel, kolom dengan modifier presisi, constraint PK/FK/UK/CHECK, index) dari katalog sistem PostgreSQL dan pembacaan data tabel terpaginasi di dalam transaksi `READ ONLY` dengan `SET LOCAL statement_timeout = 5000`.
- **Activity/SQL Structured Logging, Distributed Tracing, Audit Log, & Dataset Uji Besar (`internal/observability`, `internal/audit`)**:
  - Structured log JSON berbasis `log/slog` ke `stdout` dengan redaction otomatis (`RedactAttr`) untuk password, DSN, session ID, dan token, serta sanitasi anti-*log injection* (`SanitizeLogString`).
  - Pencatatan setiap aktivitas penting dan query SQL (termasuk pembacaan skema & data read-only pada viewer: `sql.start`, `sql.result`, `sql.commit`, `sql.rollback`, `migration.attempt`).
  - Propagasi W3C `traceparent` & `X-Correlation-ID` (`InjectTraceContext` / `ExtractTraceContext`) beserta korelasi `trace_id` dan `span_id` pada log dan audit event.
  - Penyimpanan dan pencarian jejak audit platform (`migrations_meta/003_audit_logs.sql`, `GET /api/audit-logs`) serta seed dataset pengujian batch berukuran besar (**500.000 baris** pada `docker/postgres-init/04-init-large-batch-dataset.sql`).
- **[BONUS M2] Security — SAST dengan GitHub CodeQL (`.github/workflows/codeql.yml`, `docs/codeql-sast.md`)**: Analisis statis otomatis untuk bahasa **Go** dan **JavaScript/TypeScript** (`+security-extended`). Scan awal (`1fb9fad`) mendeteksi 4 temuan (`2 High go/sql-injection`, `2 Medium go/log-injection`), seluruhnya telah diperbaiki pada commit `3b0bc21` dan terverifikasi **`0 Open · 4 Closed`** pada scan ulang.
- **[Fondasi & Bonus M1 yang Dipertahankan]**: Transactional migration engine dengan PostgreSQL advisory lock, dukungan driver MySQL/MariaDB, Schema Diff & DDL generation (`internal/schemadiff`), serta Query Execution Plan Analysis (`EXPLAIN` / `EXPLAIN ANALYZE` di `internal/explain`).

---

## Prasyarat

- **Go** (versi 1.24+ sesuai `go.mod`).
- **Node.js** (versi 20+) & **npm** (untuk membangun dan menguji frontend SPA di `frontend/`).
- **PostgreSQL 16** (untuk database metadata platform dan database target).
- **Docker & Docker Compose** (opsional, untuk menjalankan container database target beserta seed dataset 500.000 baris).

---

## 1. Konfigurasi Lingkungan

Salin template environment variables:

```bash
cp .env.example .env
```

Edit file `.env` dan lengkapi variabel berikut:

```dotenv
APP_ENV=development
ADDR=:8080
DATABASE_URL=postgres://user:password@localhost:5432/if3110_wbd_k02_08?sslmode=disable
TEST_DATABASE_URL=postgres://user:password@localhost:5432/if3110_wbd_k02_08_test?sslmode=disable
SESSION_COOKIE_SECURE=false
SPA_DIST_DIR=frontend/dist

# Hasilkan nilai acak dengan openssl
SESSION_SECRET=$(openssl rand -hex 32)
DB_ENCRYPTION_KEY=$(openssl rand -base64 32)

SEED_ADMIN_USERNAME=admin
SEED_ADMIN_PASSWORD=1234567891011121314
```

> **Catatan Keamanan**: Nilai `DB_ENCRYPTION_KEY` wajib dijaga konsistensinya karena digunakan untuk mengenkripsi dan mendekripsi password koneksi database target. Pada lingkungan produksi, aktifkan `APP_ENV=production` dan `SESSION_COOKIE_SECURE=true`.

Muat environment variables ke shell aktif:

```bash
set -a
source .env
set +a
```

---

## 2. Migrasi Database Metadata

Terapkan seluruh skema metadata aplikasi secara berurutan ke database PostgreSQL:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/003_audit_logs.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/004_group_leader.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/005_migration_lifecycle.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/006_lifecycle_execution_runs.sql
```

---

## 3. Build Frontend SPA, Seed Platform Admin, & Menjalankan Aplikasi

Bangun bundle produksi SPA React + TypeScript (`frontend/dist/` disajikan langsung oleh server Go), jalankan seed Platform Admin awal, lalu jalankan HTTP server:

```bash
cd frontend && npm install && npm run build && cd ..
set -a && source .env && set +a
go run ./cmd/seed
go run ./cmd/server
```

Buka browser di **`http://localhost:8080`** dan login menggunakan kredensial Platform Admin.

Untuk mode *development* frontend dengan *hot-reload* (Vite mem-proxy `/api/*` dan `/healthz` ke `http://localhost:8080` dalam origin yang sama):

```bash
cd frontend && npm run dev
# Buka http://localhost:5173 (pastikan Go server berjalan di :8080)
```

---

## 4. Database Testing Cepat & Dataset Besar 500.000 Baris dengan Docker (Opsional)

Tersedia lingkungan Docker mandiri yang menyediakan database target PostgreSQL (termasuk seed `large_migration_records` **500.000 baris** pada `docker/postgres-init/04-init-large-batch-dataset.sql`), MySQL, dan MariaDB:

```bash
# 1. Jalankan container database testing (Port Postgres: 5433, MySQL: 3307, MariaDB: 3308)
./docker/start-test-dbs.sh

# 2. Otomatis daftarkan koneksi via JSON API dan aktifkan akses ke web platform
set -a && source .env && set +a
./docker/register-connections.sh
```

Setelah dijalankan, buka **`http://localhost:8080/databases`** untuk langsung menguji database viewer, struktur skema, data terpaginasi, dan riwayat migrasi.

Untuk menghentikan container:
```bash
./docker/stop-test-dbs.sh
```

Panduan skenario pengujian manual tersedia di [`docker/README_DOCKER_TESTING.md`](docker/README_DOCKER_TESTING.md) dan dokumentasi benchmark dataset besar tersedia di [`docs/m2-observability-and-dataset.md`](docs/m2-observability-and-dataset.md).

---

## 5. Peta JSON API & Rute SPA

### A. Endpoint JSON API Backend (`/api/*`)

Seluruh endpoint mutasi (`POST`, `PUT`, `PATCH`, `DELETE`) mewajibkan cookie CSRF `HttpOnly` dan header `X-CSRF-Token`.

| Method | Endpoint | Guard Minimal | Keterangan |
|---|---|---|---|
| `GET` | `/healthz` | Publik | Health check endpoint |
| `GET` | `/api/auth/csrf` | Publik | Mengambil CSRF token untuk sesi browser aktif |
| `POST` | `/api/auth/login` | Publik + CSRF | Autentikasi JSON & pembuatan cookie session `HttpOnly` |
| `POST` | `/api/auth/logout` | Login + CSRF | Pencabutan session di backend & penghapusan cookie |
| `GET` | `/api/auth/me` | Login | Profil user aktif, daftar group + status leader, database yang dapat diakses |
| `GET` | `/api/databases` | Login | Daftar database target yang dapat diakses user beserta level efektif |
| `GET` | `/api/databases/{id}/schema` | View Only | Introspeksi skema, tabel, kolom, constraint, dan index (tercatat di SQL log) |
| `GET` | `/api/databases/{id}/tables/{table}` | View Only | Detail struktur kolom, constraint, dan index tabel |
| `GET` | `/api/databases/{id}/tables/{table}/data` | View Only | Data baris tabel terpaginasi dalam transaksi `READ ONLY` + statement timeout |
| `GET` | `/api/databases/{id}/access-groups` | View Only / Admin | Daftar seluruh group yang memiliki akses ke DB beserta leader & status kelengkapan approver |
| `GET` | `/api/databases/{id}/migrations` | View Only | Daftar riwayat migrasi pada database target |
| `GET` | `/api/databases/{id}/migrations/{mid}` | View Only | Detail migrasi dan diagnostik error |
| `GET`, `POST` | `/api/admin/users` | Platform Admin | Daftar seluruh user & pembuatan akun user baru |
| `POST` | `/api/admin/users/{id}/{activate\|deactivate}` | Platform Admin | Aktivasi/deaktivasi user (mencabut seluruh session aktif user) |
| `POST` | `/api/admin/users/{id}/reset-password` | Platform Admin | Reset password user & pencabutan seluruh session aktif |
| `POST` | `/api/admin/users/{id}/toggle-admin` | Platform Admin | Pemberian/pencabutan peran Platform Admin & rotasi privilege |
| `GET`, `POST` | `/api/admin/groups` | Platform Admin | Daftar group (beserta info leader & jumlah anggota) dan pembuatan group |
| `GET`, `PUT`, `DELETE` | `/api/admin/groups/{id}` | Platform Admin | Detail group, pembaruan nama/deskripsi, dan penghapusan group |
| `POST`, `DELETE` | `/api/admin/groups/{id}/members[/{uid}]` | Platform Admin | Penambahan & pengeluaran anggota group (auto-clear leader bila leader dikeluarkan) |
| `PUT` | `/api/admin/groups/{id}/leader` | Platform Admin | Penetapan, penggantian, atau pengosongan Group Leader (wajib anggota aktif) |
| `GET`, `PUT` | `/api/admin/access` | Platform Admin | Matriks pemberian hak akses group ke database (`View Only` / `View + Edit` / `None`) |
| `GET`, `POST` | `/api/admin/connections` | Platform Admin | Daftar & pembuatan profil koneksi database target (password terenkripsi AES-256-GCM) |
| `GET`, `PUT`, `DELETE` | `/api/admin/connections/{id}` | Platform Admin | Detail, pembaruan, & penghapusan koneksi (ditolak 409 jika sudah punya riwayat migrasi) |
| `POST` | `/api/admin/connections/test` | Platform Admin | Uji konektivitas langsung ke database target |
| `GET` | `/api/admin/connections/pool-stats` | Platform Admin | Statistik live connection pool (`OpenConns`, `InUse`, `Idle`, `WaitCount`, `WaitDuration`) |
| `GET` | `/api/audit-logs` | Platform Admin | Pencarian & paginasi jejak audit platform (`actor`, `event`, `database`, `status`) |

### B. Rute Halaman SPA (`frontend/`)

| Path SPA | Akses | Halaman |
|---|---|---|
| `/login` | Publik | Halaman login SPA |
| `/` | Login | Dashboard ringkasan database, keanggotaan group, dan peran leader |
| `/databases` | Login | Pemilihan database target & ringkasan hak akses efektif |
| `/databases/:id/schema` | View Only | Explorer skema, kolom, constraint, index, data tabel (read-only), & status approver group |
| `/databases/:id/migrations`, `/migrations` | View Only | Riwayat migrasi & shell integrasi Migration Editor / Approval / History (Orang 3) |
| `/approvals` | Login | Review Inbox & ringkasan group yang dipimpin user (shell integrasi Orang 3) |
| `/jobs` | Login | Job Monitoring & Batch Progress (shell integrasi Orang 4) |
| `/admin/connections` | Platform Admin | Manajemen koneksi database, test koneksi, & monitor connection pool |
| `/admin/users` | Platform Admin | Manajemen akun user, status aktif, reset password, & hak admin |
| `/admin/groups`, `/admin/groups/:id` | Platform Admin | Manajemen group, anggota, dan penetapan Group Leader |
| `/admin/access` | Platform Admin | Matriks hak akses group ↔ database & indikator kelengkapan leader |
| `/audit-logs` | Platform Admin | Penelusuran & filter platform audit logs (terintegrasi dengan modul Orang 2) |

---

## 6. Pengujian Otomatis (Automated Testing)

### A. Frontend SPA Tests & Build Check
Jalankan pemeriksaan tipe TypeScript, unit/smoke test komponen SPA (Vitest + Testing Library), dan build produksi Vite:

```bash
cd frontend
npm run lint
npm test
npm run build
cd ..
```

### B. Backend Unit Tests & Static Check
Jalankan pemeriksaan statis dan seluruh unit test Go secara terisolasi dengan *race detector*:

```bash
go vet ./...
go test -race -count=1 ./...
```

### C. PostgreSQL Integration Tests
Siapkan database pengujian terpisah dan jalankan seluruh suite integrasi:

```bash
set -a && source .env && set +a

# Terapkan seluruh skema metadata pada database pengujian
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/003_audit_logs.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/004_group_leader.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/005_migration_lifecycle.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/006_lifecycle_execution_runs.sql

# Jalankan seluruh test integrasi
TEST_METADATA_DSN="$TEST_DATABASE_URL" go test -tags=integration -count=1 ./...
```

---

## 7. Dokumentasi Teknis & Keamanan

- **Arsitektur SPA, JSON API, & Group Leader (Orang 1):** [`docs/m2-p1-spa-and-auth.md`](docs/m2-p1-spa-and-auth.md)
- **Observability, Structured Logging/Tracing, Audit Log, & Dataset 500k Row (Orang 2):** [`docs/m2-observability-and-dataset.md`](docs/m2-observability-and-dataset.md)
- **[BONUS] SAST dengan GitHub CodeQL (Temuan, Perbaikan, & Rescan):** [`docs/codeql-sast.md`](docs/codeql-sast.md)
- **Dokumen Desain Fondasi M1:** [`docs/auth-session.md`](docs/auth-session.md), [`docs/access-control.md`](docs/access-control.md), [`docs/db-management.md`](docs/db-management.md), [`docs/migration.md`](docs/migration.md), [`docs/schema-diff.md`](docs/schema-diff.md), [`docs/p1-integration-notes.md`](docs/p1-integration-notes.md)

---

## 8. Struktur Repositori

```text
├── .github/
│   └── workflows/
│       └── codeql.yml        # Workflow SAST GitHub CodeQL (Go & JavaScript/TypeScript)
├── cmd/
│   ├── seed/                 # Seeding Platform Admin awal
│   └── server/               # Composition root HTTP server (JSON API + SPA static handler)
├── docker/                   # Docker Compose database target, seed 500k row, & skrip helper
├── docs/                     # Dokumentasi arsitektur M1/M2, observability, & hasil CodeQL SAST
├── frontend/                 # Aplikasi SPA React 18 + TypeScript + Vite + Tailwind CSS
│   ├── src/
│   │   ├── api/              # Fetch client dengan manajemen CSRF otomatis & modul endpoint
│   │   ├── components/       # AppShell responsif, ProtectedRoute, ConfirmDialog, shared UI states
│   │   ├── context/          # AuthContext & ToastContext
│   │   ├── pages/            # Halaman Login, Dashboard, Viewer, Admin, Audit, & shell P3/P4
│   │   └── types/            # Kontrak tipe data TypeScript untuk JSON API
│   ├── package.json
│   ├── vite.config.ts
│   └── vitest.config.ts
├── internal/
│   ├── access/               # Matriks izin, guards, effective level, & GroupAccessInspector
│   ├── admin/                # Store & JSON API manajemen user, group, leader, dan matriks akses
│   ├── audit/                # Store PostgreSQL/in-memory & JSON API audit log (GET /api/audit-logs)
│   ├── auth/                 # Autentikasi, session middleware, serta JSON API login/logout/me/csrf
│   ├── config/               # Validasi konfigurasi environment variables
│   ├── connections/          # Koneksi target AES-256-GCM, PoolManager + PoolStats, & JSON API
│   ├── contract/             # Antarmuka, tipe data bersama, dan principal context
│   ├── dbdriver/             # Driver Postgres & MySQL/MariaDB (introspeksi katalog aman, rows, exec)
│   ├── explain/              # Parser execution plan EXPLAIN / ANALYZE
│   ├── lifecycle/            # Migration revisions, approvals, execution runs, & attempts
│   ├── migration/            # Repository, service eksekusi transaksional, & read JSON API
│   ├── observability/        # Structured JSON logger (slog + redaction), W3C tracer, & metrics recorder
│   ├── password/             # Hashing & verifikasi password PBKDF2-SHA256
│   ├── schemadiff/           # Komparasi snapshot skema & generator DDL
│   ├── session/              # Store session PostgreSQL, rotasi privilege_version, & grace period
│   ├── viewer/               # Handler & JSON API database viewer dengan structured SQL read-only logging
│   └── web/                  # Middleware CSRF (form & JSON header), security headers, & SPA handler
├── migrations_meta/          # SQL migration additive untuk database metadata platform (001 s.d. 006)
└── web/                      # Template & asset legacy M1 (dipertahankan untuk kompatibilitas unit test M1)
```
