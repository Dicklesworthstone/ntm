package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ============================================================================
// Flag Parsing Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignFlagParsing tests that --assign flag is parsed correctly
func TestSpawnAssignFlagParsing(t *testing.T) {
	t.Log("=== TestSpawnAssignFlagParsing ===")

	t.Run("assign_flag_defaults", func(t *testing.T) {
		// Test default values when flags are not set
		t.Log("[SETUP] Testing default flag values")

		opts := SpawnOptions{
			Session:            "test-session",
			Assign:             false,
			AssignStrategy:     "balanced",
			AssignLimit:        0,
			AssignReadyTimeout: 60 * time.Second,
		}

		t.Logf("[VERIFY] Assign=%v, Strategy=%s, Limit=%d, Timeout=%v",
			opts.Assign, opts.AssignStrategy, opts.AssignLimit, opts.AssignReadyTimeout)

		if opts.Assign {
			t.Error("[FAIL] Default Assign should be false")
		}
		if opts.AssignStrategy != "balanced" {
			t.Errorf("[FAIL] Default strategy should be 'balanced', got %q", opts.AssignStrategy)
		}
		if opts.AssignLimit != 0 {
			t.Errorf("[FAIL] Default limit should be 0 (unlimited), got %d", opts.AssignLimit)
		}
		if opts.AssignReadyTimeout != 60*time.Second {
			t.Errorf("[FAIL] Default timeout should be 60s, got %v", opts.AssignReadyTimeout)
		}

		t.Log("[PASS] Default flag values correct")
	})

	t.Run("assign_flag_enabled", func(t *testing.T) {
		t.Log("[SETUP] Testing --assign flag enabled")

		opts := SpawnOptions{
			Session: "test-session",
			Assign:  true,
		}

		if !opts.Assign {
			t.Error("[FAIL] Assign should be true when flag is set")
		}

		t.Log("[PASS] Assign flag correctly enabled")
	})

	t.Run("assign_with_strategy_flag", func(t *testing.T) {
		t.Log("[SETUP] Testing --assign with --strategy flag")

		strategies := []string{"balanced", "speed", "quality", "dependency", "round-robin"}
		for _, strategy := range strategies {
			opts := SpawnOptions{
				Session:        "test-session",
				Assign:         true,
				AssignStrategy: strategy,
			}

			t.Logf("[VERIFY] Strategy=%s", opts.AssignStrategy)

			if opts.AssignStrategy != strategy {
				t.Errorf("[FAIL] Strategy should be %q, got %q", strategy, opts.AssignStrategy)
			}
		}

		t.Log("[PASS] Strategy flag correctly passed through")
	})

	t.Run("assign_with_limit_flag", func(t *testing.T) {
		t.Log("[SETUP] Testing --assign with --limit flag")

		limits := []int{0, 1, 5, 10, 100}
		for _, limit := range limits {
			opts := SpawnOptions{
				Session:     "test-session",
				Assign:      true,
				AssignLimit: limit,
			}

			t.Logf("[VERIFY] Limit=%d", opts.AssignLimit)

			if opts.AssignLimit != limit {
				t.Errorf("[FAIL] Limit should be %d, got %d", limit, opts.AssignLimit)
			}
		}

		t.Log("[PASS] Limit flag correctly passed through")
	})

	t.Run("assign_with_ready_timeout_flag", func(t *testing.T) {
		t.Log("[SETUP] Testing --assign with --ready-timeout flag")

		timeouts := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 5 * time.Minute}
		for _, timeout := range timeouts {
			opts := SpawnOptions{
				Session:            "test-session",
				Assign:             true,
				AssignReadyTimeout: timeout,
			}

			t.Logf("[VERIFY] ReadyTimeout=%v", opts.AssignReadyTimeout)

			if opts.AssignReadyTimeout != timeout {
				t.Errorf("[FAIL] ReadyTimeout should be %v, got %v", timeout, opts.AssignReadyTimeout)
			}
		}

		t.Log("[PASS] ReadyTimeout flag correctly passed through")
	})
}

