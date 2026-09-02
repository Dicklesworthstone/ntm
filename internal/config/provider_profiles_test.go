package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/provider"
)

const providerProfileTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validZAIProviderProfile() ProviderProfileConfig {
	return ProviderProfileConfig{
		Provider:                "zai",
		AccountAlias:            "kevin-dev",
		Model:                   "glm-5.3-flash",
		Endpoint:                "https://api.z.ai/api/anthropic",
		Runtime:                 "claude-code",
		ConfigSHA256:            providerProfileTestHash,
		Command:                 "claude",
		AutomationPolicy:        provider.DefaultZAIAutomationPolicyName,
		ExactTargetOnly:         true,
		ProbeRequired:           true,
		ModelProbeState:         "qualified",
		ModelProbeReceiptSHA256: providerProfileTestHash,
	}
}

func TestProviderProfileBindsValidatedImmutableIdentity(t *testing.T) {
	profile := validZAIProviderProfile()
	identity, err := profile.Identity()
	if err != nil {
		t.Fatalf("Identity() error: %v", err)
	}
	if got, want := identity.Provider(), "zai"; got != want {
		t.Fatalf("provider = %q, want %q", got, want)
	}
	if got, want := identity.CapacityScope().String(), "provider:"+identity.Hash(); got != want {
		t.Fatalf("capacity scope = %q, want identity-specific scope", got)
	}
}

func TestValidateProviderProfilesRejectsUnqualifiedZAIAndAmbiguousTargets(t *testing.T) {
	profile := validZAIProviderProfile()
	profiles := map[string]ProviderProfileConfig{
		"zai-missing-gates": {
			Provider:         profile.Provider,
			AccountAlias:     profile.AccountAlias,
			Model:            profile.Model,
			Endpoint:         profile.Endpoint,
			Runtime:          profile.Runtime,
			ConfigSHA256:     profile.ConfigSHA256,
			Command:          profile.Command,
			AutomationPolicy: profile.AutomationPolicy,
		},
		"claude": profile,
	}

	errs := ValidateProviderProfiles(profiles)
	joined := errorsString(errs)
	for _, want := range []string{"exact_target_only", "probe_required", "ambiguous Claude-wide target"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidateProviderProfiles() = %q, want %q", joined, want)
		}
	}
}

func TestProviderProfileLookupProhibitsClaudeWideTargeting(t *testing.T) {
	cfg := Default()
	cfg.ProviderProfiles = map[string]ProviderProfileConfig{
		"zai-kevin-glm53": validZAIProviderProfile(),
	}

	got, err := cfg.ProviderProfile("zai-kevin-glm53")
	if err != nil {
		t.Fatalf("ProviderProfile(exact) error: %v", err)
	}
	if got.Provider != "zai" {
		t.Fatalf("ProviderProfile(exact).Provider = %q, want zai", got.Provider)
	}
	if _, err := cfg.ProviderProfile("claude"); err == nil || !strings.Contains(err.Error(), "ambiguous Claude-wide") {
		t.Fatalf("ProviderProfile(claude) error = %v, want an ambiguous-target error", err)
	}
	if _, err := cfg.ProviderProfile("ZAI-KEVIN-GLM53"); err == nil || !strings.Contains(err.Error(), "requires an exact target") {
		t.Fatalf("ProviderProfile(case-variant) error = %v, want an exact-target error", err)
	}

	unsafe := validZAIProviderProfile()
	unsafe.ProbeRequired = false
	cfg.ProviderProfiles["zai-unprobed"] = unsafe
	if _, err := cfg.ProviderProfile("zai-unprobed"); err == nil || !strings.Contains(err.Error(), "probe_required") {
		t.Fatalf("ProviderProfile(unprobed) error = %v, want probe gate", err)
	}
}

