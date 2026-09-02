package robot

// Regression tests for GitHub issue #288: --robot-status reported every agent
// as busy long after the panes had gone idle. Each fresh CLI process started
// with an empty in-process activity tracker, so the first observation of every
// pane returned "changed just now, N lines" and the adapter classified it busy
// regardless of what the pane actually showed. The persisted projection was
// then rewritten with those readings on every invocation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func readAgentFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

func TestClassifyAgentState_NoOutputClockUsesTail(t *testing.T) {
	a := NewTmuxAdapter(DefaultTmuxAdapterConfig())
	idleClaude := readAgentFixture(t, "claude_idle_completed.txt")
	workingClaude := readAgentFixture(t, "claude_working_monitor.txt")

	tests := []struct {
		name  string
		agent Agent
		tail  string
		want  state.AgentState
	}{
		{
			name:  "idle claude prompt with no clock is idle, not busy",
			agent: Agent{Type: "claude", Pane: "%1", PID: 10},
			tail:  idleClaude,
			want:  state.AgentStateIdle,
		},
		{
			name:  "working claude spinner with no clock is busy",
			agent: Agent{Type: "claude", Pane: "%2", PID: 10},
			tail:  workingClaude,
			want:  state.AgentStateBusy,
		},
		{
			name:  "user shell at prompt is idle",
			agent: Agent{Type: "user", Pane: "%3", PID: 10},
			tail:  "ls\nfoo bar\n$ ",
			want:  state.AgentStateIdle,
		},
		{
			name:  "no clock and no tail is unknown",
			agent: Agent{Type: "claude", Pane: "%4", PID: 10},
			tail:  "",
			want:  state.AgentStateUnknown,
		},
		{
			name: "fresh line delta within busy window is busy regardless of tail",
			agent: Agent{
				Type: "claude", Pane: "%5", PID: 10,
				LastOutputTS: time.Now().Add(-time.Second), SecondsSinceOutput: 1, OutputLinesSinceLast: 3,
			},
			tail: idleClaude,
			want: state.AgentStateBusy,
		},
		{
			name: "idle prompt with an old clock is idle, not stalled",
			agent: Agent{
				Type: "claude", Pane: "%6", PID: 10,
				LastOutputTS: time.Now().Add(-10 * time.Minute), SecondsSinceOutput: 600,
			},
			tail: idleClaude,
			want: state.AgentStateIdle,
		},
		{
			name: "working chrome with an old clock is stalled",
			agent: Agent{
				Type: "claude", Pane: "%7", PID: 10,
				LastOutputTS: time.Now().Add(-10 * time.Minute), SecondsSinceOutput: 600,
			},
			tail: workingClaude,
			want: state.AgentStateError,
		},
		{
			name: "old clock and no decisive tail is idle by threshold",
			agent: Agent{
				Type: "claude", Pane: "%8", PID: 10,
				LastOutputTS: time.Now().Add(-60 * time.Second), SecondsSinceOutput: 60,
			},
			tail: "",
			want: state.AgentStateIdle,
		},
		{
			name: "recent clock without line delta and no tail is active",
			agent: Agent{
				Type: "claude", Pane: "%9", PID: 10,
				LastOutputTS: time.Now().Add(-10 * time.Second), SecondsSinceOutput: 10,
			},
			tail: "",
			want: state.AgentStateActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := tt.agent
			if got := a.classifyAgentState(&agent, tt.tail); got != tt.want {
				t.Fatalf("classifyAgentState() = %q, want %q", got, tt.want)
			}
			reason := a.classifyStateReason(&agent, tt.want, tt.tail)
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("classifyStateReason() empty for state %q", tt.want)
			}
		})
	}
}