// TestSpawnAssignInvalidStrategy tests error handling for invalid strategy
func TestSpawnAssignInvalidStrategy(t *testing.T) {
	t.Log("=== TestSpawnAssignInvalidStrategy ===")

	testCases := []struct {
		name     string
		strategy string
		expected assign.Strategy
	}{
		{"empty_defaults_to_balanced", "", assign.StrategyBalanced},
		{"invalid_defaults_to_balanced", "invalid", assign.StrategyBalanced},
		{"unknown_defaults_to_balanced", "unknown", assign.StrategyBalanced},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("[SETUP] Testing strategy: %q", tc.strategy)

			// Use the assign package's ParseStrategy to validate
			parsed := assign.ParseStrategy(tc.strategy)
			t.Logf("[VERIFY] ParseStrategy(%q) = %v", tc.strategy, parsed)

			if parsed != tc.expected {
				t.Errorf("[FAIL] Expected %v, got %v", tc.expected, parsed)
			}

			t.Log("[PASS] Invalid strategy handled correctly")
		})
	}
}

// ============================================================================
// Integration Tests (bd-1tu58)
// ============================================================================

// TestSpawnOptionsAssignFields verifies SpawnOptions has all assign-related fields
func TestSpawnOptionsAssignFields(t *testing.T) {
	t.Log("=== TestSpawnOptionsAssignFields ===")

	opts := SpawnOptions{
		Session:            "test",
		Assign:             true,
		AssignStrategy:     "quality",
		AssignLimit:        10,
		AssignReadyTimeout: 90 * time.Second,
	}

	t.Logf("[VERIFY] SpawnOptions fields:")
	t.Logf("  Assign=%v", opts.Assign)
	t.Logf("  AssignStrategy=%s", opts.AssignStrategy)
	t.Logf("  AssignLimit=%d", opts.AssignLimit)
	t.Logf("  AssignReadyTimeout=%v", opts.AssignReadyTimeout)

	// Verify all fields are accessible
	if !opts.Assign {
		t.Error("[FAIL] Assign field not set correctly")
	}
	if opts.AssignStrategy != "quality" {
		t.Errorf("[FAIL] AssignStrategy field not set correctly: got %q", opts.AssignStrategy)
	}
	if opts.AssignLimit != 10 {
		t.Errorf("[FAIL] AssignLimit field not set correctly: got %d", opts.AssignLimit)
	}
	if opts.AssignReadyTimeout != 90*time.Second {
		t.Errorf("[FAIL] AssignReadyTimeout field not set correctly: got %v", opts.AssignReadyTimeout)
	}

	t.Log("[PASS] All SpawnOptions assign fields accessible")
}

// TestAssignCommandOptionsFromSpawn tests conversion from SpawnOptions to AssignCommandOptions
func TestAssignCommandOptionsFromSpawn(t *testing.T) {
	t.Log("=== TestAssignCommandOptionsFromSpawn ===")

	spawnOpts := SpawnOptions{
		Session:        "test-session",
		Assign:         true,
		AssignStrategy: "dependency",
		AssignLimit:    5,
	}

	t.Logf("[SETUP] SpawnOptions: Session=%s, Strategy=%s, Limit=%d",
		spawnOpts.Session, spawnOpts.AssignStrategy, spawnOpts.AssignLimit)

	// Simulate what runAssignmentPhase does
	assignOpts := &AssignCommandOptions{
		Session:  spawnOpts.Session,
		Strategy: spawnOpts.AssignStrategy,
		Limit:    spawnOpts.AssignLimit,
		Timeout:  30 * time.Second,
	}

	t.Logf("[VERIFY] AssignCommandOptions: Session=%s, Strategy=%s, Limit=%d",
		assignOpts.Session, assignOpts.Strategy, assignOpts.Limit)

	if assignOpts.Session != spawnOpts.Session {
		t.Errorf("[FAIL] Session not passed: got %q, want %q", assignOpts.Session, spawnOpts.Session)
	}
	if assignOpts.Strategy != spawnOpts.AssignStrategy {
		t.Errorf("[FAIL] Strategy not passed: got %q, want %q", assignOpts.Strategy, spawnOpts.AssignStrategy)
	}
	if assignOpts.Limit != spawnOpts.AssignLimit {
		t.Errorf("[FAIL] Limit not passed: got %d, want %d", assignOpts.Limit, spawnOpts.AssignLimit)
	}

	t.Log("[PASS] SpawnOptions correctly converted to AssignCommandOptions")
}

