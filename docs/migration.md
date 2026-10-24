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

Eksekusi migration dijalankan dengan prinsip keandalan atomik (*Atomic Transactions*):

1. **Blok Transaksi Target**:
   - Sebelum menjalankan SQL ke database target, sistem membuka transaksi (`BEGIN` / `BeginTx`).
   - Jika seluruh query berhasil dieksekusi, transaksi di-`COMMIT`.
   - Jika terjadi kegagalan/sintaks error, koneksi target langsung di-`ROLLBACK` sehingga database target tetap berada pada kondisi sebelum migrasi dimulai.
2. **Pencatatan Status ke Metadata**:
   - Pencatatan status `failed` dilakukan melalui koneksi ke **Metadata Database**, bukan melalui transaksi database target yang telah di-rollback.
   - Metadata DB mencatat pesan kesalahan lengkap (`error_message`), pelaksana (`executed_by`), waktu (`executed_at`), dan nomor versi untuk keperluan audit trail.
3. **Penyaringan Statement Non-Transaksional (PostgreSQL)**:
   - Beberapa perintah pemeliharaan PostgreSQL tidak dapat berjalan di dalam blok transaksi biasa, antara lain:
     - `CREATE INDEX CONCURRENTLY`
     - `DROP INDEX CONCURRENTLY`
     - `REINDEX ... CONCURRENTLY`
     - `VACUUM`
     - `CLUSTER`
   - Eksekutor PostgreSQL (`internal/dbdriver/postgres/exec.go`) secara proaktif mendeteksi dan menolak statement ini dengan pesan error ramah sebelum dikirim ke database, meminta pengguna menggunakan statement standar (misal `CREATE INDEX` biasa).

---

## 3. Keterbatasan DDL pada MySQL/MariaDB (Bonus)

Pada MySQL dan MariaDB, terdapat perbedaan fundamental perilaku transaksi dibandingkan PostgreSQL:

- **Implicit Commit pada DDL**:
  - Di MySQL, setiap statement DDL (seperti `CREATE TABLE`, `ALTER TABLE`, `DROP TABLE`, `TRUNCATE`) secara otomatis memicu perintah `COMMIT` implisit sebelum dan sesudah eksekusi statement.
  - Akibatnya, perintah `ROLLBACK` **tidak dapat membatalkan** perubahan skema DDL yang sudah dieksekusi apabila terjadi kegagalan di tengah-tengah script SQL majemuk.
- **Pencegahan & Peringatan**:
  - UI dan dokumentasi platform menampilkan peringatan eksplisit kepada pengguna saat mengelola database ber-engine `mysql` atau `mariadb`.
  - Direkomendasikan menyusun satu perubahan skema per migration pada database MySQL untuk meminimalkan status parsial.

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
| `POST` | `/databases/{id}/migrations/{mid}/execute` | View + Edit (`Guards.EditDB`) | Menjalankan eksekusi transaksional ke DB target |
| `POST` | `/databases/{id}/migrations/{mid}/explain` | View + Edit (`Guards.EditDB`) | Menganalisis execution plan query |
