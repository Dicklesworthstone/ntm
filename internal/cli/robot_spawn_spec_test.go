package cli

import (
	"strings"
	"testing"
)

// TestParseRobotSpawnAgentFlag covers the count[:model[:effort]] grammar the
// robot spawn flags share with `ntm spawn --cc/--cod/...` (bd-rr8gn).
func TestParseRobotSpawnAgentFlag(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		value      string
		agentType  AgentType
		wantCount  int
		wantModel  string
		wantEffort string
		wantErr    string
	}{
		{name: "empty means unset", flag: "--spawn-cc", value: "", agentType: AgentTypeClaude, wantCount: 0},
		{name: "whitespace only means unset", flag: "--spawn-cc", value: "  ", agentType: AgentTypeClaude, wantCount: 0},
		{name: "plain int", flag: "--spawn-cc", value: "2", agentType: AgentTypeClaude, wantCount: 2},
		{name: "plain zero preserved", flag: "--spawn-cod", value: "0", agentType: AgentTypeCodex, wantCount: 0},
		{name: "negative int passes through for robot validation", flag: "--spawn-cc", value: "-1", agentType: AgentTypeClaude, wantCount: -1},
		{name: "count and model", flag: "--spawn-cod", value: "3:gpt-5.3-codex", agentType: AgentTypeCodex, wantCount: 3, wantModel: "gpt-5.3-codex"},
		{name: "count model colon effort", flag: "--spawn-cod", value: "8:gpt-5.6-terra:high", agentType: AgentTypeCodex, wantCount: 8, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "count model at effort", flag: "--spawn-cod", value: "8:gpt-5.6-terra@high", agentType: AgentTypeCodex, wantCount: 8, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "claude model and effort", flag: "--spawn-cc", value: "2:opus:high", agentType: AgentTypeClaude, wantCount: 2, wantModel: "opus", wantEffort: "high"},
		{name: "grok at effort", flag: "--spawn-grok", value: "1:grok-4@low", agentType: AgentTypeGrok, wantCount: 1, wantModel: "grok-4", wantEffort: "low"},
		{name: "gmi model only", flag: "--spawn-gmi", value: "1:gemini-3.1-pro", agentType: AgentTypeGemini, wantCount: 1, wantModel: "gemini-3.1-pro"},
		// gmi has no effort knob, so '@' stays a literal model character —
		// identical to `ntm spawn --gmi` (agentTypeSupportsEffortSuffix).
		{name: "gmi at stays in model", flag: "--spawn-gmi", value: "1:provider/model@tag", agentType: AgentTypeGemini, wantCount: 1, wantModel: "provider/model@tag"},
		{name: "agy model override rejected", flag: "--spawn-agy", value: "1:gemini-3.1-pro-high", agentType: AgentTypeAntigravity, wantErr: "model is pinned"},
		{name: "invalid count", flag: "--spawn-cod", value: "x:gpt-5.3-codex", agentType: AgentTypeCodex, wantErr: "invalid count"},
		{name: "zero count with model rejected", flag: "--spawn-cod", value: "0:gpt-5.3-codex", agentType: AgentTypeCodex, wantErr: "count must be at least 1"},
		{name: "empty model", flag: "--spawn-cod", value: "2:", agentType: AgentTypeCodex, wantErr: "empty model"},
		{name: "empty effort", flag: "--spawn-cod", value: "2:model:", agentType: AgentTypeCodex, wantErr: "empty reasoning effort"},
		{name: "double effort rejected", flag: "--spawn-cod", value: "2:model@high:low", agentType: AgentTypeCodex, wantErr: "reasoning effort twice"},
		{name: "bad model charset", flag: "--spawn-cc", value: "2:bad model", agentType: AgentTypeClaude, wantErr: "invalid characters in model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := parseRobotSpawnAgentFlag(tt.flag, tt.value, tt.agentType)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRobotSpawnAgentFlag(%q)=%+v, want error containing %q", tt.value, spec, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.flag) && tt.value != "" {
					t.Fatalf("error %q does not name the offending flag %s", err.Error(), tt.flag)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRobotSpawnAgentFlag(%q): %v", tt.value, err)
			}
			if spec.Count != tt.wantCount || spec.Model != tt.wantModel || spec.ReasoningEffort != tt.wantEffort {
				t.Fatalf("parseRobotSpawnAgentFlag(%q)={Count:%d Model:%q Effort:%q}, want {%d %q %q}",
					tt.value, spec.Count, spec.Model, spec.ReasoningEffort, tt.wantCount, tt.wantModel, tt.wantEffort)
			}
		})
	}
}
