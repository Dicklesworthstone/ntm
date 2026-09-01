package agentmail

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestLoadBestSessionAgentRegistry_PrefersUsableProject(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	sessionName := "test-best-registry"
	staleProject := filepath.Join(tmpDir, "stale-project")
	actualProject := filepath.Join(tmpDir, "actual-project")
	// Leave staleProject absent. Besides matching a genuinely stale registry,
	// this keeps the fixture isolated when a remote test runner places
	// t.TempDir beneath an unrelated repository with its own .beads/.git.
	if err := os.MkdirAll(filepath.Join(actualProject, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	stale := NewSessionAgentRegistry(sessionName, staleProject)
	stale.AddAgent("pane-stale", "%1", "OldAgent")
	if err := SaveSessionAgentRegistry(stale); err != nil {
		t.Fatalf("SaveSessionAgentRegistry(stale) error: %v", err)
	}

	actual := NewSessionAgentRegistry(sessionName, actualProject)
	actual.AddAgent("pane-good", "%2", "GoodAgent")
	actual.UpdatedAt = time.Now().Add(-1 * time.Hour)
	if err := SaveSessionAgentRegistry(actual); err != nil {
		t.Fatalf("SaveSessionAgentRegistry(actual) error: %v", err)
	}

	loaded, err := LoadBestSessionAgentRegistry(sessionName, filepath.Join(tmpDir, "missing-project"))
	if err != nil {
		t.Fatalf("LoadBestSessionAgentRegistry error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil registry")
	}
	if loaded.ProjectKey != actualProject {
		t.Fatalf("ProjectKey = %q, want %q", loaded.ProjectKey, actualProject)
	}
	if name, ok := loaded.GetAgent("pane-good", "%2"); !ok || name != "GoodAgent" {
		t.Fatalf("expected GoodAgent mapping, got %q %v", name, ok)
	}
}

func TestLoadBestSessionAgent_PrefersUsableProject(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	sessionName := "test-best-agent"
	staleProject := filepath.Join(tmpDir, "stale-project")
	actualProject := filepath.Join(tmpDir, "actual-project")
	// The stale artifact points at a project that no longer exists. Keeping
	// the path absent prevents enclosing project markers from influencing its
	// score on runners whose temporary directory lives inside a checkout.
	if err := os.MkdirAll(filepath.Join(actualProject, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	staleInfo := &SessionAgentInfo{
		AgentName:    "OldAgent",
		ProjectKey:   staleProject,
		RegisteredAt: time.Now().Add(-2 * time.Hour),
		LastActiveAt: time.Now().Add(-2 * time.Hour),
	}
	if err := SaveSessionAgent(sessionName, staleProject, staleInfo); err != nil {
		t.Fatalf("SaveSessionAgent(stale) error: %v", err)
	}

	actualInfo := &SessionAgentInfo{
		AgentName:    "GoodAgent",
		ProjectKey:   actualProject,
		RegisteredAt: time.Now().Add(-1 * time.Hour),
		LastActiveAt: time.Now().Add(-1 * time.Hour),
	}
	if err := SaveSessionAgent(sessionName, actualProject, actualInfo); err != nil {
		t.Fatalf("SaveSessionAgent(actual) error: %v", err)
	}

	loaded, err := LoadBestSessionAgent(sessionName, filepath.Join(tmpDir, "missing-project"))
	if err != nil {
		t.Fatalf("LoadBestSessionAgent error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil agent info")
	}
	if loaded.ProjectKey != actualProject {
		t.Fatalf("ProjectKey = %q, want %q", loaded.ProjectKey, actualProject)
	}
	if loaded.AgentName != "GoodAgent" {
		t.Fatalf("AgentName = %q, want %q", loaded.AgentName, "GoodAgent")
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

// A fresh coordinator registration must persist the server-issued identity
// AND its registration token, and prime the client's in-process token cache,
// so every later ntm process re-claims the same adjective+noun identity
// instead of sending as an unregistered literal (GH coordinator-identity fix;
// PR #274/#275 context).
func TestRegisterSessionAgent_PersistsIdentityAndToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"health_check": func(map[string]interface{}) (interface{}, *JSONRPCError) {
			return map[string]interface{}{"status": "ok"}, nil
		},
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			key, _ := args["human_key"].(string)
			return Project{ID: 1, HumanKey: key}, nil
		},
		"register_agent": func(map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{ID: 2, Name: "GreenCastle", RegistrationToken: "tok-abc123"}, nil
		},
	}))
	t.Cleanup(server.Close)

	client := NewClient(WithBaseURL(server.URL + "/"))
	info, err := client.RegisterSessionAgent(context.Background(), "coord-fresh", "/proj/coord-fresh")
	if err != nil {
		t.Fatalf("RegisterSessionAgent: %v", err)
	}
	if info == nil || info.AgentName != "GreenCastle" || info.RegistrationToken != "tok-abc123" {
		t.Fatalf("info = %+v, want GreenCastle with tok-abc123", info)
	}

	loaded, err := LoadSessionAgent("coord-fresh", "/proj/coord-fresh")
	if err != nil || loaded == nil {
		t.Fatalf("LoadSessionAgent = (%+v, %v)", loaded, err)
	}
	if loaded.RegistrationToken != "tok-abc123" {
		t.Fatalf("persisted token = %q, want tok-abc123", loaded.RegistrationToken)
	}
	if tok := client.RegistrationToken("/proj/coord-fresh", "GreenCastle"); tok != "tok-abc123" {
		t.Fatalf("client token cache = %q, want tok-abc123", tok)
	}
}

// Re-registration for an already-persisted session identity must send the
// persisted registration token so the server authenticates the re-claim
// rather than rejecting the name as already taken.
func TestRegisterSessionAgent_ReusesPersistedTokenOnReclaim(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	if err := SaveSessionAgent("coord-reuse", "/proj/coord-reuse", &SessionAgentInfo{
		AgentName:         "GreenCastle",
		ProjectKey:        "/proj/coord-reuse",
		RegistrationToken: "tok-persisted",
		RegisteredAt:      time.Now().Add(-time.Hour),
		LastActiveAt:      time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed session agent: %v", err)
	}

	var gotToken string
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Project{ID: 2, HumanKey: args["human_key"].(string)}, nil
		},
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			gotToken, _ = args["registration_token"].(string)
			return Agent{ID: 2, Name: "GreenCastle", RegistrationToken: "tok-persisted"}, nil
		},
	}))
	t.Cleanup(server.Close)

	client := NewClient(WithBaseURL(server.URL + "/"))
	info, err := client.RegisterSessionAgent(context.Background(), "coord-reuse", "/proj/coord-reuse")
	if err != nil {
		t.Fatalf("RegisterSessionAgent: %v", err)
	}
	if info == nil || info.AgentName != "GreenCastle" {
		t.Fatalf("info = %+v, want reused GreenCastle identity", info)
	}
	if gotToken != "tok-persisted" {
		t.Fatalf("register_agent received token %q, want the persisted tok-persisted", gotToken)
	}
}

