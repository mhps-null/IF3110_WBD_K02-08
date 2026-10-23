# P1 — Authentication dan Session

## Parameter lifecycle

Nilai awal yang diusulkan oleh prompt kerja:

| Parameter | Nilai |
|---|---:|
| Idle timeout | 30 menit |
| Absolute expiration | 8 jam |
| Interval rotasi session ID | 15 menit |
| Grace period ID sebelumnya | 30 detik |

Nilai tersebut berada di `internal/session/store.go` pada `DefaultConfig()`. Tim harus menyepakatinya dan memperbarui dokumentasi ini jika hasil kickoff berbeda.

## Perilaku keamanan

- Session ID dihasilkan dari 32 byte acak dan dikirim hanya melalui cookie `session_id`.
- Database menyimpan SHA-256 dari ID session, bukan ID mentah.
- Cookie session memakai `HttpOnly`, `SameSite=Lax`, `Path=/`, dan `Secure` saat dikonfigurasi untuk HTTPS/production.
- Idle timeout dan absolute expiration divalidasi melalui store backend.
- Rotasi menyimpan hash ID lama dan batas grace period; rotasi tidak mengubah `absolute_expires_at`.
- Session juga menyimpan snapshot `users.privilege_version`. Jika versi otorisasi pengguna berubah, middleware merotasi session pada request terautentikasi berikutnya.
- Modul yang mengubah hak akses harus menaikkan `users.privilege_version` untuk semua pengguna terdampak dalam transaksi yang sama. Detail kontrak untuk P2 ada di `docs/p1-integration-notes.md`.
- Logout dan `RevokeAllForUser` menonaktifkan session pada metadata DB.
- Request mutasi memerlukan token CSRF bertanda tangan, sama dengan cookie `csrf_token`.
- Logging hanya mencatat method, status, dan durasi; tidak mencatat body, header, cookie, password, token, atau DSN.

## Prasyarat skema

Jalankan skema awal dan migration tambahan secara berurutan:

```bash
psql "$DATABASE_URL" -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -f migrations_meta/002_session_privilege_version.sql
```

Migration `002_session_privilege_version.sql` bersifat additive dan aman dijalankan ulang. Ia menambahkan `privilege_version` pada `users` dan `sessions` dengan nilai awal `0`.
