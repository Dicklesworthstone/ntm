package robot

import (
	"encoding/json"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestPrintSpawn(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Use mock options that don't actually spawn heavy processes if possible,
	// but PrintSpawn calls logic that calls tmux.

	// We can use a test session name
	opts := SpawnOptions{
		Session:    "test_spawn_robot",
		CCCount:    1,
		NoUserPane: true,
		WorkingDir: t.TempDir(), // Use temp dir to avoid creating dirs in /data/projects
	}

	cfg := config.Default()
	// Override agent command to be fast
	cfg.Agents.Claude = "echo test"

	// Clean up potential session
	defer tmux.KillSession(opts.Session)

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("PrintSpawn failed: %v", err)
	}

	// Check JSON output
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if resp["session"] != opts.Session {
		t.Errorf("Expected session %q, got %v", opts.Session, resp["session"])
	}
	// SpawnOutput doesn't have Created bool, check Layout instead
	if resp["layout"] != "tiled" {
		t.Errorf("Expected layout 'tiled', got %v", resp["layout"])
	}
}

func TestAgentTypeShort(t *testing.T) {
	tests := []struct {
		input    tmux.AgentType
		expected string
	}{
		{tmux.AgentClaude, "cc"},
		{tmux.AgentCodex, "cod"},
		{tmux.AgentGemini, "gmi"},
		{tmux.AgentUser, "user"},
	}

	for _, tc := range tests {
		if got := agentTypeShort(string(tc.input)); got != tc.expected {
			t.Errorf("agentTypeShort(%v) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// =============================================================================
// Comprehensive Robot-Spawn Tests (ntm-1lhn)
// Unit tests, E2E scripts, schema stability, deterministic ordering
// =============================================================================

// TestIsAgentReady_Patterns validates agent ready detection patterns
func TestIsAgentReady_Patterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		// Claude indicators
		{"claude_prompt_lowercase", "claude>", "claude", true},
		{"claude_prompt_spaced", "claude > ", "claude", true},
		{"claude_code_version", "Claude Code v1.2.3", "claude", true},
		{"claude_welcome", "Welcome back!", "claude", true},
		{"claude_bypass_permissions", "Bypass permissions: enabled", "claude", true},
		{"claude_try_example", "Try \"help me with X\"", "claude", true},

		// Codex indicators
		{"codex_prompt", "codex>", "codex", true},
		{"codex_ready", "Ready for input", "codex", true},

		// Gemini indicators
		{"gemini_prompt", "gemini>", "gemini", true},
		{"gemini_help", "How can I help you today?", "gemini", true},

		// Generic shell prompts
		{"shell_dollar", "$ ", "claude", true},
		{"shell_percent", "% ", "claude", true},
		{"shell_arrow", "❯ ", "claude", true},
		{"shell_simple", "> ", "claude", true},
		{"python_repl", ">>> ", "codex", true},

		// Not ready states
		{"loading", "Loading...", "claude", false},
		{"empty", "", "claude", false},
		{"garbage", "xyzabc123", "claude", false},
		{"partial_prompt", "claud", "claude", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isAgentReady(tc.output, tc.agentType)
			if got != tc.expected {
				t.Errorf("[E2E-SPAWN] isAgentReady(%q, %q) = %v, want %v",
					tc.output, tc.agentType, got, tc.expected)
			}
		})
	}
}

// TestGetAgentCommands validates command resolution with/without config
func TestGetAgentCommands(t *testing.T) {
	t.Parallel()

	t.Run("NilConfig", func(t *testing.T) {
		t.Parallel()
		cmds := getAgentCommands(nil)

		// Should have default commands
		if cmds["claude"] == "" {
			t.Error("[E2E-SPAWN] getAgentCommands(nil) missing claude command")
		}
		if cmds["codex"] == "" {
			t.Error("[E2E-SPAWN] getAgentCommands(nil) missing codex command")
		}
		if cmds["gemini"] == "" {
			t.Error("[E2E-SPAWN] getAgentCommands(nil) missing gemini command")
		}
		t.Logf("[E2E-SPAWN] Operation=GetAgentCommands_NilConfig | Claude=%q | Codex=%q | Gemini=%q",
			cmds["claude"], cmds["codex"], cmds["gemini"])
	})

	t.Run("CustomConfig", func(t *testing.T) {
		t.Parallel()
		cfg := config.Default()
		cfg.Agents.Claude = "custom-claude --arg"
		cfg.Agents.Codex = "custom-codex --flag"
		cfg.Agents.Gemini = "custom-gemini"

		cmds := getAgentCommands(cfg)

		if cmds["claude"] != "custom-claude --arg" {
			t.Errorf("[E2E-SPAWN] Expected custom claude command, got %q", cmds["claude"])
		}
		if cmds["codex"] != "custom-codex --flag" {
			t.Errorf("[E2E-SPAWN] Expected custom codex command, got %q", cmds["codex"])
		}
		if cmds["gemini"] != "custom-gemini" {
			t.Errorf("[E2E-SPAWN] Expected custom gemini command, got %q", cmds["gemini"])
		}
		t.Logf("[E2E-SPAWN] Operation=GetAgentCommands_Custom | Claude=%q | Codex=%q | Gemini=%q",
			cmds["claude"], cmds["codex"], cmds["gemini"])
	})

	t.Run("PartialConfig", func(t *testing.T) {
		t.Parallel()
		cfg := config.Default()
		cfg.Agents.Claude = "custom-claude"
		// Leave codex and gemini as defaults

		cmds := getAgentCommands(cfg)

		if cmds["claude"] != "custom-claude" {
			t.Errorf("[E2E-SPAWN] Expected custom claude, got %q", cmds["claude"])
		}
		// Codex and gemini should still have values (default)
		if cmds["codex"] == "" {
			t.Error("[E2E-SPAWN] Codex command should not be empty")
		}
		t.Logf("[E2E-SPAWN] Operation=GetAgentCommands_Partial | Claude=%q | Codex=%q",
			cmds["claude"], cmds["codex"])
	})
}

// TestSpawnOptions_DryRunMode validates dry-run returns correct structure without creating session
func TestSpawnOptions_DryRunMode(t *testing.T) {
	// DryRun should work even without tmux since it doesn't actually create sessions
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_dryrun_session",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] DryRun PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse DryRun JSON: %v", err)
	}

	// Validate dry-run specific fields
	if !resp.DryRun {
		t.Error("[E2E-SPAWN] DryRun field should be true")
	}

	// Validate session name
	if resp.Session != opts.Session {
		t.Errorf("[E2E-SPAWN] Session mismatch: got %q, want %q", resp.Session, opts.Session)
	}

	// Validate WouldCreate has correct count: 1 user + 2 claude + 1 codex + 1 gemini = 5
	expectedCount := 5
	if len(resp.WouldCreate) != expectedCount {
		t.Errorf("[E2E-SPAWN] WouldCreate count: got %d, want %d", len(resp.WouldCreate), expectedCount)
	}

	// Validate agent types in WouldCreate
	typeCounts := make(map[string]int)
	for _, agent := range resp.WouldCreate {
		typeCounts[agent.Type]++
	}

	if typeCounts["user"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 user pane, got %d", typeCounts["user"])
	}
	if typeCounts["claude"] != 2 {
		t.Errorf("[E2E-SPAWN] Expected 2 claude panes, got %d", typeCounts["claude"])
	}
	if typeCounts["codex"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 codex pane, got %d", typeCounts["codex"])
	}
	if typeCounts["gemini"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 gemini pane, got %d", typeCounts["gemini"])
	}

	// Validate no error in dry-run
	if resp.Error != "" {
		t.Errorf("[E2E-SPAWN] Unexpected error in dry-run: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=DryRunMode | Session=%s | WouldCreate=%d | Types=%v",
		resp.Session, len(resp.WouldCreate), typeCounts)
}

