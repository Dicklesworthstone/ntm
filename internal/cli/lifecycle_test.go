package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// Exercise the public --ssh dispatcher without supplying any signalable PID.
// Refused commands may resolve the remote session but must stop before pane or
// process observation; tmux-native graceful exit remains available.
func TestRemoteProcessOperationsCLIRefuseBeforePaneObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording command fixture requires a POSIX shell")
	}
	for _, tc := range []struct {
		name        string
		args        []string
		wantSuccess bool
	}{
		{name: "kill-agent", args: []string{"--robot-kill-agent=remote-fixture"}},
		{name: "kill-relaunch", args: []string{"--robot-kill-agent=remote-fixture", "--relaunch"}},
		{name: "smart-soft", args: []string{"--robot-smart-restart=remote-fixture"}},
		{name: "smart-soft-dry", args: []string{"--robot-smart-restart=remote-fixture", "--dry-run"}},
		{name: "smart-fallback", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill"}},
		{name: "smart-hard-only", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill-only"}},
		{name: "smart-fallback-dry", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill", "--dry-run"}},
		{name: "smart-hard-only-dry", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill-only", "--dry-run"}},
		{name: "exit-cli", args: []string{"--robot-exit-cli=remote-fixture"}, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const localScript = `#!/bin/sh
set -eu
root="$NTM_REMOTE_PROCESS_CONTROL_ROOT"
printf 'local %s\n' "$*" >> "$root/calls"
if [ "$1" = -V ]; then printf 'tmux 3.4\n'; exit 0; fi
: > "$root/forbidden"
exit 79
`
			const sshScript = `#!/bin/sh
set -eu
root="$NTM_REMOTE_PROCESS_CONTROL_ROOT"
printf 'ssh %s\n' "$3" >> "$root/calls"
case "$3" in
  *list-sessions*) printf 'remote-fixture_NTM_SEP_1_NTM_SEP_0_NTM_SEP_today\n' ;;
  *has-session*) ;;
  *list-panes*)
    if [ "$NTM_REMOTE_PROCESS_CONTROL_MODE" != exit-cli ]; then
      : > "$root/forbidden"
      exit 77
    fi
    command=codex
    if [ -f "$root/exit-keys" ] && [ "$(cat "$root/exit-keys")" -ge 2 ]; then command=zsh; fi
    printf '%%7_NTM_SEP_0_NTM_SEP_remote-fixture__cod_1_NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_0_NTM_SEP_0_NTM_SEP_0_NTM_SEP_codex\n' "$command"
    ;;
  *send-keys*)
    if [ "$NTM_REMOTE_PROCESS_CONTROL_MODE" != exit-cli ]; then
      : > "$root/forbidden"
      exit 76
    fi
    case "$3" in *C-c*) ;; *) : > "$root/forbidden"; exit 75 ;; esac
    count=0
    if [ -f "$root/exit-keys" ]; then count=$(cat "$root/exit-keys"); fi
    printf '%s' "$((count + 1))" > "$root/exit-keys"
    ;;
  *) : > "$root/forbidden"; exit 78 ;;
esac
`
			for name, script := range map[string]string{"tmux": localScript, "ssh": sshScript} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			configPath := filepath.Join(root, "config.toml")
			if err := os.WriteFile(configPath, []byte("[agent_mail]\nenabled = false\n[cleanup]\nauto_clean_on_startup = false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--config=" + configPath, "--ssh=operator@remote-fixture", "--panes=%7"}, tc.args...)
			rawArgs, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = root
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
				"PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "TMUX=",
				"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+filepath.Join(root, "tmux"),
				"NTM_REMOTE_PROCESS_CONTROL_ROOT="+root, "NTM_REMOTE_PROCESS_CONTROL_MODE="+tc.name,
				"NTM_NO_COLOR=1", "NTM_CONFIG=", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=",
				"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			if ctx.Err() != nil || (runErr == nil) != tc.wantSuccess {
				t.Fatalf("success=%t want=%t err=%v context=%v stdout=%s stderr=%s", runErr == nil, tc.wantSuccess, runErr, ctx.Err(), stdout.String(), stderr.String())
			}
			var response robot.RobotResponse
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			if response.Success != tc.wantSuccess {
				t.Fatalf("remote lifecycle response=%+v", response)
			}
			if !tc.wantSuccess && (response.ErrorCode != robot.ErrCodeInvalidFlag || !strings.Contains(response.Error, "without --ssh")) {
				t.Fatalf("remote process control lacked actionable typed refusal: %+v", response)
			}
			if _, err := os.Stat(filepath.Join(root, "forbidden")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("remote process control crossed a forbidden observation/mutation boundary: %v", err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "list-panes") != tc.wantSuccess {
				t.Fatalf("pane observation crossed admission or remote exit was blocked: %s", calls)
			}
			if tc.wantSuccess {
				keys, err := os.ReadFile(filepath.Join(root, "exit-keys"))
				if err != nil || string(keys) != "2" {
					t.Fatalf("remote exit did not use the SSH keystroke path: keys=%q err=%v", keys, err)
				}
			}
		})
	}
}

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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	// Use 'cat' as agent command - it echoes input back to stdout
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate

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

	err = spawnSessionLogicContext(t.Context(), spawnOpts)
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true

	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate

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

	err = spawnSessionLogicContext(t.Context(), spawnOpts)
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true
	cfg.Agents.Claude = testAgentCatCommandTemplate

	const iterations = 3
	for i := 0; i < iterations; i++ {
		sessionName := fmt.Sprintf("ntm-rapid-%d-%d", i, time.Now().UnixNano())
		projectDir := filepath.Join(tmpDir, sessionName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("iteration %d: failed to create project dir: %v", i, err)
		}

		// Spawn
		err := spawnSessionLogicContext(t.Context(), SpawnOptions{
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

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-idempotent-%d", time.Now().UnixNano())
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn
	err = spawnSessionLogicContext(t.Context(), SpawnOptions{
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
