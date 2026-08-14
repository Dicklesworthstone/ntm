package health

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ccAuthErrorCapture models a Claude pane frozen on an authentication-error
// frame after an OAuth token race during multi-spawn (bd-jf22c): the process
// is alive, the pane looks active, and no work will ever happen.
const ccAuthErrorCapture = ` ✻ Welcome to Claude Code

 Authentication Error: OAuth token has expired.

 Please run /login to re-authenticate your session.

 ❯
`

// codLoginGateCapture models a Codex pane parked on the device-flow login
// gate waiting for a browser that will never open inside tmux.
const codLoginGateCapture = `> Sign in with ChatGPT

  Press Enter to open browser

  Browser didn't open? Use the URL below:
  https://auth.openai.com/device?code=ABCD-1234
`

func TestDetectInteractiveGateIssue_ClaudeAuthError(t *testing.T) {
	t.Parallel()
	issue, ok := detectInteractiveGateIssue(ccAuthErrorCapture, "cc", 0)
	if !ok {
		t.Fatal("detectInteractiveGateIssue did not flag the Claude auth-error frame")
	}
	if issue.Type != "interactive_gate" {
		t.Errorf("issue.Type = %q, want %q", issue.Type, "interactive_gate")
	}
	if !strings.Contains(issue.Message, "authentication error") {
		t.Errorf("issue.Message = %q, want the matched gate phrase inside it", issue.Message)
	}
}

func TestDetectInteractiveGateIssue_UserAndUnknownPanesSkipped(t *testing.T) {
	t.Parallel()
	// A user shell grepping this repo (or a pane of unknown type) prints gate
	// phrases legitimately and must never be flagged.
	for _, agentType := range []string{"user", "unknown", ""} {
		if _, ok := detectInteractiveGateIssue(ccAuthErrorCapture, agentType, 0); ok {
			t.Errorf("detectInteractiveGateIssue flagged a %q pane", agentType)
		}
	}
}

func TestAnalyzeAgentHealth_ClaudeAuthGateIsError(t *testing.T) {
	t.Parallel()
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%3", Index: 3, Type: tmux.AgentClaude},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, ccAuthErrorCapture)
	if got.Status != StatusError {
		t.Errorf("Status = %v, want StatusError for an auth-gate-blocked pane", got.Status)
	}
	if !hasIssueType(got.Issues, "interactive_gate") {
		t.Errorf("Issues = %+v, want an interactive_gate issue", got.Issues)
	}
}

func TestAnalyzeAgentHealth_CodexLoginGateIsError(t *testing.T) {
	t.Parallel()
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%4", Index: 4, Type: tmux.AgentCodex},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, codLoginGateCapture)
	if got.Status != StatusError {
		t.Errorf("Status = %v, want StatusError for a login-gate-blocked pane", got.Status)
	}
	if !hasIssueType(got.Issues, "interactive_gate") {
		t.Errorf("Issues = %+v, want an interactive_gate issue", got.Issues)
	}
}

func TestAnalyzeAgentHealth_WorkingAgentQuotingGatePhraseNotFlagged(t *testing.T) {
	t.Parallel()
	// An actively working Claude agent whose transcript quotes a gate phrase
	// must keep its healthy verdict: the live spinner vetoes the match.
	capture := "● Adding \"authentication failed\" to the gate scanner patterns.\n\n" +
		"✻ Simmering… (esc to interrupt · 12s)\n\n ❯\n"
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%5", Index: 5, Type: tmux.AgentClaude},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, capture)
	if hasIssueType(got.Issues, "interactive_gate") {
		t.Errorf("Issues = %+v, working agent quoting a gate phrase was flagged", got.Issues)
	}
}

func TestAnalyzeAgentHealth_RecoveredPaneDialogInDeepScrollback(t *testing.T) {
	t.Parallel()
	// The dialog was answered; dozens of lines of normal output have pushed
	// it far above the live tail. The pane must not be re-flagged.
	recovered := agyTrustPromptCapture + strings.Repeat("normal agent output line\n", 60)
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%6", Index: 6, Type: tmux.AgentAntigravity},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, recovered)
	if hasIssueType(got.Issues, "interactive_gate") || hasIssueType(got.Issues, "trust_prompt") {
		t.Errorf("Issues = %+v, recovered pane was flagged from deep scrollback", got.Issues)
	}
}

func TestAnalyzeAgentHealth_TrustPromptNotDoubleReported(t *testing.T) {
	t.Parallel()
	// The Antigravity dialog matches both the agent-type-gated trust_prompt
	// signature and the generic gate detector; only one issue may be emitted.
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%7", Index: 7, Type: tmux.AgentAntigravity},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, agyTrustPromptCapture)
	if !hasIssueType(got.Issues, "trust_prompt") {
		t.Fatalf("Issues = %+v, want a trust_prompt issue", got.Issues)
	}
	if hasIssueType(got.Issues, "interactive_gate") {
		t.Errorf("Issues = %+v, trust dialog reported twice (trust_prompt + interactive_gate)", got.Issues)
	}
}

func TestCalculateStatus_InteractiveGateIsError(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "interactive_gate", Message: "blocked"}},
	}
	if got := calculateStatus(h); got != StatusError {
		t.Errorf("calculateStatus(interactive_gate) = %v, want StatusError", got)
	}
}