// TestRegisterSessionAgentProbesWithEnsureProject verifies the availability
// probe is the lightweight ensure_project call rather than the heavier
// health_check diagnostic: a loaded-but-healthy server that would blow the
// health_check budget must still yield a session identity.
func TestRegisterSessionAgentProbesWithEnsureProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var mu sync.Mutex
	var called []string
	record := func(name string) {
		mu.Lock()
		called = append(called, name)
		mu.Unlock()
	}

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			record("ensure_project")
			return Project{ID: 7, HumanKey: args["human_key"].(string)}, nil
		},
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			record("register_agent")
			return Agent{ID: 8, Name: "WhiteHorse", Program: args["program"].(string), Model: args["model"].(string), RegistrationToken: "session-token"}, nil
		},
		// No health_check handler: mockMCPHandler fails the exchange for any
		// unhandled tool, so a health_check probe would surface as an error.
	}))
	t.Cleanup(server.Close)

	client := NewClient(WithBaseURL(server.URL + "/"))
	info, err := client.RegisterSessionAgent(context.Background(), "session-probe-test", "/project/probe-test")
	if err != nil {
		t.Fatalf("RegisterSessionAgent: %v", err)
	}
	if info == nil || info.AgentName != "WhiteHorse" || info.RegistrationToken != "session-token" {
		t.Fatalf("session info = %+v", info)
	}

	mu.Lock()
	tools := append([]string(nil), called...)
	mu.Unlock()
	if len(tools) < 2 || tools[0] != "ensure_project" {
		t.Fatalf("ensure_project must be the first (probe) call, got %v", tools)
	}
	for _, tool := range tools {
		if tool == "health_check" {
			t.Fatalf("registration must not be gated on health_check, got %v", tools)
		}
	}

	loaded, err := LoadSessionAgent("session-probe-test", "/project/probe-test")
	if err != nil || loaded == nil || loaded.RegistrationToken != "session-token" {
		t.Fatalf("persisted session info = %+v, error=%v", loaded, err)
	}
	if token := client.RegistrationToken("/project/probe-test", "WhiteHorse"); token != "session-token" {
		t.Fatalf("client token cache = %q, want session-token", token)
	}
}

