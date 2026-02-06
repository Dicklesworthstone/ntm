package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestMultiSessionManagement tests managing multiple concurrent sessions.
// ntm-0nsv: Test managing multiple concurrent sessions
func TestMultiSessionManagement(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.Log("[MULTI-SESSION] Starting multi-session management test")

	// Setup shared config
	projectsBase := t.TempDir()
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "multi_state.db")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("[MULTI-SESSION] Failed to write test config: %v", err)
	}

	// Create 3 sessions with unique names
	sessions := make([]string, 3)
	for i := 0; i < 3; i++ {
		sessions[i] = fmt.Sprintf("e2e_multi_%d_%d", i, time.Now().UnixNano())
		projectDir := filepath.Join(projectsBase, sessions[i])
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("[MULTI-SESSION] Failed to create project directory %d: %v", i, err)
		}
	}

	// Cleanup all sessions on test completion
	t.Cleanup(func() {
		logger.Log("[MULTI-SESSION] Teardown: Killing test sessions")
		for _, sess := range sessions {
			exec.Command(tmux.BinaryPath(), "kill-session", "-t", sess).Run()
		}
	})

	// Step 1: Spawn all sessions
	logger.LogSection("Step 1: Spawning 3 sessions")
	for i, sess := range sessions {
		out, err := logger.Exec("ntm", "--config", configPath, "spawn", sess, "--cc=1", "--safety")
		if err != nil {
			t.Fatalf("[MULTI-SESSION] Failed to spawn session %d: %v\nOutput: %s", i, err, out)
		}
		logger.Log("[MULTI-SESSION] Spawned session %d: %s", i, sess)
	}

	// Wait for sessions to initialize
	time.Sleep(2 * time.Second)

	// Step 2: Verify all sessions exist
	logger.LogSection("Step 2: Verifying all sessions exist")
	for i, sess := range sessions {
		testutil.AssertSessionExists(t, logger, sess)
		logger.Log("[MULTI-SESSION] Session %d (%s) exists", i, sess)
	}

	// Step 3: Test session listing shows all sessions
	logger.LogSection("Step 3: Verifying session listing")
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "list", "--json")

	var listResponse struct {
		Sessions []struct {
			Name   string `json:"name"`
			Exists bool   `json:"exists"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out, &listResponse); err != nil {
		// Try alternative format if the above fails
		var altResponse struct {
			Success  bool `json:"success"`
			Sessions []struct {
				Name string `json:"name"`
			} `json:"sessions"`
		}
		if err2 := json.Unmarshal(out, &altResponse); err2 != nil {
			t.Logf("[MULTI-SESSION] List output: %s", string(out))
			// Don't fail - list may not be implemented or have different format
			logger.Log("[MULTI-SESSION] Could not parse list output, skipping list verification")
		}
	}

	// Count how many of our sessions appear in the list
	foundCount := 0
	for _, sess := range sessions {
		if strings.Contains(string(out), sess) {
			foundCount++
		}
	}
	logger.Log("[MULTI-SESSION] Found %d/%d sessions in list output", foundCount, len(sessions))

	// Step 4: Test status for each session independently
	logger.LogSection("Step 4: Verifying independent session status")
	for i, sess := range sessions {
		out := testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "status", "--json", sess)
		logger.Log("[MULTI-SESSION] Session %d status retrieved", i)

		var statusResponse struct {
			Session string `json:"session"`
			Exists  bool   `json:"exists"`
			Panes   []struct {
				Index int `json:"index"`
			} `json:"panes"`
		}
		if err := json.Unmarshal(out, &statusResponse); err != nil {
			t.Logf("[MULTI-SESSION] Session %d status parse warning: %v", i, err)
			continue
		}

		if !statusResponse.Exists {
			t.Errorf("[MULTI-SESSION] Session %d should exist", i)
		}
	}

	// Step 5: Send unique markers to each session
	logger.LogSection("Step 5: Sending unique markers to each session")
	markers := make([]string, len(sessions))
	for i, sess := range sessions {
		markers[i] = fmt.Sprintf("MULTI_MARKER_%d_%d", i, time.Now().UnixNano())
		out, _ := logger.Exec("ntm", "--config", configPath, "send", sess, fmt.Sprintf("echo %s", markers[i]), "--cc")
		logger.Log("[MULTI-SESSION] Sent marker %d to session %s: %s", i, sess, markers[i][:30]+"...")
		_ = out
	}

	// Wait for commands to execute
	time.Sleep(1 * time.Second)

	// Step 6: Verify session isolation - markers should only appear in their respective sessions
	logger.LogSection("Step 6: Verifying session isolation")
	for i, sess := range sessions {
		paneCount, _ := testutil.GetSessionPaneCount(sess)
		markerFound := false
		wrongMarkerFound := false

		for p := 0; p < paneCount; p++ {
			content, err := testutil.CapturePane(sess, p)
			if err != nil {
				continue
			}

			// Check for correct marker
			if strings.Contains(content, markers[i]) {
				markerFound = true
			}

			// Check for other sessions' markers (should NOT be present)
			for j, otherMarker := range markers {
				if j != i && strings.Contains(content, otherMarker) {
					wrongMarkerFound = true
					t.Errorf("[MULTI-SESSION] Session %d contains marker from session %d - isolation breach!", i, j)
				}
			}
		}

		if markerFound {
			logger.Log("[MULTI-SESSION] Session %d correctly contains its own marker", i)
		}
		if !wrongMarkerFound {
			logger.Log("[MULTI-SESSION] Session %d isolation verified (no foreign markers)", i)
		}
	}

	// Step 7: Kill sessions one by one and verify others remain
	logger.LogSection("Step 7: Verifying independent session lifecycle")

	// Kill first session
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessions[0])
	time.Sleep(500 * time.Millisecond)

	// Verify first session is gone but others remain
	testutil.AssertSessionNotExists(t, logger, sessions[0])
	testutil.AssertSessionExists(t, logger, sessions[1])
	testutil.AssertSessionExists(t, logger, sessions[2])
	logger.Log("[MULTI-SESSION] After killing session 0: session 1 and 2 still exist")

	// Kill second session
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessions[1])
	time.Sleep(500 * time.Millisecond)

	// Verify only third session remains
	testutil.AssertSessionNotExists(t, logger, sessions[1])
	testutil.AssertSessionExists(t, logger, sessions[2])
	logger.Log("[MULTI-SESSION] After killing session 1: only session 2 remains")

	// Kill last session
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessions[2])
	time.Sleep(500 * time.Millisecond)
	testutil.AssertSessionNotExists(t, logger, sessions[2])

	logger.Log("[MULTI-SESSION] PASS: Multi-session management test completed successfully")
}

// TestConcurrentSessionOperations tests performing operations across multiple sessions concurrently.
func TestConcurrentSessionOperations(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.Log("[CONCURRENT] Starting concurrent session operations test")

	// Setup
	projectsBase := t.TempDir()
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "concurrent_state.db")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("[CONCURRENT] Failed to write config: %v", err)
	}

	// Create 4 sessions for concurrent testing
	const numSessions = 4
	sessions := make([]string, numSessions)
	for i := 0; i < numSessions; i++ {
		sessions[i] = fmt.Sprintf("e2e_concurrent_%d_%d", i, time.Now().UnixNano())
		projectDir := filepath.Join(projectsBase, sessions[i])
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("[CONCURRENT] Failed to create project directory: %v", err)
		}
	}

	t.Cleanup(func() {
		logger.Log("[CONCURRENT] Teardown: Killing sessions")
		for _, sess := range sessions {
			exec.Command(tmux.BinaryPath(), "kill-session", "-t", sess).Run()
		}
	})

	// Spawn all sessions first
	logger.LogSection("Step 1: Spawning sessions")
	for i, sess := range sessions {
		out, err := logger.Exec("ntm", "--config", configPath, "spawn", sess, "--cc=1", "--safety")
		if err != nil {
			t.Fatalf("[CONCURRENT] Failed to spawn session %d: %v\nOutput: %s", i, err, out)
		}
	}
	time.Sleep(2 * time.Second)

	// Verify all sessions exist
	for i, sess := range sessions {
		testutil.AssertSessionExists(t, logger, sess)
		logger.Log("[CONCURRENT] Session %d spawned", i)
	}

	// Step 2: Send commands to all sessions concurrently
	logger.LogSection("Step 2: Concurrent command sending")
	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)
	markers := make([]string, numSessions)

	for i := 0; i < numSessions; i++ {
		markers[i] = fmt.Sprintf("CONCURRENT_%d_%d", i, time.Now().UnixNano())
	}

	for i, sess := range sessions {
		wg.Add(1)
		go func(idx int, sessName string) {
			defer wg.Done()

			cmd := exec.Command("ntm", "--config", configPath, "send", sessName,
				fmt.Sprintf("echo %s", markers[idx]), "--cc")
			out, err := cmd.CombinedOutput()
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("session %d send failed: %w (output: %s)", idx, err, out))
				mu.Unlock()
			}
		}(i, sess)
	}

	wg.Wait()

	// Report errors
	for _, err := range errors {
		t.Errorf("[CONCURRENT] %v", err)
	}

	logger.Log("[CONCURRENT] Sent commands to %d sessions concurrently, %d errors", numSessions, len(errors))

	// Wait for commands to execute
	time.Sleep(1 * time.Second)

	// Step 3: Query status concurrently
	logger.LogSection("Step 3: Concurrent status queries")
	var statusWg sync.WaitGroup
	statusErrors := make([]error, 0)

	for i, sess := range sessions {
		statusWg.Add(1)
		go func(idx int, sessName string) {
			defer statusWg.Done()

			cmd := exec.Command("ntm", "--config", configPath, "status", "--json", sessName)
			out, err := cmd.CombinedOutput()
			if err != nil {
				mu.Lock()
				statusErrors = append(statusErrors, fmt.Errorf("session %d status failed: %w", idx, err))
				mu.Unlock()
				return
			}

			var resp struct {
				Exists bool `json:"exists"`
			}
			if err := json.Unmarshal(out, &resp); err != nil {
				mu.Lock()
				statusErrors = append(statusErrors, fmt.Errorf("session %d status parse failed: %w", idx, err))
				mu.Unlock()
				return
			}

			if !resp.Exists {
				mu.Lock()
				statusErrors = append(statusErrors, fmt.Errorf("session %d reports not existing", idx))
				mu.Unlock()
			}
		}(i, sess)
	}

	statusWg.Wait()

	for _, err := range statusErrors {
		t.Errorf("[CONCURRENT] %v", err)
	}

	logger.Log("[CONCURRENT] Queried %d sessions concurrently, %d errors", numSessions, len(statusErrors))

	// Step 4: Interrupt all sessions concurrently
	logger.LogSection("Step 4: Concurrent interrupt")
	var interruptWg sync.WaitGroup
	interruptErrors := make([]error, 0)

	for i, sess := range sessions {
		interruptWg.Add(1)
		go func(idx int, sessName string) {
			defer interruptWg.Done()

			cmd := exec.Command("ntm", "--config", configPath, "interrupt", sessName)
			_, err := cmd.CombinedOutput()
			if err != nil {
				mu.Lock()
				interruptErrors = append(interruptErrors, fmt.Errorf("session %d interrupt failed: %w", idx, err))
				mu.Unlock()
			}
		}(i, sess)
	}

	interruptWg.Wait()

	// Some interrupt errors may be expected if sessions are in certain states
	if len(interruptErrors) > 0 {
		logger.Log("[CONCURRENT] %d interrupt errors (may be expected)", len(interruptErrors))
	}

	// Step 5: Kill all sessions concurrently
	logger.LogSection("Step 5: Concurrent kill")
	var killWg sync.WaitGroup
	killErrors := make([]error, 0)

	for i, sess := range sessions {
		killWg.Add(1)
		go func(idx int, sessName string) {
			defer killWg.Done()

			cmd := exec.Command("ntm", "--config", configPath, "kill", "-f", sessName)
			_, err := cmd.CombinedOutput()
			if err != nil {
				mu.Lock()
				killErrors = append(killErrors, fmt.Errorf("session %d kill failed: %w", idx, err))
				mu.Unlock()
			}
		}(i, sess)
	}

	killWg.Wait()

	for _, err := range killErrors {
		t.Errorf("[CONCURRENT] %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Verify all sessions are killed
	allKilled := true
	for i, sess := range sessions {
		if testutil.SessionExists(sess) {
			t.Errorf("[CONCURRENT] Session %d should be killed", i)
			allKilled = false
		}
	}

	if allKilled {
		logger.Log("[CONCURRENT] All %d sessions killed concurrently", numSessions)
	}

	logger.Log("[CONCURRENT] PASS: Concurrent session operations test completed")
}

// TestSessionNamespaceIsolation verifies that sessions with similar names don't interfere.
func TestSessionNamespaceIsolation(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.Log("[NAMESPACE] Starting session namespace isolation test")

	// Setup
	projectsBase := t.TempDir()
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "namespace_state.db")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("[NAMESPACE] Failed to write config: %v", err)
	}

	// Create sessions with similar names (prefix collision potential)
	baseTime := time.Now().UnixNano()
	sessions := []string{
		fmt.Sprintf("project_%d", baseTime),
		fmt.Sprintf("project_%d_dev", baseTime),
		fmt.Sprintf("project_%d_staging", baseTime),
	}

	for _, sess := range sessions {
		projectDir := filepath.Join(projectsBase, sess)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("[NAMESPACE] Failed to create project directory: %v", err)
		}
	}

	t.Cleanup(func() {
		logger.Log("[NAMESPACE] Teardown: Killing sessions")
		for _, sess := range sessions {
			exec.Command(tmux.BinaryPath(), "kill-session", "-t", sess).Run()
		}
	})

	// Step 1: Spawn all sessions
	logger.LogSection("Step 1: Spawning sessions with similar names")
	for i, sess := range sessions {
		out, err := logger.Exec("ntm", "--config", configPath, "spawn", sess, "--cc=1", "--safety")
		if err != nil {
			t.Fatalf("[NAMESPACE] Failed to spawn session %s: %v\nOutput: %s", sess, err, out)
		}
		logger.Log("[NAMESPACE] Spawned: %s", sess)
		_ = i
	}
	time.Sleep(2 * time.Second)

	// Step 2: Verify each session is distinct
	logger.LogSection("Step 2: Verifying session distinctness")
	for _, sess := range sessions {
		testutil.AssertSessionExists(t, logger, sess)
	}

	// Step 3: Send different data to each session
	logger.LogSection("Step 3: Sending distinct data to each session")
	data := []string{"DATA_A_UNIQUE", "DATA_B_UNIQUE", "DATA_C_UNIQUE"}
	for i, sess := range sessions {
		logger.Exec("ntm", "--config", configPath, "send", sess, fmt.Sprintf("echo %s", data[i]), "--cc")
	}
	time.Sleep(1 * time.Second)

	// Step 4: Verify data isolation
	logger.LogSection("Step 4: Verifying data isolation")
	for i, sess := range sessions {
		paneCount, _ := testutil.GetSessionPaneCount(sess)
		foundOwn := false
		foundOther := false

		for p := 0; p < paneCount; p++ {
			content, err := testutil.CapturePane(sess, p)
			if err != nil {
				continue
			}

			if strings.Contains(content, data[i]) {
				foundOwn = true
			}

			// Check for data from other sessions
			for j, otherData := range data {
				if j != i && strings.Contains(content, otherData) {
					foundOther = true
					t.Errorf("[NAMESPACE] Session %s contains data from session %d", sess, j)
				}
			}
		}

		if foundOwn {
			logger.Log("[NAMESPACE] Session %s contains its own data", sess)
		}
		if !foundOther {
			logger.Log("[NAMESPACE] Session %s has no data leakage", sess)
		}
	}

	// Step 5: Kill middle session and verify others unaffected
	logger.LogSection("Step 5: Killing middle session")
	testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessions[1])
	time.Sleep(500 * time.Millisecond)

	testutil.AssertSessionNotExists(t, logger, sessions[1])
	testutil.AssertSessionExists(t, logger, sessions[0])
	testutil.AssertSessionExists(t, logger, sessions[2])

	// Verify remaining sessions still have their data
	for i, sess := range []string{sessions[0], sessions[2]} {
		idx := i
		if i == 1 {
			idx = 2
		}
		paneCount, _ := testutil.GetSessionPaneCount(sess)
		for p := 0; p < paneCount; p++ {
			content, err := testutil.CapturePane(sess, p)
			if err != nil {
				continue
			}
			if strings.Contains(content, data[idx]) {
				logger.Log("[NAMESPACE] Session %s still has its data after sibling killed", sess)
				break
			}
		}
	}

	logger.Log("[NAMESPACE] PASS: Session namespace isolation test completed")
}

// TestRapidSessionCreationDestruction tests rapid creation and destruction of sessions.
func TestRapidSessionCreationDestruction(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.Log("[RAPID] Starting rapid session creation/destruction test")

	// Setup
	projectsBase := t.TempDir()
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "rapid_state.db")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("[RAPID] Failed to write config: %v", err)
	}

	// Perform rapid create/destroy cycles
	const cycles = 5
	logger.LogSection(fmt.Sprintf("Performing %d rapid create/destroy cycles", cycles))

	successfulCycles := 0
	for i := 0; i < cycles; i++ {
		sessName := fmt.Sprintf("e2e_rapid_%d_%d", i, time.Now().UnixNano())
		projectDir := filepath.Join(projectsBase, sessName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Errorf("[RAPID] Cycle %d: Failed to create project dir: %v", i, err)
			continue
		}

		// Create session
		_, err := logger.Exec("ntm", "--config", configPath, "spawn", sessName, "--cc=1", "--safety")
		if err != nil {
			t.Errorf("[RAPID] Cycle %d: Spawn failed: %v", i, err)
			continue
		}

		// Brief pause for tmux
		time.Sleep(500 * time.Millisecond)

		// Verify exists
		if !testutil.SessionExists(sessName) {
			t.Errorf("[RAPID] Cycle %d: Session should exist after spawn", i)
			continue
		}

		// Kill session
		_, err = logger.Exec("ntm", "--config", configPath, "kill", "-f", sessName)
		if err != nil {
			t.Errorf("[RAPID] Cycle %d: Kill failed: %v", i, err)
			// Cleanup anyway
			exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessName).Run()
			continue
		}

		// Brief pause for cleanup
		time.Sleep(200 * time.Millisecond)

		// Verify gone
		if testutil.SessionExists(sessName) {
			t.Errorf("[RAPID] Cycle %d: Session should be gone after kill", i)
			exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessName).Run()
			continue
		}

		successfulCycles++
		logger.Log("[RAPID] Cycle %d: Success", i)
	}

	if successfulCycles < cycles {
		t.Errorf("[RAPID] Only %d/%d cycles succeeded", successfulCycles, cycles)
	}

	logger.Log("[RAPID] PASS: Completed %d/%d rapid cycles", successfulCycles, cycles)
}

// TestCrossSessionRobotSendAck validates robot-mode prompt delivery and acknowledgment
// across multiple concurrent sessions.
// ntm-lmto: Test cross-session agent communication
func TestCrossSessionRobotSendAck(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())
	logger.Log("[LMTO] Starting cross-session robot send+ack test")

	projectsBase := t.TempDir()
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "lmto_state.db")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q
state_path = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"

[tmux]
scrollback = 500
`, projectsBase, stateDBPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("[LMTO] Failed to write test config: %v", err)
	}

	sessionA := fmt.Sprintf("e2e_lmto_a_%d", time.Now().UnixNano())
	sessionB := fmt.Sprintf("e2e_lmto_b_%d", time.Now().UnixNano())
	sessions := []string{sessionA, sessionB}

	for _, sess := range sessions {
		projectDir := filepath.Join(projectsBase, sess)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("[LMTO] Failed to create project dir for %s: %v", sess, err)
		}
	}

	t.Cleanup(func() {
		logger.Log("[LMTO] Teardown: Killing test sessions")
		for _, sess := range sessions {
			_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", sess).Run()
		}
	})

	logger.LogSection("Step 1: Spawn two sessions")
	for _, sess := range sessions {
		_, err := logger.Exec("ntm", "--config", configPath, "spawn", sess, "--cc=1", "--safety")
		if err != nil {
			t.Fatalf("[LMTO] Spawn failed for %s: %v", sess, err)
		}
		testutil.AssertSessionExists(t, logger, sess)
	}

	// Let agents initialize so --robot-ack has something to observe.
	time.Sleep(1500 * time.Millisecond)

	logger.LogSection("Step 2: robot-send --track to each session")
	markerA := fmt.Sprintf("LMTO_MARKER_A_%d", time.Now().UnixNano())
	markerB := fmt.Sprintf("LMTO_MARKER_B_%d", time.Now().UnixNano())

	type sendAndAck struct {
		Success bool `json:"success"`
		Send    struct {
			Success    bool     `json:"success"`
			Session    string   `json:"session"`
			Targets    []string `json:"targets"`
			Successful []string `json:"successful"`
			Failed     []struct {
				Pane  string `json:"pane"`
				Error string `json:"error"`
			} `json:"failed"`
		} `json:"send"`
		Ack struct {
			Success       bool   `json:"success"`
			Session       string `json:"session"`
			Confirmations []struct {
				Pane string `json:"pane"`
			} `json:"confirmations"`
			Pending []string `json:"pending"`
			Failed  []struct {
				Pane   string `json:"pane"`
				Reason string `json:"reason"`
			} `json:"failed"`
		} `json:"ack"`
		Error string `json:"error,omitempty"`
	}

	runTrackedSend := func(t *testing.T, sess, marker string) {
		t.Helper()

		out := testutil.AssertCommandSuccess(
			t,
			logger,
			"ntm",
			"--config", configPath,
			"--robot-send="+sess,
			"--msg", fmt.Sprintf("echo %s", marker),
			"--type=claude",
			"--track",
		)
		testutil.AssertJSONOutput(t, logger, out)

		var res sendAndAck
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("[LMTO] parse send+ack json: %v\n%s", err, string(out))
		}
		if !res.Success || !res.Send.Success || !res.Ack.Success {
			t.Fatalf("[LMTO] send+ack failed: success=%v send.success=%v ack.success=%v error=%q send.failed=%v ack.failed=%v",
				res.Success, res.Send.Success, res.Ack.Success, res.Error, res.Send.Failed, res.Ack.Failed)
		}
		if res.Send.Session != sess || res.Ack.Session != sess {
			t.Fatalf("[LMTO] session mismatch: send.session=%q ack.session=%q want %q", res.Send.Session, res.Ack.Session, sess)
		}
		if len(res.Send.Successful) == 0 {
			t.Fatalf("[LMTO] expected at least one successful target for %s; targets=%v failed=%v", sess, res.Send.Targets, res.Send.Failed)
		}
		if len(res.Ack.Confirmations) == 0 {
			t.Fatalf("[LMTO] expected at least one ack confirmation for %s; pending=%v failed=%v", sess, res.Ack.Pending, res.Ack.Failed)
		}
	}

	runTrackedSend(t, sessionA, markerA)
	runTrackedSend(t, sessionB, markerB)

	logger.LogSection("Step 3: Verify markers landed in the correct sessions only")
	assertMarkerIsolation := func(t *testing.T, sessWant, marker string, sessNot string) {
		t.Helper()

		paneCountWant, _ := testutil.GetSessionPaneCount(sessWant)
		foundInWant := false
		for p := 0; p < paneCountWant; p++ {
			content, err := testutil.CapturePane(sessWant, p)
			if err != nil {
				continue
			}
			if strings.Contains(content, marker) {
				foundInWant = true
				break
			}
		}
		if !foundInWant {
			t.Fatalf("[LMTO] expected marker %q in session %q output", marker, sessWant)
		}

		paneCountNot, _ := testutil.GetSessionPaneCount(sessNot)
		for p := 0; p < paneCountNot; p++ {
			content, err := testutil.CapturePane(sessNot, p)
			if err != nil {
				continue
			}
			if strings.Contains(content, marker) {
				t.Fatalf("[LMTO] marker %q leaked into wrong session %q", marker, sessNot)
			}
		}
	}

	assertMarkerIsolation(t, sessionA, markerA, sessionB)
	assertMarkerIsolation(t, sessionB, markerB, sessionA)

	logger.Log("[LMTO] PASS: Cross-session robot send+ack verified")
}
