package robot

import "testing"

// realIdleScrollback is the exact 20-line window the dispatch/status path
// captures (LinesStatusDetection) from a real idle Claude pane on 2026-06-14.
// The turn finished long ago — a completed spinner ("✶ Pontificating… (16m 20s)"
// / "✻ Baked for 16m 20s") is still in the window, with the idle input box and
// the "new task? /clear to save …" footer hint below it.
const realIdleScrollback = "" +
	"  thing, which also deconflicted it from\n" +
	"  the live GentleCompass pane. All\n" +
	"  commits are on local main, committed\n" +
	"  as GentleCompass — not pushed (you\n" +
	"  didn't ask to sync). Say the word and\n" +
	"  I'll run the push + the skills-repo\n" +
	"  sync.\n" +
	"\n" +
	"✶ Pontificating… (16m 20s)\n" +
	"  ⎿  Tip: Use /btw to ask a quick side\n" +
	"     question without interrupting\n" +
	"✻ Baked for 16m 20s\n" +
	"\n" +
	"────────────────────────────────────────\n" +
	"❯ push it and sync the skills repo\n" +
	"────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on          ·\n" +
	"  new task? /clear to save 200.7k tokens\n"

// genuinelyWorkingScrollback: the active spinner is the lowest meaningful line —
// the agent is really working. IsLiveBusy must stay true here.
const genuinelyWorkingScrollback = "" +
	"  Let me check the dispatcher invariants before closing this out.\n" +
	"────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────\n" +
	"✻ Noodling… (2m 5s · ↓ 6.2k tokens · thought for 7s)\n"

func TestIsLiveBusyStaleSpinnerAboveIdle(t *testing.T) {
	if IsLiveBusy(realIdleScrollback, "claude") {
		t.Errorf("IsLiveBusy=true for a finished pane whose idle 'new task?' prompt is below a stale spinner; want false (this stalled autonomous dispatch)")
	}
}

func TestIsLiveBusyGenuineWork(t *testing.T) {
	if !IsLiveBusy(genuinelyWorkingScrollback, "claude") {
		t.Errorf("IsLiveBusy=false for an active spinner below the prompt; want true (must not dispatch into a busy pane)")
	}
}

func TestClassifyIdleNotThinking(t *testing.T) {
	sc := NewStateClassifier("test", nil)
	act, err := sc.ClassifyWithOutput(realIdleScrollback)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if act.State == StateThinking {
		t.Errorf("ntm activity would report THINKING for a finished idle pane; want non-thinking (got %v)", act.State)
	}
}
