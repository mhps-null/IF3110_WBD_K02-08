package password

import (
	"encoding/hex"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("a-long-test-password-123")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "a-long-test-password-123" || hash == "" {
		t.Fatal("Hash() must return an encoded password hash")
	}
	if !Verify(hash, "a-long-test-password-123") {
		t.Fatal("Verify() rejected the correct password")
	}
	if Verify(hash, "wrong-password") {
		t.Fatal("Verify() accepted an incorrect password")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "plaintext", "$pbkdf2-sha256$1$abc$abc", "$other$600000$abc$abc"} {
		if Verify(encoded, "password") {
			t.Errorf("Verify(%q) = true, want false", encoded)
		}
	}
}

func TestPBKDF2KnownVector(t *testing.T) {
	got := hex.EncodeToString(pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32))
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if got != want {
		t.Fatalf("PBKDF2-SHA256 vector = %s, want %s", got, want)
	}
}
