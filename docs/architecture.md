# Arsitektur Sistem, Keamanan, Migration Lifecycle, & Asynchronous Engine

Dokumen ini menjelaskan desain arsitektur terpadu **Integrated Database Management Platform** (Milestone 1 & Milestone 2), mencakup komunikasi SPA ↔ JSON API, keamanan autentikasi/session/CSRF, kontrol akses berbasis grup & Group Leader, manajemen koneksi & database viewer, siklus hidup migration linear (Up/Rollback/Forward), serta engine eksekusi asynchronous (`cmd/worker`), batch cursor, dan mekanisme pemulihan lock (*lock & crash recovery*).

---

## 1. Arsitektur Layanan & Single-Page Application (SPA)

Platform terdiri atas dua proses backend Go yang terpisah dan satu aplikasi frontend SPA:

1. **Frontend SPA (`frontend/`)**: Dibangun dengan **React 18 + TypeScript + Vite 6 + Tailwind CSS 3**. Seluruh navigasi halaman (login, dashboard, administrasi, schema/data viewer, editor migration, review inbox, job monitoring, dan audit log) berjalan secara client-side tanpa *full-page reload* serta responsif pada layar desktop maupun perangkat mobile.
2. **HTTP API Server (`cmd/server`)**: Menyajikan bundle statis SPA (`internal/web/spa.go`, dilindungi `os.DirFS` + `fs.ValidPath` dan header CSP `default-src 'self'`) serta mengekspos seluruh endpoint `/api/*` dan `/api/v2/*`. **API server tidak pernah mengeksekusi SQL migration pada database target**; API hanya memvalidasi sintaks (`Parse`), mengelola review/approval, dan mempublikasikan job ke antrean durable.
3. **Asynchronous Migration Worker (`cmd/worker`)**: Service Go terpisah (dapat dijalankan banyak instance paralel) yang mengklaim job dari antrean PostgreSQL dengan *lease*, mengambil *session-scoped advisory lock* lintas worker per database target, merevalidasi izin & approval, menjalankan SQL immutable dalam mode normal maupun per batch, serta mencatat hasil dan telemetri.

---

## 2. Autentikasi, Session Lifecycle, & Proteksi CSRF

Diimplementasikan pada `internal/password`, `internal/auth`, `internal/session`, dan `internal/web`:

* **Password Hashing**: Menggunakan **PBKDF2-SHA256** (600.000 iterasi, salt acak 16-byte per akun, perbandingan *constant-time*).
* **Opaque Session & Penyimpanan Aman**: Session ID dibangkitkan dari CSPRNG 32-byte (`crypto/rand`) dan dikirim hanya melalui cookie `session_id` (`HttpOnly`, `SameSite=Lax`, `Path=/`, serta `Secure` pada HTTPS/produksi). Database metadata hanya menyimpan digest **SHA-256** dari Session ID, bukan token mentah.
* **Parameter Siklus Hidup Session**:
  * *Idle timeout*: **30 menit**
  * *Absolute expiration*: **8 jam** (tidak diperpanjang saat rotasi)
  * *Interval rotasi berkala*: **15 menit** dengan *grace period* **30 detik** untuk menghindari *race* pada tab konkuren
* **Rotasi Instan Saat Hak Akses Berubah (`privilege_version`)**: Setiap perubahan peran admin, keanggotaan grup, penetapan Group Leader, atau matriks akses database menaikkan `users.privilege_version` dalam transaksi yang sama. Middleware mendeteksi ketidaksamaan versi dan langsung merotasi Session ID pada request berikutnya. Deaktivasi user dan reset password mencabut seluruh session aktif secara seketika (`RevokeAllForUser`).
* **Proteksi CSRF**: Setiap mutasi (`POST`, `PUT`, `PATCH`, `DELETE`) memverifikasi kecocokan token antara cookie CSRF (`HttpOnly; SameSite=Strict`) dan header `X-CSRF-Token` secara *constant-time*.

---

## 3. Access Control (RBAC) & Group Leader

Diimplementasikan pada `internal/access` dan `internal/admin` (`migrations_meta/004_group_leader.sql`):

