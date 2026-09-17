package status

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

const (
	ocIdle    = "╭────────────╮\n│ Ask anything... \"fix the failing test\" │\n╰────────────╯\n"
	ocWorking = "⠹ Thinking\n╭────────────╮\n│            │\n╰────────────╯\n  esc interrupt\n"
	// A fictional plugin agent ("pix"): the generic heuristics recognise
	// neither its ready bar nor its "[stop]" hint. (omp, whose chrome this test
	// used to borrow, is a built-in type now; see TestDetermineState_Omp.)
	pixIdle = "Connected to MCP servers.\n┃ pix · awaiting the next instruction ┃\n"
	pixWork = "prompt\n ⠙ thinking [stop]\n┃ pix · awaiting the next instruction ┃\n"
)

func TestDetectIdleFromOutput_Opencode(t *testing.T) {
	if !DetectIdleFromOutput(ocIdle, "oc") {
		t.Fatal("empty OpenCode composer must read as idle")
	}
	if !DetectIdleFromOutput(ocIdle, "opencode") {
		t.Fatal("alias must canonicalize to oc")
	}
	if DetectIdleFromOutput(ocWorking, "oc") {
		t.Fatal("esc interrupt footer must veto idle")
	}
	if DetectIdleFromOutput(ocIdle+"  esc interrupt\n", "oc") {
		t.Fatal("footer below the hint text must veto idle")
	}
	// A bare shell prompt in an oc pane means the agent exited, not idle.
	if DetectIdleFromOutput("user@host:~$", "oc") {
		t.Fatal("shell prompt in an oc pane must not read as idle")
	}
}

func TestDetermineState_Opencode(t *testing.T) {
	d := NewDetector()
	recent := time.Now()
	// Recent activity would normally force WORKING; the composer hint text
	// with no footer must still classify as idle (safe to dispatch).
	if state, _ := d.determineState(ocIdle, "oc", recent); state != StateIdle {
		t.Fatalf("idle composer state = %v, want idle", state)
	}
	if state, _ := d.determineState(ocWorking, "oc", recent.Add(-time.Hour)); state != StateWorking {
		t.Fatalf("in-flight state = %v, want working even at low velocity", state)
	}
	if c := observationConfidence(AgentStatus{State: StateIdle, AgentType: "oc"}, ocIdle); c < 0.9 {
		t.Fatalf("idle confidence = %v, want actionable", c)
	}
}

func TestPluginReadinessPatterns(t *testing.T) {
	// Before registration the pix bar is unknown to every heuristic.
	d := NewDetector()
	if state, _ := d.determineState(pixIdle, "pix", time.Now().Add(-time.Hour)); state == StateIdle {
		t.Fatal("unregistered plugin chrome must not read as idle (nothing recognises it)")
	}

	if err := agent.RegisterPlugin("pix", []string{`^\s*┃ pix · awaiting the next instruction ┃\s*$`}, []string{`\[stop\]`}, nil); err != nil {
		t.Fatal(err)
	}
	if !DetectIdleFromOutput(pixIdle, "pix") {
		t.Fatal("declared idle pattern must be honoured")
	}
	if DetectIdleFromOutput(pixWork, "pix") {
		t.Fatal("declared working pattern must veto idle")
	}
	if state, _ := d.determineState(pixIdle, "pix", time.Now()); state != StateIdle {
		t.Fatalf("registered plugin idle state = %v, want idle despite recent activity", state)
	}
	if state, _ := d.determineState(pixWork, "pix", time.Now().Add(-time.Hour)); state != StateWorking {
		t.Fatalf("registered plugin working state = %v, want working", state)
	}
	if c := observationConfidence(AgentStatus{State: StateIdle, AgentType: "pix"}, pixIdle); c < 0.9 {
		t.Fatalf("plugin idle confidence = %v, want actionable", c)
	}
}
