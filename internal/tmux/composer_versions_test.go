package tmux

import "testing"

// Frames captured from the deployed composers on iris on 2026-09-02. These
// are contract fixtures: coordinator delivery must recognize the exact Codex
// 0.152.x and Claude Code 2.1.x footer/composer layouts.
const (
	codex0152IdleFrame = "• Working (1m 4s • esc to interrupt)\n\n\n› Ask Codex to do anything\n\n  gpt-5.6-sol high · /data/projects/agent-factory\n"
	codex0152HeldFrame = "• Working (1m 4s • esc to interrupt)\n\n\n› BUILD DISPATCH agent-factory-qt1k\n\n  gpt-5.6-sol high · /data/projects/agent-factory\n"
	claude21IdleFrame  = "────────────────────────────────────────\n❯\u00a0\n────────────────────────────────────────\n  ⏵⏵ bypass permissions on · shift+tab to cycle\n"
	claude21HeldFrame  = "────────────────────────────────────────\n❯\u00a0BUILD DISPATCH agent-factory-qt1k\n────────────────────────────────────────\n  ⏵⏵ bypass permissions on · shift+tab to cycle\n"
)

func TestDeployedComposerFramesCodex0152AndClaude21(t *testing.T) {
	tests := []struct {
		name, frame, message string
		agent                AgentType
		holds                bool
	}{
		{"codex 0.152 idle", codex0152IdleFrame, "BUILD DISPATCH agent-factory-qt1k", AgentCodex, false},
		{"codex 0.152 held", codex0152HeldFrame, "BUILD DISPATCH agent-factory-qt1k", AgentCodex, true},
		{"claude 2.1 idle nonbreaking space", claude21IdleFrame, "BUILD DISPATCH agent-factory-qt1k", AgentClaude, false},
		{"claude 2.1 held nonbreaking space", claude21HeldFrame, "BUILD DISPATCH agent-factory-qt1k", AgentClaude, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !composerVisibleForDelivery(test.frame, composerMarkersForAgent(test.agent)) {
				t.Fatalf("deployed %s frame was not recognized as a composer", test.agent)
			}
			var holds bool
			switch test.agent.Canonical() {
			case AgentCodex:
				holds = codexComposerHoldsPayload(test.frame, test.message)
			case AgentClaude:
				holds = claudeComposerHoldsPayload(test.frame, test.message)
			}
			if holds != test.holds {
				t.Fatalf("payload detection = %v, want %v", holds, test.holds)
			}
		})
	}
}
