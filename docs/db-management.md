# P3 — Database Connections, Introspection, and Database Viewer

## Ringkasan Modul

Modul ini bertanggung jawab atas:
1. Pengelolaan profil koneksi database target (`internal/connections`).
2. Pooling koneksi database yang aman dan efisien (`internal/connections/pool.go`).
3. Introspeksi skema database untuk PostgreSQL (`internal/dbdriver/postgres/introspect.go`) serta bonus MySQL/MariaDB (`internal/dbdriver/mysql/introspect.go`).
4. Pembacaan data tabel dengan pembatasan keamanan ketat (`Rows`).
5. Antarmuka web untuk manajemen koneksi admin (`internal/connections/handler.go`) dan database viewer (`internal/viewer/handler.go`, `render.go`).

---

## Pengelolaan Kredensial dan Enkripsi (`internal/connections/crypto.go`, `sql_repository.go`)

- **Algoritma Enkripsi**: Password database target dienkripsi menggunakan **AES-256-GCM** dengan nonce acak 12-byte yang dihasilkan secara kriptografis (`crypto/rand`) per operasi enkripsi.
- **Deteksi Tampering**: Modifikasi atau manipulasi pada ciphertext terdeteksi secara deterministik melalui authenticated tag GCM (`cipher.AEAD.Open`).
- **Kunci Enkripsi (`DB_ENCRYPTION_KEY`)**:
  - Diambil dari environment variable `DB_ENCRYPTION_KEY`.
  - Didukung dalam bentuk string ASCII mentah 32 karakter atau string ter-encode Base64 (baik Standard maupun RawURL) yang hasil decoding-nya tepat sepanjang 32 byte (256-bit).
- **Penyimpanan Terparameterisasi**: Seluruh query SQL pada `SQLRepository` menggunakan parameterized query (`$1`, `$2`, ...) pada PostgreSQL metadata untuk mencegah SQL injection.
- **Pencegahan Penghapusan Koneksi Aktif**: Profil koneksi yang telah memiliki riwayat migrasi pada tabel `migrations` (kolom `db_id`) ditolak penghapusannya dengan error khusus `ErrHasMigrationHistory` (HTTP 409 Conflict).

---

## Pool Manager (`internal/connections/pool.go`)

- **Penyimpanan Cache**: Instance `*sql.DB` disimpan dalam memori dipetakan berdasarkan ID profil koneksi (`uuid.UUID`).
- **Pemilihan Driver Sesuai Engine**:
  - `contract.EnginePostgres`: Memilih driver `pgx` (`github.com/jackc/pgx/v5/stdlib`).
  - `contract.EngineMySQL` & `contract.EngineMariaDB`: Memilih driver `mysql` (`github.com/go-sql-driver/mysql`).
- **Batas Siklus Hidup Pool**:
  - `MaxOpenConns`: 10
  - `MaxIdleConns`: 2
  - `ConnMaxLifetime`: 15 menit
  - `ConnMaxIdleTime`: 5 menit
- **Validasi Koneksi**: Dilakukan `PingContext(ctx)` dengan timeout 5 detik sebelum dimasukkan ke dalam cache pool.
- **Deduplikasi dan Pencegahan Race Condition**:
  - Panggilan konkuren membuka pool yang sama dilindungi oleh mutex dan verifikasi generasi cache (`generation counter`).
  - Jika koneksi di-invalidasi atau di-update saat proses ping berlangsung, instance pool baru yang dibuka langsung ditutup dan tidak dimasukkan ke dalam cache.
  - `Invalidate(id)`: Menutup `*sql.DB` aktif dan menaikkan generasi cache agar panggilan baru membuat koneksi segar.
  - `CloseAll()`: Menutup seluruh pool koneksi aktif dan mencegah pembuatan pool baru setelah manajer ditutup.
- **Isolasi Pengujian**: Mock driver pada unit test terikat secara lokal ke instance test dengan timeout pada pembacaan channel, mencegah kebocoran state global antar-pengujian atau deadlock.

---

## Pembentukan DSN (`internal/connections/dsn.go`)

- **PostgreSQL**: URL DSN dibentuk dengan URL escaping yang aman (`postgres://user:password@host:port/database?sslmode=...`).
- **MySQL / MariaDB**: DSN dibentuk menggunakan konfigurasi terstruktur `mysql.Config` dengan percent-encoding kredensial dan parameter timeout bawaan (`timeout=5s`, `readTimeout=10s`, `writeTimeout=10s`, `parseTime=true`).

---

## Introspeksi Skema Database (`internal/dbdriver/`)

