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
	if sw.ccStr != "1" || sw.codStr != "0" || sw.gmiStr != "0" || sw.agyStr != "0" || sw.grokStr != "0" || sw.ompStr != "0" || sw.ocStr != "0" {
		t.Fatalf("expected minimal pre-fill cc=1 and all others 0, got cc=%q cod=%q gmi=%q agy=%q grok=%q omp=%q oc=%q",
			sw.ccStr, sw.codStr, sw.gmiStr, sw.agyStr, sw.grokStr, sw.ompStr, sw.ocStr)
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

	for i := 0; i < 12; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}

	if sw.step != SpawnStepConfirm {
		t.Fatalf("expected twelve Enters to reach confirm step, got %v", sw.step)
	}
	view := sw.View()
	if !strings.Contains(view, "Spawn 1 agent(s)") {
		t.Fatalf("expected confirm summary for one agent, got:\n%s", view)
	}
	if !strings.Contains(view, "Claude: 1, Codex: 0, Gemini: 0, Antigravity: 0") ||
		!strings.Contains(view, "Grok: 0, Omp: 0, Pi: 0, OpenCode: 0") ||
		!strings.Contains(view, "Cursor: 0, Windsurf: 0, Aider: 0, Ollama: 0") {
		t.Fatalf("expected confirm summary to list all 12 harnesses, got:\n%s", view)
	}
}

func TestSpawnWizardMultiHarnessManualAndPresetCounts(t *testing.T) {
	t.Parallel()

	t.Run("quick and minimal presets reset multi-harness counts", func(t *testing.T) {
		sw := NewSpawnWizard("test", 120, 40)
		sw.grokStr = "3"
		sw.ompStr = "2"
		sw.ocStr = "4"

		sw.configMethod = "quick"
		sw.initCountsForm()
		if got := sw.currentResult(); got.CCCount != 2 || got.CodCount != 1 || got.GmiCount != 0 || got.AgyCount != 1 || got.GrokCount != 0 || got.OmpCount != 0 || got.OcCount != 0 || got.TotalCount() != 4 {
			t.Fatalf("quick preset counts = %+v (total=%d), want cc=2 cod=1 agy=1 total=4", got, got.TotalCount())
		}

		sw.grokStr = "5"
		sw.ompStr = "1"
		sw.ocStr = "2"
		sw.configMethod = "minimal"
		sw.initCountsForm()
		if got := sw.currentResult(); got.CCCount != 1 || got.CodCount != 0 || got.GmiCount != 0 || got.AgyCount != 0 || got.GrokCount != 0 || got.OmpCount != 0 || got.OcCount != 0 || got.TotalCount() != 1 {
			t.Fatalf("minimal preset counts = %+v (total=%d), want cc=1 total=1", got, got.TotalCount())
		}
	})

	t.Run("manual mode rejects all-zero counts across all twelve harnesses", func(t *testing.T) {
		sw := NewSpawnWizard("test", 120, 40)
		sw.configMethod = "manual"
		sw.step = SpawnStepCounts
		sw.initCountsForm()
		sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)

		for i := 0; i < 12; i++ {
			sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
		}

		if sw.step != SpawnStepCounts {
			t.Fatalf("expected all-zero counts to remain on counts step, got %v", sw.step)
		}
		if sw.err == nil || !strings.Contains(sw.err.Error(), "at least one agent is required") {
			t.Fatalf("expected validation error for zero total agents, got %v", sw.err)
		}
	})

	t.Run("manual multi-harness counts complete and emit SpawnWizardDoneMsg", func(t *testing.T) {
		sw := NewSpawnWizard("test", 120, 40)
		sw.configMethod = "manual"
		sw.grokStr = "2"
		sw.ompStr = "1"
		sw.piStr = "1"
		sw.ocStr = "3"
		sw.step = SpawnStepCounts
		sw.initCountsForm()
		sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)

		for i := 0; i < 12; i++ {
			sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
		}

		if sw.step != SpawnStepConfirm {
			t.Fatalf("expected manual multi-harness counts to advance to confirm, got %v (err=%v)", sw.step, sw.err)
		}
		confirmView := sw.View()
		if !strings.Contains(confirmView, "Spawn 7 agent(s)") || !strings.Contains(confirmView, "Grok: 2, Omp: 1, Pi: 1, OpenCode: 3") {
			t.Fatalf("unexpected confirm summary:\n%s", confirmView)
		}

		// Accept confirmation ("y") and capture SpawnWizardDoneMsg.
		var doneMsg *SpawnWizardDoneMsg
		model, cmd := sw.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		sw = model.(*SpawnWizard)
		for depth := 0; depth < 8 && cmd != nil && doneMsg == nil; depth++ {
			var nextCmds []tea.Cmd
			for _, msg := range collectWizardMsgs(cmd) {
				if dm, ok := msg.(SpawnWizardDoneMsg); ok {
					doneMsg = &dm
					break
				}
				m2, next := sw.Update(msg)
				sw = m2.(*SpawnWizard)
				if next != nil {
					nextCmds = append(nextCmds, next)
				}
			}
			if len(nextCmds) > 0 {
				cmd = tea.Batch(nextCmds...)
			} else {
				cmd = nil
			}
		}

		if doneMsg == nil {
			t.Fatal("expected SpawnWizardDoneMsg after confirming wizard")
		}
		if doneMsg.Cancelled || !doneMsg.Result.Confirmed {
			t.Fatalf("expected confirmed wizard result, got %+v", doneMsg)
		}
		if doneMsg.Result.GrokCount != 2 || doneMsg.Result.OmpCount != 1 || doneMsg.Result.PiCount != 1 || doneMsg.Result.OcCount != 3 || doneMsg.Result.TotalCount() != 7 {
			t.Fatalf("doneMsg.Result = %+v (total=%d), want grok=2 omp=1 pi=1 oc=3 total=7", doneMsg.Result, doneMsg.Result.TotalCount())
		}
	})
}

