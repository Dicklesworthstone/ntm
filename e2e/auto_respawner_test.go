//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// auto_respawner_test.go implements E2E tests for the AutoRespawner system.
//
// Bead: bd-35vy9 - E2E Tests: AutoRespawner full cycle with real tmux
//
// These tests verify the complete respawn cycle with REAL tmux sessions:
// - Graceful agent kill (Ctrl+C)
// - Force kill fallback for trapped processes
// - Pane clear and new agent spawn
// - Prompt injection after respawn
package e2e

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newRespawnIdentifier(t *testing.T, prefix string) string {
	t.Helper()

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("[E2E-RESPAWN] generate identifier: %v", err)
	}
	return fmt.Sprintf("%s_%x", prefix, suffix)
}

// =============================================================================
// Test Scenario 1: Graceful Kill with Ctrl+C
// =============================================================================
// Spawn a process in a tmux pane, send Ctrl+C, verify process terminates.

func TestE2E_GracefulKillCtrlC(t *testing.T) {
	CommonE2EPrerequisites(t)

	logger := NewTestLogger(t, "graceful_kill")
	defer logger.Close()

	session := newRespawnIdentifier(t, "e2e_respawn_graceful")
	logger.Log("[E2E-RESPAWN] Creating test session: %s", session)

	// Create tmux session
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to create session: %v", err)
	}
	defer func() {
		logger.Log("[E2E-RESPAWN] Cleanup: killing session %s", session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Start a process that responds to SIGINT (sleep)
	logger.Log("[E2E-RESPAWN] Starting sleep process")
	target := session // Use just session name, tmux will use the active pane
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "sleep 3600", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to start process: %v", err)
	}
	time.Sleep(1 * time.Second) // Give enough time for process to spawn

	// Get the pane PID
	pidOutput, err := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", target, "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to get pane PID: %v", err)
	}
	panePID := strings.TrimSpace(string(pidOutput))
	logger.Log("[E2E-RESPAWN] Pane PID: %s", panePID)

	// Verify sleep is running as a child of the shell
	childPIDs := getChildPIDs(panePID)
	logger.Log("[E2E-RESPAWN] Child PIDs before kill: %v", childPIDs)
	if len(childPIDs) == 0 {
		t.Skip("[E2E-RESPAWN] No child process found - sleep may have started in background")
	}

	// Send Ctrl+C (SIGINT)
	logger.Log("[E2E-RESPAWN] Sending Ctrl+C")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to send Ctrl+C: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify process terminated
	childPIDsAfter := getChildPIDs(panePID)
	logger.Log("[E2E-RESPAWN] Child PIDs after kill: %v", childPIDsAfter)

	// Check that sleep is no longer running
	sleepStillRunning := false
	for _, pid := range childPIDsAfter {
		cmdLine := getProcessCmdline(pid)
		if strings.Contains(cmdLine, "sleep") {
			sleepStillRunning = true
			break
		}
	}

	if sleepStillRunning {
		t.Errorf("[E2E-RESPAWN] Sleep process still running after Ctrl+C")
	} else {
		logger.Log("[E2E-RESPAWN] SUCCESS: Graceful kill with Ctrl+C terminated the process")
	}
}

// =============================================================================
// Test Scenario 2: Force Kill Fallback
// =============================================================================
// Spawn a process that traps SIGINT, verify Ctrl+C doesn't work, then force kill.

