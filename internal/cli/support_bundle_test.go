package cli

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/bundle"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

func TestSupportBundleEncryptionCommandRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	key := bytes.Repeat([]byte{0x43}, encryption.KeySize)
	t.Setenv("NTM_SUPPORT_BUNDLE_KEY", hex.EncodeToString(key))
	origCfg := cfg
	cfg = config.Default()
	cfg.Encryption.KeyEnv = "NTM_SUPPORT_BUNDLE_KEY"
	t.Cleanup(func() { cfg = origCfg })

	output := filepath.Join(dir, "diagnostics.zip")
	cmd := newSupportBundleCmd()
	cmd.SetArgs([]string{"--encrypt", "--include-events=false", "--output", output})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	encryptedPath := output + ".enc"
	data, err := os.ReadFile(encryptedPath)
	if err != nil || !encryption.IsEncryptedArtifact(data) {
		t.Fatalf("encrypted output missing: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("plaintext output was left behind: %v", err)
	}
	cmd = newSupportBundleCmd()
	cmd.SetArgs([]string{"--verify", encryptedPath})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("verify encrypted CLI output: %v", err)
	}
	cmd = newSupportBundleCmd()
	cmd.SetArgs([]string{"--decrypt", encryptedPath, "--output", output})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("decrypt CLI output: %v", err)
	}
	verified, err := bundle.Verify(output)
	if err != nil || !verified.Valid || verified.Encrypted {
		t.Fatalf("decrypted output: result=%+v error=%v", verified, err)
	}
	if verified.Manifest.FileCount() < 3 {
		t.Fatalf("diagnostic files missing: %+v", verified.Manifest)
	}
}

func TestSupportBundleEncryptionMissingKeyAndExplicitPlaintext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("NTM_SUPPORT_BUNDLE_MISSING_KEY", "")
	origCfg := cfg
	cfg = config.Default()
	cfg.Encryption.Enabled = true
	cfg.Encryption.KeyEnv = "NTM_SUPPORT_BUNDLE_MISSING_KEY"
	t.Cleanup(func() { cfg = origCfg })
	output := filepath.Join(dir, "diagnostics.zip")
	existing := []byte("existing output must survive missing-key failure")
	if err := os.WriteFile(output+".enc", existing, 0600); err != nil {
		t.Fatal(err)
	}
	err := runSupportBundle(t.Context(), supportBundleOptions{Output: output})
	if err == nil || !strings.Contains(err.Error(), "encryption required") {
		t.Fatalf("required encryption did not fail closed: %v", err)
	}
	actual, err := os.ReadFile(output + ".enc")
	if err != nil || !bytes.Equal(actual, existing) {
		t.Fatalf("failed encryption modified existing output: %q %v", actual, err)
	}
	cmd := newSupportBundleCmd()
	cmd.SetArgs([]string{"--allow-plaintext", "--include-events=false", "--output", output})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("explicit plaintext export: %v", err)
	}
	verified, err := bundle.Verify(output)
	if err != nil || !verified.Valid || verified.Encrypted {
		t.Fatalf("plaintext override output: result=%+v error=%v", verified, err)
	}
	if !cfg.Encryption.Enabled {
		t.Fatal("local override changed the configured encryption policy")
	}
}

func TestSupportBundleConfigNeverExportsEncryptionCredentials(t *testing.T) {
	configuration := config.Default()
	credential := strings.Repeat("42", 32)
	configuration.Encryption.Keyring = map[string]string{"current": credential}
	configuration.Encryption.KeyCommand = "printf key-command-secret"
	data, err := exportBundleConfigTOML(configuration, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(credential)) || bytes.Contains(data, []byte("key-command-secret")) {
		t.Fatal("unencrypted credential escaped into the effective config export")
	}
	if configuration.Encryption.Keyring["current"] != credential || configuration.Encryption.KeyCommand != "printf key-command-secret" {
		t.Fatal("export mutated the live encryption configuration")
	}
}