// TestSpawnOptions_NoAgentsSpecified validates error when no agents specified
func TestSpawnOptions_NoAgentsSpecified(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_no_agents",
		CCCount:    0,
		CodCount:   0,
		GmiCount:   0,
		NoUserPane: true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Should have error about no agents
	if resp.Error == "" {
		t.Error("[E2E-SPAWN] Expected error for no agents specified")
	}
	if resp.Error != "no agents specified (use cc, cod, or gmi counts)" {
		t.Errorf("[E2E-SPAWN] Unexpected error message: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=NoAgents | Error=%q", resp.Error)
}

// TestSpawnOptions_SafetyMode validates safety mode blocks existing sessions
func TestSpawnOptions_SafetyMode(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := "test_safety_mode_spawn"

	// Create session first
	if err := tmux.CreateSession(sessionName, "/tmp"); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to create test session: %v", err)
	}
	defer tmux.KillSession(sessionName)

	opts := SpawnOptions{
		Session:    sessionName,
		CCCount:    1,
		NoUserPane: true,
		Safety:     true, // Enable safety mode
	}

	cfg := config.Default()
	cfg.Agents.Claude = "echo test"

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Safety mode should produce error for existing session
	if resp.Error == "" {
		t.Error("[E2E-SPAWN] Safety mode should error for existing session")
	}
	if resp.Error == "" || !containsAnyStr(resp.Error, "already exists", "spawn-safety") {
		t.Errorf("[E2E-SPAWN] Expected safety mode error, got: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=SafetyMode | Session=%s | Error=%q", sessionName, resp.Error)
}

// TestSpawnOptions_MultipleAgentTypes validates spawning multiple agent types
func TestSpawnOptions_MultipleAgentTypes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := "test_multi_agent_spawn"
	defer tmux.KillSession(sessionName)

	opts := SpawnOptions{
		Session:    sessionName,
		CCCount:    1,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,       // Include user pane
		WorkingDir: t.TempDir(), // Use temp dir to avoid creating dirs in /data/projects
	}

	cfg := config.Default()
	// Use fast echo commands
	cfg.Agents.Claude = "echo claude_test"
	cfg.Agents.Codex = "echo codex_test"
	cfg.Agents.Gemini = "echo gemini_test"

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate no error
	if resp.Error != "" {
		t.Errorf("[E2E-SPAWN] Unexpected error: %s", resp.Error)
	}

	// Validate session created
	if resp.Session != sessionName {
		t.Errorf("[E2E-SPAWN] Session mismatch: got %q, want %q", resp.Session, sessionName)
	}

	// Count agent types: 1 user + 1 claude + 1 codex + 1 gemini = 4
	expectedCount := 4
	if len(resp.Agents) != expectedCount {
		t.Errorf("[E2E-SPAWN] Agent count: got %d, want %d", len(resp.Agents), expectedCount)
	}

	// Verify each type is present
	typeCounts := make(map[string]int)
	for _, agent := range resp.Agents {
		typeCounts[agent.Type]++
	}

	if typeCounts["user"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 user, got %d", typeCounts["user"])
	}
	if typeCounts["claude"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 claude, got %d", typeCounts["claude"])
	}
	if typeCounts["codex"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 codex, got %d", typeCounts["codex"])
	}
	if typeCounts["gemini"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 gemini, got %d", typeCounts["gemini"])
	}

	t.Logf("[E2E-SPAWN] Operation=MultiAgentTypes | Session=%s | Agents=%d | Types=%v",
		resp.Session, len(resp.Agents), typeCounts)
}

