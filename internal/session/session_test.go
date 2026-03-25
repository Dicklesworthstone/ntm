package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// --- AgentConfig Tests ---

func TestAgentConfig_Total(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config AgentConfig
		want   int
	}{
		{
			name:   "empty",
			config: AgentConfig{},
			want:   0,
		},
		{
			name:   "claude only",
			config: AgentConfig{Claude: 3},
			want:   3,
		},
		{
			name:   "all types",
			config: AgentConfig{Claude: 2, Codex: 1, Gemini: 1, User: 1},
			want:   5,
		},
		{
			name:   "typical setup",
			config: AgentConfig{Claude: 2, Codex: 1, User: 1},
			want:   4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.config.Total()
			if got != tt.want {
				t.Errorf("Total() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- sanitizeFilename Tests ---

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with spaces", "with spaces"},
		{"with/slash", "with-slash"},
		{"with\\backslash", "with-backslash"},
		{"with:colon", "with-colon"},
		{"with*asterisk", "with_asterisk"},
		{"with?question", "with_question"},
		{"with\"quote", "with_quote"},
		{"with<less", "with_less"},
		{"with>greater", "with_greater"},
		{"with|pipe", "with_pipe"},
		{"complex/path:name*test?.json", "complex-path-name_test_.json"},
		{"", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- StorageDir Tests ---

func TestStorageDir_XDGDataHome(t *testing.T) {
	// Cannot run in parallel due to environment variable manipulation

	// Save and restore original XDG_DATA_HOME
	original := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", original)

	// Set XDG_DATA_HOME and HOME (StorageDir should ignore XDG_DATA_HOME now)
	tmpDir := t.TempDir()
	homeDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", homeDir)

	got := StorageDir()
	want := filepath.Join(homeDir, ".ntm", "sessions")

	if got != want {
		t.Errorf("StorageDir() = %q, want %q", got, want)
	}
}

func TestStorageDir_Default(t *testing.T) {
	// Cannot run in parallel due to environment variable manipulation

	// Save and restore original XDG_DATA_HOME
	original := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", original)

	// Clear XDG_DATA_HOME
	os.Setenv("XDG_DATA_HOME", "")

	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)

	got := StorageDir()

	// Should be under home directory
	expected := filepath.Join(homeDir, ".ntm", "sessions")
	if got != expected {
		t.Errorf("StorageDir() = %q, want %q", got, expected)
	}
}

// --- Storage Operations Tests ---

// setupTestStorage sets up an isolated storage directory for testing.
func setupTestStorage(t *testing.T) (string, func()) {
	t.Helper()

	// Save original env vars
	originalXDG := os.Getenv("XDG_DATA_HOME")
	originalHome := os.Getenv("HOME")

	// Create temp directory and set it as HOME (StorageDir now uses ~/.ntm)
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Return cleanup function
	cleanup := func() {
		os.Setenv("XDG_DATA_HOME", originalXDG)
		os.Setenv("HOME", originalHome)
	}

	return tmpDir, cleanup
}

func createTestState(name string) *SessionState {
	return &SessionState{
		Name:      name,
		SavedAt:   time.Now().UTC(),
		WorkDir:   "/test/project",
		GitBranch: "main",
		GitCommit: "abc123",
		Agents:    AgentConfig{Claude: 2, Codex: 1},
		Panes: []PaneState{
			{Title: "cc_1", Index: 0, AgentType: "cc", Active: true},
			{Title: "cc_2", Index: 1, AgentType: "cc", Active: false},
			{Title: "cod_1", Index: 2, AgentType: "cod", Active: false},
		},
		Layout:  "tiled",
		Version: StateVersion,
	}
}

func TestSave_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("test-session")
	opts := SaveOptions{Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("Save() created file %s but it doesn't exist", path)
	}

	// Verify filename
	expectedFilename := "test-session.json"
	if filepath.Base(path) != expectedFilename {
		t.Errorf("Save() filename = %s, want %s", filepath.Base(path), expectedFilename)
	}
}

func TestSave_CustomName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("original-name")
	opts := SaveOptions{Name: "custom-name", Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	expectedFilename := "custom-name.json"
	if filepath.Base(path) != expectedFilename {
		t.Errorf("Save() filename = %s, want %s", filepath.Base(path), expectedFilename)
	}
}

func TestSave_NilState(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	if _, err := Save(nil, SaveOptions{Name: "nil-state", Overwrite: true}); err == nil {
		t.Fatal("Save() with nil state should fail")
	}
}

func TestSave_NoOverwrite(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("no-overwrite-test")
	opts := SaveOptions{Overwrite: true}

	// Save first time
	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("First Save() error = %v", err)
	}

	// Try to save again without overwrite
	opts.Overwrite = false
	_, err = Save(state, opts)
	if err == nil {
		t.Errorf("Save() without overwrite should fail, but succeeded")
	}
}

func TestSave_Overwrite(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("overwrite-test")
	opts := SaveOptions{Overwrite: true}

	// Save first time
	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("First Save() error = %v", err)
	}

	// Modify state
	state.GitBranch = "develop"

	// Save again with overwrite
	_, err = Save(state, opts)
	if err != nil {
		t.Fatalf("Second Save() with overwrite error = %v", err)
	}

	// Verify the change was saved
	loaded, err := Load("overwrite-test")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GitBranch != "develop" {
		t.Errorf("Load().GitBranch = %s, want develop", loaded.GitBranch)
	}
}

func TestLoad_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	original := createTestState("load-test")
	opts := SaveOptions{Overwrite: true}

	_, err := Save(original, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load("load-test")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify key fields
	if loaded.Name != original.Name {
		t.Errorf("Load().Name = %s, want %s", loaded.Name, original.Name)
	}
	if loaded.WorkDir != original.WorkDir {
		t.Errorf("Load().WorkDir = %s, want %s", loaded.WorkDir, original.WorkDir)
	}
	if loaded.GitBranch != original.GitBranch {
		t.Errorf("Load().GitBranch = %s, want %s", loaded.GitBranch, original.GitBranch)
	}
	if loaded.Agents.Total() != original.Agents.Total() {
		t.Errorf("Load().Agents.Total() = %d, want %d", loaded.Agents.Total(), original.Agents.Total())
	}
	if len(loaded.Panes) != len(original.Panes) {
		t.Errorf("Load() pane count = %d, want %d", len(loaded.Panes), len(original.Panes))
	}
	if loaded.Version != original.Version {
		t.Errorf("Load().Version = %d, want %d", loaded.Version, original.Version)
	}
}

func TestLoad_NotFound(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	_, err := Load("nonexistent-session")
	if err == nil {
		t.Errorf("Load() for nonexistent session should fail")
	}
}

func TestStorageOperations_RejectEmptySanitizedName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err == nil {
		t.Fatal("Save() with empty state name should fail")
	}

	state = createTestState("ignored")
	if _, err := Save(state, SaveOptions{Name: "   ", Overwrite: true}); err == nil {
		t.Fatal("Save() with empty sanitized custom name should fail")
	}
	if _, err := Save(state, SaveOptions{Name: ".", Overwrite: true}); err == nil {
		t.Fatal("Save() with '.' custom name should fail")
	}
	if _, err := Save(state, SaveOptions{Name: "..", Overwrite: true}); err == nil {
		t.Fatal("Save() with '..' custom name should fail")
	}

	if _, err := Load(""); err == nil {
		t.Fatal("Load() with empty name should fail")
	}
	if _, err := Load("   "); err == nil {
		t.Fatal("Load() with empty sanitized name should fail")
	}
	if _, err := Load("."); err == nil {
		t.Fatal("Load() with '.' should fail")
	}
	if _, err := Load(".."); err == nil {
		t.Fatal("Load() with '..' should fail")
	}

	if err := Delete(""); err == nil {
		t.Fatal("Delete() with empty name should fail")
	}
	if err := Delete("   "); err == nil {
		t.Fatal("Delete() with empty sanitized name should fail")
	}
	if err := Delete("."); err == nil {
		t.Fatal("Delete() with '.' should fail")
	}
	if err := Delete(".."); err == nil {
		t.Fatal("Delete() with '..' should fail")
	}

	if Exists("") {
		t.Fatal("Exists() with empty name should be false")
	}
	if Exists("   ") {
		t.Fatal("Exists() with empty sanitized name should be false")
	}
	if Exists(".") {
		t.Fatal("Exists() with '.' should be false")
	}
	if Exists("..") {
		t.Fatal("Exists() with '..' should be false")
	}
}

