package codex

import (
	"reflect"
	"testing"
)

// Representative captured-pane fixtures for the preflight classifier, one per
// state. They reuse the same grounding discipline as palette_test.go: idle /
// context / chevron markers come from internal/agent/patterns.go (codIdlePatterns,
// codContextPattern), the usage-limit copy from codRateLimitPatterns, and the
// "Esc to interrupt" working footer from a live Codex turn. Fixtures for the
// CONSERVATIVE states (replace-goal, background-wait, goal-completed) use the
// best-effort phrasings documented in preflight.go and are flagged there as
// unverified renderings.
const (
	pfCodexLive = `
Welcome to Codex.

47% context left · ? for shortcuts
›
`

	pfGoalInProgress = `
• Working on the task…
  Editing internal/foo.go

Esc to interrupt
`

	pfGoalCompleted = `
Goal completed: refactored the parser.

47% context left · ? for shortcuts
›
`

	pfReplaceGoalDialog = `
╭───────────────────────────────────────────────╮
│ A goal is already active.                       │
│ Replace the current goal with the new one?      │
│ › 1. Yes   2. No                                │
╰───────────────────────────────────────────────╯
`

	pfBackgroundWait = `
$ npm run build
Running in the background, waiting for command to finish…
`

	pfUsageLimit = `
You've reached your usage limit. Please try again later.
`

	pfShellNoCodex = `
user@host:~/project$ ls -la
total 8
drwxr-xr-x  2 user user 4096 .
user@host:~/project$
`

	pfStaleEmpty = ``

	pfStaleTrivial = "  \n  "

	pfUnknown = `
make: *** [build] Error 2
some compiler diagnostics that mention nothing codex-related
`
)

func TestPreflight_PerState(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantState   PreflightState
		wantAction  PreflightAction
		wantMarkers []string
	}{
		{
			name:        "codex-live",
			content:     pfCodexLive,
			wantState:   PreflightCodexLive,
			wantAction:  ActionProceed,
			wantMarkers: []string{"? for shortcuts", "% context left", "›"},
		},
		{
			name:        "goal-in-progress",
			content:     pfGoalInProgress,
			wantState:   PreflightGoalInProgress,
			wantAction:  ActionWait,
			wantMarkers: []string{"esc to interrupt"},
		},
		{
			name:        "goal-completed",
			content:     pfGoalCompleted,
			wantState:   PreflightGoalCompleted,
			wantAction:  ActionProceed,
			wantMarkers: []string{"goal completed"},
		},
		{
			name:        "replace-goal-dialog",
			content:     pfReplaceGoalDialog,
			wantState:   PreflightReplaceGoalDialog,
			wantAction:  ActionRefuse,
			wantMarkers: []string{"replace the current goal", "a goal is already active"},
		},
		{
			name:        "background-terminal-wait",
			content:     pfBackgroundWait,
			wantState:   PreflightBackgroundTerminalWait,
			wantAction:  ActionWait,
			wantMarkers: []string{"waiting for command to finish", "running in the background"},
		},
		{
			name:        "usage-limit",
			content:     pfUsageLimit,
			wantState:   PreflightUsageLimit,
			wantAction:  ActionRespawn,
			wantMarkers: []string{"you've reached your usage limit"},
		},
		{
			name:        "shell-no-codex",
			content:     pfShellNoCodex,
			wantState:   PreflightShellNoCodex,
			wantAction:  ActionAlternatePane,
			wantMarkers: []string{},
		},
		{
			// Regression for the bare-"context" marker bug: a plain shell pane
			// whose visible text merely contains the word "context" (a filename,
			// git log, prose) must NOT be classified as live Codex, or
			// send --codex-goal would blind-type into a non-Codex shell.
			name:        "shell-with-context-word-not-codex",
			content:     "user@host:~/proj$ cat context.md\n# Some context about the project\nuser@host:~/proj$ git log --oneline\nabc123 add context manager for auth\nuser@host:~/proj$ ",
			wantState:   PreflightShellNoCodex,
			wantAction:  ActionAlternatePane,
			wantMarkers: []string{},
		},
		{
			name:        "stale-scrollback-empty",
			content:     pfStaleEmpty,
			wantState:   PreflightStaleScrollback,
			wantAction:  ActionRefuse,
			wantMarkers: []string{},
		},
		{
			name:        "stale-scrollback-trivial",
			content:     pfStaleTrivial,
			wantState:   PreflightStaleScrollback,
			wantAction:  ActionRefuse,
			wantMarkers: []string{},
		},
		{
			name:        "unknown",
			content:     pfUnknown,
			wantState:   PreflightUnknown,
			wantAction:  ActionRefuse,
			wantMarkers: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Preflight(tt.content)
			if got.State != tt.wantState {
				t.Fatalf("Preflight() state = %q, want %q", got.State, tt.wantState)
			}
			if got.Action != tt.wantAction {
				t.Fatalf("Preflight() action = %q, want %q (state %q)", got.Action, tt.wantAction, got.State)
			}
			if !reflect.DeepEqual(got.MarkersMatched, tt.wantMarkers) {
				t.Fatalf("Preflight() markers = %#v, want %#v", got.MarkersMatched, tt.wantMarkers)
			}
			if got.Reason == "" {
				t.Fatalf("Preflight() reason must never be empty (state %q)", got.State)
			}
			// MarkersMatched must never be nil (JSON must encode []).
			if got.MarkersMatched == nil {
				t.Fatalf("Preflight() markers must be non-nil for JSON safety (state %q)", got.State)
			}
		})
	}
}