// TestRegisterSessionAgentEnsuresProjectOnReRegister verifies the existing
// registration fast path still runs ensure_project first: Agent Mail may have
// rebuilt its database since the identity was persisted locally.
func TestRegisterSessionAgentEnsuresProjectOnReRegister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if err := SaveSessionAgent("session-rebuild-test", "/project/rebuild-test", &SessionAgentInfo{
		AgentName:  "WhiteHorse",
		ProjectKey: "/project/rebuild-test",
	}); err != nil {
		t.Fatalf("seed session agent: %v", err)
	}

	var mu sync.Mutex
	ensures := 0
	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			mu.Lock()
			ensures++
			mu.Unlock()
			return Project{ID: 7, HumanKey: args["human_key"].(string)}, nil
		},
		"register_agent": func(args map[string]interface{}) (interface{}, *JSONRPCError) {
			return Agent{ID: 8, Name: args["name"].(string)}, nil
		},
	}))
	t.Cleanup(server.Close)

	client := NewClient(WithBaseURL(server.URL + "/"))
	info, err := client.RegisterSessionAgent(context.Background(), "session-rebuild-test", "/project/rebuild-test")
	if err != nil {
		t.Fatalf("RegisterSessionAgent: %v", err)
	}
	if info == nil || info.AgentName != "WhiteHorse" {
		t.Fatalf("session info = %+v", info)
	}
	mu.Lock()
	defer mu.Unlock()
	if ensures != 1 {
		t.Fatalf("ensure_project calls = %d, want 1", ensures)
	}
}

// TestRegisterSessionAgentFailsClosedWhenProjectEnsureFails verifies the probe
// failure is reported instead of the old silent (nil, nil) skip.
func TestRegisterSessionAgentFailsClosedWhenProjectEnsureFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	server := httptest.NewServer(mockMCPHandler(t, map[string]func(args map[string]interface{}) (interface{}, *JSONRPCError){
		"ensure_project": func(map[string]interface{}) (interface{}, *JSONRPCError) {
			return nil, &JSONRPCError{Code: -32000, Message: "database unavailable"}
		},
	}))
	t.Cleanup(server.Close)

	client := NewClient(WithBaseURL(server.URL + "/"))
	info, err := client.RegisterSessionAgent(context.Background(), "session-down-test", "/project/down-test")
	if err == nil || info != nil {
		t.Fatalf("RegisterSessionAgent = (%+v, %v), want ensure-project error", info, err)
	}
	if !strings.Contains(err.Error(), "ensuring project") {
		t.Fatalf("error should identify the failed probe, got %v", err)
	}
}
