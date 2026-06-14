package agent

import "testing"

// Real captured idle screens (2026-06-14) from a swarm processing the backlog:
// agents finished short turns in narrow 7-line panes, so the "new task?" hint is
// not visible and the ❯ box holds queued text or an … ellipsis. Only the
// compose-box footer ("⏵⏵ bypass permissions …") is present. These must classify
// idle (no active spinner) so the dispatcher can hand them the next bead;
// pre-fix they fell through to UNKNOWN and the backlog stalled with idle agents.
func TestClaudeComposeBoxIdle(t *testing.T) {
	cases := map[string]string{
		"queued-cmd-in-box": "" +
			"───────────────────────────────────────\n" +
			"❯ br ready\n" +
			"───────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on         ·\n",
		"ellipsis-only": "" +
			"────────────────────────────────────────\n" +
			"❯ …\n" +
			" \n" +
			"────────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on          ·\n",
		"completed-then-queued": "" +
			"✻ Baked for 1m 29s\n" +
			"────────────────────────────────────────\n" +
			"❯ next bead\n" +
			"────────────────────────────────────────\n" +
			"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n",
	}
	p := NewParser()
	for name, screen := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := p.ParseWithHint(screen, AgentTypeClaudeCode)
			if !st.IsIdle {
				t.Errorf("compose-box idle screen classified IsIdle=false (want true); IsWorking=%v", st.IsWorking)
			}
		})
	}
}

// The compose-box footer must NOT override an active spinner: a working pane has
// the same footer but with a live spinner, and must stay non-idle.
func TestClaudeComposeBoxWithActiveSpinnerNotIdle(t *testing.T) {
	working := "" +
		"  reasoning about the allocator…\n" +
		"✻ Noodling… (2m 5s · ↓ 6.2k tokens)\n" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on          ·\n"
	p := NewParser()
	st, _ := p.ParseWithHint(working, AgentTypeClaudeCode)
	if st.IsIdle {
		t.Errorf("active-spinner pane with compose-box footer classified idle; want not-idle (would interrupt working agent)")
	}
}
