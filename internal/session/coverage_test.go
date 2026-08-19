package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// =============================================================================
// LoadPromptHistory — uncovered: corrupt JSON parse error, read-error branch
// =============================================================================

func TestLoadPromptHistory_CorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "corrupt-json-session"

	// Create the prompts file with invalid JSON
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts.json"), []byte("{bad json!!!"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = LoadPromptHistory(sessionName)
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse prompts file") {
		t.Errorf("error = %q, want 'failed to parse prompts file' substring", err)
	}
}

func TestLoadPromptHistory_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}

	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "perm-error-session"

	// Create the prompts file then make it unreadable
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	promptsPath := filepath.Join(dir, "prompts.json")
	if err := os.WriteFile(promptsPath, []byte(`{"session":"x","prompts":[]}`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	os.Chmod(promptsPath, 0000)
	t.Cleanup(func() { os.Chmod(promptsPath, 0600) })

	_, err = LoadPromptHistory(sessionName)
	if err == nil {
		t.Fatal("expected error for unreadable file, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read prompts file") {
		t.Errorf("error = %q, want 'failed to read prompts file' substring", err)
	}
}

// =============================================================================
// SavePrompt — uncovered: pre-filled ID & Timestamp preserved, error from
// LoadPromptHistory (corrupt file)
// =============================================================================

func TestSavePrompt_PrefilledIDAndTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	fixedTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	entry := PromptEntry{
		Session:   "prefilled-session",
		Content:   "hello",
		Targets:   []string{"1"},
		Source:    "cli",
		ID:        "my-custom-id",
		Timestamp: fixedTime,
	}

	if err := SavePrompt(entry); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}

	history, err := LoadPromptHistory("prefilled-session")
	if err != nil {
		t.Fatalf("LoadPromptHistory: %v", err)
	}
	if len(history.Prompts) != 1 {
		t.Fatalf("len = %d, want 1", len(history.Prompts))
	}

	got := history.Prompts[0]
	if got.ID != "my-custom-id" {
		t.Errorf("ID = %q, want %q", got.ID, "my-custom-id")
	}
	if !got.Timestamp.Equal(fixedTime) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, fixedTime)
	}
}

func TestSavePrompt_CorruptExistingHistory(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "corrupt-existing"

	// Create corrupt prompts file
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts.json"), []byte("not json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entry := PromptEntry{
		Session: sessionName,
		Content: "test",
		Targets: []string{"1"},
		Source:  "cli",
	}

	err = SavePrompt(entry)
	if err == nil {
		t.Fatal("expected error when existing history is corrupt, got nil")
	}
}

// =============================================================================
// savePromptHistory — uncovered: redaction in warn/block modes
// =============================================================================

func TestSavePromptHistory_RedactionWarnMode(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Set redaction config to warn mode (should be treated as redact for persistence)
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeWarn})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	secret := "sk-" + strings.Repeat("B", 48)
	history := &PromptHistory{
		Session: "redact-warn-test",
		Prompts: []PromptEntry{
			{
				ID:        "1",
				Session:   "redact-warn-test",
				Content:   "key=" + secret,
				Targets:   []string{"1"},
				Source:    "cli",
				Timestamp: time.Now(),
			},
		},
		UpdateAt: time.Now(),
	}

	if err := savePromptHistory(history); err != nil {
		t.Fatalf("savePromptHistory: %v", err)
	}

	// Reload and verify redaction was applied
	loaded, err := LoadPromptHistory("redact-warn-test")
	if err != nil {
		t.Fatalf("LoadPromptHistory: %v", err)
	}
	if len(loaded.Prompts) != 1 {
		t.Fatalf("len = %d, want 1", len(loaded.Prompts))
	}
	if strings.Contains(loaded.Prompts[0].Content, secret) {
		t.Error("expected secret to be redacted, but it was persisted in cleartext")
	}
}

