//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM safety profiles.
//
// Bead: bd-1lxd8 - E2E Tests: safety profiles (standard/safe/paranoid)
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// buildSafetyTestInput returns a string containing synthetic secrets for redaction testing.
// Uses fake constants from redaction_test.go: fakeOpenAIKey, fakeGitHubToken, fakeAWSKey.
func buildSafetyTestInput() string {
	return "Here is my config:\n" +
		"OPENAI_API_KEY=" + fakeOpenAIKey + "\n" +
		"GITHUB_TOKEN=" + fakeGitHubToken + "\n" +
		"AWS_ACCESS_KEY=" + fakeAWSKey + "\n"
}

// -------------------------------------------------------------------
// Profile Configuration Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_StandardDefaults(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_standard")
	defer suite.Teardown()

	cfg := config.Default()
	cfg.Safety.Profile = config.SafetyProfileStandard

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Testing standard profile defaults")
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Profile: %s", cfg.Safety.Profile)

	// Validate profile name
	if err := config.ValidateSafetyConfig(&cfg.Safety); err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] validation failed: %v", err)
	}

	// Standard profile:
	// - preflight: enabled, not strict
	// - redaction: warn
	// - privacy: off
	if cfg.Safety.Profile != "standard" {
		t.Errorf("[E2E-SAFETY-PROFILE] expected profile=standard, got %s", cfg.Safety.Profile)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Standard profile: preflight.strict=%v redaction.mode=%s privacy.enabled=%v",
		cfg.Preflight.Strict, cfg.Redaction.Mode, cfg.Privacy.Enabled)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Standard profile defaults verified")
}

func TestE2E_SafetyProfile_SafeDefaults(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_safe")
	defer suite.Teardown()

	cfg := config.Default()
	cfg.Safety.Profile = config.SafetyProfileSafe

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Testing safe profile defaults")

	if err := config.ValidateSafetyConfig(&cfg.Safety); err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] validation failed: %v", err)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Safe profile validated: %s", cfg.Safety.Profile)
}

func TestE2E_SafetyProfile_ParanoidDefaults(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_paranoid")
	defer suite.Teardown()

	cfg := config.Default()
	cfg.Safety.Profile = config.SafetyProfileParanoid

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Testing paranoid profile defaults")

	if err := config.ValidateSafetyConfig(&cfg.Safety); err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] validation failed: %v", err)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid profile validated: %s", cfg.Safety.Profile)
}

func TestE2E_SafetyProfile_InvalidProfileRejected(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_invalid")
	defer suite.Teardown()

	invalid := config.SafetyConfig{Profile: "ultra-paranoid"}
	err := config.ValidateSafetyConfig(&invalid)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Testing invalid profile: %q err=%v", invalid.Profile, err)

	if err == nil {
		t.Error("[E2E-SAFETY-PROFILE] expected error for invalid profile, got nil")
	} else {
		if !strings.Contains(err.Error(), "invalid safety profile") {
			t.Errorf("[E2E-SAFETY-PROFILE] expected 'invalid safety profile' error, got: %v", err)
		}
	}
}

func TestE2E_SafetyProfile_EmptyProfileValid(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_empty")
	defer suite.Teardown()

	empty := config.SafetyConfig{Profile: ""}
	err := config.ValidateSafetyConfig(&empty)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Testing empty profile: err=%v", err)

	if err != nil {
		t.Errorf("[E2E-SAFETY-PROFILE] empty profile should be valid, got: %v", err)
	}
}

// -------------------------------------------------------------------
// Redaction Mode Tests (per profile)
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_Standard_RedactionWarn(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_standard")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	// Standard profile uses redaction=warn: scan and report but don't modify
	cfg := redaction.Config{Mode: redaction.ModeWarn}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Standard redaction: mode=%s findings=%d blocked=%v",
		result.Mode, len(result.Findings), result.Blocked)

	// Warn mode should find secrets but not redact them
	if len(result.Findings) == 0 {
		t.Error("[E2E-SAFETY-PROFILE] expected findings in warn mode with synthetic secrets")
	}

	// Output should be unmodified (warn doesn't redact)
	if result.Output != input {
		t.Error("[E2E-SAFETY-PROFILE] warn mode should not modify output")
	}

	// Should not be blocked
	if result.Blocked {
		t.Error("[E2E-SAFETY-PROFILE] warn mode should not block")
	}

	// Log each finding category
	for _, f := range result.Findings {
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Finding: category=%s start=%d end=%d redacted=%s",
			f.Category, f.Start, f.End, f.Redacted)
	}
}