* **Pemisahan Hak Platform vs Database**:
  * **Platform Admin (`users.is_platform_admin`)**: Hanya berwenang mengelola user, group, leader, profil koneksi database, matriks akses, dan audit log platform. Admin tanpa keanggotaan grup berakses tidak otomatis dapat melihat data atau menjalankan migration pada database target.
  * **Akses Database Berbasis Grup (`group_db_access`)**: Diberikan pada level `View Only (1)` (melihat skema, data tabel read-only, riwayat migration, dan status job) atau `View + Edit (2)` (ditambah membuat/merevisi migration, mengajukan rollback/forward, serta menekan Execute/Resume/Cancel/Compensate).
* **Resolusi Multi-Group (*Max Privilege*)**: Hak efektif user aktif (`is_active = TRUE`) pada suatu database dihitung langsung pada setiap request tanpa cache sebagai `MAX(level)` dari seluruh grup yang diikutinya (`fail-closed` bila tidak ada akses).
* **Aturan Group Leader**:
  1. Setiap grup memiliki kolom nullable `groups.leader_user_id` (`ON DELETE SET NULL`), divalidasi harus merupakan anggota aktif dari grup tersebut, dan otomatis dikosongkan bila anggota dikeluarkan dari grup.
  2. Satu user diperbolehkan menjadi leader pada beberapa grup sekaligus.
  3. Endpoint `GET /api/databases/{id}/access-groups` mengembalikan daftar seluruh grup pemangku kepentingan (*stakeholder*) beserta status leadernya.

---

## 4. Koneksi Database Target, Pool Telemetry, & Read-Only Viewer

Diimplementasikan pada `internal/connections`, `internal/dbdriver`, `internal/viewer`, `internal/schemadiff`, dan `internal/explain`:

* **Enkripsi Kredensial (`AES-256-GCM`)**: Password database target dienkripsi menggunakan kunci 32-byte `DB_ENCRYPTION_KEY` dengan nonce acak 12-byte per operasi dan tidak pernah dikembalikan melalui JSON API maupun log. Koneksi yang sudah memiliki riwayat migrasi ditolak penghapusannya (`409 Conflict`).
* **Connection Pool Manager**: Mengelola cache `*sql.DB` per koneksi (`MaxOpenConns=10`, `MaxIdleConns=2`, `ConnMaxLifetime=15m`, `ConnMaxIdleTime=5m`) dengan proteksi *generation counter* saat update/invalidasi, serta mengekspos statistik live (`InUse`, `Idle`, `WaitCount`, `WaitDuration`) ke Prometheus.
* **Introspeksi Katalog & Pembacaan Tabel Aman**:
  * Introspeksi membaca katalog sistem PostgreSQL (`pg_catalog`, mengecualikan `pg_catalog` dan `information_schema`) untuk tabel, tipe kolom presisi (`format_type`), constraint (`PK`, `FK`, `UNIQUE`, `CHECK`), dan index.
  * Pembacaan baris tabel (`GET /api/databases/{id}/tables/{table}/data`) wajib berjalan di dalam transaksi `READ ONLY` (`defer tx.Rollback()`), dibatasi `SET LOCAL statement_timeout = 5000` (tanpa mencemari koneksi pool), memvalidasi kolom `ORDER BY` terhadap *whitelist* katalog, dan membatasi ukuran halaman (`1..100`).
* **Fitur Fondasi M1 yang Dipertahankan**: Dukungan driver MySQL/MariaDB untuk viewer, komparasi snapshot skema & generator kandidat DDL (`internal/schemadiff`), serta analisis query plan `EXPLAIN` / `EXPLAIN ANALYZE` dalam transaksi yang di-rollback (`internal/explain`).

---

## 5. Migration Lifecycle, Approval Multi-Group, Rollback/Forward, & History

Diimplementasikan pada `internal/lifecycle` (`migrations_meta/005_migration_lifecycle.sql` & `006_lifecycle_execution_runs.sql`):

* **Alur Status**: `Draft → Pending Review → Approved → Pending (Queue) → Running → Success / Failed` (serta `Rejected` bila ditolak reviewer).
* **Definisi & Validasi Sintaks Tanpa Eksekusi**:
  * Setiap migration wajib memiliki `SQL Up` serta salah satu dari `SQL Down` (reversible) atau alasan tertulis `irreversible_reason` (irreversible; tidak dapat di-rollback).
  * Validasi sebelum review menggunakan fase **PostgreSQL Extended Protocol `Parse`** (`Prepare` tanpa eksekusi), mendukung satu statement SQL per Up dan satu per Down, serta mengembalikan posisi karakter dan `SQLSTATE` tanpa memodifikasi database target.