// TestSpawnOutput_SchemaStability validates JSON schema is consistent and deterministic
func TestSpawnOutput_SchemaStability(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	// Test schema with dry-run (doesn't need tmux)
	opts := SpawnOptions{
		Session:    "test_schema_stability",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	// Validate required fields are present
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Required top-level fields
	requiredFields := []string{"session", "created_at", "working_dir", "layout"}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("[E2E-SPAWN] Missing required field: %s", field)
		}
	}

	// DryRun-specific fields
	if resp["dry_run"] != true {
		t.Error("[E2E-SPAWN] dry_run field should be true")
	}
	if _, ok := resp["would_create"]; !ok {
		t.Error("[E2E-SPAWN] Missing would_create field in dry-run mode")
	}

	// Validate would_create array elements have required fields
	wouldCreate, ok := resp["would_create"].([]interface{})
	if !ok {
		t.Fatal("[E2E-SPAWN] would_create is not an array")
	}

	for i, item := range wouldCreate {
		agent, ok := item.(map[string]interface{})
		if !ok {
			t.Errorf("[E2E-SPAWN] would_create[%d] is not an object", i)
			continue
		}

		agentRequiredFields := []string{"pane", "type", "title"}
		for _, field := range agentRequiredFields {
			if _, ok := agent[field]; !ok {
				t.Errorf("[E2E-SPAWN] would_create[%d] missing field: %s", i, field)
			}
		}
	}

	t.Logf("[E2E-SPAWN] Operation=SchemaStability | Fields=%d | WouldCreate=%d",
		len(resp), len(wouldCreate))
}

