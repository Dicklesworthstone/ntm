package panels

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// wizardCmdTimeout bounds how long a single command may run while a test
// pumps the wizard. Cursor blink ticks sleep for hundreds of milliseconds and
// carry nothing the wizard's step logic depends on, so they are dropped.
const wizardCmdTimeout = 250 * time.Millisecond

// collectWizardMsgs runs cmd the way tea.Program would - expanding batches
// and sequences - and returns the messages it produced.
func collectWizardMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(wizardCmdTimeout):
		return nil
	}
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectWizardMsgs(c)...)
		}
		return out
	}
	// tea.Sequence yields an unexported []tea.Cmd message type.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				out = append(out, collectWizardMsgs(c)...)
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

// pumpWizard delivers every message produced by cmd (and by the commands
// those deliveries return) to the wizard, bounded by depth so a self-renewing
// command cannot spin forever.
func pumpWizard(t *testing.T, sw *SpawnWizard, cmd tea.Cmd, depth int) *SpawnWizard {
	t.Helper()
	if depth > 16 {
		return sw
	}
	for _, msg := range collectWizardMsgs(cmd) {
		if _, ok := msg.(SpawnWizardDoneMsg); ok {
			continue
		}
		model, next := sw.Update(msg)
		sw = model.(*SpawnWizard)
		sw = pumpWizard(t, sw, next, depth+1)
	}
	return sw
}

func pressWizard(t *testing.T, sw *SpawnWizard, key tea.KeyMsg) *SpawnWizard {
	t.Helper()
	model, cmd := sw.Update(key)
	sw = model.(*SpawnWizard)
	return pumpWizard(t, sw, cmd, 0)
}

// The huh form behind each wizard step only completes through its own
// internal messages (next-field, next-group), which arrive as commands rather
// than key presses. Given those messages, choosing Minimal and pressing Enter
// must land on the counts step pre-filled with one Claude agent (#318).
func TestSpawnWizardMinimalEnterAdvancesToCountsWhenFormMessagesDelivered(t *testing.T) {
	t.Parallel()

	sw := NewSpawnWizard("test", 120, 30)
	sw = pumpWizard(t, sw, sw.Init(), 0)

	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyDown})
	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyDown})
	if sw.step != SpawnStepMethod {
		t.Fatalf("navigating options must not leave the method step, got %v", sw.step)
	}

	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})

	if sw.step != SpawnStepCounts {
		t.Fatalf("expected Enter on Minimal to advance to counts step, got %v", sw.step)
	}
	if sw.configMethod != "minimal" {
		t.Fatalf("expected configMethod=minimal, got %q", sw.configMethod)
	}
	if sw.ccStr != "1" || sw.codStr != "0" || sw.gmiStr != "0" || sw.agyStr != "0" {
		t.Fatalf("expected minimal pre-fill cc=1 cod=0 gmi=0 agy=0, got cc=%q cod=%q gmi=%q agy=%q",
			sw.ccStr, sw.codStr, sw.gmiStr, sw.agyStr)
	}
	if view := sw.View(); !strings.Contains(view, "Claude agents (cc)") {
		t.Fatalf("expected counts form in view, got:\n%s", view)
	}
}

// Enter through each count input reaches the confirm step with the summary
// reflecting the pre-filled minimal selection.
func TestSpawnWizardEnterThroughCountsReachesConfirm(t *testing.T) {
	t.Parallel()

	sw := NewSpawnWizard("test", 120, 30)
	sw.configMethod = "minimal"
	sw.step = SpawnStepCounts
	sw.initCountsForm()
	sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)

	for i := 0; i < 4; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}

	if sw.step != SpawnStepConfirm {
		t.Fatalf("expected four Enters to reach confirm step, got %v", sw.step)
	}
	if view := sw.View(); !strings.Contains(view, "Spawn 1 agent(s)") {
		t.Fatalf("expected confirm summary for one agent, got:\n%s", view)
	}
}
