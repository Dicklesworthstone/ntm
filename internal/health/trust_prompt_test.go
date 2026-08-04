package health

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// agyTrustPromptCapture mirrors the live pane content from GH#230: an
// Antigravity pane parked on the first-run workspace-trust dialog.
const agyTrustPromptCapture = `  /data/projects/agyhealth

  Do you trust the contents of this project?

  Antigravity CLI requires permission to read, edit, and execute files here.

  > Yes, I trust this folder
    No, exit

    ↑/↓ Navigate · enter Confirm
`

func TestDetectBlockingPrompt_AntigravityTrustDialog(t *testing.T) {
	t.Parallel()
	issue, ok := detectBlockingPrompt(agyTrustPromptCapture, string(agent.AgentTypeAntigravity))
	if !ok {
		t.Fatal("detectBlockingPrompt did not flag the Antigravity workspace-trust dialog")
	}
	if issue.Type != "trust_prompt" {
		t.Errorf("issue.Type = %q, want %q", issue.Type, "trust_prompt")
	}
	if issue.Message == "" {
		t.Error("issue.Message must not be empty")
	}
}

func TestDetectBlockingPrompt_AgentTypeGated(t *testing.T) {
	t.Parallel()
	// A Claude pane that merely prints the dialog text (e.g. while editing a
	// detection test like this one) must not be flagged.
	if _, ok := detectBlockingPrompt(agyTrustPromptCapture, "cc"); ok {
		t.Error("detectBlockingPrompt flagged a non-Antigravity pane")
	}
	if _, ok := detectBlockingPrompt(agyTrustPromptCapture, "user"); ok {
		t.Error("detectBlockingPrompt flagged a user pane")
	}
}

func TestDetectBlockingPrompt_AnsweredDialogScrollsOut(t *testing.T) {
	t.Parallel()
	// Once the dialog is answered, subsequent output pushes it above the
	// live-tail window and it must stop being reported.
	answered := agyTrustPromptCapture + strings.Repeat("normal agent output line\n", blockingPromptLookbackLines+5)
	if _, ok := detectBlockingPrompt(answered, string(agent.AgentTypeAntigravity)); ok {
		t.Error("detectBlockingPrompt flagged a dialog that scrolled out of the live tail")
	}
}

func TestDetectBlockingPrompt_PartialMarkersDoNotMatch(t *testing.T) {
	t.Parallel()
	partial := "  Do you trust the contents of this project?\n  something unrelated\n"
	if _, ok := detectBlockingPrompt(partial, string(agent.AgentTypeAntigravity)); ok {
		t.Error("detectBlockingPrompt matched on a single marker; all markers must be required")
	}
}

func TestCalculateStatus_TrustPromptIsError(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "trust_prompt", Message: "blocked"}},
	}
	if got := calculateStatus(h); got != StatusError {
		t.Errorf("calculateStatus(trust_prompt) = %v, want StatusError", got)
	}
}

func TestAnalyzeAgentHealth_AntigravityTrustPromptEndToEnd(t *testing.T) {
	t.Parallel()
	pa := tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%2", Index: 2, Type: tmux.AgentAntigravity},
	}
	got := analyzeAgentHealth(newAgentHealth(pa), pa, agyTrustPromptCapture)
	if got.Status != StatusError {
		t.Errorf("Status = %v, want StatusError for a trust-prompt-blocked pane", got.Status)
	}
	found := false
	for _, issue := range got.Issues {
		if issue.Type == "trust_prompt" {
			found = true
		}
	}
	if !found {
		t.Errorf("Issues = %+v, want a trust_prompt issue", got.Issues)
	}
}
