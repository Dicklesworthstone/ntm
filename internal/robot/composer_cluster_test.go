package robot

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// bd-eeifh regression: on a ~26-column pane, codex's spinner frame wraps
// into enough physical rows that the fixed 15-line live window misses it and
// an actively-working pane classified idle. The width-adaptive window must
// keep it busy, while the same capture with an unknown width (0) preserves
// the historical conservative verdict.
func TestIsLiveBusyNarrowPaneSpinner(t *testing.T) {
	// Simulate a 26-column pane: the spinner line hard-wraps into three
	// physical rows, followed by wrapped output rows pushing it up.
	wrapped := []string{
		"• Working (4m 51s • esc",
		" to interrupt)",
	}
	filler := make([]string, 0, 20)
	for i := 0; i < 18; i++ {
		filler = append(filler, "some wrapped output row he", "re continuing the line")
	}
	capture := strings.Join(append(wrapped, filler...), "\n")

	if IsLiveBusy(capture, "codex", 26) {
		// The spinner is ~38 physical rows up; even the widened window
		// (15*4=60 at width 26 -> ceil(80/26)=4) covers it.
	} else {
		t.Fatal("IsLiveBusy(width=26) = false for a working narrow pane; want true")
	}
	if IsLiveBusy(capture, "codex", 0) {
		t.Fatal("IsLiveBusy(width unknown) = true; the fixed window must stay conservative")
	}
}

func TestWidthAdaptiveTailLines(t *testing.T) {
	cases := []struct {
		width, base, want int
	}{
		{0, 15, 15},   // unknown width: unchanged
		{80, 15, 15},  // reference width: unchanged
		{120, 15, 15}, // wide: unchanged
		{40, 15, 30},  // half width: doubled
		{26, 15, 60},  // ~third width: ceil(80/26)=4 -> capped exactly at 4x
		{10, 15, 60},  // pathological: capped at 4x
		{26, 0, 0},    // zero base: unchanged
	}
	for _, tc := range cases {
		if got := util.WidthAdaptiveTailLines(tc.width, tc.base); got != tc.want {
			t.Errorf("WidthAdaptiveTailLines(%d, %d) = %d, want %d", tc.width, tc.base, got, tc.want)
		}
	}
}

// bd-v8dqd: message-independent composer inspection.
func TestInspectComposer(t *testing.T) {
	cases := []struct {
		name      string
		capture   string
		agentType tmux.AgentType
		want      tmux.ComposerState
	}{
		{
			name:      "claude empty composer",
			capture:   "some output\n❯ \n? for shortcuts",
			agentType: tmux.AgentClaude,
			want:      tmux.ComposerState{MarkerVisible: true},
		},
		{
			name:      "claude unsubmitted text",
			capture:   "some output\n❯ fix the auth bug in server.go\n? for shortcuts",
			agentType: tmux.AgentClaude,
			want:      tmux.ComposerState{MarkerVisible: true, HoldsText: true},
		},
		{
			name:      "claude placeholder counts as empty",
			capture:   "output\n❯ Try \"fix lint errors\"\nfooter",
			agentType: tmux.AgentClaude,
			want:      tmux.ComposerState{MarkerVisible: true},
		},
		{
			name:      "claude queued messages footer",
			capture:   "❯ \nPress up to edit queued messages",
			agentType: tmux.AgentClaude,
			want:      tmux.ComposerState{MarkerVisible: true, QueuedMessages: true},
		},
		{
			name:      "codex unsubmitted text",
			capture:   "transcript\n› implement the parser\n47% context left",
			agentType: tmux.AgentCodex,
			want:      tmux.ComposerState{MarkerVisible: true, HoldsText: true},
		},
		{
			name:      "codex bottom-most marker wins over transcript echo",
			capture:   "› old submitted message echo\nagent response\n› \nfooter",
			agentType: tmux.AgentCodex,
			want:      tmux.ComposerState{MarkerVisible: true},
		},
		{
			name:      "splash screen without composer",
			capture:   "Welcome to Claude Code\nLoading...",
			agentType: tmux.AgentClaude,
			want:      tmux.ComposerState{},
		},
		{
			name:      "unknown agent type",
			capture:   "❯ text",
			agentType: tmux.AgentUnknown,
			want:      tmux.ComposerState{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.InspectComposer(tc.capture, tc.agentType); got != tc.want {
				t.Errorf("InspectComposer() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