### 1. PostgreSQL (`internal/dbdriver/postgres/introspect.go`)
- **Tabel & Kolom**: Mengambil tabel nyata (`relkind IN ('r', 'p')`) dari `pg_class`, mengecualikan skema katalog sistem (`pg_catalog`, `information_schema`, dll). Tipe data kolom diambil menggunakan `pg_catalog.format_type(atttypid, atttypmod)` untuk mempertahankan modifier panjang/presisi/skala (misalnya `character varying(100)`, `numeric(10,4)`).
- **Constraint**: Menghubungkan kolom melalui OID `pg_constraint` dan array posisi (`conkey`, `confkey`) untuk mencegah perkalian baris Cartesian product. Kolom foreign key lokal dan referensi dipasangkan berdasarkan urutan posisi. Kolom `RefTable` dan `RefColumns` hanya diisi untuk foreign key.
- **Index**: Mengambil definisi index melalui `pg_index` dan `pg_get_indexdef`. Menapis klausa `INCLUDE` dan predikat `WHERE` parsial (`indnkeyatts` vs `indnatts`) agar hanya atribut kunci indeks yang dicatat.

### 2. MySQL / MariaDB (`internal/dbdriver/mysql/introspect.go`)
- **Versi yang Didukung**: MySQL 8.0+ dan MariaDB 10.3+ (termasuk MariaDB 11.8).
- **Penanganan Skema & Isolasi Database**: Menentukan database aktif secara ketat melalui `SELECT DATABASE()`. Permintaan pembacaan lintas database (`schema=...` yang berbeda dengan database aktif) ditolak dengan status `403 Forbidden` (`contract.ErrAccessDenied`).
- **Tipe Kolom**: Diambil dari `information_schema.columns.column_type` untuk mencatat tipe penuh, enum, dan set (misalnya `varchar(100)`, `decimal(10,4)`, `enum('draft','active')`).
- **Constraint & Disambiguasi CHECK**:
  - Kolom gabungan pada PK, FK, dan UNIQUE diurutkan secara ketat sesuai `ordinal_position` dari `information_schema.key_column_usage`.
  - Pasangan kolom lokal dan referensi foreign key dipertahankan posisinya.
  - Foreign key menyimpan nama tabel referensi secara schema-qualified (`schema.table`) untuk mencegah ambiguitas referensi.
  - **Disambiguasi CHECK**: Definisi ekspresi CHECK dipetakan menggunakan kombinasi `schema.table.constraint` dengan fallback `schema.constraint`. Pada MariaDB dan MySQL versi kompatibel, kolom `TABLE_NAME` pada `information_schema.check_constraints` digunakan untuk memastikan ekspresi CHECK tidak tertukar antar-tabel yang memiliki nama constraint sama.
  - Penanganan error scan & query rows ditangani secara ketat (`rows.Err()`). Jika tabel `check_constraints` tidak tersedia pada versi database lama, query dilewati dengan aman tanpa error fatal.
- **Index Biasa & Functional Index**:
  - Mengambil index dari `information_schema.statistics` dengan urutan multi-kolom sesuai `seq_in_index`.
  - Kolom functional index (di mana `column_name` bernilai NULL) ditangani secara aman menggunakan nilai nullable dan ekspresi index diekstrak dari kolom `expression` pada MySQL 8.0.13+.

---

## Pembatasan Pembacaan Data Tabel (`Rows`)

1. **Transaksi Read-Only**: Wajib dimulai dengan `db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` dan selalu di-rollback (`defer tx.Rollback()`) saat fungsi selesai.
2. **Context Timeout**: Operasi dibatasi waktu menyeluruh melalui `context.WithTimeout(ctx, 10*time.Second)`.
3. **Pemberian Statement Timeout Tanpa Mencemari Connection Pool**:
   - Tidak menggunakan `SET SESSION` yang dapat tertinggal pada koneksi pool.
   - **PostgreSQL**: Menggunakan `SET LOCAL statement_timeout = 5000` di dalam transaksi read-only.
   - **MySQL**: Menggunakan query optimizer hint `/*+ MAX_EXECUTION_TIME(5000) */` pada `SELECT COUNT(1)` dan query data.
   - **MariaDB**: Menggunakan sintaks per-statement `SET STATEMENT max_statement_time = 5 FOR SELECT ...` pada `SELECT COUNT(1)` dan query data.
4. **Perlindungan COUNT**: `COUNT(1)` dijalankan di dalam transaksi read-only di bawah perlindungan timeout yang sama sebelum data baris diambil.
5. **Whitelist Kolom**: Nama kolom divalidasi terhadap daftar kolom tabel yang sah sebelum dipakai pada klausa `ORDER BY`. Jika nama kolom tidak sah, query ditolak dengan error.
6. **Paginasi Data**: Halaman dinormalisasi (minimal 1), ukuran halaman dibatasi (1 hingga 100, default 20) menggunakan klausa `LIMIT` dan `OFFSET`.

---

## Antarmuka Web, Template Rendering, dan Keamanan Viewer (`internal/viewer/`, `internal/connections/`)

