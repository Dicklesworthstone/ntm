package coordinator

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// staleErrorScrollback builds a pane capture whose error line sits outside
// the robot classifier's 15-line live tail but inside the static detector's
// 50-line scan window, followed by `tailLines` lines of working output.
func staleErrorScrollback(errorLine string, tailLines int) string {
	var b strings.Builder
	b.WriteString(errorLine)
	b.WriteString("\n")
	for i := 0; i < 20; i++ {
		b.WriteString("    filler line that scrolled past the live window\n")
	}
	for i := 0; i < tailLines; i++ {
		b.WriteString("    working output line\n")
	}
	return b.String()
}

// liveTailErrorScrollback builds a pane capture whose error line is the last
// line, inside the robot classifier's live tail.
func liveTailErrorScrollback(errorLine string) string {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString("    working output line\n")
	}
	b.WriteString(errorLine)
	b.WriteString("\n")
	return b.String()
}

// paneObservation builds a fresh PaneObservation with the given static state
// and raw output, for driving resultFromPaneObservation without a tmux server.
func paneObservation(paneID, agentType string, static status.AgentState, rawOutput string) status.PaneObservation {
	return status.PaneObservation{
		Pane:      tmux.PaneRef{ID: paneID},
		AgentType: agentType,
		Current: status.StateObservation{
			Status:     status.AgentStatus{State: static},
			Freshness:  status.FreshnessFresh,
			Confidence: 0.95,
		},
		RawOutput: rawOutput,
	}
}

// TestResolveStatus pins the decision function directly: the robot verdict
// downgrades a static StateError, and non-error static verdicts are left
// untouched so Claude, Codex and pi panes keep their short-circuit behaviour.
func TestResolveStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		static     robot.AgentState
		robotState robot.AgentState
		want       robot.AgentState
	}{
		{"static error downgraded to generating", robot.StateError, robot.StateGenerating, robot.StateGenerating},
		{"static error downgraded to thinking", robot.StateError, robot.StateThinking, robot.StateThinking},
		{"static error downgraded to waiting", robot.StateError, robot.StateWaiting, robot.StateWaiting},
		{"static error kept when robot also errors", robot.StateError, robot.StateError, robot.StateError},
		{"static error kept when robot is unknown", robot.StateError, robot.StateUnknown, robot.StateUnknown},
		{"static generating untouched by robot thinking", robot.StateGenerating, robot.StateThinking, robot.StateGenerating},
		{"static waiting untouched by robot generating", robot.StateWaiting, robot.StateGenerating, robot.StateWaiting},
		{"static unknown untouched by robot waiting", robot.StateUnknown, robot.StateWaiting, robot.StateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveStatus(tt.static, tt.robotState)
			if got != tt.want {
				t.Errorf("resolveStatus(%s, %s) = %s, want %s", tt.static, tt.robotState, got, tt.want)
			}
		})
	}
}

// TestResultFromPaneObservation_StaleErrorDowngradedWhenProgressing is the
// defect fixture: an exposed-type pane whose scrollback holds a stale error
// outside the live tail, and whose output is growing, must be downgraded from
// the static StateError to a working verdict and offered work.
func TestResultFromPaneObservation_StaleErrorDowngradedWhenProgressing(t *testing.T) {
	m := NewAgentMonitor("test-session", nil, "/tmp/test")

	// First observation establishes the classifier baseline: a working pane
	// with no error yet.
	_ = m.resultFromPaneObservation(paneObservation("%1", "gemini", status.StateWorking, "working line 1\nworking line 2"))

	// Second observation: the transient error has scrolled out of the live
	// tail and the pane kept producing output. The static detector flags the
	// stale error; the robot classifier sees growth and drops it.
	time.Sleep(10 * time.Millisecond)
	result := m.resultFromPaneObservation(paneObservation("%1", "gemini", status.StateError, staleErrorScrollback("invalid_api_key", 5)))

	if result.Status == robot.StateError {
		t.Fatalf("progressing pane with stale error still classified ERROR")
	}
	if result.Status == robot.StateUnknown {
		t.Fatalf("progressing pane downgraded to UNKNOWN; want a working verdict")
	}
	if !result.Healthy {
		t.Fatalf("progressing pane should be healthy, got %+v", result)
	}

	// The downgraded verdict must reach the dispatch gate: a non-error,
	// healthy, current pane is an assignment candidate.
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.config.AssignOnlyIdle = false
	c.agents["%1"] = &AgentState{
		PaneID: "%1", Status: result.Status, Healthy: result.Healthy,
		ObservedAt: time.Now(), ObservationFreshness: status.FreshnessFresh,
	}
	candidates := c.getAssignmentCandidates()
	if len(candidates) != 1 || candidates[0].PaneID != "%1" {
		t.Fatalf("progressing pane not offered work: %+v", candidates)
	}
}

