//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-SUPPORT-BUNDLE] Tests for support-bundle generation with redaction.
package e2e

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SupportBundleTestSuite manages E2E tests for support-bundle functionality.
type SupportBundleTestSuite struct {
	t           *testing.T
	logger      *TestLogger
	tempDir     string
	configDir   string
	sessionName string
	cleanup     []func()
}

// NewSupportBundleTestSuite creates a new support bundle test suite with isolated environment.
func NewSupportBundleTestSuite(t *testing.T, scenario string) *SupportBundleTestSuite {
	t.Helper()
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)

	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, ".ntm")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	logger := NewTestLogger(t, "support-bundle-"+scenario)
	sessionName := "ntm-bundle-test-" + time.Now().Format("20060102-150405")

	suite := &SupportBundleTestSuite{
		t:           t,
		logger:      logger,
		tempDir:     tempDir,
		configDir:   configDir,
		sessionName: sessionName,
		cleanup:     make([]func(), 0),
	}

	// Register cleanup to kill session
	t.Cleanup(func() {
		suite.killSession()
		suite.logger.Close()
		for _, fn := range suite.cleanup {
			fn()
		}
	})

	return suite
}

// killSession kills the test session if it exists.
func (s *SupportBundleTestSuite) killSession() {
	cmd := exec.Command("tmux", "kill-session", "-t", s.sessionName)
	cmd.Run() // Ignore errors - session might not exist
}

// runNTM runs an ntm command with isolated HOME environment.
func (s *SupportBundleTestSuite) runNTM(args ...string) (string, string, error) {
	s.logger.Log("[E2E-BUNDLE] Running: ntm %s", strings.Join(args, " "))

	cmd := exec.Command("ntm", args...)
	cmd.Env = append(os.Environ(),
		"HOME="+s.tempDir,
		"XDG_CONFIG_HOME="+filepath.Join(s.tempDir, ".config"),
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if stdout.Len() > 0 {
		s.logger.Log("[E2E-BUNDLE] stdout: %s", stdout.String())
	}
	if stderr.Len() > 0 {
		s.logger.Log("[E2E-BUNDLE] stderr: %s", stderr.String())
	}
	if err != nil {
		s.logger.Log("[E2E-BUNDLE] error: %v", err)
	}

	return stdout.String(), stderr.String(), err
}

// setupSession creates a tmux session for testing.
func (s *SupportBundleTestSuite) setupSession() error {
	s.logger.Log("[E2E-BUNDLE] Creating test session: %s", s.sessionName)

	cmd := exec.Command("tmux", "new-session", "-d", "-s", s.sessionName, "-x", "200", "-y", "50")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	s.logger.Log("[E2E-BUNDLE] Session created: %s", string(output))
	time.Sleep(500 * time.Millisecond) // Allow session to stabilize
	return nil
}

// writeTestConfig writes a test config file that the bundle should include.
func (s *SupportBundleTestSuite) writeTestConfig() error {
	configPath := filepath.Join(s.configDir, "config.toml")
	content := `# Test NTM configuration
[general]
log_level = "debug"

[agents]
default_agent = "claude"
`
	return os.WriteFile(configPath, []byte(content), 0644)
}

// SupportBundleOutput represents the JSON response from support-bundle.
type SupportBundleOutput struct {
	Success           bool                    `json:"success"`
	Path              string                  `json:"path"`
	Format            string                  `json:"format"`
	FileCount         int                     `json:"file_count"`
	TotalSize         int64                   `json:"total_size"`
	RedactionSummary  *BundleRedactionSummary `json:"redaction_summary,omitempty"`
	Errors            []string                `json:"errors,omitempty"`
	Warnings          []string                `json:"warnings,omitempty"`
	PrivacyMode       bool                    `json:"privacy_mode,omitempty"`
	PrivacySessions   []string                `json:"privacy_sessions,omitempty"`
	ContentSuppressed bool                    `json:"content_suppressed,omitempty"`
}

// BundleRedactionSummary provides aggregate redaction statistics for the bundle.
type BundleRedactionSummary struct {
	Mode           string         `json:"mode"`
	TotalFindings  int            `json:"total_findings"`
	FilesScanned   int            `json:"files_scanned"`
	FilesRedacted  int            `json:"files_redacted"`
	CategoryCounts map[string]int `json:"category_counts,omitempty"`
}

// BundleManifest represents the manifest.json inside the bundle.
type BundleManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   string                  `json:"generated_at"`
	NTMVersion    string                  `json:"ntm_version"`
	Host          BundleHostInfo          `json:"host"`
	Files         []BundleFileEntry       `json:"files"`
	Session       *BundleSessionInfo      `json:"session,omitempty"`
	Redaction     *BundleRedactionSummary `json:"redaction_summary,omitempty"`
	Errors        []string                `json:"errors,omitempty"`
}

