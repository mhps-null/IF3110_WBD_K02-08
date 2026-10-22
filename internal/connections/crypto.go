package connections

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	ErrInvalidKey        = errors.New("encryption key must be exactly 32 bytes")
	ErrInvalidCiphertext = errors.New("invalid or corrupted ciphertext")
)

// LoadEncryptionKey loads and validates the 32-byte encryption key from the environment.
func LoadEncryptionKey() ([]byte, error) {
	val := os.Getenv("DB_ENCRYPTION_KEY")
	if val == "" {
		return nil, errors.New("DB_ENCRYPTION_KEY environment variable is required")
	}

	if decoded, err := base64.StdEncoding.DecodeString(val); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(val); err == nil && len(decoded) == 32 {
		return decoded, nil
	}

	raw := []byte(val)
	if len(raw) != 32 {
		return nil, ErrInvalidKey
	}
	return raw, nil
}

// Encrypt encrypts a plaintext string using AES-256-GCM and returns a base64 URL-safe string.
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher block: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm mode: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts an AES-256-GCM base64 URL-safe string produced by Encrypt.
func Decrypt(encodedCiphertext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidKey
	}

	data, err := base64.RawURLEncoding.DecodeString(encodedCiphertext)
	if err != nil {
		return "", ErrInvalidCiphertext
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher block: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm mode: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return "", ErrInvalidCiphertext
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}

	return string(plaintext), nil
}
