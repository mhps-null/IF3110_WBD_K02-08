package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	flashCookieName = "flash_notice"
	flashMaxAge     = 2 * time.Minute
	maxFlashMessage = 512
)

// FlashManager carries a short-lived, signed notice across a redirect without
// exposing message text in the URL or trusting client-controlled cookie values.
type FlashManager struct {
	key    []byte
	secure bool
}

func NewFlashManager(secret []byte, secure bool) (*FlashManager, error) {
	if len(secret) < 32 {
		return nil, errors.New("flash signing secret must be at least 32 bytes")
	}
	return &FlashManager{key: append([]byte(nil), secret...), secure: secure}, nil
}

// Set stores an allow-listed message category and message in a signed cookie.
// Callers should pass fixed application messages, never raw driver/user errors.
func (m *FlashManager) Set(w http.ResponseWriter, kind, message string) {
	if m == nil || strings.TrimSpace(message) == "" || len(message) > maxFlashMessage {
		return
	}
	if !validFlashKind(kind) {
		kind = "info"
	}
	value := m.sign(kind, message, time.Now().UTC())
	http.SetCookie(w, &http.Cookie{
		Name: flashCookieName, Value: value, Path: "/",
		MaxAge: int(flashMaxAge.Seconds()), Expires: time.Now().Add(flashMaxAge),
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

// Consume validates the message and expires the cookie, even if it is invalid.
// The caller should copy the returned values into the page data for this response.
func (m *FlashManager) Consume(w http.ResponseWriter, r *http.Request) (message, kind string) {
	if m == nil {
		return "", ""
	}
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return "", ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookieName, Value: "", Path: "/", MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return m.verify(cookie.Value)
}

func (m *FlashManager) sign(kind, message string, issuedAt time.Time) string {
	payload := fmt.Sprintf("%d\n%s\n%s", issuedAt.Unix(), kind, message)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("if3110-flash:" + encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature
}

func (m *FlashManager) verify(value string) (message, kind string) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("if3110-flash:" + parts[0]))
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return "", ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ""
	}
	fields := strings.SplitN(string(decoded), "\n", 3)
	if len(fields) != 3 {
		return "", ""
	}
	issuedUnix, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || !validFlashKind(fields[1]) || fields[2] == "" || len(fields[2]) > maxFlashMessage {
		return "", ""
	}
	age := time.Since(time.Unix(issuedUnix, 0))
	if age < -5*time.Second || age > flashMaxAge {
		return "", ""
	}
	return fields[2], fields[1]
}

func validFlashKind(kind string) bool {
	switch kind {
	case "success", "error", "info":
		return true
	default:
		return false
	}
}