func TestRestore_NilState(t *testing.T) {
	t.Parallel()

	if err := Restore(nil, RestoreOptions{Name: "nil-state"}); err == nil {
		t.Fatal("Restore(nil, ...) should fail")
	}
}

func TestRestoreAgents_NilState(t *testing.T) {
	t.Parallel()

	if err := RestoreAgents("nil-state", nil, AgentCommands{}); err == nil {
		t.Fatal("RestoreAgents with nil state should fail")
	}
}

func TestDelete_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("delete-test")
	opts := SaveOptions{Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("File should exist before delete")
	}

	// Delete
	err = Delete("delete-test")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("File should not exist after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	err := Delete("nonexistent-session")
	if err == nil {
		t.Errorf("Delete() for nonexistent session should fail")
	}
}

func TestExists_True(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("exists-test")
	opts := SaveOptions{Overwrite: true}

	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if !Exists("exists-test") {
		t.Errorf("Exists() = false, want true")
	}
}

func TestExists_False(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	if Exists("nonexistent-session") {
		t.Errorf("Exists() = true, want false")
	}
}

func TestList_Empty(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 0 {
		t.Errorf("List() returned %d sessions, want 0", len(sessions))
	}
}

func TestList_Multiple(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Create multiple sessions
	for _, name := range []string{"session-a", "session-b", "session-c"} {
		state := createTestState(name)
		opts := SaveOptions{Overwrite: true}
		if _, err := Save(state, opts); err != nil {
			t.Fatalf("Save(%s) error = %v", name, err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 3 {
		t.Fatalf("List() returned %d sessions, want 3", len(sessions))
	}

	// Verify sorted by time (newest first)
	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].SavedAt.Before(sessions[i+1].SavedAt) {
			t.Errorf("List() not sorted by time (newest first)")
		}
	}
}

func TestList_SessionInfo(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("info-test")
	state.WorkDir = "/home/user/project"
	state.GitBranch = "feature-branch"
	opts := SaveOptions{Overwrite: true}

	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("List() returned %d sessions, want 1", len(sessions))
	}

	s := sessions[0]
	if s.Name != "info-test" {
		t.Errorf("List()[0].Name = %s, want info-test", s.Name)
	}
	if s.WorkDir != "/home/user/project" {
		t.Errorf("List()[0].WorkDir = %s, want /home/user/project", s.WorkDir)
	}
	if s.GitBranch != "feature-branch" {
		t.Errorf("List()[0].GitBranch = %s, want feature-branch", s.GitBranch)
	}
	if s.Agents != state.Agents.Total() {
		t.Errorf("List()[0].Agents = %d, want %d", s.Agents, state.Agents.Total())
	}
	if s.FileSize == 0 {
		t.Errorf("List()[0].FileSize = 0, want > 0")
	}
}

