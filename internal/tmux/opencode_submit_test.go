package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The opencode_*.txt fixtures are verbatim OpenCode 1.18.32 captures of a
// 200x50 pane (local paths scrubbed), taken while reproducing GH #333:
//   - opencode_idle.txt: fresh pane, empty composer with its placeholder
//   - opencode_draft.txt: a short one-line prompt pasted, not submitted
//   - opencode_paste_token.txt: a 108-line prompt pasted, collapsed to a token
//   - opencode_working.txt: that prompt submitted — echoed into the transcript
//     with the same bar prefix, a sidebar beside the composer, and the
//     "esc interrupt" working footer.

func TestOpencodeComposerDraft(t *testing.T) {
	tests := []struct {
		fixture   string
		wantFound bool
		want      string
	}{
		{"opencode_idle.txt", true, ""},
		{"opencode_draft.txt", true, "please run the focused tests and report back ENDMK60"},
		{"opencode_paste_token.txt", true, "[Pasted ~108 lines]"},
		// The transcript echo and the sidebar share rows/columns with the bar
		// glyph but are not the composer, which is empty after submission.
		{"opencode_working.txt", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			got, found := opencodeComposerDraft(ompFixture(t, tc.fixture))
			if found != tc.wantFound || got != tc.want {
				t.Fatalf("opencodeComposerDraft = (%q, %v), want (%q, %v)", got, found, tc.want, tc.wantFound)
			}
		})
	}

	if _, found := opencodeComposerDraft("$ ls\nREADME.md\n$ "); found {
		t.Fatal("a plain shell screen has no OpenCode composer")
	}
	if _, found := opencodeComposerDraft(ompFixture(t, "omp_nerd_draft.txt")); found {
		t.Fatal("another agent's TUI must not parse as an OpenCode composer")
	}
}

func TestOpencodeComposerHoldsPayload(t *testing.T) {
	message := "please run the focused tests and report back ENDMK60"
	if !opencodeComposerHoldsPayload(ompFixture(t, "opencode_draft.txt"), message) {
		t.Error("the delivered prompt sitting in the composer must count as held")
	}
	if opencodeComposerHoldsPayload(ompFixture(t, "opencode_draft.txt"), "an unrelated prompt") {
		t.Error("an unrelated draft must not count as the delivered payload")
	}
	if !opencodeComposerHoldsPayload(ompFixture(t, "opencode_paste_token.txt"), "MARCHING ORDER START\nstep 001") {
		t.Error("a collapsed paste token is the payload by construction")
	}
	if opencodeComposerHoldsPayload(ompFixture(t, "opencode_working.txt"), "MARCHING ORDER START\nstep 001: canonical_json\nENDMARKER333") {
		t.Error("a submitted prompt echoed into the transcript is not held in the composer")
	}
	if opencodeComposerHoldsPayload(ompFixture(t, "opencode_idle.txt"), "Ask anything") {
		t.Error("the empty-composer placeholder is not a payload")
	}
}

// TestNeedsBufferSend_Opencode is the GH #333 transport guard: typed bursts
// into OpenCode are dropped, so every non-empty payload is pasted.
func TestNeedsBufferSend_Opencode(t *testing.T) {
	for _, content := range []string{
		"ok",
		"/status",
		strings.Repeat("x", 400),
		"line one\nline two",
		strings.Repeat("step: canonical_json check the module\n", 108),
	} {
		if !needsBufferSend(AgentOpencode, content) {
			t.Errorf("needsBufferSend(opencode, %d bytes) = false, want bracketed paste", len(content))
		}
		if !needsBufferSend(AgentType("opencode"), content) {
			t.Errorf("needsBufferSend(opencode alias, %d bytes) = false, want bracketed paste", len(content))
		}
	}
	if needsBufferSend(AgentOpencode, "") {
		t.Error("an empty payload has nothing to paste")
	}
}

func TestRequireOpencodePayloadStaged_LivePane(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	ctx := context.Background()

	t.Run("empty composer means the payload was dropped", func(t *testing.T) {
		pane := startOmpEmulatorPane(t, fmt.Sprintf("clear; cat %q\nsleep 60\n", ompFixturePath(t, "opencode_idle.txt")))
		waitForPaneContains(t, pane, "Ask anything")
		err := DefaultClient.requireOpencodePayloadStaged(ctx, pane)
		if !errors.Is(err, ErrOpencodePayloadNotStaged) {
			t.Fatalf("requireOpencodePayloadStaged = %v, want ErrOpencodePayloadNotStaged", err)
		}
	})

	t.Run("staged paste token passes", func(t *testing.T) {
		pane := startOmpEmulatorPane(t, fmt.Sprintf("clear; cat %q\nsleep 60\n", ompFixturePath(t, "opencode_paste_token.txt")))
		waitForPaneContains(t, pane, "[Pasted ~108 lines]")
		if err := DefaultClient.requireOpencodePayloadStaged(ctx, pane); err != nil {
			t.Fatalf("requireOpencodePayloadStaged = %v, want nil", err)
		}
	})

	t.Run("unrecognized screen fails open", func(t *testing.T) {
		pane := startOmpEmulatorPane(t, "clear; echo 'no composer here'\nsleep 60\n")
		waitForPaneContains(t, pane, "no composer here")
		if err := DefaultClient.requireOpencodePayloadStaged(ctx, pane); err != nil {
			t.Fatalf("requireOpencodePayloadStaged = %v, want nil (fail open)", err)
		}
	})
}

