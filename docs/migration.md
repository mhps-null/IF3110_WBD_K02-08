# P4 — Dokumentasi Migration, Eksekusi Transaksional, dan Analisis EXPLAIN

Dokumen ini menjelaskan keputusan desain arsitektur, mekanisme integritas, dan aturan operasional untuk modul **Migration** (P4) pada Database Management Platform IF3110 Milestone 1.

---

## 1. Aturan Versi Linear & Konkurensi

Sistem migration mencatat seluruh perubahan struktur dan data skema secara berurutan (*linear history*) per database target:

1. **Status Draft**:
   - Migration baru disimpan dengan status `draft`.
   - Nilai kolom `version` adalah `NULL`.
   - Draft dapat ditinjau, diedit, diuji dengan `EXPLAIN`, atau dihapus oleh pengguna yang memiliki hak akses **View + Edit**.
2. **Pemberian Nomor Versi**:
   - Nomor versi linear ($1, 2, 3, \dots$) diberikan saat proses eksekusi dimulai.
   - Penomoran menggunakan query `SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE db_id = $1 AND version IS NOT NULL`.
   - Batasan integritas database dijamin oleh `CONSTRAINT uq_migrations_db_version UNIQUE (db_id, version)`.
3. **Pencegahan Versi Bercabang (Advisory Lock)**:
   - Untuk mencegah dua eksekusi paralel pada database yang sama menghasilkan nomor versi bentrok atau bercabang (*branching*), proses pemberian versi dan eksekusi diamankan menggunakan PostgreSQL advisory lock:
     ```sql
     SELECT pg_advisory_xact_lock(hashtext('db_migration_' || $1::text));
     ```
   - Lock ini terikat pada transaksi metadata dan otomatis terlepas begitu transaksi selesai.
4. **Immutability (Kekekalan Riwayat)**:
   - Begitu migration berstatus `success` atau `failed`, record tersebut menjadi **immutable** (tidak dapat diubah atau dihapus kembali).
   - Setiap perubahan skema berikutnya harus diajukan melalui migration baru.
   - Perubahan SQL pada draft divalidasi dengan checksum SHA-256 saat eksekusi.

---

## 2. Perilaku Transaksi & Penanganan Kegagalan

Perilaku transaksi bergantung pada jenis statement dan engine. Sistem tidak boleh mengklaim rollback berhasil ketika engine tidak memberikan jaminan tersebut:

1. **Statement transaksional (misalnya DML)**:
   - Untuk statement yang tidak teridentifikasi sebagai pemicu implicit commit pada MySQL/MariaDB, executor membuka transaksi (`BeginTx`), mengeksekusi statement, lalu melakukan `COMMIT` ketika berhasil.
   - Jika statement gagal, executor meminta `ROLLBACK` dan memeriksa hasilnya. Jika rollback gagal, error menyatakan bahwa rollback tidak dapat dikonfirmasi.
   - Jika `COMMIT` mengembalikan error, hasil target dianggap tidak pasti karena server mungkin sudah melakukan commit sebelum konfirmasi hilang. Executor tidak mengasumsikan rollback sesudahnya dapat menyelesaikan ketidakpastian tersebut.
2. **DDL dan statement pemicu implicit commit pada MySQL/MariaDB**:
   - Statement tersebut dijalankan langsung melalui `db.ExecContext`, tidak dibungkus transaksi `database/sql` yang memberi kesan keliru bahwa perubahannya bisa di-rollback.
   - Jika eksekusi gagal, error menyatakan bahwa perubahan mungkin sudah mengalami implicit commit dan tidak dapat dijamin dibatalkan oleh migration executor.
   - Status `failed` pada metadata menunjukkan eksekusi melaporkan error; status itu **bukan bukti** bahwa target tidak berubah, terutama bila DDL/prosedur tersimpan gagal atau hasil `COMMIT` tidak dapat dikonfirmasi. Periksa schema/data target sebelum menjalankan ulang migration yang gagal.
   - Satu migration MySQL/MariaDB harus berisi tepat satu statement SQL. Executor menolak statement majemuk (dengan mengabaikan semikolon di literal dan komentar) agar DDL setelah statement transaksional tidak memicu implicit commit tersembunyi.
   - Jangan memasukkan perintah pengendali transaksi (`BEGIN`, `COMMIT`, `ROLLBACK`, `SAVEPOINT`, atau perubahan `autocommit`) karena batas transaksi dikelola executor.
3. **Pencatatan Status ke Metadata**:
   - Pencatatan status `failed` dilakukan melalui koneksi ke **Metadata Database**, bukan melalui transaksi database target yang telah di-rollback.
   - Metadata DB mencatat pesan kesalahan lengkap (`error_message`), pelaksana (`executed_by`), waktu (`executed_at`), dan nomor versi untuk keperluan audit trail.