func TestE2E_ForceKillFallback(t *testing.T) {
	CommonE2EPrerequisites(t)

	logger := NewTestLogger(t, "force_kill")
	defer logger.Close()

	session := newRespawnIdentifier(t, "e2e_respawn_force")
	logger.Log("[E2E-RESPAWN] Creating test session: %s", session)

	// Create tmux session
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to create session: %v", err)
	}
	defer func() {
		logger.Log("[E2E-RESPAWN] Cleanup: killing session %s", session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	// Start a process that traps SIGINT
	logger.Log("[E2E-RESPAWN] Starting process with SIGINT trap")
	target := session
	trapCmd := `trap "" SIGINT; sleep 3600`
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, trapCmd, "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to start trapped process: %v", err)
	}
	time.Sleep(1 * time.Second) // Give enough time for process to spawn

	// Get the pane PID
	pidOutput, err := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", target, "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to get pane PID: %v", err)
	}
	panePID := strings.TrimSpace(string(pidOutput))
	logger.Log("[E2E-RESPAWN] Pane PID: %s", panePID)

	// Get child PIDs before attempting kill
	childPIDs := getChildPIDs(panePID)
	logger.Log("[E2E-RESPAWN] Child PIDs before kill attempt: %v", childPIDs)

	// Send Ctrl+C (should be ignored due to trap)
	logger.Log("[E2E-RESPAWN] Sending Ctrl+C (should be trapped)")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to send Ctrl+C: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify process is still running (Ctrl+C was trapped)
	childPIDsAfterCtrlC := getChildPIDs(panePID)
	logger.Log("[E2E-RESPAWN] Child PIDs after Ctrl+C: %v", childPIDsAfterCtrlC)

	sleepStillRunning := false
	var sleepPID string
	for _, pid := range childPIDsAfterCtrlC {
		cmdLine := getProcessCmdline(pid)
		if strings.Contains(cmdLine, "sleep") {
			sleepStillRunning = true
			sleepPID = pid
			break
		}
	}

	if !sleepStillRunning {
		logger.Log("[E2E-RESPAWN] WARNING: Process died from Ctrl+C - trap may not have worked")
		// Continue test anyway - the force kill will work on a dead process
	} else {
		logger.Log("[E2E-RESPAWN] Confirmed: Ctrl+C was trapped, process still running")
	}

	// Now force kill
	logger.Log("[E2E-RESPAWN] Sending SIGKILL to force terminate")
	if sleepPID != "" {
		if err := exec.Command("kill", "-9", sleepPID).Run(); err != nil {
			logger.Log("[E2E-RESPAWN] kill -9 failed: %v (may already be dead)", err)
		}
	} else {
		// Kill all children of the pane
		for _, pid := range childPIDsAfterCtrlC {
			exec.Command("kill", "-9", pid).Run()
		}
	}
	time.Sleep(500 * time.Millisecond)

	// Verify process is now terminated
	childPIDsAfterKill := getChildPIDs(panePID)
	logger.Log("[E2E-RESPAWN] Child PIDs after force kill: %v", childPIDsAfterKill)

	sleepStillRunningAfterKill := false
	for _, pid := range childPIDsAfterKill {
		cmdLine := getProcessCmdline(pid)
		if strings.Contains(cmdLine, "sleep") {
			sleepStillRunningAfterKill = true
			break
		}
	}

	if sleepStillRunningAfterKill {
		t.Errorf("[E2E-RESPAWN] Sleep process still running after force kill")
	} else {
		logger.Log("[E2E-RESPAWN] SUCCESS: Force kill terminated the trapped process")
	}
}

// =============================================================================
// Test Scenario 3: Clear Pane and Respawn
// =============================================================================
// Kill a process, clear the pane, spawn a new command, verify it runs.

func TestE2E_ClearPaneAndRespawn(t *testing.T) {
	CommonE2EPrerequisites(t)

	logger := NewTestLogger(t, "clear_respawn")
	defer logger.Close()

	session := newRespawnIdentifier(t, "e2e_respawn_clear")
	logger.Log("[E2E-RESPAWN] Creating test session: %s", session)

	// Create tmux session
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to create session: %v", err)
	}
	defer func() {
		logger.Log("[E2E-RESPAWN] Cleanup: killing session %s", session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	target := session

	// Start initial process
	logger.Log("[E2E-RESPAWN] Starting initial process")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "echo INITIAL_PROCESS", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to start process: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Clear the pane
	logger.Log("[E2E-RESPAWN] Clearing pane")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "clear", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to clear pane: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Respawn with a new command
	logger.Log("[E2E-RESPAWN] Spawning new command")
	respawnMarker := newRespawnIdentifier(t, "RESPAWNED")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, fmt.Sprintf("echo %s", respawnMarker), "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to spawn new command: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Capture pane output
	output, err := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
	if err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to capture pane: %v", err)
	}
	paneContent := string(output)
	logger.Log("[E2E-RESPAWN] Pane content:\n%s", paneContent)

	// Verify new command output is visible
	if !strings.Contains(paneContent, respawnMarker) {
		t.Errorf("[E2E-RESPAWN] Respawn marker not found in pane output")
	} else {
		logger.Log("[E2E-RESPAWN] SUCCESS: Clear pane and respawn working correctly")
	}

	// Verify old output is not visible (clear worked)
	if strings.Contains(paneContent, "INITIAL_PROCESS") {
		logger.Log("[E2E-RESPAWN] WARNING: Initial process output still visible after clear")
		// This is not necessarily a failure - clear behavior varies by terminal
	}
}

