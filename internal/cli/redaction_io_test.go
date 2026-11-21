package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestApplyOutputRedaction_NoFindings(t *testing.T) {
	out, summary, err := applyOutputRedaction("hello world", redaction.Config{Mode: redaction.ModeRedact})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if summary != nil {
		t.Fatalf("expected nil summary when no findings, got %+v", *summary)
	}
	if out != "hello world" {
		t.Fatalf("expected output to be unchanged, got %q", out)
	}
}

func TestApplyOutputRedaction_WarnMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	out, summary, err := applyOutputRedaction(input, redaction.Config{Mode: redaction.ModeWarn})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if out != input {
		t.Fatalf("expected output to be unchanged in warn mode, got %q", out)
	}
	if summary == nil {
		t.Fatalf("expected non-nil summary")
	}
	if summary.Action != "warn" {
		t.Fatalf("expected action=warn, got %q", summary.Action)
	}
	if summary.Findings == 0 {
		t.Fatalf("expected findings > 0")
	}
	if got := summary.Categories["PASSWORD"]; got == 0 {
		t.Fatalf("expected PASSWORD category count > 0, got %d (%v)", got, summary.Categories)
	}
}

func TestApplyOutputRedaction_RedactMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	out, summary, err := applyOutputRedaction(input, redaction.Config{Mode: redaction.ModeRedact})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if strings.Contains(out, "hunter2hunter2") {
		t.Fatalf("expected output to be redacted, got %q", out)
	}
	if !strings.Contains(out, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected password placeholder, got %q", out)
	}
	if summary == nil {
		t.Fatalf("expected non-nil summary")
	}
	if summary.Action != "redact" {
		t.Fatalf("expected action=redact, got %q", summary.Action)
	}
}

func TestApplyOutputRedaction_BlockMode(t *testing.T) {
	input := "prefix password=hunter2hunter2 suffix\n"
	out, summary, err := applyOutputRedaction(input, redaction.Config{Mode: redaction.ModeBlock})
	if err == nil {
		t.Fatalf("expected error")
	}
	if out != "" {
		t.Fatalf("expected empty output on block, got %q", out)
	}
	if summary == nil {
		t.Fatalf("expected non-nil summary")
	}

	var blocked redactionBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected redactionBlockedError, got %T: %v", err, err)
	}
	if summary.Action != "block" {
		t.Fatalf("expected action=block, got %q", summary.Action)
	}
	if !strings.Contains(err.Error(), "PASSWORD") {
		t.Fatalf("expected error to mention category, got %q", err.Error())
	}
}

// ----------------------------------------------------------------------------
// applyRedactionFlagOverrides precedence tests
// Priority: --allow-secret > --redact > config > default
// ----------------------------------------------------------------------------

// saveAndRestoreFlags saves current flag state and returns a cleanup function.
func saveAndRestoreFlags(t *testing.T) func() {
	t.Helper()
	savedRedactMode := redactMode
	savedAllowSecret := allowSecret
	return func() {
		redactMode = savedRedactMode
		allowSecret = savedAllowSecret
	}
}

func TestApplyRedactionFlagOverrides_NilConfig(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	// Should not panic with nil config.
	if err := applyRedactionFlagOverrides(nil); err != nil {
		t.Fatalf("applyRedactionFlagOverrides(nil) error = %v", err)
	}
}

func TestApplyRedactionFlagOverrides_ConfigDefaultAppliedWithNoFlags(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	// Reset flags.
	redactMode = ""
	allowSecret = false

	cfg := config.Default()
	original := cfg.Redaction.Mode
	if err := applyRedactionFlagOverrides(cfg); err != nil {
		t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
	}

	if cfg.Redaction.Mode != original {
		t.Errorf("config mode should be unchanged when no flags; got %q, want %q", cfg.Redaction.Mode, original)
	}
}

func TestApplyRedactionFlagOverrides_RedactFlagOverridesConfig(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	tests := []struct {
		name       string
		configMode string
		flagMode   string
		want       string
	}{
		{"off overrides warn", "warn", "off", "off"},
		{"block overrides warn", "warn", "block", "block"},
		{"redact overrides off", "off", "redact", "redact"},
		{"warn overrides block", "block", "warn", "warn"},
		{"same mode is idempotent", "redact", "redact", "redact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redactMode = tt.flagMode
			allowSecret = false

			cfg := config.Default()
			cfg.Redaction.Mode = tt.configMode
			if err := applyRedactionFlagOverrides(cfg); err != nil {
				t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
			}

			if cfg.Redaction.Mode != tt.want {
				t.Errorf("got %q, want %q", cfg.Redaction.Mode, tt.want)
			}
		})
	}
}

func TestApplyRedactionFlagOverrides_AllowSecretOverridesBlock(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	// Set --allow-secret, config is "block".
	redactMode = ""
	allowSecret = true

	cfg := config.Default()
	cfg.Redaction.Mode = "block"
	if err := applyRedactionFlagOverrides(cfg); err != nil {
		t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
	}

	// --allow-secret should downgrade block to warn.
	if cfg.Redaction.Mode != "warn" {
		t.Errorf("--allow-secret should downgrade block to warn; got %q", cfg.Redaction.Mode)
	}
}

