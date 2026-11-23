# Security: SAST dengan CodeQL (Milestone 2 Bonus)

Dokumen ini mencatat konfigurasi Static Application Security Testing (SAST) menggunakan **GitHub CodeQL**, hasil scan awal, analisis setiap temuan (rule, severity, lokasi, potensi dampak, klasifikasi, dan keputusan), referensi commit perbaikan, serta hasil scan ulang sesuai spesifikasi Milestone 2.

---

## 1. Konfigurasi Workflow CodeQL

Berkas workflow: [`.github/workflows/codeql.yml`](../.github/workflows/codeql.yml)

* **Trigger:** `push` (`main`, `feat/**`), `pull_request` (`main`, `feat/**`), `schedule` mingguan, dan `workflow_dispatch`.
* **Action Version:** `github/codeql-action/init@v4` dan `github/codeql-action/analyze@v4`.
* **Bahasa & Query Suite:**
  * **`go`** (`build-mode: autobuild`, query suite `+security-extended`) — mencakup `cmd/` dan seluruh `internal/`.
  * **`javascript-typescript`** (`build-mode: none`, query suite `+security-extended`) — mencakup seluruh SPA React + TypeScript di `frontend/src/`.

---

## 2. Hasil Scan Awal (Baseline Scan #1)

* **Branch:** `feat/m2-p1`
* **Commit Scan Awal:** `1fb9fad216cd068d76f00317e98591c454686116` (`1fb9fad`)
* **Workflow Run:** `CodeQL SAST #1` (`Analyze (go)` ✓, `Analyze (javascript-typescript)` ✓, durasi `1m 4s`)
* **Ringkasan Temuan Awal:**
  * **Go:** **4 Open** (`2 High`, `2 Medium`)
  * **JavaScript/TypeScript (`frontend/src`):** **0 Open**

---

## 3. Daftar Temuan, Analisis Dampak, & Keputusan Tindak Lanjut

| Alert # | Bahasa | Rule ID | Severity | Lokasi (File:Baris) | Potensi Dampak | Klasifikasi | Keputusan & Alasan Perbaikan | Commit Fix | Status Rescan |
|---|---|---|---|---|---|---|---|---|---|
| #1 | Go | `go/sql-injection` (*Database query built from user-controlled sources*) | **High** | `internal/dbdriver/postgres/introspect.go:613` | `QualifyTable(schema, table)` pada query `SELECT COUNT(1) FROM ...` masih menerima string `schema` & `table` langsung dari parameter HTTP (walau eksistensi kolom sudah dicek di `pg_attribute`) dan belum mengecualikan schema sistem (`pg_catalog`, `information_schema`). | **Valid (Defense-in-Depth)** | **Diperbaiki:** `colQuery` kini juga membaca `ns.nspname` dan `cls.relname` dari `pg_catalog` dengan filter `ns.nspname NOT IN ('pg_catalog', 'information_schema') AND cls.relkind IN ('r', 'p')`, lalu `QualifyTable` hanya dibangun dari identifier hasil katalog DB (bukan input HTTP mentah). | `3b0bc21` | **Closed (Resolved)** |
| #2 | Go | `go/sql-injection` (*Database query built from user-controlled sources*) | **High** | `internal/dbdriver/postgres/introspect.go:631` | Query paginasi `SELECT ... FROM ... ORDER BY ...` memakai `qualifiedTable` yang sama dari input request mentah. | **Valid (Defense-in-Depth)** | **Diperbaiki:** Sama dengan #1 — seluruh identifier tabel dan kolom pada `dataQuery` kini 100% berasal dari `pg_catalog` (`resolvedSchema`, `resolvedTable`, `validColumns`, `orderCol`). | `3b0bc21` | **Closed (Resolved)** |
| #3 | Go | `go/log-injection` (*Log entries created from user input*) | **Medium** | `internal/observability/logger.go:69` | Atribut HTTP seperti `r.URL.Path`, `r.Method`, atau header `X-Correlation-ID` diteruskan ke `LogActivity` (`slog.LogAttrs`) tanpa pembersihan karakter baris baru (`\r`/`\n`), berpotensi memalsukan baris log (log forging) pada parser log berbasis baris. | **Valid** | **Diperbaiki:** Menambahkan `SanitizeLogString` (menghapus `\r` dan `\n`) di `internal/observability/redact.go`, menerapkannya pada `Middleware` (`trace.go`) serta seluruh atribut string di `LogActivity` dan `baseAttrs` (`logger.go`), dan menambahkan unit test `TestLogInjectionSanitization`. | `3b0bc21` | **Closed (Resolved)** |
| #4 | Go | `go/log-injection` (*Log entries created from user input*) | **Medium** | `internal/observability/logger.go:150` | Nama tabel, schema, dan kolom `order_by` dari query viewer masuk ke `emitSQL` (`LogSQLStart`/`LogSQLResult`) tanpa pembersihan `\r`/`\n`. | **Valid** | **Diperbaiki:** Menerapkan `SanitizeLogString` pada parameter viewer (`internal/viewer/api.go`) dan di dalam `emitSQL` / `RedactString` (`internal/observability/`). | `3b0bc21` | **Closed (Resolved)** |

---

## 4. Hasil Scan Ulang (Rescan #2)

* **Branch:** `feat/m2-p1`
* **Commit Perbaikan:** `3b0bc2159949b11961fdabcd4030ebdd55f65d06` (`3b0bc21` — `fix(security): remediate CodeQL sql-injection and log-injection alerts`)
* **Hasil Rescan (`is:open branch:feat/m2-p1`):** **`0 Open` · `4 Closed`** (seluruh 2 alert High dan 2 alert Medium terverifikasi selesai oleh GitHub CodeQL).
* **Verifikasi Regresi Lokal:** `go vet ./...`, `go build ./...`, `go test -race` (`postgres`, `observability`, `viewer`, `audit`), dan `TestPostgresIntrospectIntegration` seluruhnya lulus.

### Catatan Keterbatasan SAST
Cakupan analisis mencakup seluruh kode Go (`cmd/`, `internal/`) dan frontend TypeScript/React (`frontend/src/`). **Hasil scan CodeQL yang bersih (`0 Open`) tidak menjamin aplikasi pasti bebas dari kerentanan keamanan**; SAST tetap harus dilengkapi dengan enforcement otorisasi backend, proteksi CSRF/session cookie, unit/integration test (`go test -race`), dan peninjauan kode manual.
