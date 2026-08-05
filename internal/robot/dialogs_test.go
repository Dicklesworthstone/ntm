package robot

import (
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