* **Aturan Review & Approval Semua Grup**:
  * Setiap pengajuan perubahan (`up`, `rollback`, maupun `forward`) mewajibkan persetujuan dari **leader seluruh grup** yang memiliki akses ke database target, **termasuk grup `View Only`**.
  * Jika satu user memimpin lebih dari satu grup berakses, **satu tindakan Approve otomatis mewakili seluruh grup yang dipimpinnya**.
  * Jika terdapat grup berakses yang **belum memiliki leader aktif**, request tetap berstatus **`Pending Review`** (tidak otomatis ditolak). Pemilik request dapat menekan *Refresh review* setelah leader ditetapkan.
  * Mengubah SQL pada request yang belum pernah dieksekusi akan membuat revisi immutable baru, mengembalikan status ke `Draft`, dan me-reset approval aktif (keputusan lama tetap tersimpan di `migration_approval_decisions`).
  * Setelah seluruh leader menyetujui, status menjadi `Approved` dan **menunggu pengguna berhak `View + Edit` menekan tombol `Execute`** (tidak *auto-enqueue*).
* **Immutability, Linear Rollback/Forward, & `Hide rollback`**:
  * Begitu sebuah migration pernah memiliki attempt eksekusi (**baik berhasil maupun gagal/ter-rollback**), definisi `SQL Up` dan `SQL Down` menjadi **immutable** secara permanen (dijaga juga oleh trigger database). Perbaikan wajib diajukan sebagai migration baru.
  * **Rollback** hanya diizinkan pada migration aktif paling akhir (untuk urutan `A → B → C`, `C` harus berhasil di-rollback sebelum `B`) menggunakan `SQL Down` asli dari revisi terkait dan menyimpan rujukan `rollback_of_execution_id`.
  * **Forward** menjalankan kembali `SQL Up` asli secara linear dalam urutan kebalikan rollback (satu per satu, tidak otomatis menjalankan migration sesudahnya). Eksekusi migration `Up` baru menggantikan cabang forward yang ditinggalkan.
  * **Pencatatan Semua Attempt & Filter `Hide rollback`**: Seluruh attempt (`up`, `rollback`, `forward`, `retry`, baik `success` maupun `failed`) disimpan secara *append-only* pada `migration_execution_runs` & `migration_execution_attempts` (mencatat `migration_id`, `execution_id`, `revision_id`, `user_id`, waktu, status, error, dan salinan `sql_snapshot`). Opsi **Hide rollback** hanya menyembunyikan pasangan `(eksekusi sukses yang dibatalkan, rollback sukses yang merujuk execution_id tersebut)` — eksekusi **Forward** berikutnya dan **seluruh attempt gagal** tetap ditampilkan, tanpa menghapus data riwayat maupun audit log.

---

## 6. Asynchronous Queue, Cross-Worker Lock, Batch Cursor, & Pemulihan Kegagalan

Diimplementasikan pada `internal/jobs` dan `cmd/worker` (`migrations_meta/007_job_queue_worker.sql`):

### A. Durable Queue & Worker Lease
* Tabel `migration_jobs` di database metadata bertindak sebagai antrean durable *brokerless* yang bertahan melintasi restart API maupun worker. Payload antrean hanya memuat ID job/request, `db_id`, `idempotency_key`, `enqueued_by`, konfigurasi batch, serta W3C `traceparent` / `correlation_id` (**tanpa** kredensial, session ID, atau teks SQL; worker selalu membaca SQL dari revisi immutable di database metadata).
* Worker mengklaim job siap jalan menggunakan `FOR UPDATE SKIP LOCKED` dengan *lease* (`lease_owner`, `lease_expires_at`, default `30s`) yang diperpanjang oleh goroutine *heartbeat* setiap `lease / 3`. Kehilangan lease otomatis membatalkan context eksekusi (*fencing*).

### B. Cross-Worker Per-Database Lock & Runbook Pemulihan Lock
Worker menggunakan **dua level PostgreSQL session-scoped advisory lock** pada koneksi khusus (`internal/jobs/lock.go`):
1. **Metadata DB Lock (`0x4f34<<48 | db_id`)**: Dipakai bersama oleh seluruh instance worker sehingga **satu database hanya menjalankan maksimal satu migration pada satu waktu** (termasuk melindungi sela di antara commit batch), sementara **database berbeda dieksekusi secara paralel**.
2. **Target DB Fencing Lock**: Diambil langsung pada server database target untuk mencegah *split-brain* apabila sesi metadata worker lama terputus sementara sesi targetnya masih aktif.