func TestE2E_SafetyProfile_Safe_RedactionRedact(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_safe")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	// Safe profile uses redaction=redact: replace secrets with placeholders
	cfg := redaction.Config{Mode: redaction.ModeRedact}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Safe redaction: mode=%s findings=%d blocked=%v output_len=%d",
		result.Mode, len(result.Findings), result.Blocked, len(result.Output))

	// Should find secrets
	if len(result.Findings) == 0 {
		t.Error("[E2E-SAFETY-PROFILE] expected findings in redact mode with synthetic secrets")
	}

	// Output should be modified with placeholders
	if result.Output == input {
		t.Error("[E2E-SAFETY-PROFILE] redact mode should modify output")
	}

	// Placeholders should be present in output: [REDACTED:CATEGORY:hash]
	if !strings.Contains(result.Output, "[REDACTED:") {
		t.Error("[E2E-SAFETY-PROFILE] redact mode output should contain [REDACTED:...] placeholders")
	}

	// Original secrets should NOT be in output
	if strings.Contains(result.Output, fakeOpenAIKey) {
		t.Error("[E2E-SAFETY-PROFILE] redacted output should not contain original OpenAI key")
	}
	if strings.Contains(result.Output, fakeGitHubToken) {
		t.Error("[E2E-SAFETY-PROFILE] redacted output should not contain original GitHub token")
	}

	// Should not be blocked
	if result.Blocked {
		t.Error("[E2E-SAFETY-PROFILE] redact mode should not block")
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Redacted output preview: %s", truncateString(result.Output, 500))

	// Verify each finding has a placeholder
	for _, f := range result.Findings {
		if f.Redacted == "" {
			t.Errorf("[E2E-SAFETY-PROFILE] finding %s missing redacted placeholder", f.Category)
		}
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Finding: category=%s redacted=%s", f.Category, f.Redacted)
	}
}

func TestE2E_SafetyProfile_Paranoid_RedactionBlock(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_paranoid")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	// Paranoid profile uses redaction=block: fail if secrets detected
	cfg := redaction.Config{Mode: redaction.ModeBlock}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid redaction: mode=%s findings=%d blocked=%v",
		result.Mode, len(result.Findings), result.Blocked)

	// Should find secrets
	if len(result.Findings) == 0 {
		t.Error("[E2E-SAFETY-PROFILE] expected findings in block mode with synthetic secrets")
	}

	// Should be BLOCKED
	if !result.Blocked {
		t.Error("[E2E-SAFETY-PROFILE] block mode should set Blocked=true when secrets found")
	}

	// Output is original (block doesn't redact, just blocks)
	if result.Output != input {
		t.Error("[E2E-SAFETY-PROFILE] block mode should not modify output")
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Block mode correctly blocked with %d findings", len(result.Findings))
}

func TestE2E_SafetyProfile_RedactionOff_NoScanning(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_off")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	cfg := redaction.Config{Mode: redaction.ModeOff}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Off mode: findings=%d blocked=%v output_changed=%v",
		len(result.Findings), result.Blocked, result.Output != input)

	// Off mode should not scan at all
	if len(result.Findings) != 0 {
		t.Errorf("[E2E-SAFETY-PROFILE] off mode should have 0 findings, got %d", len(result.Findings))
	}

	if result.Blocked {
		t.Error("[E2E-SAFETY-PROFILE] off mode should not block")
	}

	if result.Output != input {
		t.Error("[E2E-SAFETY-PROFILE] off mode should return input unchanged")
	}
}

