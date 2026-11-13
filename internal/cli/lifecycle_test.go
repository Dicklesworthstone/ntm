package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// Exercise the public --ssh dispatcher without supplying any signalable PID.
// Refused commands may resolve the remote session but must stop before pane or
// process observation. Pane-preserving exit also needs host-local root-process
// evidence, so it must refuse before consulting an unrelated local PID.
func TestRemoteProcessOperationsCLIRefuseBeforePaneObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording command fixture requires a POSIX shell")
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "kill-agent", args: []string{"--robot-kill-agent=remote-fixture"}},
		{name: "kill-relaunch", args: []string{"--robot-kill-agent=remote-fixture", "--relaunch"}},
		{name: "smart-soft", args: []string{"--robot-smart-restart=remote-fixture"}},
		{name: "smart-soft-dry", args: []string{"--robot-smart-restart=remote-fixture", "--dry-run"}},
		{name: "smart-fallback", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill"}},
		{name: "smart-hard-only", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill-only"}},
		{name: "smart-fallback-dry", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill", "--dry-run"}},
		{name: "smart-hard-only-dry", args: []string{"--robot-smart-restart=remote-fixture", "--hard-kill-only", "--dry-run"}},
		{name: "exit-cli", args: []string{"--robot-exit-cli=remote-fixture"}},
		{name: "exit-relaunch", args: []string{"--robot-exit-cli=remote-fixture", "--relaunch"}},
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
  *list-panes*|*send-keys*) : > "$root/forbidden"; exit 77 ;;
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
				"NTM_REMOTE_PROCESS_CONTROL_ROOT="+root,
				"NTM_NO_COLOR=1", "NTM_CONFIG=", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=",
				"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			if ctx.Err() != nil || runErr == nil {
				t.Fatalf("remote lifecycle command was not refused: err=%v context=%v stdout=%s stderr=%s", runErr, ctx.Err(), stdout.String(), stderr.String())
			}
			var response robot.RobotResponse
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			if response.Success || response.ErrorCode != robot.ErrCodeInvalidFlag || !strings.Contains(response.Error, "without --ssh") {
				t.Fatalf("remote process control lacked actionable typed refusal: %+v", response)
			}
			if _, err := os.Stat(filepath.Join(root, "forbidden")); !errors.Is(err, os.ErrNotExist) {
				calls, _ := os.ReadFile(filepath.Join(root, "calls"))
				t.Fatalf("remote process control crossed a forbidden observation/mutation boundary: %v; calls=%s", err, calls)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "list-panes") || strings.Contains(string(calls), "send-keys") {
				t.Fatalf("remote pane observation or input crossed admission: %s", calls)
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
	cfg.Agents.Claude = testAgentClaudeCommandTemplate
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

	cfg.Agents.Claude = testAgentClaudeCommandTemplate
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
	cfg.Agents.Claude = testAgentClaudeCommandTemplate

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
	cfg.Agents.Claude = testAgentClaudeCommandTemplate

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

func TestForcedRotationEscalatesOnlyWithVerifiedOriginalShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("forced rotation fixture requires POSIX shells")
	}
	const script = `#!/bin/sh
set -eu
root="$NTM_FORCED_ROTATION_TEST_ROOT"
mode="$NTM_FORCED_ROTATION_TEST_MODE"
case "$1" in
  has-session)
    if [ -f "$root/exit-attempted" ]; then
      : > "$root/kill-fallback"
      case "$mode" in
        replacement-at-fallback|provider-at-fallback|kill-unverified|kill-replaced|kill-exited-shell-lost) ;;
        *) printf 'stop fixture before process-tree inspection\n' >&2; exit 71 ;;
      esac
    fi
    ;;
  list-panes)
    pid="$NTM_FORCED_ROTATION_SHELL_PID"
    agent=cc
    command=claude
    if [ -f "$root/kill-preflighted" ]; then
      case "$mode" in
        kill-unverified) printf 'temporary kill verification failure\n' >&2; exit 79 ;;
        kill-replaced) pid="$NTM_FORCED_ROTATION_OTHER_PID" ;;
        kill-exited-shell-lost)
          if ! kill -0 "$NTM_FORCED_ROTATION_AGENT_PID" 2>/dev/null; then exit 0; fi
          ;;
      esac
    fi
    if [ -f "$root/exit-attempted" ]; then
      case "$mode" in
        gone) exit 0 ;;
        unverified) printf 'temporary observation failure\n' >&2; exit 72 ;;
        replaced) pid="$NTM_FORCED_ROTATION_OTHER_PID" ;;
        replacement-at-fallback)
          if [ -f "$root/kill-fallback" ]; then pid="$NTM_FORCED_ROTATION_OTHER_PID"; fi
          ;;
        provider-at-fallback)
          if [ -f "$root/kill-fallback" ]; then agent=cod; fi
          ;;
      esac
    fi
    printf '%%7_NTM_SEP_1_NTM_SEP_session__cc_1_NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$command" "$pid" "$agent"
    ;;
  show-options)
    if [ -f "$root/kill-fallback" ]; then
      case "$mode" in
        kill-unverified|kill-replaced|kill-exited-shell-lost) : > "$root/kill-preflighted" ;;
        *)
          : > "$root/forbidden-fallback"
          printf 'replacement crossed lifecycle admission\n' >&2
          exit 78
          ;;
      esac
    fi
    [ "$5" = '%7' ] && [ "$6" = '@ntm_agent_launch' ] || exit 73
    cat "$root/original"
    ;;
  display-message)
    [ "$4" = '%7' ] || exit 74
    case "$5" in
      '[#{pane_current_path}]') printf '[%s]\n' "$root" ;;
      '[#{@ntm_agent_type}]') printf '[cc]\n' ;;
      *) exit 75 ;;
    esac
    ;;
  if-shell)
    [ "$2" = '-F' ] && [ "$4" = '%7' ] || exit 76
    eval "set -- $6"
    [ "$1" = send-keys ] && [ "$3" = '%7' ] && [ "$4" = C-c ] || exit 80
    : > "$root/exit-attempted"
    printf 'C-c\n' >> "$root/keys"
    case "$mode" in
      gone|unverified|replaced|failed-send)
        # Failed delivery cannot authorize an automatic process-kill retry,
        # even when the original child remains positively observable.
        printf 'ntm_pane_identity_changed\n'
        ;;
      *)
        # A successfully delivered pair may leave a wedged agent active.
        # Exercise both complete grace periods before admitting escalation.
        ;;
    esac
    ;;
  *) printf 'unexpected fixture command %s\n' "$1" >&2; exit 77 ;;
esac
`
	for _, mode := range []string{"gone", "unverified", "replaced", "failed-send", "still-running", "replacement-at-fallback", "provider-at-fallback", "kill-unverified", "kill-replaced", "kill-exited-shell-lost"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			tmuxPath := filepath.Join(root, "tmux")
			if err := os.WriteFile(tmuxPath, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForcedRotationLifecycleHelper$")
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
				"NTM_CONFIG="+filepath.Join(root, "config.toml"), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+tmuxPath,
				"NTM_FORCED_ROTATION_TEST_ROOT="+root, "NTM_FORCED_ROTATION_TEST_MODE="+mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("forced rotation lifecycle boundary: %v\n%s", err, output)
			}
		})
	}
}