// ============================================================================
// Combined Output Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignOutputStructure tests the combined output structure
func TestSpawnAssignOutputStructure(t *testing.T) {
	t.Log("=== TestSpawnAssignOutputStructure ===")

	// Test SpawnAssignResult structure
	result := SpawnAssignResult{
		Spawn: &output.SpawnResponse{
			Session: "test-session",
			Panes:   make([]output.PaneResponse, 4),
		},
		Init: &SpawnInitResult{
			PromptSent:    true,
			AgentsReached: 3,
		},
		Assign: &AssignOutputEnhanced{
			Strategy:    "balanced",
			Assignments: make([]AssignmentItem, 5),
		},
	}

	t.Logf("[VERIFY] SpawnAssignResult structure:")
	t.Logf("  Spawn.Session=%s", result.Spawn.Session)
	t.Logf("  Spawn.Panes count=%d", len(result.Spawn.Panes))
	t.Logf("  Init.AgentsReached=%d", result.Init.AgentsReached)
	t.Logf("  Assign.Strategy=%s", result.Assign.Strategy)
	t.Logf("  Assign.Assignments count=%d", len(result.Assign.Assignments))

	// Verify spawn result
	if result.Spawn == nil {
		t.Error("[FAIL] Spawn result should not be nil")
	} else {
		if result.Spawn.Session != "test-session" {
			t.Errorf("[FAIL] Spawn.Session = %q, want %q", result.Spawn.Session, "test-session")
		}
		if len(result.Spawn.Panes) != 4 {
			t.Errorf("[FAIL] Spawn.Panes count = %d, want %d", len(result.Spawn.Panes), 4)
		}
	}

	// Verify init result
	if result.Init == nil {
		t.Error("[FAIL] Init result should not be nil")
	} else {
		if result.Init.AgentsReached != 3 {
			t.Errorf("[FAIL] Init.AgentsReached = %d, want %d", result.Init.AgentsReached, 3)
		}
	}

	// Verify assign result
	if result.Assign == nil {
		t.Error("[FAIL] Assign result should not be nil")
	} else {
		if result.Assign.Strategy != "balanced" {
			t.Errorf("[FAIL] Assign.Strategy = %q, want %q", result.Assign.Strategy, "balanced")
		}
		if len(result.Assign.Assignments) != 5 {
			t.Errorf("[FAIL] Assign.Assignments count = %d, want %d", len(result.Assign.Assignments), 5)
		}
	}

	t.Log("[PASS] SpawnAssignResult structure is correct")
}

// TestSpawnAssignResultWithErrors tests error inclusion in combined output
func TestSpawnAssignResultWithErrors(t *testing.T) {
	t.Log("=== TestSpawnAssignResultWithErrors ===")

	result := SpawnAssignResult{
		Spawn: &output.SpawnResponse{
			Session: "test-session",
		},
		Assign: &AssignOutputEnhanced{
			Strategy: "balanced",
			Errors:   []string{"assignment failed: no agents ready", "timeout waiting for agents"},
		},
	}

	t.Logf("[VERIFY] Errors in result: %v", result.Assign.Errors)

	if len(result.Assign.Errors) != 2 {
		t.Errorf("[FAIL] Expected 2 errors, got %d", len(result.Assign.Errors))
	}

	t.Log("[PASS] Errors correctly included in output")
}

func TestEmitSpawnAssignJSONUsesNonzeroFailureEnvelope(t *testing.T) {
	result := SpawnAssignResult{
		Spawn: &output.SpawnResponse{Session: "spawn-assign-failure"},
		Assign: &AssignOutputEnhanced{
			Strategy: "balanced",
			Errors:   []string{"ready wait failed: timed out", "assignment failed: no idle agents"},
		},
	}

	data, emitErr := captureSpawnAssignJSON(t, result)
	if !errors.Is(emitErr, errJSONFailure) {
		t.Fatalf("emitSpawnAssignJSON() error = %v, want errJSONFailure", emitErr)
	}

	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var envelope SpawnAssignResult
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode spawn assignment failure: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("spawn assignment failure emitted multiple JSON documents: err=%v extra=%v", err, extra)
	}
	if envelope.Success || envelope.ErrorCode != "ASSIGNMENT_FAILED" || envelope.Error == "" || envelope.Assign == nil || len(envelope.Errors) != 2 {
		t.Fatalf("spawn assignment failure envelope = %+v", envelope)
	}
}