// --- Sanitize Roundtrip Test ---

func TestSanitize_Roundtrip(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Test that sanitized names work for save/load
	names := []string{
		"simple",
		"with-hyphen",
		"with_underscore",
		"with.period",
		"with spaces",
		"project/branch", // Will be sanitized to project-branch
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			state := createTestState(name)
			opts := SaveOptions{Overwrite: true}

			_, err := Save(state, opts)
			if err != nil {
				t.Fatalf("Save(%s) error = %v", name, err)
			}

			sanitized := sanitizeFilename(name)
			loaded, err := Load(sanitized)
			if err != nil {
				t.Fatalf("Load(%s) error = %v", sanitized, err)
			}

			if loaded.Name != name {
				t.Errorf("Load().Name = %s, want %s", loaded.Name, name)
			}
		})
	}
}

// --- Session Recovery Helper Tests ---

func TestGetAgentCommand(t *testing.T) {
	t.Parallel()

	cmds := AgentCommands{
		Claude: "claude --flag",
		Codex:  "codex-cli run",
		Gemini: "gemini start",
	}

	tests := []struct {
		agentType string
		want      string
	}{
		{"cc", "claude --flag"},
		{"claude", "claude --flag"},
		{"cod", "codex-cli run"},
		{"codex", "codex-cli run"},
		{"gmi", "gemini start"},
		{"gemini", "gemini start"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := getAgentCommand(tt.agentType, cmds)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestShouldCreateDir(t *testing.T) {
	// Cannot run in parallel due to home directory dependency

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "two levels under home",
			path: filepath.Join(home, "Developer", "project"),
			want: true,
		},
		{
			name: "three levels under home",
			path: filepath.Join(home, "Developer", "org", "project"),
			want: true,
		},
		{
			name: "one level under home",
			path: filepath.Join(home, "project"),
			want: false,
		},
		{
			name: "home itself",
			path: home,
			want: false,
		},
		{
			name: "root",
			path: "/",
			want: false,
		},
		{
			name: "outside home",
			path: "/tmp/project",
			want: false,
		},
		{
			name: "etc dir",
			path: "/etc/something",
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCreateDir(tt.path)
			if got != tt.want {
				t.Errorf("shouldCreateDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// --- RestoreOptions and SaveOptions Tests ---

func TestRestoreOptions_Defaults(t *testing.T) {
	t.Parallel()

	opts := RestoreOptions{}

	// Verify defaults
	if opts.Name != "" {
		t.Errorf("RestoreOptions.Name default = %q, want empty", opts.Name)
	}
	if opts.SkipGitCheck {
		t.Errorf("RestoreOptions.SkipGitCheck default = true, want false")
	}
	if opts.Force {
		t.Errorf("RestoreOptions.Force default = true, want false")
	}
}

func TestSaveOptions_Defaults(t *testing.T) {
	t.Parallel()

	opts := SaveOptions{}

	// Verify defaults
	if opts.Name != "" {
		t.Errorf("SaveOptions.Name default = %q, want empty", opts.Name)
	}
	if opts.Overwrite {
		t.Errorf("SaveOptions.Overwrite default = true, want false")
	}
	if opts.IncludeGit {
		t.Errorf("SaveOptions.IncludeGit default = true, want false")
	}
	if opts.Description != "" {
		t.Errorf("SaveOptions.Description default = %q, want empty", opts.Description)
	}
}

// --- countAgents Tests ---

func TestCountAgents(t *testing.T) {
	t.Parallel()

	t.Run("empty panes", func(t *testing.T) {
		t.Parallel()
		cfg := countAgents(nil)
		if cfg.Total() != 0 {
			t.Errorf("Total() = %d, want 0", cfg.Total())
		}
	})

	t.Run("one of each type", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentCodex},
			{Type: tmux.AgentGemini},
			{Type: tmux.AgentCursor},
			{Type: tmux.AgentWindsurf},
			{Type: tmux.AgentAider},
			{Type: tmux.AgentUser},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 1 {
			t.Errorf("Claude = %d, want 1", cfg.Claude)
		}
		if cfg.Codex != 1 {
			t.Errorf("Codex = %d, want 1", cfg.Codex)
		}
		if cfg.Gemini != 1 {
			t.Errorf("Gemini = %d, want 1", cfg.Gemini)
		}
		if cfg.Cursor != 1 {
			t.Errorf("Cursor = %d, want 1", cfg.Cursor)
		}
		if cfg.Windsurf != 1 {
			t.Errorf("Windsurf = %d, want 1", cfg.Windsurf)
		}
		if cfg.Aider != 1 {
			t.Errorf("Aider = %d, want 1", cfg.Aider)
		}
		if cfg.User != 1 {
			t.Errorf("User = %d, want 1", cfg.User)
		}
		if cfg.Total() != 7 {
			t.Errorf("Total() = %d, want 7", cfg.Total())
		}
	})

	t.Run("multiple of same type", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentCodex},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 3 {
			t.Errorf("Claude = %d, want 3", cfg.Claude)
		}
		if cfg.Codex != 1 {
			t.Errorf("Codex = %d, want 1", cfg.Codex)
		}
		if cfg.Total() != 4 {
			t.Errorf("Total() = %d, want 4", cfg.Total())
		}
	})

	t.Run("unknown type ignored", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentUnknown},
			{Type: tmux.AgentClaude},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 1 {
			t.Errorf("Claude = %d, want 1", cfg.Claude)
		}
		if cfg.Total() != 1 {
			t.Errorf("Total() = %d, want 1 (unknown should not count)", cfg.Total())
		}
	})
}