func TestForcedRotationLifecycleHelper(t *testing.T) {
	root := os.Getenv("NTM_FORCED_ROTATION_TEST_ROOT")
	if root == "" {
		return
	}
	startShell := func(active bool) (int, int) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-i")
		process.DetachControllingTerminal(cmd)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			t.Fatal(err)
		}
		var childPID int
		t.Cleanup(func() {
			if childPID > 0 {
				if child, err := os.FindProcess(childPID); err == nil {
					_ = child.Kill()
				}
			}
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		if active {
			if _, err := fmt.Fprintln(stdin, "sleep 60"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				children := process.GetChildPIDs(cmd.Process.Pid, 16)
				if len(children) == 1 && process.IsAlive(children[0]) {
					childPID = children[0]
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if childPID == 0 {
				t.Fatal("owned shell did not start the active fixture child")
			}
		}
		return cmd.Process.Pid, childPID
	}
	shellPID, agentPID := startShell(true)
	otherPID, _ := startShell(false)
	t.Setenv("NTM_FORCED_ROTATION_SHELL_PID", strconv.Itoa(shellPID))
	t.Setenv("NTM_FORCED_ROTATION_AGENT_PID", strconv.Itoa(agentPID))
	t.Setenv("NTM_FORCED_ROTATION_OTHER_PID", strconv.Itoa(otherPID))
	// Keep real lifetimes and parentage, modeling only terminal groups that a
	// pipe-backed interactive shell lacks. Native robot tests cover real TTYs.
	realPS, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	const psScript = `#!/bin/sh
set -eu
if [ "$*" != '-ww -axo pid=,ppid=,pgid=,tpgid=,stat=,lstart=,ucomm=' ]; then
  exec "$NTM_FORCED_ROTATION_REAL_PS" "$@"
fi
"$NTM_FORCED_ROTATION_REAL_PS" "$@" | awk -v root="$NTM_FORCED_ROTATION_SHELL_PID" '
  { rows[NR] = $0; if ($2 == root && $5 !~ /^[ZX]/) child = $1 }
  END {
    for (i = 1; i <= NR; i++) {
      n = split(rows[i], fields)
      if (fields[1] == root) { fields[3] = root; fields[4] = child ? child : root }
      if (fields[2] == root) { fields[3] = fields[1]; fields[4] = fields[1] }
      for (j = 1; j <= n; j++) printf "%s%s", fields[j], j == n ? "\n" : " "
    }
  }'
`
	if err := os.WriteFile(filepath.Join(root, "ps"), []byte(psScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_FORCED_ROTATION_REAL_PS", realPS)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[agents]\nclaude = \"cat\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(struct {
		PaneID string               `json:"pane_id"`
		Spec   tmux.AgentLaunchSpec `json:"spec"`
	}{
		PaneID: "%7",
		Spec: tmux.AgentLaunchSpec{
			Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "cat",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "original"), []byte(base64.StdEncoding.EncodeToString(record)), 0600); err != nil {
		t.Fatal(err)
	}
	err = executeForcedRotation(t.Context(), "session", 1, "%7", "")
	if err == nil {
		t.Fatal("fixture must finish with a refusal, not claim a rotation completed")
	}
	if _, statErr := os.Stat(filepath.Join(root, "exit-attempted")); statErr != nil {
		t.Fatalf("fixture did not reach graceful exit: result=%v observation=%v", err, statErr)
	}
	_, fallbackErr := os.Stat(filepath.Join(root, "kill-fallback"))
	mode := os.Getenv("NTM_FORCED_ROTATION_TEST_MODE")
	if !process.IsAlive(shellPID) || !process.IsAlive(otherPID) {
		t.Fatal("forced rotation terminated an owned pane root")
	}
	wantAgentAlive := mode != "kill-exited-shell-lost"
	if process.IsAlive(agentPID) != wantAgentAlive {
		t.Fatalf("forced rotation acted on an unverified child or missed a confirmed exit: mode=%s agentAlive=%t result=%v", mode, process.IsAlive(agentPID), err)
	}
	keys, readErr := os.ReadFile(filepath.Join(root, "keys"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantKeys := "C-c\nC-c\nC-c\nC-c\n"
	if mode == "gone" || mode == "unverified" || mode == "replaced" || mode == "failed-send" {
		wantKeys = "C-c\n"
	}
	if string(keys) != wantKeys {
		t.Fatalf("forced rotation skipped completed grace periods or replayed failed input: keys=%q want=%q result=%v", keys, wantKeys, err)
	}
	if mode == "replacement-at-fallback" || mode == "provider-at-fallback" {
		_, forbiddenErr := os.Stat(filepath.Join(root, "forbidden-fallback"))
		if fallbackErr != nil || !errors.Is(forbiddenErr, os.ErrNotExist) || !strings.Contains(err.Error(), "identity changed") {
			t.Fatalf("kill fallback rebound to a replacement: result=%v fallback=%v forbidden=%v", err, fallbackErr, forbiddenErr)
		}
		return
	}
	if strings.HasPrefix(mode, "kill-") {
		_, preflightErr := os.Stat(filepath.Join(root, "kill-preflighted"))
		if fallbackErr != nil || preflightErr != nil || !strings.Contains(err.Error(), "could not verify the original shell") ||
			strings.Contains(err.Error(), "survived SIGKILL") || strings.Contains(err.Error(), "--robot-restart-pane") {
			t.Fatalf("kill observation failure was misreported as a surviving process: result=%v fallback=%v preflight=%v", err, fallbackErr, preflightErr)
		}
		return
	}
	if mode == "still-running" {
		// The verified old shell is still hosting an agent. Keep the existing
		// recovery ladder; the fixture stops that fallback before any signal.
		if fallbackErr != nil {
			t.Fatalf("verified still-running agent lost hard-kill fallback: result=%v marker=%v", err, fallbackErr)
		}
		return
	}
	if !errors.Is(fallbackErr, os.ErrNotExist) || !strings.Contains(err.Error(), "refusing to escalate") {
		t.Fatalf("uncertain or replaced shell triggered kill fallback: result=%v marker=%v", err, fallbackErr)
	}
}
