package encryption

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// DefaultKeyringService is the OS keyring service/item name queried when
// key_source="keyring".
const DefaultKeyringService = "ntm-encryption-key"

// osKeyringLookupFunc queries the OS keyring (Linux Secret Service via
// secret-tool or macOS Keychain via security). Overridable in tests.
var osKeyringLookupFunc = lookupOSKeyring

// KeyConfig holds key resolution parameters.
type KeyConfig struct {
	KeySource   string            // env, file, command, or keyring
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
	case "keyring":
		encoded, err = resolveFromOSKeyring(cfg)
	default:
		return nil, fmt.Errorf("unsupported key_source %q: use env, file, command, or keyring", cfg.KeySource)
	}
	if err != nil {
		return nil, err
	}

	return decodeKey(encoded, cfg.KeyFormat)
}

// ResolveKeyring builds the list of keys to try when decrypting, in a
// deterministic order: the active key first (it wrote the newest artifacts, so
// it is the most likely match), then the remaining keyring entries by ascending
// key ID. TOML decodes the keyring into a map, so declaration order is not
// recoverable; sorting keeps the attempt order stable across runs.
//
// If no keyring is configured, the single key from the key source is returned.
func ResolveKeyring(cfg KeyConfig) ([][]byte, error) {
	if len(cfg.Keyring) == 0 {
		key, err := ResolveKey(cfg)
		if err != nil {
			return nil, err
		}
		return [][]byte{key}, nil
	}

	ids := make([]string, 0, len(cfg.Keyring))
	for id := range cfg.Keyring {
		if id == cfg.ActiveKeyID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if cfg.ActiveKeyID != "" {
		if _, ok := cfg.Keyring[cfg.ActiveKeyID]; !ok {
			return nil, fmt.Errorf("active_key_id %q not found in keyring", cfg.ActiveKeyID)
		}
		ids = append([]string{cfg.ActiveKeyID}, ids...)
	}

	keys := make([][]byte, 0, len(ids))
	for _, id := range ids {
		key, err := decodeKey(cfg.Keyring[id], cfg.KeyFormat)
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

func resolveFromOSKeyring(cfg KeyConfig) (string, error) {
	account := strings.TrimSpace(cfg.ActiveKeyID)
	if account == "" {
		account = DefaultKeyringService
	}
	val, krErr := osKeyringLookupFunc(DefaultKeyringService, account)
	if krErr == nil && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val), nil
	}
	// Headless fallback when the operator explicitly configured key_env or key_file.
	if envName := strings.TrimSpace(cfg.KeyEnv); envName != "" {
		if envVal := strings.TrimSpace(os.Getenv(envName)); envVal != "" {
			return envVal, nil
		}
	}
	if filePath := strings.TrimSpace(cfg.KeyFile); filePath != "" {
		if data, fileErr := os.ReadFile(filePath); fileErr == nil && strings.TrimSpace(string(data)) != "" {
			return strings.TrimSpace(string(data)), nil
		}
	}
	if krErr != nil {
		return "", fmt.Errorf("OS keyring lookup failed for %q (%v); populate your OS keyring or set key_env/key_file for headless fallback", account, krErr)
	}
	return "", fmt.Errorf("OS keyring entry %q is empty; populate your OS keyring or set key_env/key_file for headless fallback", account)
}

func lookupOSKeyring(service, account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err != nil {
			return "", fmt.Errorf("macOS security CLI not found: %w", err)
		}
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
		if err != nil {
			out, err = exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w").Output()
			if err != nil {
				return "", fmt.Errorf("keychain query failed: %w", err)
			}
		}
		return strings.TrimSpace(string(out)), nil
	default:
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return "", fmt.Errorf("secret-tool (libsecret) not found on PATH: %w", err)
		}
		out, err := exec.CommandContext(ctx, "secret-tool", "lookup", "service", service, "account", account).Output()
		if err != nil {
			return "", fmt.Errorf("secret-tool lookup failed: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
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
