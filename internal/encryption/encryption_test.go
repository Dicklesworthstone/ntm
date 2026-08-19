package encryption

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := randomKey(t)
	plaintext := []byte("ntm encryption round-trip test")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ciphertext) <= headerSize {
		t.Fatalf("ciphertext too short: %d", len(ciphertext))
	}

	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", string(got), string(plaintext))
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key := randomKey(t)
	wrongKey := randomKey(t)
	plaintext := []byte("ntm encryption wrong-key test")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = Decrypt(wrongKey, ciphertext)
	if err == nil {
		t.Fatal("Decrypt: expected error")
	}
	if !IsWrongKey(err) {
		t.Fatalf("expected wrong-key error, got %v", err)
	}
}

func TestDecryptCorruptedCiphertext(t *testing.T) {
	key := randomKey(t)
	plaintext := []byte("ntm encryption corrupted test")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ciphertext) <= headerSize {
		t.Fatalf("ciphertext too short: %d", len(ciphertext))
	}

	// Truncate to simulate corruption (remove ciphertext entirely).
	corrupted := ciphertext[:headerSize]
	_, err = Decrypt(key, corrupted)
	if err == nil {
		t.Fatal("Decrypt: expected error")
	}
	if !IsKind(err, ErrCorruptedData) {
		t.Fatalf("expected corrupted-data error, got %v", err)
	}
}

func TestEncryptInvalidKey(t *testing.T) {
	_, err := Encrypt([]byte("short"), []byte("data"))
	if err == nil {
		t.Fatal("Encrypt: expected error")
	}
	if !IsKind(err, ErrInvalidKey) {
		t.Fatalf("expected invalid-key error, got %v", err)
	}
}

// =============================================================================
// Nonce randomness
// =============================================================================

func TestEncryptNonceRandomness(t *testing.T) {
	t.Parallel()
	key := randomKey(t)
	plaintext := []byte("identical plaintext for nonce randomness test")

	ct1, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt #1: %v", err)
	}
	ct2, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt #2: %v", err)
	}

	t.Logf("ciphertext 1 length: %d", len(ct1))
	t.Logf("ciphertext 2 length: %d", len(ct2))

	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of identical plaintext produced identical ciphertext; nonce is not random")
	}

	// Both must still decrypt correctly.
	for i, ct := range [][]byte{ct1, ct2} {
		got, err := Decrypt(key, ct)
		if err != nil {
			t.Fatalf("Decrypt #%d: %v", i+1, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("Decrypt #%d: mismatch", i+1)
		}
	}
}

// =============================================================================
// Empty plaintext
// =============================================================================

func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	t.Parallel()
	key := randomKey(t)

	ciphertext, err := Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	t.Logf("format version: %d, ciphertext length: %d", ciphertext[0], len(ciphertext))

	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(got))
	}
}

// =============================================================================
// Corrupted header (bad version byte)
// =============================================================================

func TestDecryptUnsupportedVersion(t *testing.T) {
	t.Parallel()
	key := randomKey(t)

	ciphertext, err := Encrypt(key, []byte("version test"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Overwrite version byte with an unsupported value.
	ciphertext[0] = 99
	_, err = Decrypt(key, ciphertext)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if !IsKind(err, ErrUnsupportedFormat) {
		t.Fatalf("expected unsupported_format error, got %v", err)
	}
	t.Logf("error type: %s", err)
}

// =============================================================================
// Data too short
// =============================================================================

func TestDecryptDataTooShort(t *testing.T) {
	t.Parallel()
	key := randomKey(t)

	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{FormatVersion}},
		{"partial nonce", append([]byte{FormatVersion}, make([]byte, NonceSize-1)...)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Decrypt(key, tc.data)
			if err == nil {
				t.Fatal("expected error for short data")
			}
			if !IsKind(err, ErrCorruptedData) {
				t.Fatalf("expected corrupted_data error, got %v", err)
			}
			t.Logf("error type: %s", err)
		})
	}
}

// =============================================================================
// Decrypt with invalid key size
// =============================================================================

func TestDecryptInvalidKey(t *testing.T) {
	t.Parallel()
	key := randomKey(t)

	ciphertext, err := Encrypt(key, []byte("decrypt invalid key test"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = Decrypt([]byte("short"), ciphertext)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	if !IsKind(err, ErrInvalidKey) {
		t.Fatalf("expected invalid_key error, got %v", err)
	}
}

// =============================================================================
// Error type methods
// =============================================================================

func TestErrorMethods(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var e *Error
		if e.Error() != "<nil>" {
			t.Errorf("Error() on nil = %q, want %q", e.Error(), "<nil>")
		}
		if e.Unwrap() != nil {
			t.Errorf("Unwrap() on nil = %v, want nil", e.Unwrap())
		}
	})

	t.Run("nil inner error", func(t *testing.T) {
		t.Parallel()
		e := &Error{Kind: ErrWrongKey, Err: nil}
		got := e.Error()
		want := "wrong_key"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if e.Unwrap() != nil {
			t.Errorf("Unwrap() = %v, want nil", e.Unwrap())
		}
	})

	t.Run("with inner error", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("test cause")
		e := &Error{Kind: ErrCorruptedData, Err: inner}
		got := e.Error()
		want := "corrupted_data: test cause"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if e.Unwrap() != inner {
			t.Errorf("Unwrap() = %v, want %v", e.Unwrap(), inner)
		}
	})
}

// =============================================================================
// IsKind with non-Error type
// =============================================================================

func TestIsKindNonError(t *testing.T) {
	t.Parallel()

	if IsKind(errors.New("plain error"), ErrWrongKey) {
		t.Error("IsKind should return false for non-*Error")
	}
	if IsKind(nil, ErrWrongKey) {
		t.Error("IsKind should return false for nil")
	}
}

// =============================================================================
// EncryptFile / DecryptFile round-trip
// =============================================================================

func randomKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return key
}