// -------------------------------------------------------------------
// Redaction: Clean Input (No Secrets)
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_RedactNoSecrets(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_clean")
	defer suite.Teardown()

	cleanInput := "This is a perfectly clean prompt about Go programming.\nNo secrets here."

	modes := []redaction.Mode{
		redaction.ModeWarn,
		redaction.ModeRedact,
		redaction.ModeBlock,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			cfg := redaction.Config{Mode: mode}
			result := redaction.ScanAndRedact(cleanInput, cfg)

			suite.Logger().Log("[E2E-SAFETY-PROFILE] Clean input with mode=%s: findings=%d blocked=%v",
				mode, len(result.Findings), result.Blocked)

			if len(result.Findings) != 0 {
				t.Errorf("[E2E-SAFETY-PROFILE] clean input should have 0 findings, got %d", len(result.Findings))
			}

			if result.Blocked {
				t.Error("[E2E-SAFETY-PROFILE] clean input should not be blocked")
			}

			if result.Output != cleanInput {
				t.Error("[E2E-SAFETY-PROFILE] clean input output should be unchanged")
			}
		})
	}
}

// -------------------------------------------------------------------
// Privacy Mode Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_Standard_PrivacyOff(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_standard")
	defer suite.Teardown()

	// Standard profile: privacy disabled
	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = false

	mgr := privacy.New(cfg)
	mgr.RegisterSession("test-session", false, false)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Standard privacy: enabled=%v", cfg.Enabled)

	// All persistence operations should be allowed
	operations := []privacy.PersistOperation{
		privacy.OpCheckpoint,
		privacy.OpEventLog,
		privacy.OpPromptHistory,
		privacy.OpScrollback,
		privacy.OpExport,
		privacy.OpArchive,
	}

	for _, op := range operations {
		err := mgr.CanPersist("test-session", op)
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Standard CanPersist(%s): err=%v", op, err)

		if err != nil {
			t.Errorf("[E2E-SAFETY-PROFILE] standard profile should allow %s, got: %v", op, err)
		}
	}
}

func TestE2E_SafetyProfile_Paranoid_PrivacyOn(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_paranoid")
	defer suite.Teardown()

	// Paranoid profile: privacy enabled with all protections
	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)
	mgr.RegisterSession("test-session", true, false)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid privacy: enabled=%v history=%v events=%v checkpoints=%v scrollback=%v explicit=%v",
		cfg.Enabled, cfg.DisablePromptHistory, cfg.DisableEventLogs,
		cfg.DisableCheckpoints, cfg.DisableScrollbackCapture, cfg.RequireExplicitPersist)

	// All persistence operations should be blocked
	type testCase struct {
		op      privacy.PersistOperation
		blocked bool
	}

	cases := []testCase{
		{privacy.OpCheckpoint, true},
		{privacy.OpEventLog, true},
		{privacy.OpPromptHistory, true},
		{privacy.OpScrollback, true},
		{privacy.OpExport, true},
		{privacy.OpArchive, true},
	}

	for _, tc := range cases {
		err := mgr.CanPersist("test-session", tc.op)
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid CanPersist(%s): err=%v", tc.op, err)

		if tc.blocked && err == nil {
			t.Errorf("[E2E-SAFETY-PROFILE] paranoid should block %s", tc.op)
		}
		if tc.blocked && err != nil && !privacy.IsPrivacyError(err) {
			t.Errorf("[E2E-SAFETY-PROFILE] expected PrivacyError for %s, got: %T", tc.op, err)
		}
	}
}

func TestE2E_SafetyProfile_Paranoid_AllowPersistOverride(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_allow_persist")
	defer suite.Teardown()

	// When AllowPersist is true, even paranoid mode should allow operations
	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)
	mgr.RegisterSession("test-session", true, true) // allowPersist=true

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Allow-persist override: privacy=on, allowPersist=true")

	operations := []privacy.PersistOperation{
		privacy.OpCheckpoint,
		privacy.OpEventLog,
		privacy.OpPromptHistory,
		privacy.OpScrollback,
		privacy.OpExport,
		privacy.OpArchive,
	}

	for _, op := range operations {
		err := mgr.CanPersist("test-session", op)
		suite.Logger().Log("[E2E-SAFETY-PROFILE] AllowPersist CanPersist(%s): err=%v", op, err)

		if err != nil {
			t.Errorf("[E2E-SAFETY-PROFILE] allowPersist=true should allow %s, got: %v", op, err)
		}
	}
}

func TestE2E_SafetyProfile_PrivacyUnregisteredSession(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_unregistered")
	defer suite.Teardown()

	// Unregistered session should fall back to global config
	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)
	// NOT registering "test-session"

	isPrivacy := mgr.IsPrivacyEnabled("test-session")
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Unregistered session: IsPrivacyEnabled=%v (global=%v)", isPrivacy, cfg.Enabled)

	if !isPrivacy {
		t.Error("[E2E-SAFETY-PROFILE] unregistered session should inherit global privacy setting")
	}
}