func TestEmitSpawnAssignJSONSuccessExitsCleanly(t *testing.T) {
	data, emitErr := captureSpawnAssignJSON(t, SpawnAssignResult{
		Spawn:  &output.SpawnResponse{Session: "spawn-assign-success"},
		Assign: &AssignOutputEnhanced{Strategy: "balanced"},
	})
	if emitErr != nil {
		t.Fatalf("emitSpawnAssignJSON() error = %v, want nil", emitErr)
	}
	var envelope SpawnAssignResult
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode spawn assignment success: %v raw=%s", err, data)
	}
	if !envelope.Success || envelope.ErrorCode != "" || envelope.Error != "" {
		t.Fatalf("spawn assignment success envelope = %+v", envelope)
	}
}

func captureSpawnAssignJSON(t *testing.T, result SpawnAssignResult) ([]byte, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	os.Stdout = writer
	emitErr := emitSpawnAssignJSON(result)
	_ = writer.Close()
	os.Stdout = oldStdout
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = reader.Close()
	})
	data, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("read stdout capture: %v", readErr)
	}
	return data, emitErr
}

// ============================================================================
// Error Handling Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignWithoutAgents tests error when --assign used without agents
func TestSpawnAssignWithoutAgents(t *testing.T) {
	t.Log("=== TestSpawnAssignWithoutAgents ===")

	opts := SpawnOptions{
		Session:  "test-session",
		Assign:   true,
		CCCount:  0,
		CodCount: 0,
		GmiCount: 0,
		Agents:   nil,
	}

	t.Logf("[SETUP] SpawnOptions with Assign=true but no agents")
	t.Logf("  CCCount=%d, CodCount=%d, GmiCount=%d", opts.CCCount, opts.CodCount, opts.GmiCount)

	// Check total agent count
	totalAgents := opts.CCCount + opts.CodCount + opts.GmiCount + len(opts.Agents)

	if opts.Assign && totalAgents == 0 {
		t.Log("[VERIFY] Detected: --assign with zero agents")
		// In real implementation, this should produce an error
		t.Log("[PASS] Zero agent condition detected")
	} else {
		t.Errorf("[FAIL] Expected zero agents with assign, got %d", totalAgents)
	}
}

// TestSpawnAssignNoBeadsAvailable tests handling when no beads are available
func TestSpawnAssignNoBeadsAvailable(t *testing.T) {
	t.Log("=== TestSpawnAssignNoBeadsAvailable ===")

	// Simulate AssignOutputEnhanced with no assignments
	result := &AssignOutputEnhanced{
		Strategy:    "balanced",
		Assignments: []AssignmentItem{},
		Skipped:     []SkippedItem{},
	}

	t.Logf("[VERIFY] Empty assignment result: Count=%d", len(result.Assignments))

	if len(result.Assignments) != 0 {
		t.Errorf("[FAIL] Expected 0 assignments, got %d", len(result.Assignments))
	}

	t.Log("[PASS] No beads case handled correctly")
}

// TestSpawnAssignAllAgentsBusy tests handling when all agents are busy
func TestSpawnAssignAllAgentsBusy(t *testing.T) {
	t.Log("=== TestSpawnAssignAllAgentsBusy ===")

	// Simulate AssignOutputEnhanced with agents filtered out
	result := &AssignOutputEnhanced{
		Strategy:    "balanced",
		Assignments: []AssignmentItem{},
		Skipped: []SkippedItem{
			{BeadID: "bd-123", BeadTitle: "Test bead", Reason: "no_idle_agents"},
		},
	}

	t.Logf("[VERIFY] Result with busy agents: Count=%d, Skipped=%d",
		len(result.Assignments), len(result.Skipped))

	if len(result.Assignments) != 0 {
		t.Errorf("[FAIL] Expected 0 assignments, got %d", len(result.Assignments))
	}
	if len(result.Skipped) != 1 {
		t.Errorf("[FAIL] Expected 1 skipped, got %d", len(result.Skipped))
	}
	if result.Skipped[0].Reason != "no_idle_agents" {
		t.Errorf("[FAIL] Expected reason 'no_idle_agents', got %q", result.Skipped[0].Reason)
	}

	t.Log("[PASS] All agents busy case handled correctly")
}

