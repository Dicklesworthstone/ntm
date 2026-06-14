package status

import "testing"

// Real captured idle Claude screens (2026-06-14). The turn is finished — a
// completed spinner sits above an idle input box whose ❯ chevron holds queued
// text or an … placeholder, followed by the "new task? /clear to save …" footer
// hint. Before the fix, DetectIdleFromOutput returned false (no prompt pattern
// matched the queued-text box), so `ntm status` reported these idle panes as
// "working".
func TestDetectIdleNewTaskHint(t *testing.T) {
	cases := map[string]string{
		"queued-text-box": "" +
			"✶ Pontificating… (16m 20s)\n" +
			"✻ Baked for 16m 20s\n" +
			"\n" +
			"────────────────────────────────────────\n" +
			"❯ push it and sync the skills repo\n" +
			"────────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on          ·\n" +
			"  new task? /clear to save 200.7k tokens\n",
		"ellipsis-box": "" +
			"❯ …\n" +
			"───────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on         ·\n" +
			"  new task? /clear to save 323.7k tokens\n",
	}
	for name, screen := range cases {
		t.Run(name, func(t *testing.T) {
			if !DetectIdleFromOutput(screen, "cc") {
				t.Errorf("DetectIdleFromOutput=false, want true for idle 'new task?' screen")
			}
		})
	}
}

// A genuinely working screen: the active spinner is the lowest line. Must NOT
// classify as idle, or status would mislabel a busy agent as available.
func TestDetectIdleActiveSpinnerNotIdle(t *testing.T) {
	working := "" +
		"  Working through the dispatcher invariants.\n" +
		"❯ \n" +
		"✻ Noodling… (4m 8s · ↓ 6.2k tokens · thought for 14s)\n"
	if DetectIdleFromOutput(working, "cc") {
		t.Errorf("DetectIdleFromOutput=true for active-spinner screen, want false")
	}
}