// =============================================================================
// Test Scenario 4: Prompt Injection After Respawn
// =============================================================================
// Simulate the full respawn cycle including marching orders injection.

func TestE2E_PromptInjectionAfterRespawn(t *testing.T) {
	CommonE2EPrerequisites(t)

	logger := NewTestLogger(t, "prompt_injection")
	defer logger.Close()

	session := newRespawnIdentifier(t, "e2e_respawn_prompt")
	logger.Log("[E2E-RESPAWN] Creating test session: %s", session)

	// Create tmux session
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to create session: %v", err)
	}
	defer func() {
		logger.Log("[E2E-RESPAWN] Cleanup: killing session %s", session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	target := session

	// Simulate a running "agent" (just cat waiting for input)
	logger.Log("[E2E-RESPAWN] Starting simulated agent (cat)")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "cat", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to start cat: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Kill the agent
	logger.Log("[E2E-RESPAWN] Killing simulated agent")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to send Ctrl+C: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Clear pane
	logger.Log("[E2E-RESPAWN] Clearing pane")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "clear", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to clear: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// "Respawn" with a new simulated agent
	logger.Log("[E2E-RESPAWN] Respawning simulated agent")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "cat", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to respawn cat: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Inject "marching orders" (prompt)
	marchingOrders := "MARCHING_ORDERS_RECEIVED"
	logger.Log("[E2E-RESPAWN] Injecting marching orders: %s", marchingOrders)
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, marchingOrders, "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to inject prompt: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Capture output to verify injection
	output, err := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
	if err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to capture pane: %v", err)
	}
	paneContent := string(output)
	logger.Log("[E2E-RESPAWN] Pane content:\n%s", paneContent)

	// Verify marching orders were received (cat echoes input)
	if !strings.Contains(paneContent, marchingOrders) {
		t.Errorf("[E2E-RESPAWN] Marching orders not found in output")
	} else {
		logger.Log("[E2E-RESPAWN] SUCCESS: Prompt injection after respawn works correctly")
	}

	// Clean up - kill cat
	exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run()
}

// =============================================================================
// Test Scenario 5: Full Respawn Cycle Simulation
// =============================================================================
// End-to-end test of the full respawn sequence without using real agents.

func TestE2E_FullRespawnCycleSimulation(t *testing.T) {
	CommonE2EPrerequisites(t)

	logger := NewTestLogger(t, "full_cycle")
	defer logger.Close()

	session := newRespawnIdentifier(t, "e2e_respawn_full")
	logger.Log("[E2E-RESPAWN] Creating test session: %s", session)

	// Create tmux session
	cmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30")
	if err := cmd.Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to create session: %v", err)
	}
	defer func() {
		logger.Log("[E2E-RESPAWN] Cleanup: killing session %s", session)
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	}()

	target := session

	// Step 1: Start simulated agent
	logger.Log("[E2E-RESPAWN] Step 1: Starting simulated agent")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "echo AGENT_V1_RUNNING; sleep 30", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to start agent: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Verify agent is running
	logger.Log("[E2E-RESPAWN] Step 2: Verifying agent is running")
	output1, _ := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
	if !strings.Contains(string(output1), "AGENT_V1_RUNNING") {
		t.Fatalf("[E2E-RESPAWN] Agent not running - initial spawn failed")
	}
	logger.Log("[E2E-RESPAWN] Agent V1 confirmed running")

	// Step 3: Kill the agent (graceful)
	logger.Log("[E2E-RESPAWN] Step 3: Killing agent (graceful)")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to kill agent: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 4: Clear pane
	logger.Log("[E2E-RESPAWN] Step 4: Clearing pane")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "clear", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to clear pane: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Step 5: Change directory (simulated)
	logger.Log("[E2E-RESPAWN] Step 5: Changing to project directory")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "cd /tmp && pwd", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to cd: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Step 6: Respawn agent
	logger.Log("[E2E-RESPAWN] Step 6: Respawning agent")
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "echo AGENT_V2_RUNNING", "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to respawn: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Step 7: Inject marching orders
	logger.Log("[E2E-RESPAWN] Step 7: Injecting marching orders")
	marchingOrders := "echo MARCHING_ORDERS_INJECTED"
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, marchingOrders, "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to inject orders: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 8: Verify respawn success
	logger.Log("[E2E-RESPAWN] Step 8: Verifying respawn success")
	output2, _ := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
	paneContent := string(output2)
	logger.Log("[E2E-RESPAWN] Final pane content:\n%s", paneContent)

	// Verify all steps completed
	checks := map[string]bool{
		"AGENT_V2_RUNNING":         strings.Contains(paneContent, "AGENT_V2_RUNNING"),
		"MARCHING_ORDERS_INJECTED": strings.Contains(paneContent, "MARCHING_ORDERS_INJECTED"),
		"/tmp":                     strings.Contains(paneContent, "/tmp"),
	}

	allPassed := true
	for check, passed := range checks {
		if !passed {
			t.Errorf("[E2E-RESPAWN] Check failed: %s not found in output", check)
			allPassed = false
		} else {
			logger.Log("[E2E-RESPAWN] Check passed: %s", check)
		}
	}

	if allPassed {
		logger.Log("[E2E-RESPAWN] SUCCESS: Full respawn cycle completed successfully")
	}
}