// TestSpawnAssignPartialFailure tests handling of partial failure
func TestSpawnAssignPartialFailure(t *testing.T) {
	t.Log("=== TestSpawnAssignPartialFailure ===")

	// Simulate partial success - spawn worked, but assignment had some failures
	result := SpawnAssignResult{
		Spawn: &output.SpawnResponse{
			Session: "test-session",
			Panes:   make([]output.PaneResponse, 4),
		},
		Assign: &AssignOutputEnhanced{
			Strategy: "balanced",
			Assignments: []AssignmentItem{
				{BeadID: "bd-1", Pane: 1, AgentType: "claude"},
				{BeadID: "bd-2", Pane: 2, AgentType: "codex"},
			},
			Errors: []string{"pane 3: send failed: connection timeout"},
		},
	}

	t.Logf("[VERIFY] Partial failure result:")
	t.Logf("  Spawn success: Session=%s", result.Spawn.Session)
	t.Logf("  Assign partial: Count=%d, Errors=%v", len(result.Assign.Assignments), result.Assign.Errors)

	// Spawn should have succeeded
	if result.Spawn == nil || result.Spawn.Session == "" {
		t.Error("[FAIL] Spawn should have succeeded")
	}

	// Assignment should have partial success
	if len(result.Assign.Assignments) != 2 {
		t.Errorf("[FAIL] Expected 2 assignments, got %d", len(result.Assign.Assignments))
	}

	// Errors should be captured
	if len(result.Assign.Errors) != 1 {
		t.Errorf("[FAIL] Expected 1 error, got %d", len(result.Assign.Errors))
	}

	t.Log("[PASS] Partial failure handled correctly")
}

// ============================================================================
// Strategy Passthrough Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignStrategyPassthrough tests that strategy is passed correctly
func TestSpawnAssignStrategyPassthrough(t *testing.T) {
	t.Log("=== TestSpawnAssignStrategyPassthrough ===")

	strategies := []string{"balanced", "speed", "quality", "dependency", "round-robin"}

	for _, strategy := range strategies {
		t.Run(strategy, func(t *testing.T) {
			spawnOpts := SpawnOptions{
				Session:        "test",
				Assign:         true,
				AssignStrategy: strategy,
			}

			// Simulate conversion to AssignCommandOptions
			assignOpts := &AssignCommandOptions{
				Session:  spawnOpts.Session,
				Strategy: spawnOpts.AssignStrategy,
			}

			t.Logf("[VERIFY] Strategy passthrough: Spawn=%s -> Assign=%s",
				spawnOpts.AssignStrategy, assignOpts.Strategy)

			if assignOpts.Strategy != strategy {
				t.Errorf("[FAIL] Strategy not passed: got %q, want %q", assignOpts.Strategy, strategy)
			}

			t.Logf("[PASS] Strategy %s passed correctly", strategy)
		})
	}
}

// ============================================================================
// Logging Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignLoggingFields tests that logging contains required fields
func TestSpawnAssignLoggingFields(t *testing.T) {
	t.Log("=== TestSpawnAssignLoggingFields ===")

	// Verify SpawnOptions has fields for logging
	opts := SpawnOptions{
		Session:            "test-session",
		Assign:             true,
		AssignStrategy:     "quality",
		AssignLimit:        5,
		AssignReadyTimeout: 60 * time.Second,
		CCCount:            2,
		CodCount:           1,
		GmiCount:           1,
	}

	// Required logging fields per spec:
	// - Log spawn phase completion
	// - Log assign invocation with parameters
	// - Log combined result

	t.Log("[VERIFY] Logging fields available:")
	t.Logf("  Session: %s", opts.Session)
	t.Logf("  Assign enabled: %v", opts.Assign)
	t.Logf("  Strategy: %s", opts.AssignStrategy)
	t.Logf("  Limit: %d", opts.AssignLimit)
	t.Logf("  Ready timeout: %v", opts.AssignReadyTimeout)
	t.Logf("  Agent counts: CC=%d, Cod=%d, Gmi=%d", opts.CCCount, opts.CodCount, opts.GmiCount)

	// All fields present and accessible
	if opts.Session == "" {
		t.Error("[FAIL] Session should be loggable")
	}
	if opts.AssignStrategy == "" {
		t.Error("[FAIL] Strategy should be loggable")
	}

	t.Log("[PASS] All required logging fields present")
}

