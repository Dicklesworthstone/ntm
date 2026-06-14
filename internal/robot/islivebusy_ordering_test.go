package robot

import "testing"

// IsLiveBusy must not pin a finished Claude pane to "busy" on a STALE spinner that
// sits above the turn-ended completion line. A naive CategoryThinking match does;
// for Claude we defer to the ordering-aware ClaudeActivelyWorking. Without this,
// determineAgentState overrides the correct idle verdict to "working" and the
// dispatcher sees 0 idle agents after every burst (swarm stalls). Found live (Haiku).
func TestIsLiveBusy_StaleSpinnerAboveCompletion(t *testing.T) {
	// stale spinner, THEN a completion line below it ⇒ idle ⇒ not busy.
	idle := "" +
		"· Thundering… (4s)\n" +
		"  ⎿  Tip: Run claude --continue\n" +
		"✻ Churned for 6s\n" +
		"────────\n❯ \n────────\n  ⏵⏵ bypass permissions on  ·\n"
	if IsLiveBusy(idle, "claude") {
		t.Errorf("IsLiveBusy=true on a finished pane (stale spinner above completion) — false-busy stalls dispatch")
	}
	// genuinely active spinner (most-recent dynamic marker) ⇒ busy.
	busy := "  reasoning\n✻ Thundering… (4s · ctrl+c to interrupt)\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on  ·\n"
	if !IsLiveBusy(busy, "claude") {
		t.Errorf("IsLiveBusy=false on a genuinely active spinner — would dispatch into a working agent")
	}
}