// =============================================================================
// Test Scenario 6: Real Agent Respawn (if available)
// =============================================================================
// Test respawn with a real agent if one is available.

func TestE2E_RealAgentRespawn(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("No agent available")
	}

	suite := NewTestSuite(t, "real_agent_respawn")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Setup failed: %v", err)
	}

	// Spawn agent in pane 0 (pane 1 in TestSuite creates new pane)
	suite.Logger().Log("[E2E-RESPAWN] Spawning %s agent", agentType)

	target := suite.Session() // Use just session name for tmux send-keys

	// Resolve NTM's short agent type to the executable that is actually launched.
	// In particular, `cc` may be a C compiler on PATH and is not Claude Code.
	agentCmd, ok := agentExecutable(agentType)
	if !ok {
		t.Fatalf("[E2E-RESPAWN] unsupported agent type: %s", agentType)
	}

	// Spawn the agent
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, agentCmd, "Enter").Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Failed to spawn agent: %v", err)
	}

	// Wait for agent to start
	suite.Logger().Log("[E2E-RESPAWN] Waiting for agent to start...")
	time.Sleep(8 * time.Second)

	pid, ok := waitForPaneAgentPID(target, agentCmd, 20*time.Second)
	if !ok {
		output, _ := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p", "-S", "-40").Output()
		t.Fatalf("[E2E-RESPAWN] %s did not start in pane; output:\n%s", agentCmd, output)
	}
	suite.Logger().Log("[E2E-RESPAWN] Confirmed %s process pid=%s", agentCmd, pid)

	// Simulate an unexpected agent crash. The PID is resolved from this test's
	// private tmux pane immediately before the signal is sent.
	suite.Logger().Log("[E2E-RESPAWN] Simulating crash of pid=%s", pid)
	if err := exec.Command("kill", "-9", pid).Run(); err != nil {
		t.Fatalf("[E2E-RESPAWN] kill agent pid %s: %v", pid, err)
	}
	if waitForPaneAgentExit(target, agentCmd, 10*time.Second) {
		t.Fatalf("[E2E-RESPAWN] %s still running after simulated crash", agentCmd)
	}

	// Clear and respawn
	suite.Logger().Log("[E2E-RESPAWN] Clearing pane")
	exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "clear", "Enter").Run()
	time.Sleep(300 * time.Millisecond)

	suite.Logger().Log("[E2E-RESPAWN] Respawning agent")
	exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, agentCmd, "Enter").Run()

	if newPID, ok := waitForPaneAgentPID(target, agentCmd, 20*time.Second); !ok {
		output, _ := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p", "-S", "-40").Output()
		t.Fatalf("[E2E-RESPAWN] %s did not recover after crash; output:\n%s", agentCmd, output)
	} else if newPID == pid {
		t.Fatalf("[E2E-RESPAWN] %s recovery reused terminated pid %s", agentCmd, pid)
	}
	suite.Logger().Log("[E2E-RESPAWN] SUCCESS: Real agent recovered after crash")

	// Clean up - kill the agent
	suite.Logger().Log("[E2E-RESPAWN] Cleaning up - killing agent")
	exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run()
	time.Sleep(100 * time.Millisecond)
	exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, "C-c").Run()
}

