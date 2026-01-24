package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestAgentLifecycleSpawnWorkKill tests the complete agent lifecycle:
// spawn -> send work -> verify output -> kill -> verify cleanup
// This is the comprehensive integration test for bead ntm-j8eo.
func TestAgentLifecycleSpawnWorkKill(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-lifecycle-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true

	// Use 'cat' as agent command - it echoes input back to stdout
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
	cfg.Agents.Gemini = "cat"

	sessionName := fmt.Sprintf("ntm-lifecycle-%d", time.Now().UnixNano())

	// Track cleanup in case of test failure
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Create project dir
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// ============================================================
	// PHASE 1: SPAWN
	// ============================================================
	t.Log("Phase 1: Spawning agent session...")

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}

	spawnOpts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}

	err = spawnSessionLogic(spawnOpts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Verify session exists
	if !tmux.SessionExists(sessionName) {
		t.Fatalf("session %s was not created", sessionName)
	}

	// Let tmux settle
	time.Sleep(300 * time.Millisecond)

	// Get panes and verify structure
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	if len(panes) != 2 {
		t.Fatalf("expected 2 panes (user + claude), got %d", len(panes))
	}

	// Find the agent pane
	var agentPane *tmux.Pane
	for i := range panes {
		if panes[i].Type == tmux.AgentClaude {
			agentPane = &panes[i]
			break
		}
	}
	if agentPane == nil {
		t.Fatal("Agent pane not found after spawn")
	}

	t.Logf("Spawn successful: session=%s, panes=%d, agent_pane=%s", sessionName, len(panes), agentPane.ID)

	// ============================================================
	// PHASE 2: SEND WORK
	// ============================================================
	t.Log("Phase 2: Sending work to agent...")

	testPrompt := "Hello from lifecycle test " + fmt.Sprintf("%d", time.Now().UnixNano())

	sendOpts := SendOptions{
		Session:   sessionName,
		Prompt:    testPrompt,
		TargetAll: true,
		SkipFirst: false,
		PaneIndex: -1,
	}

	err = runSendWithTargets(sendOpts)
	if err != nil {
		t.Fatalf("runSendWithTargets failed: %v", err)
	}

	// Let keys be processed
	time.Sleep(500 * time.Millisecond)

	// ============================================================
	// PHASE 3: VERIFY OUTPUT
	// ============================================================
	t.Log("Phase 3: Verifying output in agent pane...")

	output, err := tmux.CapturePaneOutput(agentPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, testPrompt) {
		t.Errorf("Pane output did not contain prompt %q.\nGot:\n%s", testPrompt, output)
	} else {
		t.Logf("Output verification successful: found prompt in pane output")
	}

	// ============================================================
	// PHASE 4: KILL SESSION
	// ============================================================
	t.Log("Phase 4: Killing session...")

	// Use direct tmux.KillSession to avoid interactive prompts
	err = tmux.KillSession(sessionName)
	if err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	// Let cleanup happen
	time.Sleep(200 * time.Millisecond)

	// ============================================================
	// PHASE 5: VERIFY CLEANUP
	// ============================================================
	t.Log("Phase 5: Verifying cleanup...")

	if tmux.SessionExists(sessionName) {
		t.Error("Session still exists after kill")
	} else {
		t.Log("Session cleanup verified: session no longer exists")
	}

	// Verify panes are gone
	_, err = tmux.GetPanes(sessionName)
	if err == nil {
		t.Error("GetPanes should fail for killed session")
	}

	t.Log("Lifecycle test completed successfully!")
}

