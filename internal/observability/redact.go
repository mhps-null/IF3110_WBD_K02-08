package observability

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const RedactedValue = "[REDACTED]"

var sensitiveKeywords = []string{
	"password", "passwd", "secret", "encryption_key",
	"token", "session", "cookie", "authorization",
	"dsn", "database_url", "credential", "api_key",
}

var (
	pgURLPattern    = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:\s/]+:)([^@\s]+)(@)`)
	mysqlDSNPattern = regexp.MustCompile(`([^:\s/]+:)([^@\s]+)(@tcp\([^)]+\))`)
	kvSecretPattern = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|csrf_token|session_id|db_encryption_key|session_secret|authorization)\s*([=:])\s*([^\s,;'"&]+)`)
	bearerPattern   = regexp.MustCompile(`(?i)\b(Bearer\s+)([A-Za-z0-9\-._~+/]+=*)`)
)

// IsSensitiveKey reports whether an attribute key holds confidential data.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "pass" {
		return true
	}
	for _, s := range sensitiveKeywords {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// SanitizeLogString strips CR/LF characters to prevent log forging / injection.
func SanitizeLogString(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// RedactString masks credentials, tokens, and DSN passwords in free-form strings
// and strips CR/LF control characters to prevent log injection.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	out := SanitizeLogString(s)
	out = pgURLPattern.ReplaceAllString(out, "${1}"+RedactedValue+"${3}")
	out = mysqlDSNPattern.ReplaceAllString(out, "${1}"+RedactedValue+"${3}")
	out = kvSecretPattern.ReplaceAllString(out, "${1}${2}"+RedactedValue)
	return bearerPattern.ReplaceAllString(out, "${1}"+RedactedValue)
}

// RedactValue sanitizes a value based on its key and string patterns.
func RedactValue(key string, val any) any {
	if IsSensitiveKey(key) {
		return RedactedValue
	}
	switch v := val.(type) {
	case string:
		return RedactString(v)
	case error:
		if v != nil {
			return RedactString(v.Error())
		}
	case fmt.Stringer:
		if v != nil {
			return RedactString(v.String())
		}
	case []any:
		return RedactParams(v)
	}
	return val
}

// RedactParams sanitizes SQL parameters before logging.
func RedactParams(params []any) []any {
	if len(params) == 0 {
		return nil
	}
	out := make([]any, len(params))
	for i, p := range params {
		out[i] = RedactValue("", p)
	}
	return out
}

// RedactAttr is a slog ReplaceAttr callback that masks sensitive fields.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		cleaned := make([]slog.Attr, len(group))
		for i, ga := range group {
			cleaned[i] = RedactAttr(nil, ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}
	case slog.KindAny:
		return slog.Any(a.Key, RedactValue(a.Key, a.Value.Any()))
	default:
		return a
	}
}
