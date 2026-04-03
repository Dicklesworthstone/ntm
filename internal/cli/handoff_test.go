package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
)

func TestNewHandoffCmd(t *testing.T) {
	cmd := newHandoffCmd()
	if cmd == nil {
		t.Fatal("newHandoffCmd() returned nil")
	}
	if cmd.Use != "handoff" {
		t.Errorf("Use = %q, want %q", cmd.Use, "handoff")
	}
	if !cmd.HasSubCommands() {
		t.Error("expected handoff command to have subcommands")
	}
}

func TestNewHandoffCreateCmd(t *testing.T) {
	cmd := newHandoffCreateCmd()
	if cmd == nil {
		t.Fatal("newHandoffCreateCmd() returned nil")
	}
	if cmd.Use != "create [session]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "create [session]")
	}

	// Check flags exist
	flags := []string{"goal", "now", "from-file", "auto", "description", "json"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag %q to exist", name)
		}
	}
}

func TestNewHandoffListCmd(t *testing.T) {
	cmd := newHandoffListCmd()
	if cmd == nil {
		t.Fatal("newHandoffListCmd() returned nil")
	}
	if cmd.Use != "list [session]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "list [session]")
	}

	// Check flags exist
	flags := []string{"limit", "json"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag %q to exist", name)
		}
	}
}

func TestNewHandoffShowCmd(t *testing.T) {
	cmd := newHandoffShowCmd()
	if cmd == nil {
		t.Fatal("newHandoffShowCmd() returned nil")
	}
	if cmd.Use != "show <path>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "show <path>")
	}

	// Check flags exist
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected flag 'json' to exist")
	}
}

func TestNewHandoffLedgerCmd(t *testing.T) {
	cmd := newHandoffLedgerCmd()
	if cmd == nil {
		t.Fatal("newHandoffLedgerCmd() returned nil")
	}
	if cmd.Use != "ledger [session]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "ledger [session]")
	}
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected flag 'json' to exist")
	}
}

func TestGenerateDescription(t *testing.T) {
	tests := []struct {
		goal     string
		expected string
	}{
		{"", "handoff"},
		{"Implemented authentication", "implemented-authentication"},
		{"Fixed bug in the API handler", "fixed-bug-in-the-api-handler"},
		{"A VERY LONG GOAL THAT EXCEEDS THE LIMIT", "a-very-long-goal-that-exceeds"},
		{"With  multiple   spaces", "with-multiple-spaces"},
		{"Special!@#$%^&*()chars", "specialchars"},
		{"kebab-case-already", "kebab-case-already"},
	}

	for _, tc := range tests {
		t.Run(tc.goal, func(t *testing.T) {
			got := generateDescription(tc.goal)
			if got != tc.expected {
				t.Errorf("generateDescription(%q) = %q, want %q", tc.goal, got, tc.expected)
			}
		})
	}
}

func TestRunHandoffLedgerTextOutput(t *testing.T) {
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, ".ntm", "ledgers")
	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}

	ledgerPath := filepath.Join(ledgerDir, "CONTINUITY_testsession.md")
	content := "## 2026-01-01T00:00:00Z (manual)\n- goal: test\n\n"
	if err := os.WriteFile(ledgerPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write ledger: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffLedger(cmd, "testsession", false); err != nil {
		t.Fatalf("runHandoffLedger() error: %v", err)
	}

	if got := buf.String(); got != content {
		t.Errorf("unexpected ledger output: %q", got)
	}
}

func TestRunHandoffLedgerJSONOutput(t *testing.T) {
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, ".ntm", "ledgers")
	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}

	ledgerPath := filepath.Join(ledgerDir, "CONTINUITY_testsession.md")
	content := "## 2026-01-01T00:00:00Z (manual)\n- goal: test\n\n"
	if err := os.WriteFile(ledgerPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write ledger: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffLedger(cmd, "testsession", true); err != nil {
		t.Fatalf("runHandoffLedger() error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal json: %v", err)
	}

	if payload["session"] != "testsession" {
		t.Errorf("session = %v, want testsession", payload["session"])
	}
	if payload["path"] == "" {
		t.Error("expected path to be set")
	}
	if payload["content"] != content {
		t.Errorf("content mismatch: %q", payload["content"])
	}
}