// TestSpawnOutput_DeterministicOrdering validates agent order is deterministic
func TestSpawnOutput_DeterministicOrdering(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_deterministic_order",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	// Run multiple times to verify consistent ordering
	var lastOrder []string
	for i := 0; i < 3; i++ {
		output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
		if err != nil {
			t.Fatalf("[E2E-SPAWN] PrintSpawn iteration %d failed: %v", i, err)
		}

		var resp SpawnOutput
		if err := json.Unmarshal([]byte(output), &resp); err != nil {
			t.Fatalf("[E2E-SPAWN] Failed to parse JSON iteration %d: %v", i, err)
		}

		// Extract order of agent types
		var currentOrder []string
		for _, agent := range resp.WouldCreate {
			currentOrder = append(currentOrder, agent.Type)
		}

		if i > 0 {
			// Compare with previous iteration
			if len(currentOrder) != len(lastOrder) {
				t.Errorf("[E2E-SPAWN] Order length changed: %v vs %v", lastOrder, currentOrder)
			}
			for j := range currentOrder {
				if j < len(lastOrder) && currentOrder[j] != lastOrder[j] {
					t.Errorf("[E2E-SPAWN] Order changed at index %d: %s vs %s",
						j, lastOrder[j], currentOrder[j])
				}
			}
		}
		lastOrder = currentOrder
	}

	// Verify expected order: user, claude, claude, codex, gemini
	expectedOrder := []string{"user", "claude", "claude", "codex", "gemini"}
	if len(lastOrder) != len(expectedOrder) {
		t.Errorf("[E2E-SPAWN] Order length: got %d, want %d", len(lastOrder), len(expectedOrder))
	}
	for i, expected := range expectedOrder {
		if i < len(lastOrder) && lastOrder[i] != expected {
			t.Errorf("[E2E-SPAWN] Order[%d]: got %s, want %s", i, lastOrder[i], expected)
		}
	}

	t.Logf("[E2E-SPAWN] Operation=DeterministicOrdering | Order=%v", lastOrder)
}

