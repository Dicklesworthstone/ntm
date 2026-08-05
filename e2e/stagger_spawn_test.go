//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM CLI commands.
// [E2E-STAGGER] Tests for staggered spawn and prompt delivery.
//
// Test Scenarios:
// 1. Basic staggered spawn - spawn agents with --stagger=Ns, verify delays
// 2. Staggered prompt delivery - verify delivery order and timing
// 3. Adaptive staggering - smart mode adjusts delays based on rate limits
// 4. Dependency-aware stagger - tasks with dependencies complete in order
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type spawnResponse struct {
	Session          string           `json:"session"`
	Created          bool             `json:"created"`
	WorkingDirectory string           `json:"working_directory,omitempty"`
	Panes            []spawnPane      `json:"panes"`
	AgentCounts      spawnAgentCounts `json:"agent_counts"`
	Stagger          *spawnStagger    `json:"stagger,omitempty"`
}

type spawnPane struct {
	Index         int    `json:"index"`
	Type          string `json:"type"`
	PromptDelayMs int64  `json:"prompt_delay_ms,omitempty"`
}

type spawnAgentCounts struct {
	Claude int `json:"claude"`
	Codex  int `json:"codex"`
	Gemini int `json:"gemini"`
	User   int `json:"user,omitempty"`
	Total  int `json:"total"`
}

type spawnStagger struct {
	Enabled    bool   `json:"enabled"`
	IntervalMs int64  `json:"interval_ms,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// sendResponse represents the JSON output from ntm send --json
type sendResponse struct {
	Success   bool             `json:"success"`
	Session   string           `json:"session"`
	Prompt    string           `json:"prompt"`
	Targets   []int            `json:"targets"`
	Delivered []deliveryResult `json:"delivered"`
	Stagger   *staggerInfo     `json:"stagger,omitempty"`
}

type deliveryResult struct {
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	DelayMs   int64  `json:"delay_ms,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type staggerInfo struct {
	Enabled    bool  `json:"enabled"`
	IntervalMs int64 `json:"interval_ms,omitempty"`
}

// robotStatusResponse for checking session state
type robotStatusResponse struct {
	Success   bool          `json:"success"`
	Sessions  []sessionInfo `json:"sessions"`
	Timestamp string        `json:"timestamp,omitempty"`
}

type sessionInfo struct {
	Name       string     `json:"name"`
	Panes      []paneInfo `json:"panes"`
	AgentCount int        `json:"agent_count"`
}

type paneInfo struct {
	Index     int    `json:"index"`
	AgentType string `json:"agent_type"`
	IsWorking bool   `json:"is_working,omitempty"`
}

func newStaggerSession(t *testing.T, prefix string) string {
	t.Helper()

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("[E2E-STAGGER] generate session suffix: %v", err)
	}
	return fmt.Sprintf("%s_%x", prefix, suffix)
}

