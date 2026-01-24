package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestSpawnSessionLogic(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-projects")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize global cfg (unexported in cli package, but accessible here)
	// Save/Restore to prevent side effects
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true

	// Override templates to avoid dependency on actual agent binaries
	cfg.Agents.Claude = "echo 'Claude started'; sleep 10"
	cfg.Agents.Codex = "echo 'Codex started'; sleep 10"
	cfg.Agents.Gemini = "echo 'Gemini started'; sleep 10"

	// Unique session name
	sessionName := fmt.Sprintf("ntm-test-spawn-%d", time.Now().UnixNano())

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-3-5-sonnet-20241022"},
	}

	// Pre-create project directory to avoid interactive prompt
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Execute spawn
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogic(opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Validate session exists
	if !tmux.SessionExists(sessionName) {
		t.Errorf("session %s was not created", sessionName)
	}

	// Validate panes
	// Expected: 1 user pane + 1 claude pane = 2 panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	if len(panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(panes))
	}

	// Validate user pane and agent pane
	foundClaude := false
	for _, p := range panes {
		if p.Type == tmux.AgentClaude {
			foundClaude = true
			// Check title format: session__type_index_variant
			expectedTitle := fmt.Sprintf("%s__cc_1_claude-3-5-sonnet-20241022", sessionName)
			if p.Title != expectedTitle {
				t.Errorf("expected pane title %q, got %q", expectedTitle, p.Title)
			}
		}
	}

	if !foundClaude {
		t.Error("did not find Claude agent pane")
	}

	// Verify project directory creation
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		t.Errorf("project directory %s was not created", projectDir)
	}
}

// bd-3f53: Tests for getMemoryContext and formatMemoryContext

func TestFormatMemoryContext_Nil(t *testing.T) {
	t.Parallel()

	result := formatMemoryContext(nil)
	if result != "" {
		t.Errorf("formatMemoryContext(nil) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_EmptyResult(t *testing.T) {
	t.Parallel()

	result := formatMemoryContext(&cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.CLIRule{},
		AntiPatterns:    []cm.CLIRule{},
	})
	if result != "" {
		t.Errorf("formatMemoryContext(empty) = %q, want empty string", result)
	}
}

func TestFormatMemoryContext_RulesOnly(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.CLIRule{
			{ID: "b-8f3a2c", Content: "Always use structured logging with log/slog", Category: "best-practice"},
			{ID: "b-4e1d7b", Content: "Database migrations must be idempotent", Category: "database"},
		},
		AntiPatterns: []cm.CLIRule{},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Check rules section
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section header")
	}

	// Check rule formatting
	if !strings.Contains(result, "[b-8f3a2c] Always use structured logging with log/slog") {
		t.Error("missing first rule")
	}
	if !strings.Contains(result, "[b-4e1d7b] Database migrations must be idempotent") {
		t.Error("missing second rule")
	}

	// Should NOT have anti-patterns section
	if strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("should not have Anti-Patterns section when empty")
	}
}

func TestFormatMemoryContext_AntiPatternsOnly(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success:         true,
		Task:            "test task",
		RelevantBullets: []cm.CLIRule{},
		AntiPatterns: []cm.CLIRule{
			{ID: "b-7d3e8c", Content: "Don't add backwards-compatibility shims", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check header
	if !strings.Contains(result, "# Project Memory from Past Sessions") {
		t.Error("missing main header")
	}

	// Should NOT have rules section
	if strings.Contains(result, "## Key Rules for This Project") {
		t.Error("should not have Key Rules section when empty")
	}

	// Check anti-patterns section
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section header")
	}
	if !strings.Contains(result, "[b-7d3e8c] Don't add backwards-compatibility shims") {
		t.Error("missing anti-pattern")
	}
}

func TestFormatMemoryContext_BothSections(t *testing.T) {
	t.Parallel()

	resp := &cm.CLIContextResponse{
		Success: true,
		Task:    "test task",
		RelevantBullets: []cm.CLIRule{
			{ID: "b-rule1", Content: "Use Go 1.25 features", Category: "best-practice"},
		},
		AntiPatterns: []cm.CLIRule{
			{ID: "b-anti1", Content: "Avoid using deprecated APIs", Category: "anti-pattern"},
		},
	}

	result := formatMemoryContext(resp)

	// Check both sections present
	if !strings.Contains(result, "## Key Rules for This Project") {
		t.Error("missing Key Rules section")
	}
	if !strings.Contains(result, "## Anti-Patterns to Avoid") {
		t.Error("missing Anti-Patterns section")
	}

	// Check both items present
	if !strings.Contains(result, "[b-rule1]") {
		t.Error("missing rule ID")
	}
	if !strings.Contains(result, "[b-anti1]") {
		t.Error("missing anti-pattern ID")
	}

	// Check order: rules should come before anti-patterns
	rulesIdx := strings.Index(result, "## Key Rules")
	antiIdx := strings.Index(result, "## Anti-Patterns")
	if rulesIdx > antiIdx {
		t.Error("Key Rules should appear before Anti-Patterns")
	}
}

func TestGetMemoryContext_ConfigDisabled(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	// Create config with CM memories disabled
	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = false

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with disabled config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_NilConfig(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = nil

	result := getMemoryContext("test-project", "test task")
	if result != "" {
		t.Errorf("getMemoryContext with nil config = %q, want empty string", result)
	}
}

func TestGetMemoryContext_EmptyTask(t *testing.T) {
	t.Parallel()

	// Save and restore global config
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = config.Default()
	cfg.SessionRecovery.IncludeCMMemories = true

	// This test verifies the function handles empty task gracefully
	// Even if CM is not installed, it should return empty string without error
	result := getMemoryContext("test-project", "")

	// Result should be empty (CM likely not installed in test environment)
	// but the function should not panic
	_ = result // Just verify no panic
}