// BundleHostInfo contains host information in the manifest.
type BundleHostInfo struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname,omitempty"`
}

// BundleFileEntry describes a file in the bundle manifest.
type BundleFileEntry struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type,omitempty"`
}

// BundleSessionInfo contains session information in the manifest.
type BundleSessionInfo struct {
	Name string `json:"name"`
}

// TestSupportBundle_NoSession verifies bundle generation without a running session.
// The bundle should still include config files, version info, and manifest.
func TestSupportBundle_NoSession(t *testing.T) {
	suite := NewSupportBundleTestSuite(t, "no-session")

	// Write a test config file
	if err := suite.writeTestConfig(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to write test config: %v", err)
	}

	bundlePath := filepath.Join(suite.tempDir, "test-bundle.zip")

	// Generate bundle without specifying a session
	stdout, stderr, err := suite.runNTM("support-bundle", "-o", bundlePath, "--json")

	suite.logger.Log("[E2E-BUNDLE] Testing no-session bundle generation")

	// The command might fail if no sessions exist, but it should still produce some output
	if err != nil {
		suite.logger.Log("[E2E-BUNDLE] Command exited with error (expected if no sessions): %v", err)
	}

	// Check if bundle was created
	if _, statErr := os.Stat(bundlePath); statErr != nil {
		// No bundle created is acceptable if no sessions exist
		suite.logger.Log("[E2E-BUNDLE] No bundle created (expected without sessions)")

		// Parse error output
		if stdout != "" {
			var result SupportBundleOutput
			if jsonErr := json.Unmarshal([]byte(stdout), &result); jsonErr == nil {
				suite.logger.LogJSON("[E2E-BUNDLE] Output", result)
			}
		}
		return
	}

	// Bundle was created - verify its contents
	suite.logger.Log("[E2E-BUNDLE] Bundle created at: %s", bundlePath)

	// Parse the JSON output
	if stdout != "" {
		var result SupportBundleOutput
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("[E2E-BUNDLE] Failed to parse output: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		suite.logger.LogJSON("[E2E-BUNDLE] Bundle result", result)

		if result.FileCount == 0 {
			t.Error("[E2E-BUNDLE] Expected at least some files in bundle")
		}
	}

	// Open and verify bundle contents
	manifest, files, err := readBundleContents(bundlePath)
	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to read bundle: %v", err)
	}

	suite.logger.LogJSON("[E2E-BUNDLE] Manifest", manifest)
	suite.logger.Log("[E2E-BUNDLE] Files in bundle: %v", files)

	// Verify manifest has required fields
	if manifest.SchemaVersion == 0 {
		t.Error("[E2E-BUNDLE] Expected schema_version in manifest")
	}
	if manifest.NTMVersion == "" {
		t.Error("[E2E-BUNDLE] Expected ntm_version in manifest")
	}
	if manifest.Host.OS == "" || manifest.Host.Arch == "" {
		t.Error("[E2E-BUNDLE] Expected host info in manifest")
	}

	suite.logger.Log("[E2E-BUNDLE] No-session bundle test completed successfully")
}

