package status

import (
	"testing"
	"time"
)

// A live Claude Code pane that has finished its turn but keeps repainting its
// idle input box (cursor, "new task? /clear to save …" footer) has a FRESH tmux
// pane-activity timestamp even though no work is happening. determineState must
// report idle on the structural idle-prompt signal, not WORKING on the repaint.
func TestDetermineStateIdleDespiteFreshActivity(t *testing.T) {
	d := NewDetectorWithConfig(DefaultConfig())
	idleScreen := "" +
		"✶ Pontificating… (16m 20s)\n" +
		"✻ Baked for 16m 20s\n" +
		"\n" +
		"────────────────────────────────────────\n" +
		"❯ push it and sync the skills repo\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on          ·\n" +
		"  new task? /clear to save 200.7k tokens\n"

	// lastActivity = now → NOT low velocity (the repaint case).
	state, _ := d.determineState(idleScreen, "cc", time.Now())
	if state != StateIdle {
		t.Errorf("determineState = %v with fresh activity; want StateIdle (TUI repaint must not mask a genuine idle prompt)", state)
	}
}

// Sanity: a genuinely working pane (active spinner below the prompt, fresh
// activity) must still report working, not idle.
func TestDetermineStateWorkingWithSpinnerBelowPrompt(t *testing.T) {
	d := NewDetectorWithConfig(DefaultConfig())
	working := "" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────\n" +
		"✻ Noodling… (2m 5s · ↓ 6.2k tokens · thought for 7s)\n"
	state, _ := d.determineState(working, "cc", time.Now())
	if state == StateIdle {
		t.Errorf("determineState = StateIdle for an active-spinner pane; want working/non-idle")
	}
}
