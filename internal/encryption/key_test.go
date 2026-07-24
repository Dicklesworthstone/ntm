package encryption

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKey_Env(t *testing.T) {
	// Generate a valid 32-byte key as hex
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}
	hexKey := hex.EncodeToString(keyBytes)

	envVar := "TEST_NTM_ENC_KEY_" + t.Name()
	os.Setenv(envVar, hexKey)
	defer os.Unsetenv(envVar)

	cfg := KeyConfig{
		KeySource: "env",
		KeyEnv:    envVar,
		KeyFormat: "hex",
	}

	key, err := ResolveKey(cfg)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("key length = %d, want %d", len(key), KeySize)
	}
}

func TestResolveKey_EnvDefault(t *testing.T) {
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 10)
	}
	hexKey := hex.EncodeToString(keyBytes)

	os.Setenv("NTM_ENCRYPTION_KEY", hexKey)
	defer os.Unsetenv("NTM_ENCRYPTION_KEY")

	cfg := KeyConfig{
		KeySource: "env",
		KeyEnv:    "", // should default to NTM_ENCRYPTION_KEY
		KeyFormat: "hex",
	}

	key, err := ResolveKey(cfg)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("key length = %d, want %d", len(key), KeySize)
	}
}

func TestResolveKey_File(t *testing.T) {
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 20)
	}
	hexKey := hex.EncodeToString(keyBytes)

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.hex")
	if err := os.WriteFile(keyPath, []byte(hexKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := KeyConfig{
		KeySource: "file",
		KeyFile:   keyPath,
		KeyFormat: "hex",
	}

	key, err := ResolveKey(cfg)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("key length = %d, want %d", len(key), KeySize)
	}
}

func TestResolveKey_Command(t *testing.T) {
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 30)
	}
	hexKey := hex.EncodeToString(keyBytes)

	cfg := KeyConfig{
		KeySource:  "command",
		KeyCommand: "echo " + hexKey,
		KeyFormat:  "hex",
	}

	key, err := ResolveKey(cfg)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("key length = %d, want %d", len(key), KeySize)
	}
}

func TestResolveKey_InvalidSource(t *testing.T) {
	cfg := KeyConfig{KeySource: "magic"}
	_, err := ResolveKey(cfg)
	if err == nil {
		t.Error("expected error for invalid source")
	}
}

func TestResolveKey_Keyring(t *testing.T) {
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 40)
	}
	hexKey := hex.EncodeToString(keyBytes)

	cfg := KeyConfig{
		ActiveKeyID: "primary",
		Keyring:     map[string]string{"primary": hexKey},
		KeyFormat:   "hex",
	}

	key, err := ResolveKey(cfg)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("key length = %d, want %d", len(key), KeySize)
	}
}

func TestResolveKey_KeyringMissingActiveID(t *testing.T) {
	cfg := KeyConfig{
		ActiveKeyID: "missing",
		Keyring:     map[string]string{"other": "deadbeef"},
		KeyFormat:   "hex",
	}

	_, err := ResolveKey(cfg)
	if err == nil {
		t.Error("expected error for missing active key ID")
	}
}

func TestResolveKeyring(t *testing.T) {
	key1 := make([]byte, KeySize)
	key2 := make([]byte, KeySize)
	for i := range key1 {
		key1[i] = byte(i)
	}
	for i := range key2 {
		key2[i] = byte(i + 100)
	}

	cfg := KeyConfig{
		Keyring: map[string]string{
			"k1": hex.EncodeToString(key1),
			"k2": hex.EncodeToString(key2),
		},
		KeyFormat: "hex",
	}

	keys, err := ResolveKeyring(cfg)
	if err != nil {
		t.Fatalf("ResolveKeyring: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestResolveKeyring_ActiveKeyFirstThenSorted(t *testing.T) {
	// Distinct key material per ID so ordering is observable.
	material := map[string][]byte{}
	keyring := map[string]string{}
	for i, id := range []string{"k1", "k2", "k3"} {
		key := make([]byte, KeySize)
		for j := range key {
			key[j] = byte(i*10 + j)
		}
		material[id] = key
		keyring[id] = hex.EncodeToString(key)
	}

	cfg := KeyConfig{Keyring: keyring, KeyFormat: "hex", ActiveKeyID: "k3"}

	// Repeat: Go randomizes map iteration, so a single pass can pass by luck.
	for attempt := 0; attempt < 32; attempt++ {
		keys, err := ResolveKeyring(cfg)
		if err != nil {
			t.Fatalf("ResolveKeyring: %v", err)
		}
		want := [][]byte{material["k3"], material["k1"], material["k2"]}
		if len(keys) != len(want) {
			t.Fatalf("got %d keys, want %d", len(keys), len(want))
		}
		for i := range want {
			if !bytes.Equal(keys[i], want[i]) {
				t.Fatalf("attempt %d: key[%d] mismatch: got %x want %x", attempt, i, keys[i], want[i])
			}
		}
	}
}

func TestResolveKeyring_ActiveKeyNotInKeyring(t *testing.T) {
	key := make([]byte, KeySize)
	cfg := KeyConfig{
		Keyring:     map[string]string{"k1": hex.EncodeToString(key)},
		KeyFormat:   "hex",
		ActiveKeyID: "missing",
	}
	if _, err := ResolveKeyring(cfg); err == nil {
		t.Fatal("expected error when active_key_id is absent from the keyring")
	}
}

func TestResolveKeyring_EmptyUsesSource(t *testing.T) {
	keyBytes := make([]byte, KeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 50)
	}
	hexKey := hex.EncodeToString(keyBytes)

	envVar := "TEST_NTM_KR_" + t.Name()
	os.Setenv(envVar, hexKey)
	defer os.Unsetenv(envVar)

	cfg := KeyConfig{
		KeySource: "env",
		KeyEnv:    envVar,
		KeyFormat: "hex",
	}

	keys, err := ResolveKeyring(cfg)
	if err != nil {
		t.Fatalf("ResolveKeyring: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key from source, got %d", len(keys))
	}
}

func TestDecodeKey_WrongSize(t *testing.T) {
	_, err := decodeKey("deadbeef", "hex") // only 4 bytes
	if err == nil {
		t.Error("expected error for wrong key size")
	}
	if !IsInvalidKey(err) {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}
}

func TestDecodeKey_InvalidHex(t *testing.T) {
	_, err := decodeKey("not-hex-data!", "hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestDecodeKey_InvalidBase64(t *testing.T) {
	_, err := decodeKey("not valid base64!!!", "base64")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestDecodeKey_UnsupportedFormat(t *testing.T) {
	_, err := decodeKey("data", "raw")
	if err == nil {
		t.Error("expected error for unsupported format")
	}
}

func TestDecodeKey_Empty(t *testing.T) {
	_, err := decodeKey("", "hex")
	if err == nil {
		t.Error("expected error for empty key")
	}
}