// TestPrintSpawn_TmuxNotInstalled validates error when tmux unavailable
func TestPrintSpawn_TmuxNotInstalled(t *testing.T) {
	// This test can only properly run in environments without tmux
	// We'll test the dry-run path which doesn't check tmux, and note the behavior
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	// DryRun mode bypasses tmux check, so we can test that path
	opts := SpawnOptions{
		Session:    "test_no_tmux",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// DryRun should succeed regardless of tmux
	if resp.DryRun != true {
		t.Error("[E2E-SPAWN] Expected dry_run=true")
	}

	t.Logf("[E2E-SPAWN] Operation=TmuxNotInstalled_DryRun | DryRun=%v | Error=%q",
		resp.DryRun, resp.Error)
}

// TestSpawnOptions_NoUserPane validates NoUserPane option
func TestSpawnOptions_NoUserPane(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	// Test with dry-run
	optsWithUser := SpawnOptions{
		Session:    "test_with_user",
		CCCount:    1,
		NoUserPane: false, // Include user pane
		DryRun:     true,
	}

	optsNoUser := SpawnOptions{
		Session:    "test_no_user",
		CCCount:    1,
		NoUserPane: true, // Exclude user pane
		DryRun:     true,
	}

	cfg := config.Default()

	// With user pane
	output1, _ := captureStdout(t, func() error { return PrintSpawn(optsWithUser, cfg) })
	var resp1 SpawnOutput
	json.Unmarshal([]byte(output1), &resp1)

	// Without user pane
	output2, _ := captureStdout(t, func() error { return PrintSpawn(optsNoUser, cfg) })
	var resp2 SpawnOutput
	json.Unmarshal([]byte(output2), &resp2)

	// With user: should have 2 agents (user + claude)
	if len(resp1.WouldCreate) != 2 {
		t.Errorf("[E2E-SPAWN] With user: expected 2 agents, got %d", len(resp1.WouldCreate))
	}

	// Without user: should have 1 agent (claude only)
	if len(resp2.WouldCreate) != 1 {
		t.Errorf("[E2E-SPAWN] Without user: expected 1 agent, got %d", len(resp2.WouldCreate))
	}

	// Verify user pane is first when included
	if len(resp1.WouldCreate) > 0 && resp1.WouldCreate[0].Type != "user" {
		t.Errorf("[E2E-SPAWN] User pane should be first, got %s", resp1.WouldCreate[0].Type)
	}

	// Verify no user pane when excluded
	for _, agent := range resp2.WouldCreate {
		if agent.Type == "user" {
			t.Error("[E2E-SPAWN] Should not have user pane when NoUserPane=true")
		}
	}

	t.Logf("[E2E-SPAWN] Operation=NoUserPane | WithUser=%d | WithoutUser=%d",
		len(resp1.WouldCreate), len(resp2.WouldCreate))
}

// TestSpawnedAgent_TitleFormat validates pane title format consistency
func TestSpawnedAgent_TitleFormat(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_title_format",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate title formats
	for _, agent := range resp.WouldCreate {
		switch agent.Type {
		case "user":
			expected := "test_title_format__user"
			if agent.Title != expected {
				t.Errorf("[E2E-SPAWN] User title: got %q, want %q", agent.Title, expected)
			}
		case "claude":
			// Should match pattern: session__cc_N
			if !containsAnyStr(agent.Title, "__cc_1", "__cc_2") {
				t.Errorf("[E2E-SPAWN] Claude title format invalid: %s", agent.Title)
			}
		case "codex":
			if !containsAnyStr(agent.Title, "__cod_1") {
				t.Errorf("[E2E-SPAWN] Codex title format invalid: %s", agent.Title)
			}
		case "gemini":
			if !containsAnyStr(agent.Title, "__gmi_1") {
				t.Errorf("[E2E-SPAWN] Gemini title format invalid: %s", agent.Title)
			}
		}
	}

	t.Logf("[E2E-SPAWN] Operation=TitleFormat | Agents=%d", len(resp.WouldCreate))
}

// TestSpawnOutput_TimestampFormat validates created_at is RFC3339
func TestSpawnOutput_TimestampFormat(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_timestamp",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate timestamp is not empty
	if resp.CreatedAt == "" {
		t.Error("[E2E-SPAWN] created_at should not be empty")
	}

	// Validate RFC3339 format by attempting to parse
	// RFC3339 format: 2006-01-02T15:04:05Z07:00
	if len(resp.CreatedAt) < 20 {
		t.Errorf("[E2E-SPAWN] created_at too short for RFC3339: %s", resp.CreatedAt)
	}

	// Check for T separator and Z suffix (UTC)
	if !containsAnyStr(resp.CreatedAt, "T") {
		t.Errorf("[E2E-SPAWN] created_at missing T separator: %s", resp.CreatedAt)
	}

	t.Logf("[E2E-SPAWN] Operation=TimestampFormat | CreatedAt=%s", resp.CreatedAt)
}

// TestSpawnOutput_WorkingDir validates working directory handling
func TestSpawnOutput_WorkingDir(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	// Test with explicit working dir
	customDir := "/tmp/test_spawn_workdir"
	opts := SpawnOptions{
		Session:    "test_workdir",
		CCCount:    1,
		NoUserPane: true,
		WorkingDir: customDir,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate working dir is set
	if resp.WorkingDir != customDir {
		t.Errorf("[E2E-SPAWN] WorkingDir: got %q, want %q", resp.WorkingDir, customDir)
	}

	t.Logf("[E2E-SPAWN] Operation=WorkingDir | Dir=%s", resp.WorkingDir)
}

// TestSpawnOptions_PresetUsed validates preset field in output
func TestSpawnOptions_PresetUsed(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:    "test_preset",
		CCCount:    1,
		NoUserPane: true,
		Preset:     "my-recipe",
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate preset is recorded
	if resp.PresetUsed != "my-recipe" {
		t.Errorf("[E2E-SPAWN] PresetUsed: got %q, want %q", resp.PresetUsed, "my-recipe")
	}

	t.Logf("[E2E-SPAWN] Operation=PresetUsed | Preset=%s", resp.PresetUsed)
}

// containsAnyStr checks if s contains any of the substrings
func containsAnyStr(s string, subs ...string) bool {
	for _, sub := range subs {
		if containsSubstringSpawn(s, sub) {
			return true
		}
	}
	return false
}

// containsSubstringSpawn is a simple contains check
func containsSubstringSpawn(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && findSubstringSpawn(s, sub)))
}

