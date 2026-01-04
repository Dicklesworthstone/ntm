package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
		{"single", []string{"single"}},
		{"", []string{}},
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
