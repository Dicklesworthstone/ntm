package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

// EncryptionConfig configures transparent AES-256-GCM encryption at rest for
// checkpoint metadata, pane scrollback, git patches/status, and exported
// checkpoint bundles (docs/ENCRYPTION_SPEC.md Phase 2).
type EncryptionConfig struct {
	Enabled     bool
	EncryptKey  []byte
	DecryptKeys [][]byte
}

var (
	checkpointEncMu    sync.RWMutex
	checkpointEncOn    bool
	checkpointEncKey   []byte
	checkpointDecKeys  [][]byte
	errMissingDecKey   = errors.New("encrypted checkpoint artifact requires [encryption] key configuration")
	errMissingEncKey   = errors.New("checkpoint encryption enabled without active encryption key")
)

// SetEncryptionConfig configures (or clears when cfg == nil or !cfg.Enabled)
// transparent checkpoint encryption and keyring decryption.
func SetEncryptionConfig(cfg *EncryptionConfig) {
	checkpointEncMu.Lock()
	defer checkpointEncMu.Unlock()

	if cfg == nil || !cfg.Enabled {
		checkpointEncOn = false
		checkpointEncKey = nil
		checkpointDecKeys = nil
		return
	}

	checkpointEncOn = true
	if len(cfg.EncryptKey) > 0 {
		checkpointEncKey = append([]byte(nil), cfg.EncryptKey...)
	} else {
		checkpointEncKey = nil
	}

	if len(cfg.DecryptKeys) > 0 {
		keys := make([][]byte, 0, len(cfg.DecryptKeys)+1)
		for _, k := range cfg.DecryptKeys {
			if len(k) > 0 {
				keys = append(keys, append([]byte(nil), k...))
			}
		}
		// Ensure the active encryption key can always decrypt artifacts it just wrote.
		if len(checkpointEncKey) > 0 && !containsKey(keys, checkpointEncKey) {
			keys = append([][]byte{append([]byte(nil), checkpointEncKey...)}, keys...)
		}
		checkpointDecKeys = keys
	} else if len(checkpointEncKey) > 0 {
		checkpointDecKeys = [][]byte{append([]byte(nil), checkpointEncKey...)}
	} else {
		checkpointDecKeys = nil
	}
}

func containsKey(keys [][]byte, target []byte) bool {
	for _, k := range keys {
		if bytes.Equal(k, target) {
			return true
		}
	}
	return false
}

func isCheckpointEncryptionEnabled() bool {
	checkpointEncMu.RLock()
	defer checkpointEncMu.RUnlock()
	return checkpointEncOn
}

func encryptCheckpointBytes(plaintext []byte) ([]byte, error) {
	checkpointEncMu.RLock()
	enabled := checkpointEncOn
	key := checkpointEncKey
	checkpointEncMu.RUnlock()

	if !enabled {
		return plaintext, nil
	}
	if len(key) == 0 {
		return nil, errMissingEncKey
	}
	if encryption.IsEncryptedArtifact(plaintext) {
		return plaintext, nil
	}
	ciphertext, err := encryption.EncryptArtifact(key, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting checkpoint artifact: %w", err)
	}
	return ciphertext, nil
}

func decryptCheckpointBytes(data []byte) ([]byte, error) {
	if !encryption.IsEncryptedArtifact(data) {
		return data, nil
	}

	checkpointEncMu.RLock()
	keys := checkpointDecKeys
	checkpointEncMu.RUnlock()

	if len(keys) == 0 {
		return nil, errMissingDecKey
	}
	plaintext, err := encryption.DecryptArtifactWithKeyring(keys, data)
	if err != nil {
		return nil, fmt.Errorf("decrypting checkpoint artifact: %w", err)
	}
	return plaintext, nil
}