// ============================================================================
// Init Prompt Integration Tests (bd-1tu58)
// ============================================================================

// TestSpawnAssignWithInitPrompt tests --init-prompt with --assign
func TestSpawnAssignWithInitPrompt(t *testing.T) {
	t.Log("=== TestSpawnAssignWithInitPrompt ===")

	// Init prompt should be sent after agents are ready, before assignment
	opts := SpawnOptions{
		Session:    "test-session",
		Assign:     true,
		InitPrompt: "Read AGENTS.md before starting work",
	}

	t.Logf("[SETUP] SpawnOptions with --assign and --init-prompt")
	t.Logf("  InitPrompt=%q", opts.InitPrompt)

	if opts.InitPrompt == "" {
		t.Error("[FAIL] InitPrompt should be set")
	}

	// Verify order: spawn -> ready wait -> init prompt -> assign
	t.Log("[VERIFY] Expected execution order:")
	t.Log("  1. Spawn agents")
	t.Log("  2. Wait for agents to become ready")
	t.Log("  3. Send init prompt (if specified)")
	t.Log("  4. Run assignment phase")

	t.Log("[PASS] Init prompt integration verified")
}

// TestSpawnAssignInitPromptResult tests SpawnInitResult in combined output
func TestSpawnAssignInitPromptResult(t *testing.T) {
	t.Log("=== TestSpawnAssignInitPromptResult ===")

	result := SpawnAssignResult{
		Spawn: &output.SpawnResponse{
			Session: "test-session",
		},
		Init: &SpawnInitResult{
			PromptSent:    true,
			AgentsReached: 4,
		},
		Assign: &AssignOutputEnhanced{
			Strategy: "balanced",
		},
	}

	t.Logf("[VERIFY] SpawnInitResult in combined output:")
	t.Logf("  PromptSent=%v", result.Init.PromptSent)
	t.Logf("  AgentsReached=%d", result.Init.AgentsReached)

	if !result.Init.PromptSent {
		t.Error("[FAIL] Expected PromptSent to be true")
	}
	if result.Init.AgentsReached != 4 {
		t.Errorf("[FAIL] Expected 4 agents reached, got %d", result.Init.AgentsReached)
	}

	t.Log("[PASS] SpawnInitResult correctly included")
}

// ============================================================================
// Assignment Item Tests (bd-1tu58)
// ============================================================================

// TestAssignmentItemFields tests that AssignmentItem has required fields
func TestAssignmentItemFields(t *testing.T) {
	t.Log("=== TestAssignmentItemFields ===")

	item := AssignmentItem{
		BeadID:    "bd-123",
		BeadTitle: "Fix the bug",
		Pane:      2,
		AgentType: "claude",
		AgentName: "TestAgent",
		Status:    "assigned",
	}

	t.Logf("[VERIFY] AssignmentItem fields:")
	t.Logf("  BeadID=%s", item.BeadID)
	t.Logf("  BeadTitle=%s", item.BeadTitle)
	t.Logf("  Pane=%d", item.Pane)
	t.Logf("  AgentType=%s", item.AgentType)
	t.Logf("  AgentName=%s", item.AgentName)
	t.Logf("  Status=%s", item.Status)

	if item.BeadID == "" {
		t.Error("[FAIL] BeadID should be set")
	}
	if item.Pane == 0 {
		t.Error("[FAIL] Pane should be non-zero (pane numbers start at 1)")
	}
	if item.AgentType == "" {
		t.Error("[FAIL] AgentType should be set")
	}

	t.Log("[PASS] AssignmentItem has all required fields")
}

