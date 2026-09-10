package tmux

// Guard for the "handle approval dialogs without pressing Enter" requirement
// of GitHub issue #320.
//
// Wiring submission verification into pipeline dispatch means a rescue Enter
// can now reach panes it never reached before, and an approval dialog is the
// one place a stray Enter is actively harmful: it would answer a permission
// question the operator never saw.
//
// The verifiers are structurally safe here, because the rescue Enter sits
// behind `codexLooksWorking(capture) || !composerHoldsPayload(capture, msg)`
// — it is reached ONLY when the payload is visibly still in the composer. An
// approval dialog can only appear after a prompt was submitted, which leaves
// the composer empty of that payload. These cases pin that reasoning against
// real dialog chrome so a future predicate change cannot quietly enable an
// Enter into a dialog.

import "testing"

func TestApprovalDialogIsNeverTreatedAsAnUnsubmittedComposer(t *testing.T) {
	const message = "Refactor the auth layer and run the tests."

	cases := []struct {
		name    string
		capture string
	}{
		{
			name: "codex command approval dialog",
			capture: "› Refactor the auth layer and run the tests.\n" +
				"\n" +
				"  Codex wants to run: cargo test --all\n" +
				"  1. Yes, run it\n" +
				"  2. No, and tell Codex what to do differently\n" +
				"› \n",
		},
		{
			name: "codex file write approval dialog",
			capture: "› Refactor the auth layer and run the tests.\n" +
				"\n" +
				"  Allow write to src/auth.rs?\n" +
				"  ❯ 1. Yes\n" +
				"    2. No\n" +
				"› \n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if codexComposerHoldsPayload(tc.capture, message) {
				t.Errorf("an approval dialog was read as an unsubmitted composer, which would send a rescue Enter into the dialog and answer it\ncapture:\n%s", tc.capture)
			}
		})
	}
}

func TestClaudeApprovalDialogIsNeverTreatedAsAnUnsubmittedComposer(t *testing.T) {
	const message = "Refactor the auth layer and run the tests."

	capture := "> Refactor the auth layer and run the tests.\n" +
		"\n" +
		"  Do you want to make this edit to auth.ts?\n" +
		"  ❯ 1. Yes\n" +
		"    2. No, tell Claude what to do differently\n" +
		"❯ \n"

	if claudeComposerHoldsPayload(capture, message) {
		t.Errorf("a Claude approval dialog was read as an unsubmitted composer, which would send a rescue Enter into the dialog\ncapture:\n%s", capture)
	}
}

// TestStrandedComposerIsStillDetected is the positive control: the guard above
// must not be satisfied by a predicate that simply never fires.
func TestStrandedComposerIsStillDetected(t *testing.T) {
	const message = "Refactor the auth layer and run the tests."

	capture := "previous answer\n› Refactor the auth layer and run the tests.\n"
	if !codexComposerHoldsPayload(capture, message) {
		t.Error("a genuinely stranded codex composer must still be detected, otherwise the rescue never runs")
	}

	pasted := "previous answer\n› [Pasted repair instructions]\n"
	if !codexComposerHoldsPayload(pasted, message) {
		t.Error("the [Pasted ...] stand-in for a long prompt must still count as a stranded composer")
	}
}
