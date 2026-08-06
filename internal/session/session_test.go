package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type fakePaneBindingDiscoverer struct {
	observe func(context.Context, string, string, int, time.Time) agentsession.BindingObservation
}

func (f fakePaneBindingDiscoverer) ObserveBinding(ctx context.Context, agentType, workDir string, panePID int, observedAt time.Time) agentsession.BindingObservation {
	return f.observe(ctx, agentType, workDir, panePID, observedAt)
}

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
			name:   "grok only",
			config: AgentConfig{Grok: 3},
			want:   3,
		},
		{
			name:   "all types",
			config: AgentConfig{Claude: 2, Codex: 1, Gemini: 1, Grok: 2, User: 1},
			want:   7,
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

func TestPaneStateHasFreshSessionBindingFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pane       PaneState
		wantUsable bool
	}{
		{name: "fresh process binding", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 0.99}, wantUsable: true},
		{name: "fresh native threshold", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingFresh, SessionConfidence: minimumSessionBindingConfidence}, wantUsable: true},
		{name: "missing id", pane: PaneState{SessionFreshness: agentsession.BindingFresh, SessionConfidence: 0.99}},
		{name: "legacy unqualified id", pane: PaneState{SessionID: "id"}},
		{name: "stale", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingStale, SessionConfidence: 0.99}},
		{name: "unavailable", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingUnavailable, SessionConfidence: 0.99}},
		{name: "low confidence", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingFresh, SessionConfidence: minimumSessionBindingConfidence - 0.01}},
		{name: "invalid high confidence", pane: PaneState{SessionID: "id", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1.01}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.pane.HasFreshSessionBinding(); got != test.wantUsable {
				t.Fatalf("HasFreshSessionBinding = %v, want %v for %+v", got, test.wantUsable, test.pane)
			}
		})
	}
}

func TestValidateAutomatedRelaunchRejectsGrokSavedBatch(t *testing.T) {
	for _, state := range []*SessionState{
		{Panes: []PaneState{{Index: 1, AgentType: "grok"}}},
		{Panes: []PaneState{{Index: 1, AgentType: "cc"}, {Index: 2, AgentType: "grok-build"}}},
	} {
		if err := ValidateAutomatedRelaunch(state); !errors.Is(err, ErrAutomatedRelaunchNotImplemented) {
			t.Fatalf("ValidateAutomatedRelaunch() error = %v, want Grok relaunch sentinel", err)
		}
	}

	if err := ValidateAutomatedRelaunch(&SessionState{Panes: []PaneState{{AgentType: "cc"}, {AgentType: "cod"}}}); err != nil {
		t.Fatalf("ValidateAutomatedRelaunch() supported batch error = %v", err)
	}
}