func TestApplyRedactionFlagOverrides_AllowSecretDoesNotAffectOtherModes(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	modes := []string{"off", "warn", "redact"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			redactMode = ""
			allowSecret = true

			cfg := config.Default()
			cfg.Redaction.Mode = mode
			if err := applyRedactionFlagOverrides(cfg); err != nil {
				t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
			}

			if cfg.Redaction.Mode != mode {
				t.Errorf("--allow-secret should not affect %q mode; got %q", mode, cfg.Redaction.Mode)
			}
		})
	}
}

func TestApplyRedactionFlagOverrides_RedactFlagThenAllowSecret(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	// Both flags set: --redact=block --allow-secret
	// Priority: --allow-secret > --redact
	// So --redact sets block, then --allow-secret downgrades to warn.
	redactMode = "block"
	allowSecret = true

	cfg := config.Default()
	if err := applyRedactionFlagOverrides(cfg); err != nil {
		t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
	}

	if cfg.Redaction.Mode != "warn" {
		t.Errorf("--allow-secret should override --redact=block; got %q", cfg.Redaction.Mode)
	}
}

func TestApplyRedactionFlagOverrides_InvalidRedactModeIgnored(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	redactMode = "invalid"
	allowSecret = false

	cfg := config.Default()
	original := cfg.Redaction.Mode
	if err := applyRedactionFlagOverrides(cfg); err == nil {
		t.Fatal("applyRedactionFlagOverrides() error = nil, want invalid mode error")
	}

	// Invalid value should be ignored, config unchanged.
	if cfg.Redaction.Mode != original {
		t.Errorf("invalid --redact should be ignored; got %q, want %q", cfg.Redaction.Mode, original)
	}
}

func TestApplyRedactionFlagOverrides_FullPrecedenceChain(t *testing.T) {
	cleanup := saveAndRestoreFlags(t)
	defer cleanup()

	// Test the full precedence chain: --allow-secret > --redact > config
	tests := []struct {
		name        string
		configMode  string
		redactFlag  string
		allowSecret bool
		want        string
	}{
		{"config only", "redact", "", false, "redact"},
		{"--redact overrides config", "redact", "warn", false, "warn"},
		{"--redact=block", "warn", "block", false, "block"},
		{"--allow-secret downgrades block from config", "block", "", true, "warn"},
		{"--allow-secret downgrades --redact=block", "off", "block", true, "warn"},
		{"--allow-secret with non-block mode unchanged", "warn", "", true, "warn"},
		{"--allow-secret with --redact=warn unchanged", "off", "warn", true, "warn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redactMode = tt.redactFlag
			allowSecret = tt.allowSecret

			cfg := config.Default()
			cfg.Redaction.Mode = tt.configMode
			if err := applyRedactionFlagOverrides(cfg); err != nil {
				t.Fatalf("applyRedactionFlagOverrides() error = %v", err)
			}

			if cfg.Redaction.Mode != tt.want {
				t.Errorf("got %q, want %q", cfg.Redaction.Mode, tt.want)
			}
		})
	}
}

// Test that allowlist in config is passed through to redaction library.
func TestApplyOutputRedaction_AllowlistSuppressesFalsePositives(t *testing.T) {
	// Construct test secret.
	secret := "password=supersecret123"
	input := "config: " + secret

	// First verify it's detected without allowlist.
	cfg := redaction.Config{Mode: redaction.ModeWarn}
	_, summary, _ := applyOutputRedaction(input, cfg)
	if summary == nil || summary.Findings == 0 {
		t.Fatal("expected findings without allowlist")
	}

	// Now with allowlist.
	cfgWithAllowlist := redaction.Config{
		Mode:      redaction.ModeWarn,
		Allowlist: []string{"supersecret123"},
	}
	_, summaryWithAllowlist, _ := applyOutputRedaction(input, cfgWithAllowlist)

	if summaryWithAllowlist != nil && summaryWithAllowlist.Findings > 0 {
		t.Errorf("allowlist should suppress false positives; got %d findings", summaryWithAllowlist.Findings)
	}
}

func TestApplyEncryptionFlagOverrides(t *testing.T) {
	savedEncrypt := encryptFlag
	savedAllowPlaintext := allowPlaintextFlag
	t.Cleanup(func() {
		encryptFlag = savedEncrypt
		allowPlaintextFlag = savedAllowPlaintext
	})

	t.Run("encrypt_forces_enabled", func(t *testing.T) {
		encryptFlag = true
		allowPlaintextFlag = false
		c := config.Default()
		c.Encryption.Enabled = false
		if err := applyEncryptionFlagOverrides(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !c.Encryption.Enabled {
			t.Fatal("expected --encrypt to enable cfg.Encryption.Enabled")
		}
	})

	t.Run("allow_plaintext_forces_disabled", func(t *testing.T) {
		encryptFlag = false
		allowPlaintextFlag = true
		c := config.Default()
		c.Encryption.Enabled = true
		if err := applyEncryptionFlagOverrides(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Encryption.Enabled {
			t.Fatal("expected --allow-plaintext to disable cfg.Encryption.Enabled")
		}
	})

	t.Run("mutually_exclusive_rejected", func(t *testing.T) {
		encryptFlag = true
		allowPlaintextFlag = true
		c := config.Default()
		if err := applyEncryptionFlagOverrides(c); err == nil {
			t.Fatal("expected error when both --encrypt and --allow-plaintext are set")
		}
	})
}
