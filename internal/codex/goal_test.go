package codex

import (
	"reflect"
	"testing"
)

// Live-grounded fixtures captured against Codex 0.137.0 during the #165/#168/#169
// implementation session. These are the exact on-screen shapes the commands rely
// on (status-bar pursuing-goal counter, goal-active banner, replace-goal modal,
// hook-init / Agent Mail macro_start_session).
const (
	gfPursuing1s = `• Goal active   Objective: refactor the parser
• Working (1s • esc to interrupt)
  gpt-5.5 medium · /data/projects/ntm · Working · Context 100% left · Pursuing goal (1s)`

	gfPursuing17s = `• Goal active   Objective: refactor the parser
• Working (17s • esc to interrupt)
  gpt-5.5 medium · /data/projects/ntm · Working · Context 100% left · Pursuing goal (17s)`

	gfHookInit = `• Goal active   Objective: add unit test
  └ mcp_agent_mail.macro_start_session({"human_key":"/data/projects/ntm","file_reservation_paths":["internal/cli/codex.go"]})
• Working (3s • esc to interrupt)
  gpt-5.5 medium · Working · Pursuing goal (3s)`

	gfReplaceDialog = `• Ran git status --short
  Replace goal?
  New objective: document the preflight package thoroughly
› 1. Replace current goal  Set the new objective and start it now
  2. Cancel                Keep the current goal
  Press enter to confirm or esc to go back`

	gfUsageLimit = `• Goal active   Objective: refactor the parser
You've reached your usage limit. Please try again later.
  Pursuing goal (2s)`

	gfNoGoalLive = `╭──────────────────────────────────────────────╮
│ >_ OpenAI Codex (v0.137.0)                   │
╰──────────────────────────────────────────────╯
›
  gpt-5.5 medium · /data/projects/ntm · Ready · Context 100% left`

	gfShell = `user@host:~/project$ ls -la
total 8
user@host:~/project$ `
)

// TestSampleEngagement verifies the per-capture signal extraction.
func TestSampleEngagement(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantCounter   int
		wantPursuing  bool
		wantGoal      bool
		wantHook      bool
		wantWorking   bool
		wantReplace   bool
		wantUsage     bool
		wantCodexLive bool
	}{
		{
			name:          "pursuing-1s",
			content:       gfPursuing1s,
			wantCounter:   1,
			wantPursuing:  true,
			wantGoal:      true,
			wantWorking:   true,
			wantCodexLive: true,
		},
		{
			name:          "hook-init",
			content:       gfHookInit,
			wantCounter:   3,
			wantPursuing:  true,
			wantGoal:      true,
			wantHook:      true,
			wantWorking:   true,
			wantCodexLive: true,
		},
		{
			name:          "replace-dialog",
			content:       gfReplaceDialog,
			wantCounter:   -1,
			wantReplace:   true,
			wantCodexLive: true, // replace-goal still implies Codex is present
		},
		{
			name:          "usage-limit",
			content:       gfUsageLimit,
			wantCounter:   2, // pursuing counter is still parsed even under usage-limit
			wantPursuing:  true,
			wantGoal:      true,
			wantUsage:     true,
			wantCodexLive: false, // usage-limit preflight state is not in the live set
		},
		{
			name:          "no-goal-live",
			content:       gfNoGoalLive,
			wantCounter:   -1,
			wantCodexLive: true,
		},
		{
			name:        "shell-no-codex",
			content:     gfShell,
			wantCounter: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SampleEngagement(tt.content)
			if s.PursuingPresent != tt.wantPursuing {
				t.Errorf("PursuingPresent = %v, want %v", s.PursuingPresent, tt.wantPursuing)
			}
			// Counter only meaningful when pursuing was present.
			if tt.wantPursuing && s.PursuingCounter != tt.wantCounter {
				t.Errorf("PursuingCounter = %d, want %d", s.PursuingCounter, tt.wantCounter)
			}
			if s.GoalActive != tt.wantGoal {
				t.Errorf("GoalActive = %v, want %v", s.GoalActive, tt.wantGoal)
			}
			if s.HookInit != tt.wantHook {
				t.Errorf("HookInit = %v, want %v", s.HookInit, tt.wantHook)
			}
			if s.Working != tt.wantWorking {
				t.Errorf("Working = %v, want %v", s.Working, tt.wantWorking)
			}
			if s.ReplaceDialog != tt.wantReplace {
				t.Errorf("ReplaceDialog = %v, want %v", s.ReplaceDialog, tt.wantReplace)
			}
			if s.UsageLimit != tt.wantUsage {
				t.Errorf("UsageLimit = %v, want %v", s.UsageLimit, tt.wantUsage)
			}
			if s.CodexLive != tt.wantCodexLive {
				t.Errorf("CodexLive = %v, want %v", s.CodexLive, tt.wantCodexLive)
			}
		})
	}
}