// TestPreflight_UsageLimitWins proves usage-limit takes precedence even when a
// live Codex prompt is also on screen — a limited account cannot do goal work
// regardless of overlay state.
func TestPreflight_UsageLimitWins(t *testing.T) {
	combined := pfCodexLive + pfUsageLimit
	got := Preflight(combined)
	if got.State != PreflightUsageLimit {
		t.Fatalf("expected usage-limit to win, got %q", got.State)
	}
	if got.Action != ActionRespawn {
		t.Fatalf("expected respawn action, got %q", got.Action)
	}
}

// TestPreflight_InProgressWinsOverLive proves an actively-working pane is
// classified goal-in-progress (wait) even though the idle markers may also be
// present in scrollback above the working footer.
func TestPreflight_InProgressWinsOverLive(t *testing.T) {
	combined := pfCodexLive + pfGoalInProgress
	got := Preflight(combined)
	if got.State != PreflightGoalInProgress {
		t.Fatalf("expected goal-in-progress to win over codex-live, got %q", got.State)
	}
}

// TestPreflight_CaseInsensitive proves matching ignores case.
func TestPreflight_CaseInsensitive(t *testing.T) {
	got := Preflight("ESC TO INTERRUPT")
	if got.State != PreflightGoalInProgress {
		t.Fatalf("expected goal-in-progress for upper-cased working footer, got %q", got.State)
	}
}

// TestEveryStateMapsToAClosedAction guards that each marker-driven rule maps to
// an action in the closed action set, and that the action set itself is closed.
func TestEveryStateMapsToAClosedAction(t *testing.T) {
	validActions := map[PreflightAction]bool{}
	for _, a := range AllPreflightActions() {
		validActions[a] = true
	}
	for _, r := range preflightRules {
		if !validActions[r.Action] {
			t.Fatalf("rule for state %q uses action %q outside the closed set", r.State, r.Action)
		}
	}
}

// TestOrderedPreflightRules_SortedByPriority guards the precedence contract
// independent of authoring order.
func TestOrderedPreflightRules_SortedByPriority(t *testing.T) {
	rules := orderedPreflightRules()
	for i := 1; i < len(rules); i++ {
		if rules[i].Priority < rules[i-1].Priority {
			t.Fatalf("orderedPreflightRules not ascending: %d before %d", rules[i-1].Priority, rules[i].Priority)
		}
	}
}

// TestAllPreflightStates_Closed guards the closed state set / count for #167.
func TestAllPreflightStates_Closed(t *testing.T) {
	want := []PreflightState{
		PreflightCodexLive,
		PreflightShellNoCodex,
		PreflightGoalInProgress,
		PreflightGoalCompleted,
		PreflightReplaceGoalDialog,
		PreflightBackgroundTerminalWait,
		PreflightUsageLimit,
		PreflightStaleScrollback,
		PreflightUnknown,
	}
	if !reflect.DeepEqual(AllPreflightStates(), want) {
		t.Fatalf("AllPreflightStates() = %#v, want %#v", AllPreflightStates(), want)
	}
}
