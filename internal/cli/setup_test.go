package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupCmd(t *testing.T) {
	cmd := newSetupCmd()
	if cmd.Use != "setup" {
		t.Errorf("expected Use to be 'setup', got %q", cmd.Use)
	}

	// Test help doesn't error
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("help command failed: %v", err)
	}

	// Check flags exist
	expectedFlags := []string{"wrappers", "hooks", "force"}
	for _, name := range expectedFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("expected --%s flag", name)
		}
	}

	// Check alias
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "project-init" {
		t.Errorf("expected alias 'project-init', got %v", cmd.Aliases)
	}
}

func TestWriteDefaultConfig(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ntm-setup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	err = writeDefaultConfig(configPath)
	if err != nil {
		t.Fatalf("writeDefaultConfig failed: %v", err)
	}

	// Verify file exists
	if !fileExists(configPath) {
		t.Error("config file should exist")
	}

	// Verify content
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	// Check for expected sections
	checks := []string{
		"session:",
		"agents:",
		"dashboard:",
		"logging:",
	}
	for _, check := range checks {
		if !bytes.Contains(content, []byte(check)) {
			t.Errorf("expected %q in config content", check)
		}
	}
}

func TestWriteDefaultSetupPolicy(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ntm-setup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	policyPath := filepath.Join(tmpDir, "policy.yaml")
	err = writeDefaultSetupPolicy(policyPath)
	if err != nil {
		t.Fatalf("writeDefaultSetupPolicy failed: %v", err)
	}

	// Verify file exists
	if !fileExists(policyPath) {
		t.Error("policy file should exist")
	}

	// Verify content
	content, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("failed to read policy: %v", err)
	}

	// Check for expected sections
	checks := []string{
		"version: 1",
		"automation:",
		"allowed:",
		"blocked:",
		"approval_required:",
		"slb: true",
	}
	for _, check := range checks {
		if !bytes.Contains(content, []byte(check)) {
			t.Errorf("expected %q in policy content", check)
		}
	}
}

func TestEnsureGitignoreEntry(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ntm-setup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gitignorePath := filepath.Join(tmpDir, ".gitignore")

	// Test adding to new file
	if err := os.WriteFile(gitignorePath, []byte("node_modules/\n"), 0644); err != nil {
		t.Fatalf("failed to create gitignore: %v", err)
	}

	err = ensureGitignoreEntry(gitignorePath, ".ntm/")
	if err != nil {
		t.Fatalf("ensureGitignoreEntry failed: %v", err)
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read gitignore: %v", err)
	}

	if !bytes.Contains(content, []byte(".ntm/")) {
		t.Error("expected .ntm/ in gitignore")
	}

	// Test idempotency - should not add duplicate
	err = ensureGitignoreEntry(gitignorePath, ".ntm/")
	if err != nil {
		t.Fatalf("second ensureGitignoreEntry failed: %v", err)
	}

	content2, _ := os.ReadFile(gitignorePath)
	if bytes.Count(content2, []byte(".ntm/")) != 1 {
		t.Error("should not have duplicate .ntm/ entry")
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"a\nb\nc\n", []string{"a", "b", "c"}},
		{"a\nb\nc\n\n", []string{"a", "b", "c"}},
		{"a\r\nb\r\n\r\n", []string{"a", "b"}},
		{"single", []string{"single"}},
		{"", []string{}},
		{"\n\n", []string{}},
	}

	for _, tc := range tests {
		result := splitLines(tc.input)
		if len(result) != len(tc.expected) {
			t.Errorf("splitLines(%q) = %v, want %v", tc.input, result, tc.expected)
			continue
		}
		for i := range result {
			if result[i] != tc.expected[i] {
				t.Errorf("splitLines(%q)[%d] = %q, want %q", tc.input, i, result[i], tc.expected[i])
			}
		}
	}
}

func FuzzSplitLines(f *testing.F) {
	seeds := []string{
		"",
		"a\nb\nc",
		"a\nb\nc\n\n",
		"\n\n",
		"a\r\nb\r\n",
		"line\r\n\r\n",
		"line\rinside\nnext",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			t.Skip()
		}

		lines := splitLines(input)
		if strings.TrimRight(input, "\r\n") == "" && len(lines) != 0 {
			t.Fatalf("expected no lines for all-trailing-line-ending input, got %q", lines)
		}
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			t.Fatalf("last line should not be empty for input %q: %q", input, lines)
		}
		for i, line := range lines {
			if strings.HasSuffix(line, "\r") {
				t.Fatalf("line %d still has trailing CR for input %q: %q", i, input, lines)
			}
		}
	})
}