// TestAgentLifecycleMultipleAgents tests spawning multiple agent types
func TestAgentLifecycleMultipleAgents(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-lifecycle-multi")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true

	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
	cfg.Agents.Gemini = "cat"

	sessionName := fmt.Sprintf("ntm-lifecycle-multi-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn 3 different agent types
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "claude-model"},
		{Type: AgentTypeCodex, Index: 1, Model: "codex-model"},
		{Type: AgentTypeGemini, Index: 1, Model: "gemini-model"},
	}

	spawnOpts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		CodCount: 1,
		GmiCount: 1,
		UserPane: true,
	}

	err = spawnSessionLogic(spawnOpts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	// Expected: 1 user + 3 agents = 4 panes
	if len(panes) != 4 {
		t.Errorf("expected 4 panes (user + 3 agents), got %d", len(panes))
	}

	// Verify each agent type exists
	agentTypes := make(map[tmux.AgentType]bool)
	for _, p := range panes {
		if p.Type != tmux.AgentUser {
			agentTypes[p.Type] = true
		}
	}

	if !agentTypes[tmux.AgentClaude] {
		t.Error("missing Claude agent pane")
	}
	if !agentTypes[tmux.AgentCodex] {
		t.Error("missing Codex agent pane")
	}
	if !agentTypes[tmux.AgentGemini] {
		t.Error("missing Gemini agent pane")
	}

	// Send to all agents
	testPrompt := "Hello all agents"
	err = runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    testPrompt,
		TargetAll: true,
		SkipFirst: false,
		PaneIndex: -1,
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Verify prompt appears in at least one agent pane
	foundPrompt := false
	for _, p := range panes {
		if p.Type != tmux.AgentUser {
			out, err := tmux.CapturePaneOutput(p.ID, 10)
			if err == nil && strings.Contains(out, testPrompt) {
				foundPrompt = true
				break
			}
		}
	}

	if !foundPrompt {
		t.Error("prompt not found in any agent pane")
	}

	// Kill and verify cleanup
	err = tmux.KillSession(sessionName)
	if err != nil {
		t.Fatalf("KillSession failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if tmux.SessionExists(sessionName) {
		t.Error("session still exists after kill")
	}
}

// TestAgentLifecycleRapidSpawnKill tests rapid spawn/kill cycles for stability
func TestAgentLifecycleRapidSpawnKill(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-lifecycle-rapid")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true
	cfg.Agents.Claude = "cat"

	const iterations = 3
	for i := 0; i < iterations; i++ {
		sessionName := fmt.Sprintf("ntm-rapid-%d-%d", i, time.Now().UnixNano())
		projectDir := filepath.Join(tmpDir, sessionName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("iteration %d: failed to create project dir: %v", i, err)
		}

		// Spawn
		err := spawnSessionLogic(SpawnOptions{
			Session:  sessionName,
			Agents:   []FlatAgent{{Type: AgentTypeClaude, Index: 1, Model: "test"}},
			CCCount:  1,
			UserPane: true,
		})
		if err != nil {
			t.Fatalf("iteration %d: spawn failed: %v", i, err)
		}

		time.Sleep(200 * time.Millisecond)

		if !tmux.SessionExists(sessionName) {
			t.Fatalf("iteration %d: session not created", i)
		}

		// Kill
		err = tmux.KillSession(sessionName)
		if err != nil {
			t.Fatalf("iteration %d: kill failed: %v", i, err)
		}

		time.Sleep(200 * time.Millisecond)

		if tmux.SessionExists(sessionName) {
			t.Fatalf("iteration %d: session still exists after kill", i)
		}
	}

	t.Logf("Rapid spawn/kill completed %d iterations successfully", iterations)
}

// TestAgentLifecycleSendBeforeSpawn verifies error handling for send without spawn
func TestAgentLifecycleSendBeforeSpawn(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	nonexistentSession := fmt.Sprintf("ntm-nonexistent-%d", time.Now().UnixNano())

	err := runSendWithTargets(SendOptions{
		Session:   nonexistentSession,
		Prompt:    "This should fail",
		TargetAll: true,
	})

	if err == nil {
		t.Error("expected error when sending to non-existent session")
	}
}

// TestAgentLifecycleKillIdempotent verifies that killing an already-killed session is handled
func TestAgentLifecycleKillIdempotent(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-lifecycle-idempotent")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	jsonOutput = true
	cfg.Agents.Claude = "cat"

	sessionName := fmt.Sprintf("ntm-idempotent-%d", time.Now().UnixNano())
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn
	err = spawnSessionLogic(SpawnOptions{
		Session:  sessionName,
		Agents:   []FlatAgent{{Type: AgentTypeClaude, Index: 1, Model: "test"}},
		CCCount:  1,
		UserPane: true,
	})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Kill first time
	err = tmux.KillSession(sessionName)
	if err != nil {
		t.Fatalf("first kill failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Kill second time - should fail gracefully or return error
	err = tmux.KillSession(sessionName)
	// The second kill may or may not error depending on implementation,
	// but it should not panic
	if err != nil {
		t.Logf("Second kill returned error (expected): %v", err)
	}

	// Session should definitely not exist
	if tmux.SessionExists(sessionName) {
		t.Error("session exists after double kill")
	}
}