func TestRunHandoffLedgerInvalidSession(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffLedger(cmd, "../bad", false); err == nil {
		t.Fatal("expected error for invalid session name")
	}
}

func TestTruncateForDisplay(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 10, "hello w..."},
		{"hi", 10, "hi"},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
		{"", 10, ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := truncateForDisplay(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Errorf("truncateForDisplay(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.expected)
			}
		})
	}
}

func TestSplitAndTrim(t *testing.T) {
	tests := []struct {
		input    string
		sep      string
		expected []string
	}{
		{"a,b,c", ",", []string{"a", "b", "c"}},
		{"a, b , c", ",", []string{"a", "b", "c"}},
		{" a , b , c ", ",", []string{"a", "b", "c"}},
		{"", ",", []string{}},
		{"a,,b", ",", []string{"a", "b"}}, // Empty entries removed
		{"single", ",", []string{"single"}},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := splitAndTrim(tc.input, tc.sep)
			if len(got) != len(tc.expected) {
				t.Errorf("splitAndTrim(%q, %q) length = %d, want %d", tc.input, tc.sep, len(got), len(tc.expected))
				return
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("splitAndTrim(%q, %q)[%d] = %q, want %q", tc.input, tc.sep, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestRunHandoffCreateWithFlags(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run create with flags
	err = runHandoffCreate(cmd, "testsession", "Test goal", "Next task", "", false, "test-desc", false, "", "yaml", false)
	if err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "Handoff created:") {
		t.Errorf("expected output to contain 'Handoff created:', got: %s", output)
	}
	if !strings.Contains(output, "testsession") {
		t.Errorf("expected output to contain session name, got: %s", output)
	}

	// Verify file was created
	reader := handoff.NewReader(tmpDir)
	h, path, err := reader.FindLatest("testsession")
	if err != nil {
		t.Fatalf("FindLatest() error: %v", err)
	}
	if h == nil {
		t.Fatal("expected handoff to be created")
	}
	if h.Goal != "Test goal" {
		t.Errorf("Goal = %q, want %q", h.Goal, "Test goal")
	}
	if h.Now != "Next task" {
		t.Errorf("Now = %q, want %q", h.Now, "Next task")
	}
	if !strings.Contains(path, "test-desc") {
		t.Errorf("expected path to contain 'test-desc', got: %s", path)
	}
}

func TestRunHandoffCreateJSONOutput(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run create with JSON output
	err = runHandoffCreate(cmd, "testsession", "Test goal", "Next task", "", false, "", true, "", "json", false)
	if err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	// Verify JSON output
	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["success"] != true {
		t.Errorf("expected success = true, got %v", result["success"])
	}
	if result["session"] != "testsession" {
		t.Errorf("session = %q, want %q", result["session"], "testsession")
	}
	if result["goal"] != "Test goal" {
		t.Errorf("goal = %q, want %q", result["goal"], "Test goal")
	}
	if result["now"] != "Next task" {
		t.Errorf("now = %q, want %q", result["now"], "Next task")
	}
	if result["path"] == nil || result["path"] == "" {
		t.Error("expected path to be set")
	}
}

func TestRunHandoffCreateUsesProjectRootFromSubdir(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	subDir := filepath.Join(tmpDir, "nested", "deeper")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffCreate(cmd, "testsession", "Test goal", "Next task", "", false, "root-check", false, "", "yaml", false); err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	reader := handoff.NewReader(tmpDir)
	h, path, err := reader.FindLatest("testsession")
	if err != nil {
		t.Fatalf("FindLatest() error: %v", err)
	}
	if h == nil {
		t.Fatal("expected handoff to be created at project root")
	}
	if !strings.HasPrefix(path, filepath.Join(tmpDir, ".ntm", "handoffs")) {
		t.Fatalf("expected handoff path under project root, got %s", path)
	}
}

func TestRunHandoffCreateUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "testsession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffCreate(cmd, "testsession", "Scoped goal", "Scoped next", "", false, "session-scope", false, "", "yaml", false); err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	reader := handoff.NewReader(projectDir)
	h, path, err := reader.FindLatest("testsession")
	if err != nil {
		t.Fatalf("FindLatest() error: %v", err)
	}
	if h == nil {
		t.Fatal("expected handoff to be created under session project")
	}
	if !strings.HasPrefix(path, filepath.Join(projectDir, ".ntm", "handoffs")) {
		t.Fatalf("expected handoff path under session project, got %s", path)
	}
}

func TestRunHandoffCreateRejectsInvalidSessionBeforePathResolution(t *testing.T) {
	projectsBase := t.TempDir()
	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmd := &cobra.Command{}
	err := runHandoffCreate(cmd, "../escape", "Goal", "Now", "", false, "invalid", false, "", "yaml", false)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunHandoffList(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a handoff first
	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Test goal"
	h.Now = "Next task"
	h.Status = handoff.StatusComplete
	_, err = writer.Write(h, "test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run list for session
	err = runHandoffList(cmd, "testsession", 10, false)
	if err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "testsession") {
		t.Errorf("expected output to contain session name, got: %s", output)
	}
	if !strings.Contains(output, "Test goal") {
		t.Errorf("expected output to contain goal, got: %s", output)
	}
}

func TestRunHandoffListUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "testsession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	writer := handoff.NewWriter(projectDir)
	h := handoff.New("testsession")
	h.Goal = "Scoped goal"
	h.Now = "Scoped now"
	h.Status = handoff.StatusComplete
	if _, err := writer.Write(h, "scoped"); err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffList(cmd, "testsession", 10, false); err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Scoped goal") {
		t.Fatalf("expected output to contain session-project handoff, got: %s", output)
	}
}

func TestRunHandoffListRejectsInvalidSessionBeforePathResolution(t *testing.T) {
	projectsBase := t.TempDir()
	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmd := &cobra.Command{}
	err := runHandoffList(cmd, "../escape", 10, false)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunHandoffLedgerUsesProjectRootFromSubdir(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(filepath.Join(configDir, "ledgers"), 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	content := "## 2026-01-01T00:00:00Z (manual)\n- goal: nested\n\n"
	if err := os.WriteFile(filepath.Join(configDir, "ledgers", "CONTINUITY_testsession.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write ledger: %v", err)
	}
	subDir := filepath.Join(tmpDir, "nested", "deeper")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runHandoffLedger(cmd, "testsession", false); err != nil {
		t.Fatalf("runHandoffLedger() error: %v", err)
	}
	if got := buf.String(); got != content {
		t.Fatalf("unexpected ledger output: %q", got)
	}
}

func TestRunHandoffListSessions(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create handoffs for two sessions
	writer := handoff.NewWriter(tmpDir)

	h1 := handoff.New("session1")
	h1.Goal = "Goal 1"
	h1.Now = "Now 1"
	h1.Status = handoff.StatusComplete
	_, err = writer.Write(h1, "test1")
	if err != nil {
		t.Fatalf("failed to write handoff 1: %v", err)
	}

	h2 := handoff.New("session2")
	h2.Goal = "Goal 2"
	h2.Now = "Now 2"
	h2.Status = handoff.StatusComplete
	_, err = writer.Write(h2, "test2")
	if err != nil {
		t.Fatalf("failed to write handoff 2: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run list without session (should list sessions)
	err = runHandoffList(cmd, "", 10, false)
	if err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	// Verify output contains both sessions
	output := buf.String()
	if !strings.Contains(output, "session1") {
		t.Errorf("expected output to contain 'session1', got: %s", output)
	}
	if !strings.Contains(output, "session2") {
		t.Errorf("expected output to contain 'session2', got: %s", output)
	}
}

func TestRunHandoffListJSONOutput(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a handoff first
	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Test goal"
	h.Now = "Next task"
	h.Status = handoff.StatusComplete
	_, err = writer.Write(h, "test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run list with JSON output
	err = runHandoffList(cmd, "testsession", 10, true)
	if err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	// Verify JSON output
	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["session"] != "testsession" {
		t.Errorf("session = %q, want %q", result["session"], "testsession")
	}
	handoffs := result["handoffs"].([]interface{})
	if len(handoffs) != 1 {
		t.Errorf("expected 1 handoff, got %d", len(handoffs))
	}
}

func TestRunHandoffShow(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a handoff
	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Test goal for show"
	h.Now = "Next task for show"
	h.Status = handoff.StatusComplete
	h.Outcome = handoff.OutcomeSucceeded
	h.Blockers = []string{"Blocker 1", "Blocker 2"}
	h.Decisions = map[string]string{"arch": "microservices"}
	h.Next = []string{"Step 1", "Step 2"}
	path, err := writer.Write(h, "test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run show
	err = runHandoffShow(cmd, path, false)
	if err != nil {
		t.Fatalf("runHandoffShow() error: %v", err)
	}

	// Verify output
	output := buf.String()
	expectedParts := []string{
		"Handoff:",
		"Session: testsession",
		"Goal: Test goal for show",
		"Now: Next task for show",
		"Status: complete",
		"Blockers:",
		"Blocker 1",
		"Decisions:",
		"arch: microservices",
		"Next Steps:",
		"Step 1",
	}
	for _, part := range expectedParts {
		if !strings.Contains(output, part) {
			t.Errorf("expected output to contain %q, got: %s", part, output)
		}
	}
}

func TestRunHandoffShowJSONOutput(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a handoff
	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Test goal"
	h.Now = "Next task"
	h.Status = handoff.StatusComplete
	path, err := writer.Write(h, "test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run show with JSON output
	err = runHandoffShow(cmd, path, true)
	if err != nil {
		t.Fatalf("runHandoffShow() error: %v", err)
	}

	// Verify JSON output
	var result handoff.Handoff
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result.Session != "testsession" {
		t.Errorf("Session = %q, want %q", result.Session, "testsession")
	}
	if result.Goal != "Test goal" {
		t.Errorf("Goal = %q, want %q", result.Goal, "Test goal")
	}
	if result.Now != "Next task" {
		t.Errorf("Now = %q, want %q", result.Now, "Next task")
	}
}

func TestRunHandoffCreateFromFile(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a source handoff file first
	writer := handoff.NewWriter(tmpDir)
	sourceHandoff := handoff.New("sourcesession")
	sourceHandoff.Goal = "Source goal"
	sourceHandoff.Now = "Source now"
	sourceHandoff.Status = handoff.StatusComplete
	sourceHandoff.Blockers = []string{"Blocker from file"}
	sourcePath, err := writer.Write(sourceHandoff, "source")
	if err != nil {
		t.Fatalf("failed to write source handoff: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run create from file, overriding session name
	err = runHandoffCreate(cmd, "newsession", "", "", sourcePath, false, "from-file", false, "", "yaml", false)
	if err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	// Verify the new handoff was created with new session name
	reader := handoff.NewReader(tmpDir)
	h, _, err := reader.FindLatest("newsession")
	if err != nil {
		t.Fatalf("FindLatest() error: %v", err)
	}
	if h == nil {
		t.Fatal("expected handoff to be created")
	}
	if h.Session != "newsession" {
		t.Errorf("Session = %q, want %q", h.Session, "newsession")
	}
	if h.Goal != "Source goal" {
		t.Errorf("Goal = %q, want %q", h.Goal, "Source goal")
	}
	if len(h.Blockers) != 1 || h.Blockers[0] != "Blocker from file" {
		t.Errorf("Blockers not preserved: %v", h.Blockers)
	}
}

func TestRunHandoffCreateFromFileUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "newsession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	sourceDir := t.TempDir()
	writer := handoff.NewWriter(sourceDir)
	sourceHandoff := handoff.New("sourcesession")
	sourceHandoff.Goal = "Source goal"
	sourceHandoff.Now = "Source now"
	sourceHandoff.Status = handoff.StatusComplete
	sourcePath, err := writer.Write(sourceHandoff, "source")
	if err != nil {
		t.Fatalf("failed to write source handoff: %v", err)
	}

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	cmd := &cobra.Command{}
	if err := runHandoffCreate(cmd, "newsession", "", "", sourcePath, false, "from-file", false, "", "yaml", false); err != nil {
		t.Fatalf("runHandoffCreate() error: %v", err)
	}

	reader := handoff.NewReader(projectDir)
	h, path, err := reader.FindLatest("newsession")
	if err != nil {
		t.Fatalf("FindLatest() error: %v", err)
	}
	if h == nil {
		t.Fatal("expected handoff to be created under session project")
	}
	if !strings.HasPrefix(path, filepath.Join(projectDir, ".ntm", "handoffs")) {
		t.Fatalf("expected handoff path under session project, got %s", path)
	}
}

func TestRunHandoffListLimit(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create multiple handoffs
	writer := handoff.NewWriter(tmpDir)
	for i := 0; i < 5; i++ {
		h := handoff.New("testsession")
		h.Goal = "Goal " + string(rune('A'+i))
		h.Now = "Now " + string(rune('A'+i))
		h.Status = handoff.StatusComplete
		_, err = writer.Write(h, "test-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("failed to write handoff %d: %v", i, err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run list with limit
	err = runHandoffList(cmd, "testsession", 2, true)
	if err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	// Verify JSON output has limited results
	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	handoffs := result["handoffs"].([]interface{})
	if len(handoffs) != 2 {
		t.Errorf("expected 2 handoffs with limit, got %d", len(handoffs))
	}
}

func TestRunHandoffShowRelativePath(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a handoff
	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Test goal"
	h.Now = "Next task"
	h.Status = handoff.StatusComplete
	path, err := writer.Write(h, "test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	// Get relative path
	relPath, err := filepath.Rel(tmpDir, path)
	if err != nil {
		t.Fatalf("failed to get relative path: %v", err)
	}

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run show with relative path
	err = runHandoffShow(cmd, relPath, false)
	if err != nil {
		t.Fatalf("runHandoffShow() with relative path error: %v", err)
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "Test goal") {
		t.Errorf("expected output to contain goal, got: %s", output)
	}
}

func TestRunHandoffShowRelativePathFromSubdir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "nested", "deeper")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	writer := handoff.NewWriter(tmpDir)
	h := handoff.New("testsession")
	h.Goal = "Nested relative goal"
	h.Now = "Nested relative now"
	h.Status = handoff.StatusComplete
	path, err := writer.Write(h, "nested-test")
	if err != nil {
		t.Fatalf("failed to write handoff: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	relPath, err := filepath.Rel(subDir, path)
	if err != nil {
		t.Fatalf("failed to get relative path: %v", err)
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err = runHandoffShow(cmd, relPath, false)
	if err != nil {
		t.Fatalf("runHandoffShow() with nested relative path error: %v", err)
	}

	if !strings.Contains(buf.String(), "Nested relative goal") {
		t.Fatalf("expected output to contain nested handoff goal, got: %s", buf.String())
	}
}

func TestResolveHandoffProjectDirRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveHandoffProjectDir("../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveHandoffProjectDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir workspace git dir: %v", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	_, err := resolveHandoffProjectDir("mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveHandoffProjectDirUsesSavedSessionAgentProjectKey(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	session := "testsession"
	actualProject := filepath.Join(t.TempDir(), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, session, actualProject, "GreenCastle")

	projectDir, err := resolveHandoffProjectDir(session)
	if err != nil {
		t.Fatalf("resolveHandoffProjectDir() error = %v", err)
	}
	if projectDir != actualProject {
		t.Fatalf("resolveHandoffProjectDir() = %q, want saved session agent project %q", projectDir, actualProject)
	}
}

func TestResolveHandoffProjectDirResolvesProjectScopedPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	origCfg := cfg
	origDir, _ := os.Getwd()
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	got, err := resolveHandoffProjectDir("mypro")
	if err != nil {
		t.Fatalf("resolveHandoffProjectDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("resolveHandoffProjectDir() = %q, want %q", got, projectDir)
	}
}

func TestRunHandoffListNoHandoffs(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Create a test command with buffer for output
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run list for non-existent session
	err = runHandoffList(cmd, "nonexistent", 10, false)
	if err != nil {
		t.Fatalf("runHandoffList() error: %v", err)
	}

	// Verify output indicates no handoffs
	output := buf.String()
	if !strings.Contains(output, "No handoffs found") {
		t.Errorf("expected output to indicate no handoffs, got: %s", output)
	}
}

func TestRunHandoffCreateValidation(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "handoff-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp dir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	// Run create with goal but without now should still work (uses defaults)
	err = runHandoffCreate(cmd, "testsession", "Test goal", "Task now", "", false, "", false, "", "yaml", false)
	if err != nil {
		t.Fatalf("runHandoffCreate() with goal and now should succeed: %v", err)
	}
}
