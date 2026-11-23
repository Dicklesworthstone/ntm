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
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/startup"
)

func isolateSupportBundleToolPATH(t *testing.T) {
	t.Helper()
	toolDir := t.TempDir()
	writeFakeVersionTool(t, toolDir, "tmux", "tmux 3.4")
	t.Setenv("PATH", toolDir)
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(toolDir, "tmux"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
}

func TestSupportBundleEncryptionCommandRoundTrip(t *testing.T) {
	isolateSupportBundleToolPATH(t)
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
	isolateSupportBundleToolPATH(t)
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
	t.Parallel()
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

// The diagnostic override authorizes only the requested export. Exercise the
// production startup hook, because command-local tests cannot catch a global
// persistence policy being disabled before the bundle command runs (bd-vbnrd).
func TestSupportBundleStartupKeepsOtherPersistenceEncrypted(t *testing.T) {
	for _, position := range []string{"after subcommand", "before subcommand"} {
		t.Run(position, func(t *testing.T) {
			isolateSupportBundleToolPATH(t)
			resetFlags()
			t.Cleanup(resetFlags)
			startup.ResetConfig()
			t.Cleanup(startup.ResetConfig)
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
			t.Setenv("NTM_CONFIG", "")
			t.Setenv("NTM_SUPPORT_BUNDLE_MISSING_KEY", "")
			configPath := filepath.Join(dir, "encryption.toml")
			settings := "[encryption]\nenabled = true\nkey_source = 'env'\nkey_env = 'NTM_SUPPORT_BUNDLE_MISSING_KEY'\nkey_format = 'hex'\n"
			if err := os.WriteFile(configPath, []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			oldCfg, oldCfgFile := cfg, cfgFile
			cfg = nil
			t.Cleanup(func() {
				cfg, cfgFile = oldCfg, oldCfgFile
				history.SetEncryptionConfig(nil)
				events.SetEncryptionConfig(nil)
				checkpoint.SetEncryptionConfig(nil)
				bundle.SetEncryptionConfig(nil)
			})
			commandRoot := &cobra.Command{Use: "ntm", PersistentPreRunE: rootCmd.PersistentPreRunE}
			commandRoot.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")
			commandRoot.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
			commandRoot.PersistentFlags().BoolVar(&allowPlaintextFlag, "allow-plaintext", false, "allow plaintext")
			commandRoot.AddCommand(newSupportBundleCmd())
			output := filepath.Join(dir, "diagnostics.zip")
			args := []string{"--config", configPath, "--json"}
			if position == "before subcommand" {
				args = append(args, "--allow-plaintext", "support-bundle")
			} else {
				args = append(args, "support-bundle", "--allow-plaintext")
			}
			args = append(args, "--include-events=false", "--output", output)
			stdout, err := captureStdout(t, func() error {
				commandRoot.SetArgs(args)
				return commandRoot.ExecuteContext(t.Context())
			})
			if err != nil {
				t.Fatalf("explicit plaintext export with unavailable key: %v\n%s", err, stdout)
			}
			var result BundleResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil || !result.Success || result.Encrypted {
				t.Fatalf("plaintext export response: result=%+v error=%v", result, err)
			}
			verified, err := bundle.Verify(output)
			if err != nil || !verified.Valid || verified.Encrypted {
				t.Fatalf("plaintext export invalid: result=%+v error=%v", verified, err)
			}
			if cfg == nil || !cfg.Encryption.Enabled {
				t.Fatal("local export disabled the configured encryption policy")
			}
			err = history.Append(&history.HistoryEntry{ID: "must-refuse", Prompt: "private history", Timestamp: time.Now(), Source: history.SourceCLI})
			if !encryption.IsKind(err, encryption.ErrInvalidKey) {
				t.Fatalf("bundle override authorized unrelated history persistence: %v", err)
			}
			data, err := os.ReadFile(history.StoragePath())
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(data) != 0 {
				t.Fatal("bundle override wrote unrelated history in plaintext")
			}
			storage := &checkpoint.Storage{BaseDir: filepath.Join(dir, "checkpoints")}
			if err := storage.SaveGitStatus("diagnostic-test", "checkpoint", "private status"); err == nil {
				t.Fatal("bundle override authorized unrelated checkpoint persistence")
			}
			statusPath := filepath.Join(storage.CheckpointDir("diagnostic-test", "checkpoint"), checkpoint.GitStatusFile)
			if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
				t.Fatalf("bundle override published unrelated checkpoint: %v", err)
			}
			policy := bundle.ActiveEncryptionConfig()
			if policy == nil || !policy.Enabled || len(policy.EncryptKey) != 0 {
				t.Fatal("bundle override removed the required policy for other bundle generators")
			}
		})
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
	isolateSupportBundleToolPATH(t)
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
