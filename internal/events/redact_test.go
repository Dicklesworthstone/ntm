package events

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
	})
}

func TestRedactEvent(t *testing.T) {
	// Ensure clean state
	SetRedactionConfig(nil)
	defer SetRedactionConfig(nil)

	// Synthetic test secret
	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	errorWithSecret := "Failed to authenticate with key: " + testSecret

	t.Run("nil_config_no_redaction", func(t *testing.T) {
		SetRedactionConfig(nil)

		event := NewEvent(EventError, "test-session", map[string]interface{}{
			"message": errorWithSecret,
		})
		redacted := RedactEvent(event)

		if msg, ok := redacted.Data["message"].(string); ok {
			if msg != errorWithSecret {
				t.Error("event should be unchanged when redaction disabled")
			}
		}
	})

	t.Run("off_mode_no_redaction", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeOff})

		event := NewEvent(EventError, "test-session", map[string]interface{}{
			"message": errorWithSecret,
		})
		redacted := RedactEvent(event)

		if msg, ok := redacted.Data["message"].(string); ok {
			if msg != errorWithSecret {
				t.Error("event should be unchanged in off mode")
			}
		}
	})

	t.Run("warn_mode_redacts_for_storage", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeWarn})

		event := NewEvent(EventError, "test-session", map[string]interface{}{
			"message": errorWithSecret,
		})
		redacted := RedactEvent(event)

		if msg, ok := redacted.Data["message"].(string); ok {
			if contains(msg, testSecret) {
				t.Errorf("redacted message still contains secret in warn mode: %q", msg)
			}
			if !contains(msg, "[REDACTED:") {
				t.Errorf("redacted message should contain redaction marker in warn mode: %q", msg)
			}
		} else {
			t.Error("message field missing or not a string")
		}
	})

	t.Run("redact_mode_removes_secrets_from_message", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})

		event := NewEvent(EventError, "test-session", map[string]interface{}{
			"message": errorWithSecret,
		})
		redacted := RedactEvent(event)

		if msg, ok := redacted.Data["message"].(string); ok {
			if contains(msg, testSecret) {
				t.Errorf("redacted message still contains secret: %q", msg)
			}
			if !contains(msg, "[REDACTED:") {
				t.Errorf("redacted message should contain redaction marker: %q", msg)
			}
		} else {
			t.Error("message field missing or not a string")
		}
	})

	t.Run("preserves_non_sensitive_fields", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})

		event := NewEvent(EventPromptSend, "test-session", map[string]interface{}{
			"target_count":  3,
			"prompt_length": 100,
			"template":      "my-template",
		})
		redacted := RedactEvent(event)

		if redacted.Data["target_count"] != 3 {
			t.Errorf("target_count = %v, want 3", redacted.Data["target_count"])
		}
		if redacted.Data["prompt_length"] != 100 {
			t.Errorf("prompt_length = %v, want 100", redacted.Data["prompt_length"])
		}
		if redacted.Data["template"] != "my-template" {
			t.Errorf("template = %v, want my-template", redacted.Data["template"])
		}
	})

	t.Run("nil_event_returns_nil", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
		if RedactEvent(nil) != nil {
			t.Error("expected nil for nil event")
		}
	})

	t.Run("nil_data_preserved", func(t *testing.T) {
		SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
		event := NewEvent(EventSessionCreate, "test-session", nil)
		redacted := RedactEvent(event)
		if redacted.Data != nil {
			t.Error("nil data should remain nil")
		}
	})
}

func TestRedactDataMap(t *testing.T) {
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	defer SetRedactionConfig(nil)

	testSecret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"

	t.Run("redacts_sensitive_fields", func(t *testing.T) {
		data := map[string]interface{}{
			"message":    "Error with secret: " + testSecret,
			"error":      "Auth failed: " + testSecret,
			"safe_field": "This is safe",
			"count":      42,
		}

		redacted := redactDataMap(data)

		// message and error should be redacted
		if msg, ok := redacted["message"].(string); ok && contains(msg, testSecret) {
			t.Errorf("message field should be redacted")
		}
		if err, ok := redacted["error"].(string); ok && contains(err, testSecret) {
			t.Errorf("error field should be redacted")
		}

		// safe_field should be unchanged (not in sensitive list)
		if redacted["safe_field"] != "This is safe" {
			t.Errorf("safe_field should be unchanged")
		}

		// Non-string fields unchanged
		if redacted["count"] != 42 {
			t.Errorf("count should be unchanged")
		}
	})
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
