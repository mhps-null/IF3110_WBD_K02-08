# Milestone 2 — Orang 1: SPA, Auth/Session JSON API, Group Leader, & Database Viewer

Dokumen ini merangkum arsitektur frontend SPA, kontrak JSON API, aturan Group Leader, serta titik integrasi bagi Orang 2, Orang 3, dan Orang 4 pada branch `feat/m2-p1`.

---

## 1. Keputusan Stack Frontend SPA

* **Framework & Toolchain:** React 18 + TypeScript + Vite 6 + Tailwind CSS 3 (`frontend/`).
* **Development Mode:**
  * Backend Go berjalan di `:8080` (`go run ./cmd/server`).
  * Vite dev server berjalan di `:5173` (`cd frontend && npm run dev`) dengan proxy `/api/*` dan `/healthz` ke `http://localhost:8080` (`changeOrigin: false`) sehingga cookie `HttpOnly` dan CSRF tetap same-origin.
* **Production / Demo Mode:**
  * Jalankan `cd frontend && npm run build` untuk menghasilkan bundle statis di `frontend/dist/`.
  * Server Go (`cmd/server/main.go`) langsung menyajikan SPA melalui `web.NewSPAHandler(cfg.SPADistDir)` (default `frontend/dist`, dapat diubah via env `SPA_DIST_DIR`). Semua rute non-`/api/*` diarahkan ke `index.html`, sedangkan rute `/api/*` yang tidak terdaftar selalu mengembalikan JSON `404`.
* **Keamanan CSP:** Seluruh UI menggunakan kelas utilitas Tailwind tanpa inline script maupun inline style sehingga kompatibel dengan header `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'`.

---

## 2. Migrasi Skema Metadata Tambahan

Jalankan seluruh file di `migrations_meta/` secara berurutan:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/001_initial_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/002_session_privilege_version.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/003_audit_logs.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations_meta/004_group_leader.sql
```

### Aturan Group Leader (`004_group_leader.sql`)
1. Kolom `groups.leader_user_id` bersifat **nullable** (`ON DELETE SET NULL`). Jika suatu group memiliki akses ke database tetapi belum memiliki leader aktif, API mengembalikan `has_leader: false` dan `approval_status: "pending_leader_assignment"` sehingga workflow approval Orang 3 tetap berstatus **Pending** (bukan ditolak), sesuai `QNA-07 #2`.
2. Tidak ada constraint `UNIQUE` pada `leader_user_id` sehingga satu user dapat menjadi leader di lebih dari satu group sekaligus (`QNA-06 #1`).
3. Validasi penetapan leader (`Store.SetGroupLeader`): user target harus ada, berstatus `is_active = TRUE`, dan merupakan anggota group di `group_members`.
4. Mengeluarkan anggota (`Store.RemoveMember`) secara atomik mengosongkan `leader_user_id` jika anggota tersebut sedang menjabat sebagai leader.
5. Setiap perubahan leader menaikkan `users.privilege_version` untuk leader lama dan/atau baru dalam transaksi yang sama agar rotasi session M1 tetap bekerja.

### Perhitungan Effective Permission (Multi-Group)
Sesuai M1 (`internal/access/checker.go`), jika user aktif tergabung dalam beberapa group yang mengakses database yang sama, hak akses efektifnya adalah **level tertinggi** (`MAX(level)`): `View + Edit (2)` mengungguli `View Only (1)`. Platform Admin tidak otomatis mendapat akses data database kecuali menjadi anggota group yang diberi akses.

---

## 3. Peta JSON API Backend

Semua request mutasi (`POST`, `PUT`, `PATCH`, `DELETE`) dilindungi cookie CSRF `HttpOnly; SameSite=Strict` + header `X-CSRF-Token`. Client SPA (`frontend/src/api/client.ts`) mengambil token via `GET /api/auth/csrf` secara otomatis. Session identifier hanya dikirim lewat cookie `HttpOnly; SameSite=Lax` (`Secure` di HTTPS/production) dan tidak pernah muncul pada URL, response body, atau log.

