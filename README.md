# Integrated Database Management Platform

> Tugas Besar IF3110 Web-Based Development Milestone 1

<p align="center">
  <img src="preview.png" width="60%"/>
</p>

Platform manajemen database terintegrasi berbasis **Go** (*Server-Side Rendering non-SPA*) dengan kontrol akses berbasis grup, database viewer, eksekusi migrasi transaksional linier, serta fitur analisis lanjutan (Multi-DBMS MySQL/MariaDB, Schema Diff, dan Query Execution Plan Analysis).

---

## Fitur Utama

- **Autentikasi & Session**: Hash password aman (PBKDF2-SHA256 600.000 iterasi), proteksi CSRF pada seluruh mutasi, session identifier acak (CSPRNG 32-byte), rotasi session saat login dan perubahan hak akses, idle timeout (30 menit), absolute expiration (8 jam), serta grace period.
- **Access Control Berbasis Grup**: Pemisahan jelas antara Platform Admin (kelola platform) dengan hak akses database (`View Only` vs `View + Edit`). Resolusi deterministik *Max Privilege* untuk multi-grup dan pencabutan akses instan untuk user non-aktif.
- **Database Management & Viewer**: Enkripsi password koneksi menggunakan **AES-256-GCM**, connection pooling dengan health check, introspeksi skema lengkap (tabel, kolom dengan modifier presisi, constraint PK/FK/UK/CHECK, index), serta pembacaan data tabel terpaginasi di dalam transaksi *read-only* dengan *statement timeout*.
- **Migration Engine**: Riwayat migrasi linier per database yang dijamin dengan *PostgreSQL advisory lock*, commit atomik jika sukses dan rollback otomatis jika gagal, immutability riwayat yang sudah dieksekusi, serta pencatatan audit trail lengkap.
- **[BONUS] Multi-DBMS**: Dukungan penuh untuk PostgreSQL, MySQL (8.0+), dan MariaDB (10.3+).
- **[BONUS] Schema Diff & DDL Generation**: Komparasi dua kondisi skema database, visual diff, pembuatan kandidat DDL berurutan topologis dengan penanda perubahan destruktif, dan prefill ke editor migrasi untuk ditinjau sebelum eksekusi.
- **[BONUS] Query Execution Plan Analysis**: Tampilan pohon hasil `EXPLAIN` dan `EXPLAIN ANALYZE` (scan type, index usage, join strategy, total cost, actual time, actual rows) dengan jaminan rollback transaksi pada statement DML.

---

## Prasyarat

- **Go** (versi 1.24+ sesuai `go.mod`).
- **PostgreSQL 16** (untuk metadata platform).
- **Docker & Docker Compose** (opsional, untuk menjalankan database target otomatis).

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

Terapkan skema metadata aplikasi ke database PostgreSQL:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
```

---

## 3. Seed Platform Admin & Menjalankan Aplikasi

Jalankan perintah seed untuk membuat akun Platform Admin awal, lalu jalankan HTTP server:

```bash
go run ./cmd/seed
go run ./cmd/server
```

Buka browser di **`http://localhost:8080`** dan login menggunakan kredensial admin yang telah dikonfigurasi.

---

## 4. Database Testing Cepat dengan Docker (Opsional)

Tersedia lingkungan Docker mandiri yang menyediakan database target PostgreSQL, MySQL, dan MariaDB dengan skema dan data terisi (*pre-seeded*):

```bash
# 1. Jalankan container database testing (Port Postgres: 5433, MySQL: 3307, MariaDB: 3308)
./docker/start-test-dbs.sh

# 2. Otomatis daftarkan koneksi dan aktifkan akses ke web platform
./docker/register-connections.sh
```

Setelah dijalankan, buka **`http://localhost:8080/databases`** untuk langsung menguji viewer, migrasi, perbandingan skema (Schema Diff), dan query plan (EXPLAIN).

Untuk menghentikan container:
```bash
./docker/stop-test-dbs.sh
```

Panduan skenario pengujian manual tersedia di [`docker/README_DOCKER_TESTING.md`](docker/README_DOCKER_TESTING.md).

---

## 5. Rute Utama Server

