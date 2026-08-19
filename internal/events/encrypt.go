package events

import (
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

var (
	// encryptionEnabled indicates whether line-level encryption is active.
	encryptionEnabled bool
	// encryptKey is the active AES-256 key for encrypting new entries.
	encryptKey []byte
	// decryptKeys holds all keyring keys for decryption (includes encryptKey).
	decryptKeys [][]byte
	encryptMu   sync.RWMutex
)

// EncryptionConfig holds resolved encryption keys for event log persistence.
type EncryptionConfig struct {
	Enabled     bool
	EncryptKey  []byte   // Active key for writing new entries
	DecryptKeys [][]byte // All keys for reading (keyring)
}

// SetEncryptionConfig sets the global encryption config for event log writes/reads.
// Pass nil to disable encryption.
func SetEncryptionConfig(cfg *EncryptionConfig) {
	encryptMu.Lock()
	defer encryptMu.Unlock()
	if cfg != nil && cfg.Enabled && len(cfg.EncryptKey) > 0 {
		encryptionEnabled = true
		encryptKey = make([]byte, len(cfg.EncryptKey))
		copy(encryptKey, cfg.EncryptKey)
		decryptKeys = make([][]byte, len(cfg.DecryptKeys))
		for i, k := range cfg.DecryptKeys {
			decryptKeys[i] = make([]byte, len(k))
			copy(decryptKeys[i], k)
		}
	} else {
		encryptionEnabled = false
		encryptKey = nil
		decryptKeys = nil
	}
}

// encryptJSONLine encrypts a marshaled JSON line if encryption is enabled.
// Returns the original data unchanged when encryption is disabled.
func encryptJSONLine(data []byte) ([]byte, error) {
	encryptMu.RLock()
	enabled := encryptionEnabled
	key := encryptKey
	encryptMu.RUnlock()

	if !enabled || key == nil {
		return data, nil
	}
	return encryption.EncryptLine(key, data)
}

// decryptJSONLine decrypts an encrypted JSONL line if needed.
// Plaintext lines (starting with '{') are returned as-is for backward compatibility.
func decryptJSONLine(line []byte) ([]byte, error) {
	if !encryption.IsEncryptedLine(line) {
		return line, nil
	}

	encryptMu.RLock()
	keys := decryptKeys
	encryptMu.RUnlock()

	if len(keys) == 0 {
		// Encrypted data but no keys configured — return raw (will fail JSON unmarshal)
		return line, nil
	}
	return encryption.DecryptLineWithKeyring(keys, line)
}
