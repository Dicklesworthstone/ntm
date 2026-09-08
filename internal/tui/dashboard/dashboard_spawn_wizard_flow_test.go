package dashboard

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

// dashboardCmdTimeout bounds how long a single command may run while a test
// pumps the dashboard. Tick and cursor-blink commands sleep and carry nothing
// the spawn wizard depends on, so they are dropped.
const dashboardCmdTimeout = 250 * time.Millisecond

// collectDashboardMsgs runs cmd the way tea.Program would - expanding batches
// and sequences - and returns the messages it produced.
func collectDashboardMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(dashboardCmdTimeout):
		return nil
	}
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectDashboardMsgs(c)...)
		}
		return out
	}
	// tea.Sequence yields an unexported []tea.Cmd message type.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				out = append(out, collectDashboardMsgs(c)...)
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

// pumpDashboard delivers every message produced by cmd (and by the commands
// those deliveries return) to the dashboard model, bounded by depth so a
// self-renewing command cannot spin forever. Dashboard tick and spawn
// execution results are dropped so the test never touches tmux, and so the
// add-agents runner is observed only through the stub installed by the test.
func pumpDashboard(t *testing.T, m Model, cmd tea.Cmd, depth int) Model {
	t.Helper()
	if depth > 16 {
		return m
	}
	for _, msg := range collectDashboardMsgs(cmd) {
		switch msg.(type) {
		case DashboardTickMsg, SpawnWizardExecResultMsg:
			continue
		}
		updated, next := m.Update(msg)
		m = updated.(Model)
		m = pumpDashboard(t, m, next, depth+1)
	}
	return m
}

func pressDashboard(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	updated, cmd := m.Update(key)
	m = updated.(Model)
	return pumpDashboard(t, m, cmd, 0)
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// Regression for #318: the wizard's huh form completes through its own
// internal messages (next-field / next-group), which the form returns as
// commands. The dashboard routed only key presses to the overlay and dropped
// everything else, so selecting "Minimal" and pressing Enter never left the
// method step. Driving the real dashboard model with the same event loop
// semantics as tea.Program must reach the counts form.
func TestDashboardSpawnWizardMinimalEnterAdvancesToCounts(t *testing.T) {
	m := newTestModel(140)

	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyCtrlW})
	if !m.showSpawnWizard || m.spawnWizard == nil {
		t.Fatal("expected spawn wizard overlay to open")
	}

	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if !m.showSpawnWizard || m.spawnWizard == nil {
		t.Fatal("wizard must stay open after choosing a method")
	}
	view := status.StripANSI(m.View())
	if !strings.Contains(view, "Claude agents (cc)") {
		t.Fatalf("expected wizard to advance to the counts form, got:\n%s", view)
	}
	if !strings.Contains(view, "✓ Method") {
		t.Fatalf("expected step indicator to mark Method complete, got:\n%s", view)
	}
}

// The complete Minimal flow through the dashboard - method, counts, confirm -
// must run the add-agents command exactly once with the pre-filled counts.
// Forwarding form messages to the overlay must never bounce the wizard's own
// completion message back into a finished form and spawn twice.
func TestDashboardSpawnWizardMinimalFlowRunsAddOnce(t *testing.T) {
	oldRun := dashboardRunAddAgents
	defer func() { dashboardRunAddAgents = oldRun }()

	var (
		calls     int
		gotResult panels.SpawnWizardResult
	)
	dashboardRunAddAgents = func(ctx context.Context, projectDir, session string, result panels.SpawnWizardResult) (string, error) {
		calls++
		gotResult = result
		return "added", nil
	}

	m := newTestModel(140)
	m.projectDir = "/tmp/ntm-project"

	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyCtrlW})
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	// Four count inputs pre-filled by Minimal; Enter through each.
	for i := 0; i < 4; i++ {
		m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if view := status.StripANSI(m.View()); !strings.Contains(view, "Spawn 1 agent(s)") {
		t.Fatalf("expected confirm step summary, got:\n%s", view)
	}

	// "y" accepts the confirm prompt and submits it.
	m = pressDashboard(t, m, runeKey('y'))

	if m.showSpawnWizard || m.spawnWizard != nil {
		t.Fatal("expected wizard to close after confirmation")
	}
	if calls != 1 {
		t.Fatalf("add-agents runner called %d times, want exactly 1", calls)
	}
	if !gotResult.Confirmed || gotResult.CCCount != 1 || gotResult.CodCount != 0 || gotResult.GmiCount != 0 || gotResult.AgyCount != 0 {
		t.Fatalf("runner result = %+v, want confirmed cc=1 cod=0 gmi=0 agy=0", gotResult)
	}
}

// Resizing the terminal while the wizard is open must propagate to the
// overlay so later steps lay out against the current dimensions.
func TestDashboardSpawnWizardTracksWindowSize(t *testing.T) {
	m := newTestModel(140)
	m = pressDashboard(t, m, tea.KeyMsg{Type: tea.KeyCtrlW})
	if m.spawnWizard == nil {
		t.Fatal("expected wizard")
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)
	if m.spawnWizard == nil {
		t.Fatal("wizard must survive a resize")
	}
	if w, h := m.spawnWizard.Size(); w != 100 || h != 40 {
		t.Fatalf("expected wizard size 100x40 after resize, got %dx%d", w, h)
	}
	if cmd == nil {
		t.Fatal("resize must still schedule the repaint command")
	}
}