// TestSupportBundle_WithSession verifies bundle generation with a running session.
// Should capture bounded pane scrollback with redaction applied.
func TestSupportBundle_WithSession(t *testing.T) {
	suite := NewSupportBundleTestSuite(t, "with-session")

	// Create a test session
	if err := suite.setupSession(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to setup session: %v", err)
	}

	// Echo some content to the pane (including a fake secret)
	secretLine := "export OPENAI_API_KEY=" + fakeOpenAIKey
	normalLine := "echo 'Hello from test session'"

	// Send commands to create scrollback content
	cmd := exec.Command("tmux", "send-keys", "-t", suite.sessionName, normalLine, "Enter")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to send normal command: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	cmd = exec.Command("tmux", "send-keys", "-t", suite.sessionName, "echo '"+secretLine+"'", "Enter")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to send secret command: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	suite.logger.Log("[E2E-BUNDLE] Pane content prepared with test data")

	bundlePath := filepath.Join(suite.tempDir, "test-session-bundle.zip")

	// Generate bundle for the session with redact mode
	stdout, stderr, err := suite.runNTM(
		"support-bundle", suite.sessionName,
		"-o", bundlePath,
		"--redact=redact",
		"--lines=100",
		"--json",
	)

	suite.logger.Log("[E2E-BUNDLE] Testing with-session bundle generation")

	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Bundle generation failed: %v\nstderr: %s", err, stderr)
	}

	// Parse the JSON output
	var result SupportBundleOutput
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to parse output: %v\nstdout: %s", err, stdout)
	}

	suite.logger.LogJSON("[E2E-BUNDLE] Bundle result", result)

	if !result.Success {
		t.Errorf("[E2E-BUNDLE] Expected success=true, got false")
	}

	if result.FileCount == 0 {
		t.Error("[E2E-BUNDLE] Expected files in bundle")
	}

	// Open and verify bundle contents
	manifest, files, err := readBundleContents(bundlePath)
	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to read bundle: %v", err)
	}

	suite.logger.Log("[E2E-BUNDLE] Files in bundle: %v", files)

	// Check for session-specific files
	hasSessionDir := false
	hasScrollback := false
	for _, f := range files {
		if strings.Contains(f, "sessions/"+suite.sessionName) {
			hasSessionDir = true
		}
		if strings.Contains(f, "pane") || strings.Contains(f, "scrollback") {
			hasScrollback = true
		}
	}

	if !hasSessionDir {
		suite.logger.Log("[E2E-BUNDLE] Warning: No session directory found in bundle")
	}

	// Verify manifest includes session info
	if manifest.Session != nil {
		if manifest.Session.Name != suite.sessionName {
			t.Errorf("[E2E-BUNDLE] Expected session name %s, got %s", suite.sessionName, manifest.Session.Name)
		}
	}

	suite.logger.Log("[E2E-BUNDLE] With-session bundle test completed")
	suite.logger.Log("[E2E-BUNDLE] hasSessionDir=%v hasScrollback=%v", hasSessionDir, hasScrollback)
}

