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
- Logout dan `RevokeAllForUser` menonaktifkan session pada metadata DB.
- Request mutasi memerlukan token CSRF bertanda tangan, sama dengan cookie `csrf_token`.
- Logging hanya mencatat method, path tanpa query, status, dan durasi; tidak mencatat body, header, cookie, password, token, atau DSN.

## Prasyarat skema

Implementasi ini mengasumsikan kolom `sessions` yang disebut dalam spesifikasi: `user_id`, `id_hash`, `prev_id_hash`, `prev_valid_until`, `created_at`, `last_seen_at`, `rotated_at`, `absolute_expires_at`, `revoked_at`. Tabel `users` diasumsikan memiliki `id`, `username`, `password_hash`, `is_platform_admin`, `is_active`, dan `created_at`.

**Tidak ada migration metadata yang dibuat oleh scaffold ini.** Cocokkan nama dan tipe dengan skema tim yang telah dibekukan sebelum menjalankan program.
