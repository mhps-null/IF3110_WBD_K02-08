# PANDUAN DOCKER DATABASE TESTING MANUAL IF3110

Lingkungan Docker ini dirancang agar Anda dapat langsung melakukan pengujian manual (*manual testing*) terhadap seluruh fitur aplikasi dan bonus tanpa perlu mengonfigurasi database baru dari awal.

---

## 1. Menjalankan Docker Database

Dari root repositori, jalankan perintah berikut:

```bash
# 1. Jalankan container PostgreSQL & MySQL
./docker/start-test-dbs.sh

# 2. Otomatis daftarkan koneksi & beri akses ke Platform Web
set -a; source .env; set +a
./docker/register-connections.sh
```

> **Catatan Port**: Container berjalan di port host **5433** (PostgreSQL), **3307** (MySQL), dan **3308** (MariaDB) agar tidak bentrok dengan PostgreSQL lokal (5432) atau MySQL lokal (3306) Anda.

---

## 2. Kredensial Database Testing yang Tersedia

Jika Anda ingin mendaftarkan atau mengecek koneksi secara manual melalui menu **Admin > Kelola Koneksi** (`http://localhost:8080/admin/connections`), gunakan data berikut:

### Database 1: PostgreSQL E-Commerce (Source)
- **Nama Profil**: `Docker PostgreSQL E-Commerce (Source)`
- **Engine**: `PostgreSQL`
- **Host**: `localhost`
- **Port**: `5433`
- **Database**: `test_ecommerce_pg`
- **Username**: `test_user`
- **Password**: `test_password`
- **SSL Mode**: `disable`

### Database 2: PostgreSQL E-Commerce Target (Untuk Schema Diff)
- **Nama Profil**: `Docker PostgreSQL E-Commerce (Target Diff)`
- **Engine**: `PostgreSQL`
- **Host**: `localhost`
- **Port**: `5433`
- **Database**: `test_ecommerce_pg_target`
- **Username**: `test_user`
- **Password**: `test_password`
- **SSL Mode**: `disable`

### Database 3: MySQL Inventory (Bonus Multi-DBMS)
- **Nama Profil**: `Docker MySQL Inventory`
- **Engine**: `MySQL`
- **Host**: `localhost`
- **Port**: `3307`
- **Database**: `test_inventory_mysql`
- **Username**: `test_user`
- **Password**: `test_password`
- **SSL Mode**: `disable`

### Database 4: MariaDB Human Resources (Bonus Multi-DBMS)
- **Nama Profil**: `Docker MariaDB Human Resources`
- **Engine**: `MariaDB`
- **Host**: `localhost`
- **Port**: `3308`
- **Database**: `test_hr_mariadb`
- **Username**: `test_user`
- **Password**: `test_password`
- **SSL Mode**: `disable`

---

## 3. Skenario Pengujian Manual (Step-by-Step Test Scenarios)

Buka browser Anda dan akses: **`http://localhost:8080`**  
Masuk menggunakan akun Platform Admin:
- **Username**: `admin`
- **Password**: `1234567891011121314`

---

### Skenario A: Pengujian Database Viewer & Multi-Schema (PostgreSQL)
1. Buka menu **Daftar Database** (`/databases`).
2. Pilih database **`Docker PostgreSQL E-Commerce (Source)`** dan klik **Lihat Schema**.
3. **Verifikasi Skema & Tabel**:
   - Skema `public`: Periksa tabel `customers`, `products`, `orders`, `order_items`.
   - Skema `analytics`: Periksa tabel `user_metrics`.
4. **Verifikasi Kolom, Constraint & Index**:
   - Buka tabel `orders`. Anda akan melihat:
     - Tipe kolom: `numeric(12,2)`, `varchar(30)`, `timestamptz`.
     - Constraint: Primary Key, Foreign Key ke `customers(id)` (`ON DELETE CASCADE`), Check Constraint `total_amount >= 0`.
     - Index: B-Tree biasa `idx_orders_customer` dan Partial Index `idx_orders_completed WHERE status = 'completed'`.
5. **Verifikasi Pembacaan Data & Paginasi**:
   - Buka **Lihat Data** pada tabel `customers` atau `products`.
   - Coba ubah nomor halaman (*page*) atau ukuran halaman (*page size*).
   - Klik header kolom untuk mencoba fitur *sorting* (`order_by`).

---

### Skenario B: Pengujian Schema Diff & Auto-Generate DDL (Bonus 2)
1. Buka menu **Bandingkan Schema** pada database `Docker PostgreSQL E-Commerce (Source)`.
2. Pada dropdown **Database Target**, pilih:  
   **`Docker PostgreSQL E-Commerce (Target Diff)`**.