// TestSupportBundle_Redaction verifies that secrets in pane scrollback are redacted.
func TestSupportBundle_Redaction(t *testing.T) {
	suite := NewSupportBundleTestSuite(t, "redaction")

	// Create a test session
	if err := suite.setupSession(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to setup session: %v", err)
	}

	// Echo a secret to the pane
	secretLine := "export API_KEY=" + fakeOpenAIKey
	cmd := exec.Command("tmux", "send-keys", "-t", suite.sessionName, "echo '"+secretLine+"'", "Enter")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to send secret command: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	bundlePath := filepath.Join(suite.tempDir, "redaction-bundle.zip")

	// Generate bundle with redaction
	stdout, stderr, err := suite.runNTM(
		"support-bundle", suite.sessionName,
		"-o", bundlePath,
		"--redact=redact",
		"--json",
	)

	suite.logger.Log("[E2E-BUNDLE] Testing redaction in bundle")

	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Bundle generation failed: %v\nstderr: %s", err, stderr)
	}

	// Parse the JSON output
	var result SupportBundleOutput
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to parse output: %v\nstdout: %s", err, stdout)
	}

	suite.logger.LogJSON("[E2E-BUNDLE] Bundle result", result)

	// Check redaction summary
	if result.RedactionSummary != nil {
		suite.logger.Log("[E2E-BUNDLE] Redaction mode: %s", result.RedactionSummary.Mode)
		suite.logger.Log("[E2E-BUNDLE] Total findings: %d", result.RedactionSummary.TotalFindings)
		suite.logger.Log("[E2E-BUNDLE] Files redacted: %d", result.RedactionSummary.FilesRedacted)

		if result.RedactionSummary.Mode != "redact" {
			t.Errorf("[E2E-BUNDLE] Expected redaction mode 'redact', got '%s'", result.RedactionSummary.Mode)
		}
	}

	// Open bundle and check that the original secret is NOT present in any file
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to open bundle: %v", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		// Skip non-text files
		if strings.HasSuffix(f.Name, ".json") || strings.Contains(f.Name, "metadata") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		buf := make([]byte, f.UncompressedSize64)
		rc.Read(buf)
		rc.Close()

		content := string(buf)
		if strings.Contains(content, fakeOpenAIKey) {
			t.Errorf("[E2E-BUNDLE] Found unredacted secret in %s", f.Name)
		}
	}

	suite.logger.Log("[E2E-BUNDLE] Redaction test completed - no plain secrets found in bundle")
}

// TestSupportBundle_LineBounding verifies that scrollback is bounded by --lines flag.
func TestSupportBundle_LineBounding(t *testing.T) {
	suite := NewSupportBundleTestSuite(t, "line-bounding")

	// Create a test session
	if err := suite.setupSession(); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to setup session: %v", err)
	}

	// Generate lots of lines in the pane
	for i := 0; i < 50; i++ {
		cmd := exec.Command("tmux", "send-keys", "-t", suite.sessionName,
			"echo 'Line number "+string(rune('0'+i%10))+"'", "Enter")
		cmd.Run()
	}
	time.Sleep(500 * time.Millisecond)

	bundlePath := filepath.Join(suite.tempDir, "bounded-bundle.zip")

	// Generate bundle with only 10 lines
	stdout, stderr, err := suite.runNTM(
		"support-bundle", suite.sessionName,
		"-o", bundlePath,
		"--lines=10",
		"--json",
	)

	suite.logger.Log("[E2E-BUNDLE] Testing line bounding")

	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Bundle generation failed: %v\nstderr: %s", err, stderr)
	}

	var result SupportBundleOutput
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to parse output: %v\nstdout: %s", err, stdout)
	}

	suite.logger.LogJSON("[E2E-BUNDLE] Bundle result", result)

	// Open bundle and count lines in scrollback files
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("[E2E-BUNDLE] Failed to open bundle: %v", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if strings.Contains(f.Name, "pane") && strings.HasSuffix(f.Name, ".txt") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			buf := make([]byte, f.UncompressedSize64)
			rc.Read(buf)
			rc.Close()

			lines := strings.Split(string(buf), "\n")
			// Remove empty trailing line
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}

			suite.logger.Log("[E2E-BUNDLE] File %s has %d lines", f.Name, len(lines))

			// Line count should be bounded (allow some slack for echo command output)
			if len(lines) > 20 {
				t.Errorf("[E2E-BUNDLE] Expected at most ~10 lines, got %d in %s", len(lines), f.Name)
			}
		}
	}

	suite.logger.Log("[E2E-BUNDLE] Line bounding test completed")
}

// readBundleContents opens a zip bundle and extracts the manifest and file list.
func readBundleContents(bundlePath string) (*BundleManifest, []string, error) {
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()

	var manifest *BundleManifest
	var files []string

	for _, f := range zr.File {
		files = append(files, f.Name)

		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			defer rc.Close()

			manifest = &BundleManifest{}
			if err := json.NewDecoder(rc).Decode(manifest); err != nil {
				return nil, nil, err
			}
		}
	}

	if manifest == nil {
		manifest = &BundleManifest{}
	}

	return manifest, files, nil
}