func TestParseElapsedSeconds(t *testing.T) {
	cases := map[string]int{
		"1s":    1,
		"17s":   17,
		"45s":   45,
		"2m3s":  123,
		"1m":    60,
		"1h2m":  3720,
		"12":    12,
		"":      0,
		"  5s ": 5,
	}
	for in, want := range cases {
		if got := parseElapsedSeconds(in); got != want {
			t.Errorf("parseElapsedSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestClassifyEngagement is the core #169 table-driven classifier test.
func TestClassifyEngagement(t *testing.T) {
	tests := []struct {
		name        string
		samples     []EngagementSample
		timedOut    bool
		wantOutcome EngagementOutcome
		wantDelta   int
		wantInit    int
		wantHook    bool
		wantMixed   bool
		wantExitNZ  bool
	}{
		{
			name:        "no-samples-unconfirmed",
			samples:     nil,
			timedOut:    true,
			wantOutcome: EngagementUnconfirmed,
			wantInit:    -1,
			wantExitNZ:  true,
		},
		{
			name: "advancing-counter-engaged",
			samples: []EngagementSample{
				{PursuingPresent: true, PursuingCounter: 1, GoalActive: true, Working: true, CodexLive: true},
				{PursuingPresent: true, PursuingCounter: 3, GoalActive: true, Working: true, CodexLive: true},
				{PursuingPresent: true, PursuingCounter: 5, GoalActive: true, Working: true, CodexLive: true},
			},
			wantOutcome: EngagementEngaged,
			wantDelta:   4,
			wantInit:    1,
			wantExitNZ:  false,
		},
		{
			name: "counter-plus-hookinit-engaged",
			samples: []EngagementSample{
				{PursuingPresent: true, PursuingCounter: 2, GoalActive: true, HookInit: true, Working: true, CodexLive: true},
			},
			wantOutcome: EngagementEngaged,
			wantDelta:   0,
			wantInit:    2,
			wantHook:    true,
			wantExitNZ:  false,
		},
		{
			name: "banner-only-engaging",
			samples: []EngagementSample{
				{GoalActive: true, Working: true, CodexLive: true},
			},
			wantOutcome: EngagementEngaging,
			wantInit:    -1,
			wantExitNZ:  false,
		},
		{
			name: "static-counter-no-hook-engaging",
			samples: []EngagementSample{
				{PursuingPresent: true, PursuingCounter: 4, GoalActive: true, Working: true, CodexLive: true},
				{PursuingPresent: true, PursuingCounter: 4, GoalActive: true, Working: true, CodexLive: true},
			},
			wantOutcome: EngagementEngaging,
			wantDelta:   0,
			wantInit:    4,
			wantExitNZ:  false,
		},
		{
			name: "replace-dialog-stuck",
			samples: []EngagementSample{
				{GoalActive: true, CodexLive: true},
				{ReplaceDialog: true, CodexLive: true},
			},
			timedOut:    false,
			wantOutcome: EngagementDialogStuck,
			wantInit:    -1,
			wantExitNZ:  true,
		},
		{
			name: "usage-limit-respawn",
			samples: []EngagementSample{
				{PursuingPresent: true, PursuingCounter: 1, GoalActive: true, CodexLive: true},
				{UsageLimit: true, GoalActive: true},
			},
			wantOutcome: EngagementRespawnRequired,
			wantInit:    1,
			wantMixed:   true,
			wantExitNZ:  true,
		},
		{
			name: "pane-died-respawn",
			samples: []EngagementSample{
				{GoalActive: true, CodexLive: true},
				{CodexLive: false},
			},
			wantOutcome: EngagementRespawnRequired,
			wantInit:    -1,
			wantExitNZ:  true,
		},
		{
			name: "never-engaged-unconfirmed",
			samples: []EngagementSample{
				{CodexLive: true},
				{CodexLive: true},
			},
			timedOut:    true,
			wantOutcome: EngagementUnconfirmed,
			wantInit:    -1,
			wantExitNZ:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyEngagement(tt.samples, tt.timedOut)
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("Outcome = %q, want %q (reason: %s)", got.Outcome, tt.wantOutcome, got.Reason)
			}
			if got.CounterInit != tt.wantInit {
				t.Errorf("CounterInit = %d, want %d", got.CounterInit, tt.wantInit)
			}
			if got.CumulativeDelta != tt.wantDelta {
				t.Errorf("CumulativeDelta = %d, want %d", got.CumulativeDelta, tt.wantDelta)
			}
			if got.HookInit != tt.wantHook {
				t.Errorf("HookInit = %v, want %v", got.HookInit, tt.wantHook)
			}
			if got.UsageLimitMixedState != tt.wantMixed {
				t.Errorf("UsageLimitMixedState = %v, want %v", got.UsageLimitMixedState, tt.wantMixed)
			}
			if got.Reason == "" {
				t.Errorf("Reason must never be empty")
			}
			if EngagementExitNonZero(got.Outcome) != tt.wantExitNZ {
				t.Errorf("EngagementExitNonZero(%q) = %v, want %v", got.Outcome, EngagementExitNonZero(got.Outcome), tt.wantExitNZ)
			}
		})
	}
}

// TestAllEngagementOutcomes_Closed guards the closed outcome set.
func TestAllEngagementOutcomes_Closed(t *testing.T) {
	want := []EngagementOutcome{
		EngagementEngaged,
		EngagementEngaging,
		EngagementDialogStuck,
		EngagementUnconfirmed,
		EngagementRespawnRequired,
	}
	if !reflect.DeepEqual(AllEngagementOutcomes(), want) {
		t.Fatalf("AllEngagementOutcomes() = %#v, want %#v", AllEngagementOutcomes(), want)
	}
}

// TestDetectReplaceGoalDialog covers the #168 detection + old-goal-closed proof.
func TestDetectReplaceGoalDialog(t *testing.T) {
	t.Run("live-dialog-interactive-with-proof", func(t *testing.T) {
		d := DetectReplaceGoalDialog(gfReplaceDialog)
		if !d.Present {
			t.Fatalf("expected Present=true for live replace-goal dialog")
		}
		if !d.Interactive {
			t.Errorf("expected Interactive=true (confirm affordance present)")
		}
		if !d.OldGoalClosed {
			t.Errorf("expected OldGoalClosed=true ('keep the current goal' / 'replace current goal' present)")
		}
		if d.NewObjective != "document the preflight package thoroughly" {
			t.Errorf("NewObjective = %q, want %q", d.NewObjective, "document the preflight package thoroughly")
		}
		if len(d.MarkersMatched) == 0 {
			t.Errorf("expected non-empty MarkersMatched")
		}
	})

	t.Run("no-dialog", func(t *testing.T) {
		d := DetectReplaceGoalDialog(gfNoGoalLive)
		if d.Present {
			t.Fatalf("expected Present=false when no dialog is on screen")
		}
		if d.Interactive || d.OldGoalClosed {
			t.Errorf("absent dialog must not be interactive or proven")
		}
	})

	t.Run("dialog-not-yet-interactive", func(t *testing.T) {
		// Modal header + option present but the confirm affordance not yet rendered.
		partial := "Replace goal?\nNew objective: foo\n2. Cancel  Keep the current goal\n"
		d := DetectReplaceGoalDialog(partial)
		if !d.Present {
			t.Fatalf("expected Present=true (header + markers present)")
		}
		if d.Interactive {
			t.Errorf("expected Interactive=false without the confirm affordance")
		}
	})
}

func TestReplaceGoalSelection_Strings(t *testing.T) {
	if ReplaceGoalReplace.String() != "replace" {
		t.Errorf("ReplaceGoalReplace = %q", ReplaceGoalReplace)
	}
	if ReplaceGoalCancel.String() != "cancel" {
		t.Errorf("ReplaceGoalCancel = %q", ReplaceGoalCancel)
	}
}
