package agent

import "testing"

// Real captured Claude Code idle screens from a live swarm (2026-06-14). After a
// turn completes, modern Claude Code parks the pane showing its input box plus a
// "new task? /clear to save <N> tokens" footer hint. The input box's ❯ chevron
// may be empty (with an … placeholder) or hold queued, unsent text. None of these
// matched the pre-fix idle patterns (the ❯…$ pattern needs an *empty* chevron;
// the ^.{0,40}\?$ pattern needs the ? at line-end), so an idle agent classified
// as neither idle nor working — invisible to autonomous dispatch.
var ccNewTaskIdleScreens = map[string]string{
	"queued-text-in-box": "" +
		"────────────────────────────────────────\n" +
		"❯ push it and sync the skills repo\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on          ·\n" +
		"  new task? /clear to save 200.7k tokens\n",
	"ellipsis-placeholder-box": "" +
		"❯ …\n" +
		" \n" +
		"───────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on         ·\n" +
		"  new task? /clear to save 323.7k tokens\n",
	"next-ready-bead-queued": "" +
		"────────────────────────────────────────────────────────────────────\n" +
		"❯ next ready bead\n" +
		"────────────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
		"                              new task? /clear to save 113.1k tokens\n",
}

// A real working-state screen: the live spinner footer. The "new task?" hint is
// NOT present during work — it renders only after the turn completes — so keying
// idle detection on that hint cannot false-positive over active work.
const ccWorkingSpinnerScreen = "" +
	"  Let me check the dispatcher invariants before I close this.\n" +
	"\n" +
	"✻ Noodling… (4m 8s · ↓ 6.2k tokens · thought for 14s)\n"

func TestClaudeCodeNewTaskHintIsIdle(t *testing.T) {
	p := NewParser()
	for name, screen := range ccNewTaskIdleScreens {
		t.Run(name, func(t *testing.T) {
			st, err := p.ParseWithHint(screen, AgentTypeClaudeCode)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !st.IsIdle {
				t.Errorf("idle 'new task?' screen classified IsIdle=false (want true); IsWorking=%v IsMenuBlocked=%v", st.IsWorking, st.IsMenuBlocked)
			}
			if st.IsWorking {
				t.Errorf("idle 'new task?' screen classified IsWorking=true (want false)")
			}
		})
	}
}

func TestClaudeCodeWorkingSpinnerNotIdle(t *testing.T) {
	p := NewParser()
	st, err := p.ParseWithHint(ccWorkingSpinnerScreen, AgentTypeClaudeCode)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.IsIdle {
		t.Errorf("active spinner screen classified IsIdle=true (want false) — false-idle would let dispatch interrupt working agent")
	}
}