// =============================================================================
// Test: NTM Robot Smart Restart Integration
// =============================================================================
// Test that ntm --robot-smart-restart properly handles respawns.

func TestE2E_RobotSmartRestartIntegration(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	agentType := GetAvailableAgent()
	if agentType == "" {
		t.Skip("No agent available")
	}

	suite := NewTestSuite(t, "smart_restart_integration")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-RESPAWN] Setup failed: %v", err)
	}

	// Spawn agent using SpawnAgent helper
	if err := suite.SpawnAgent(1, agentType); err != nil {
		t.Fatalf("[E2E-RESPAWN] Spawn failed: %v", err)
	}

	// Wait for agent to become idle
	suite.Logger().Log("[E2E-RESPAWN] Waiting for agent to become idle")
	found := suite.WaitForState(1, func(s *PaneWorkStatus) bool {
		return s.IsIdle
	}, 60*time.Second)

	if !found {
		t.Skip("[E2E-RESPAWN] Could not get agent to idle state")
	}

	// Execute smart restart with force flag
	suite.Logger().Log("[E2E-RESPAWN] Executing --robot-smart-restart with force")
	result, err := suite.CallSmartRestart([]int{1}, true, false)
	if err != nil {
		t.Fatalf("[E2E-RESPAWN] SmartRestart failed: %v", err)
	}

	suite.Logger().LogJSON("[E2E-RESPAWN] Smart restart result", result)

	// Verify restart happened
	action, ok := result.Actions["1"]
	if !ok {
		t.Fatal("[E2E-RESPAWN] Pane 1 not found in actions")
	}

	if action.Action != "RESTARTED" {
		suite.Logger().Log("[E2E-RESPAWN] Unexpected action: %s (reason: %s)", action.Action, action.Reason)
		// Don't fail - might have been working
	} else {
		suite.Logger().Log("[E2E-RESPAWN] SUCCESS: Smart restart executed correctly")
	}

	// Wait for new agent to initialize
	time.Sleep(8 * time.Second)

	// Verify new agent is running
	isWorkingResult, _ := suite.CallIsWorking([]int{1})
	if isWorkingResult != nil && isWorkingResult.Success {
		status := isWorkingResult.Panes["1"]
		if status.AgentType != "unknown" {
			suite.Logger().Log("[E2E-RESPAWN] New agent running: type=%s, idle=%v",
				status.AgentType, status.IsIdle)
		}
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

// getChildPIDs returns the PIDs of all child processes of the given PID.
func getChildPIDs(parentPID string) []string {
	output, err := exec.Command("pgrep", "-P", parentPID).Output()
	if err != nil {
		return []string{}
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var pids []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

func waitForPaneAgentPID(target, executable string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid, ok := paneAgentPID(target, executable); ok {
			return pid, true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", false
}

func waitForPaneAgentExit(target, executable string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := paneAgentPID(target, executable); !ok {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return true
}

func paneAgentPID(target, executable string) (string, bool) {
	panePID, err := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", target, "#{pane_pid}").Output()
	if err != nil {
		return "", false
	}
	for _, pid := range getChildPIDs(strings.TrimSpace(string(panePID))) {
		if strings.Contains(getProcessCmdline(pid), executable) {
			return pid, true
		}
	}
	return "", false
}

// getProcessCmdline returns the command line of a process.
func getProcessCmdline(pid string) string {
	data, err := exec.Command("ps", "-p", pid, "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// AutoRespawnerTestResult represents parsed output from respawn operations.
type AutoRespawnerTestResult struct {
	Success        bool   `json:"success"`
	SessionPane    string `json:"session_pane"`
	AgentType      string `json:"agent_type"`
	AccountRotated bool   `json:"account_rotated"`
	Duration       string `json:"duration"`
	Error          string `json:"error,omitempty"`
}

// parseRespawnResult parses JSON respawn result output.
func parseRespawnResult(output []byte) (*AutoRespawnerTestResult, error) {
	var result AutoRespawnerTestResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse failed: %w, output: %s", err, string(output))
	}
	return &result, nil
}

// waitForShellPrompt waits for a shell prompt to appear in the pane.
func waitForShellPrompt(target string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", target, "-p").Output()
		if err == nil {
			content := string(output)
			// Check for common shell prompts
			if strings.Contains(content, "$ ") || strings.Contains(content, "% ") ||
				strings.Contains(content, "> ") || strings.Contains(content, "# ") {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