// --- mapPaneStates Tests ---

func TestMapPaneStates(t *testing.T) {
	t.Parallel()

	t.Run("empty panes", func(t *testing.T) {
		t.Parallel()
		states := mapPaneStates(nil)
		if len(states) != 0 {
			t.Errorf("expected empty for nil input, got len=%d", len(states))
		}
	})

	t.Run("single pane preserves fields", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{
				ID:      "%5",
				Index:   2,
				Title:   "myproject__cc_1_opus",
				Type:    tmux.AgentClaude,
				Variant: "opus",
				Active:  true,
				Width:   120,
				Height:  40,
			},
		}
		states := mapPaneStates(panes)
		if len(states) != 1 {
			t.Fatalf("len = %d, want 1", len(states))
		}
		s := states[0]
		if s.Title != "myproject__cc_1_opus" {
			t.Errorf("Title = %q", s.Title)
		}
		if s.Index != 2 {
			t.Errorf("Index = %d, want 2", s.Index)
		}
		if s.AgentType != string(tmux.AgentClaude) {
			t.Errorf("AgentType = %q", s.AgentType)
		}
		if s.Model != "opus" {
			t.Errorf("Model = %q, want opus", s.Model)
		}
		if !s.Active {
			t.Error("Active should be true")
		}
		if s.Width != 120 {
			t.Errorf("Width = %d, want 120", s.Width)
		}
		if s.Height != 40 {
			t.Errorf("Height = %d, want 40", s.Height)
		}
		if s.PaneID != "%5" {
			t.Errorf("PaneID = %q, want %%5", s.PaneID)
		}
	})

	t.Run("multiple panes preserve order", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Index: 0, Type: tmux.AgentUser, Title: "bash"},
			{Index: 1, Type: tmux.AgentClaude, Title: "proj__cc_1"},
			{Index: 2, Type: tmux.AgentCodex, Title: "proj__cod_1"},
		}
		states := mapPaneStates(panes)
		if len(states) != 3 {
			t.Fatalf("len = %d, want 3", len(states))
		}
		for i, s := range states {
			if s.Index != i {
				t.Errorf("states[%d].Index = %d, want %d", i, s.Index, i)
			}
		}
		if states[0].AgentType != string(tmux.AgentUser) {
			t.Errorf("states[0].AgentType = %q, want user", states[0].AgentType)
		}
		if states[1].AgentType != string(tmux.AgentClaude) {
			t.Errorf("states[1].AgentType = %q, want cc", states[1].AgentType)
		}
		if states[2].AgentType != string(tmux.AgentCodex) {
			t.Errorf("states[2].AgentType = %q, want cod", states[2].AgentType)
		}
	})
}