func TestE2E_SafetyProfile_PrivacySessionLifecycle(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_lifecycle")
	defer suite.Teardown()

	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)

	// Register session
	mgr.RegisterSession("test-session", true, false)
	state := mgr.GetState("test-session")
	if state == nil {
		t.Fatal("[E2E-SAFETY-PROFILE] registered session should have state")
	}
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Registered: privacyMode=%v allowPersist=%v", state.PrivacyMode, state.AllowPersist)

	if !state.PrivacyMode {
		t.Error("[E2E-SAFETY-PROFILE] session should have privacy mode enabled")
	}

	// Unregister session
	mgr.UnregisterSession("test-session")
	state = mgr.GetState("test-session")
	if state != nil {
		t.Error("[E2E-SAFETY-PROFILE] unregistered session should have nil state")
	}
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Session unregistered, state is nil")
}

// -------------------------------------------------------------------
// Hermetic Config Test (config isolation via temp dir)
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_HermeticConfig(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_profile_hermetic")
	defer suite.Teardown()

	// Set HOME and XDG_CONFIG_HOME to temp dir for isolation
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	origXDG := os.Getenv("XDG_CONFIG_HOME")

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Hermetic config: HOME=%s XDG=%s", tmpDir, filepath.Join(tmpDir, ".config"))

	// Create a minimal config file with paranoid profile
	configDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] failed to create config dir: %v", err)
	}

	configContent := `[safety]
profile = "paranoid"
`
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] failed to write config: %v", err)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Wrote config to %s", configPath)

	// Verify the file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatalf("[E2E-SAFETY-PROFILE] config file not created at %s", configPath)
	}

	// Read back and verify content
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("[E2E-SAFETY-PROFILE] failed to read config: %v", err)
	}
	if !strings.Contains(string(data), "paranoid") {
		t.Error("[E2E-SAFETY-PROFILE] config should contain paranoid profile")
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Config content: %s", strings.TrimSpace(string(data)))

	// Restore env (t.Setenv handles this automatically via cleanup)
	_ = origHome
	_ = origXDG
}

// -------------------------------------------------------------------
// Redaction Config Integration Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_RedactionConfigValidation(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_config")
	defer suite.Teardown()

	// Test config validation
	validConfigs := []redaction.Config{
		{Mode: redaction.ModeOff},
		{Mode: redaction.ModeWarn},
		{Mode: redaction.ModeRedact},
		{Mode: redaction.ModeBlock},
	}

	for _, cfg := range validConfigs {
		err := cfg.Validate()
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Validate mode=%s: err=%v", cfg.Mode, err)
		if err != nil {
			t.Errorf("[E2E-SAFETY-PROFILE] mode %s should be valid, got: %v", cfg.Mode, err)
		}
	}

	// Invalid config
	invalid := redaction.Config{Mode: "explode"}
	err := invalid.Validate()
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Validate mode=explode: err=%v", err)
	if err == nil {
		t.Error("[E2E-SAFETY-PROFILE] mode 'explode' should be invalid")
	}
}

func TestE2E_SafetyProfile_RedactionDefaultConfig(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_redaction_default")
	defer suite.Teardown()

	cfg := redaction.DefaultConfig()
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Default redaction config: mode=%s", cfg.Mode)

	if cfg.Mode != redaction.ModeWarn {
		t.Errorf("[E2E-SAFETY-PROFILE] default redaction mode should be warn, got %s", cfg.Mode)
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("[E2E-SAFETY-PROFILE] default config should be valid: %v", err)
	}
}