| Method | Route | Hak Akses Minimal | Keterangan |
|---|---|---|---|
| `GET` | `/healthz` | Publik | Health check endpoint |
| `GET`, `POST` | `/login` | Publik | Halaman dan proses autentikasi |
| `POST` | `/logout` | Login | Terminasi sesi dan pembatalan cookie |
| `GET` | `/` | Login | Beranda aplikasi |
| `GET` | `/databases` | Login | Daftar database yang dapat diakses pengguna |
| `GET` | `/databases/{id}/schema` | View Only | Introspeksi skema, tabel, constraint, dan index |
| `GET` | `/databases/{id}/tables/{table}` | View Only | Detail kolom dan constraint tabel |
| `GET` | `/databases/{id}/tables/{table}/data` | View Only | Data baris tabel (paginasi & sorting aman) |
| `GET`, `POST` | `/databases/{id}/schema-diff` | View Only | Komparasi skema dan pembuatan kandidat DDL |
| `GET` | `/databases/{id}/migrations` | View Only | Riwayat linier migrasi |
| `GET` | `/databases/{id}/migrations/{mid}` | View Only | Detail audit migrasi |
| `GET`, `POST` | `/databases/{id}/migrations/new` | View + Edit | Form dan pembuatan draft migrasi baru |
| `POST` | `/databases/{id}/migrations/{mid}` | View + Edit | Perubahan draft migrasi yang belum dieksekusi |
| `POST` | `/databases/{id}/migrations/{mid}/execute` | View + Edit | Eksekusi transaksional migrasi |
| `POST` | `/databases/{id}/migrations/{mid}/explain` | View + Edit | Analisis query plan (EXPLAIN & ANALYZE) |
| `GET`, `POST` | `/admin/connections` | Platform Admin | Kelola profil koneksi database target |
| `GET`, `POST` | `/admin/groups` | Platform Admin | Kelola kelompok pengguna dan anggota |
| `GET`, `POST` | `/admin/access` | Platform Admin | Matriks pemberian hak akses grup ke database |
| `GET`, `POST` | `/admin/users` | Platform Admin | Kelola akun pengguna, reset password, & status aktif |

---

## 6. Pengujian Otomatis (Automated Testing)

### Unit Tests & Static Check
Jalankan pemeriksaan kode dan unit test secara terisolasi dengan *race detector*:

```bash
go vet ./...
go test -race -count=1 ./...
```

### Integration Tests
Siapkan database pengujian terpisah dan jalankan suite integrasi:

```bash
# Terapkan skema pada database pengujian
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql

# Jalankan seluruh test integrasi (memerlukan TEST_METADATA_DSN)
TEST_METADATA_DSN="$TEST_DATABASE_URL" go test -tags=integration -count=1 ./...
```

---

## 7. Struktur Repositori

```text
├── cmd/
│   ├── seed/             # Seeding Platform Admin awal
│   └── server/           # Composition root dan server HTTP
├── docker/               # Lingkungan Docker database testing & skrip helper
├── docs/                 # Dokumentasi keputusan arsitektur & keamanan
├── internal/
│   ├── access/           # Matriks izin, guards, dan effective level checker
│   ├── admin/            # Manajemen user, group, koneksi, dan akses
│   ├── auth/             # Autentikasi, login/logout, dan middleware auth
│   ├── config/           # Validasi environment variables
│   ├── connections/      # Pengelolaan koneksi target & enkripsi AES-256-GCM
│   ├── contract/         # Antarmuka, tipe data bersama, dan contracts
│   ├── dbdriver/         # Driver Postgres & MySQL/MariaDB (introspeksi, query data, exec)
│   ├── explain/          # Parser execution plan EXPLAIN / ANALYZE
│   ├── migration/        # Repository, service eksekusi transaksional, & handler
│   ├── password/         # Hashing & verifikasi password PBKDF2-SHA256
│   ├── schemadiff/       # Komparasi snapshot skema & generator DDL
│   ├── session/          # Store session PostgreSQL, rotasi, & grace period
│   └── web/              # Renderer template, CSRF, flash message, & security headers
├── migrations_meta/      # SQL migration untuk database metadata platform
└── web/
    ├── static/           # Asset CSS dan JavaScript vanilla (non-SPA)
    └── templates/        # Template HTML Go (SSR)
```