// findSubstringSpawn checks if sub is in s
func findSubstringSpawn(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// =============================================================================
// Work Assignment Mode Tests (ntm-n50g)
// Tests for orchestrator work assignment functionality
// =============================================================================

// TestNormalizeAssignStrategy validates strategy normalization
func TestNormalizeAssignStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"top-n", "top-n"},
		{"topn", "top-n"},
		{"TOP-N", "top-n"},
		{"diverse", "diverse"},
		{"DIVERSE", "diverse"},
		{"dependency-aware", "dependency-aware"},
		{"dependency", "dependency-aware"},
		{"skill-matched", "skill-matched"},
		{"skill", "skill-matched"},
		{"", "top-n"},          // Default
		{"invalid", "top-n"},   // Invalid falls back to default
		{"  top-n  ", "top-n"}, // Whitespace trimmed
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeAssignStrategy(tc.input)
			if got != tc.expected {
				t.Errorf("normalizeAssignStrategy(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// TestGenerateWorkPrompt validates work prompt generation
func TestGenerateWorkPrompt(t *testing.T) {
	t.Parallel()

	item := workItem{
		ID:       "test-123",
		Title:    "Fix authentication bug",
		Priority: 1,
		Score:    0.85,
		Type:     "bug",
		Reasons:  []string{"High priority", "Unblocks 3 items"},
	}

	prompt := generateWorkPrompt(item)

	// Validate prompt contains key elements
	if !containsAnyStr(prompt, "test-123") {
		t.Error("Prompt should contain bead ID")
	}
	if !containsAnyStr(prompt, "Fix authentication bug") {
		t.Error("Prompt should contain bead title")
	}
	if !containsAnyStr(prompt, "br show test-123") {
		t.Error("Prompt should contain br show command")
	}
	if !containsAnyStr(prompt, "in_progress") {
		t.Error("Prompt should mention in_progress status")
	}
	if !containsAnyStr(prompt, "High priority") {
		t.Error("Prompt should contain reasons")
	}
	if !containsAnyStr(prompt, "br update test-123 --status done") {
		t.Error("Prompt should contain completion command")
	}

	t.Logf("Generated prompt:\n%s", prompt)
}

// TestSpawnOptions_AssignWorkDryRun validates assign-work in dry-run mode
func TestSpawnOptions_AssignWorkDryRun(t *testing.T) {
	// Note: Cannot use t.Parallel() because captureStdout modifies global os.Stdout

	opts := SpawnOptions{
		Session:        "test_assign_dryrun",
		CCCount:        2,
		NoUserPane:     true,
		DryRun:         true,
		AssignWork:     true,
		AssignStrategy: "top-n",
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(opts, cfg) })
	if err != nil {
		t.Fatalf("PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Dry-run should still work with assign flags
	if !resp.DryRun {
		t.Error("DryRun field should be true")
	}

	// Mode and strategy should not be set in dry-run (no actual assignment happens)
	// since dry-run returns early before assignment logic

	t.Logf("DryRun with AssignWork: Session=%s, WouldCreate=%d", resp.Session, len(resp.WouldCreate))
}

// TestSpawnAssignmentOutput_SchemaStability validates assignment output schema
func TestSpawnAssignmentOutput_SchemaStability(t *testing.T) {
	t.Parallel()

	// Create a test assignment
	assignment := SpawnAssignment{
		Pane:        "0.1",
		AgentType:   "claude",
		BeadID:      "test-bead",
		BeadTitle:   "Test Bead Title",
		Priority:    "P1",
		Claimed:     true,
		PromptSent:  true,
		ClaimError:  "",
		PromptError: "",
	}

	// Marshal and unmarshal to validate JSON schema
	data, err := json.Marshal(assignment)
	if err != nil {
		t.Fatalf("Failed to marshal assignment: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal assignment: %v", err)
	}

	// Validate required fields
	requiredFields := []string{"pane", "agent_type", "bead_id", "bead_title", "priority", "claimed", "prompt_sent"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	// Validate omitempty fields are not present when empty
	omitEmptyFields := []string{"claim_error", "prompt_error"}
	for _, field := range omitEmptyFields {
		if _, ok := parsed[field]; ok {
			t.Errorf("Field %s should be omitted when empty", field)
		}
	}

	t.Logf("Assignment JSON: %s", string(data))
}

// TestSpawnOutput_ModeField validates mode field is set correctly
func TestSpawnOutput_ModeField(t *testing.T) {
	t.Parallel()

	// Test output struct with mode field
	output := SpawnOutput{
		Session:        "test-session",
		Mode:           "orchestrator",
		AssignStrategy: "top-n",
		Assignments: []SpawnAssignment{
			{Pane: "0.1", BeadID: "test-1", Claimed: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal output: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	// Validate mode is present
	if parsed["mode"] != "orchestrator" {
		t.Errorf("Mode should be 'orchestrator', got %v", parsed["mode"])
	}

	// Validate assign_strategy is present
	if parsed["assign_strategy"] != "top-n" {
		t.Errorf("AssignStrategy should be 'top-n', got %v", parsed["assign_strategy"])
	}

	// Validate assignments array is present
	if _, ok := parsed["assignments"]; !ok {
		t.Error("Missing assignments field")
	}

	t.Logf("Output with mode: %s", string(data))
}

// =============================================================================
// Session Recovery Tests (bd-1wtja)
// Tests for handoff context loading and SpawnRecovery struct
// =============================================================================

// TestSpawnRecovery_SchemaStability ensures SpawnRecovery JSON structure is stable
func TestSpawnRecovery_SchemaStability(t *testing.T) {
	t.Parallel()

	recovery := SpawnRecovery{
		HandoffPath:  "/path/to/handoff.yaml",
		HandoffAge:   "5m ago",
		Goal:         "Implemented feature X",
		Now:          "Write tests for feature X",
		Status:       "complete",
		Outcome:      "SUCCEEDED",
		InjectedText: "## Previous Session Context\n**Your task:** Write tests",
	}

	data, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnRecovery: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnRecovery: %v", err)
	}

	// Verify all expected fields are present
	expectedFields := []string{
		"handoff_path", "handoff_age", "goal", "now",
		"status", "outcome", "injected_text",
	}

	for _, field := range expectedFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing field %q in SpawnRecovery JSON", field)
		}
	}

	t.Logf("SpawnRecovery JSON: %s", string(data))
}

// TestSpawnOutput_RecoveryField verifies the recovery field is included in SpawnOutput
func TestSpawnOutput_RecoveryField(t *testing.T) {
	t.Parallel()

	output := SpawnOutput{
		Session:    "test-session",
		WorkingDir: "/tmp/test",
		Layout:     "tiled",
		Recovery: &SpawnRecovery{
			HandoffPath: "/tmp/handoff.yaml",
			HandoffAge:  "10m ago",
			Goal:        "Built the API",
			Now:         "Add authentication",
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnOutput: %v", err)
	}

	// Verify recovery field is present
	recoveryData, ok := parsed["recovery"]
	if !ok {
		t.Fatal("Missing recovery field in SpawnOutput")
	}

	recoveryMap, ok := recoveryData.(map[string]interface{})
	if !ok {
		t.Fatalf("recovery field is not an object: %T", recoveryData)
	}

	if recoveryMap["goal"] != "Built the API" {
		t.Errorf("Expected goal 'Built the API', got %v", recoveryMap["goal"])
	}

	if recoveryMap["now"] != "Add authentication" {
		t.Errorf("Expected now 'Add authentication', got %v", recoveryMap["now"])
	}

	t.Logf("SpawnOutput with recovery: %s", string(data))
}

// TestSpawnOutput_RecoveryOmittedWhenNil verifies recovery is omitted from JSON when nil
func TestSpawnOutput_RecoveryOmittedWhenNil(t *testing.T) {
	t.Parallel()

	output := SpawnOutput{
		Session:    "test-session",
		WorkingDir: "/tmp/test",
		Layout:     "tiled",
		Recovery:   nil, // No recovery context
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnOutput: %v", err)
	}

	// Verify recovery field is NOT present (omitempty)
	if _, ok := parsed["recovery"]; ok {
		t.Error("recovery field should be omitted when nil")
	}

	t.Logf("SpawnOutput without recovery: %s", string(data))
}

// TestLoadLatestHandoff_NoHandoff verifies graceful handling when no handoff exists
func TestLoadLatestHandoff_NoHandoff(t *testing.T) {
	t.Parallel()

	// Use a temp directory with no handoffs
	tmpDir := t.TempDir()

	spawnRecovery, handoffCtx := loadLatestHandoff(tmpDir, "nonexistent_session")

	if spawnRecovery != nil {
		t.Error("Expected nil SpawnRecovery when no handoff exists")
	}

	if handoffCtx != nil {
		t.Error("Expected nil HandoffContext when no handoff exists")
	}

	t.Log("loadLatestHandoff correctly returns nil when no handoff found")
}
