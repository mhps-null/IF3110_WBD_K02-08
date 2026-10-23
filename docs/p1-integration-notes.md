# Catatan integrasi P1 sebelum merge

1. Ganti `github.com/example/if3110-milestone1` di `go.mod` dan seluruh import internal dengan module path repository kelompok.
2. Kontrak `internal/contract` dan skema metadata harus mengikuti hasil kickoff. Perubahan di sini adalah keputusan integrasi awal karena P2 belum mulai; sepakati sebelum P2 mengimplementasikan CRUD akses.
3. Terapkan migration tambahan `migrations_meta/002_session_privilege_version.sql` setelah `001_initial_schema.sql` pada metadata database development dan database integration test.
4. P1 membandingkan `sessions.privilege_version` dengan `users.privilege_version` pada setiap request terlindungi. Jika berbeda, session ID dirotasi pada request itu; absolute expiration tetap.
5. **Kontrak wajib untuk P2:** setiap perubahan yang memengaruhi otorisasi pengguna harus menaikkan `users.privilege_version` untuk user yang terdampak, dalam transaksi yang sama. Ini termasuk perubahan `is_platform_admin`, keanggotaan `group_members`, dan akses grup melalui `group_db_access` (untuk perubahan akses grup, naikkan versi semua anggota grup terkait). Jangan naikkan versi hanya untuk perubahan nama/deskripsi group.
6. P1 menyediakan `Store.Rotate` untuk rotasi token aktif. Rotasi otomatis berdasarkan `privilege_version` tidak membutuhkan P2 mengimpor paket internal P1.
7. Tambahkan/pertahankan integration test dengan database metadata test tersendiri untuk login sukses/gagal, session idle/absolute expiration, rotasi/grace period, perubahan privilege, revoke/logout, dan penolakan CSRF tanpa token.
8. Setelah dependency dapat diunduh dengan toolchain Go yang ditetapkan di `go.mod`, jalankan `go mod tidy`, `go test ./...`, `go vet ./...`, dan `go build ./...` menggunakan driver sebenarnya.