// TestEnrichAgentStatus_FreshProcessDoesNotFabricateOutput is the exact #288
// shape: a brand-new process (empty tracker, no projection store) observes a
// pane sitting at its prompt. The enrichment must not invent a last-output
// timestamp or a line delta, and the adapter must therefore report idle.
func TestEnrichAgentStatus_FreshProcessDoesNotFabricateOutput(t *testing.T) {
	clearPaneStates()
	SetProjectionStore(nil)
	t.Cleanup(func() { SetProjectionStore(nil) })

	idleClaude := readAgentFixture(t, "claude_idle_completed.txt")
	agent := &Agent{PID: os.Getpid(), Pane: "%288", Type: "claude"}
	enrichAgentStatus(agent, "sess", "", idleClaude)

	if !agent.LastOutputTS.IsZero() {
		t.Fatalf("LastOutputTS = %v, want zero on first observation", agent.LastOutputTS)
	}
	if agent.OutputLinesSinceLast != 0 || agent.SecondsSinceOutput != 0 {
		t.Fatalf("lines=%d seconds=%d, want 0/0 on first observation", agent.OutputLinesSinceLast, agent.SecondsSinceOutput)
	}

	a := NewTmuxAdapter(DefaultTmuxAdapterConfig())
	ra := a.NormalizeAgent("sess", agent, idleClaude)
	if ra.State != state.AgentStateIdle {
		t.Fatalf("NormalizeAgent state = %q (%s), want idle", ra.State, ra.StateReason)
	}

	// A real change seen by the same process still counts as output.
	enrichAgentStatus(agent, "sess", "", idleClaude+"\nmore output\n")
	if agent.LastOutputTS.IsZero() || agent.OutputLinesSinceLast == 0 {
		t.Fatalf("in-process change not tracked: ts=%v lines=%d", agent.LastOutputTS, agent.OutputLinesSinceLast)
	}
}

// TestEnrichAgentStatus_DurableSequenceSuppliesClockAcrossProcesses covers the
// projection-store path: each "process" (simulated by clearing the in-process
// tracker) inherits the last-change clock from the durable output sequence
// instead of restarting from "changed just now".
func TestEnrichAgentStatus_DurableSequenceSuppliesClockAcrossProcesses(t *testing.T) {
	store := newProjectionTestStore(t)
	SetProjectionStore(store)
	t.Cleanup(func() { SetProjectionStore(nil) })

	idleClaude := readAgentFixture(t, "claude_idle_completed.txt")
	newAgent := func() *Agent { return &Agent{PID: os.Getpid(), Pane: "%289", Type: "claude"} }

	// Process 1: first observation ever. No baseline anywhere.
	clearPaneStates()
	agent := newAgent()
	enrichAgentStatus(agent, "sess", "", idleClaude)
	if !agent.LastOutputTS.IsZero() || agent.OutputLinesSinceLast != 0 {
		t.Fatalf("first observation ever: ts=%v lines=%d, want zero/0", agent.LastOutputTS, agent.OutputLinesSinceLast)
	}

	// Process 2: same content. Still no change ever recorded.
	clearPaneStates()
	agent = newAgent()
	enrichAgentStatus(agent, "sess", "", idleClaude)
	if !agent.LastOutputTS.IsZero() || agent.OutputLinesSinceLast != 0 {
		t.Fatalf("unchanged across processes: ts=%v lines=%d, want zero/0", agent.LastOutputTS, agent.OutputLinesSinceLast)
	}

	// Process 3: content changed since the previous observer looked. That is
	// real output evidence and must produce a clock plus a change marker.
	clearPaneStates()
	agent = newAgent()
	enrichAgentStatus(agent, "sess", "", idleClaude+"\nnew line\n")
	if agent.LastOutputTS.IsZero() || agent.OutputLinesSinceLast != 1 {
		t.Fatalf("changed across processes: ts=%v lines=%d, want recent/1", agent.LastOutputTS, agent.OutputLinesSinceLast)
	}
	if agent.SecondsSinceOutput > 5 {
		t.Fatalf("SecondsSinceOutput = %d, want recent", agent.SecondsSinceOutput)
	}

	// Process 4: same content again. The clock carries over, the delta does not.
	clearPaneStates()
	agent = newAgent()
	enrichAgentStatus(agent, "sess", "", idleClaude+"\nnew line\n")
	if agent.LastOutputTS.IsZero() || agent.OutputLinesSinceLast != 0 {
		t.Fatalf("unchanged after change: ts=%v lines=%d, want carried clock/0", agent.LastOutputTS, agent.OutputLinesSinceLast)
	}
}

