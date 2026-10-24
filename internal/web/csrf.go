package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

const csrfCookieName = "csrf_token"

type CSRFMiddleware struct {
	secret []byte
	secure bool
}

func NewCSRFMiddleware(secret []byte, secure bool) *CSRFMiddleware {
	return &CSRFMiddleware{secret: append([]byte(nil), secret...), secure: secure}
}

func (m *CSRFMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(csrfCookieName)
		token := ""
		if err == nil && m.valid(cookie.Value) {
			token = cookie.Value
		} else {
			var genErr error
			token, genErr = m.generate()
			if genErr != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: csrfCookieName, Value: token, Path: "/", Secure: m.secure,
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			maxBytes := int64(64 << 10)
			if strings.Contains(r.URL.Path, "/migrations") {
				maxBytes = 1 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Permintaan tidak valid.", http.StatusBadRequest)
				return
			}
			supplied := r.Header.Get("X-CSRF-Token")
			if supplied == "" {
				supplied = r.FormValue("csrf_token")
			}
			if !m.valid(token) || !m.valid(supplied) || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
				http.Error(w, "Permintaan ditolak.", http.StatusForbidden)
				return
			}
		}
		ctx := contract.WithCSRFToken(r.Context(), token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *CSRFMiddleware) generate() (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte("if3110-csrf:" + payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func (m *CSRFMiddleware) valid(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) < 20 {
		return false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte("if3110-csrf:" + parts[0]))
	expected := mac.Sum(nil)
	return hmac.Equal(provided, expected)
}

// CSRFTokenFromRequest adalah helper pengujian tanpa mengekspos secret.
func CSRFTokenFromRequest(r *http.Request) string {
	token, _ := contract.CSRFTokenFrom(r.Context())
	return token
}
