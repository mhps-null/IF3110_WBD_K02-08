# Catatan integrasi P1 sebelum merge

1. Ganti `github.com/example/if3110-milestone1` di `go.mod` dan seluruh import internal dengan module path repository kelompok.
2. **Kontrak belum boleh dianggap final.** Scaffold `internal/contract` hanya draft minimum. Jika kontrak kickoff sudah di-commit, gunakan versi repository tersebut dan sesuaikan P1; jangan menimpa kontrak yang sudah dibekukan.
3. Cocokkan kolom tabel `users` dan `sessions` dengan `migrations_meta/` yang disepakati. Scaffold ini sengaja tidak membuat atau mengubah skema metadata.
4. P1 menyediakan `session.Store.Rotate`, tetapi rancangan kontrak bersama yang diberikan hanya menampilkan `SessionRevoker`. Agar P2 dapat meminta rotasi saat privilege dinaikkan tanpa mengimpor paket internal P1, tentukan interface rotator bersama saat integrasi. Jangan mengubah kontrak yang sudah dibekukan secara sepihak.
5. Tambahkan integration test dengan database metadata test tersendiri untuk login sukses/gagal, session idle/absolute expiration, rotasi/grace period, revoke/logout, dan penolakan CSRF tanpa token. Test saat ini adalah unit test/fake; belum memverifikasi SQL terhadap PostgreSQL nyata.
6. Setelah dependency dapat diunduh, jalankan `go mod tidy` untuk menghasilkan `go.sum`, lalu `go test ./...`, `go vet ./...`, dan `go build ./...` menggunakan driver pgx sebenarnya.
