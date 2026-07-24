//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM.
// [E2E-ENCRYPTION] Encryption-at-rest for prompt history and event logs.
//
// These tests drive the real ntm binary against hermetic HOME/XDG directories so
// they never touch a developer's config, history, or keys. Every key is a
// synthetic fixture generated in-process.
package e2e

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
)

// plaintextMarker is the synthetic needle searched for in raw artifact bytes. If
// it survives into an artifact while encryption is enabled, the artifact is
// plaintext and the test must fail.
const plaintextMarker = "SYNTHETIC-PLAINTEXT-MARKER-8f3a2c"

// encryptionFixture owns the hermetic environment for one encryption scenario.
type encryptionFixture struct {
	t          *testing.T
	logger     *TestLogger
	runID      string
	bin        string
	home       string
	dataHome   string
	configPath string
	keyEnv     string
	keyHex     string
}

// resolveNTMBin honors the same E2E_NTM_BIN override the scenario harness uses so
// a locally built binary can be exercised without installing it.
func resolveNTMBin() string {
	if override := strings.TrimSpace(os.Getenv("E2E_NTM_BIN")); override != "" {
		return override
	}
	return "ntm"
}

func newEncryptionFixture(t *testing.T, scenario string) *encryptionFixture {
	t.Helper()

	logger := NewTestLogger(t, scenario)
	t.Cleanup(logger.Close)

	root := t.TempDir()
	fixture := &encryptionFixture{
		t:          t,
		logger:     logger,
		runID:      fmt.Sprintf("%s-%d", scenario, time.Now().UnixNano()),
		bin:        resolveNTMBin(),
		home:       filepath.Join(root, "home"),
		dataHome:   filepath.Join(root, "data"),
		configPath: filepath.Join(root, "config.toml"),
		keyEnv:     "NTM_E2E_ENCRYPTION_KEY",
		keyHex:     hex.EncodeToString(randomKey(t)),
	}

	for _, dir := range []string{fixture.home, fixture.dataHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("[E2E-ENCRYPTION] create %s: %v", dir, err)
		}
	}

	logger.Log("[E2E-ENCRYPTION] test_run_id=%s ntm_bin=%s", fixture.runID, fixture.bin)
	logger.Log("[E2E-ENCRYPTION] HOME=%s XDG_DATA_HOME=%s", fixture.home, fixture.dataHome)
	logger.Log("[E2E-ENCRYPTION] config=%s key_env=%s key=<redacted %d hex chars>",
		fixture.configPath, fixture.keyEnv, len(fixture.keyHex))

	return fixture
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("[E2E-ENCRYPTION] generate synthetic key: %v", err)
	}
	return key
}

// writeConfig writes an encryption config. An empty keyEnv disables the
// [encryption] section entirely.
func (f *encryptionFixture) writeConfig(enabled bool) {
	f.t.Helper()

	body := "[encryption]\nenabled = false\n"
	if enabled {
		body = fmt.Sprintf("[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = %q\nkey_format = \"hex\"\n", f.keyEnv)
	}
	if err := os.WriteFile(f.configPath, []byte(body), 0o600); err != nil {
		f.t.Fatalf("[E2E-ENCRYPTION] write config: %v", err)
	}
	f.logger.Log("[E2E-ENCRYPTION] config written (encryption_enabled=%t):\n%s", enabled, body)
}

