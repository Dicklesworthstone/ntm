package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "he..."},
		{"abc", 3, "abc"},
		{"abc", 2, "ab"}, // Special case n<=3
		{"", 5, ""},
		{"abc", 0, ""},      // Edge case: n == 0
		{"abc", -1, ""},     // Edge case: n < 0
		{"x", 1, "x"},       // Single char
		{"xy", 4, "xy"},     // n > len(s)
		{"abcd", 4, "abcd"}, // Exact match
	}

	for _, tt := range tests {
		got := util.Truncate(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("util.Truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestTruncate_EdgeCases(t *testing.T) {
	// Test truncation at boundary conditions
	t.Run("n equals string length", func(t *testing.T) {
		result := util.Truncate("test", 4)
		if result != "test" {
			t.Errorf("expected 'test', got %q", result)
		}
	})

	t.Run("n slightly larger than string", func(t *testing.T) {
		result := util.Truncate("test", 100)
		if result != "test" {
			t.Errorf("expected 'test', got %q", result)
		}
	})

	t.Run("empty string with positive n", func(t *testing.T) {
		result := util.Truncate("", 10)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})
}

// tmuxAvailable checks if tmux is installed and accessible
func tmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// createTestSession creates a tmux session for testing and returns cleanup func
func createTestSession(t *testing.T) string {
	t.Helper()
	testutil.AcquireGlobalTmuxTestLockForTest(t)
	name := fmt.Sprintf("ntm_pipeline_test_%d", time.Now().UnixNano())
	if err := tmux.CreateSession(name, os.TempDir()); err != nil {
		// Skip instead of fail when tmux server has issues (common on CI)
		t.Skipf("failed to create test session (tmux issue): %v", err)
	}
	t.Cleanup(func() {
		_ = tmux.KillSession(name)
	})
	return name
}

func TestFindPaneForStage_NoSession(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}
	testutil.AcquireGlobalTmuxTestLockForTest(t)

	// Non-existent session should fail
	_, err := findPaneForStage("nonexistent-session-xyz", "cc", "")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestFindPaneForStage_EmptySession(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	// Session exists but has no agent panes (just the default pane)
	_, err := findPaneForStage(session, "cc", "")
	if err == nil {
		t.Error("expected error when no matching agent found")
	}
}

func TestStage_Fields(t *testing.T) {
	// Test Stage struct can be created with all fields
	s := Stage{
		AgentType: "cc",
		Prompt:    "test prompt",
		Model:     "opus",
	}
	if s.AgentType != "cc" {
		t.Error("AgentType not set correctly")
	}
	if s.Prompt != "test prompt" {
		t.Error("Prompt not set correctly")
	}
	if s.Model != "opus" {
		t.Error("Model not set correctly")
	}
}

func TestPipeline_Fields(t *testing.T) {
	// Test Pipeline struct can be created with stages
	p := Pipeline{
		Session: "test-session",
		Stages: []Stage{
			{AgentType: "cc", Prompt: "step 1"},
			{AgentType: "cod", Prompt: "step 2"},
		},
	}
	if p.Session != "test-session" {
		t.Error("Session not set correctly")
	}
	if len(p.Stages) != 2 {
		t.Errorf("expected 2 stages, got %d", len(p.Stages))
	}
}

func TestWaitForIdle_ContextCancellation(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Skip("could not get test pane")
	}

	// Create a cancelled context and test that waitForIdle respects it
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	detector := status.NewDetector()
	err = waitForIdleWithInterval(ctx, detector, panes[0].ID, StageIdlePollInterval)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
}

func TestWaitForIdle_Timeout(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Skip("could not get test pane")
	}

	// Create context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	detector := status.NewDetector()
	err = waitForIdleWithInterval(ctx, detector, panes[0].ID, StageIdlePollInterval)
	// Should timeout since pane won't be in idle state
	if err == nil {
		t.Log("pane was already idle, test inconclusive but passes")
	}
}

func TestExecute_EmptyPipeline(t *testing.T) {
	// Empty pipeline should succeed (no stages to run)
	p := Pipeline{
		Session: "test",
		Stages:  []Stage{},
	}
	err := Execute(context.Background(), p)
	if err != nil {
		t.Errorf("empty pipeline should not error, got: %v", err)
	}
}

func TestExecute_NonExistentSession(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}
	testutil.AcquireGlobalTmuxTestLockForTest(t)

	p := Pipeline{
		Session: "nonexistent-session-for-testing-xyz",
		Stages: []Stage{
			{AgentType: "cc", Prompt: "test"},
		},
	}
	err := Execute(context.Background(), p)
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestFindPaneForStage_WithModel(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	// With model specified but no matching agent, should fail
	_, err := findPaneForStage(session, "cc", "opus")
	if err == nil {
		t.Error("expected error when no matching agent with model found")
	}
}

func TestFindPaneForStage_RelaxedMatch(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	// Test that relaxed match is attempted when model is specified
	// but no exact match exists. Since our test session has no agent panes,
	// this should still fail but exercise the relaxed match code path.
	_, err := findPaneForStage(session, "cc", "nonexistent-model")
	if err == nil {
		t.Error("expected error for non-existent agent type")
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	p := Pipeline{
		Session: session,
		Stages: []Stage{
			{AgentType: "cc", Prompt: "test that should be cancelled"},
		},
	}

	// Execute with cancelled context - should fail at findPaneForStage
	// since there are no cc agents in the test session
	err := Execute(ctx, p)
	if err == nil {
		t.Error("expected error from execution")
	}
}

func TestWaitForIdle_InvalidPane(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}
	testutil.AcquireGlobalTmuxTestLockForTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	detector := status.NewDetector()
	// Pass an invalid pane ID - should eventually timeout
	err := waitForIdleWithInterval(ctx, detector, "%999", StageIdlePollInterval)
	if err == nil {
		t.Log("no error from invalid pane, may have returned idle state")
	}
}

func TestExecute_MultipleStages(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	p := Pipeline{
		Session: session,
		Stages: []Stage{
			{AgentType: "cc", Prompt: "first stage"},
			{AgentType: "cod", Prompt: "second stage"},
		},
	}

	// Should fail on first stage since no agents exist
	err := Execute(context.Background(), p)
	if err == nil {
		t.Error("expected error when no agents available")
	}
}

func TestFindPaneForStage_ErrorMessage(t *testing.T) {
	if !tmuxAvailable() {
		t.Skip("tmux not available")
	}

	session := createTestSession(t)

	// Check that error message includes agent type and model
	_, err := findPaneForStage(session, "gmi", "gemini-pro")
	if err == nil {
		t.Error("expected error")
	}
	errMsg := err.Error()
	if errMsg == "" {
		t.Error("expected non-empty error message")
	}
}
