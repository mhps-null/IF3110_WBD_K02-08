# P1 — Authentication dan Session

## Parameter lifecycle

Nilai awal usulan prompt kerja:

| Parameter | Nilai |
|---|---:|
| Idle timeout | 30 menit |
| Absolute expiration | 8 jam |
| Interval rotasi Session ID | 15 menit |
| Grace period ID sebelumnya | 30 detik |

Nilai tersebut berasal dari `internal/session/store.go` (`DefaultConfig()`). Tim harus menyepakatinya dan memperbarui dokumentasi ini jika hasil kickoff berbeda.

## Perilaku keamanan

- Session ID dihasilkan dari 32 byte acak dan dikirim hanya melalui cookie `session_id`.
- Metadata database menyimpan SHA-256 dari Session ID, bukan ID mentah.
- Cookie menggunakan `HttpOnly`, `SameSite=Lax`, `Path=/`, dan `Secure` saat dikonfigurasi untuk HTTPS/production.
- Idle timeout dan absolute expiration diperiksa pada setiap request terlindungi.
- Rotasi menyimpan hash ID lama selama grace period; rotasi tidak mengubah `absolute_expires_at`.
- Setiap session menyimpan snapshot `sessions.privilege_version`. Middleware membandingkannya dengan `users.privilege_version`; jika berbeda, Session ID dirotasi pada request terlindungi berikutnya dan snapshot diperbarui.
- Modul yang mengubah otorisasi wajib menaikkan versi semua pengguna terdampak dalam transaksi yang sama. Detail kontraknya ada di `docs/p1-integration-notes.md`.
- Logout dan `RevokeAllForUser` menonaktifkan session di metadata database.
- Request yang mengubah state memerlukan token CSRF.
- Logging tidak mencatat body, query string, header, cookie, password, token, atau DSN.

## Prasyarat skema

Terapkan migration metadata secara berurutan pada database yang belum memiliki skema tersebut:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/003_audit_logs.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/004_group_leader.sql
```

Migration `002` bersifat additive dan idempotent. Migration ini menambahkan `users.privilege_version` dan `sessions.privilege_version` dengan nilai awal `0`. Pada Milestone 2, `003_audit_logs.sql` menambahkan tabel `audit_logs` dan `004_group_leader.sql` menambahkan `groups.leader_user_id`. Jangan mengubah kolom yang sudah ada dalam skema metadata yang dibekukan.

## Pembaruan Milestone 2 (JSON API & SPA)

- Seluruh endpoint autentikasi disajikan sebagai JSON API (`GET /api/auth/csrf`, `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/me`) untuk dikonsumsi SPA di `frontend/`.
- Middleware `auth.Handler.Authenticate` mengembalikan JSON `401 Unauthorized` / `503 Service Unavailable` pada rute `/api/*` dengan tetap mempertahankan aturan rotasi session, `privilege_version`, grace period, idle timeout, dan absolute expiration yang sama.
- Middleware `web.CSRFMiddleware` menerima token CSRF melalui header `X-CSRF-Token` untuk request JSON tanpa mengonsumsi body `application/json`. Detail lengkap terdapat di `docs/m2-p1-spa-and-auth.md`.


## Flash message setelah redirect

- Pesan lintas redirect disimpan dalam cookie `flash_notice` yang ditandatangani dengan HMAC-SHA256 menggunakan `SESSION_SECRET` dan domain separation `if3110-flash:`.
- Cookie berlaku paling lama dua menit, memakai `HttpOnly`, `SameSite=Lax`, dan mengikuti konfigurasi `Secure` aplikasi.
- Pesan divalidasi dan cookie dihapus saat halaman login atau beranda memakainya. Pesan harus berupa teks tetap dari aplikasi; jangan memasukkan error driver, DSN, atau input pengguna mentah.