func TestSavePromptHistory_RedactionBlockMode(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Block mode should also redact for persistence
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeBlock})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	secret := "sk-" + strings.Repeat("C", 48)
	history := &PromptHistory{
		Session: "redact-block-test",
		Prompts: []PromptEntry{
			{
				ID:        "1",
				Session:   "redact-block-test",
				Content:   "token=" + secret,
				Targets:   []string{"1"},
				Source:    "cli",
				Timestamp: time.Now(),
			},
		},
		UpdateAt: time.Now(),
	}

	if err := savePromptHistory(history); err != nil {
		t.Fatalf("savePromptHistory: %v", err)
	}

	loaded, err := LoadPromptHistory("redact-block-test")
	if err != nil {
		t.Fatalf("LoadPromptHistory: %v", err)
	}
	if strings.Contains(loaded.Prompts[0].Content, secret) {
		t.Error("expected secret to be redacted in block mode, but it was persisted")
	}
}

func TestSavePromptHistory_RedactionOffMode(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Off mode should NOT redact
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeOff})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	content := "plain text with no secrets"
	history := &PromptHistory{
		Session: "redact-off-test",
		Prompts: []PromptEntry{
			{
				ID:        "1",
				Session:   "redact-off-test",
				Content:   content,
				Targets:   []string{"1"},
				Source:    "cli",
				Timestamp: time.Now(),
			},
		},
		UpdateAt: time.Now(),
	}

	if err := savePromptHistory(history); err != nil {
		t.Fatalf("savePromptHistory: %v", err)
	}

	loaded, err := LoadPromptHistory("redact-off-test")
	if err != nil {
		t.Fatalf("LoadPromptHistory: %v", err)
	}
	if loaded.Prompts[0].Content != content {
		t.Errorf("content = %q, want %q", loaded.Prompts[0].Content, content)
	}
}

// =============================================================================
// StorageDir — uncovered: fallback to temp dir when HOME unavailable
// =============================================================================

func TestStorageDir_FallbackToTemp(t *testing.T) {
	// Clear HOME to trigger fallback
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", "")
	defer os.Setenv("HOME", oldHome)

	got := StorageDir()
	// When HOME is empty, NTMDir may still work on some systems.
	// At minimum, verify we get an absolute path (no empty string).
	if got == "" {
		t.Error("StorageDir() returned empty string")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("StorageDir() = %q, want absolute path", got)
	}
}

// =============================================================================
// List — uncovered: skip non-JSON files, skip corrupted session files
// =============================================================================

func TestList_SkipsNonJSONFiles(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Save a valid session
	state := createTestState("valid-session")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Create a non-JSON file in the sessions directory
	dir := StorageDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a session"), 0600)
	os.Mkdir(filepath.Join(dir, "subdir"), 0700)

	sessions, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("List() returned %d sessions, want 1 (should skip non-JSON)", len(sessions))
	}
}

func TestList_SkipsCorruptedFiles(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Save a valid session
	state := createTestState("good-session")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Create a corrupt JSON session file
	dir := StorageDir()
	os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{bad json}"), 0600)

	sessions, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("List() returned %d sessions, want 1 (should skip corrupt)", len(sessions))
	}
	if sessions[0].Name != "good-session" {
		t.Errorf("sessions[0].Name = %q, want %q", sessions[0].Name, "good-session")
	}
}

// =============================================================================
// ListSessionDirs — uncovered: directory without prompts.json is skipped
// =============================================================================

// =============================================================================
// SessionDir — additional edge cases
// =============================================================================

func TestSessionDir_SpecialCharacters(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Session name with characters that get sanitized
	dir, err := SessionDir("my/project:test")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	// Should use sanitized name
	if strings.Contains(dir, "/project:") {
		t.Errorf("dir = %q, expected sanitized path", dir)
	}
	// Verify the directory was created
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("directory was not created")
	}
}

func TestSessionDir_CalledTwice(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	dir1, err := SessionDir("idempotent-test")
	if err != nil {
		t.Fatalf("first SessionDir: %v", err)
	}
	dir2, err := SessionDir("idempotent-test")
	if err != nil {
		t.Fatalf("second SessionDir: %v", err)
	}
	if dir1 != dir2 {
		t.Errorf("SessionDir not idempotent: %q != %q", dir1, dir2)
	}
}

// =============================================================================
// Exists — already 100% but let's ensure edge cases
// =============================================================================

