package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
)

func TestNewCheckpointCmd(t *testing.T) {
	cmd := newCheckpointCmd()

	if cmd.Use != "checkpoint" {
		t.Errorf("Use = %q, want %q", cmd.Use, "checkpoint")
	}

	// Verify subcommands are registered
	subcommands := cmd.Commands()
	names := make(map[string]bool)
	for _, sub := range subcommands {
		names[sub.Use] = true
	}

	expected := []string{
		"save <session>",
		"list [session]",
		"show <session> <id>",
		"restore <session> [checkpoint-id]",
		"delete <session> <id>",
	}
	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("missing subcommand %q", exp)
		}
	}
}

func TestNewCheckpointSaveCmd(t *testing.T) {
	cmd := newCheckpointSaveCmd()

	if cmd.Use != "save <session>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "save <session>")
	}

	// Verify flags exist
	flags := []string{"message", "scrollback", "no-git"}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}
}

func TestNewCheckpointListCmd(t *testing.T) {
	cmd := newCheckpointListCmd()

	if cmd.Use != "list [session]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "list [session]")
	}
}

func TestNewCheckpointShowCmd(t *testing.T) {
	cmd := newCheckpointShowCmd()

	if cmd.Use != "show <session> <id>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "show <session> <id>")
	}
}

func TestNewCheckpointDeleteCmd(t *testing.T) {
	cmd := newCheckpointDeleteCmd()

	if cmd.Use != "delete <session> <id>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "delete <session> <id>")
	}

	// Verify force flag exists
	if cmd.Flags().Lookup("force") == nil {
		t.Error("missing force flag")
	}
}

