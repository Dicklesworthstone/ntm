package agent

import "testing"

func TestAgentTypeZAIIsDistinctFromClaudeCode(t *testing.T) {
	if got := AgentType("zai").Canonical(); got != AgentTypeZAI {
		t.Fatalf("Canonical(zai) = %q, want %q", got, AgentTypeZAI)
	}
	if AgentTypeZAI.Canonical() == AgentTypeClaudeCode || !AgentTypeZAI.IsValid() {
		t.Fatalf("Z.ai must remain a valid, distinct provider lane")
	}
}