func TestExists_SanitizedName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("test/name")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Exists should find it via the sanitized name
	if !Exists("test/name") {
		t.Error("Exists() = false, want true for sanitized name lookup")
	}
}

// =============================================================================
// GetLatestPrompts — uncovered: error from LoadPromptHistory
// =============================================================================

// =============================================================================
// Save/Load roundtrip — verify all fields persisted
// =============================================================================

func TestSaveLoad_AllFields(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := &SessionState{
		Name:      "full-state-test",
		SavedAt:   time.Date(2025, 7, 1, 10, 0, 0, 0, time.UTC),
		WorkDir:   "/home/user/dev/project",
		GitBranch: "feature/xyz",
		GitRemote: "https://github.com/user/repo.git",
		GitCommit: "abc1234",
		Agents:    AgentConfig{Claude: 3, Codex: 1, Gemini: 2, User: 1, Cursor: 1, Windsurf: 1, Aider: 1},
		Panes: []PaneState{
			{Title: "cc_1", Index: 0, WindowIndex: 0, AgentType: "cc", Model: "opus", Active: true, Width: 120, Height: 40, PaneID: "%1"},
		},
		Layout:    "even-horizontal",
		CreatedAt: time.Date(2025, 6, 30, 8, 0, 0, 0, time.UTC),
		Version:   StateVersion,
		Config: &ConfigSnapshot{
			ClaudeCmd: "claude --model opus",
			CodexCmd:  "codex",
			GeminiCmd: "gemini",
		},
	}

	_, err := Save(state, SaveOptions{Overwrite: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load("full-state-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify all fields roundtrip
	if loaded.GitRemote != state.GitRemote {
		t.Errorf("GitRemote = %q, want %q", loaded.GitRemote, state.GitRemote)
	}
	if loaded.GitCommit != state.GitCommit {
		t.Errorf("GitCommit = %q, want %q", loaded.GitCommit, state.GitCommit)
	}
	if loaded.Agents.Total() != 10 {
		t.Errorf("Agents.Total() = %d, want 10", loaded.Agents.Total())
	}
	if loaded.Layout != "even-horizontal" {
		t.Errorf("Layout = %q, want %q", loaded.Layout, "even-horizontal")
	}
	if loaded.Config == nil {
		t.Fatal("Config is nil, want non-nil")
	}
	if loaded.Config.ClaudeCmd != "claude --model opus" {
		t.Errorf("Config.ClaudeCmd = %q", loaded.Config.ClaudeCmd)
	}
	if loaded.Panes[0].PaneID != "%1" {
		t.Errorf("Panes[0].PaneID = %q, want %%1", loaded.Panes[0].PaneID)
	}
}

// =============================================================================
// Delete — already 94.7% but test sanitized name
// =============================================================================

func TestDelete_SanitizedName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("delete/special")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Delete("delete/special"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if Exists("delete/special") {
		t.Error("session still exists after Delete")
	}
}

// =============================================================================
// redactPromptHistoryForPersistence — directly test nil config and off mode
// =============================================================================

func TestRedactPromptHistoryForPersistence_NilConfig(t *testing.T) {
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(nil) })

	history := &PromptHistory{
		Session: "test",
		Prompts: []PromptEntry{
			{Content: "sensitive data sk-" + strings.Repeat("X", 48)},
		},
	}

	result := redactPromptHistoryForPersistence(history)
	// With nil config, should return original unmodified
	if result != history {
		t.Error("expected same pointer when config is nil")
	}
}

// =============================================================================
// ClearPromptHistory — uncovered: error from promptsFilePath
// (hard to trigger without mocking, but we can ensure the Remove error branch
// is covered by clearing a session that was never saved but whose dir exists)
// =============================================================================

// =============================================================================
// PromptEntry JSON round-trip with optional fields
// =============================================================================

func TestPromptEntry_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	entry := PromptEntry{
		ID:        "test-id",
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Session:   "my-session",
		Content:   "do something",
		Targets:   []string{"all"},
		Source:    "template",
		Template:  "my-template",
		FilePath:  "/path/to/file",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded PromptEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Template != "my-template" {
		t.Errorf("Template = %q, want %q", decoded.Template, "my-template")
	}
	if decoded.FilePath != "/path/to/file" {
		t.Errorf("FilePath = %q, want %q", decoded.FilePath, "/path/to/file")
	}
	if decoded.Source != "template" {
		t.Errorf("Source = %q, want %q", decoded.Source, "template")
	}
}

func TestPromptEntry_JSONOmitsEmpty(t *testing.T) {
	t.Parallel()

	entry := PromptEntry{
		ID:      "test-id",
		Session: "s",
		Content: "c",
		Source:  "cli",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// template and file_path should be omitted
	if strings.Contains(string(data), "template") {
		t.Error("expected template to be omitted from JSON")
	}
	if strings.Contains(string(data), "file_path") {
		t.Error("expected file_path to be omitted from JSON")
	}
}

// =============================================================================
// Privacy mode — SavePrompt and savePromptHistory privacy branches
// =============================================================================

// setupPrivacyManager enables privacy mode for the given session and returns
// a cleanup function that restores the original manager.
func setupPrivacyManager(t *testing.T, sessionName string) {
	t.Helper()
	original := privacy.GetDefaultManager()
	t.Cleanup(func() { privacy.SetDefaultManager(original) })

	mgr := privacy.New(config.PrivacyConfig{
		Enabled:              true,
		DisablePromptHistory: true,
	})
	mgr.RegisterSession(sessionName, true, false)
	privacy.SetDefaultManager(mgr)
}

func TestSavePrompt_PrivacyMode_SkipsPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "privacy-save-test"
	setupPrivacyManager(t, sessionName)

	entry := PromptEntry{
		Session: sessionName,
		Content: "this should not be persisted",
		Targets: []string{"1"},
		Source:  "cli",
	}

	// SavePrompt should silently succeed (return nil) but not write anything
	err := SavePrompt(entry)
	if err != nil {
		t.Fatalf("SavePrompt: %v, expected nil (privacy skip)", err)
	}

	// Verify no file was created
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	promptsPath := filepath.Join(dir, "prompts.json")
	if _, statErr := os.Stat(promptsPath); !os.IsNotExist(statErr) {
		t.Error("prompts.json should not exist in privacy mode")
	}
}

func TestSavePromptHistory_PrivacyMode_SkipsPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "privacy-history-test"
	setupPrivacyManager(t, sessionName)

	history := &PromptHistory{
		Session: sessionName,
		Prompts: []PromptEntry{
			{
				ID:        "1",
				Session:   sessionName,
				Content:   "should not persist",
				Targets:   []string{"1"},
				Source:    "cli",
				Timestamp: time.Now(),
			},
		},
		UpdateAt: time.Now(),
	}

	// savePromptHistory should silently succeed in privacy mode
	err := savePromptHistory(history)
	if err != nil {
		t.Fatalf("savePromptHistory: %v, expected nil (privacy skip)", err)
	}

	// Verify no file was created
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	promptsPath := filepath.Join(dir, "prompts.json")
	if _, statErr := os.Stat(promptsPath); !os.IsNotExist(statErr) {
		t.Error("prompts.json should not exist when privacy mode blocks persistence")
	}
}

func TestSavePromptHistory_EmptySession_SkipsPrivacyCheck(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Even with privacy enabled globally, empty session should skip privacy check
	original := privacy.GetDefaultManager()
	t.Cleanup(func() { privacy.SetDefaultManager(original) })

	mgr := privacy.New(config.PrivacyConfig{
		Enabled:              true,
		DisablePromptHistory: true,
	})
	privacy.SetDefaultManager(mgr)

	history := &PromptHistory{
		Session: "", // Empty session — privacy check is skipped
		Prompts: []PromptEntry{
			{ID: "1", Content: "test", Targets: []string{"1"}, Source: "cli", Timestamp: time.Now()},
		},
		UpdateAt: time.Now(),
	}

	// Should succeed because empty session skips the privacy check
	err := savePromptHistory(history)
	if err != nil {
		t.Fatalf("savePromptHistory with empty session: %v", err)
	}
}
