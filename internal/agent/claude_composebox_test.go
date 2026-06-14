package agent

import "testing"

// A freshly-spawned Claude agent (init prompt prefilled in the box, no completion
// line / "new task?" hint yet) or a box showing only the "…" placeholder must read
// IDLE so the dispatcher can start the swarm. Upstream's empty-❯ / completion /
// new-task patterns miss these; the compose-box footer (gated on
// !ClaudeActivelyWorking) covers them. Found live: a fresh `ntm spawn` showed
// "Idle Agents: 0" and the swarm never started.
func TestDetectIdle_ComposeBoxFreshSpawn(t *testing.T) {
	p := NewParser()
	cases := map[string]string{
		"prefilled-init-box": "  Reread AGENTS.md and\n  continue from where\n────────\n❯ Reread AGENTS.md\n────────\n  ⏵⏵ bypass permissions on  ·\n",
		"ellipsis-placeholder": "────────\n❯ …\n────────\n  ⏵⏵ bypass permissions on  ·\n",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := p.ParseWithHint(s, AgentTypeClaudeCode)
			if !st.IsIdle {
				t.Errorf("compose-box state not idle (IsWorking=%v)", st.IsWorking)
			}
		})
	}
}

// The compose-box footer must NOT override active work: a live spinner with the
// same footer below stays working (the gate).
func TestDetectIdle_ComposeBoxDoesNotMaskWork(t *testing.T) {
	p := NewParser()
	w := "✻ Cooking… (5s · ctrl+c to interrupt)\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on  ·\n"
	st, _ := p.ParseWithHint(w, AgentTypeClaudeCode)
	if st.IsIdle {
		t.Errorf("active spinner + compose-box footer wrongly classified idle (false-idle would interrupt working agent)")
	}
}