func TestSupportBundleCollectsEncryptedEventsWithRotatedKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	origCfg := cfg
	cfg = config.Default()
	oldKey := bytes.Repeat([]byte{0x29}, encryption.KeySize)
	cfg.Encryption.ActiveKeyID = "new"
	cfg.Encryption.Keyring = map[string]string{
		"old": hex.EncodeToString(oldKey),
		"new": strings.Repeat("42", encryption.KeySize),
	}
	t.Cleanup(func() { cfg = origCfg })
	event, err := json.Marshal(map[string]any{"timestamp": time.Now().UTC(), "type": "session_created", "session": "encrypted-event"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encryption.EncryptLine(oldKey, event)
	if err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(dir, ".config", "ntm", "analytics", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsPath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := collectBundleEvents("2h", nil)
	if err != nil || !bytes.Equal(bytes.TrimSpace(data), event) {
		t.Fatalf("encrypted event omitted: data=%s error=%v", data, err)
	}
	delete(cfg.Encryption.Keyring, "old")
	if _, err := collectBundleEvents("2h", nil); err == nil {
		t.Fatal("unreadable encrypted events were silently discarded")
	}
}

func TestSupportBundleEncryptionOverridesStayLocal(t *testing.T) {
	root := &cobra.Command{Use: "ntm"}
	bundleCommand := newSupportBundleCmd()
	root.AddCommand(bundleCommand)
	if !supportBundleHandlesEncryption(bundleCommand) {
		t.Fatal("direct support-bundle command does not own its export policy")
	}
	other := &cobra.Command{Use: "send"}
	root.AddCommand(other)
	if supportBundleHandlesEncryption(other) || supportBundleHandlesEncryption(root) || supportBundleHandlesEncryption(nil) {
		t.Fatal("support bundle override applies to an unrelated command")
	}
	nested := newSupportBundleCmd()
	other.AddCommand(nested)
	if supportBundleHandlesEncryption(nested) {
		t.Fatal("nested command can bypass startup encryption")
	}
	for _, args := range [][]string{
		{"--encrypt", "--allow-plaintext", "-o", "unused.zip"},
		{"--verify", "unused.zip.enc", "--allow-plaintext"},
		{"--decrypt", "unused.zip.enc", "--encrypt", "-o", "unused.zip"},
	} {
		cmd := newSupportBundleCmd()
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("accepted conflicting encryption flags: %v", args)
		}
	}
}

func TestSupportBundleCreateAndVerify(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	origCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = origCfg })

	outputPath := filepath.Join(tmpDir, "diag-bundle.zip")
	err := runSupportBundle(t.Context(), supportBundleOptions{
		Output:          outputPath,
		Lines:           50,
		MaxSize:         10 * 1024 * 1024,
		IncludeSessions: false,
		IncludeEvents:   true,
	})
	if err != nil {
		t.Fatalf("runSupportBundle(create) error = %v", err)
	}

	verRes, err := bundle.Verify(outputPath)
	if err != nil {
		t.Fatalf("bundle.Verify() error = %v", err)
	}
	if !verRes.Valid {
		t.Fatalf("bundle.Verify() = %+v, want valid bundle", verRes)
	}
	if verRes.Manifest == nil {
		t.Fatal("expected non-nil manifest")
	}
	if verRes.Manifest.FileCount() < 2 {
		t.Fatalf("FileCount() = %d, want >= 2 (versions.json + doctor.json)", verRes.Manifest.FileCount())
	}
	if verRes.Manifest.TotalSize() <= 0 {
		t.Fatalf("TotalSize() = %d, want > 0", verRes.Manifest.TotalSize())
	}

	// Verify via CLI --verify flag
	if err := runSupportBundle(t.Context(), supportBundleOptions{
		Verify: outputPath,
	}); err != nil {
		t.Fatalf("runSupportBundle(--verify) error = %v", err)
	}
}

func TestSupportBundleVerifyDetectsTamperedArchive(t *testing.T) {
	tmpDir := t.TempDir()
	tamperedPath := filepath.Join(tmpDir, "tampered.zip")

	f, err := os.Create(tamperedPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)

	m := bundle.NewManifest("v1.0.0")
	m.AddFile(bundle.FileEntry{
		Path:        "versions.json",
		SHA256:      bundle.HashBytes([]byte(`{"ntm":"v1.0.0"}`)),
		SizeBytes:   16,
		ContentType: bundle.ContentTypeMetadata,
	})
	mBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	mw, _ := zw.Create(bundle.ManifestFileName)
	_, _ = mw.Write(mBytes)

	// Write tampered payload whose checksum does not match manifest
	vw, _ := zw.Create("versions.json")
	_, _ = vw.Write([]byte(`{"ntm":"tampered"}`))
	_ = zw.Close()
	_ = f.Close()

	err = runSupportBundle(t.Context(), supportBundleOptions{
		Verify:    tamperedPath,
		RobotMode: true,
	})
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSupportBundle(--verify tampered --json) error = %v, want errJSONFailure", err)
	}

	err = runSupportBundle(t.Context(), supportBundleOptions{
		Verify: tamperedPath,
	})
	if err == nil || !strings.Contains(err.Error(), "bundle verification failed") {
		t.Fatalf("runSupportBundle(--verify tampered) error = %v, want verification failure", err)
	}
}
