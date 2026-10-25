# Catatan integrasi P1 sebelum merge

1. Ganti `github.com/mhps-null/IF3110_WBD_K02-08` di `go.mod` dan seluruh import internal dengan module path repository kelompok.
2. Kontrak `internal/contract` dan skema metadata harus mengikuti keputusan kickoff. Karena P2 belum mengimplementasikan perubahan akses, ketentuan versi otorisasi di bawah perlu disepakati sebelum P2 mulai.
3. Terapkan migration additive `migrations_meta/002_session_privilege_version.sql` setelah `001_initial_schema.sql` pada database metadata development dan database integration test. Migration menambahkan `users.privilege_version` serta `sessions.privilege_version`; jangan mengedit migration `001` yang sudah dibekukan.
4. Pada pembuatan session, P1 menyalin `users.privilege_version` ke `sessions.privilege_version`. Pada setiap request terlindungi, P1 membandingkan keduanya. Jika berbeda, middleware merotasi Session ID dan menyimpan versi terbaru pada row session. `absolute_expires_at` tidak berubah.

## Kontrak wajib untuk perubahan otorisasi (P2 dan integrasi terkait)

`users.privilege_version` adalah revisi otorisasi per pengguna, bukan level akses dan bukan pengganti `AccessChecker`. Setiap perubahan metadata yang dapat mengubah hak efektif pengguna wajib menaikkan versi pengguna terdampak dalam **transaksi yang sama** dengan perubahan tersebut. P1 tidak dapat mendeteksi perubahan hak akses jika versi tidak dinaikkan.

- Perubahan `users.is_platform_admin`: naikkan versi pengguna yang diubah dalam statement/transaksi yang sama.
- Penambahan atau penghapusan `group_members`: naikkan versi user yang ditambahkan/dihapus, dalam transaksi yang sama. Untuk penghapusan, simpan `user_id` sebelum baris membership dihapus.
- Penambahan, pengubahan, atau penghapusan `group_db_access`: naikkan versi semua user yang menjadi anggota group terkait, dalam transaksi yang sama.
- Penghapusan group yang meng-cascade `group_members`: naikkan versi anggota group sebelum penghapusan/cascade.
- Modul lain yang menghapus database target dan menyebabkan `group_db_access` terhapus melalui cascade harus mengoordinasikan bump versi untuk pengguna terdampak sebelum cascade. Jangan menganggap perubahan akses yang terjadi melalui cascade sebagai tidak relevan.
- Nonaktifkan pengguna dengan mekanisme pencabutan session yang disepakati; jangan mengandalkan rotasi saja untuk menonaktifkan akun.

Contoh perubahan hak akses suatu group (dijalankan di dalam transaksi yang sama dengan perubahan `group_db_access`):

```sql
UPDATE users AS u
SET privilege_version = u.privilege_version + 1
WHERE EXISTS (
    SELECT 1
    FROM group_members AS gm
    WHERE gm.user_id = u.id AND gm.group_id = $1
);
```

Untuk perubahan membership, bump versi `user_id` yang terkena secara langsung. Pada penghapusan group, bump versi seluruh anggota sebelum `DELETE FROM groups`. Semua query harus menggunakan parameter.

Versi dinaikkan pada setiap perubahan otorisasi yang relevan, baik grant maupun revoke. Ini sengaja lebih konservatif daripada mendeteksi hanya kenaikan privilege: session tetap dirotasi setelah perubahan akses sehingga snapshot session selalu mengikuti revisi otorisasi terbaru. Rotasi terjadi pada request terlindungi berikutnya. Authorization guard P2 tetap wajib menghitung izin terbaru pada backend setiap request; rotasi tidak menggantikan pemeriksaan izin.

5. P1 menyediakan `session.Store.Rotate`; modul lain tidak perlu mengimpor paket internal P1. P2 mengubah data otorisasi dan menaikkan `privilege_version`; P1 mendeteksinya melalui metadata.
6. Integration test P1 menggunakan `TEST_DATABASE_URL` dan memerlukan schema `001` serta migration `002`. Test harus memverifikasi login, session idle/absolute expiry, rotasi/grace period, rotasi setelah revisi otorisasi berubah, logout, serta pencabutan session.
7. Jalankan `go test ./...`, `go vet ./...`, dan `go build ./...` menggunakan toolchain yang ditetapkan `go.mod` sebelum merge. Test integration P1 dapat dijalankan dengan `go test -tags=integration ./internal/auth` setelah database test disiapkan.