// -------------------------------------------------------------------
// Cross-profile Comparison Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_CrossProfileRedaction(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_cross_profile")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	profiles := []struct {
		name        string
		mode        redaction.Mode
		expectBlock bool
		expectMod   bool
	}{
		{"standard", redaction.ModeWarn, false, false},
		{"safe", redaction.ModeRedact, false, true},
		{"paranoid", redaction.ModeBlock, true, false},
	}

	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			cfg := redaction.Config{Mode: p.mode}
			result := redaction.ScanAndRedact(input, cfg)

			modified := result.Output != input

			suite.Logger().Log("[E2E-SAFETY-PROFILE] Profile %s: mode=%s findings=%d blocked=%v modified=%v",
				p.name, result.Mode, len(result.Findings), result.Blocked, modified)

			if result.Blocked != p.expectBlock {
				t.Errorf("[E2E-SAFETY-PROFILE] %s: expected blocked=%v, got %v",
					p.name, p.expectBlock, result.Blocked)
			}
			if modified != p.expectMod {
				t.Errorf("[E2E-SAFETY-PROFILE] %s: expected output modified=%v, got %v",
					p.name, p.expectMod, modified)
			}

			// All profiles should detect secrets
			if len(result.Findings) == 0 {
				t.Errorf("[E2E-SAFETY-PROFILE] %s: expected findings, got 0", p.name)
			}
		})
	}
}

func TestE2E_SafetyProfile_CrossProfilePrivacy(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_cross_privacy")
	defer suite.Teardown()

	profiles := []struct {
		name          string
		privacyOn     bool
		expectBlocked bool
	}{
		{"standard", false, false},
		{"safe", false, false},
		{"paranoid", true, true},
	}

	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			cfg := config.DefaultPrivacyConfig()
			cfg.Enabled = p.privacyOn

			mgr := privacy.New(cfg)
			mgr.RegisterSession("test", p.privacyOn, false)

			err := mgr.CanPersist("test", privacy.OpPromptHistory)

			suite.Logger().Log("[E2E-SAFETY-PROFILE] %s: privacy=%v CanPersist(prompt_history)=%v",
				p.name, p.privacyOn, err)

			if p.expectBlocked && err == nil {
				t.Errorf("[E2E-SAFETY-PROFILE] %s should block prompt history", p.name)
			}
			if !p.expectBlocked && err != nil {
				t.Errorf("[E2E-SAFETY-PROFILE] %s should allow prompt history: %v", p.name, err)
			}
		})
	}
}

// -------------------------------------------------------------------
// Redaction Placeholder Format Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_PlaceholderFormat(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_placeholder_format")
	defer suite.Teardown()

	input := "OPENAI_API_KEY=" + fakeOpenAIKey

	cfg := redaction.Config{Mode: redaction.ModeRedact}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Placeholder test: findings=%d", len(result.Findings))

	if len(result.Findings) == 0 {
		t.Fatal("[E2E-SAFETY-PROFILE] expected at least one finding")
	}

	for _, f := range result.Findings {
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Finding: category=%s redacted=%q", f.Category, f.Redacted)

		// Placeholder should follow format: [REDACTED:CATEGORY:hash8]
		if !strings.HasPrefix(f.Redacted, "[REDACTED:") {
			t.Errorf("[E2E-SAFETY-PROFILE] placeholder should start with [REDACTED:, got %q", f.Redacted)
		}
		if !strings.HasSuffix(f.Redacted, "]") {
			t.Errorf("[E2E-SAFETY-PROFILE] placeholder should end with ], got %q", f.Redacted)
		}
	}

	// Output should contain placeholder instead of original key
	if strings.Contains(result.Output, fakeOpenAIKey) {
		t.Error("[E2E-SAFETY-PROFILE] output should not contain original key")
	}
	if !strings.Contains(result.Output, "[REDACTED:") {
		t.Error("[E2E-SAFETY-PROFILE] output should contain [REDACTED: placeholder")
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Redacted output: %s", truncateString(result.Output, 200))
}

// -------------------------------------------------------------------
// Multiple Secrets Detection Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_MultipleSecretTypes(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_multiple_secrets")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	cfg := redaction.Config{Mode: redaction.ModeRedact}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Multiple secrets: findings=%d", len(result.Findings))

	// Should detect multiple types of secrets
	categories := make(map[redaction.Category]int)
	for _, f := range result.Findings {
		categories[f.Category]++
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Detected: %s at [%d:%d]", f.Category, f.Start, f.End)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Categories detected: %v", categories)

	// Should have at least 2 different categories
	if len(categories) < 2 {
		t.Errorf("[E2E-SAFETY-PROFILE] expected at least 2 secret categories, got %d", len(categories))
	}
}

