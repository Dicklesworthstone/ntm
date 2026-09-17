package robot

import (
	"reflect"
	"strings"
	"testing"
)

// Live fixture captured 2026-08-04 from a real Claude Code first boot.
const trustPromptFixture = `
 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`

const rateLimitFixture = `
 You've reached your usage limit for this session.

   1. Wait for the limit to reset
 ❯ 2. Use extra usage beyond the included amount
   3. Upgrade my plan
`

const destructiveConfirmFixture = `
 Claude wants to run the following command:

   git push --force origin main

 Do you want to proceed?

 ❯ 1. Yes, run it
   2. Yes, and don't ask again for git push
   3. No, and tell Claude what to do differently
`

const codexPasteLimboFixture = `
transcript line
› [Pasted text #1 +48 lines]
  send a message
`

const usageOverlayFixture = `
 Usage

 Current session: 42% used
 Weekly limit: 18% used

 esc to close
`

const idleClaudeFixture = `
 some transcript

 ❯ Try "refactor <filepath>"
`

func TestClassifyDialog_Fixtures(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		agent   string
		want    string
	}{
		{"trust prompt", trustPromptFixture, "claude", DialogTrustPrompt},
		{"rate limit options", rateLimitFixture, "claude", DialogRateLimitOptions},
		{"destructive confirm", destructiveConfirmFixture, "claude", DialogDestructiveConfirm},
		{"codex paste limbo", codexPasteLimboFixture, "codex", DialogPasteLimbo},
		{"usage overlay", usageOverlayFixture, "claude", DialogUsageOverlay},
		{"idle composer is none", idleClaudeFixture, "claude", DialogNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDialog(tc.capture, tc.agent)
			if got.Class != tc.want {
				t.Fatalf("classifyDialog(%s) = %s, want %s (options: %s)", tc.name, got.Class, tc.want, describeOptions(got.Options))
			}
		})
	}
}

func TestClassifyDialog_ExtractsOptionsAndSelection(t *testing.T) {
	state := classifyDialog(trustPromptFixture, "claude")
	if len(state.Options) != 2 {
		t.Fatalf("expected 2 options, got %s", describeOptions(state.Options))
	}
	if !state.Options[0].Selected || state.Options[1].Selected {
		t.Fatalf("expected option 1 selected, option 2 not: %+v", state.Options)
	}
	if !strings.Contains(state.Options[1].Label, "No, exit") {
		t.Fatalf("option 2 label = %q", state.Options[1].Label)
	}
}

func TestResolveDialogAnswer_DestructivePolicy(t *testing.T) {
	state := classifyDialog(destructiveConfirmFixture, "claude")
	if state.Class != DialogDestructiveConfirm {
		t.Fatalf("fixture must classify destructive, got %s", state.Class)
	}
	// Accept-side answers refused, whether direct or by option number.
	for _, choice := range []string{"option-1", "option-2"} {
		if _, err := resolveDialogAnswer(state, choice); err == nil || !strings.Contains(err.Error(), "POLICY_REFUSED") {
			t.Fatalf("choice %s must be POLICY_REFUSED, got err=%v", choice, err)
		}
	}
	// Decline-side allowed both ways.
	keys, err := resolveDialogAnswer(state, "option-3")
	if err != nil || len(keys) != 2 || keys[0] != "3" {
		t.Fatalf("option-3 should type 3+Enter, got %v err=%v", keys, err)
	}
	keys, err = resolveDialogAnswer(state, "decline")
	if err != nil || keys[0] != "3" {
		t.Fatalf("decline should pick option 3, got %v err=%v", keys, err)
	}
}

func TestResolveDialogAnswer_ExtraUsage(t *testing.T) {
	state := classifyDialog(rateLimitFixture, "claude")
	keys, err := resolveDialogAnswer(state, "extra-usage")
	if err != nil || keys[0] != "2" {
		t.Fatalf("extra-usage should pick option 2, got %v err=%v", keys, err)
	}
	if _, err := resolveDialogAnswer(classifyDialog(trustPromptFixture, "claude"), "extra-usage"); err == nil {
		t.Fatal("extra-usage on a trust prompt must error")
	}
}

func TestResolveDialogAnswer_DismissAndNone(t *testing.T) {
	keys, err := resolveDialogAnswer(classifyDialog(usageOverlayFixture, "claude"), "dismiss")
	if err != nil || len(keys) != 1 || keys[0] != "Escape" {
		t.Fatalf("dismiss = %v err=%v", keys, err)
	}
	if _, err := resolveDialogAnswer(DialogState{Class: DialogNone}, "dismiss"); err == nil {
		t.Fatal("answering a pane with no dialog must error")
	}
}

