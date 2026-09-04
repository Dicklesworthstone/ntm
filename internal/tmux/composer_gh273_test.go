package tmux

import "testing"

// Captured frames from GH#273 (codex-cli 0.150.1/0.151.0). Codex swaps the
// composer prompt glyph with the reasoning effort — "›" (U+203A) everywhere
// except effort ultra, which renders "»" (U+00BB) — and its update dialog
// reuses "›" as a selection cursor on the highlighted option row.
const (
	codexUltraIdleFrame = "• You have 1 usage limit reset available. Run /usage to use one.\n" +
		"» Ask Codex to do anything\n" +
		"  gpt-5.6-sol ultra fast · /home/user/project\n"

	codexXhighIdleFrame = "› Ask Codex to do anything\n" +
		"  gpt-5.6-sol xhigh fast · /home/user/project\n"

	codexUpdateDialogFrame = "Codex update available!\n" +
		"\n" +
		"› 1. Update now (runs `bun install -g @openai/codex`)\n" +
		"  2. Skip\n" +
		"  3. Skip until next version\n" +
		"  Press enter to continue\n"
)

// GH#273 root causes 1+2: an idle codex pane at reasoning effort ultra draws
// its composer prompt as "»", so a delivery gate that only knows "›" refused
// every send to an ultra pane. Both glyphs must read as a visible composer;
// the update dialog (root cause 3) must NOT — its "›" is a selection cursor,
// and accepting it made a robot send's Enter select "Update now" and
// self-update the agent out from under the session.
func TestComposerVisibleForDelivery_CodexGlyphsAndDialogs(t *testing.T) {
	markers := composerMarkersForAgent(AgentCodex)

	cases := []struct {
		name    string
		capture string
		want    bool
	}{
		{"ultra idle composer (») is ready", codexUltraIdleFrame, true},
		{"xhigh idle composer (›) is still ready", codexXhighIdleFrame, true},
		{"update dialog is NOT a ready composer", codexUpdateDialogFrame, false},
		{"empty ultra composer with no hint", "transcript\n» \n  footer\n", true},
		{"numbered option under » cursor rejected", "» 1. Replace current goal  Set the new objective and start it now\n  2. Keep current goal\n", false},
		{"press-enter chrome below marker rejected", "some dialog\n› Yes, continue\n\nPress enter to continue\n", false},
		{"no marker at all", "plain shell\n$ \n", false},
		{"transcript mentions options but live composer below wins", "  1. first thing to do\n  2. second thing\n› Ask Codex to do anything\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composerVisibleForDelivery(tc.capture, markers); got != tc.want {
				t.Errorf("composerVisibleForDelivery() = %v, want %v\ncapture:\n%s", got, tc.want, tc.capture)
			}
		})
	}
}

// The post-send verifier must see an unsubmitted payload sitting behind the
// ultra "»" prompt exactly as it does behind "›" — otherwise a stuck ultra
// delivery would be reported as submitted and never rescued.
func TestCodexComposerHoldsPayload_UltraGlyph(t *testing.T) {
	message := "Reply with exactly the word ULTRA-OK and nothing else."

	cases := []struct {
		name    string
		capture string
		want    bool
	}{
		{"payload stuck behind ultra prompt", "transcript\n» Reply with exactly the word ULTRA-OK and nothing else.\n", true},
		{"pasted stand-in behind ultra prompt", "transcript\n» [Pasted 3 lines]\n", true},
		{"empty ultra composer holds nothing", "transcript\n» \n", false},
		{"ultra placeholder hint is not payload", "transcript\n» Ask Codex to do anything\n", false},
		{"payload stuck behind xhigh prompt still detected", "transcript\n› Reply with exactly the word ULTRA-OK and nothing else.\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexComposerHoldsPayload(tc.capture, message); got != tc.want {
				t.Errorf("codexComposerHoldsPayload() = %v, want %v\ncapture:\n%s", got, tc.want, tc.capture)
			}
		})
	}
}

// The composer-emptiness verifier (pre-send clear, InspectComposer) must
// recognize the ultra glyph too: the placeholder hint counts as empty, real
// residue does not.
func TestComposerLineEmpty_UltraGlyph(t *testing.T) {
	markers := composerMarkersForAgent(AgentCodex)

	found, empty, _ := composerBlockEmpty(codexUltraIdleFrame, markers, composerPlaceholderPrefixes(AgentCodex))
	if !found || !empty {
		t.Fatalf("ultra idle frame: got (found=%v, empty=%v), want (true, true)", found, empty)
	}

	found, empty, _ = composerBlockEmpty("transcript\n» stale half-typed prompt\n", markers, composerPlaceholderPrefixes(AgentCodex))
	if !found || empty {
		t.Fatalf("ultra residue: got (found=%v, empty=%v), want (true, false)", found, empty)
	}

	state := InspectComposer("chat\n» leftover draft\n", AgentCodex)
	if !state.MarkerVisible || !state.HoldsText {
		t.Fatalf("InspectComposer ultra residue = %+v, want marker visible and holding text", state)
	}
}
