package robot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/provider"
)

func TestGetProviderCapabilitiesRedactsConfiguredProfiles(t *testing.T) {
	configHash := strings.Repeat("a", 64)
	probeHash := strings.Repeat("b", 64)
	cfg := &config.Config{}
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{
		"zai-operator-glm": {
			Provider: "zai", AccountAlias: "operator", Model: "glm-5.3-flash",
			Endpoint: "https://api.z.ai/api/anthropic", Runtime: "claude-code", ConfigSHA256: configHash,
			Command: "claude", AutomationPolicy: provider.DefaultZAIAutomationPolicyName,
			ExactTargetOnly: true, ProbeRequired: true, ModelProbeState: "live_verified", ModelProbeReceiptSHA256: probeHash,
		},
		"invalid-profile-with-secret": {
			Provider: "zai", AccountAlias: "bad", Model: "glm", Endpoint: "https://example.invalid/?token=secret-token",
			Runtime: "claude-code", ConfigSHA256: configHash, Command: "do-not-leak-secret-command", AutomationPolicy: "secret-policy",
			ModelProbeState: "not-a-real-state",
		},
	}

	output, err := GetProviderCapabilities(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Success || !output.ConfigSupplied || len(output.ProviderProfiles) != 2 {
		t.Fatalf("output = %+v", output)
	}
	if output.Transports["xai_acp"].Completion != provider.EvidenceAuthoritative || !output.OfflineConformanceHarness.Available {
		t.Fatalf("transport/conformance capability = %+v", output)
	}
	if output.GrokAutomationPolicy.Name == "" || output.GrokAutomationPolicy.Sandbox != "read-only" || output.GrokAutomationPolicy.SHA256 == "" || output.GrokAutomationPolicy.AllowRuleCount == 0 || output.GrokAutomationPolicy.DenyRuleCount == 0 {
		t.Fatalf("Grok policy capability = %+v", output.GrokAutomationPolicy)
	}

	valid := output.ProviderProfiles[1]
	if valid.IdentityState != "valid" || valid.ProfileState != "live_probe_required" || valid.IdentitySHA256 == "" || !valid.ProbeRequired || !valid.ExactTargetOnly || valid.ModelProbeState != "live_verified" || valid.ModelProbeQualified {
		t.Fatalf("valid profile capability = %+v", valid)
	}
	invalid := output.ProviderProfiles[0]
	if invalid.IdentityState != "invalid" || invalid.ProfileState != "invalid" || invalid.IdentitySHA256 != "" || invalid.ModelProbeState != "unknown" || invalid.ModelProbeQualified {
		t.Fatalf("invalid profile capability = %+v", invalid)
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"zai-operator-glm", "invalid-profile-with-secret", "https://api.z.ai", "credential.example", "secret-token", "do-not-leak-secret-command", "secret-policy", "XAI_API_KEY", configHash, probeHash} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provider capability output leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestGetProviderCapabilitiesDoesNotCallConfiguredGrokProfileLaunchable(t *testing.T) {
	hash := strings.Repeat("a", 64)
	cfg := &config.Config{ProviderProfiles: map[string]config.ProviderProfileConfig{
		"xai-grok": {
			Provider: "xai", AccountAlias: "operator", Model: "grok-4.6",
			Endpoint: "https://api.x.ai/v1", Runtime: "grok", ConfigSHA256: hash,
			Command: "grok", AutomationPolicy: "grok-readonly-ci", ExactTargetOnly: true,
		},
	}}
	output, err := GetProviderCapabilities(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := output.ProviderProfiles[0].ProfileState; got != "operation_evidence_required" {
		t.Fatalf("configured Grok profile state = %q", got)
	}
}

func TestGetProviderCapabilitiesDoesNotPromoteTupleValidUnlaunchableProfile(t *testing.T) {
	hash := strings.Repeat("a", 64)
	cfg := &config.Config{ProviderProfiles: map[string]config.ProviderProfileConfig{
		"zai-valid-tuple-invalid-policy": {
			Provider: "zai", AccountAlias: "operator", Model: "glm-5.3-flash",
			Endpoint: "https://api.z.ai/api/anthropic", Runtime: "claude-code", ConfigSHA256: hash,
			Command: "claude", AutomationPolicy: "",
			ExactTargetOnly: true, ProbeRequired: true, ModelProbeState: "qualified", ModelProbeReceiptSHA256: hash,
		},
	}}

	output, err := GetProviderCapabilities(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profile := output.ProviderProfiles[0]
	if profile.IdentityState != "valid" || profile.IdentitySHA256 == "" {
		t.Fatalf("tuple evidence should remain independently valid: %+v", profile)
	}
	if profile.ProfileState != "invalid" || profile.ModelProbeQualified {
		t.Fatalf("unlaunchable profile was promoted: %+v", profile)
	}
}

func TestGetProviderCapabilitiesWithoutConfigUsesEmptyProfileArray(t *testing.T) {
	output, err := GetProviderCapabilities(nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.ConfigSupplied || output.ProviderProfiles == nil || len(output.ProviderProfiles) != 0 {
		t.Fatalf("nil-config receipt = %+v", output)
	}
}

func TestPrintProviderCapabilitiesEmitsRobotJSON(t *testing.T) {
	original := GetOutputFormat()
	SetOutputFormat(FormatJSON)
	t.Cleanup(func() { SetOutputFormat(original) })
	stdout, err := captureStdout(t, func() error { return PrintProviderCapabilities(nil) })
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProviderCapabilitiesOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode PrintProviderCapabilities output: %v; output=%q", err, stdout)
	}
	if !decoded.Success || decoded.ProviderProfiles == nil || decoded.OfflineConformanceHarness.Description == "" {
		t.Fatalf("printed output = %+v", decoded)
	}
}
