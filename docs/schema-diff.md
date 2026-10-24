# Schema Diff

## Tujuan

Schema Diff membandingkan dua `contract.SchemaSnapshot` dan menghasilkan kandidat DDL untuk mengubah schema sumber (snapshot A) agar sesuai dengan schema target (snapshot B). SQL yang dihasilkan harus ditinjau dan dapat diedit sebelum dijadikan migration; proses diff tidak mengeksekusi SQL tersebut.

## Perubahan yang dibandingkan

- Tabel: buat atau hapus tabel.
- Kolom: tambah, hapus, dan ubah tipe data, nullability, atau default.
- Constraint: tambah/hapus `PRIMARY KEY`, `FOREIGN KEY`, `UNIQUE`, dan `CHECK`. Constraint dengan nama sama tetapi definisi berbeda dihasilkan sebagai operasi drop lalu add.
- Index: tambah/hapus index dan deteksi perubahan kolom, keunikan, atau metode index. Generator menyediakan sintaks untuk index BTREE/HASH, FULLTEXT, dan SPATIAL yang dikenali introspector MySQL/MariaDB, serta metode index yang dilaporkan PostgreSQL.
- Index yang menjadi backing index untuk primary key/unique constraint tidak dibuat dua kali karena ikut dikelola oleh constraint.

Perbandingan mempertahankan urutan kolom dalam constraint/index dan mengurutkan hasil berdasarkan nama objek agar SQL lebih deterministik.

## Urutan SQL

Generator mengutamakan urutan dependensi:

1. Buat tabel baru.
2. Lepas constraint dan index lama yang menghalangi perubahan kolom atau penghapusan tabel.
3. Tambah, ubah, atau hapus kolom.
4. Hapus tabel yang tidak lagi ada pada snapshot target.
5. Tambahkan constraint setelah tabel/kolom yang dibutuhkan tersedia, lalu tambahkan index independen.

Statement yang menghapus tabel/kolom atau constraint diberi penanda `WARNING: DESTRUCTIVE CHANGE`. SQL tetap merupakan kandidat migration dan wajib ditinjau, khususnya untuk operasi destruktif.

## Batasan yang perlu diperhatikan

Snapshot saat ini tidak menyimpan seluruh properti lanjutan dari setiap objek. Identitas tabel pada diff masih menggunakan nama tabel tanpa memasukkan `Schema`; ekspresi predicate/partial index dan beberapa opsi FK MySQL seperti `ON UPDATE`/`ON DELETE` belum direpresentasikan lengkap pada model `contract.Index`/`contract.Constraint`. Pada database yang memakai multi-schema atau objek dengan properti tersebut, jangan jalankan SQL hasil generator tanpa pemeriksaan manual dan verifikasi tambahan.