func TestPrintProviderProfilesRoundTripsWithoutCredentials(t *testing.T) {
	cfg := Default()
	cfg.ProviderProfiles = map[string]ProviderProfileConfig{
		"zai-kevin-glm53": validZAIProviderProfile(),
	}

	var buf bytes.Buffer
	if err := Print(cfg, &buf); err != nil {
		t.Fatalf("Print() error: %v", err)
	}
	printed := buf.String()
	if !strings.Contains(printed, `[provider_profiles."zai-kevin-glm53"]`) {
		t.Fatalf("Print() omitted provider profile table: %s", printed)
	}
	if strings.Contains(strings.ToLower(printed), "api_key") || strings.Contains(printed, "xai-") {
		t.Fatalf("Print() rendered an unexpected credential-shaped value: %s", printed)
	}

	var decoded Config
	if _, err := toml.Decode(printed, &decoded); err != nil {
		t.Fatalf("decode printed config: %v", err)
	}
	decodedProfile, err := decoded.ProviderProfile("zai-kevin-glm53")
	if err != nil {
		t.Fatalf("decoded ProviderProfile() error: %v", err)
	}
	if _, err := decodedProfile.Identity(); err != nil {
		t.Fatalf("decoded profile Identity() error: %v", err)
	}
}

func TestValidateProviderProfilesRejectsUnsafeCommandAndInvalidIdentity(t *testing.T) {
	profile := validZAIProviderProfile()
	profile.Command = "claude\n--unsafe"
	profile.Endpoint = "http://api.z.ai"
	profile.AutomationPolicy = ""
	errs := ValidateProviderProfiles(map[string]ProviderProfileConfig{"zai-kevin-glm53": profile})
	joined := errorsString(errs)
	for _, want := range []string{"endpoint must be an absolute HTTPS URL", "command must be non-empty", "automation_policy must be non-empty"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidateProviderProfiles() = %q, want %q", joined, want)
		}
	}
}

func TestValidateProviderProfilesRejectsACPCommandArgumentsAndWrongPolicy(t *testing.T) {
	profile := ProviderProfileConfig{
		Provider:         "xai",
		AccountAlias:     "kevin-dev",
		Model:            "grok-code-fast-1",
		Endpoint:         "https://api.x.ai",
		Runtime:          "grok",
		ConfigSHA256:     providerProfileTestHash,
		Command:          "grok --no-auto-update",
		AutomationPolicy: "always-approve",
		ExactTargetOnly:  true,
	}
	joined := errorsString(ValidateProviderProfiles(map[string]ProviderProfileConfig{"xai-grok-primary": profile}))
	for _, want := range []string{"must be one executable name", "grok-readonly-ci"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidateProviderProfiles() = %q, want %q", joined, want)
		}
	}
}

func TestValidateProviderProfilesAllowsAbsoluteACPExecutablePathWithSpaces(t *testing.T) {
	profile := ProviderProfileConfig{
		Provider:         "xai",
		AccountAlias:     "kevin-dev",
		Model:            "grok-code-fast-1",
		Endpoint:         "https://api.x.ai",
		Runtime:          "grok",
		ConfigSHA256:     providerProfileTestHash,
		Command:          "/opt/Grok CLI/grok",
		AutomationPolicy: agent.DefaultGrokAutomationPolicyName,
		ExactTargetOnly:  true,
	}
	if errs := ValidateProviderProfiles(map[string]ProviderProfileConfig{"xai-grok-primary": profile}); len(errs) != 0 {
		t.Fatalf("ValidateProviderProfiles() errors = %v", errs)
	}
}

func TestValidateProviderProfilesRejectsZAIPolicyAndPermissionBypass(t *testing.T) {
	profile := validZAIProviderProfile()
	profile.AutomationPolicy = "custom"
	profile.Command = "claude --dangerously-skip-permissions"
	joined := errorsString(ValidateProviderProfiles(map[string]ProviderProfileConfig{"zai-kevin-glm53": profile}))
	for _, want := range []string{provider.DefaultZAIAutomationPolicyName, "must be one executable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidateProviderProfiles() = %q, want %q", joined, want)
		}
	}
}

func TestValidateProviderProfilesRejectsUnofficialZAIEndpointAndRuntime(t *testing.T) {
	profile := validZAIProviderProfile()
	profile.Endpoint = "https://example.invalid/api/anthropic"
	profile.Runtime = "claude-glm"
	joined := errorsString(ValidateProviderProfiles(map[string]ProviderProfileConfig{"zai-kevin-glm53": profile}))
	for _, want := range []string{"official Claude-compatible endpoint", "runtime must be claude-code"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidateProviderProfiles() = %q, want %q", joined, want)
		}
	}
}

func errorsString(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "\n")
}