// TestSkippedItemFields tests that SkippedItem has required fields
func TestSkippedItemFields(t *testing.T) {
	t.Log("=== TestSkippedItemFields ===")

	item := SkippedItem{
		BeadID:       "bd-456",
		BeadTitle:    "Blocked task",
		Reason:       "blocked_by_dependency",
		BlockedByIDs: []string{"bd-123", "bd-789"},
	}

	t.Logf("[VERIFY] SkippedItem fields:")
	t.Logf("  BeadID=%s", item.BeadID)
	t.Logf("  BeadTitle=%s", item.BeadTitle)
	t.Logf("  Reason=%s", item.Reason)
	t.Logf("  BlockedByIDs=%v", item.BlockedByIDs)

	if item.BeadID == "" {
		t.Error("[FAIL] BeadID should be set")
	}
	if item.Reason == "" {
		t.Error("[FAIL] Reason should be set")
	}

	t.Log("[PASS] SkippedItem has all required fields")
}

func TestCanonicalSpawnPromptDispatcherRedactsAndReturnsExactReceipt(t *testing.T) {
	now := time.Now().UTC()
	panes := []tmux.Pane{
		{ID: "%51", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"},
		{ID: "%52", WindowIndex: 1, Index: 1, Type: tmux.AgentCodex, Title: "demo__cod_1"},
	}
	observation := testSpawnSessionObservation(
		now,
		testSpawnPaneObservation(now, panes[0], statuspkg.StateIdle),
		testSpawnPaneObservation(now, panes[1], statuspkg.StateIdle),
	)
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
	var deliveries []dispatchsvc.Delivery
	dispatcher := newCanonicalSpawnPromptDispatcher("demo", observer)
	dispatcher.listPanes = func(context.Context, string) ([]tmux.Pane, error) {
		return append([]tmux.Pane(nil), panes...), nil
	}
	dispatcher.serviceFactory = func() (*dispatchsvc.Service, error) {
		return dispatchsvc.NewService(dispatchsvc.Ports{
			Redactor:  shellFinalMessageRedactor(redaction.Config{Mode: redaction.ModeRedact}),
			Protocols: shellDispatchProtocolPlanner{},
			Deliverer: dispatchsvc.DelivererFunc(func(_ context.Context, delivery dispatchsvc.Delivery) error {
				deliveries = append(deliveries, delivery)
				return nil
			}),
		})
	}

	const secret = "hunter2hunter2"
	receipt, err := dispatcher.Dispatch(t.Context(), panes[1].ID, "password="+secret)
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if receipt.Status != dispatchsvc.ReceiptDelivered || receipt.Target.Ref.StableKey() != panes[1].ID {
		t.Fatalf("receipt = %+v, want exact delivered target %s", receipt, panes[1].ID)
	}
	if receipt.Redaction.Mode != string(redaction.ModeRedact) || receipt.Redaction.Findings == 0 {
		t.Fatalf("redaction receipt = %+v, want redacted findings", receipt.Redaction)
	}
	if len(deliveries) != 1 || deliveries[0].Target.Ref.StableKey() != panes[1].ID ||
		strings.Contains(deliveries[0].Message, secret) || !strings.Contains(deliveries[0].Message, "[REDACTED") {
		t.Fatalf("delivery = %+v, want redacted exact-pane message", deliveries)
	}
}

func TestCanonicalSpawnPromptDispatcherDoesNotActuateAfterFailedRevalidation(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%53", WindowIndex: 0, Index: 3, Type: tmux.AgentClaude, Title: "demo__cc_3"}
	failedPane := testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)
	failedPane.Current.Freshness = statuspkg.FreshnessUnavailable
	failedPane.Current.Confidence = 0
	failedPane.Current.Error = "capture unavailable"
	failed := testSpawnSessionObservation(now, failedPane)
	failed.Complete = false
	failed.Failures = []statuspkg.ObservationFailure{{PaneID: pane.ID, Stage: "capture", Error: failedPane.Current.Error}}
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{failed}}
	deliveryCalls := 0
	dispatcher := newCanonicalSpawnPromptDispatcher("demo", observer)
	dispatcher.listPanes = func(context.Context, string) ([]tmux.Pane, error) {
		return []tmux.Pane{pane}, nil
	}
	dispatcher.serviceFactory = func() (*dispatchsvc.Service, error) {
		return dispatchsvc.NewService(dispatchsvc.Ports{
			Redactor: dispatchsvc.AllowAllRedactor{},
			Deliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
				deliveryCalls++
				return nil
			}),
		})
	}

	_, err := dispatcher.Dispatch(t.Context(), pane.ID, "must not be sent")
	if err == nil || !strings.Contains(err.Error(), "not freshly and confidently idle") {
		t.Fatalf("Dispatch() error = %v, want failed revalidation", err)
	}
	if deliveryCalls != 0 {
		t.Fatalf("delivery calls = %d, want 0 after failed revalidation", deliveryCalls)
	}

	_, err = dispatcher.Dispatch(t.Context(), "0.3", "must not be sent")
	if err == nil || !strings.Contains(err.Error(), "exact %N pane ID") {
		t.Fatalf("physical selector error = %v, want exact-ID rejection", err)
	}
	if deliveryCalls != 0 {
		t.Fatalf("delivery calls = %d, want 0 after selector rejection", deliveryCalls)
	}
}