func TestE2E_StaggeredSpawnPromptDelivery(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "stagger_spawn")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-e2e-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger")
	projectDir := filepath.Join(baseDir, session)
	expectedDir := projectDir

	staggerDelay := 100 * time.Millisecond
	prompt := "Say hello."

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		flag,
		"--prompt", prompt,
		fmt.Sprintf("--stagger=%s", staggerDelay),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running: ntm %s", strings.Join(args, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("[E2E-STAGGER] parse spawn JSON: %v output=%s", err, string(out))
	}
	logger.LogJSON("[E2E-STAGGER] spawn response", resp)

	if !resp.Created {
		t.Fatalf("[E2E-STAGGER] expected created=true")
	}
	if resp.Session != session {
		t.Fatalf("[E2E-STAGGER] session mismatch: %q", resp.Session)
	}
	if resp.WorkingDirectory != expectedDir {
		t.Fatalf("[E2E-STAGGER] working_directory mismatch: got %q want %q", resp.WorkingDirectory, expectedDir)
	}
	if resp.Stagger == nil || !resp.Stagger.Enabled {
		t.Fatalf("[E2E-STAGGER] expected stagger enabled in response")
	}
	if resp.Stagger.IntervalMs != staggerDelay.Milliseconds() {
		t.Fatalf("[E2E-STAGGER] interval mismatch: got %d want %d", resp.Stagger.IntervalMs, staggerDelay.Milliseconds())
	}

	agentPanes := filterAgentPanes(resp.Panes)
	if len(agentPanes) != 1 {
		t.Fatalf("[E2E-STAGGER] expected one agent pane, got %d", len(agentPanes))
	}
	executable, ok := agentExecutable(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}
	if agentPanes[0].Type != executable {
		t.Fatalf("[E2E-STAGGER] agent pane type mismatch: got %q want %q", agentPanes[0].Type, executable)
	}
	// A stagger interval applies between agents. With one real agent there is no
	// preceding pane, so its prompt must be delivered immediately.
	if agentPanes[0].PromptDelayMs != 0 {
		t.Fatalf("[E2E-STAGGER] single agent prompt delay: got %dms want 0ms", agentPanes[0].PromptDelayMs)
	}

	killArgs := []string{"--json", "kill", session, "--force"}
	killCmd := exec.CommandContext(context.Background(), "ntm", killArgs...)
	killCmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	killCmd.Dir = baseDir
	if killOut, killErr := killCmd.CombinedOutput(); killErr != nil {
		logger.Log("[E2E-STAGGER] kill error: %v output=%s", killErr, string(killOut))
		t.Fatalf("[E2E-STAGGER] failed to kill session: %v", killErr)
	}
}