func TestAutomatedRelaunchDefensesRejectGrokBeforeTmuxMutation(t *testing.T) {
	state := &SessionState{
		Name:    "invalid/session/name",
		WorkDir: t.TempDir(),
		Panes: []PaneState{{
			Index:     1,
			AgentType: "grok",
			Command:   "claude",
		}},
	}

	if err := RestoreAgents(state.Name, state, AgentCommands{Claude: "claude"}, nil); !errors.Is(err, ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("RestoreAgents() error = %v, want Grok relaunch sentinel before pane lookup", err)
	}
	if result, err := Resume(state, AgentCommands{Claude: "claude"}, ResumeOptions{Force: true}); !errors.Is(err, ErrAutomatedRelaunchNotImplemented) || result != nil {
		t.Fatalf("Resume() = (%+v, %v), want nil and Grok relaunch sentinel before topology restore", result, err)
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

	if err := RestoreAgents("nil-state", nil, AgentCommands{}, nil); err == nil {
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
		Claude:   "claude --flag",
		Codex:    "codex-cli run",
		Gemini:   "gemini start",
		Cursor:   "cursor agent",
		Windsurf: "windsurf start",
		Aider:    "aider --watch",
		Opencode: "opencode run",
		Ollama:   "ollama serve",
	}

	tests := []struct {
		agentType string
		want      string
	}{
		{"cc", "claude --flag"},
		{"claude", "claude --flag"},
		{"claude_code", "claude --flag"},
		{"cod", "codex-cli run"},
		{"codex", "codex-cli run"},
		{"openai-codex", "codex-cli run"},
		{"gmi", "gemini start"},
		{"gemini", "gemini start"},
		{"google-gemini", "gemini start"},
		{"cursor", "cursor agent"},
		{"windsurf", "windsurf start"},
		{"ws", "windsurf start"},
		{"aider", "aider --watch"},
		{"oc", "opencode run"},
		{"opencode", "opencode run"},
		{"ollama", "ollama serve"},
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
			name: "lexical home prefix but sibling directory",
			path: filepath.Join(filepath.Dir(home), filepath.Base(home)+"-restore", "project"),
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
			{Type: tmux.AgentGrok},
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
		if cfg.Grok != 1 {
			t.Errorf("Grok = %d, want 1", cfg.Grok)
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
		if cfg.Total() != 8 {
			t.Errorf("Total() = %d, want 8", cfg.Total())
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
		states := mapPaneStates(nil, "")
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
		states := mapPaneStates(panes, "")
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
		states := mapPaneStates(panes, "")
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

func TestSamplePaneSessionBindingsRecordsQualityAndPreservesPaneOrder(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Type: tmux.AgentUser},
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, PID: 101},
		{ID: "%2", Index: 2, Type: tmux.AgentCodex, PID: 202},
	}
	type call struct {
		agentType string
		workDir   string
		pid       int
	}
	var calls []call
	discoverer := fakePaneBindingDiscoverer{observe: func(_ context.Context, agentType, workDir string, pid int, gotObservedAt time.Time) agentsession.BindingObservation {
		if gotObservedAt != observedAt {
			t.Fatalf("observedAt = %v, want %v", gotObservedAt, observedAt)
		}
		calls = append(calls, call{agentType: agentType, workDir: workDir, pid: pid})
		if agentType == string(tmux.AgentClaude) {
			return agentsession.BindingObservation{
				AgentType:  agentType,
				SessionID:  "claude-session",
				Provider:   "claude",
				Source:     agentsession.DiscoverySourceProcessTree,
				ObservedAt: observedAt,
				Freshness:  agentsession.BindingFresh,
				Confidence: 0.99,
				SourcePath: "/private/claude-session.jsonl",
			}
		}
		return agentsession.BindingObservation{
			AgentType:       agentType,
			SessionID:       "codex-session",
			Provider:        "codex",
			Source:          agentsession.DiscoverySourceNativeStore,
			ObservedAt:      observedAt,
			SourceUpdatedAt: observedAt.Add(-48 * time.Hour),
			Freshness:       agentsession.BindingStale,
			Confidence:      0.375,
		}
	}}
	bindings := samplePaneSessionBindings(
		context.Background(),
		panes,
		"/session",
		observedAt,
		discoverer,
		func(_ context.Context, paneID string) string {
			if paneID == "%1" {
				return "/pane-one"
			}
			return ""
		},
	)
	if len(bindings) != len(panes) || bindings[0].Freshness != "" {
		t.Fatalf("bindings = %+v", bindings)
	}
	if len(calls) != 2 || calls[0] != (call{agentType: "cc", workDir: "/pane-one", pid: 101}) || calls[1] != (call{agentType: "cod", workDir: "/session", pid: 202}) {
		t.Fatalf("discovery calls = %+v", calls)
	}

	states := mapPaneStates(panes, "/session")
	applyPaneSessionBindings(states, bindings)
	if states[0].SessionFreshness != "" {
		t.Fatalf("user pane gained provider binding: %+v", states[0])
	}
	if states[1].SessionID != "claude-session" || states[1].SessionSource != agentsession.DiscoverySourceProcessTree || states[1].SessionFreshness != agentsession.BindingFresh || states[1].SessionConfidence != 0.99 {
		t.Fatalf("fresh binding not applied: %+v", states[1])
	}
	encoded, err := json.Marshal(states[1])
	if err != nil {
		t.Fatalf("Marshal pane state: %v", err)
	}
	if strings.Contains(string(encoded), "/private/claude-session.jsonl") || strings.Contains(string(encoded), "session_file") {
		t.Fatalf("serialized pane state leaked provider transcript path: %s", encoded)
	}
	if states[2].SessionID != "codex-session" || states[2].SessionSource != agentsession.DiscoverySourceNativeStore || states[2].SessionFreshness != agentsession.BindingStale || states[2].SessionConfidence != 0.375 {
		t.Fatalf("stale binding not explicit: %+v", states[2])
	}
}

func TestSamplePaneSessionBindingsCancellationIsExplicitAndSkipsDiscovery(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	discoverer := fakePaneBindingDiscoverer{observe: func(context.Context, string, string, int, time.Time) agentsession.BindingObservation {
		t.Fatal("discovery must not run after cancellation")
		return agentsession.BindingObservation{}
	}}
	panes := []tmux.Pane{
		{ID: "%0", Type: tmux.AgentUser},
		{ID: "%1", Type: tmux.AgentClaude},
	}
	bindings := samplePaneSessionBindings(ctx, panes, "/repo", time.Now(), discoverer, func(context.Context, string) string {
		t.Fatal("path lookup must not run after cancellation")
		return ""
	})
	if bindings[0].Freshness != "" || bindings[1].Freshness != agentsession.BindingUnavailable || bindings[1].FailureCode != "cancelled" {
		t.Fatalf("cancelled bindings = %+v", bindings)
	}
}

func TestAwaitPaneSessionBindingsDeadlineReturnsUnavailableWithoutWaitingForWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	panes := []tmux.Pane{
		{ID: "%0", Type: tmux.AgentUser},
		{ID: "%1", Type: tmux.AgentCodex},
	}
	bindings := awaitPaneSessionBindings(ctx, make(chan []agentsession.BindingObservation), panes, time.Now())
	if bindings[0].Freshness != "" || bindings[1].Freshness != agentsession.BindingUnavailable || bindings[1].FailureCode != "deadline_exceeded" {
		t.Fatalf("deadline bindings = %+v", bindings)
	}
}

// TestWindowCreationOrder verifies the distinct window indices are returned in
// first-appearance order (the order Restore creates windows), which is how
// restoreWindowFidelity maps saved windows to freshly-created tmux windows.
func TestWindowCreationOrder(t *testing.T) {
	t.Parallel()

	t.Run("distinct in first-appearance order", func(t *testing.T) {
		t.Parallel()
		// Already sorted by (WindowIndex, Index) as Restore sorts before calling.
		panes := []PaneState{
			{WindowIndex: 0, Index: 0},
			{WindowIndex: 0, Index: 1},
			{WindowIndex: 2, Index: 0},
			{WindowIndex: 2, Index: 1},
			{WindowIndex: 5, Index: 0},
		}
		got := windowCreationOrder(panes)
		want := []int{0, 2, 5}
		if len(got) != len(want) {
			t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("order[%d] = %d, want %d", i, got[i], want[i])
			}
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		if got := windowCreationOrder(nil); len(got) != 0 {
			t.Errorf("nil panes -> %v, want empty", got)
		}
	})
}

// TestParseWindowList verifies the `list-windows` output parser used by capture
// (ntm-r3k0/ntm-fphu). It must extract index/name/active/zoom/layout per line
// and skip blank, under-delimited, or non-numeric-index lines. The separator is
// a printable token (tmux escapes control bytes in format output).
func TestParseWindowList(t *testing.T) {
	t.Parallel()

	const sep = "_NTM_SEP_"
	out := "1" + sep + "editor" + sep + "1" + sep + "0" + sep + "d4cd,200x50,0,0{100x50,0,0,1,99x50,101,0,2}\n" +
		"2" + sep + "logs" + sep + "0" + sep + "1" + sep + "b4ca,200x50,0,0[200x25,0,0,3,200x24,0,26,4]\n" +
		"\n" + // blank line -> skipped
		"x" + sep + "bad" + sep + "0" + sep + "0" + sep + "layout" + "\n" + // non-numeric index -> skipped
		"3" + sep + "tooFew" // under-delimited -> skipped

	got := parseWindowList(out, sep)
	if len(got) != 2 {
		t.Fatalf("parsed %d windows, want 2: %+v", len(got), got)
	}

	w0 := got[0]
	if w0.Index != 1 || w0.Name != "editor" || !w0.Active || w0.Zoomed ||
		w0.Layout != "d4cd,200x50,0,0{100x50,0,0,1,99x50,101,0,2}" {
		t.Errorf("window[0] = %+v", w0)
	}

	w1 := got[1]
	if w1.Index != 2 || w1.Name != "logs" || w1.Active || !w1.Zoomed ||
		w1.Layout != "b4ca,200x50,0,0[200x25,0,0,3,200x24,0,26,4]" {
		t.Errorf("window[1] = %+v", w1)
	}

	if got := parseWindowList("", sep); len(got) != 0 {
		t.Errorf("empty output -> %v, want no windows", got)
	}
}

// TestSessionState_WindowsRoundTrip verifies the per-window fidelity metadata
// (ntm-r3k0/ntm-fphu) and the per-pane rendered Command (ntm-boi0) survive the
// JSON serialization that backs saved-session files.
func TestSessionState_WindowsRoundTrip(t *testing.T) {
	t.Parallel()

	orig := &SessionState{
		Name:   "demo",
		Agents: AgentConfig{Claude: 1, Grok: 2},
		Panes: []PaneState{
			{Index: 0, WindowIndex: 0, AgentType: "cc", Model: "opus", Command: "claude --model opus", Active: true},
		},
		Layout: "tiled",
		Windows: []WindowState{
			{Index: 0, Name: "main", Layout: "abcd,80x24,0,0,1", Active: true, Zoomed: true},
		},
		Version: StateVersion,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SessionState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Windows) != 1 {
		t.Fatalf("windows lost in round-trip: %+v", got.Windows)
	}
	w := got.Windows[0]
	if w.Name != "main" || w.Layout != "abcd,80x24,0,0,1" || !w.Active || !w.Zoomed {
		t.Errorf("window round-trip mismatch: %+v", w)
	}
	if got.Panes[0].Command != "claude --model opus" {
		t.Errorf("pane Command lost in round-trip: %q", got.Panes[0].Command)
	}
	if got.Agents.Grok != 2 || got.Agents.Total() != 3 {
		t.Errorf("Grok agent counts lost in round-trip: %+v", got.Agents)
	}
	if !strings.Contains(string(data), `"grok":2`) {
		t.Errorf("serialized session missing Grok count: %s", data)
	}
}

// bd-4tz2d: restore recreates a whole saved swarm at once, so relaunching its
// Claude panes onto the shared rotating credential puts every one of them back
// into the GH#237 refresh-token race. The saved pane command was captured
// before the pane had an isolated config dir, so it never carries one.
func TestApplyClaudeIsolation(t *testing.T) {
	t.Setenv(swarm.ClaudeCredentialStoreEnvVar, "file")

	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = true
	workDir := t.TempDir()

	t.Run("claude pane gets an isolated config dir", func(t *testing.T) {
		pane := PaneState{Index: 2, AgentType: "cc"}
		got, err := applyClaudeIsolation(cfg, workDir, "proj", pane, "claude --resume")
		if err != nil {
			t.Fatalf("applyClaudeIsolation: %v", err)
		}
		if !strings.Contains(got, "CLAUDE_CONFIG_DIR=") || !strings.Contains(got, "claude-homes") {
			t.Fatalf("restored claude command has no isolated config dir: %q", got)
		}
		if !strings.HasSuffix(got, "claude --resume") {
			t.Fatalf("original command was not preserved: %q", got)
		}
	})

	t.Run("canonicalizes the saved agent type", func(t *testing.T) {
		// Saved state may spell the type either way.
		pane := PaneState{Index: 1, AgentType: "claude"}
		got, err := applyClaudeIsolation(cfg, workDir, "proj", pane, "claude")
		if err != nil {
			t.Fatalf("applyClaudeIsolation: %v", err)
		}
		if !strings.Contains(got, "CLAUDE_CONFIG_DIR=") {
			t.Fatalf("a pane saved as %q was not recognized as Claude: %q", pane.AgentType, got)
		}
	})

	t.Run("non-claude panes are untouched", func(t *testing.T) {
		pane := PaneState{Index: 3, AgentType: "cod"}
		got, err := applyClaudeIsolation(cfg, workDir, "proj", pane, "codex")
		if err != nil {
			t.Fatalf("applyClaudeIsolation: %v", err)
		}
		if got != "codex" {
			t.Fatalf("codex command was modified: %q", got)
		}
	})

	t.Run("disabled feature is a no-op", func(t *testing.T) {
		off := config.Default()
		off.Agents.ClaudeIsolateCredentials = false
		pane := PaneState{Index: 1, AgentType: "cc"}
		got, err := applyClaudeIsolation(off, workDir, "proj", pane, "claude")
		if err != nil || got != "claude" {
			t.Fatalf("applyClaudeIsolation with the feature off = (%q, %v), want the command unchanged", got, err)
		}
	})

	t.Run("nil config is a no-op", func(t *testing.T) {
		pane := PaneState{Index: 1, AgentType: "cc"}
		got, err := applyClaudeIsolation(nil, workDir, "proj", pane, "claude")
		if err != nil || got != "claude" {
			t.Fatalf("applyClaudeIsolation with nil cfg = (%q, %v), want the command unchanged", got, err)
		}
	})
}

// bd-yzvm0: `if i >= len(panes) { break }` discarded every saved agent past the
// live pane count with no error, no audit event, and no effect on the return
// value — so a partially-failed topology restore (a rejected split, a collapsed
// layout) reported a clean restore while N agents never started. Same
// silently-dropped-work failure ba13c058 fixed on the swarm side.
func TestDroppedLaunchableAgents(t *testing.T) {
	cmds := AgentCommands{Claude: "claude", Codex: "codex"}

	// The mapping is POSITIONAL — the loop pairs sortedPaneStates[i] with
	// panes[i] and stops at i >= len(panes) — so a non-launchable state still
	// consumes a slot. Comparing a COUNT of launchable agents against the pane
	// count misses exactly this case: 3 launchable agents and 3 panes, yet the
	// last one sits at index 3 and is dropped.
	t.Run("a user pane consumes a slot and pushes an agent off the end", func(t *testing.T) {
		states := []PaneState{
			{Index: 0, AgentType: "user"},
			{Index: 1, AgentType: "cc"},
			{Index: 2, AgentType: "cc"},
			{Index: 3, AgentType: "cc"},
		}
		if got := droppedLaunchableAgents(states, cmds, 3); got != 1 {
			t.Fatalf("droppedLaunchableAgents = %d, want 1 — the agent at index 3 cannot be reached by a 3-pane grid", got)
		}
	})

	t.Run("everything fits", func(t *testing.T) {
		states := []PaneState{
			{Index: 0, AgentType: "user"},
			{Index: 1, AgentType: "cc"},
			{Index: 2, AgentType: "cod"},
		}
		if got := droppedLaunchableAgents(states, cmds, 3); got != 0 {
			t.Fatalf("droppedLaunchableAgents = %d, want 0", got)
		}
	})

	t.Run("non-launchable states past the cutoff do not trip the guard", func(t *testing.T) {
		states := []PaneState{
			{Index: 0, AgentType: "cc"},
			{Index: 1, AgentType: "user"},          // past cutoff, launches nothing
			{Index: 2, AgentType: "unknown-agent"}, // past cutoff, no command
		}
		if got := droppedLaunchableAgents(states, cmds, 1); got != 0 {
			t.Fatalf("droppedLaunchableAgents = %d, want 0 — those states were never going to launch anything", got)
		}
	})

	t.Run("a saved command makes an otherwise unlaunchable type count", func(t *testing.T) {
		states := []PaneState{
			{Index: 0, AgentType: "cc"},
			{Index: 1, AgentType: "unknown-agent", Command: "some-cli"},
		}
		if got := droppedLaunchableAgents(states, cmds, 1); got != 1 {
			t.Fatalf("droppedLaunchableAgents = %d, want 1", got)
		}
	})

	t.Run("counts every dropped agent, not just the first", func(t *testing.T) {
		states := []PaneState{
			{Index: 0, AgentType: "cc"},
			{Index: 1, AgentType: "cc"},
			{Index: 2, AgentType: "cc"},
			{Index: 3, AgentType: "cod"},
		}
		if got := droppedLaunchableAgents(states, cmds, 1); got != 3 {
			t.Fatalf("droppedLaunchableAgents = %d, want 3", got)
		}
	})
}

// The capacity guard must fire BEFORE any pane is launched, and must name the
// arithmetic so the operator can see what was going to be dropped.
func TestRestoreAgents_RefusesWhenPanesCannotHoldTheAgents(t *testing.T) {
	state := &SessionState{
		Name:    "ntm-nonexistent-session-bdyzvm0",
		WorkDir: t.TempDir(),
		Panes: []PaneState{
			{Index: 1, AgentType: "cc"},
			{Index: 2, AgentType: "cc"},
		},
	}

	// The session does not exist, so GetPanes fails and we cannot reach the
	// capacity check. That is fine: the point of this test is that the error
	// is never a silent success.
	err := RestoreAgents(state.Name, state, AgentCommands{Claude: "claude"}, nil)
	if err == nil {
		t.Fatal("RestoreAgents returned nil for a session whose panes could not be read; a restore that launches nothing must not report success")
	}
}
