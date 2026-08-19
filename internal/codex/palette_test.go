package codex

import (
	"reflect"
	"testing"
)

// Representative captured-pane fixtures, one per state. These approximate a
// tmux capture of a Codex pane in each state, built from strings verified
// against Codex's own source: the slash palette renders each command as
// "/<command>" (codex-rs/tui/src/bottom_pane/command_popup.rs), and the
// approval headers/option labels are from
// codex-rs/tui/src/bottom_pane/approval_overlay.rs.
const (
	fixtureIdle = `
Welcome to Codex.

›
? for shortcuts
`

	fixtureSlashPaletteOpen = `
› /
  /model      switch the active model
  /review     review my current changes
  /init       create an AGENTS.md file
  /compact    summarize conversation to prevent hitting the context limit
  /mention    mention a file
  /mcp        list configured MCP servers
`

	// A slash palette filtered to goal/plan commands (e.g. after typing "/g" or
	// "/p") — the broad palette entries are absent, so the goal rule wins over
	// the general slash-palette rule.
	fixtureGoalPalettePrimed = `
› /
  /goal   set the active goal for this session
  /plan   enter plan mode
`

	fixtureDialogOpen = `
╭─────────────────────────────────────────────────╮
│ Would you like to run the following command?     │
│   $ rm -rf build/                                │
│                                                  │
│ › 1. Yes, proceed                                │
│   2. No, and tell Codex what to do differently   │
╰─────────────────────────────────────────────────╯
`

	fixtureUnknown = `
some unrelated shell output
make: *** [build] Error 2
$ ls -la
`
)

func TestClassify_PerState(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantState   PaletteState
		wantMarkers []string
	}{
		{
			name:        "idle",
			content:     fixtureIdle,
			wantState:   StateIdle,
			wantMarkers: []string{"? for shortcuts", "›"},
		},
		{
			name:        "slash_palette_open",
			content:     fixtureSlashPaletteOpen,
			wantState:   StateSlashPaletteOpen,
			wantMarkers: []string{"/compact", "/mention", "/review", "/mcp", "/init", "/model"},
		},
		{
			name:        "goal_palette_primed",
			content:     fixtureGoalPalettePrimed,
			wantState:   StateGoalPalettePrimed,
			wantMarkers: []string{"/goal", "/plan"},
		},
		{
			name:        "dialog_open",
			content:     fixtureDialogOpen,
			wantState:   StateDialogOpen,
			wantMarkers: []string{"Would you like to run the following command?", "No, and tell Codex what to do differently", "Yes, proceed"},
		},
		{
			name:        "unknown",
			content:     fixtureUnknown,
			wantState:   StateUnknown,
			wantMarkers: nil,
		},
		{
			name:        "empty",
			content:     "",
			wantState:   StateUnknown,
			wantMarkers: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.content)
			if got.State != tt.wantState {
				t.Fatalf("Classify() state = %q, want %q", got.State, tt.wantState)
			}
			if !reflect.DeepEqual(got.MarkersMatched, tt.wantMarkers) {
				t.Fatalf("Classify() markers = %#v, want %#v", got.MarkersMatched, tt.wantMarkers)
			}
		})
	}
}

// TestClassify_Precedence proves the priority ordering: when a higher-priority
// overlay (a modal dialog) is drawn over a lower-priority one (an open slash
// palette), the most input-capturing state wins.
func TestClassify_Precedence(t *testing.T) {
	combined := fixtureSlashPaletteOpen + fixtureDialogOpen
	got := Classify(combined)
	if got.State != StateDialogOpen {
		t.Fatalf("expected dialog_open to win over slash_palette_open, got %q", got.State)
	}
}

// TestClassify_CaseInsensitive proves matching ignores case.
func TestClassify_CaseInsensitive(t *testing.T) {
	got := Classify("? FOR SHORTCUTS")
	if got.State != StateIdle {
		t.Fatalf("expected idle for upper-cased idle marker, got %q", got.State)
	}
}

// TestOrderedRules_SortedByPriority guards the precedence contract independent
// of the literal authoring order of StateMarkers.
func TestOrderedRules_SortedByPriority(t *testing.T) {
	rules := orderedRules()
	for i := 1; i < len(rules); i++ {
		if rules[i].Priority < rules[i-1].Priority {
			t.Fatalf("orderedRules not ascending: %d before %d", rules[i-1].Priority, rules[i].Priority)
		}
	}
}