func TestNewCheckpointRestoreCmd(t *testing.T) {
	cmd := newCheckpointRestoreCmd()

	if cmd.Use != "restore <session> [checkpoint-id]" {
		t.Errorf("Use = %q, want %q", cmd.Use, "restore <session> [checkpoint-id]")
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"just now", 30 * time.Second, "just now"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		{"days", 2 * 24 * time.Hour, "2d ago"},
		// Week+ uses date format, harder to test exactly
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime := time.Now().Add(-tt.ago)
			got := formatAge(testTime)
			if got != tt.want {
				t.Errorf("formatAge() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
		{"", 5, ""},
		{"hello", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := truncateStr(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}

// TestFormatAge_WeekPlus tests the default case (>7 days) that returns a date format.
func TestFormatAge_WeekPlus(t *testing.T) {
	t.Parallel()

	// 10 days ago
	testTime := time.Now().Add(-10 * 24 * time.Hour)
	got := formatAge(testTime)
	// Should return something like "Jan 26" — not "just now", "Xm ago", "Xh ago", "Xd ago"
	if strings.Contains(got, "ago") || got == "just now" {
		t.Errorf("formatAge(10 days ago) = %q, expected date format (e.g. 'Jan 26')", got)
	}
}

// TestTruncateStr_MultibyteLoopFallthrough tests line 852: all rune starts
// fit within targetLen but string length exceeds maxLen.
func TestTruncateStr_MultibyteLoopFallthrough(t *testing.T) {
	t.Parallel()

	// "aaaa🌍" = 8 bytes. maxLen=7, targetLen=4.
	// Rune starts: 0,1,2,3,4. All <=4. Loop completes.
	// prevI=4. return s[:4]+"..." = "aaaa..."
	s := "aaaa\xf0\x9f\x8c\x8d" // "aaaa🌍"
	got := truncateStr(s, 7)
	want := "aaaa..."
	if got != want {
		t.Errorf("truncateStr(%q, 7) = %q, want %q", s, got, want)
	}
}

// TestTruncateStr_MaxLen1 tests a very small positive maxLen.
func TestTruncateStr_MaxLen1(t *testing.T) {
	t.Parallel()

	got := truncateStr("hello", 1)
	want := "."
	if got != want {
		t.Errorf("truncateStr(\"hello\", 1) = %q, want %q", got, want)
	}
}

// TestTruncateStr_MaxLen2 tests maxLen=2 with "..."[:2]
func TestTruncateStr_MaxLen2(t *testing.T) {
	t.Parallel()

	got := truncateStr("hello", 2)
	want := ".."
	if got != want {
		t.Errorf("truncateStr(\"hello\", 2) = %q, want %q", got, want)
	}
}

func TestListCheckpointSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-cli-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := checkpoint.NewStorageWithDir(tmpDir)

	// Empty directory should return nil
	sessions, err := listCheckpointSessions(storage)
	if err != nil {
		t.Fatalf("listCheckpointSessions error: %v", err)
	}
	if sessions != nil && len(sessions) > 0 {
		t.Errorf("expected empty sessions, got %v", sessions)
	}

	// Create an empty session directory; it should not count as a session with checkpoints.
	sessDir := filepath.Join(tmpDir, "test-session")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	sessions, err = listCheckpointSessions(storage)
	if err != nil {
		t.Fatalf("listCheckpointSessions error: %v", err)
	}
	if sessions != nil && len(sessions) > 0 {
		t.Errorf("expected empty sessions for bare session dir, got %v", sessions)
	}

	cp := &checkpoint.Checkpoint{
		ID:          "cp-001",
		SessionName: "test-session",
		CreatedAt:   time.Now(),
		Session: checkpoint.SessionState{
			Panes: []checkpoint.PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	sessions, err = listCheckpointSessions(storage)
	if err != nil {
		t.Fatalf("listCheckpointSessions error after save: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != "test-session" {
		t.Errorf("expected [test-session], got %v", sessions)
	}
}

func TestListCheckpointSessions_SkipsSymlinkSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(tmpDir, "symlink-session")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	sessions, err := listCheckpointSessions(storage)
	if err != nil {
		t.Fatalf("listCheckpointSessions error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected symlink-backed session dir to be skipped, got %v", sessions)
	}
}

func TestListCheckpointSessions_IncludesSessionWithOnlyInvalidCheckpoints(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	sessionName := "broken-session"
	cpDir := filepath.Join(tmpDir, sessionName, "20251210-120000-broken")
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	sessions, err := listCheckpointSessions(storage)
	if err != nil {
		t.Fatalf("listCheckpointSessions error: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != sessionName {
		t.Fatalf("expected [%s], got %v", sessionName, sessions)
	}
}

func TestListSessionCheckpoints_JSONMarksInvalidOnlySession(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	sessionName := "broken-session"
	cpDir := filepath.Join(tmpDir, sessionName, "20251210-120000-broken")
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	oldJSONOutput := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSONOutput })

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	callErr := listSessionCheckpoints(storage, sessionName)
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if callErr != nil {
		t.Fatalf("listSessionCheckpoints error: %v", callErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decoding JSON output: %v\noutput=%s", err, out)
	}
	if decoded["session"] != sessionName {
		t.Fatalf("session = %v, want %s", decoded["session"], sessionName)
	}
	if decoded["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", decoded["count"])
	}
	if decoded["invalid_checkpoints_present"] != true {
		t.Fatalf("invalid_checkpoints_present = %v, want true", decoded["invalid_checkpoints_present"])
	}
	invalidIDs, ok := decoded["invalid_checkpoint_ids"].([]interface{})
	if !ok || len(invalidIDs) != 1 || invalidIDs[0] != "20251210-120000-broken" {
		t.Fatalf("invalid_checkpoint_ids = %#v, want [20251210-120000-broken]", decoded["invalid_checkpoint_ids"])
	}
}

func TestListSessionCheckpoints_JSONIncludesInvalidIDsAlongsideValidCheckpoints(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	sessionName := "mixed-session"
	valid := &checkpoint.Checkpoint{
		Version:     checkpoint.CurrentVersion,
		ID:          "20251210-120100-valid",
		Name:        "valid",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: checkpoint.SessionState{
			Panes: []checkpoint.PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(valid): %v", err)
	}

	cpDir := filepath.Join(tmpDir, sessionName, "20251210-120000-broken")
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	oldJSONOutput := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSONOutput })

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	callErr := listSessionCheckpoints(storage, sessionName)
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if callErr != nil {
		t.Fatalf("listSessionCheckpoints error: %v", callErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decoding JSON output: %v\noutput=%s", err, out)
	}
	if decoded["count"] != float64(1) {
		t.Fatalf("count = %v, want 1", decoded["count"])
	}
	invalidIDs, ok := decoded["invalid_checkpoint_ids"].([]interface{})
	if !ok || len(invalidIDs) != 1 || invalidIDs[0] != "20251210-120000-broken" {
		t.Fatalf("invalid_checkpoint_ids = %#v, want [20251210-120000-broken]", decoded["invalid_checkpoint_ids"])
	}
}

func TestCheckpointDeleteCmd_DeletesInvalidCheckpointEntry(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	storage := checkpoint.NewStorage()
	sessionName := "delete-invalid-session"
	checkpointID := "20251210-120000-broken"
	cpDir := filepath.Join(storage.BaseDir, sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	cmd := newCheckpointDeleteCmd()
	cmd.SetArgs([]string{sessionName, checkpointID, "--force"})

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	exists, err := storage.HasCheckpointPath(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("HasCheckpointPath(): %v", err)
	}
	if exists {
		t.Fatal("invalid checkpoint entry still exists after delete command")
	}
}

func TestVerifySingleCheckpoint_JSONReturnsErrorForInvalidCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	sessionName := "verify-invalid-session"
	checkpointID := "20251210-120000-broken"
	cpDir := filepath.Join(tmpDir, sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	oldJSONOutput := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSONOutput })

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	callErr := verifySingleCheckpoint(storage, sessionName, checkpointID)
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if callErr == nil {
		t.Fatal("verifySingleCheckpoint() error = nil, want verification failure")
	}
	if !strings.Contains(callErr.Error(), "verification failed") {
		t.Fatalf("verifySingleCheckpoint() error = %v, want verification failure context", callErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decoding JSON output: %v\noutput=%s", err, out)
	}
	if decoded["valid"] != false {
		t.Fatalf("valid = %v, want false", decoded["valid"])
	}
	if decoded["id"] != checkpointID {
		t.Fatalf("id = %v, want %s", decoded["id"], checkpointID)
	}
}

func TestVerifyAllCheckpoints_JSONReturnsErrorForInvalidCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	storage := checkpoint.NewStorageWithDir(tmpDir)

	sessionName := "verify-all-invalid-session"
	valid := &checkpoint.Checkpoint{
		Version:     checkpoint.CurrentVersion,
		ID:          "20251210-120100-valid",
		Name:        "valid",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: checkpoint.SessionState{
			Panes: []checkpoint.PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(valid): %v", err)
	}

	invalidID := "20251210-120000-broken"
	cpDir := filepath.Join(tmpDir, sessionName, invalidID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	oldJSONOutput := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSONOutput })

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	callErr := verifyAllCheckpoints(storage, sessionName)
	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	if callErr == nil {
		t.Fatal("verifyAllCheckpoints() error = nil, want verification failure")
	}
	if !strings.Contains(callErr.Error(), "1 checkpoint(s) failed verification") {
		t.Fatalf("verifyAllCheckpoints() error = %v, want verification failure count", callErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stdout: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decoding JSON output: %v\noutput=%s", err, out)
	}
	if decoded["valid_count"] != float64(1) {
		t.Fatalf("valid_count = %v, want 1", decoded["valid_count"])
	}
	if decoded["total_count"] != float64(2) {
		t.Fatalf("total_count = %v, want 2", decoded["total_count"])
	}
}

func TestCheckpointExportCmd_InvalidCheckpointReportsLoadFailure(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	storage := checkpoint.NewStorage()
	sessionName := "export-invalid-session"
	checkpointID := "20251210-120000-broken"
	cpDir := filepath.Join(storage.BaseDir, sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("failed to create checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	cmd := newCheckpointExportCmd()
	outputPath := filepath.Join(t.TempDir(), "checkpoint.tar.gz")
	cmd.SetArgs([]string{sessionName, checkpointID, "--output", outputPath})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want load failure for invalid checkpoint")
	}
	if !strings.Contains(err.Error(), "loading checkpoint:") {
		t.Fatalf("Execute() error = %v, want load failure context", err)
	}
	if strings.Contains(err.Error(), "checkpoint not found") {
		t.Fatalf("Execute() error = %v, want invalid checkpoint to be distinguished from not found", err)
	}
}

// listCheckpointSessionsWithDir is a helper for testing that accepts a custom directory.
func listCheckpointSessionsWithDir(baseDir string) ([]string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}
	return sessions, nil
}

func TestCheckpointRestoreCmdArgs(t *testing.T) {
	cmd := newCheckpointRestoreCmd()

	if err := cmd.Args(cmd, []string{"myproject"}); err != nil {
		t.Errorf("Args(1 arg) returned error: %v", err)
	}
	if err := cmd.Args(cmd, []string{"myproject", "last"}); err != nil {
		t.Errorf("Args(2 args) returned error: %v", err)
	}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("Args(0 args) = nil, want error")
	}
	if err := cmd.Args(cmd, []string{"myproject", "last", "extra"}); err == nil {
		t.Error("Args(3 args) = nil, want error")
	}
}

func TestCheckpointCmdJSONOutput(t *testing.T) {
	// Test that JSON output produces valid JSON structure
	tmpDir, err := os.MkdirTemp("", "ntm-cli-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Simulate what JSON output would look like
	result := map[string]interface{}{
		"session":          "test-session",
		"panes_restored":   2,
		"context_injected": false,
		"dry_run":          true,
		"warnings":         []string{"test warning"},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(result); err != nil {
		t.Fatalf("JSON encode error: %v", err)
	}

	// Verify it decodes back correctly
	var decoded map[string]interface{}
	if err := json.NewDecoder(&buf).Decode(&decoded); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}

	if decoded["session"] != "test-session" {
		t.Errorf("session = %v, want test-session", decoded["session"])
	}
	if decoded["panes_restored"] != float64(2) {
		t.Errorf("panes_restored = %v, want 2", decoded["panes_restored"])
	}
}

func TestCheckpointSaveCmdFlags(t *testing.T) {
	cmd := newCheckpointSaveCmd()

	// Verify default values
	scrollback := cmd.Flags().Lookup("scrollback")
	if scrollback.DefValue != "1000" {
		t.Errorf("scrollback default = %s, want 1000", scrollback.DefValue)
	}

	noGit := cmd.Flags().Lookup("no-git")
	if noGit.DefValue != "false" {
		t.Errorf("no-git default = %s, want false", noGit.DefValue)
	}
}

func TestCheckpointRestoreCmdFlags(t *testing.T) {
	cmd := newCheckpointRestoreCmd()

	flags := []string{
		"force",
		"attach",
		"skip-git-check",
		"inject-context",
		"dry-run",
		"directory",
		"scrollback",
	}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}

	scrollback := cmd.Flags().Lookup("scrollback")
	if scrollback.DefValue != "0" {
		t.Errorf("scrollback default = %s, want 0", scrollback.DefValue)
	}
}

func TestCheckpointRestoreCmd_InvalidCheckpointReportsLoadFailure(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	storage := checkpoint.NewStorage()
	sessionName := "restore-invalid"
	checkpointID := "broken-restore"
	cpDir := filepath.Join(storage.BaseDir, sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cmd := newCheckpointRestoreCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{sessionName, checkpointID, "--dry-run"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want load failure")
	}
	if !strings.Contains(err.Error(), "loading checkpoint:") {
		t.Fatalf("error = %q, want loading checkpoint context", err)
	}
	if strings.Contains(err.Error(), "finding checkpoint: no checkpoint found matching") {
		t.Fatalf("error = %q, want exact invalid checkpoint load failure", err)
	}
}
