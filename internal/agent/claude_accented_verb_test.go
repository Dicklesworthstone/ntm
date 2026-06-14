package agent

import "testing"

// Claude Code's spinner verbs include accented forms (Sautéed, Flambéed). The
// turn-ended completion-line regex must recognize them; otherwise a stale active
// spinner above an accented completion line leaves the finished pane falsely
// "working", withholding an idle agent from dispatch. Found in a Haiku stress run.
func TestClaudeActivelyWorking_AccentedCompletionVerb(t *testing.T) {
	idle := "" +
		"✶ Sketching… (30s)\n" +
		"* Sketching… (34s)\n" +
		"✻ Sautéed for 36s\n" +
		"────────\n❯ next bead\n────────\n  ⏵⏵ bypass permissions on          ·\n"
	if ClaudeActivelyWorking(idle) {
		t.Errorf("accented completion line not recognized as turn-ended; pane reads as working (false-working)")
	}
	working := "  thinking\n✻ Sautéing… (5s · ↓ 1.2k tokens)\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on          ·\n"
	if !ClaudeActivelyWorking(working) {
		t.Errorf("active accented spinner read as not-working")
	}
}

func TestClaudeIsTurnEndedLine_AccentedVerbs(t *testing.T) {
	for _, ln := range []string{"✻ Sautéed for 36s", "✶ Flambéed for 2m 5s", "✽ Baked for 16m 20s"} {
		if !claudeIsTurnEndedLine(ln) {
			t.Errorf("claudeIsTurnEndedLine(%q) = false; want true", ln)
		}
	}
	// Guard: lowercase prose must NOT match (capitalized-verb requirement preserved).
	if claudeIsTurnEndedLine("  ...we waited for 5m before retry") {
		t.Errorf("lowercase prose wrongly matched as a completion line")
	}
}
