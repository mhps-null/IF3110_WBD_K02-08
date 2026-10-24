# Schema Diff

## Tujuan

Schema Diff membandingkan dua `contract.SchemaSnapshot` dan menghasilkan kandidat DDL untuk mengubah schema sumber (snapshot A) agar sesuai dengan schema target (snapshot B). SQL yang dihasilkan harus ditinjau dan dapat diedit sebelum dijadikan migration; proses diff tidak mengeksekusi SQL tersebut.

## Perubahan yang dibandingkan

- Tabel: buat atau hapus tabel, dengan identitas pasangan `(schema, nama tabel)`. Tabel bernama sama pada schema PostgreSQL yang berbeda tetap dibandingkan terpisah.
- Kolom: tambah, hapus, dan ubah tipe data, nullability, atau default.
- Constraint: tambah/hapus `PRIMARY KEY`, `FOREIGN KEY`, `UNIQUE`, dan `CHECK`. Constraint dengan nama sama tetapi definisi berbeda dihasilkan sebagai operasi drop lalu add. Foreign key mempertahankan schema/tabel referensi, urutan kolom, serta aturan `ON DELETE` dan `ON UPDATE`.
- Index: tambah/hapus index dan deteksi perubahan kolom, keunikan, metode, atau predicate partial index PostgreSQL. Definisi key dari catalog dipisahkan dari nama kolom biasa agar identifier yang mengandung tanda kurung atau kutip tidak keliru dianggap sebagai ekspresi SQL.
- Index yang menjadi backing index untuk primary key/unique constraint tidak dibuat dua kali karena ikut dikelola oleh constraint.

Perbandingan mempertahankan urutan kolom dalam constraint/index dan mengurutkan hasil berdasarkan nama objek agar SQL lebih deterministik.

Schema, tabel, kolom, constraint, dan index di-quote per komponen. Tanda kutip ganda PostgreSQL dan backtick MySQL/MariaDB di-escape dengan menggandakan delimiter. Titik di dalam nama tabel tetap bagian dari satu identifier.

Snapshot MySQL/MariaDB mencakup database aktif. Saat membandingkan dua database berbeda, objek lokal dan foreign key yang menunjuk database pembanding dipetakan ke namespace database sumber. Kandidat SQL tetap ditujukan untuk database sumber; referensi ke database eksternal dipertahankan. Schema PostgreSQL tidak dipetakan ulang.

## Urutan SQL

Generator mengutamakan urutan dependensi:

1. Buat tabel baru.
2. Lepas constraint dan index lama yang menghalangi perubahan kolom atau penghapusan tabel.
3. Tambah, ubah, atau hapus kolom.
4. Hapus tabel yang tidak lagi ada pada snapshot target.
5. Tambahkan primary key/unique/check, kemudian index pendukung, dan terakhir foreign key setelah key yang direferensikan tersedia. Urutan ini juga menghindari duplikasi index otomatis foreign key MySQL/MariaDB.

Statement yang menghapus tabel/kolom atau constraint diberi penanda `WARNING: DESTRUCTIVE CHANGE`. SQL tetap merupakan kandidat migration dan wajib ditinjau, khususnya untuk operasi destruktif.

## Batasan yang perlu diperhatikan

Perbandingan lintas DBMS belum didukung. Generator mengasumsikan schema PostgreSQL yang dirujuk sudah tersedia; pembuatan/penghapusan schema kosong bukan bagian diff. Snapshot belum mencakup seluruh properti lanjutan seperti trigger, partitioning, storage parameter, INCLUDE pada index PostgreSQL, serta panjang prefix dan arah pengurutan index MySQL/MariaDB. SQL hasil generator tetap perlu ditinjau sebelum dieksekusi.

## Pengujian

Unit test mencakup identitas multi-schema, perubahan predicate/aturan foreign key, pemetaan namespace MySQL/MariaDB, serta golden SQL lintas dialect dengan identifier khusus di `internal/schemadiff/testdata/`. Integration test membuat objek berawalan `test_diff_`, membaca snapshot, menjalankan DDL hasil generator, memastikan diff akhir kosong, lalu membersihkan objek uji tersebut.

```powershell
go test -mod=readonly ./internal/schemadiff
# Atur TEST_DATABASE_URL untuk PostgreSQL dan TEST_MYSQL_URL atau TEST_MARIADB_URL
# untuk database khusus pengujian, lalu:
go test -mod=readonly -tags=integration -v ./internal/schemadiff -run Integration
```
