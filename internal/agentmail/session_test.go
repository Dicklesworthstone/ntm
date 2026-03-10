package agentmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"myproject", "myproject"},
		{"my-project", "my_project"},
		{"my_project", "my_project"},
		{"MyProject", "myproject"},
		{"my.project.name", "my_project_name"},
		{"project@123", "project_123"},
		{"---project---", "project"},
		{"Project With Spaces", "project_with_spaces"},
		{"...", "hex_2e2e2e"}, // "..." -> "" -> hex
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeSessionName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeSessionName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSessionAgentPath(t *testing.T) {
	path := sessionAgentPath("myproject", "/abs/path/to/project")
	if !filepath.IsAbs(path) {
		t.Errorf("sessionAgentPath should return absolute path, got %q", path)
	}
	if !contains(path, "myproject") {
		t.Errorf("sessionAgentPath should contain session name, got %q", path)
	}
	if !contains(path, "agent.json") {
		t.Errorf("sessionAgentPath should end with agent.json, got %q", path)
	}
}

func TestLoadSaveSessionAgent(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "agentmail-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override the config dir for testing
	// On Linux, os.UserConfigDir() uses XDG_CONFIG_HOME, not HOME
	// On macOS, os.UserConfigDir() uses HOME/Library/Application Support
	// t.Setenv handles cleanup automatically
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	sessionName := "test-session"

	// Initially no agent should be loaded
	info, err := LoadSessionAgent(sessionName, "/path/to/project")
	if err != nil {
		t.Fatalf("LoadSessionAgent failed: %v", err)
	}
	if info != nil {
		t.Error("Expected nil info for non-existent session")
	}

	// Save agent info
	saveInfo := &SessionAgentInfo{
		AgentName:  "ntm_test_session",
		ProjectKey: "/path/to/project",
	}
	if err := SaveSessionAgent(sessionName, saveInfo.ProjectKey, saveInfo); err != nil {
		t.Fatalf("SaveSessionAgent failed: %v", err)
	}

	// Load it back
	loaded, err := LoadSessionAgent(sessionName, saveInfo.ProjectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgent failed after save: %v", err)
	}
	if loaded == nil {
		t.Fatal("Expected loaded info to be non-nil")
	}
	if loaded.AgentName != saveInfo.AgentName {
		t.Errorf("AgentName = %q, want %q", loaded.AgentName, saveInfo.AgentName)
	}
	if loaded.ProjectKey != saveInfo.ProjectKey {
		t.Errorf("ProjectKey = %q, want %q", loaded.ProjectKey, saveInfo.ProjectKey)
	}

	// Delete agent info
	if err := DeleteSessionAgent(sessionName, saveInfo.ProjectKey); err != nil {
		t.Fatalf("DeleteSessionAgent failed: %v", err)
	}

	// Verify it's gone
	info, err = LoadSessionAgent(sessionName, saveInfo.ProjectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgent failed after delete: %v", err)
	}
	if info != nil {
		t.Error("Expected nil info after delete")
	}
}

func TestIsNameTakenError(t *testing.T) {
	tests := []struct {
		errStr   string
		expected bool
	}{
		{"name already in use", true},
		{"agent name taken", true},
		{"agent already registered", true},
		{"some other error", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.errStr, func(t *testing.T) {
			var err error
			if tt.errStr != "" {
				err = NewAPIError("test", 0, &testError{msg: tt.errStr})
			}
			result := IsNameTakenError(err)
			if result != tt.expected {
				t.Errorf("IsNameTakenError(%q) = %v, want %v", tt.errStr, result, tt.expected)
			}
		})
	}
}

// Helper functions for tests

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// Tests for SessionAgentRegistry