// run executes the ntm binary in the hermetic environment. keyValue is the value
// exported as the key env var; pass "" to simulate a missing key.
func (f *encryptionFixture) run(keyValue string, args ...string) (string, string, error) {
	f.t.Helper()

	full := append([]string{"--config", f.configPath}, args...)
	cmd := exec.Command(f.bin, full...)
	cmd.Dir = f.home
	cmd.Env = append(os.Environ(),
		"HOME="+f.home,
		"XDG_CONFIG_HOME="+filepath.Join(f.home, ".config"),
		"XDG_DATA_HOME="+f.dataHome,
		"NTM_CONFIG=",
		"NTM_NO_COLOR=1",
		f.keyEnv+"="+keyValue,
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	f.logger.Log("[E2E-ENCRYPTION] run: ntm %s", strings.Join(full, " "))
	f.logger.Log("[E2E-ENCRYPTION] exit_err=%v stdout_bytes=%d stderr=%s", err, stdout.Len(), stderr.String())

	return stdout.String(), stderr.String(), err
}

func (f *encryptionFixture) historyPath() string {
	return filepath.Join(f.dataHome, "ntm", "history.jsonl")
}

func (f *encryptionFixture) eventLogPath() string {
	return filepath.Join(f.home, ".config", "ntm", "analytics", "events.jsonl")
}

// seedPlaintextHistory writes count plaintext history entries, each carrying the
// synthetic marker, exactly as a pre-encryption NTM would have left them.
func (f *encryptionFixture) seedPlaintextHistory(count int) {
	f.t.Helper()

	path := f.historyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("[E2E-ENCRYPTION] create history dir: %v", err)
	}

	var buf strings.Builder
	for i := 0; i < count; i++ {
		entry := history.HistoryEntry{
			ID:        fmt.Sprintf("seed-%03d", i),
			Timestamp: time.Now().UTC().Add(time.Duration(i-count) * time.Minute),
			Session:   "e2e-encryption",
			Targets:   []string{"1"},
			Prompt:    fmt.Sprintf("%s entry %d", plaintextMarker, i),
			Source:    history.SourceCLI,
			Success:   true,
		}
		line, err := json.Marshal(entry)
		if err != nil {
			f.t.Fatalf("[E2E-ENCRYPTION] marshal seed entry: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		f.t.Fatalf("[E2E-ENCRYPTION] write seed history: %v", err)
	}

	f.logger.Log("[E2E-ENCRYPTION] seeded %d plaintext history entries at %s", count, path)
	f.describeArtifact("history (seeded plaintext)", path)
}

// describeArtifact logs the size, sha256, and plaintext-marker presence of an
// on-disk artifact. The digest is of whatever bytes are on disk, so for an
// encrypted artifact it is a ciphertext digest.
func (f *encryptionFixture) describeArtifact(label, path string) []byte {
	f.t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatalf("[E2E-ENCRYPTION] read %s (%s): %v", label, path, err)
	}
	f.logger.Log("[E2E-ENCRYPTION] artifact=%q path=%s size=%d sha256=%x marker_present=%t",
		label, path, len(data), sha256.Sum256(data), strings.Contains(string(data), plaintextMarker))
	return data
}

// TestEncryptionAtRestHistoryIsNotPlaintext proves that with encryption enabled a
// history rewrite produces ciphertext on disk while `ntm history` still shows the
// original prompts.
func TestEncryptionAtRestHistoryIsNotPlaintext(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	f := newEncryptionFixture(t, "encryption-history-not-plaintext")
	f.writeConfig(true)
	f.seedPlaintextHistory(4)

	// Pruning rewrites the whole file through the encrypting write path.
	if _, _, err := f.run(f.keyHex, "history", "prune", "--keep=2"); err != nil {
		t.Fatalf("[E2E-ENCRYPTION] history prune failed: %v", err)
	}

	raw := f.describeArtifact("history (after encrypted rewrite)", f.historyPath())
	if len(raw) == 0 {
		t.Fatal("[E2E-ENCRYPTION] history file is empty after prune; the rewrite lost every entry")
	}
	if strings.Contains(string(raw), plaintextMarker) {
		t.Fatalf("[E2E-ENCRYPTION] plaintext marker %q survived into %s: artifact is not encrypted",
			plaintextMarker, f.historyPath())
	}
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if !encryption.IsEncryptedLine([]byte(line)) {
			t.Fatalf("[E2E-ENCRYPTION] history line %d is still plaintext JSON: %q", i, line)
		}
	}

	// The same binary must decrypt for display.
	stdout, _, err := f.run(f.keyHex, "--json", "history", "--limit", "10")
	if err != nil {
		t.Fatalf("[E2E-ENCRYPTION] history read-back failed: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, plaintextMarker) {
		t.Fatalf("[E2E-ENCRYPTION] ntm history did not decrypt for display; stdout=%q", stdout)
	}
	f.logger.Log("[E2E-ENCRYPTION] ntm history decrypted %d bytes of output containing the marker", len(stdout))
}

// TestEncryptionAtRestEventLogReadableViaAnalytics proves `ntm analytics` reads an
// encrypted event log instead of silently reporting zero events.
func TestEncryptionAtRestEventLogReadableViaAnalytics(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	f := newEncryptionFixture(t, "encryption-event-log-analytics")
	f.writeConfig(true)

	key, err := hex.DecodeString(f.keyHex)
	if err != nil {
		t.Fatalf("[E2E-ENCRYPTION] decode synthetic key: %v", err)
	}

	logPath := f.eventLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("[E2E-ENCRYPTION] create analytics dir: %v", err)
	}

	sessionName := plaintextMarker + "-session"
	seeded := []*events.Event{
		events.NewEvent(events.EventSessionCreate, sessionName, map[string]interface{}{"agents": 2}),
		events.NewEvent(events.EventPromptSend, sessionName, map[string]interface{}{"targets": 2}),
	}

	var buf strings.Builder
	for _, event := range seeded {
		plain, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("[E2E-ENCRYPTION] marshal seed event: %v", err)
		}
		line, err := encryption.EncryptLine(key, plain)
		if err != nil {
			t.Fatalf("[E2E-ENCRYPTION] encrypt seed event: %v", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("[E2E-ENCRYPTION] write encrypted event log: %v", err)
	}

	raw := f.describeArtifact("event log (encrypted)", logPath)
	if strings.Contains(string(raw), plaintextMarker) {
		t.Fatalf("[E2E-ENCRYPTION] encrypted event log still contains %q", plaintextMarker)
	}

	stdout, stderr, err := f.run(f.keyHex, "analytics", "--format", "json", "--sessions", "--days", "1")
	if err != nil {
		t.Fatalf("[E2E-ENCRYPTION] analytics failed: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	var stats struct {
		TotalSessions int `json:"total_sessions"`
		TotalPrompts  int `json:"total_prompts"`
		Sessions      []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("[E2E-ENCRYPTION] analytics output is not JSON: %v (stdout=%q)", err, stdout)
	}
	f.logger.LogJSON("analytics stats", stats)

	if stats.TotalSessions != 1 || stats.TotalPrompts != 1 {
		t.Fatalf("[E2E-ENCRYPTION] analytics reported sessions=%d prompts=%d, want 1/1; encrypted lines were skipped instead of decrypted",
			stats.TotalSessions, stats.TotalPrompts)
	}
	if len(stats.Sessions) != 1 || stats.Sessions[0].Name != sessionName {
		t.Fatalf("[E2E-ENCRYPTION] analytics sessions=%+v, want one entry named %q", stats.Sessions, sessionName)
	}
}

// TestEncryptionAtRestFailsClosedOnMissingKey proves NTM refuses to run rather
// than downgrading to plaintext persistence when the key cannot be resolved.
func TestEncryptionAtRestFailsClosedOnMissingKey(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	f := newEncryptionFixture(t, "encryption-fails-closed-missing-key")
	f.writeConfig(true)
	f.seedPlaintextHistory(4)

	before := f.describeArtifact("history (before failed prune)", f.historyPath())

	// Empty key value simulates an unset NTM_E2E_ENCRYPTION_KEY.
	stdout, stderr, err := f.run("", "history", "prune", "--keep=2")
	if err == nil {
		t.Fatalf("[E2E-ENCRYPTION] expected a non-zero exit with an unresolvable key; stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "encryption key resolution failed") {
		t.Fatalf("[E2E-ENCRYPTION] stderr did not report the key failure: %q", stderr)
	}
	if strings.Contains(stderr, "encryption disabled") {
		t.Fatalf("[E2E-ENCRYPTION] NTM downgraded to unencrypted persistence: %q", stderr)
	}

	after := f.describeArtifact("history (after failed prune)", f.historyPath())
	if string(before) != string(after) {
		t.Fatal("[E2E-ENCRYPTION] history was rewritten even though key resolution failed")
	}
}
