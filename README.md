# IF3110 Milestone 1 — P1 Foundation Scaffold

Scaffold fondasi P1: startup server, renderer `html/template`, login/logout, session berbasis metadata DB, CSRF, security headers, request logging yang tidak mencatat query/header/cookie, dan seed Platform Admin.

> **Penting:** pastikan `internal/contract` dan skema metadata mengikuti keputusan integrasi tim. Migration `002_session_privilege_version.sql` menambah kolom otorisasi yang diperlukan P1; jangan mengubah migration `001` yang sudah dibekukan. Catatan integrasi dan kewajiban P2 untuk memperbarui versi otorisasi ada di `docs/p1-integration-notes.md`.

## Prasyarat

- Go 1.24.0 sesuai `go.mod`
- PostgreSQL dengan skema metadata Milestone 1 yang sudah diterapkan, termasuk migration `002_session_privilege_version.sql`
- Environment variables pada `.env.example` (ekspor secara manual atau gunakan pengelola environment)

## Jalankan

```bash
cp .env.example .env
# Atur DATABASE_URL dan SESSION_SECRET, lalu ekspor variabel dari .env
set -a; . ./.env; set +a
go mod tidy
# Jika database baru, terapkan migration metadata sesuai urutan di docs/auth-session.md
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

Integration test auth/session menggunakan database PostgreSQL yang ditentukan oleh `TEST_DATABASE_URL`; pastikan database pengujian memiliki schema `001` dan migration `002` sebelum menjalankan test dengan build tag `integration`.

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
