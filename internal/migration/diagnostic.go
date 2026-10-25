package migration

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// ErrorDiagnostic represents sanitized, structured diagnostics for migration failures.
type ErrorDiagnostic struct {
	Category        string
	CauseMessage    string
	Suggestion      string
	SQLState        string
	ErrorCode       string
	StatementNumber int
	SanitizedDetail string
	RollbackStatus  string
	RollbackNote    string
}

var (
	sensitiveSecretRegex = regexp.MustCompile(`(?i)(password|pwd|secret|token)\s*=\s*[^\s;]+`)
	credentialInURI      = regexp.MustCompile(`:[^:@/]+@`)
	serverPathRegex      = regexp.MustCompile(`/(?:home|root|tmp|var|etc|usr|opt)/[a-zA-Z0-9_\-\./]+`)
	mysqlStatePattern    = regexp.MustCompile(`(?i)Error\s+(\d+)\s*\(([0-9A-Z]{5})\)`)
	sqlStateExplicit     = regexp.MustCompile(`(?i)\b(?:SQLSTATE|code:)\s*([0-9A-Z]{5})\b`)
)

// SanitizeErrorText removes server filesystem paths, database credentials, and internal traces.
func SanitizeErrorText(raw string) string {
	if raw == "" {
		return ""
	}
	cleaned := sensitiveSecretRegex.ReplaceAllString(raw, "$1=REDACTED")
	cleaned = credentialInURI.ReplaceAllString(cleaned, ":REDACTED@")
	cleaned = serverPathRegex.ReplaceAllString(cleaned, "[internal_path]")
	cleaned = strings.TrimPrefix(cleaned, "pq: ")

	// If there's an internal line/file detail (common in driver stack), strip it
	if idx := strings.Index(cleaned, "\n"); idx != -1 {
		cleaned = cleaned[:idx]
	}

	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > 260 {
		cleaned = cleaned[:260] + "…"
	}
	return cleaned
}

// DiagnoseError generates safe, structured diagnostics for migration failures.
func DiagnoseError(err error, rawMsg *string, engine contract.Engine) *ErrorDiagnostic {
	if err == nil && (rawMsg == nil || *rawMsg == "") {
		return nil
	}

	var fullMsg string
	if err != nil {
		fullMsg = err.Error()
	} else if rawMsg != nil {
		fullMsg = *rawMsg
	}

	diag := &ErrorDiagnostic{
		StatementNumber: 1,
		RollbackStatus:  "Rollback Berhasil (Atomik)",
		RollbackNote:    "Transaksi dibatalkan secara otomatis (ROLLBACK). Tidak ada struktur atau baris data parsial yang tersimpan pada database target.",
	}

	// 1. Inspect driver error instances
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		diag.SQLState = pgErr.SQLState()
		diag.ErrorCode = pgErr.Code
		if pgErr.Message != "" {
			diag.SanitizedDetail = SanitizeErrorText(pgErr.Message)
		}
	}

	var myErr *mysqldriver.MySQLError
	if errors.As(err, &myErr) {
		diag.SQLState = string(myErr.SQLState[:])
		diag.ErrorCode = fmt.Sprintf("%d", myErr.Number)
		if myErr.Message != "" {
			diag.SanitizedDetail = SanitizeErrorText(myErr.Message)
		}
	}

	// 2. Fallback parsing from stored message string
	if diag.SQLState == "" {
		diag.SQLState, diag.ErrorCode = extractSQLState(fullMsg)
	}
	if diag.SanitizedDetail == "" {
		diag.SanitizedDetail = SanitizeErrorText(fullMsg)
	}

	// 3. Engine-specific rollback status check
	lower := strings.ToLower(fullMsg)
	if engine == contract.EngineMySQL || engine == contract.EngineMariaDB {
		if strings.Contains(lower, "implicit commit") {
			diag.RollbackStatus = "Implicit Commit oleh DBMS"
			diag.RollbackNote = "Statement DDL pada MySQL/MariaDB melakukan auto-commit di tingkat engine sehingga tidak dapat dibatalkan dengan rollback transaksional."
		}
	}

	// 4. Categorize cause and suggestion
	categorizeDiagnostic(diag, lower)

	return diag
}

func extractSQLState(msg string) (string, string) {
	if m := mysqlStatePattern.FindStringSubmatch(msg); len(m) == 3 {
		return m[2], m[1]
	}
	if m := sqlStateExplicit.FindStringSubmatch(msg); len(m) == 2 {
		return m[1], ""
	}

	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "syntax error"):
		return "42601", "42601"
	case strings.Contains(lower, "does not exist") && strings.Contains(lower, "relation"):
		return "42P01", "42P01"
	case strings.Contains(lower, "doesn't exist") && strings.Contains(lower, "table"):
		return "42S02", "1146"
	case strings.Contains(lower, "does not exist") && strings.Contains(lower, "column"):
		return "42703", "42703"
	case strings.Contains(lower, "unknown column"):
		return "42S22", "1054"
	case strings.Contains(lower, "violates unique constraint") || strings.Contains(lower, "duplicate key"):
		return "23505", "23505"
	case strings.Contains(lower, "duplicate entry"):
		return "23000", "1062"
	case strings.Contains(lower, "violates foreign key constraint") || strings.Contains(lower, "foreign key"):
		return "23503", "23503"
	case strings.Contains(lower, "violates check constraint") || strings.Contains(lower, "check constraint"):
		return "23514", "23514"
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "access denied"):
		return "42501", "42501"
	case strings.Contains(lower, "cannot run inside a transactional"):
		return "25001", "25001"
	default:
		return "—", ""
	}
}