// TestResultFromPaneObservation_LiveTailErrorKept is the inverse: a fresh
// error inside the live tail must keep the static StateError, so the pane is
// withheld and the agent.error event still fires.
func TestResultFromPaneObservation_LiveTailErrorKept(t *testing.T) {
	m := NewAgentMonitor("test-session", nil, "/tmp/test")

	result := m.resultFromPaneObservation(paneObservation("%1", "gemini", status.StateError, liveTailErrorScrollback("invalid_api_key")))

	if result.Status != robot.StateError {
		t.Fatalf("live-tail error downgraded to %s; want ERROR", result.Status)
	}
	if result.Healthy {
		t.Fatalf("live-tail error pane should be unhealthy, got %+v", result)
	}
}

// TestResultFromPaneObservation_StuckPaneKeepsError ensures a genuinely stuck
// pane — a stale error and no output growth between captures — is still
// withheld: the fix must not turn the gate off, only stop it firing on
// recovered panes.
func TestResultFromPaneObservation_StuckPaneKeepsError(t *testing.T) {
	m := NewAgentMonitor("test-session", nil, "/tmp/test")

	content := staleErrorScrollback("invalid_api_key", 2)
	_ = m.resultFromPaneObservation(paneObservation("%1", "gemini", status.StateError, content))
	time.Sleep(10 * time.Millisecond)
	result := m.resultFromPaneObservation(paneObservation("%1", "gemini", status.StateError, content))

	if result.Status != robot.StateError {
		t.Fatalf("stuck pane with stale error classified %s; want ERROR", result.Status)
	}
}

// TestResultFromPaneObservation_ClaudeCodexPiUnchanged is the regression
// surface: Claude, Codex and pi panes short-circuit in the static detector, so
// their non-error verdicts must be left untouched by the robot override.
func TestResultFromPaneObservation_ClaudeCodexPiUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		agentType  string
		static     status.AgentState
		rawOutput  string
		wantStatus robot.AgentState
	}{
		{
			name:       "claude working",
			agentType:  "claude",
			static:     status.StateWorking,
			rawOutput:  "✻ Churning… (ctrl+c to interrupt · 4s)\n────────────\n❯ \n────────────",
			wantStatus: robot.StateGenerating,
		},
		{
			name:       "claude idle",
			agentType:  "claude",
			static:     status.StateIdle,
			rawOutput:  "completed\n────────────\n❯ \n────────────",
			wantStatus: robot.StateWaiting,
		},
		{
			name:       "codex working",
			agentType:  "codex",
			static:     status.StateWorking,
			rawOutput:  "• Working (4m 51s • esc to interrupt)\n› Improve documentation i\n  gpt-5.4 xhigh · 52% left",
			wantStatus: robot.StateGenerating,
		},
		{
			name:       "codex idle",
			agentType:  "codex",
			static:     status.StateIdle,
			rawOutput:  "completed\n────────────\n› \n────────────",
			wantStatus: robot.StateWaiting,
		},
		{
			name:       "pi working",
			agentType:  "pi",
			static:     status.StateWorking,
			rawOutput:  "⠴ Working...\n↑100k ↓958 13.0%/262k (auto)",
			wantStatus: robot.StateGenerating,
		},
		{
			name:       "pi idle",
			agentType:  "pi",
			static:     status.StateIdle,
			rawOutput:  "↑100k ↓958 13.0%/262k (auto)",
			wantStatus: robot.StateWaiting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewAgentMonitor("test-session", nil, "/tmp/test")
			result := m.resultFromPaneObservation(paneObservation("%1", tt.agentType, tt.static, tt.rawOutput))
			if result.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (unchanged from static)", result.Status, tt.wantStatus)
			}
		})
	}
}

// TestGetAssignmentCandidates_SkipsErroredAgent is the regression guard that
// the dispatch gate is not quietly disabled: a genuinely errored agent is
// still withheld even when queueing is enabled.
func TestGetAssignmentCandidates_SkipsErroredAgent(t *testing.T) {
	c := New("coordinator-error-gate", t.TempDir(), nil, "CoordinatorAgent")
	now := time.Now().UTC()
	c.config.IdleThreshold = 0
	c.config.AssignOnlyIdle = false
	c.agents["%1"] = &AgentState{
		PaneID: "%1", Status: robot.StateError, Healthy: false, SafeToDispatch: false,
		ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.agents["%2"] = &AgentState{
		PaneID: "%2", Status: robot.StateGenerating, Healthy: true, SafeToDispatch: false,
		ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}

	candidates := c.getAssignmentCandidates()
	if len(candidates) != 1 || candidates[0].PaneID != "%2" {
		t.Fatalf("candidates = %+v, want only %%2 (errored %%1 skipped)", candidates)
	}
}
