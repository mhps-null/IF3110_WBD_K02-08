package connections

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestCrypto(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	encrypted, err := Encrypt("secret", key)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(encrypted, key)
	if err != nil || plain != "secret" || encrypted == plain {
		t.Fatalf("round trip: plain=%q err=%v", plain, err)
	}
	second, err := Encrypt("secret", key)
	if err != nil || second == encrypted {
		t.Fatal("encryption must use a fresh nonce")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	for _, tc := range []struct {
		name, payload string
		key           []byte
		want          error
	}{
		{"tampered", base64.RawURLEncoding.EncodeToString(raw), key, ErrInvalidCiphertext},
		{"wrong key", encrypted, bytes.Repeat([]byte{2}, 32), ErrInvalidCiphertext},
		{"invalid key", encrypted, []byte("short"), ErrInvalidKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decrypt(tc.payload, tc.key); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoadEncryptionKey(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"missing", "", false},
		{"raw", "12345678901234567890123456789012", true},
		{"base64", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), true},
		{"short", "short", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DB_ENCRYPTION_KEY", tc.value)
			key, err := LoadEncryptionKey()
			if tc.valid && (err != nil || len(key) != 32) {
				t.Fatalf("key=%v err=%v", key, err)
			}
			if !tc.valid && err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
