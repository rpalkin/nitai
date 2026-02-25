package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Encrypt encrypts plaintext using AES-256-GCM with a random 12-byte nonce.
// The nonce is prepended to the returned ciphertext.
func Encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	return plaintext, nil
}

// LoadKeyFromEnv loads and decodes the encryption key from ENCRYPTION_KEY.
func LoadKeyFromEnv() ([]byte, error) {
	val := os.Getenv("ENCRYPTION_KEY")
	if val == "" {
		return nil, errors.New("ENCRYPTION_KEY environment variable not set")
	}
	return DecodeKey(val)
}

// DecodeKey decodes a hex- or base64-encoded 32-byte key.
func DecodeKey(s string) ([]byte, error) {
	// Try hex (64 hex chars = 32 bytes)
	if len(s) == 64 {
		b, err := hex.DecodeString(s)
		if err == nil && len(b) == 32 {
			return b, nil
		}
	}
	// Try standard base64, then URL base64
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return nil, errors.New("key is not valid hex or base64")
		}
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes, got %d", len(b))
	}
	return b, nil
}
