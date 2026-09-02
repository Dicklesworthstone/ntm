package robot

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/provider"
)

func TestGetProviderConformanceRunsSyntheticHarnessWithoutCoordinationStores(t *testing.T) {
	cfg := &config.Config{ProviderProfiles: map[string]config.ProviderProfileConfig{
		"xai-primary": {
			Provider: "xai", AccountAlias: "test", Model: "grok-code", Endpoint: "https://api.x.ai/v1",
			Runtime: "grok", ConfigSHA256: strings.Repeat("a", 64), Command: "grok",
			AutomationPolicy: agent.DefaultGrokAutomationPolicyName, ExactTargetOnly: true,
		},
		"zai-primary": {
			Provider: "zai", AccountAlias: "test", Model: "glm-5.3-flash", Endpoint: "https://api.z.ai/api/anthropic",
			Runtime: "claude-code", ConfigSHA256: strings.Repeat("b", 64), Command: "claude",
			AutomationPolicy: provider.DefaultZAIAutomationPolicyName, ExactTargetOnly: true, ProbeRequired: true,
			ModelProbeState: "qualified", ModelProbeReceiptSHA256: strings.Repeat("c", 64),
		},
	}}
	for _, test := range []struct{ profile, transport string }{
		{"xai-primary", "xai_acp"},
		{"xai-primary", "xai_grok_tui"},
		{"zai-primary", "zai_claude_runtime"},
	} {
		out, err := GetProviderConformance(t.Context(), cfg, test.profile, test.transport)
		if err != nil || !out.Success || !out.Passed || out.Mode != "synthetic_offline" || out.Report.Coverage.Satisfied != 7 {
			t.Fatalf("%s output=%+v err=%v", test.transport, out, err)
		}
	}
}

func TestGetProviderConformanceRejectsProviderTransportMismatch(t *testing.T) {
	cfg := &config.Config{ProviderProfiles: map[string]config.ProviderProfileConfig{
		"xai-primary": {
			Provider: "xai", AccountAlias: "test", Model: "grok-code", Endpoint: "https://api.x.ai/v1",
			Runtime: "grok", ConfigSHA256: strings.Repeat("a", 64), Command: "grok",
			AutomationPolicy: agent.DefaultGrokAutomationPolicyName, ExactTargetOnly: true,
		},
	}}
	if _, err := GetProviderConformance(t.Context(), cfg, "xai-primary", "zai_claude_runtime"); err == nil {
		t.Fatal("provider/transport mismatch was accepted")
	}
}