func categorizeDiagnostic(diag *ErrorDiagnostic, lower string) {
	switch {
	case diag.SQLState == "42601" || diag.SQLState == "42000" || diag.ErrorCode == "1064" || strings.Contains(lower, "syntax error") || strings.Contains(lower, "syntax"):
		diag.Category = "Kesalahan Sintaks SQL (Syntax Error)"
		diag.CauseMessage = "Format penulisan perintah SQL tidak valid, terdapat kata kunci yang salah tempat, atau tanda pemisah statement (seperti tanda titik koma ';') belum lengkap."
		diag.Suggestion = "Periksa kembali urutan penulisan query, kecocokan tanda kurung, dan pastikan setiap perintah ditutup dengan tanda titik koma (;)."

	case diag.SQLState == "42P01" || diag.SQLState == "42S02" || diag.ErrorCode == "1146" || (strings.Contains(lower, "exist") && (strings.Contains(lower, "table") || strings.Contains(lower, "relation"))):
		diag.Category = "Tabel Tidak Ditemukan (Undefined Table)"
		diag.CauseMessage = "Tabel atau relasi yang dirujuk dalam query tidak ditemukan pada database yang sedang aktif."
		diag.Suggestion = "Pastikan nama tabel dieja dengan benar dan tabel tersebut memang berada di dalam skema database yang dipilih pada menu Schema."

	case diag.SQLState == "42703" || diag.SQLState == "42S22" || diag.ErrorCode == "1054" || strings.Contains(lower, "unknown column") || (strings.Contains(lower, "column") && strings.Contains(lower, "does not exist")):
		diag.Category = "Kolom Tidak Ditemukan (Undefined Column)"
		diag.CauseMessage = "Nama kolom yang dirujuk pada query tidak terdaftar pada tabel target."
		diag.Suggestion = "Buka tab Schema dan periksa daftar kolom tabel target untuk memastikan penulisan nama kolom sudah sesuai."

	case diag.SQLState == "23505" || diag.ErrorCode == "1062" || strings.Contains(lower, "unique") || strings.Contains(lower, "duplicate"):
		diag.Category = "Pelanggaran Batasan Unik (Unique Violation)"
		diag.CauseMessage = "Data yang dicoba disimpan memiliki nilai yang sudah digunakan pada kolom dengan constraint UNIQUE atau PRIMARY KEY."
		diag.Suggestion = "Gunakan nilai identifier/kunci yang berbeda atau gunakan perintah UPDATE untuk memperbarui baris yang sudah ada."

	case diag.SQLState == "23503" || diag.ErrorCode == "1451" || diag.ErrorCode == "1452" || strings.Contains(lower, "foreign key"):
		diag.Category = "Pelanggaran Foreign Key (Integrity Constraint)"
		diag.CauseMessage = "Nilai referensi tidak ditemukan di tabel induk, atau baris tidak dapat dihapus karena masih dirujuk oleh tabel anak."
		diag.Suggestion = "Pastikan record pada tabel referensi (induk) telah dibuat sebelum menambahkan data pada tabel turunan."

	case diag.SQLState == "23514" || diag.ErrorCode == "3819" || strings.Contains(lower, "check constraint"):
		diag.Category = "Pelanggaran Validasi Kolom (Check Constraint)"
		diag.CauseMessage = "Nilai data yang dimasukkan melanggar aturan validasi Check Constraint (misalnya nilai bernilai negatif atau di luar rentang yang diizinkan)."
		diag.Suggestion = "Periksa aturan batasan nilai kolom pada struktur tabel dan sesuaikan nilai data yang diinput."

	case diag.SQLState == "42501" || diag.ErrorCode == "1044" || diag.ErrorCode == "1045" || strings.Contains(lower, "permission denied") || strings.Contains(lower, "access denied"):
		diag.Category = "Hak Akses Ditolak (Permission Denied)"
		diag.CauseMessage = "User koneksi database target tidak memiliki izin yang cukup untuk menjalankan perintah DDL/DML ini."
		diag.Suggestion = "Periksa konfigurasi hak akses pengguna basis data target di server DBMS."

	case diag.SQLState == "25001" || strings.Contains(lower, "transactional"):
		diag.Category = "Statement Non-Transaksional"
		diag.CauseMessage = "Pernyataan SQL yang dicoba dijalankan tidak didukung di dalam blok transaksi terkelola (seperti VACUUM atau CREATE INDEX CONCURRENTLY)."
		diag.Suggestion = "Gunakan perintah DDL standar yang mendukung eksekusi transaksional atomik."

	default:
		diag.Category = "Kegagalan Eksekusi Database"
		diag.CauseMessage = "Server database target menolak eksekusi perintah SQL atau terjadi kegagalan operasional."
		diag.Suggestion = "Tinjau kembali teks SQL migration dan uji coba query terlebih dahulu menggunakan fitur 'Analisis execution plan'."
	}
}