func TestNewSessionAgentRegistry(t *testing.T) {
	sessionName := "test-session"
	projectKey := "/test/project"

	registry := NewSessionAgentRegistry(sessionName, projectKey)

	if registry.SessionName != sessionName {
		t.Errorf("session name mismatch: got %q, want %q", registry.SessionName, sessionName)
	}
	if registry.ProjectKey != projectKey {
		t.Errorf("project key mismatch: got %q, want %q", registry.ProjectKey, projectKey)
	}
	if registry.Agents == nil {
		t.Error("Agents map should not be nil")
	}
	if registry.PaneIDMap == nil {
		t.Error("PaneIDMap should not be nil")
	}
	if registry.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should be set")
	}
	if registry.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestSessionAgentRegistry_AddAgent(t *testing.T) {
	registry := NewSessionAgentRegistry("test-session", "/test/project")

	// Add first agent
	registry.AddAgent("test__cc_1", "%1", "GreenCastle")
	if registry.Count() != 1 {
		t.Errorf("expected count 1, got %d", registry.Count())
	}

	// Add second agent
	registry.AddAgent("test__cod_1", "%2", "BlueLake")
	if registry.Count() != 2 {
		t.Errorf("expected count 2, got %d", registry.Count())
	}

	// Add agent with empty pane ID (should still work)
	registry.AddAgent("test__gmi_1", "", "RedStone")
	if registry.Count() != 3 {
		t.Errorf("expected count 3, got %d", registry.Count())
	}

	// Verify pane ID map has only 2 entries (one had empty ID)
	if len(registry.PaneIDMap) != 2 {
		t.Errorf("expected PaneIDMap length 2, got %d", len(registry.PaneIDMap))
	}
}

func TestSessionAgentRegistry_AddAgentWithNilMaps(t *testing.T) {
	// Test that AddAgent handles nil maps gracefully
	registry := &SessionAgentRegistry{
		SessionName: "test",
		ProjectKey:  "/test",
		// Agents and PaneIDMap intentionally nil
	}

	// Should not panic
	registry.AddAgent("pane1", "id1", "Agent1")

	if registry.Agents == nil {
		t.Error("Agents should be initialized")
	}
	if registry.PaneIDMap == nil {
		t.Error("PaneIDMap should be initialized")
	}
	if registry.Count() != 1 {
		t.Errorf("expected count 1, got %d", registry.Count())
	}
}

func TestSessionAgentRegistry_AddAgent_RefreshesMappingsForSameAgent(t *testing.T) {
	registry := NewSessionAgentRegistry("test-session", "/test/project")

	registry.AddAgent("old_title", "%1", "GreenCastle")
	registry.AddAgent("new_title", "%9", "GreenCastle")

	if _, ok := registry.GetAgentByTitle("old_title"); ok {
		t.Fatal("expected old title mapping to be removed")
	}
	if _, ok := registry.GetAgentByID("%1"); ok {
		t.Fatal("expected old pane ID mapping to be removed")
	}

	name, ok := registry.GetAgentByTitle("new_title")
	if !ok || name != "GreenCastle" {
		t.Fatalf("expected new title mapping to GreenCastle, got %q, %v", name, ok)
	}
	name, ok = registry.GetAgentByID("%9")
	if !ok || name != "GreenCastle" {
		t.Fatalf("expected new pane ID mapping to GreenCastle, got %q, %v", name, ok)
	}
	if registry.Count() != 1 {
		t.Fatalf("expected count 1 after remap, got %d", registry.Count())
	}
}

func TestSessionAgentRegistry_GetAgentByTitle(t *testing.T) {
	registry := NewSessionAgentRegistry("test-session", "/test/project")
	registry.AddAgent("test__cc_1", "%1", "GreenCastle")

	// Test found
	name, ok := registry.GetAgentByTitle("test__cc_1")
	if !ok {
		t.Error("expected to find agent by title")
	}
	if name != "GreenCastle" {
		t.Errorf("expected GreenCastle, got %s", name)
	}

	// Test not found
	_, ok = registry.GetAgentByTitle("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent agent")
	}

	// Test nil registry
	var nilRegistry *SessionAgentRegistry
	_, ok = nilRegistry.GetAgentByTitle("test")
	if ok {
		t.Error("expected false for nil registry")
	}
}

func TestSessionAgentRegistry_GetAgentByID(t *testing.T) {
	registry := NewSessionAgentRegistry("test-session", "/test/project")
	registry.AddAgent("test__cc_1", "%1", "GreenCastle")

	// Test found
	name, ok := registry.GetAgentByID("%1")
	if !ok {
		t.Error("expected to find agent by ID")
	}
	if name != "GreenCastle" {
		t.Errorf("expected GreenCastle, got %s", name)
	}

	// Test not found
	_, ok = registry.GetAgentByID("%99")
	if ok {
		t.Error("expected not to find nonexistent pane ID")
	}

	// Test nil registry
	var nilRegistry *SessionAgentRegistry
	_, ok = nilRegistry.GetAgentByID("%1")
	if ok {
		t.Error("expected false for nil registry")
	}
}