- **Pemilihan Introspektor Sesuai Engine**: `NewMultiEngineHandler` memilih introspektor PostgreSQL atau MySQL berdasarkan engine profil koneksi yang sedang dibuka.
- **Penyesuaian Default Schema**: Query parameter `schema` otomatis dinormalisasi—skema `"public"` dibersihkan saat membuka database MySQL/MariaDB agar query tidak gagal.
- **Fail-Closed Security Guards**: Jika guard `Admin` (pada connections) atau `ViewDB` (pada viewer) bernilai `nil` atau akses ditolak oleh `AccessChecker`, handler langsung mengembalikan `403 Forbidden` (`Access denied: security guard not configured`).
- **Eksekusi Template Ter-buffer (`HTMLRenderer`)**:
  - `HTMLRenderer` mengeksekusi template HTML ke dalam memory buffer (`bytes.Buffer`) terlebih dahulu sebelum menulis output ke `http.ResponseWriter`.
  - Jika parsing atau eksekusi template gagal, handler mengembalikan error generik `500 Internal Server Error` (`failed to render page`). Hal ini mencegah terkirimnya respon parsial berstatus 200 dengan HTML terpotong.
  - Template helper `deref`, `add`, dan `sub` terdaftar aman dengan auto-escaping konteks `html/template`.
- **Masking Error Internal Database**: Seluruh pesan error driver mentah disamarkan menjadi pesan aman pada respons HTTP (`unable to connect to target database`, `failed to introspect database schema`, `failed to query table data`) untuk mencegah kebocoran informasi kredensial atau skema internal.
- **Kepatuhan CSP**: Seluruh fungsionalitas interaktif (seperti tombol test connection dan dialog konfirmasi) diimplementasikan dalam JavaScript eksternal `web/static/js/connections.js` tanpa inline script atau inline handler (`onclick`, `onsubmit`).

---

## Verifikasi dan Pengujian

### 1. Menjalankan Unit Test (dengan Race Detector)

Seluruh pengujian unit test menggunakan pola table-driven yang ringkas, mock terisolasi, dan tidak saling bergantung.

- **Bash**:
  ```bash
  go test -v -race ./internal/connections/... ./internal/dbdriver/... ./internal/viewer/...
  ```
- **PowerShell (Windows)**:
  ```powershell
  go test -v -race ./internal/connections/... ./internal/dbdriver/... ./internal/viewer/...
  ```

### 2. Pengujian Integrasi Database Nyata (`-tags=integration`)

> [!NOTE]
> Pengujian integrasi memerlukan instance database aktif dan variabel lingkungan terkait (`TEST_DATABASE_URL`, `TEST_MYSQL_URL`, atau `TEST_MARIADB_URL`). Jika variabel lingkungan tidak diset, pengujian integrasi dilewati secara aman (`t.Skip`). Pengujian integrasi mencakup tiga alur wajib:
> 1. **Success Flow**: Verifikasi introspeksi tabel, kolom, constraint, index, dan query data pada skema nyata.
> 2. **Failure Flow**: Verifikasi penanganan error saat koneksi gagal, timeout, atau query tabel non-eksisten.
> 3. **Access Denied Flow**: Verifikasi penolakan akses lintas database/skema (`ErrAccessDenied`).

#### A. PostgreSQL (`internal/dbdriver/postgres/`, `internal/connections/`)

- **Bash**:
  ```bash
  TEST_DATABASE_URL="postgres://user:password@localhost:5432/test_db?sslmode=disable" \
  go test -v -race -tags=integration ./internal/dbdriver/postgres/... ./internal/connections/...
  ```
- **PowerShell**:
  ```powershell
  $env:TEST_DATABASE_URL="postgres://user:password@localhost:5432/test_db?sslmode=disable"
  go test -v -race -tags=integration ./internal/dbdriver/postgres/... ./internal/connections/...
  ```

#### B. MySQL / MariaDB (`internal/dbdriver/mysql/`)

- **Bash (MySQL)**:
  ```bash
  TEST_MYSQL_URL="root:password@tcp(127.0.0.1:3306)/test_db?parseTime=true" \
  go test -v -race -tags=integration ./internal/dbdriver/mysql/...
  ```
- **PowerShell (MySQL)**:
  ```powershell
  $env:TEST_MYSQL_URL="root:password@tcp(127.0.0.1:3306)/test_db?parseTime=true"
  go test -v -race -tags=integration ./internal/dbdriver/mysql/...
  ```
- **Bash / PowerShell (MariaDB 10.3+ / 11.8)**:
  Gunakan variabel `TEST_MARIADB_URL` dengan format DSN yang sama untuk menguji compatibility engine MariaDB:
  ```powershell
  $env:TEST_MARIADB_URL="root:password@tcp(127.0.0.1:3306)/test_db?parseTime=true"
  go test -v -race -tags=integration ./internal/dbdriver/mysql/...
  ```