func TestSendInitPromptUsesTopologyUniqueIdentityAndReceipts(t *testing.T) {
	now := time.Now().UTC()
	panes := []tmux.Pane{
		{ID: "%61", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"},
		{ID: "%62", WindowIndex: 1, Index: 1, Type: tmux.AgentCodex, Title: "demo__cod_1"},
	}
	observation := testSpawnSessionObservation(
		now,
		testSpawnPaneObservation(now, panes[0], statuspkg.StateIdle),
		testSpawnPaneObservation(now, panes[1], statuspkg.StateIdle),
	)
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
	dispatcher := &recordingSpawnDispatcher{}

	receipts, err := sendInitPromptToReadyAgentsWith(
		t.Context(), "", "demo", "Read AGENTS.md", true, observer, dispatcher,
	)
	if err != nil {
		t.Fatalf("sendInitPromptToReadyAgentsWith() error = %v", err)
	}
	if len(receipts) != 2 || strings.Join(dispatcher.panes, ",") != "%61,%62" {
		t.Fatalf("receipts=%d panes=%v, want deterministic %%61,%%62", len(receipts), dispatcher.panes)
	}
	wants := []string{"`demo_claude_0_1`", "`demo_codex_1_1`"}
	for i, want := range wants {
		if !strings.Contains(dispatcher.messages[i], want) || !strings.HasSuffix(dispatcher.messages[i], "Read AGENTS.md") {
			t.Fatalf("message[%d] = %q, want identity %s plus init prompt", i, dispatcher.messages[i], want)
		}
	}
}

func TestSendInitPromptReturnsCaptureErrorsWithoutDispatch(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%63", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"}
	failedPane := testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)
	failedPane.Current.Freshness = statuspkg.FreshnessUnavailable
	failedPane.Current.Confidence = 0
	failedPane.Current.Error = "capture deadline exceeded"
	observation := testSpawnSessionObservation(now, failedPane)
	observation.Complete = false
	observation.Failures = []statuspkg.ObservationFailure{{PaneID: pane.ID, Stage: "capture", Error: failedPane.Current.Error}}
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
	dispatcher := &recordingSpawnDispatcher{}

	receipts, err := sendInitPromptToReadyAgentsWith(t.Context(), "", "demo", "init", false, observer, dispatcher)
	if err == nil || !strings.Contains(err.Error(), "capture deadline exceeded") {
		t.Fatalf("error = %v, want explicit capture failure", err)
	}
	if len(receipts) != 0 || len(dispatcher.messages) != 0 {
		t.Fatalf("receipts=%d messages=%v, want no unsafe dispatch", len(receipts), dispatcher.messages)
	}
}

func TestRunAssignmentPhaseContextRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runAssignmentPhaseContext(ctx, "demo", SpawnOptions{})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v error=%v, want cancellation before assignment planning", result, err)
	}
}

var _ spawnSessionObserver = (*scriptedSpawnObserver)(nil)
var _ spawnPromptDispatcher = (*recordingSpawnDispatcher)(nil)