3. Klik tombol **Bandingkan**.
4. **Verifikasi Diff yang Ditemukan**:
   - Sistem akan mendeteksi perbedaan secara otomatis:
     - Tabel baru: `reviews` (beserta FK ke `customers` dan `products`).
     - Kolom baru pada `customers`: `loyalty_points integer`.
     - Kolom baru pada `products`: `discount_rate numeric(5,2)`.
     - Kolom baru pada `orders`: `shipping_address text`.
     - Index baru: `idx_reviews_product`.
5. **Verifikasi Kandidat SQL**:
   - DDL yang dihasilkan telah diurutkan sesuai dependensi.
   - Klik **Jadikan Draft Migration**: query otomatis diprefill ke editor migrasi tanpa langsung dijalankan! Anda dapat meninjau dan mengedit SQL tersebut sebelum eksekusi.

---

### Skenario C: Pengujian Multi-DBMS MySQL & MariaDB (Bonus 1)
1. **Pengujian MySQL**:
   - Buka menu **Daftar Database** dan pilih **`Docker MySQL Inventory`**.
   - Klik **Lihat Schema**: Periksa tabel `suppliers`, `warehouses`, `inventory`, tipe `ENUM('active','inactive','discontinued')`, auto_increment, dan foreign key.
   - Klik **Lihat Data** pada tabel `inventory`: Data tampil dengan benar dengan batasan query timeout MySQL.
2. **Pengujian MariaDB**:
   - Buka menu **Daftar Database** dan pilih **`Docker MariaDB Human Resources`**.
   - Klik **Lihat Schema**:
     - Periksa tabel `departments`, `positions`, `employees`, `performance_reviews`.
     - Periksa kolom `employment_type` bertipe `ENUM('permanent','contract','intern')`, `salary DECIMAL(12,2)`, dan Check Constraint `salary > 0` serta `score >= 1 AND score <= 100`.
   - Klik **Lihat Data** pada tabel `employees`:
     - Verifikasi data pegawai muncul lengkap dengan NIP, email, gaji, dan status aktif.
     - Coba pengurutan (*order by salary* atau *hire_date*).

---

### Skenario D: Pengujian Query Execution Plan (EXPLAIN & EXPLAIN ANALYZE) (Bonus 3)
1. Masuk ke halaman **Riwayat Migration** pada database PostgreSQL, MySQL, atau MariaDB (misal: `Docker MariaDB Human Resources`).
2. Klik **Buat Migration Baru**.
3. Buat draft migrasi dengan SQL DML:
   - Untuk PostgreSQL:
     ```sql
     INSERT INTO products (sku, title, price, stock, category)
     VALUES ('SKU-TEST-EXPLAIN', 'Mouse Gaming RGB', 450000.00, 20, 'Aksesoris');
     ```
   - Untuk MariaDB:
     ```sql
     INSERT INTO performance_reviews (employee_id, review_period, score, notes, reviewed_at)
     VALUES (1, '2026-H2', 96, 'Exceeded annual engineering goals', NOW());
     ```
4. Simpan sebagai Draft.
5. Pada halaman detail migrasi, klik tombol **Analisis Plan (EXPLAIN)**:
   - Sistem menampilkan pohon eksekusi: jenis scan, estimated startup & total cost, serta planned rows.
6. Coba centang **Jalankan EXPLAIN ANALYZE**:
   - Sistem menampilkan *actual execution time* dan *actual rows*.
   - **Jaminan Keamanan**: Karena dibungkus dalam transaksi yang di-rollback otomatis, baris produk baru tersebut **tidak tersimpan** di tabel sampai tombol **Eksekusi Migration** ditekan!
7. Klik **Eksekusi Migration**:
   - Status berubah menjadi `success`, versi naik linear, dan data resmi tersimpan di database.

---

### Skenario E: Pengujian Transactional Rollback pada Migrasi Gagal
1. Klik **Buat Migration Baru**.
2. Masukkan SQL yang melanggar constraint, misalnya harga bernilai negatif pada tabel `products`:
   ```sql
   INSERT INTO products (sku, title, price, stock, category)
   VALUES ('SKU-FAIL', 'Barang Rusak', -50000.00, 10, 'Aksesoris');
   ```
3. Simpan dan klik **Eksekusi Migration**:
   - Eksekusi akan gagal karena melanggar constraint `price > 0`.
   - **Verifikasi**: Status tercatat `failed`, pesan kesalahan lengkap dari database ditampilkan, transaksi di-rollback secara atomik (tidak ada data setengah jadi), dan database tetap bersih.

---

## 4. Menghentikan Container

Jika sesi pengujian manual telah selesai, Anda dapat menghentikan container dengan:

```bash
./docker/stop-test-dbs.sh
```

Untuk membersihkan data dan container sepenuhnya:
```bash
docker compose -f docker/docker-compose.yml down -v
```
