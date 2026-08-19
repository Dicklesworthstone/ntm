package encryption

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptLineDecryptLineRoundTrip(t *testing.T) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"prompt":"hello world","session":"test-1"}`)

	encrypted, err := EncryptLine(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptLine: %v", err)
	}

	// Encrypted output should be base64 (no newlines, not starting with '{')
	if bytes.Contains(encrypted, []byte("\n")) {
		t.Error("encrypted line contains newline")
	}
	if len(encrypted) > 0 && encrypted[0] == '{' {
		t.Error("encrypted line starts with '{', should be base64")
	}

	decrypted, err := DecryptLineWithKeyring([][]byte{key}, encrypted)
	if err != nil {
		t.Fatalf("DecryptLine: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptLineWithKeyring(t *testing.T) {
	key1 := make([]byte, KeySize)
	key2 := make([]byte, KeySize)
	key3 := make([]byte, KeySize)
	if _, err := rand.Read(key1); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(key2); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(key3); err != nil {
		t.Fatal(err)
	}

	plaintext := []byte(`{"event":"test"}`)
	encrypted, err := EncryptLine(key2, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	// key2 is in the keyring at position 1
	keyring := [][]byte{key1, key2, key3}
	decrypted, err := DecryptLineWithKeyring(keyring, encrypted)
	if err != nil {
		t.Fatalf("DecryptLineWithKeyring: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptLineWithKeyring_NoMatch(t *testing.T) {
	key1 := make([]byte, KeySize)
	key2 := make([]byte, KeySize)
	keyEnc := make([]byte, KeySize)
	if _, err := rand.Read(key1); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(key2); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(keyEnc); err != nil {
		t.Fatal(err)
	}

	encrypted, err := EncryptLine(keyEnc, []byte("data"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = DecryptLineWithKeyring([][]byte{key1, key2}, encrypted)
	if err == nil {
		t.Fatal("expected error when no key matches")
	}
	if !IsWrongKey(err) {
		t.Errorf("expected ErrWrongKey, got %v", err)
	}
}

func TestIsEncryptedLine(t *testing.T) {
	tests := []struct {
		name string
		line []byte
		want bool
	}{
		{"empty", nil, false},
		{"json_object", []byte(`{"key":"value"}`), false},
		{"json_array", []byte(`["a","b"]`), false},
		{"base64_data", []byte("AQAAAA=="), true},
		{"random_text", []byte("hello"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEncryptedLine(tt.line); got != tt.want {
				t.Errorf("IsEncryptedLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestEncryptLine_EmptyPlaintext(t *testing.T) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	encrypted, err := EncryptLine(key, []byte{})
	if err != nil {
		t.Fatalf("EncryptLine empty: %v", err)
	}

	decrypted, err := DecryptLineWithKeyring([][]byte{key}, encrypted)
	if err != nil {
		t.Fatalf("DecryptLine empty: %v", err)
	}
	if len(decrypted) != 0 {
		t.Errorf("expected empty, got %q", decrypted)
	}
}