| Domain | Method & Path | Guard | Keterangan |
|---|---|---|---|
| Auth | `GET /api/auth/csrf` | Publik | Mendapatkan CSRF token |
| Auth | `POST /api/auth/login` | Publik + CSRF | Login JSON, set opaque session cookie |
| Auth | `POST /api/auth/logout` | Auth + CSRF | Revoke session & hapus cookie |
| Auth | `GET /api/auth/me` | Auth | Identitas user, daftar group + status leader, database yang diakses, CSRF token |
| Admin Users | `GET/POST /api/admin/users` | Admin | Daftar & buat user |
| Admin Users | `POST /api/admin/users/{id}/{activate\|deactivate\|reset-password\|toggle-admin}` | Admin + CSRF | Kelola status, password, dan hak admin (mencabut session) |
| Admin Groups | `GET/POST /api/admin/groups` | Admin | Daftar (beserta info leader) & buat group |
| Admin Groups | `GET/PUT/DELETE /api/admin/groups/{id}` | Admin | Detail, ubah, hapus group |
| Admin Groups | `POST /api/admin/groups/{id}/members`, `DELETE .../members/{uid}` | Admin + CSRF | Tambah/hapus anggota group |
| Admin Groups | `PUT /api/admin/groups/{id}/leader` | Admin + CSRF | Tetapkan/ganti/kosongkan leader group |
| Admin Access | `GET/PUT /api/admin/access` | Admin | Matriks akses group ↔ database + status leader |
| Approvers | `GET /api/databases/{id}/access-groups` | Auth + (Admin / ViewDB) | Daftar seluruh group yang punya akses ke DB + leader-nya + deteksi group tanpa leader |
| Connections | `GET/POST /api/admin/connections`, `GET/PUT/DELETE .../{id}` | Admin | CRUD koneksi database (password terenkripsi AES-256-GCM, tidak pernah dikembalikan di JSON) |
| Connections | `POST /api/admin/connections/test` | Admin + CSRF | Uji konektivitas target DB |
| Connections | `GET /api/admin/connections/pool-stats` | Admin | Statistik connection pool live (`PoolStatsProvider`) |
| Viewer | `GET /api/databases` | Auth | Daftar database yang dapat diakses user |
| Viewer | `GET /api/databases/{id}/schema` | Auth + ViewDB | Introspeksi skema/tabel/kolom/constraint/index (tercatat di application log) |
| Viewer | `GET /api/databases/{id}/tables/{table}` | Auth + ViewDB | Detail struktur tabel |
| Viewer | `GET /api/databases/{id}/tables/{table}/data` | Auth + ViewDB | Data tabel terpaginasi dalam transaksi `READ ONLY` + statement timeout (tercatat di SQL log) |
| Audit (P2) | `GET /api/audit-logs` | Admin | Pencarian & paginasi audit log (wired dari `internal/audit`) |
| Migrations (M1 bridge) | `GET /api/databases/{id}/migrations`, `.../{mid}` | Auth + ViewDB | Read API riwayat M1 sambil menunggu modul M2 Orang 3 |

---

## 4. Kontrak Integrasi untuk Anggota Lain

* **Untuk Orang 2 (Observability & Prometheus):**
  * `connections.PoolManager` mengimplementasikan `contract.PoolStatsProvider` (`PoolStats() []contract.PoolStat`) dan mendukung `SetMetrics(observability.MetricsRecorder)` sehingga exporter Prometheus dapat langsung membaca `InUse`, `Idle`, `WaitCount`, dan `WaitDuration`.
  * `cmd/server/main.go` sudah mengaktifkan `observability.Middleware(tracer, obsLogger)` dan `audit.Handler` (`GET /api/audit-logs`), serta halaman SPA `/audit-logs` sudah terhubung langsung.
* **Untuk Orang 3 (Migration Lifecycle & Approval):**
  * Gunakan `contract.GroupAccessInspector` (`internal/access.Checker`) di backend Go: `ListGroupsForDatabase(ctx, dbID)` mengembalikan semua group dengan akses `View Only` maupun `View + Edit` beserta `LeaderUserID`, `LeaderIsActive`, dan `HasLeader`.
  * Di frontend SPA, sambungkan editor migration, review inbox, dan history ke `frontend/src/pages/MigrationsShellPage.tsx` dan `frontend/src/pages/ApprovalsInboxPage.tsx` dengan `apiRequest()` dari `frontend/src/api/client.ts`.
* **Untuk Orang 4 (Queue, Worker, Batch, Job Monitoring):**
  * Di frontend SPA, sambungkan job monitoring dan progress bar ke `frontend/src/pages/JobsMonitorPage.tsx` menggunakan `apiRequest()` dan shared components (`LoadingState`, `EmptyState`, `ErrorState`, `Badge`, `ConfirmDialog`, `useToast`).
