# IF3110 Milestone 1 — P1 Foundation Scaffold

Scaffold fondasi P1: startup server, renderer `html/template`, login/logout, session berbasis metadata DB, CSRF, security headers, request logging yang tidak mencatat query/header/cookie, dan seed Platform Admin.

> **Penting:** ini scaffold baru, bukan hasil merge ke repository kelompok. Sebelum integrasi, ganti module path, cocokkan `internal/contract` dengan kontrak yang dibekukan saat kickoff, dan pastikan nama/tipe kolom `users` serta `sessions` sama dengan `migrations_meta/` di repository resmi. Catatan integrasi rinci ada di `docs/p1-integration-notes.md`. File migration metadata tidak dibuat atau diubah di scaffold ini.

## Prasyarat

- Go 1.23+
- PostgreSQL dengan skema metadata Milestone 1 yang sudah diterapkan
- Environment variables pada `.env.example` (ekspor secara manual atau gunakan pengelola environment)

## Jalankan

```bash
cp .env.example .env
# Atur DATABASE_URL dan SESSION_SECRET, lalu ekspor variabel dari .env
set -a; . ./.env; set +a
go mod tidy
go run ./cmd/seed
go run ./cmd/server
```

Buka `http://localhost:8080`. Untuk HTTPS/production, set `APP_ENV=production` dan `SESSION_COOKIE_SECURE=true` di belakang terminasi TLS yang benar.

## Verifikasi

```bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
go build ./...
```

Test yang memerlukan PostgreSQL:

```bash
TEST_DATABASE_URL='postgres://.../database_test?sslmode=disable' go test -tags=integration ./internal/session ./internal/auth
```

Saat ini test integrasi database belum diimplementasikan otomatis; perintah di atas adalah pengingat untuk tahap berikutnya, bukan jaminan bahwa test integrasi sudah tersedia.

## Scope P1 yang disiapkan

- `cmd/server/main.go`: composition root
- `cmd/seed/main.go`: seed Platform Admin pertama
- `internal/contract/`: kontrak principal dan session revocation (draft sementara)
- `internal/password/`: wrapper `Hash` dan `Verify`
- `internal/session/`: store dan lifecycle session berbasis PostgreSQL
- `internal/auth/`: handler login/logout
- `internal/web/`: router, template renderer, CSRF, security headers, logging
- `web/templates/`, `web/static/`: halaman dan CSS dasar
- `docs/auth-session.md`: parameter session

Belum termasuk modul P2/P3/P4, policy authorization final, CRUD admin, koneksi database target, viewer, maupun migration executor.