// bd-70c00: Resolved used to be `After == None || After != Before`, so a
// dialog REPLACED by a different dialog reported resolved:true with a modal
// still blocking the pane. The caller then sent work the new dialog swallowed
// — success without verified effect, the exact failure ntm-epu6 exists to
// eliminate.
func TestApplyDialogResolution(t *testing.T) {
	tests := []struct {
		name           string
		before, after  string
		wantResolved   bool
		wantSuccess    bool
		wantFollowUp   string
		wantErrContain string
	}{
		{
			name: "dialog cleared", before: DialogDestructiveConfirm, after: DialogNone,
			wantResolved: true, wantSuccess: true,
		},
		{
			name: "same dialog still present", before: DialogTrustPrompt, after: DialogTrustPrompt,
			wantResolved: false, wantSuccess: false,
			wantErrContain: "still present",
		},
		{
			name: "answering surfaced a DIFFERENT dialog", before: DialogDestructiveConfirm, after: DialogRateLimitOptions,
			wantResolved: false, wantSuccess: false,
			wantFollowUp:   DialogRateLimitOptions,
			wantErrContain: "follow-up dialog",
		},
		{
			name: "post-answer capture failed", before: DialogTrustPrompt, after: DialogUnknown,
			wantResolved: false, wantSuccess: false,
			wantFollowUp:   DialogUnknown,
			wantErrContain: "follow-up dialog",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			out := &AnswerDialogOutput{
				RobotResponse: NewRobotResponse(true),
				Before:        DialogState{Class: tt.before},
				After:         DialogState{Class: tt.after},
			}
			applyDialogResolution(out)

			if out.Resolved != tt.wantResolved {
				t.Fatalf("Resolved = %v, want %v (before=%s after=%s)", out.Resolved, tt.wantResolved, tt.before, tt.after)
			}
			if out.Success != tt.wantSuccess {
				t.Fatalf("Success = %v, want %v", out.Success, tt.wantSuccess)
			}
			if out.FollowUpDialog != tt.wantFollowUp {
				t.Fatalf("FollowUpDialog = %q, want %q", out.FollowUpDialog, tt.wantFollowUp)
			}
			if tt.wantErrContain != "" && !strings.Contains(out.Error, tt.wantErrContain) {
				t.Fatalf("Error = %q, want it to mention %q", out.Error, tt.wantErrContain)
			}
		})
	}
}

// Live captures from GH#325, not inferred provider layouts. Claude's default
// selection is the DECLINE option and differs from Antigravity's default.
const claudeCursorTrustFixture = `
 Accessing workspace:
 /tmp/project
 Quick safety check: Is this a project you created or one you trust?
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

const antigravityCursorTrustFixture = `
Accessing workspace:
/tmp/project
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> Yes, I trust this folder
  No, exit
  ↑/↓ Navigate · enter Confirm
