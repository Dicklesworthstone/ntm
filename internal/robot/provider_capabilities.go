package robot

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/provider"
)

// ProviderCapabilitiesOutput is a read-only discovery receipt for provider
// transports and configured identity boundaries. It intentionally excludes
// executable commands, endpoints, raw configuration, credentials, and keys.
type ProviderCapabilitiesOutput struct {
	RobotResponse
	Transports                map[string]provider.OperationCapabilities `json:"transports"`
	GrokAutomationPolicy      GrokPolicyCapability                      `json:"grok_automation_policy"`
	OfflineConformanceHarness ProviderConformanceCapability             `json:"offline_conformance_harness"`
	ConfigSupplied            bool                                      `json:"config_supplied"`
	ProviderProfiles          []ProviderProfileCapability               `json:"provider_profiles"`
}

// GrokPolicyCapability describes the compiled-in named policy without
// serializing its rules. The digest binds a receipt to the policy definition.
type GrokPolicyCapability struct {
	Name           string `json:"name"`
	Sandbox        string `json:"sandbox"`
	PermissionMode string `json:"permission_mode"`
	SHA256         string `json:"sha256"`
	AllowRuleCount int    `json:"allow_rule_count"`
	DenyRuleCount  int    `json:"deny_rule_count"`
}

// ProviderConformanceCapability advertises the offline-only conformance
// harness. Availability here is not a claim that a provider account has been
// qualified or contacted.
type ProviderConformanceCapability struct {
	Available   bool   `json:"available"`
	Description string `json:"description"`
}

// ProviderProfileCapability is the redacted configured-profile projection.
// ProfileKeySHA256 permits deterministic correlation without publishing a
// profile selector; IdentitySHA256 is present only for a valid identity tuple.
type ProviderProfileCapability struct {
	ProfileKeySHA256    string `json:"profile_key_sha256"`
	IdentitySHA256      string `json:"identity_sha256,omitempty"`
	IdentityState       string `json:"identity_state"`
	ProfileState        string `json:"profile_state"`
	ExactTargetOnly     bool   `json:"exact_target_only"`
	ProbeRequired       bool   `json:"probe_required"`
	ModelProbeState     string `json:"model_probe_state"`
	ModelProbeQualified bool   `json:"model_probe_qualified"`
}

// GetProviderCapabilities builds a deterministic, entirely local provider
// capability receipt. cfg may be nil when no configuration has been loaded.
func GetProviderCapabilities(cfg *config.Config) (*ProviderCapabilitiesOutput, error) {
	policy := agentpkg.DefaultGrokAutomationPolicy()
	output := &ProviderCapabilitiesOutput{
		RobotResponse: NewRobotResponse(true),
		Transports:    provider.CapabilityMatrix(),
		GrokAutomationPolicy: GrokPolicyCapability{
			Name:           policy.Name,
			Sandbox:        policy.Sandbox,
			PermissionMode: policy.PermissionMode,
			SHA256:         agentpkg.DefaultGrokAutomationPolicySHA256(),
			AllowRuleCount: len(policy.AllowRules),
			DenyRuleCount:  len(policy.DenyRules),
		},
		OfflineConformanceHarness: ProviderConformanceCapability{
			Available:   true,
			Description: "operator-runnable synthetic lifecycle conformance via --robot-provider-conformance; no provider, network, Beads, or Agent Mail calls",
		},
		ProviderProfiles: []ProviderProfileCapability{},
	}
	if cfg == nil {
		return output, nil
	}

	output.ConfigSupplied = true
	keys := make([]string, 0, len(cfg.ProviderProfiles))
	for key := range cfg.ProviderProfiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		profile := cfg.ProviderProfiles[key]
		capability := ProviderProfileCapability{
			ProfileKeySHA256:    sha256Text(key),
			IdentityState:       "invalid",
			ProfileState:        "invalid",
			ExactTargetOnly:     profile.ExactTargetOnly,
			ProbeRequired:       profile.ProbeRequired,
			ModelProbeState:     safeModelProbeState(profile.ModelProbeState),
			ModelProbeQualified: false,
		}
		if identity, err := profile.Identity(); err == nil {
			capability.IdentitySHA256 = identity.Hash()
			capability.IdentityState = "valid"
		}
		if validated, err := cfg.ProviderProfile(key); err == nil {
			if strings.EqualFold(strings.TrimSpace(validated.Provider), "zai") {
				// A local tuple can be valid and still has no fresh provider proof.
				// Production --spawn-zai runs its nonce-bound probe separately.
				capability.ProfileState = "live_probe_required"
			} else {
				// Configuration validity is not operation evidence. Native Grok
				// ACP still requires a fresh nonce and session-scoped exact-model
				// observation before an operation can succeed.
				capability.ProfileState = "operation_evidence_required"
			}
			capability.ModelProbeQualified = false
		}
		output.ProviderProfiles = append(output.ProviderProfiles, capability)
	}
	return output, nil
}

// PrintProviderCapabilities writes the redacted receipt to the standard robot
// output. It performs no provider, filesystem, or network access.
func PrintProviderCapabilities(cfg *config.Config) error {
	output, err := GetProviderCapabilities(cfg)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot provider capabilities failed")
}

func safeModelProbeState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "qualified", "live_verified", "unprobed", "rejected", "expired":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "unknown"
	}
}

func sha256Text(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
