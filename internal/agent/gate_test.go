package agent

import (
	"strings"
	"testing"
)

// gateTrustDialogFixture mirrors the live pane content from GH#230: an
// Antigravity pane parked on the first-run workspace-trust dialog.
const gateTrustDialogFixture = `  /data/projects/agyhealth

  Do you trust the contents of this project?

  Antigravity CLI requires permission to read, edit, and execute files here.

  > Yes, I trust this folder
    No, exit

    ↑/↓ Navigate · enter Confirm
`

// gateClaudeAuthErrorFixture models a Claude pane frozen on an
// authentication-error frame after an OAuth token race during multi-spawn.
const gateClaudeAuthErrorFixture = ` ✻ Welcome to Claude Code

 Authentication Error: OAuth token has expired.

 Please run /login to re-authenticate your session.

 ❯
`

// gateCodexLoginFixture models a Codex pane parked on the device-flow login
// gate waiting for a browser that will never open in a tmux pane.
const gateCodexLoginFixture = `> Sign in with ChatGPT

  Press Enter to open browser

  Browser didn't open? Use the URL below:
  https://auth.openai.com/device?code=ABCD-1234
`

func TestDetectInteractiveGate_AntigravityTrustDialog(t *testing.T) {
	t.Parallel()
	gate, ok := DetectInteractiveGate(gateTrustDialogFixture, 0)
	if !ok {
		t.Fatal("DetectInteractiveGate did not flag the Antigravity workspace-trust dialog")
	}
	if gate != "do you trust the contents of this project" {
		t.Errorf("gate = %q, want the trust-dialog phrase", gate)
	}
}

func TestDetectInteractiveGate_ClaudeAuthErrorFrame(t *testing.T) {
	t.Parallel()
	gate, ok := DetectInteractiveGate(gateClaudeAuthErrorFixture, 0)
	if !ok {
		t.Fatal("DetectInteractiveGate did not flag the Claude authentication-error frame")
	}
	// The tightened marker list deliberately excludes generic failure
	// vocabulary like "authentication error"; the gate-screen instruction
	// line is the load-bearing phrase.
	if gate != "please run /login" {
		t.Errorf("gate = %q, want %q", gate, "please run /login")
	}
}

func TestDetectInteractiveGate_CodexBrowserLoginGate(t *testing.T) {
	t.Parallel()
	gate, ok := DetectInteractiveGate(gateCodexLoginFixture, 0)
	if !ok {
		t.Fatal("DetectInteractiveGate did not flag the Codex browser login gate")
	}
	if !strings.Contains(gate, "browser didn't open") && !strings.Contains(gate, "press enter to open browser") {
		t.Errorf("gate = %q, want a login-gate phrase", gate)
	}
}

func TestDetectInteractiveGate_CurlyApostropheNormalized(t *testing.T) {
	t.Parallel()
	fixture := "  Browser didn’t open? Use the URL below:\n  https://example.com/device\n"
	if _, ok := DetectInteractiveGate(fixture, 0); !ok {
		t.Error("DetectInteractiveGate did not normalize the typographic apostrophe")
	}
}

func TestDetectInteractiveGate_QuotedPhraseWhileClaudeWorking(t *testing.T) {
	t.Parallel()
	// A working Claude agent QUOTING a gate phrase in its transcript must not
	// be flagged: the live spinner vetoes the match.
	fixture := "● I'll add \"authentication failed\" and \"please sign in\" to the gate scanner.\n\n" +
		"✻ Simmering… (esc to interrupt · 12s)\n\n ❯\n"
	if !ClaudeActivelyWorking(fixture, 0) {
		t.Fatal("fixture must read as actively working for this test to be meaningful")
	}
	if gate, ok := DetectInteractiveGate(fixture, 0); ok {
		t.Errorf("DetectInteractiveGate flagged a working agent quoting %q", gate)
	}
}

func TestDetectInteractiveGate_QuotedPhraseWhileCodexWorking(t *testing.T) {
	t.Parallel()
	fixture := "  Updating the docs to mention the \"login required\" gate screen.\n\n" +
		"• Working (esc to interrupt)\n"
	if !CodexActivelyWorking(fixture, 0) {
		t.Fatal("fixture must read as actively working for this test to be meaningful")
	}
	if gate, ok := DetectInteractiveGate(fixture, 0); ok {
		t.Errorf("DetectInteractiveGate flagged a working Codex agent quoting %q", gate)
	}
}

func TestDetectInteractiveGate_DialogOnlyInDeepScrollback(t *testing.T) {
	t.Parallel()
	// A recovered pane: the dialog was answered and 40+ lines of normal
	// output have since scrolled it out of the live tail.
	recovered := gateTrustDialogFixture + strings.Repeat("normal agent output line\n", gateTailBaseLines+25)
	if gate, ok := DetectInteractiveGate(recovered, 0); ok {
		t.Errorf("DetectInteractiveGate flagged a dialog deep in scrollback (%q)", gate)
	}
}

func TestDetectInteractiveGate_EmptyAndCleanOutput(t *testing.T) {
	t.Parallel()
	if _, ok := DetectInteractiveGate("", 0); ok {
		t.Error("DetectInteractiveGate flagged empty content")
	}
	if _, ok := DetectInteractiveGate("$ ls\nREADME.md\n$ ", 0); ok {
		t.Error("DetectInteractiveGate flagged a plain shell transcript")
	}
}

func TestDetectInteractiveGate_NarrowPaneWindowScales(t *testing.T) {
	t.Parallel()
	// On a narrow pane the same logical screen occupies more capture rows;
	// the width-adaptive window must still reach the gate phrase when extra
	// wrapped chrome rows sit below it.
	fixture := gateTrustDialogFixture + strings.Repeat("wrapped row\n", gateTailBaseLines+5)
	if _, ok := DetectInteractiveGate(fixture, 0); ok {
		t.Fatal("phrase should be outside the base window for this test to be meaningful")
	}
	if _, ok := DetectInteractiveGate(fixture, 26); !ok {
		t.Error("DetectInteractiveGate did not scale the window for a narrow pane")
	}
}
