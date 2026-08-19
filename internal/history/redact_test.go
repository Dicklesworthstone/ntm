package history

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestSetRedactionConfig(t *testing.T) {
	// Ensure clean state
	SetRedactionConfig(nil)
	defer SetRedactionConfig(nil)

	t.Run("nil_config_disables_redaction", func(t *testing.T) {
		SetRedactionConfig(nil)
		cfg := redactionConfigForTest()
		if cfg != nil {
			t.Error("expected nil config after setting nil")
		}
	})

	t.Run("set_and_get_config", func(t *testing.T) {
		cfg := &redaction.Config{
			Mode:      redaction.ModeRedact,
			Allowlist: []string{"test-.*"},
		}
		SetRedactionConfig(cfg)

		got := redactionConfigForTest()
		if got == nil {
			t.Fatal("expected non-nil config")
		}
		if got.Mode != redaction.ModeRedact {
			t.Errorf("mode = %q, want %q", got.Mode, redaction.ModeRedact)
		}
		if len(got.Allowlist) != 1 {
			t.Errorf("allowlist length = %d, want 1", len(got.Allowlist))
		}
	})

	t.Run("config_is_copied", func(t *testing.T) {
		cfg := &redaction.Config{Mode: redaction.ModeRedact}
		SetRedactionConfig(cfg)

		// Modify original
		cfg.Mode = redaction.ModeBlock

		// Get should return the original value
		got := redactionConfigForTest()
		if got.Mode != redaction.ModeRedact {
			t.Errorf("config should be a copy, got mode = %q", got.Mode)
		}
	})
}

func TestRedactEntry(t *testing.T) {
	// Ensure clean state
	SetRedactionConfig(nil)
	defer SetRedactionConfig(nil)

	// Synthetic test secret (matches OPENAI_KEY pattern)
	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	promptWithSecret := "Please use this API key: " + testSecret + " for authentication."

	t.Run("nil_config_no_redaction", func(t *testing.T) {
		SetRedactionConfig(nil)

		entry := NewEntry("test-session", []string{"0"}, promptWithSecret, SourceCLI)
		redacted := RedactEntry(entry)

		if redacted.Prompt != promptWithSecret {
			t.Errorf("prompt should be unchanged when redaction disabled")
		}
	})

	t.Run("off_mode_no_redaction", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeOff})

		entry := NewEntry("test-session", []string{"0"}, promptWithSecret, SourceCLI)
		redacted := RedactEntry(entry)

		if redacted.Prompt != promptWithSecret {
			t.Errorf("prompt should be unchanged in off mode")
		}
	})

	t.Run("warn_mode_redacts_for_storage", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeWarn})

		entry := NewEntry("test-session", []string{"0"}, promptWithSecret, SourceCLI)
		redacted := RedactEntry(entry)

		if contains(redacted.Prompt, testSecret) {
			t.Errorf("redacted prompt still contains secret in warn mode: %q", redacted.Prompt)
		}
		if !contains(redacted.Prompt, "[REDACTED:") {
			t.Errorf("redacted prompt should contain redaction marker in warn mode: %q", redacted.Prompt)
		}
	})

	t.Run("redact_mode_removes_secrets", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})

		entry := NewEntry("test-session", []string{"0"}, promptWithSecret, SourceCLI)
		redacted := RedactEntry(entry)

		// Secret should be replaced
		if redacted.Prompt == promptWithSecret {
			t.Errorf("prompt should be redacted in redact mode")
		}
		if contains(redacted.Prompt, testSecret) {
			t.Errorf("redacted prompt still contains secret: %q", redacted.Prompt)
		}
		// Should contain redaction marker
		if !contains(redacted.Prompt, "[REDACTED:") {
			t.Errorf("redacted prompt should contain redaction marker: %q", redacted.Prompt)
		}
	})

	t.Run("block_mode_redacts_too", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeBlock})

		entry := NewEntry("test-session", []string{"0"}, promptWithSecret, SourceCLI)
		redacted := RedactEntry(entry)

		// Block mode also stores redacted version
		if contains(redacted.Prompt, testSecret) {
			t.Errorf("redacted prompt still contains secret in block mode: %q", redacted.Prompt)
		}
	})

	t.Run("preserves_other_fields", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})

		entry := NewEntry("my-session", []string{"0", "1"}, promptWithSecret, SourcePalette)
		entry.Template = "test-template"
		entry.Success = true
		entry.DurationMs = 100

		redacted := RedactEntry(entry)

		if redacted.Session != "my-session" {
			t.Errorf("session = %q, want %q", redacted.Session, "my-session")
		}
		if len(redacted.Targets) != 2 {
			t.Errorf("targets length = %d, want 2", len(redacted.Targets))
		}
		if redacted.Source != SourcePalette {
			t.Errorf("source = %q, want %q", redacted.Source, SourcePalette)
		}
		if redacted.Template != "test-template" {
			t.Errorf("template = %q, want %q", redacted.Template, "test-template")
		}
		if !redacted.Success {
			t.Error("success should be true")
		}
		if redacted.DurationMs != 100 {
			t.Errorf("duration_ms = %d, want 100", redacted.DurationMs)
		}
	})

	t.Run("nil_entry_returns_nil", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
		if RedactEntry(nil) != nil {
			t.Error("expected nil for nil entry")
		}
	})
}

func TestRedactPromptIntegration(t *testing.T) {
	// Test multiple secret types
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	defer SetRedactionConfig(nil)

	tests := []struct {
		name           string
		prompt         string
		shouldContain  string // substring that SHOULD remain
		shouldNotMatch string // substring that should be redacted
	}{
		{
			name:           "openai_key",
			prompt:         "key: sk-proj-FAKEtestkey1234567890123456789012345678901234",
			shouldContain:  "key:",
			shouldNotMatch: "sk-proj-FAKE",
		},
		{
			name:           "github_token",
			prompt:         "token: ghp_FAKEtesttokenvalue12345678901234567",
			shouldContain:  "token:",
			shouldNotMatch: "ghp_FAKE",
		},
		{
			name:           "aws_key",
			prompt:         "AWS: AKIAFAKETEST12345678",
			shouldContain:  "AWS:",
			shouldNotMatch: "AKIAFAKE",
		},
		{
			name:           "no_secrets",
			prompt:         "This is a safe prompt with no secrets",
			shouldContain:  "This is a safe prompt",
			shouldNotMatch: "", // nothing to redact
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := NewEntry("test", []string{"0"}, tt.prompt, SourceCLI)
			redacted := RedactEntry(entry)

			if tt.shouldContain != "" && !contains(redacted.Prompt, tt.shouldContain) {
				t.Errorf("prompt should contain %q, got: %q", tt.shouldContain, redacted.Prompt)
			}
			if tt.shouldNotMatch != "" && contains(redacted.Prompt, tt.shouldNotMatch) {
				t.Errorf("prompt should NOT contain %q, got: %q", tt.shouldNotMatch, redacted.Prompt)
			}
		})
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && indexOf(s, substr) >= 0
}

// indexOf returns index of substr in s, or -1 if not found
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// redactionConfigForTest returns the currently configured redaction config.
func redactionConfigForTest() *redaction.Config {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	return redactionConfig
}
