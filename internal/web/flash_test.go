package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFlashManagerRoundTripAndConsume(t *testing.T) {
	manager, err := NewFlashManager([]byte(strings.Repeat("s", 32)), false)
	if err != nil {
		t.Fatal(err)
	}
	setRecorder := httptest.NewRecorder()
	manager.Set(setRecorder, "success", "Berhasil masuk.")
	cookies := setRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count=%d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != flashCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge <= 0 {
		t.Fatal("flash cookie attributes are incomplete")
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	consumeRecorder := httptest.NewRecorder()
	message, kind := manager.Consume(consumeRecorder, request)
	if message != "Berhasil masuk." || kind != "success" {
		t.Fatalf("consume=(%q, %q), want (%q, %q)", message, kind, "Berhasil masuk.", "success")
	}
	cleared := consumeRecorder.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != flashCookieName || cleared[0].MaxAge != -1 {
		t.Fatal("consume did not expire flash cookie")
	}

	// A subsequent request that no longer sends the expired cookie has no message.
	secondRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	secondRecorder := httptest.NewRecorder()
	if message, kind := manager.Consume(secondRecorder, secondRequest); message != "" || kind != "" {
		t.Fatalf("second consume=(%q, %q), want empty message", message, kind)
	}
}

func TestFlashManagerRejectsTamperedCookie(t *testing.T) {
	manager, err := NewFlashManager([]byte(strings.Repeat("s", 32)), false)
	if err != nil {
		t.Fatal(err)
	}
	setRecorder := httptest.NewRecorder()
	manager.Set(setRecorder, "success", "Berhasil masuk.")
	cookie := setRecorder.Result().Cookies()[0]
	b := []byte(cookie.Value)
	if b[0] == 'x' {
		b[0] = 'y'
	} else {
		b[0] = 'x'
	}
	cookie.Value = string(b)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	consumeRecorder := httptest.NewRecorder()
	if message, kind := manager.Consume(consumeRecorder, request); message != "" || kind != "" {
		t.Fatalf("tampered cookie accepted: (%q, %q)", message, kind)
	}
	if got := consumeRecorder.Result().Cookies(); len(got) != 1 || got[0].MaxAge != -1 {
		t.Fatal("tampered flash cookie was not expired")
	}
}

func TestFlashManagerRejectsExpiredCookie(t *testing.T) {
	manager, err := NewFlashManager([]byte(strings.Repeat("s", 32)), false)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: flashCookieName, Value: manager.sign("info", "Pesan lama.", time.Now().Add(-flashMaxAge-time.Second))}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	consumeRecorder := httptest.NewRecorder()
	if message, kind := manager.Consume(consumeRecorder, request); message != "" || kind != "" {
		t.Fatalf("expired cookie accepted: (%q, %q)", message, kind)
	}
}

func TestNewFlashManagerRequiresStrongSecret(t *testing.T) {
	if _, err := NewFlashManager([]byte("too-short"), false); err == nil {
		t.Fatal("expected short secret to be rejected")
	}
}
