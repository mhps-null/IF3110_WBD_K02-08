# P2 — Access Control

## Konsep Dasar

- User dapat menjadi anggota satu atau lebih group.
- Akses ke database diberikan melalui group, bukan langsung ke user.
- Platform Admin adalah role di level platform untuk mengelola user, group, koneksi, dan matriks akses. Role ini bukan merupakan level akses ke database.

## Level Akses

| Level | Nilai di `group_db_access.level` | Kemampuan |
|---|---:|---|
| `AccessNone` | — | Tidak memiliki akses |
| `AccessViewOnly` | `1` | Melihat schema, data, dan riwayat migration |
| `AccessViewEdit` | `2` | Semua akses View Only, ditambah membuat, mengedit, dan mengeksekusi migration |

## Aturan Akses Efektif

Akses efektif user ke suatu database diambil dari level akses tertinggi yang dimiliki melalui seluruh group yang diikutinya.

Jika tidak ada group yang memberikan akses, maka hasilnya adalah `AccessNone`.

Contoh:

- User U mendapat `View Only` dari Group A dan `View Edit` dari Group B untuk DB1.
  → `EffectiveLevel(U, DB1) = AccessViewEdit`
- User U mendapat `View Only` untuk DB1, tetapi tidak memiliki akses ke DB2.
  → `EffectiveLevel(U, DB2) = AccessNone`
- User U tidak tergabung dalam group mana pun.
  → `EffectiveLevel(U, *) = AccessNone`

Platform Admin juga mengikuti aturan ini. Jadi, admin yang tidak memiliki group tetap tidak memiliki akses ke database. Kasus ini diuji di `internal/access/checker_integration_test.go` melalui test `admin tanpa grup → none`.

User yang sudah tidak aktif juga dianggap tidak memiliki akses. Query `EffectiveLevel` hanya menghitung user dengan `is_active = TRUE`. Dengan begitu, session yang masih aktif setelah user dinonaktifkan tetap tidak bisa digunakan untuk mengakses database.

## Aksi dan Level Minimum

Daftar action dan level minimumnya didefinisikan di `internal/access/policy.go` dan digunakan oleh `Guards`.

| Aksi | Level Minimum |
|---|---|
| `view_schema` | View Only |
| `view_data` | View Only |
| `view_history` | View Only |
| `view_schema_diff` | View Only |
| `create_migration` | View + Edit |
| `edit_migration` | View + Edit |
| `execute_migration` | View + Edit |

Jika sebuah action tidak terdaftar di `policy.go`, aksesnya ditolak secara default (fail-closed).

Aksi di level platform, seperti CRUD user, group, koneksi, dan matriks akses, tidak menggunakan `policy.go`. Akses untuk fitur tersebut diperiksa melalui `Guards.Admin` berdasarkan `Principal.IsPlatformAdmin`.

## Pengecekan Akses Setiap Request

`Guards.ViewDB` dan `Guards.EditDB` memanggil `AccessChecker.EffectiveLevel` pada setiap request dan tidak menggunakan cache.

Dengan cara ini, perubahan atau pencabutan akses langsung berlaku pada request berikutnya. Penggunaan cache dapat membuat akses lama tetap berlaku sampai cache expired, sehingga tidak sesuai dengan kebutuhan keamanan sistem.

## Rotasi Session Saat Privilege Berubah

Saat membership atau matriks akses berubah, `Store` menaikkan `users.privilege_version` untuk user yang terdampak.

P1 kemudian mendeteksi perubahan versi tersebut dan merotasi session ID pada request berikutnya, tanpa memperpanjang absolute expiration.

Rotasi hanya dilakukan jika memang ada perubahan data. Misalnya, menekan tombol "Simpan" tanpa mengubah data tidak akan memicu rotasi session yang tidak diperlukan.

## Penolakan Akses

| Kondisi | Response |
|---|---|
| Tidak ada `Principal` | `401 Unauthorized` |
| `dbID` pada route tidak valid | `400 Bad Request` |
| Level akses tidak mencukupi | `403 Forbidden` |
| Terjadi error pada `AccessChecker` | `500 Internal Server Error` |

Untuk akses yang ditolak karena level tidak mencukupi, response menggunakan `contract.ErrAccessDenied`.

Pemeriksaan akses dilakukan di backend. UI hanya mengatur tampilan dan tidak dijadikan sebagai mekanisme utama untuk membatasi akses.