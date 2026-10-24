# IF3110 Milestone 1 — P1 Foundation, Authentication, and Session

Dokumentasi implementasi P1 untuk fondasi aplikasi Database Management Platform. Cakupan P1 meliputi konfigurasi aplikasi, seed Platform Admin, autentikasi, session berbasis PostgreSQL, proteksi CSRF, template HTML, flash message, logging aman, dan security headers.

## Prasyarat

- Go sesuai versi yang ditentukan di `go.mod`.
- PostgreSQL untuk metadata aplikasi.
- `psql` untuk menerapkan migration metadata.
- OpenSSL untuk menghasilkan secret acak (atau generator kriptografis lain yang sesuai).

## 1. Konfigurasi

Buat file konfigurasi lokal:

```bash
cp .env.example .env
```

Edit `.env` dan isi setidaknya:

- `DATABASE_URL`: DSN PostgreSQL metadata aplikasi.
- `SESSION_SECRET`: secret acak minimal 32 karakter.
- `SEED_ADMIN_USERNAME`: username Platform Admin awal.
- `SEED_ADMIN_PASSWORD`: password awal minimal 14 karakter dan bukan password placeholder.

Untuk lingkungan lokal melalui HTTP, `APP_ENV=development` dan `SESSION_COOKIE_SECURE=false` dapat digunakan. Dalam produksi, aplikasi mensyaratkan `APP_ENV=production` dan `SESSION_COOKIE_SECURE=true`, serta harus berjalan di belakang terminasi TLS yang benar.

Buat secret dengan:

```bash
openssl rand -hex 32
```

Masukkan hasilnya sebagai nilai `SESSION_SECRET` di `.env`. Jangan commit `.env` atau membagikan isi konfigurasi yang mengandung kredensial.

Muat environment variables ke shell:

```bash
set -a
source ./.env
set +a
```

## 2. Migration metadata

Untuk database metadata baru atau database yang belum memiliki skema, terapkan migration secara berurutan:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
```

Jangan menerapkan ulang migration awal pada database yang sudah memiliki skema tanpa memeriksa keadaan database terlebih dahulu. Migration `002` menambahkan snapshot versi privilege yang digunakan untuk validasi dan rotasi session.

Rincian lifecycle session dan kontrak integrasi privilege tersedia di `docs/auth-session.md` dan `docs/p1-integration-notes.md`.

## 3. Seed Platform Admin dan jalankan server

Pastikan `.env` sudah dimuat ke shell, database dapat diakses, dan migration sudah diterapkan. Kemudian jalankan:

```bash
go run ./cmd/seed
go run ./cmd/server
```

Seed membuat Platform Admin awal jika username belum ada. Jika username tersebut sudah ada, seed tidak mengubah password atau privilege akun yang sudah ada.

Server menggunakan alamat dari `ADDR`, dengan nilai default `:8080`. Buka `http://localhost:8080` untuk mengakses aplikasi lokal.

Endpoint dasar yang tersedia pada implementasi P1:

| Method | Route | Fungsi |
|---|---|---|
| `GET` | `/healthz` | Health check |
| `GET` | `/login` | Halaman login |
| `POST` | `/login` | Memproses login dengan CSRF |
| `GET` | `/` | Beranda dengan autentikasi |
| `POST` | `/logout` | Logout dengan autentikasi dan CSRF |

## 4. Verifikasi unit test dan build

Jalankan dari root repo:

```bash
go test ./...
go vet ./...
go build ./...
```

## 5. Integration test

Integration test autentikasi, session, dan CSRF tersedia melalui build tag `integration`.

Gunakan database PostgreSQL khusus pengujian melalui `TEST_DATABASE_URL`. **Jangan gunakan database development atau database produksi sebagai database test**, karena test membuat dan menghapus data pengguna.

Isi `TEST_DATABASE_URL` di `.env` dengan DSN yang menunjuk ke database pengujian terpisah. Muat ulang environment variables:

```bash
set -a
source ./.env
set +a
```

Pada database test yang belum memiliki skema, terapkan migration yang sama:

```bash
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
```

Jalankan integration test:

```bash
go test -tags=integration -v ./internal/auth ./internal/session ./internal/web
```

Pastikan kedua test yang memerlukan database, yaitu `TestAuthSessionIntegration` dan `TestRevokeAllForUserIntegration`, benar-benar berjalan dan tidak berstatus `SKIP`.

## 6. Cakupan P1

- `cmd/server/`: composition root dan HTTP server.
- `cmd/seed/`: seed Platform Admin awal.
- `internal/config/`: validasi konfigurasi lingkungan.
- `internal/password/`: hashing dan verifikasi password.
- `internal/session/`: store PostgreSQL, expiry, rotasi, grace period, dan revokasi session.
- `internal/auth/`: login, logout, autentikasi middleware, dan penanganan error.
- `internal/web/`: renderer, CSRF, flash message, logging, dan security headers.
- `internal/contract/`: kontrak identitas, session, dan data halaman.
- `web/templates/` dan `web/static/`: template HTML dan CSS.
- `docs/auth-session.md`: dokumentasi lifecycle session.

Cakupan implementasi P1 ini belum mencakup seluruh modul akses database, koneksi database target, database viewer, administrasi user/group, dan migration executor yang menjadi bagian pekerjaan P2, P3, dan P4.

## 7. Keamanan konfigurasi

- Jangan commit `.env`, kredensial database, atau secret aplikasi.
- Gunakan database khusus untuk integration test.
- Password dan Session ID tidak boleh dicatat ke log.
- Cookie session menggunakan `HttpOnly` dan `SameSite=Lax`, serta `Secure` sesuai konfigurasi.
- Pesan flash ditandatangani, tetapi tidak dienkripsi. Isinya dapat dibaca client; gunakan hanya pesan teks tetap yang tidak sensitif.
- Pada produksi, aktifkan `SESSION_COOKIE_SECURE=true` dan gunakan HTTPS.
