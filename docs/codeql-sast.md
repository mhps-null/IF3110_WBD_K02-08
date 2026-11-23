# Security: SAST dengan CodeQL (Milestone 2 Bonus)

Dokumen ini mencatat konfigurasi Static Application Security Testing (SAST) menggunakan **GitHub CodeQL**, cakupan kode yang dianalisis, serta tabel pelacakan temuan, keputusan tindak lanjut, commit perbaikan, dan hasil scan ulang sesuai spesifikasi Milestone 2.

---

## 1. Konfigurasi Workflow CodeQL

Berkas workflow: [`.github/workflows/codeql.yml`](../.github/workflows/codeql.yml)

* **Trigger:**
  * `push` pada branch `main` dan `feat/**`
  * `pull_request` ke branch `main` dan `feat/**`
  * `schedule` mingguan (Senin 02:30 UTC)
  * `workflow_dispatch` untuk memicu scan manual kapan saja dari tab **Actions** GitHub
* **Bahasa & Query Suite yang Dianalisis:**
  * **`go`** (`build-mode: autobuild`, query suite `+security-extended`) — mencakup `cmd/` dan seluruh paket `internal/` (auth, session, CSRF, access control, admin, connections, dbdriver, viewer, migration, observability, audit, web).
  * **`javascript-typescript`** (`build-mode: none`, query suite `+security-extended`) — mencakup seluruh kode React + TypeScript SPA di `frontend/src/`.

### Cara Menjalankan & Mengambil Bukti Scan di GitHub
1. Push branch `feat/m2-p1` (atau buat PR) ke GitHub, atau buka tab **Actions → CodeQL SAST → Run workflow**.
2. Setelah workflow selesai, buka tab **Security → Code scanning** pada repository GitHub.
3. Ambil screenshot/ringkasan hasil scan awal dan isi tabel temuan di bawah ini. Jika ada temuan valid, perbaiki, catat SHA commit perbaikan, push ulang, dan verifikasi hasil rescan.

---

## 2. Hardening Proaktif Sebelum Scan Pertama

Beberapa titik kerentanan umum OWASP Top 10 / CodeQL yang sudah diaudit dan diamankan saat implementasi Orang 1:

* **Path Traversal (`go/path-injection`):** `internal/web/spa.go` menggunakan `os.DirFS` + `fs.ValidPath` sehingga permintaan asset statis SPA tidak pernah bisa keluar dari direktori `frontend/dist`.
* **SQL Injection (`go/sql-injection`):** Seluruh query baru di `internal/access/checker.go` dan `internal/admin/store.go` menggunakan parameterized query (`$1`, `$2`, ...).
* **Sensitive Data Exposure (`go/clear-text-logging`):** `Connection.PasswordEnc` ditandai `json:"-"`, session cookie menggunakan `HttpOnly` + `SameSite=Lax` + `Secure` (production), `internal/observability.RedactAttr` menyamarkan field sensitif, dan login/auth API tidak pernah menuliskan password, hash, maupun raw session ID ke response body, URL, atau log.
* **CSRF & XSS:** Mutasi JSON API tetap divalidasi dengan cookie CSRF `HttpOnly; SameSite=Strict` + header `X-CSRF-Token`, React melakukan auto-escaping default (tanpa `dangerouslySetInnerHTML`), dan header `Content-Security-Policy` menolak inline script (`script-src 'self'`).

---

## 3. Log Temuan Scan CodeQL & Keputusan Tindak Lanjut

> **Instruksi pengisian setelah push ke GitHub Actions:** Isi setiap temuan dari tab **Security → Code scanning** (baik scan awal maupun setelah rescan).

| # | Bahasa | Rule ID | Severity | Lokasi (File:Baris) | Potensi Dampak | Klasifikasi | Keputusan & Alasan | Commit Fix | Status Rescan |
|---|---|---|---|---|---|---|---|---|---|
| 1 | *(isi setelah run GitHub Actions)* | — | — | — | — | — | — | — | — |

### Bukti Cakupan & Catatan Keterbatasan SAST
* **Cakupan kode:** Backend Go (`cmd/`, `internal/`) dan Frontend TypeScript/React (`frontend/src/`).
* **Catatan penting:** Hasil scan CodeQL yang bersih **tidak menjamin aplikasi pasti bebas dari kerentanan keamanan**. CodeQL adalah analisis statis (SAST) yang harus tetap dilengkapi dengan pemeriksaan otorisasi runtime, automated testing (`go test -race`, integration test), code review, serta pengujian konfigurasi deployment.