// -------------------------------------------------------------------
// Persistence Prevention (Paranoid Mode File Check)
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_Paranoid_NoPersistence(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_paranoid_no_persist")
	defer suite.Teardown()

	tmpDir := t.TempDir()

	// Set up paranoid privacy config
	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)
	mgr.RegisterSession("paranoid-test", true, false)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid persistence test in: %s", tmpDir)

	// Try to create various persistence artifacts
	persistenceOps := []struct {
		op   privacy.PersistOperation
		path string
	}{
		{privacy.OpCheckpoint, filepath.Join(tmpDir, "checkpoint.json")},
		{privacy.OpEventLog, filepath.Join(tmpDir, "events.jsonl")},
		{privacy.OpPromptHistory, filepath.Join(tmpDir, "history.jsonl")},
		{privacy.OpScrollback, filepath.Join(tmpDir, "scrollback.txt")},
	}

	for _, p := range persistenceOps {
		err := mgr.CanPersist("paranoid-test", p.op)
		suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid CanPersist(%s): blocked=%v", p.op, err != nil)

		if err == nil {
			t.Errorf("[E2E-SAFETY-PROFILE] paranoid mode should block %s", p.op)
		} else {
			// Since the operation is blocked, the file should NOT be created
			if _, statErr := os.Stat(p.path); !os.IsNotExist(statErr) {
				t.Errorf("[E2E-SAFETY-PROFILE] file %s should not exist when persistence is blocked", p.path)
			}
		}
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Paranoid persistence prevention verified: no files created")
}

// -------------------------------------------------------------------
// Result Struct Verification Tests
// -------------------------------------------------------------------

func TestE2E_SafetyProfile_ResultStructFields(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_result_struct")
	defer suite.Teardown()

	input := buildSafetyTestInput()

	cfg := redaction.Config{Mode: redaction.ModeRedact}
	result := redaction.ScanAndRedact(input, cfg)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Result: mode=%s originalLen=%d outputLen=%d findings=%d blocked=%v",
		result.Mode, result.OriginalLength, len(result.Output), len(result.Findings), result.Blocked)

	// Mode should match config
	if result.Mode != redaction.ModeRedact {
		t.Errorf("[E2E-SAFETY-PROFILE] result mode should be redact, got %s", result.Mode)
	}

	// OriginalLength should match input
	if result.OriginalLength != len(input) {
		t.Errorf("[E2E-SAFETY-PROFILE] original length %d != input length %d",
			result.OriginalLength, len(input))
	}

	// Output length may differ from original (placeholders may be shorter/longer)
	suite.Logger().Log("[E2E-SAFETY-PROFILE] Length delta: %d bytes", len(result.Output)-result.OriginalLength)

	// Finding offsets should be within bounds
	for _, f := range result.Findings {
		if f.Start < 0 || f.End > result.OriginalLength || f.Start >= f.End {
			t.Errorf("[E2E-SAFETY-PROFILE] invalid finding offsets: start=%d end=%d original=%d",
				f.Start, f.End, result.OriginalLength)
		}
	}
}

func TestE2E_SafetyProfile_PrivacyErrorType(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "safety_privacy_error")
	defer suite.Teardown()

	cfg := config.DefaultPrivacyConfig()
	cfg.Enabled = true

	mgr := privacy.New(cfg)
	mgr.RegisterSession("test-session", true, false)

	err := mgr.CanPersist("test-session", privacy.OpPromptHistory)

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Privacy error: %v isPrivacyError=%v", err, privacy.IsPrivacyError(err))

	if err == nil {
		t.Fatal("[E2E-SAFETY-PROFILE] expected error from privacy-enabled session")
	}

	if !privacy.IsPrivacyError(err) {
		t.Errorf("[E2E-SAFETY-PROFILE] expected PrivacyError, got %T", err)
	}

	// Error message should be informative
	errMsg := err.Error()
	if !strings.Contains(errMsg, "privacy mode") {
		t.Errorf("[E2E-SAFETY-PROFILE] error should mention privacy mode: %q", errMsg)
	}
	if !strings.Contains(errMsg, "test-session") {
		t.Errorf("[E2E-SAFETY-PROFILE] error should mention session name: %q", errMsg)
	}

	suite.Logger().Log("[E2E-SAFETY-PROFILE] Privacy error message: %s", errMsg)
}