func TestSessionAgentRegistry_GetAgent(t *testing.T) {
	registry := NewSessionAgentRegistry("test-session", "/test/project")
	registry.AddAgent("test__cc_1", "%1", "GreenCastle")
	registry.AddAgent("test__cod_1", "%2", "BlueLake")

	// Test found by title
	name, ok := registry.GetAgent("test__cc_1", "")
	if !ok {
		t.Error("expected to find agent")
	}
	if name != "GreenCastle" {
		t.Errorf("expected GreenCastle, got %s", name)
	}

	// Test found by ID (when title doesn't match)
	name, ok = registry.GetAgent("wrong_title", "%2")
	if !ok {
		t.Error("expected to find agent by ID fallback")
	}
	if name != "BlueLake" {
		t.Errorf("expected BlueLake, got %s", name)
	}

	// Test not found by either
	_, ok = registry.GetAgent("wrong_title", "%99")
	if ok {
		t.Error("expected not to find agent")
	}
}

func TestSessionAgentRegistry_Count(t *testing.T) {
	// Test nil registry
	var nilRegistry *SessionAgentRegistry
	if nilRegistry.Count() != 0 {
		t.Error("expected count 0 for nil registry")
	}

	// Test empty registry
	registry := NewSessionAgentRegistry("test", "/test")
	if registry.Count() != 0 {
		t.Error("expected count 0 for empty registry")
	}

	// Test with agents
	registry.AddAgent("pane1", "id1", "Agent1")
	registry.AddAgent("pane2", "id2", "Agent2")
	if registry.Count() != 2 {
		t.Errorf("expected count 2, got %d", registry.Count())
	}
}

func TestSessionAgentRegistryPersistence(t *testing.T) {
	// Use temp directory for testing
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	sessionName := "test-persist-session"
	projectKey := filepath.Join(tmpDir, "project")

	// Create and save registry
	registry := NewSessionAgentRegistry(sessionName, projectKey)
	registry.AddAgent("test__cc_1", "%1", "GreenCastle")
	registry.AddAgent("test__cod_1", "%2", "BlueLake")

	if err := SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("SaveSessionAgentRegistry error: %v", err)
	}

	// Load and verify
	loaded, err := LoadSessionAgentRegistry(sessionName, projectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded registry is nil")
	}

	if loaded.SessionName != sessionName {
		t.Errorf("session name mismatch: got %q, want %q", loaded.SessionName, sessionName)
	}
	if loaded.ProjectKey != projectKey {
		t.Errorf("project key mismatch: got %q, want %q", loaded.ProjectKey, projectKey)
	}
	if loaded.Count() != 2 {
		t.Errorf("agent count mismatch: got %d, want 2", loaded.Count())
	}

	name, ok := loaded.GetAgentByTitle("test__cc_1")
	if !ok || name != "GreenCastle" {
		t.Errorf("agent mapping mismatch: got %q, %v", name, ok)
	}

	// Test delete
	if err := DeleteSessionAgentRegistry(sessionName, projectKey); err != nil {
		t.Fatalf("DeleteSessionAgentRegistry error: %v", err)
	}

	// Verify deleted
	deleted, err := LoadSessionAgentRegistry(sessionName, projectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry after delete error: %v", err)
	}
	if deleted != nil {
		t.Error("expected nil after delete")
	}
}

func TestLoadSessionAgentRegistry_ProjectKeyValidation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	sessionName := "test-validation"
	projectKey := filepath.Join(tmpDir, "project1")

	// Create and save registry
	registry := NewSessionAgentRegistry(sessionName, projectKey)
	registry.AddAgent("pane1", "id1", "Agent1")
	if err := SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("SaveSessionAgentRegistry error: %v", err)
	}

	// Load with matching project key - should succeed
	loaded, err := LoadSessionAgentRegistry(sessionName, projectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil registry")
	}

	// Load with different project key - should return nil (not found)
	differentKey := filepath.Join(tmpDir, "project2")
	loaded, err = LoadSessionAgentRegistry(sessionName, differentKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil for different project key")
	}
}

func TestLoadSessionAgentRegistry_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Load non-existent registry
	loaded, err := LoadSessionAgentRegistry("nonexistent-session", "/nonexistent/project")
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil for non-existent registry")
	}
}