// TestProjectionStatusIdlePanesAreNotReportedBusy drives the full projection
// path with synthetic pane states shaped like the #288 report: a fresh
// process, several idle agent panes, one user pane, one working pane. The
// persisted rows and the status summary built from them must agree with the
// live classifier instead of reporting every agent busy.
func TestProjectionStatusIdlePanesAreNotReportedBusy(t *testing.T) {
	store := newProjectionTestStore(t)
	SetProjectionStore(store)
	t.Cleanup(func() { SetProjectionStore(nil) })
	clearPaneStates()

	idleClaude := readAgentFixture(t, "claude_idle_completed.txt")
	workingClaude := readAgentFixture(t, "claude_working_monitor.txt")

	panes := []struct {
		id, typ, tail string
	}{
		{"%10", "claude", idleClaude},
		{"%11", "claude", idleClaude},
		{"%12", "codex", "some earlier output\n› "},
		{"%13", "user", "make test\nok\n$ "},
		{"%14", "claude", workingClaude},
	}

	sess := tmux.Session{Name: "sweet--canary"}
	agents := make([]Agent, 0, len(panes))
	tails := make(map[string]string, len(panes))
	for _, p := range panes {
		agent := Agent{PID: os.Getpid(), Pane: p.id, Type: p.typ}
		enrichAgentStatus(&agent, sess.Name, "", p.tail)
		agents = append(agents, agent)
		tails[p.id] = p.tail
	}

	adapter := NewTmuxAdapter(DefaultTmuxAdapterConfig())
	snapshot := adapter.NormalizeSnapshot([]tmux.Session{sess}, map[string][]Agent{sess.Name: agents}, map[string]map[string]string{sess.Name: tails})

	collectedAt := time.Now().UTC()
	expiresAt := collectedAt.Add(time.Hour)
	for _, row := range buildRuntimeSessionRows(snapshot, collectedAt, expiresAt) {
		if err := store.UpsertRuntimeSession(row); err != nil {
			t.Fatalf("UpsertRuntimeSession: %v", err)
		}
	}
	for _, row := range buildRuntimeAgentRows(snapshot, collectedAt, expiresAt) {
		if err := store.UpsertRuntimeAgent(row); err != nil {
			t.Fatalf("UpsertRuntimeAgent: %v", err)
		}
	}

	withProjectionLiveSessions(t, func() ([]tmux.Session, error) {
		t.Fatal("fresh projection must not trigger a live tmux check")
		return nil, nil
	})
	output, err := buildProjectionBackedStatus(store, nil, PaginationOptions{})
	if err != nil {
		t.Fatalf("buildProjectionBackedStatus: %v", err)
	}

	got := output.Summary.AgentsByState
	if got["busy"] != 1 {
		t.Fatalf("busy = %d, want 1 (only the working pane); by_state=%v", got["busy"], got)
	}
	if got["idle"] != 4 {
		t.Fatalf("idle = %d, want 4; by_state=%v", got["idle"], got)
	}
	if output.Summary.TotalAgents != len(panes) {
		t.Fatalf("total_agents = %d, want %d", output.Summary.TotalAgents, len(panes))
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].IdleAgents != 4 || snapshot.Sessions[0].ActiveAgents != 1 {
		t.Fatalf("session counts = %+v, want idle=4 active=1", snapshot.Sessions[0])
	}
}
