package password

import (
	"encoding/hex"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("valid-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || hash == "valid-test-password" || !Verify(hash, "valid-test-password") || Verify(hash, "wrong") {
		t.Fatal("password hashing/verification failed")
	}
}
func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, hash := range []string{"", "plaintext", "$pbkdf2-sha256$1$abc$abc", "$other$600000$abc$abc"} {
		if Verify(hash, "password") {
			t.Fatalf("malformed hash accepted: %q", hash)
		}
	}
}
func TestPBKDF2KnownVector(t *testing.T) {
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if got := hex.EncodeToString(pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)); got != want {
		t.Fatalf("PBKDF2=%s", got)
	}
}