`

const grokHotkeyTrustFixture = `
/tmp/project
Grok Build may run or modify contents in this directory,
posing security risks.
Yes, proceed                 y
No, quit                     n
`

func TestDialogUnnumberedProviderProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, capture, agent, mode, choice string
		keys                               []string
	}{
		{"claude accept is second", claudeCursorTrustFixture, "claude", "cursor", "option-2", []string{"Down", "Enter"}},
		{"claude decline is selected", claudeCursorTrustFixture, "claude", "cursor", "decline", []string{"Enter"}},
		{"antigravity decline is second", antigravityCursorTrustFixture, "antigravity", "cursor", "decline", []string{"Down", "Enter"}},
		{"antigravity accept is selected", antigravityCursorTrustFixture, "antigravity", "cursor", "option-1", []string{"Enter"}},
		{"grok accept is immediate", grokHotkeyTrustFixture, "grok", "hotkey", "option-1", []string{"y"}},
		{"grok decline is immediate", grokHotkeyTrustFixture, "grok", "hotkey", "decline", []string{"n"}},
		{"numbered still works", trustPromptFixture, "claude", "numbered", "decline", []string{"2", "Enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := classifyDialog(tc.capture, tc.agent)
			if state.Class != DialogTrustPrompt || state.InputMode != tc.mode || len(state.Options) != 2 {
				t.Fatalf("unexpected classification: %+v", state)
			}
			for i, option := range state.Options {
				if option.Number != i+1 {
					t.Fatalf("unstable display order: %+v", state.Options)
				}
			}
			keys, err := resolveDialogAnswer(state, tc.choice)
			if err != nil || !reflect.DeepEqual(keys, tc.keys) {
				t.Fatalf("answer = %v, %v; want %v", keys, err, tc.keys)
			}
		})
	}
}

func TestDialogCursorNavigationUsesActualSelection(t *testing.T) {
	capture := "Usage limit reached\n  Wait for the limit to reset\n  Use extra usage\n❯ Upgrade my plan\nEnter to confirm"
	state := classifyDialog(capture, "claude")
	keys, err := resolveDialogAnswer(state, "extra-usage")
	if state.Class != DialogRateLimitOptions || err != nil || !reflect.DeepEqual(keys, []string{"Up", "Enter"}) {
		t.Fatalf("extra-usage = %v, %v, state=%+v", keys, err, state)
	}
	keys, err = resolveDialogAnswer(state, "option-1")
	if err != nil || !reflect.DeepEqual(keys, []string{"Up", "Up", "Enter"}) {
		t.Fatalf("option-1 = %v, %v", keys, err)
	}
}

func TestDialogUnnumberedRejectsNonLiveMenus(t *testing.T) {
	for _, capture := range []string{
		"We discussed trust.\nYes, that looks good\nNo, that is not necessary",
		"```text\n" + claudeCursorTrustFixture + "```",
		claudeCursorTrustFixture + "\n❯ Try refactoring a file",
		grokHotkeyTrustFixture + "\nThe workspace is ready.\n❯",
		trustPromptFixture + "\nThe job finished.\n❯",
		"trust\n1. Yes\ntext interrupting the menu\n2. No\nEnter to confirm",
		"trust\n1. Yes\n2. No\n3. Maybe\n4. A\n5. B\n6. C\n7. D\n8. E\n9. F\n10. G",
		"trust\nYes, proceed  y\nNo, quit  y",
	} {
		if state := classifyDialog(capture, "claude"); state.Class != DialogNone {
			t.Errorf("non-live/invalid menu classified as %+v: %q", state, capture)
		}
	}
}

func TestDialogUnnumberedDoesNotWeakenDestructivePolicy(t *testing.T) {
	for _, capture := range []string{
		"Run git push --force origin main?\n❯ Yes, run it\n  No, cancel\nEnter to confirm",
		"Run git reset --hard?\nYes, proceed  y\nNo, quit  n",
	} {
		state := classifyDialog(capture, "claude")
		if state.Class != DialogDestructiveConfirm {
			t.Fatalf("not classified destructive: %+v", state)
		}
		if keys, err := resolveDialogAnswer(state, "option-1"); err == nil || !strings.Contains(err.Error(), "POLICY_REFUSED") || len(keys) != 0 {
			t.Fatalf("destructive acceptance not refused: keys=%v err=%v", keys, err)
		}
		if _, err := resolveDialogAnswer(state, "decline"); err != nil {
			t.Fatalf("decline should remain possible: %v", err)
		}
	}
}

func TestDialogCursorAnswerFailsClosedWithoutUniqueSelection(t *testing.T) {
	for _, capture := range []string{
		strings.Replace(claudeCursorTrustFixture, "❯ No", "  No", 1),
		strings.Replace(claudeCursorTrustFixture, "   Yes", " ❯ Yes", 1),
	} {
		state := classifyDialog(capture, "claude")
		if state.Class != DialogTrustPrompt {
			t.Fatalf("gate must remain visible even without usable cursor: %+v", state)
		}
		if keys, err := resolveDialogAnswer(state, "option-2"); err == nil || len(keys) != 0 {
			t.Fatalf("ambiguous cursor allowed actuation: keys=%v err=%v", keys, err)
		}
	}
}

func TestDialogNewestMenuWins(t *testing.T) {
	capture := trustPromptFixture + "\nOld dialog answered\n" + claudeCursorTrustFixture
	state := classifyDialog(capture, "claude")
	if state.InputMode != "cursor" || len(state.Options) != 2 || state.Options[0].Label != "No, exit" {
		t.Fatalf("historical numbered menu displaced the live one: %+v", state)
	}
	for _, choice := range []string{"option-0", "option-3", "option-9999999999999999999999999", "approve"} {
		if keys, err := resolveDialogAnswer(state, choice); err == nil || len(keys) != 0 {
			t.Errorf("invalid choice %q actuated: keys=%v err=%v", choice, keys, err)
		}
	}
}

func TestCursorDialogRequiresVerifiedSelectionBeforeEnter(t *testing.T) {
	before := classifyDialog(claudeCursorTrustFixture, "claude")
	selected := strings.Replace(claudeCursorTrustFixture, "❯ No, exit\n   Yes, I trust this folder", "  No, exit\n ❯ Yes, I trust this folder", 1)
	if err := verifyCursorDialogSelection(before, classifyDialog(selected, "claude"), "option-2"); err != nil {
		t.Fatalf("correctly selected option refused: %v", err)
	}
	for _, capture := range []string{
		claudeCursorTrustFixture,      // dropped Down: decline remains selected
		idleClaudeFixture,             // original dialog vanished
		antigravityCursorTrustFixture, // option order changed
		strings.Replace(selected, "trust this folder", "trust a different folder", 1),
		"git reset --hard\n" + selected, // destructive follow-up
	} {
		if err := verifyCursorDialogSelection(before, classifyDialog(capture, "claude"), "option-2"); err == nil {
			t.Errorf("unverified Enter was permitted: %q", capture)
		}
	}
	if err := verifyCursorDialogSelection(before, before, "decline"); err != nil {
		t.Fatalf("already-selected decline refused: %v", err)
	}
}
