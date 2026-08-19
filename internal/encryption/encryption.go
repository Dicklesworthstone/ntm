package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySize is the required AES-256 key size in bytes.
	KeySize = 32
	// NonceSize is the AES-GCM nonce size in bytes.
	NonceSize = 12
	// FormatVersion is the current encryption format version.
	FormatVersion byte = 1
)

const headerSize = 1 + NonceSize // version + nonce

// ErrorKind categorizes encryption/decryption failures.
type ErrorKind string

const (
	ErrInvalidKey        ErrorKind = "invalid_key"
	ErrWrongKey          ErrorKind = "wrong_key"
	ErrCorruptedData     ErrorKind = "corrupted_data"
	ErrUnsupportedFormat ErrorKind = "unsupported_format"
)

// Error is a typed error for encryption/decryption failures.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsKind reports whether err is an *Error with the given kind.
func IsKind(err error, kind ErrorKind) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == kind
	}
	return false
}

// IsWrongKey reports whether err indicates an authentication failure (wrong key).
func IsWrongKey(err error) bool {
	return IsKind(err, ErrWrongKey)
}

// Encrypt encrypts plaintext with AES-256-GCM.
// Format: [version (1 byte)][nonce (12 bytes)][ciphertext]
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() != NonceSize {
		return nil, &Error{Kind: ErrUnsupportedFormat, Err: fmt.Errorf("unexpected nonce size %d", gcm.NonceSize())}
	}

	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, headerSize+len(ciphertext))
	out = append(out, FormatVersion)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// Decrypt decrypts data produced by Encrypt.
// Returns ErrWrongKey for authentication failures and ErrCorruptedData for malformed inputs.
func Decrypt(key, data []byte) ([]byte, error) {
	if len(data) < headerSize {
		return nil, &Error{Kind: ErrCorruptedData, Err: fmt.Errorf("data too short (%d bytes)", len(data))}
	}

	version := data[0]
	if version != FormatVersion {
		return nil, &Error{Kind: ErrUnsupportedFormat, Err: fmt.Errorf("unsupported version %d", version)}
	}

	block, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() != NonceSize {
		return nil, &Error{Kind: ErrUnsupportedFormat, Err: fmt.Errorf("unexpected nonce size %d", gcm.NonceSize())}
	}

	nonce := data[1 : 1+NonceSize]
	ciphertext := data[1+NonceSize:]
	if len(ciphertext) < gcm.Overhead() {
		return nil, &Error{Kind: ErrCorruptedData, Err: fmt.Errorf("ciphertext too short (%d bytes)", len(ciphertext))}
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, &Error{Kind: ErrWrongKey, Err: fmt.Errorf("authentication failed (wrong key or corrupted data): %w", err)}
	}
	return plaintext, nil
}

func newCipher(key []byte) (cipher.Block, error) {
	if len(key) != KeySize {
		return nil, &Error{Kind: ErrInvalidKey, Err: fmt.Errorf("expected %d bytes, got %d", KeySize, len(key))}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &Error{Kind: ErrInvalidKey, Err: err}
	}
	return block, nil
}
