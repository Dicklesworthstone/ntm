package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseWebhookConfig_Basic(t *testing.T) {
	t.Setenv("NTM_WEBHOOK_SECRET", "secret-value")

	content := `
scanner:
  defaults:
    timeout: 30s
webhooks:
  - name: slack-notifications
    url: https://hooks.slack.com/services/xxx
    events:
      - agent.completed
      - agent.error
    formatter: slack
    filter:
      session: myproject*
      agent_type: [claude, codex]
      severity: [warning, error]
    retry:
      max_attempts: 5
      backoff: exponential
    timeout: 30s
    secret: ${NTM_WEBHOOK_SECRET}
`

	cfgs, err := ParseWebhookConfig([]byte(content))
	if err != nil {
		t.Fatalf("ParseWebhookConfig failed: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(cfgs))
	}

	cfg := cfgs[0]
	if cfg.Name != "slack-notifications" {
		t.Fatalf("unexpected name: %q", cfg.Name)
	}
	if cfg.Formatter != "slack" {
		t.Fatalf("unexpected formatter: %q", cfg.Formatter)
	}
	if cfg.Secret != "secret-value" {
		t.Fatalf("expected env-substituted secret, got %q", cfg.Secret)
	}
	if cfg.Filter.Session != "myproject*" {
		t.Fatalf("unexpected filter.session: %q", cfg.Filter.Session)
	}
	if len(cfg.Filter.AgentType) != 2 {
		t.Fatalf("unexpected filter.agent_type: %#v", cfg.Filter.AgentType)
	}
	if cfg.Timeout != "30s" {
		t.Fatalf("unexpected timeout: %q", cfg.Timeout)
	}
}

func TestParseWebhookConfig_SupportsExtendedAgentTypes(t *testing.T) {
	content := `
webhooks:
  - name: agent-routing
    url: https://example.com/hook
    filter:
      agent_type: [cursor, ws, aider, ollama, claude-code, codex-cli, google-gemini]
`

	cfgs, err := ParseWebhookConfig([]byte(content))
	if err != nil {
		t.Fatalf("ParseWebhookConfig failed: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(cfgs))
	}
	if got := len(cfgs[0].Filter.AgentType); got != 7 {
		t.Fatalf("unexpected filter.agent_type length: %d", got)
	}
}

func TestParseWebhookConfig_MissingEnvVar(t *testing.T) {
	content := `
webhooks:
  - name: missing-secret
    url: https://example.com/hook
    secret: ${NTM_MISSING_SECRET}
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for missing env var, got nil")
	}
	if !strings.Contains(err.Error(), "missing environment variables") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebhookConfig_UnknownWebhookField(t *testing.T) {
	content := `
webhooks:
  - name: bad
    url: https://example.com/hook
    unknown_field: true
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestParseWebhookConfig_InvalidURL(t *testing.T) {
	content := `
webhooks:
  - name: bad-url
    url: ftp://example.com/hook
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for invalid url, got nil")
	}
	if !strings.Contains(err.Error(), "invalid url scheme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebhookConfig_HTTPDisallowed(t *testing.T) {
	content := `
webhooks:
  - name: http-not-allowed
    url: http://example.com/hook
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for insecure http url, got nil")
	}
	if !strings.Contains(err.Error(), "only https is allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebhookConfig_HTTPLocalhostAllowed(t *testing.T) {
	content := `
webhooks:
  - name: localhost-http
    url: http://localhost:8080/hook
`
	cfgs, err := ParseWebhookConfig([]byte(content))
	if err != nil {
		t.Fatalf("expected localhost http to be allowed, got error: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(cfgs))
	}
	if cfgs[0].URL != "http://localhost:8080/hook" {
		t.Fatalf("unexpected url: %q", cfgs[0].URL)
	}
}

func TestParseWebhookConfig_InvalidFormatter(t *testing.T) {
	content := `
webhooks:
  - name: bad-formatter
    url: https://example.com/hook
    formatter: not-a-real-formatter
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for invalid formatter, got nil")
	}
	if !strings.Contains(err.Error(), "unknown formatter") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebhookConfig_InvalidEvent(t *testing.T) {
	content := `
webhooks:
  - name: bad-event
    url: https://example.com/hook
    events: ["not.real"]
`
	_, err := ParseWebhookConfig([]byte(content))
	if err == nil {
		t.Fatal("expected error for invalid event, got nil")
	}
	if !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebhookConfig_RetryDefaultsApplyWhenMaxAttemptsOmitted(t *testing.T) {
	content := `
webhooks:
  - name: partial-retry
    url: https://example.com/hook
    retry:
      backoff: exponential
`

	cfgs, err := ParseWebhookConfig([]byte(content))
	if err != nil {
		t.Fatalf("ParseWebhookConfig failed: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(cfgs))
	}
	if cfgs[0].Retry.MaxAttempts != 5 {
		t.Fatalf("expected default retry.max_attempts of 5, got %d", cfgs[0].Retry.MaxAttempts)
	}
	if cfgs[0].Retry.Backoff != "exponential" {
		t.Fatalf("expected retry.backoff to remain exponential, got %q", cfgs[0].Retry.Backoff)
	}
}

func TestParseWebhookConfig_RetryZeroPreservedWhenExplicit(t *testing.T) {
	content := `
webhooks:
  - name: disabled-retry
    url: https://example.com/hook
    retry:
      max_attempts: 0
`

	cfgs, err := ParseWebhookConfig([]byte(content))
	if err != nil {
		t.Fatalf("ParseWebhookConfig failed: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(cfgs))
	}
	if cfgs[0].Retry.MaxAttempts != 0 {
		t.Fatalf("expected explicit retry.max_attempts=0 to be preserved, got %d", cfgs[0].Retry.MaxAttempts)
	}
	if cfgs[0].Retry.Backoff != "exponential" {
		t.Fatalf("expected default retry.backoff, got %q", cfgs[0].Retry.Backoff)
	}
}

// TestIsValidWebhookAgentType tests all branches of the agent type validator.
func TestIsValidWebhookAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"claude", true},
		{"cc", true},
		{"codex", true},
		{"cod", true},
		{"gemini", true},
		{"gmi", true},
		{"CLAUDE", true},
		{"  cc  ", true},
		{"Codex", true},
		{"cursor", true},
		{"windsurf", true},
		{"ws", true},
		{"aider", true},
		{"ollama", true},
		{"claude-code", true},
		{"codex-cli", true},
		{"google-gemini", true},
		{"", false},
		{"python", false},
		{"claude2", false},
		{"   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := isValidWebhookAgentType(tc.input)
			if got != tc.want {
				t.Errorf("isValidWebhookAgentType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestIsValidWebhookSeverity tests all branches of the severity validator.
func TestIsValidWebhookSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"info", true},
		{"success", true},
		{"warning", true},
		{"warn", true},
		{"error", true},
		{"INFO", true},
		{"  warning  ", true},
		{"Error", true},
		{"", false},
		{"debug", false},
		{"fatal", false},
		{"   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := isValidWebhookSeverity(tc.input)
			if got != tc.want {
				t.Errorf("isValidWebhookSeverity(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// webhookNames — all branches (bd-4b4zf)
// =============================================================================

// =============================================================================
// findTopLevelYAMLKey — all branches (bd-4b4zf)
// =============================================================================

func TestFindTopLevelYAMLKey_AllBranches(t *testing.T) {
	t.Parallel()

	t.Run("nil root returns nil", func(t *testing.T) {
		t.Parallel()
		if got := findTopLevelYAMLKey(nil, "key"); got != nil {
			t.Error("expected nil for nil root")
		}
	})

	t.Run("non-mapping node returns nil", func(t *testing.T) {
		t.Parallel()
		node := &yaml.Node{Kind: yaml.ScalarNode, Value: "hello"}
		if got := findTopLevelYAMLKey(node, "key"); got != nil {
			t.Error("expected nil for scalar node")
		}
	})

	t.Run("document node unwraps to mapping", func(t *testing.T) {
		t.Parallel()
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte("webhooks:\n  - name: test"), &doc); err != nil {
			t.Fatal(err)
		}
		got := findTopLevelYAMLKey(&doc, "webhooks")
		if got == nil {
			t.Fatal("expected non-nil for existing key under document node")
		}
	})

	t.Run("key not found returns nil", func(t *testing.T) {
		t.Parallel()
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte("other: value"), &doc); err != nil {
			t.Fatal(err)
		}
		if got := findTopLevelYAMLKey(&doc, "missing"); got != nil {
			t.Error("expected nil for missing key")
		}
	})

	t.Run("key found returns value node", func(t *testing.T) {
		t.Parallel()
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte("foo: bar\nbaz: qux"), &doc); err != nil {
			t.Fatal(err)
		}
		got := findTopLevelYAMLKey(&doc, "baz")
		if got == nil || got.Value != "qux" {
			t.Errorf("expected value 'qux', got %v", got)
		}
	})
}