func TestLoadSessionAgentRegistry_CleanPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	sessionName := "test-clean-path"
	projectKey := filepath.Join(tmpDir, "project")

	// Create and save registry with clean path
	registry := NewSessionAgentRegistry(sessionName, projectKey)
	registry.AddAgent("pane1", "id1", "Agent1")
	if err := SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("SaveSessionAgentRegistry error: %v", err)
	}

	// Load with path containing trailing slash - should succeed if cleaned
	dirtyKey := projectKey + string(filepath.Separator)
	loaded, err := LoadSessionAgentRegistry(sessionName, dirtyKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil registry when loading with dirty path (trailing slash)")
	}
}

func TestSaveSessionAgentRegistry_NilError(t *testing.T) {
	err := SaveSessionAgentRegistry(nil)
	if err == nil {
		t.Error("expected error for nil registry")
	}
}

func TestRegistryPath(t *testing.T) {
	// Override config dir for predictable paths
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Test with project key
	path := registryPath("mysession", "/data/project")
	if !filepath.IsAbs(path) {
		t.Errorf("expected absolute path, got %q", path)
	}
	if !contains(path, "mysession") {
		t.Errorf("path should contain session name: %q", path)
	}
	if !contains(path, "agent_registry.json") {
		t.Errorf("path should end with agent_registry.json: %q", path)
	}

	// Test without project key
	legacyPath := registryPath("mysession", "")
	if !filepath.IsAbs(legacyPath) {
		t.Errorf("expected absolute path for no-project case, got %q", legacyPath)
	}
}

func TestSessionAgentStorageRejectsInvalidSessionNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	info := &SessionAgentInfo{
		AgentName:  "BlueLake",
		ProjectKey: "/tmp/project",
	}

	if err := SaveSessionAgent("../escape", info.ProjectKey, info); err == nil {
		t.Fatal("expected invalid session name error from SaveSessionAgent")
	}
	if _, err := LoadSessionAgent("../escape", info.ProjectKey); err == nil {
		t.Fatal("expected invalid session name error from LoadSessionAgent")
	}
	if err := DeleteSessionAgent("../escape", info.ProjectKey); err == nil {
		t.Fatal("expected invalid session name error from DeleteSessionAgent")
	}
}

func TestSessionAgentRegistryRejectsInvalidSessionNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	registry := NewSessionAgentRegistry("../escape", "/tmp/project")
	if err := SaveSessionAgentRegistry(registry); err == nil {
		t.Fatal("expected invalid session name error from SaveSessionAgentRegistry")
	}
	if _, err := LoadSessionAgentRegistry("../escape", "/tmp/project"); err == nil {
		t.Fatal("expected invalid session name error from LoadSessionAgentRegistry")
	}
	if err := DeleteSessionAgentRegistry("../escape", "/tmp/project"); err == nil {
		t.Fatal("expected invalid session name error from DeleteSessionAgentRegistry")
	}
}

func TestLoadSessionAgentRegistryContinuesPastMismatchedCandidate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	sessionName := "test-registry-continue"
	projectKey := "/tmp/project-a"
	wrongRegistry := NewSessionAgentRegistry(sessionName, "/tmp/project-b")
	wrongRegistry.AddAgent("pane-wrong", "%1", "WrongAgent")
	correctRegistry := NewSessionAgentRegistry(sessionName, projectKey)
	correctRegistry.AddAgent("pane-right", "%2", "RightAgent")

	newPath := registryPath(sessionName, projectKey)
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatalf("mkdir new path: %v", err)
	}
	wrongData, err := json.Marshal(wrongRegistry)
	if err != nil {
		t.Fatalf("marshal wrong registry: %v", err)
	}
	if err := os.WriteFile(newPath, wrongData, 0644); err != nil {
		t.Fatalf("write wrong registry: %v", err)
	}

	legacyPath := registryPath(sessionName, "")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
		t.Fatalf("mkdir legacy path: %v", err)
	}
	correctData, err := json.Marshal(correctRegistry)
	if err != nil {
		t.Fatalf("marshal correct registry: %v", err)
	}
	if err := os.WriteFile(legacyPath, correctData, 0644); err != nil {
		t.Fatalf("write correct registry: %v", err)
	}

	loaded, err := LoadSessionAgentRegistry(sessionName, projectKey)
	if err != nil {
		t.Fatalf("LoadSessionAgentRegistry error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected registry, got nil")
	}
	if loaded.ProjectKey != projectKey {
		t.Fatalf("expected project key %s, got %s", projectKey, loaded.ProjectKey)
	}
}
