package robot

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// robotTrustDialogCapture mirrors the Antigravity workspace-trust dialog
// (GH#230): the pane is alive and classifies idle, but every keystroke-free
// health surface must report it blocked, not OK.
const robotTrustDialogCapture = `  /data/projects/agyhealth

  Do you trust the contents of this project?

  Antigravity CLI requires permission to read, edit, and execute files here.

  > Yes, I trust this folder
    No, exit

    ↑/↓ Navigate · enter Confirm
`

// robotClaudeAuthCapture models a Claude pane frozen on an authentication
// error frame after an OAuth token race during multi-spawn.
const robotClaudeAuthCapture = ` ✻ Welcome to Claude Code

 Authentication Error: OAuth token has expired.

 Please run /login to re-authenticate your session.

 ❯
`

func idleWorkStatus() PaneWorkStatus {
	return PaneWorkStatus{
		IsIdle:         true,
		Recommendation: string(agent.RecommendSafeToRestart),
		IndicatorBasis: "idle_prompt",
	}
}

func TestApplyInteractiveGateOverride_TrustDialog(t *testing.T) {
	t.Parallel()
	status := idleWorkStatus()
	if !applyInteractiveGateOverride(&status, agent.AgentTypeAntigravity, robotTrustDialogCapture, 0) {
		t.Fatal("applyInteractiveGateOverride did not fire on the trust dialog")
	}
	if status.IsIdle {
		t.Error("IsIdle = true, a gate-blocked pane must not be advertised as feedable")
	}
	if status.Recommendation != string(agent.RecommendErrorState) {
		t.Errorf("Recommendation = %q, want %q", status.Recommendation, agent.RecommendErrorState)
	}
	if status.IndicatorBasis != "interactive_gate" {
		t.Errorf("IndicatorBasis = %q, want %q", status.IndicatorBasis, "interactive_gate")
	}
	if !strings.Contains(status.RecommendationReason, "do you trust the contents of this project") {
		t.Errorf("RecommendationReason = %q, want the gate text inside it", status.RecommendationReason)
	}
}

func TestApplyInteractiveGateOverride_ClaudeAuthFrame(t *testing.T) {
	t.Parallel()
	status := idleWorkStatus()
	if !applyInteractiveGateOverride(&status, agent.AgentTypeClaudeCode, robotClaudeAuthCapture, 0) {
		t.Fatal("applyInteractiveGateOverride did not fire on the auth-error frame")
	}
	if !strings.Contains(status.RecommendationReason, "authentication error") {
		t.Errorf("RecommendationReason = %q, want the gate text inside it", status.RecommendationReason)
	}
}

func TestApplyInteractiveGateOverride_Negatives(t *testing.T) {
	t.Parallel()

	t.Run("working pane keeps verdict", func(t *testing.T) {
		t.Parallel()
		status := idleWorkStatus()
		status.IsWorking = true
		status.IsIdle = false
		if applyInteractiveGateOverride(&status, agent.AgentTypeClaudeCode, robotClaudeAuthCapture, 0) {
			t.Error("override fired on a working pane")
		}
	})

	t.Run("rate-limited pane keeps WAIT precedence", func(t *testing.T) {
		t.Parallel()
		status := idleWorkStatus()
		status.IsRateLimited = true
		if applyInteractiveGateOverride(&status, agent.AgentTypeClaudeCode, robotClaudeAuthCapture, 0) {
			t.Error("override fired on a rate-limited pane")
		}
	})

	t.Run("user and unknown panes skipped", func(t *testing.T) {
		t.Parallel()
		for _, at := range []agent.AgentType{agent.AgentTypeUser, agent.AgentTypeUnknown} {
			status := idleWorkStatus()
			if applyInteractiveGateOverride(&status, at, robotTrustDialogCapture, 0) {
				t.Errorf("override fired on a %q pane", at)
			}
		}
	})

	t.Run("working agent quoting a gate phrase not flagged", func(t *testing.T) {
		t.Parallel()
		capture := "● Adding \"login required\" to the scanner.\n\n✻ Simmering… (esc to interrupt · 9s)\n"
		status := idleWorkStatus()
		if applyInteractiveGateOverride(&status, agent.AgentTypeClaudeCode, capture, 0) {
			t.Error("override fired while working chrome was visible")
		}
	})

	t.Run("dialog deep in scrollback not flagged", func(t *testing.T) {
		t.Parallel()
		recovered := robotTrustDialogCapture + strings.Repeat("normal agent output line\n", 60)
		status := idleWorkStatus()
		if applyInteractiveGateOverride(&status, agent.AgentTypeAntigravity, recovered, 0) {
			t.Error("override fired on a recovered pane with the dialog only in deep scrollback")
		}
	})
}

func TestCollectIssues_InteractiveGateSurfacesGateText(t *testing.T) {
	t.Parallel()
	reason := `Blocked on interactive gate screen ("authentication error"); needs a keystroke before the pane can accept work`
	localState := &PaneWorkStatus{
		Recommendation:       string(agent.RecommendErrorState),
		IndicatorBasis:       "interactive_gate",
		RecommendationReason: reason,
		Confidence:           0.9,
	}
	issues := CollectIssues(localState, nil)
	found := false
	for _, issue := range issues {
		if issue == reason {
			found = true
		}
	}
	if !found {
		t.Errorf("CollectIssues = %v, want the gate reason %q", issues, reason)
	}
}

func TestDeriveHealthRecommendation_InteractiveGate(t *testing.T) {
	t.Parallel()
	reason := `Blocked on interactive gate screen ("do you trust the contents of this project"); needs a keystroke before the pane can accept work`
	localState := &PaneWorkStatus{
		Recommendation:       string(agent.RecommendErrorState),
		IndicatorBasis:       "interactive_gate",
		RecommendationReason: reason,
		Confidence:           0.9,
	}
	score := CalculateHealthScore(localState, nil)
	if score >= 70 {
		t.Errorf("CalculateHealthScore = %d, a gate-blocked pane must not score healthy (>=70)", score)
	}
	rec, gotReason := DeriveHealthRecommendation(localState, nil, score)
	if rec != RecommendRestartUrgent {
		t.Errorf("recommendation = %q, want %q", rec, RecommendRestartUrgent)
	}
	if gotReason != reason {
		t.Errorf("reason = %q, want the gate text %q", gotReason, reason)
	}
}
