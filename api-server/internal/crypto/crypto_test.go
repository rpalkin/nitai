package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestRoundTrip(t *testing.T) {
	key := testKey(t)
	plaintext := []byte("hello, world")

	ct, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(ct, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestEmptyPlaintext(t *testing.T) {
	key := testKey(t)
	ct, err := Encrypt([]byte{}, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(ct, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty plaintext, got %q", got)
	}
}

func TestWrongKey(t *testing.T) {
	key := testKey(t)
	ct, err := Encrypt([]byte("secret"), key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	wrongKey := make([]byte, 32)
	_, err = Decrypt(ct, wrongKey)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestCiphertextTooShort(t *testing.T) {
	key := testKey(t)
	_, err := Decrypt([]byte("short"), key)
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestNonceUniqueness(t *testing.T) {
	key := testKey(t)
	plaintext := []byte("same plaintext")

	ct1, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	ct2, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonces not random)")
	}
}

func TestDecodeKeyHex(t *testing.T) {
	raw := testKey(t)
	encoded := hex.EncodeToString(raw)
	got, err := DecodeKey(encoded)
	if err != nil {
		t.Fatalf("DecodeKey hex: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("hex key mismatch")
	}
}

func TestDecodeKeyBase64(t *testing.T) {
	raw := testKey(t)
	encoded := base64.StdEncoding.EncodeToString(raw)
	got, err := DecodeKey(encoded)
	if err != nil {
		t.Fatalf("DecodeKey base64: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("base64 key mismatch")
	}
}