**Mekanisme Pemulihan Lock Ketika Worker Gagal (*Crash / Network Drop*):**
1. Saat proses worker mati atau koneksinya terputus, PostgreSQL secara otomatis menutup sesi TCP terkait dan **langsung melepaskan kedua session-scoped advisory lock** tanpa intervensi manual; `pg_advisory_unlock` hanya dapat dipanggil oleh sesi pemilik sehingga worker lain tidak pernah bisa melepas lock milik worker yang masih hidup.
2. Setelah `lease_expires_at` terlewati, siklus `RecoverExpired` pada worker aktif memindahkan job *apply* ke status **`uncertain`** (dan memasang penahan pada `migration_db_holds`) serta mencatat event `job.lease_expired`.
3. Worker berikutnya mengambil job tersebut, mengakuisisi ulang `DBLock`, dan **memeriksa tabel bukti eksekusi `_dbplatform.migration_ledger` pada database target sebelum melakukan tindakan apa pun**:
   * Setiap statement normal maupun setiap batch selalu meng-`INSERT` baris `_dbplatform.migration_ledger (job_id, direction, batch_seq)` **di dalam transaksi target yang sama** dengan perubahan skema/data.
   * Jika baris ledger ditemukan → transaksi sebelumnya terbukti sudah `COMMIT` (tidak dieksekusi ulang → bebas *double commit*).
   * Jika baris ledger tidak ditemukan → transaksi sebelumnya terbukti ter-rollback; attempt gagal dicatat lalu dijadwalkan retry terbatas dengan *exponential backoff* (untuk error sementara) atau dilanjutkan dari batch berikutnya.
   * Jika database target sedang tidak terjangkau sehingga hasil commit belum dapat diverifikasi → job tetap berstatus **`uncertain`** dan `migration_db_holds` **menahan seluruh migration berikutnya pada database yang sama** sampai verifikasi berhasil.

### C. Pemrosesan Per Batch, Checkpoint, Resume, & Kompensasi Terbalik
* **Kontrak Kursor Batch**: Pengguna memilih kolom integer unik yang tidak berubah (mis. `id`, divalidasi langsung terhadap `pg_catalog`) dan `batch_size`. SQL memakai parameter `$1` (batas bawah eksklusif) dan `$2` (batas atas inklusif): `WHERE id > $1 AND id <= $2`. Worker hanya membaca batas atas tiap batch (`SELECT max(c) ... ORDER BY id LIMIT batch_size`) tanpa memuat seluruh dataset ke memori.
* **Transaksi & Checkpoint Per Batch**: Setiap batch dijalankan dalam transaksi terpisah yang meng-commit perubahan data bersama baris checkpoint `_dbplatform.migration_ledger`, lalu memperbarui progres `rows_done`, `batches_done`, dan `last_cursor` untuk dipantau live di SPA (`/jobs`).
* **Kegagalan Parsial (`Partial`)**: Jika satu batch gagal, hanya transaksi batch tersebut yang di-rollback; batch sebelumnya tetap committed, status job menjadi **`partial`**, dan database ditahan (`migration_db_holds`).
* **Melanjutkan dari Checkpoint (`Resume`)**: `POST /api/v2/jobs/{id}/resume` melanjutkan eksekusi dari `last_cursor` yang sudah committed tanpa mengulang batch yang telah selesai (tetap melewati revalidasi izin `View + Edit` dan keabsahan snapshot approval).
* **Kompensasi Per Batch (`Compensate`)**: `POST /api/v2/jobs/{id}/compensate` menjalankan `SQL Down` yang immutable dan telah disetujui secara per batch dalam **urutan batch terbalik**, masing-masing dengan bukti ledger `compensate`. Jika gagal di tengah kompensasi, checkpoint tersimpan dengan status **`partially_reverted`** agar dapat dilanjutkan hingga **`reverted`**. Jika migration bersifat *irreversible* atau `SQL Down` tidak kompatibel dengan batching, pembatalan ditolak (`409 Conflict`) dan diarahkan untuk membuat migration perbaikan baru.
