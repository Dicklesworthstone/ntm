package agent

import "testing"

// Real working screen (2026-06-14): an active spinner ("· Processing… (2m 8s …)")
// is the live bottom dynamic line, with Claude's persistent EMPTY input box
// rendered below it. This is the case that produced the dangerous false-idle:
// the empty ❯ box is the lowest line, so any "idle prompt below the spinner"
// ordering misclassifies this busy pane as idle.
const ccWorkingSpinnerAboveEmptyBox = "" +
	"      probe|≤$|GCP|rung'\n" +
	"      docs/provenance/founder-decisions/\n" +
	"      21-mpk-hardware-acq…)\n" +
	"  ⎿  Waiting…\n" +
	"· Processing… (2m 8s · ↓ 7.2k tokens)\n" +
	"  ⎿  Tip: Use /btw to ask a quick side\n" +
	"     question without interrupting\n" +
	"· Processing… (2m 8s · ↑ 7.2k tokens)\n" +
	"  ⎿  Tip: Use /btw to ask a quick side\n" +
	"     question without interrupting\n" +
	"────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on          ·\n"

// Real idle screen: a STALE completed spinner ("✶ Pontificating… (16m 20s)") and
// a completion line ("✻ Baked for 16m 20s") sit above the input box, and the
// work-exclusive "new task? /clear" hint is the lowest dynamic marker.
const ccIdleStaleSpinnerThenNewTask = "" +
	"✶ Pontificating… (16m 20s)\n" +
	"  ⎿  Tip: Use /btw to ask a quick side\n" +
	"✻ Baked for 16m 20s\n" +
	"\n" +
	"────────────────────────────────────────\n" +
	"❯ push it and sync the skills repo\n" +
	"────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on          ·\n" +
	"  new task? /clear to save 200.7k tokens\n"

// Settled idle: agent finished, input box empty, no spinner, no new-task hint.
const ccIdleSettledEmptyBox = "" +
	"     hardware gate. Measurements stay\n" +
	"────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on          ·\n"

func TestClaudeActivelyWorking(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"active-spinner-above-empty-box", ccWorkingSpinnerAboveEmptyBox, true},
		{"idle-stale-spinner-then-new-task", ccIdleStaleSpinnerThenNewTask, false},
		{"idle-settled-empty-box", ccIdleSettledEmptyBox, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClaudeActivelyWorking(c.text); got != c.want {
				t.Errorf("ClaudeActivelyWorking = %v, want %v", got, c.want)
			}
		})
	}
}

// The spinner annotation "thought for 14s" inside an active spinner line must NOT
// be mistaken for a completion ("X for Ns") turn-ended marker.
func TestClaudeActivelyWorkingThoughtForAnnotation(t *testing.T) {
	text := "" +
		"  Reasoning about the dispatcher.\n" +
		"✻ Noodling… (4m 8s · ↓ 6.2k tokens · thought for 14s)\n" +
		"────────\n❯ \n────────\n"
	if !ClaudeActivelyWorking(text) {
		t.Errorf("active spinner with 'thought for 14s' annotation read as not-working; want working")
	}
}

// detectIdle must agree: working screen NOT idle, idle screens idle.
func TestDetectIdleUsesActivelyWorking(t *testing.T) {
	p := NewParser()
	mustIdle := func(name, text string, wantIdle bool) {
		t.Run(name, func(t *testing.T) {
			st, _ := p.ParseWithHint(text, AgentTypeClaudeCode)
			if st.IsIdle != wantIdle {
				t.Errorf("IsIdle=%v want %v", st.IsIdle, wantIdle)
			}
			if wantIdle && st.IsWorking {
				t.Errorf("IsWorking=true on an idle screen")
			}
		})
	}
	mustIdle("working", ccWorkingSpinnerAboveEmptyBox, false)
	mustIdle("idle-newtask", ccIdleStaleSpinnerThenNewTask, true)
	mustIdle("idle-settled", ccIdleSettledEmptyBox, true)
}