func TestSpawnWizardViewMultiHarnessTerminalSizes(t *testing.T) {
	t.Parallel()

	sizes := []struct {
		name   string
		width  int
		height int
	}{
		{name: "standard_120x40", width: 120, height: 40},
		{name: "compact_80x24", width: 80, height: 24},
	}

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			sw := NewSpawnWizard("test-session", sz.width, sz.height)
			sw.configMethod = "manual"
			sw.grokStr = "1"
			sw.ompStr = "2"
			sw.ocStr = "1"
			sw.step = SpawnStepCounts
			sw.initCountsForm()
			sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)

			topView := sw.View()
			if topView == "" || !strings.Contains(topView, "Spawn Wizard") {
				t.Fatalf("expected non-empty counts view at %dx%d", sz.width, sz.height)
			}
			if lines := strings.Count(topView, "\n") + 1; lines > sz.height {
				t.Fatalf("counts view height %d exceeded terminal height %d:\n%s", lines, sz.height, topView)
			}

			// Step down to the final Ollama field to verify viewport scrolling renders cleanly.
			for i := 0; i < 11; i++ {
				sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
			}
			bottomView := sw.View()
			if !strings.Contains(bottomView, "Ollama agents (ollama)") {
				t.Fatalf("expected scrolled counts view at %dx%d to display Ollama input, got:\n%s", sz.width, sz.height, bottomView)
			}
			if lines := strings.Count(bottomView, "\n") + 1; lines > sz.height {
				t.Fatalf("scrolled counts view height %d exceeded terminal height %d:\n%s", lines, sz.height, bottomView)
			}

			// Advance to confirm step and verify confirm view fits too.
			sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
			if sw.step != SpawnStepConfirm {
				t.Fatalf("expected confirm step at %dx%d, got %v", sz.width, sz.height, sw.step)
			}
			confirmView := sw.View()
			if !strings.Contains(confirmView, "Grok: 1, Omp: 2, Pi: 0, OpenCode: 1") {
				t.Fatalf("expected multi-harness confirm summary at %dx%d, got:\n%s", sz.width, sz.height, confirmView)
			}
			if lines := strings.Count(confirmView, "\n") + 1; lines > sz.height {
				t.Fatalf("confirm view height %d exceeded terminal height %d:\n%s", lines, sz.height, confirmView)
			}
		})
	}
}
