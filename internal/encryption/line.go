package encryption

import (
	"encoding/base64"
	"fmt"
)

// EncryptLine encrypts a JSON line and returns base64-encoded ciphertext.
// The result is safe for use in JSONL files (no newlines).
func EncryptLine(key, plaintext []byte) ([]byte, error) {
	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(ciphertext)))
	base64.StdEncoding.Encode(encoded, ciphertext)
	return encoded, nil
}

// DecryptLineWithKeyring tries each key in order until one succeeds.
// Returns the decrypted plaintext or ErrWrongKey if no key works.
func DecryptLineWithKeyring(keys [][]byte, encoded []byte) ([]byte, error) {
	ciphertext := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(ciphertext, encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	data := ciphertext[:n]

	for _, key := range keys {
		plaintext, err := Decrypt(key, data)
		if err == nil {
			return plaintext, nil
		}
		if !IsWrongKey(err) {
			return nil, err
		}
	}
	return nil, &Error{Kind: ErrWrongKey, Err: fmt.Errorf("no key in keyring could decrypt the line")}
}

// IsEncryptedLine returns true if the line appears to be encrypted
// (not plaintext JSON). Plaintext JSON lines start with '{' or '['.
func IsEncryptedLine(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	return line[0] != '{' && line[0] != '['
}