4. **Penyaringan Statement Non-Transaksional (PostgreSQL)**:
   - Beberapa perintah pemeliharaan PostgreSQL tidak dapat berjalan di dalam blok transaksi biasa, antara lain:
     - `CREATE INDEX CONCURRENTLY`
     - `DROP INDEX CONCURRENTLY`
     - `REINDEX ... CONCURRENTLY`
     - `VACUUM`
     - `CLUSTER`
   - Eksekutor PostgreSQL (`internal/dbdriver/postgres/exec.go`) secara proaktif mendeteksi dan menolak statement ini dengan pesan error ramah sebelum dikirim ke database, meminta pengguna menggunakan statement standar (misal `CREATE INDEX` biasa).

---

## 3. Keterbatasan DDL pada MySQL/MariaDB (Bonus)

MySQL/MariaDB melakukan implicit commit untuk banyak statement DDL dan sejumlah statement administratif. Karena itu, executor menjalankan statement yang terdeteksi sebagai pemicu implicit commit langsung melalui `db.ExecContext`, tanpa `BeginTx`/`Rollback` dari `database/sql`. Pada kegagalan, executor tidak mengklaim perubahan telah dibatalkan.

Peringatan ini tidak berarti semua DDL pasti meninggalkan perubahan parsial; artinya executor tidak dapat menggunakan transaksi aplikasi untuk menjamin rollback. Status `failed` perlu ditindaklanjuti dengan pemeriksaan keadaan aktual database. Sebaiknya gunakan satu perubahan DDL per migration dan hindari retry otomatis sebelum schema target diperiksa. Statement yang mengatur batas transaksi atau `autocommit` ditolak karena executor harus mengendalikan transaksi sendiri.

---

## 4. Analisis Query Plan (Bonus EXPLAIN & EXPLAIN ANALYZE)

Platform menyediakan fitur peninjauan execution plan sebelum eksekusi migrasi:

1. **EXPLAIN (Estimated)**:
   - Menjalankan `EXPLAIN (FORMAT JSON)` pada PostgreSQL atau `EXPLAIN FORMAT=JSON` pada MySQL.
   - Mengurai hasil menjadi struct pohon `contract.Plan` dan `contract.PlanNode`.
   - Menampilkan:
     - Jenis scan tabel (Seq Scan, Index Scan, Bitmap Index Scan, Ref Scan).
     - Indeks yang digunakan.
     - Strategi join (Nested Loop, Hash Join, Merge Join).
     - Estimasi biaya (*startup cost* & *total cost*).
     - Estimasi jumlah baris (*planned rows*).
2. **EXPLAIN ANALYZE (Actual Execution)**:
   - Menjalankan `EXPLAIN (ANALYZE, FORMAT JSON)` untuk mengukur metrik aktual (*actual execution time* dalam milidetik dan *actual rows*).
   - **Jaminan Keamanan (Safety Guarantee)**:
     - Pada query DML (`INSERT`, `UPDATE`, `DELETE`), opsi `ANALYZE` benar-benar memanipulasi baris data di target.
     - Untuk menjamin tidak ada efek samping data sebelum migrasi di-commit secara resmi, sistem **selalu membungkus EXPLAIN ANALYZE di dalam transaksi yang dipastikan di-rollback** (`defer tx.Rollback()`).
3. **Statement DDL**:
   - Engine PostgreSQL tidak mendukung perintah `EXPLAIN` untuk perintah DDL (misal `CREATE TABLE`).
   - Sistem mendeteksi perintah DDL dan mengembalikan tampilan informatif *"EXPLAIN tidak tersedia untuk statement DDL"* tanpa menimbulkan error crash pada sistem.

---

## 5. Rute HTTP Modul Migration

Semua endpoint dilindungi oleh CSRF protection dan guard otorisasi berbasis role:

| Method | Path | Hak Akses Minimal | Deskripsi |
|---|---|---|---|
| `GET` | `/databases/{id}/migrations` | View Only (`Guards.ViewDB`) | Melihat riwayat daftar linear migrasi |
| `GET` | `/databases/{id}/migrations/new` | View + Edit (`Guards.EditDB`) | Membuka form editor migrasi kosong |
| `POST` | `/databases/{id}/migrations/new` | View + Edit (`Guards.EditDB`) | Menerima prefill `title` dan `sql` dari modul Schema Diff (P2) tanpa langsung menyimpan |
| `POST` | `/databases/{id}/migrations` | View + Edit (`Guards.EditDB`) | Menyimpan draft migrasi baru |
| `GET` | `/databases/{id}/migrations/{mid}` | View Only (`Guards.ViewDB`) | Melihat rincian migrasi |
| `POST` | `/databases/{id}/migrations/{mid}` | View + Edit (`Guards.EditDB`) | Memperbarui query/judul draft |
| `POST` | `/databases/{id}/migrations/{mid}/execute` | View + Edit (`Guards.EditDB`) | Mengeksekusi migration dengan perilaku transaksi sesuai engine dan jenis statement |
| `POST` | `/databases/{id}/migrations/{mid}/explain` | View + Edit (`Guards.EditDB`) | Menganalisis execution plan query |