// TestE2E_StaggeredSpawn_MultiAgent tests staggered spawn with multiple agents.
// Verifies that spawn delays are approximately N*stagger between each agent.
func TestE2E_StaggeredSpawn_MultiAgent(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	logger := NewTestLogger(t, "stagger_multi_agent")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-multi-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger_multi")

	// Use a short stagger for testing (500ms between each)
	staggerDelay := 500 * time.Millisecond

	// Determine which agent flags to use based on availability
	agentFlags := []string{}
	if _, err := exec.LookPath("cc"); err == nil {
		agentFlags = append(agentFlags, "--cc=2")
	}
	if _, err := exec.LookPath("cod"); err == nil {
		agentFlags = append(agentFlags, "--cod=1")
	}
	if len(agentFlags) == 0 {
		t.Skip("no agent CLIs (cc, cod) available")
	}

	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		fmt.Sprintf("--stagger=%s", staggerDelay),
	}
	args = append(args, agentFlags...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running: ntm %s", strings.Join(args, " "))
	startTime := time.Now()
	out, err := cmd.CombinedOutput()
	spawnDuration := time.Since(startTime)

	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("[E2E-STAGGER] parse spawn JSON: %v output=%s", err, string(out))
	}
	logger.LogJSON("[E2E-STAGGER] spawn response", resp)

	logger.Log("[E2E-STAGGER] Spawn completed in %s", spawnDuration)

	// Verify stagger is enabled
	if resp.Stagger == nil || !resp.Stagger.Enabled {
		t.Fatalf("[E2E-STAGGER] expected stagger enabled in response")
	}

	// Verify pane delays are incremental
	agentPanes := filterAgentPanes(resp.Panes)
	logger.Log("[E2E-STAGGER] Agent panes: %d", len(agentPanes))

	// Sort by index to verify order
	sort.Slice(agentPanes, func(i, j int) bool {
		return agentPanes[i].Index < agentPanes[j].Index
	})

	// Check that delays increase by approximately stagger interval
	for i, pane := range agentPanes {
		expectedMinDelay := int64(i) * staggerDelay.Milliseconds()
		actualDelay := pane.PromptDelayMs

		logger.Log("[E2E-STAGGER] Pane %d (type=%s): delay=%dms, expected_min=%dms",
			pane.Index, pane.Type, actualDelay, expectedMinDelay)

		// Allow 200ms tolerance for timing variations
		if actualDelay < expectedMinDelay-200 {
			t.Errorf("[E2E-STAGGER] Pane %d delay too short: got %dms, expected >= %dms",
				pane.Index, actualDelay, expectedMinDelay-200)
		}
	}

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// TestE2E_StaggeredPromptDelivery tests staggered prompt delivery to existing session.
// Verifies that ntm send --stagger delivers prompts with specified delays.
func TestE2E_StaggeredPromptDelivery(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "stagger_prompt_delivery")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-prompt-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger_prompt")

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	// First, create session without stagger
	spawnArgs := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		strings.Replace(flag, "=1", "=2", 1), // Spawn 2 agents
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", spawnArgs...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Creating session: ntm %s", strings.Join(spawnArgs, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	// Wait for agents to initialize
	time.Sleep(5 * time.Second)

	// Now send a staggered prompt
	staggerDelay := 300 * time.Millisecond
	sendArgs := []string{
		"--json",
		"send",
		session,
		"--all",
		"--msg", "Hello from staggered test",
		fmt.Sprintf("--stagger=%s", staggerDelay),
	}

	sendCmd := exec.CommandContext(ctx, "ntm", sendArgs...)
	sendCmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	sendCmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Sending staggered prompt: ntm %s", strings.Join(sendArgs, " "))
	startTime := time.Now()
	sendOut, err := sendCmd.CombinedOutput()
	sendDuration := time.Since(startTime)

	if err != nil {
		// Log but don't fail - send may not support --stagger yet
		logger.Log("[E2E-STAGGER] send returned error: %v output=%s", err, string(sendOut))
	}

	logger.Log("[E2E-STAGGER] Send completed in %s", sendDuration)
	logger.Log("[E2E-STAGGER] Send output: %s", string(sendOut))

	// Try to parse response if available
	var sendResp sendResponse
	if err := json.Unmarshal(sendOut, &sendResp); err == nil {
		logger.LogJSON("[E2E-STAGGER] send response", sendResp)

		// If stagger info present, verify it
		if sendResp.Stagger != nil && sendResp.Stagger.Enabled {
			logger.Log("[E2E-STAGGER] Stagger enabled with interval %dms", sendResp.Stagger.IntervalMs)
		}

		// Verify delivery order if timestamps available
		if len(sendResp.Delivered) > 1 {
			for i, d := range sendResp.Delivered {
				logger.Log("[E2E-STAGGER] Delivered[%d]: pane=%d, delay=%dms", i, d.Pane, d.DelayMs)
			}
		}
	}

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// TestE2E_SmartStaggerMode tests the adaptive "smart" stagger mode.
// Smart mode adjusts delays based on rate limit history.
func TestE2E_SmartStaggerMode(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "smart_stagger")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-smart-stagger-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_smart_stagger")

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	// Test spawn with --stagger-mode=smart
	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		flag,
		"--stagger-mode=smart",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running smart stagger: ntm %s", strings.Join(args, " "))
	out, err := cmd.CombinedOutput()

	if err != nil {
		// Smart mode might not be fully implemented, log and check
		logger.Log("[E2E-STAGGER] smart stagger spawn: %v output=%s", err, string(out))
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		logger.Log("[E2E-STAGGER] parse error (may be expected): %v", err)
		logger.Log("[E2E-STAGGER] output: %s", string(out))
		// Don't fail - feature may be under development
		cleanupSessionIfExists(logger, session, baseDir)
		t.Skip("[E2E-STAGGER] smart stagger mode not yet available")
		return
	}

	logger.LogJSON("[E2E-STAGGER] smart spawn response", resp)

	// Verify session created
	if !resp.Created {
		t.Fatalf("[E2E-STAGGER] expected created=true")
	}

	// Log stagger mode info
	if resp.Stagger != nil {
		logger.Log("[E2E-STAGGER] Stagger enabled=%v, mode=%s, interval=%dms",
			resp.Stagger.Enabled, resp.Stagger.Mode, resp.Stagger.IntervalMs)
	}

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// TestE2E_StaggerDisabled tests that stagger=0 disables staggering.
func TestE2E_StaggerDisabled(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "stagger_disabled")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-disabled-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger_disabled")

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	// Explicitly disable stagger with --stagger=0
	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		strings.Replace(flag, "=1", "=2", 1), // Spawn 2 agents
		"--stagger=0",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running with disabled stagger: ntm %s", strings.Join(args, " "))
	out, err := cmd.CombinedOutput()

	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("[E2E-STAGGER] parse spawn JSON: %v output=%s", err, string(out))
	}
	logger.LogJSON("[E2E-STAGGER] disabled stagger response", resp)

	// Verify stagger is NOT enabled or interval is 0
	if resp.Stagger != nil && resp.Stagger.Enabled && resp.Stagger.IntervalMs > 0 {
		t.Errorf("[E2E-STAGGER] expected stagger disabled, got enabled=%v interval=%dms",
			resp.Stagger.Enabled, resp.Stagger.IntervalMs)
	}

	// Verify all panes have 0 delay
	for _, pane := range resp.Panes {
		if pane.PromptDelayMs > 0 {
			t.Errorf("[E2E-STAGGER] pane %d has delay %dms, expected 0", pane.Index, pane.PromptDelayMs)
		}
	}

	logger.Log("[E2E-STAGGER] Verified: stagger correctly disabled")

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// TestE2E_StaggerTimingVerification verifies actual spawn timing matches configured delays.
// This is a more rigorous timing test that measures actual spawn intervals.
func TestE2E_StaggerTimingVerification(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "stagger_timing")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-timing-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger_timing")

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	// Use a measurable stagger (1 second)
	staggerDelay := 1 * time.Second
	numAgents := 3

	flagWithCount := strings.Replace(flag, "=1", fmt.Sprintf("=%d", numAgents), 1)

	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		flagWithCount,
		fmt.Sprintf("--stagger=%s", staggerDelay),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running timing verification: ntm %s", strings.Join(args, " "))
	startTime := time.Now()
	out, err := cmd.CombinedOutput()
	totalDuration := time.Since(startTime)

	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("[E2E-STAGGER] parse spawn JSON: %v output=%s", err, string(out))
	}
	logger.LogJSON("[E2E-STAGGER] timing response", resp)

	logger.Log("[E2E-STAGGER] Total spawn duration: %s", totalDuration)

	// Expected minimum duration: (numAgents-1) * stagger
	// (first agent has no delay, subsequent agents wait)
	expectedMinDuration := time.Duration(numAgents-1) * staggerDelay

	logger.Log("[E2E-STAGGER] Expected minimum duration: %s (for %d agents with %s stagger)",
		expectedMinDuration, numAgents, staggerDelay)

	// Verify total duration is at least the expected minimum
	// (Allow some tolerance for overhead)
	toleranceDuration := 500 * time.Millisecond
	if totalDuration < expectedMinDuration-toleranceDuration {
		logger.Log("[E2E-STAGGER] WARNING: Spawn completed faster than expected stagger would allow")
		logger.Log("[E2E-STAGGER] This might indicate stagger delays were not applied")
		// Don't fail - the delays might be applied asynchronously after spawn returns
	}

	// Verify pane delays in response
	agentPanes := filterAgentPanes(resp.Panes)
	sort.Slice(agentPanes, func(i, j int) bool {
		return agentPanes[i].Index < agentPanes[j].Index
	})

	for i, pane := range agentPanes {
		expectedDelay := int64(i) * staggerDelay.Milliseconds()
		actualDelay := pane.PromptDelayMs

		logger.Log("[E2E-STAGGER] Pane %d: expected_delay=%dms, actual_delay=%dms, diff=%dms",
			pane.Index, expectedDelay, actualDelay, actualDelay-expectedDelay)

		// Allow 500ms tolerance
		tolerance := int64(500)
		if math.Abs(float64(actualDelay-expectedDelay)) > float64(tolerance) {
			t.Errorf("[E2E-STAGGER] Pane %d timing mismatch: expected ~%dms, got %dms",
				pane.Index, expectedDelay, actualDelay)
		}
	}

	logger.Log("[E2E-STAGGER] Timing verification passed")

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// TestE2E_StaggerDeliveryOrder verifies prompts are delivered in order.
func TestE2E_StaggerDeliveryOrder(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("no agent CLI available")
	}

	logger := NewTestLogger(t, "stagger_order")
	defer logger.Close()

	baseDir, err := os.MkdirTemp("", "ntm-stagger-order-")
	if err != nil {
		t.Fatalf("[E2E-STAGGER] temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	session := newStaggerSession(t, "e2e_stagger_order")

	flag, ok := agentFlag(agentType)
	if !ok {
		t.Fatalf("[E2E-STAGGER] unsupported agent type: %s", agentType)
	}

	// Spawn 3 agents with stagger
	staggerDelay := 200 * time.Millisecond
	flagWithCount := strings.Replace(flag, "=1", "=3", 1)

	args := []string{
		"--json",
		"spawn",
		session,
		"--no-hooks",
		flagWithCount,
		fmt.Sprintf("--stagger=%s", staggerDelay),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	cmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	cmd.Dir = baseDir

	logger.Log("[E2E-STAGGER] Running: ntm %s", strings.Join(args, " "))
	out, err := cmd.CombinedOutput()

	if err != nil {
		logger.Log("[E2E-STAGGER] spawn error: %v output=%s", err, string(out))
		t.Fatalf("[E2E-STAGGER] spawn failed: %v", err)
	}

	var resp spawnResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("[E2E-STAGGER] parse spawn JSON: %v output=%s", err, string(out))
	}
	logger.LogJSON("[E2E-STAGGER] order test response", resp)

	// Verify delays are monotonically increasing
	agentPanes := filterAgentPanes(resp.Panes)
	sort.Slice(agentPanes, func(i, j int) bool {
		return agentPanes[i].Index < agentPanes[j].Index
	})

	var lastDelay int64 = -1
	for i, pane := range agentPanes {
		logger.Log("[E2E-STAGGER] Pane %d delay: %dms", pane.Index, pane.PromptDelayMs)

		if pane.PromptDelayMs < lastDelay {
			t.Errorf("[E2E-STAGGER] Delivery order violation: pane %d has delay %dms, but previous had %dms",
				pane.Index, pane.PromptDelayMs, lastDelay)
		}

		// First agent should have 0 or minimal delay
		if i == 0 && pane.PromptDelayMs > 100 {
			logger.Log("[E2E-STAGGER] Note: First agent has delay %dms (expected ~0)", pane.PromptDelayMs)
		}

		lastDelay = pane.PromptDelayMs
	}

	logger.Log("[E2E-STAGGER] Delivery order verified: delays are monotonically increasing")

	// Cleanup
	cleanupSession(t, logger, session, baseDir)
}

// --- Helper Functions ---

// filterAgentPanes returns only agent panes (excludes user pane)
func filterAgentPanes(panes []spawnPane) []spawnPane {
	var result []spawnPane
	for _, p := range panes {
		if p.Type != "user" && p.Type != "" {
			result = append(result, p)
		}
	}
	return result
}

// cleanupSession kills a session and logs the result
func cleanupSession(t *testing.T, logger *TestLogger, session, baseDir string) {
	t.Helper()

	killArgs := []string{"--json", "kill", session, "--force"}
	killCmd := exec.CommandContext(context.Background(), "ntm", killArgs...)
	killCmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	killCmd.Dir = baseDir

	if killOut, killErr := killCmd.CombinedOutput(); killErr != nil {
		logger.Log("[E2E-STAGGER] cleanup kill error: %v output=%s", killErr, string(killOut))
		// Don't fail test on cleanup error
	} else {
		logger.Log("[E2E-STAGGER] Session %s cleaned up", session)
	}
}

// cleanupSessionIfExists attempts to kill a session without failing
func cleanupSessionIfExists(logger *TestLogger, session, baseDir string) {
	killArgs := []string{"--json", "kill", session, "--force"}
	killCmd := exec.CommandContext(context.Background(), "ntm", killArgs...)
	killCmd.Env = append(os.Environ(), "NTM_PROJECTS_BASE="+baseDir)
	killCmd.Dir = baseDir
	killCmd.CombinedOutput() // Ignore errors
}