// TestSendKeysForAgentDoubleEnter_OpencodeDroppedPayloadSendsNoEnter drives the
// real protocol into a pane whose composer stays empty (the TUI ignores the
// paste): the send must fail loudly instead of pressing Enter and reporting
// success, which is what GH #333 observed.
func TestSendKeysForAgentDoubleEnter_OpencodeDroppedPayloadSendsNoEnter(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	// The emulator exits on the first Enter it reads, so an Enter reaching it
	// replaces the idle screen with a marker.
	script := fmt.Sprintf("clear; cat %q\nstty -echo -icanon\nwhile IFS= read -r -s -n1 c; do if [ -z \"$c\" ]; then clear; echo ENTER-RECEIVED; fi; done\n",
		ompFixturePath(t, "opencode_idle.txt"))
	pane := startOmpEmulatorPane(t, script)
	waitForPaneContains(t, pane, "Ask anything")

	err := SendKeysForAgentDoubleEnterContext(context.Background(), pane, "please run the tests", AgentOpencode)
	if !errors.Is(err, ErrOpencodePayloadNotStaged) {
		t.Fatalf("double-Enter send = %v, want ErrOpencodePayloadNotStaged", err)
	}
	out, captureErr := CapturePaneVisible(pane)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	if strings.Contains(out, "ENTER-RECEIVED") {
		t.Fatal("an Enter was sent into a composer that never received the payload")
	}
}

func TestVerifyOpencodeSubmission_LivePane(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	ctx := context.Background()
	message := "MARCHING ORDER START\nstep 001: canonical_json check the module"

	t.Run("submitted prompt is confirmed without a rescue", func(t *testing.T) {
		pane := startOmpEmulatorPane(t, fmt.Sprintf("clear; cat %q\nsleep 60\n", ompFixturePath(t, "opencode_working.txt")))
		waitForPaneContains(t, pane, "esc interrupt")
		confirmed, rescued, err := VerifyOpencodeSubmissionContext(ctx, pane, message, 200)
		if err != nil || !confirmed || rescued {
			t.Fatalf("VerifyOpencodeSubmission = (confirmed=%v, rescued=%v, err=%v), want confirmed without rescue", confirmed, rescued, err)
		}
	})

	t.Run("stranded paste token gets one rescue Enter", func(t *testing.T) {
		script := fmt.Sprintf("clear; cat %q\nread -r _\nclear; cat %q\nsleep 60\n",
			ompFixturePath(t, "opencode_paste_token.txt"), ompFixturePath(t, "opencode_working.txt"))
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, "[Pasted ~108 lines]")
		confirmed, rescued, err := VerifyOpencodeSubmissionContext(ctx, pane, message, 200)
		if err != nil || !confirmed || !rescued {
			t.Fatalf("VerifyOpencodeSubmission = (confirmed=%v, rescued=%v, err=%v), want confirmed after rescue", confirmed, rescued, err)
		}
	})
}

func TestInterruptKeyForAgent_OpencodeUsesEscape(t *testing.T) {
	for _, oc := range []AgentType{AgentOpencode, "oc", "opencode", "opencode-cli"} {
		if got := InterruptKeyForAgent(oc); got != "Escape" {
			t.Fatalf("InterruptKeyForAgent(%q) = %q, want Escape", oc, got)
		}
		if got := InterruptKeyLabel(oc); got != "Escape" {
			t.Fatalf("InterruptKeyLabel(%q) = %q, want Escape", oc, got)
		}
	}
	for _, ctrlC := range []AgentType{AgentClaude, AgentCodex, AgentGrok, AgentAntigravity} {
		if got := InterruptKeyForAgent(ctrlC); got != "C-c" {
			t.Fatalf("InterruptKeyForAgent(%q) = %q, want C-c", ctrlC, got)
		}
	}
}

func TestInspectComposerAndVisibleForDelivery_AntigravityAndOpencode(t *testing.T) {
	agyIdle := "Antigravity\n\n  >\n\n  ? for shortcuts\n  Gemini 4 Argon (High)\n"
	if !AntigravityComposerVisible(agyIdle) || !ComposerVisibleForDelivery(agyIdle, AgentAntigravity) {
		t.Fatal("expected Antigravity idle composer to be visible")
	}
	if st := InspectComposer(agyIdle, AgentAntigravity); !st.MarkerVisible || st.HoldsText {
		t.Fatalf("InspectComposer(agyIdle) = %+v, want MarkerVisible=true HoldsText=false", st)
	}

	agyDraft := "Antigravity\n\n  > fix the race condition\n\n  ? for shortcuts\n  Gemini 4 Argon (High)\n"
	if st := InspectComposer(agyDraft, AgentAntigravity); !st.MarkerVisible || !st.HoldsText {
		t.Fatalf("InspectComposer(agyDraft) = %+v, want MarkerVisible=true HoldsText=true", st)
	}

	agyTrust := "Do you trust the contents of this project?\n> Yes, I trust this folder\n  No, exit\n↑/↓ Navigate · enter Confirm"
	if AntigravityComposerVisible(agyTrust) || InspectComposer(agyTrust, AgentAntigravity).MarkerVisible {
		t.Fatal("Antigravity trust dialog must not match composer")
	}

	ocIdle := ompFixture(t, "opencode_idle.txt")
	if st := InspectComposer(ocIdle, AgentOpencode); !st.MarkerVisible || st.HoldsText {
		t.Fatalf("InspectComposer(ocIdle) = %+v, want MarkerVisible=true HoldsText=false", st)
	}
	ocDraft := ompFixture(t, "opencode_draft.txt")
	if st := InspectComposer(ocDraft, AgentOpencode); !st.MarkerVisible || !st.HoldsText {
		t.Fatalf("InspectComposer(ocDraft) = %+v, want MarkerVisible=true HoldsText=true", st)
	}
}