func TestSetupResponse(t *testing.T) {
	resp := SetupResponse{
		Success:      true,
		ProjectPath:  "/test/project",
		NTMDir:       "/test/project/.ntm",
		CreatedDirs:  []string{".ntm", ".ntm/logs"},
		CreatedFiles: []string{".ntm/config.yaml"},
	}

	if !resp.Success {
		t.Error("expected Success to be true")
	}
	if len(resp.CreatedDirs) != 2 {
		t.Errorf("expected 2 created dirs, got %d", len(resp.CreatedDirs))
	}
}

func TestBuildAgentMailProjectUpdates_ClearsRegisteredAtWhenUnregistered(t *testing.T) {
	now := time.Date(2026, time.April, 3, 2, 20, 0, 0, time.UTC)

	updates := buildAgentMailProjectUpdates("/tmp/project", false, now)
	if updates["agent_mail_registered"] != "false" {
		t.Fatalf("expected registered=false, got %q", updates["agent_mail_registered"])
	}
	if updates["agent_mail_registered_at"] != `""` {
		t.Fatalf("expected registered_at to be cleared, got %q", updates["agent_mail_registered_at"])
	}

	updates = buildAgentMailProjectUpdates("/tmp/project", true, now)
	if updates["agent_mail_registered"] != "true" {
		t.Fatalf("expected registered=true, got %q", updates["agent_mail_registered"])
	}
	if updates["agent_mail_registered_at"] != `"2026-04-03T02:20:00Z"` {
		t.Fatalf("expected registered_at timestamp, got %q", updates["agent_mail_registered_at"])
	}
}

func TestUpdateTomlSection_OverwritesStaleAgentMailRegistrationTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	initial := `[integrations]
agent_mail = true
agent_mail_project_key = "/tmp/project"
agent_mail_registered = true
agent_mail_registered_at = "2026-04-01T00:00:00Z"
`
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	updates := buildAgentMailProjectUpdates("/tmp/project", false, time.Date(2026, time.April, 3, 2, 20, 0, 0, time.UTC))
	if err := updateTomlSection(configPath, "integrations", updates); err != nil {
		t.Fatalf("updateTomlSection failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Contains(content, []byte(`agent_mail_registered = false`)) {
		t.Fatalf("expected agent_mail_registered to be false, got:\n%s", content)
	}
	if !bytes.Contains(content, []byte(`agent_mail_registered_at = ""`)) {
		t.Fatalf("expected stale registered_at to be cleared, got:\n%s", content)
	}
}

func FuzzUpdateTomlSection(f *testing.F) {
	seeds := []string{
		"",
		"[integrations]\nagent_mail_registered = true\n",
		"[project]\nname = \"demo\"\n\n[integrations]\nagent_mail = false\n",
		"# comment\n[other]\nvalue = 1\n",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, initial string) {
		if len(initial) > 4096 {
			t.Skip()
		}

		configPath := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		updates := buildAgentMailProjectUpdates("/tmp/project", false, time.Date(2026, time.April, 3, 2, 20, 0, 0, time.UTC))
		if err := updateTomlSection(configPath, "integrations", updates); err != nil {
			t.Fatalf("updateTomlSection failed: %v", err)
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if len(content) == 0 || content[len(content)-1] != '\n' {
			t.Fatalf("updated config should end with newline, got %q", content)
		}
		for _, expected := range []string{
			"agent_mail = true",
			`agent_mail_project_key = "/tmp/project"`,
			"agent_mail_registered = false",
			`agent_mail_registered_at = ""`,
		} {
			if !strings.Contains(string(content), expected) {
				t.Fatalf("expected %q in updated config, got:\n%s", expected, content)
			}
		}
	})
}

func TestBoolCallWithTimeout_ReturnsValue(t *testing.T) {
	value, timedOut := boolCallWithTimeout(50*time.Millisecond, func() bool {
		return true
	})
	if timedOut {
		t.Fatal("expected function to return before timeout")
	}
	if !value {
		t.Fatal("expected true value from callback")
	}
}

func TestBoolCallWithTimeout_TimesOut(t *testing.T) {
	unblock := make(chan struct{})
	value, timedOut := boolCallWithTimeout(10*time.Millisecond, func() bool {
		<-unblock
		return true
	})
	close(unblock)
	if !timedOut {
		t.Fatal("expected timeout for blocked callback")
	}
	if value {
		t.Fatal("expected false value when timeout occurs")
	}
}
