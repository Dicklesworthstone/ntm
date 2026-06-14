package agent
import "testing"

// realMenuFooters are footers captured live from Claude Code pickers. Each must
// classify the pane as menu-blocked so autonomous dispatch can Esc-recover it.
var realMenuFooters = map[string]string{
	"effort/list picker": "  5. Chat about this\n\nEnter to select · Tab/Arrow keys to navigate · Esc to cancel\n",
	"wrapped nav hint":   "  5. Chat about this\n\nEnter to select · Tab/Arrow keys to\nnavigate · Esc to cancel\n",
	"/model picker":      "  ◐ Medium effort ←/→ to adjust\n  Enter to set as default · s to use\n  this session only · Esc to cancel\n",
}

func TestInteractiveMenuDetection(t *testing.T) {
	for name, footer := range realMenuFooters {
		body := "  Some agent output above.\n────────────────────────────────────────\n" + footer
		st, _ := NewParser().ParseWithHint(body, AgentTypeClaudeCode)
		if !st.IsMenuBlocked {
			t.Errorf("[%s] IsMenuBlocked=false, want true", name)
		}
		if st.IsIdle || st.IsWorking {
			t.Errorf("[%s] should be neither idle nor working; idle=%v working=%v", name, st.IsIdle, st.IsWorking)
		}
		if got := st.GetRecommendation(); got != RecommendRecoverMenu {
			t.Errorf("[%s] recommendation=%v, want RECOVER_MENU", name, got)
		}
	}
}

func TestCleanIdleNotMenu(t *testing.T) {
	idle := "✻ Baked for 16m 20s\n\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on          ·\n"
	st, _ := NewParser().ParseWithHint(idle, AgentTypeClaudeCode)
	if st.IsMenuBlocked {
		t.Errorf("clean idle pane wrongly flagged IsMenuBlocked")
	}
	if !st.IsIdle {
		t.Errorf("clean idle pane should be idle; got idle=%v", st.IsIdle)
	}
}

func TestProseNotMenu(t *testing.T) {
	// Prose mentioning one phrase but not the menu structure must not match.
	for _, prose := range []string{
		"I will let you select the option and press Enter when ready.\n❯ \n",
		"Press Esc to cancel the operation if needed; otherwise continue.\n❯ \n",
	} {
		st, _ := NewParser().ParseWithHint(prose, AgentTypeClaudeCode)
		if st.IsMenuBlocked {
			t.Errorf("prose wrongly flagged as menu: %q", prose)
		}
	}
}
