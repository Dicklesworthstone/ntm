package encryption

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// KeyConfig holds key resolution parameters.
type KeyConfig struct {
	KeySource   string            // env, file, or command
	KeyEnv      string            // Environment variable name
	KeyFile     string            // Path to key file
	KeyCommand  string            // Shell command to retrieve key
	KeyFormat   string            // hex or base64
	ActiveKeyID string            // Active key for writes
	Keyring     map[string]string // Key ID -> encoded key material
}

// ResolveKey loads the encryption key from the configured source.
// Returns the 32-byte AES-256 key or an error with remediation hints.
func ResolveKey(cfg KeyConfig) ([]byte, error) {
	if cfg.ActiveKeyID != "" && len(cfg.Keyring) > 0 {
		encoded, ok := cfg.Keyring[cfg.ActiveKeyID]
		if !ok {
			return nil, fmt.Errorf("active_key_id %q not found in keyring", cfg.ActiveKeyID)
		}
		return decodeKey(encoded, cfg.KeyFormat)
	}

	var encoded string
	var err error

	switch cfg.KeySource {
	case "env":
		encoded, err = resolveFromEnv(cfg.KeyEnv)
	case "file":
		encoded, err = resolveFromFile(cfg.KeyFile)
	case "command":
		encoded, err = resolveFromCommand(cfg.KeyCommand)
	default:
		return nil, fmt.Errorf("unsupported key_source %q: use env, file, or command", cfg.KeySource)
	}
	if err != nil {
		return nil, err
	}

	return decodeKey(encoded, cfg.KeyFormat)
}

// ResolveKeyring builds a list of all keys in the keyring for decryption attempts.
// Returns keys in declaration order. If no keyring, returns a single key from the source.
func ResolveKeyring(cfg KeyConfig) ([][]byte, error) {
	if len(cfg.Keyring) == 0 {
		key, err := ResolveKey(cfg)
		if err != nil {
			return nil, err
		}
		return [][]byte{key}, nil
	}

	var keys [][]byte
	for id, encoded := range cfg.Keyring {
		key, err := decodeKey(encoded, cfg.KeyFormat)
		if err != nil {
			return nil, fmt.Errorf("keyring entry %q: %w", id, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func resolveFromEnv(envVar string) (string, error) {
	if envVar == "" {
		envVar = "NTM_ENCRYPTION_KEY"
	}
	val := os.Getenv(envVar)
	if val == "" {
		return "", fmt.Errorf("encryption key not set: export %s=<hex-encoded-32-byte-key>", envVar)
	}
	return val, nil
}

func resolveFromFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("encryption.key_file is required when key_source=file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading key file %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func resolveFromCommand(cmd string) (string, error) {
	if cmd == "" {
		return "", fmt.Errorf("encryption.key_command is required when key_source=command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).Output()
	if err != nil {
		// Don't include the command string in the error — it may contain secrets
		return "", fmt.Errorf("key command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func decodeKey(encoded, format string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("encryption key is empty")
	}

	if format == "" {
		format = "hex"
	}

	var key []byte
	var err error

	switch format {
	case "hex":
		key, err = hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid hex key: %w (expected 64 hex characters for AES-256)", err)
		}
	case "base64":
		key, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 key: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported key_format %q: use hex or base64", format)
	}

	if len(key) != KeySize {
		return nil, &Error{
			Kind: ErrInvalidKey,
			Err:  fmt.Errorf("decoded key is %d bytes, expected %d (AES-256)", len(key), KeySize),
		}
	}
	return key, nil
}
