package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestBuildSafetyDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.Redaction.Mode = "redact"
	cfg.Redaction.Allowlist = []string{"safe-token", "test-key"}
	cfg.Privacy.Enabled = true
	cfg.Preflight.Enabled = true
	cfg.Preflight.Strict = true

	got := buildSafetyDefaults(cfg)

	if got.RedactionMode != "redact" {
		t.Fatalf("RedactionMode=%q, want %q", got.RedactionMode, "redact")
	}
	if !got.RedactionAllowlistEnabled {
		t.Fatal("RedactionAllowlistEnabled=false, want true")
	}
	if got.RedactionAllowlistCount != 2 {
		t.Fatalf("RedactionAllowlistCount=%d, want 2", got.RedactionAllowlistCount)
	}
	if !got.PrivacyDefaultEnabled {
		t.Fatal("PrivacyDefaultEnabled=false, want true")
	}
	if got.EncryptionAtRestEnabled {
		t.Fatal("EncryptionAtRestEnabled=true, want false")
	}
	if !got.PreflightDefaultEnabled {
		t.Fatal("PreflightDefaultEnabled=false, want true")
	}
	if !got.PreflightDefaultStrict {
		t.Fatal("PreflightDefaultStrict=false, want true")
	}
}

// TestBuildSafetyDefaults_NilConfig tests the nil cfg branch (line 459-461).
func TestBuildSafetyDefaults_NilConfig(t *testing.T) {

	got := buildSafetyDefaults(nil)
	// nil config falls back to config.Default(), which has a non-empty Mode
	if got.RedactionMode == "" {
		t.Error("RedactionMode should be non-empty with nil config (uses default)")
	}
}

// TestBuildSafetyDefaults_EmptyRedactionMode tests the empty mode branch (line 464-466).
func TestBuildSafetyDefaults_EmptyRedactionMode(t *testing.T) {

	cfg := config.Default()
	cfg.Redaction.Mode = "" // Empty mode should fall back to default

	got := buildSafetyDefaults(cfg)
	if got.RedactionMode == "" {
		t.Error("RedactionMode should not be empty when Mode is empty string")
	}
	// Should use the default mode
	defaultMode := config.DefaultRedactionConfig().Mode
	if got.RedactionMode != defaultMode {
		t.Errorf("RedactionMode = %q, want default %q", got.RedactionMode, defaultMode)
	}
}

func TestEncodeDoctorJSONIncludesSafetyDefaults(t *testing.T) {
	report := &DoctorReport{
		Timestamp: time.Date(2026, 2, 4, 0, 0, 0, 0, time.UTC),
		Overall:   "healthy",
		SafetyDefaults: SafetyDefaults{
			RedactionMode:             "warn",
			RedactionAllowlistEnabled: true,
			RedactionAllowlistCount:   1,
			PrivacyDefaultEnabled:     false,
			EncryptionAtRestEnabled:   false,
			PreflightDefaultEnabled:   true,
			PreflightDefaultStrict:    false,
		},
		Tools:         []ToolCheck{},
		Dependencies:  []DepCheck{},
		Daemons:       []DaemonCheck{},
		Configuration: []ConfigCheck{},
		Invariants:    []InvariantCheck{},
	}

	buf := &bytes.Buffer{}
	if err := encodeDoctorJSON(buf, report); err != nil {
		t.Fatalf("encodeDoctorJSON error: %v", err)
	}

	var decoded DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}

	if decoded.SafetyDefaults.RedactionMode != "warn" {
		t.Fatalf("decoded RedactionMode=%q, want %q", decoded.SafetyDefaults.RedactionMode, "warn")
	}
	if !decoded.SafetyDefaults.PreflightDefaultEnabled {
		t.Fatalf("decoded PreflightDefaultEnabled=false, want true")
	}
}

func TestRenderDoctorTUIIncludesSafetyDefaults(t *testing.T) {
	report := &DoctorReport{
		Timestamp: time.Now(),
		Overall:   "healthy",
		SafetyDefaults: SafetyDefaults{
			RedactionMode:           "warn",
			PreflightDefaultEnabled: true,
		},
	}

	buf := &bytes.Buffer{}
	if err := renderDoctorTUITo(buf, report); err != nil {
		t.Fatalf("renderDoctorTUITo error: %v", err)
	}

	out := buf.String()
	for _, needle := range []string{
		"Safety Defaults",
		"Redaction mode",
		"Privacy default",
		"Encryption at rest",
		"Prompt preflight",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("expected output to contain %q", needle)
		}
	}
}

func TestEnrichAgentMailDaemonCheckUsesMCPHealth(t *testing.T) {
	oldCheck := checkAgentMailServerHealthDoctor
	t.Cleanup(func() {
		checkAgentMailServerHealthDoctor = oldCheck
	})
	checkAgentMailServerHealthDoctor = func(ctx context.Context, serverURL, token string) tools.AgentMailServerHealth {
		if serverURL != "http://127.0.0.1:8765" {
			t.Fatalf("serverURL=%q, want default Agent Mail URL", serverURL)
		}
		if token != "" {
			t.Fatalf("token should be empty in default doctor health scope")
		}
		return tools.AgentMailServerHealth{
			Healthy:      false,
			DoctorStatus: "error",
			Message:      "Agent Mail health_check status=ok health_level=green recovery_mode=corrupt",
		}
	}

	check := DaemonCheck{
		Name:    "agent-mail",
		Running: true,
		Status:  "ok",
		Message: "listening on port 8765",
	}
	enrichAgentMailDaemonCheck(context.Background(), &check)

	if check.Status != "error" {
		t.Fatalf("Status=%q, want error", check.Status)
	}
	if !strings.Contains(check.Message, "recovery_mode=corrupt") {
		t.Fatalf("Message=%q, want corrupt recovery", check.Message)
	}
	if check.AgentMailEndpoint == nil {
		t.Fatal("AgentMailEndpoint should be populated")
	}
	if check.AgentMailEndpoint.ConfiguredURL != "http://127.0.0.1:8765" {
		t.Fatalf("ConfiguredURL=%q, want default", check.AgentMailEndpoint.ConfiguredURL)
	}
}

func TestConfiguredAgentMailHealthScopeUsesConfig(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() {
		cfg = oldCfg
	})
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	cfg = config.Default()
	cfg.AgentMail.URL = "http://example.test:8765/mcp/"
	cfg.AgentMail.Token = "configured-token"

	serverURL, token := configuredAgentMailHealthScope()
	if serverURL != cfg.AgentMail.URL {
		t.Fatalf("serverURL=%q, want config URL", serverURL)
	}
	if token != cfg.AgentMail.Token {
		t.Fatalf("token did not come from config")
	}
}

func TestEnrichAgentMailDaemonCheckSuggestsLocalEndpointWhenConfiguredRemoteFails(t *testing.T) {
	oldCfg := cfg
	oldCheck := checkAgentMailServerHealthDoctor
	t.Cleanup(func() {
		cfg = oldCfg
		checkAgentMailServerHealthDoctor = oldCheck
	})
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	cfg = config.Default()
	cfg.AgentMail.URL = "https://agentmail.zeststream.ai/mcp/"
	cfg.AgentMail.Token = "configured-secret-token"

	checkAgentMailServerHealthDoctor = func(ctx context.Context, serverURL, token string) tools.AgentMailServerHealth {
		if serverURL == cfg.AgentMail.URL {
			if token != cfg.AgentMail.Token {
				t.Fatalf("remote token=%q, want configured token", token)
			}
			return tools.AgentMailServerHealth{
				Healthy:      false,
				DoctorStatus: "error",
				Message:      "Agent Mail server not responding",
				Error:        "remote unavailable",
			}
		}
		if serverURL != agentMailLocalEndpointURL {
			t.Fatalf("unexpected fallback serverURL=%q", serverURL)
		}
		if token != "" {
			t.Fatalf("local fallback token should be implicit, got %q", token)
		}
		return tools.AgentMailServerHealth{
			Healthy:      true,
			DoctorStatus: "ok",
			Message:      "Agent Mail health_check status=ok health_level=green",
			HealthStatus: "ok",
			HealthLevel:  "green",
		}
	}

	check := DaemonCheck{
		Name:    "agent-mail",
		Running: true,
		Status:  "ok",
		Message: "listening on port 8765",
	}
	enrichAgentMailDaemonCheck(context.Background(), &check)

	if check.AgentMailEndpoint == nil {
		t.Fatal("AgentMailEndpoint should be populated")
	}
	diag := check.AgentMailEndpoint
	if diag.ConfiguredURL != cfg.AgentMail.URL {
		t.Fatalf("ConfiguredURL=%q, want %q", diag.ConfiguredURL, cfg.AgentMail.URL)
	}
	if !diag.LocalFallbackAvailable {
		t.Fatal("LocalFallbackAvailable=false, want true")
	}
	wantCommand := "ntm config set agent_mail.url http://127.0.0.1:8765/mcp/ --json"
	if diag.RemediationCommand != wantCommand {
		t.Fatalf("RemediationCommand=%q, want %q", diag.RemediationCommand, wantCommand)
	}
	if !strings.Contains(check.Message, wantCommand) {
		t.Fatalf("Message=%q, want remediation command", check.Message)
	}

	data, err := json.Marshal(check)
	if err != nil {
		t.Fatalf("marshal check: %v", err)
	}
	if strings.Contains(string(data), cfg.AgentMail.Token) {
		t.Fatalf("diagnostic leaked configured token: %s", string(data))
	}
}

func TestBuildAgentMailEndpointDiagnosticDoesNotSuggestFallbackWhenLocalIsUnhealthy(t *testing.T) {
	oldCheck := checkAgentMailServerHealthDoctor
	t.Cleanup(func() {
		checkAgentMailServerHealthDoctor = oldCheck
	})

	checkAgentMailServerHealthDoctor = func(ctx context.Context, serverURL, token string) tools.AgentMailServerHealth {
		return tools.AgentMailServerHealth{
			Healthy:      false,
			DoctorStatus: "warning",
			Message:      "Agent Mail server listening but MCP health_check failed",
		}
	}

	diag := buildAgentMailEndpointDiagnostic(context.Background(), "https://agentmail.zeststream.ai/mcp/", tools.AgentMailServerHealth{
		Healthy:      false,
		DoctorStatus: "error",
		Message:      "Agent Mail server not responding",
	})

	if diag.LocalFallbackAvailable {
		t.Fatal("LocalFallbackAvailable=true, want false")
	}
	if diag.RemediationCommand != "" {
		t.Fatalf("RemediationCommand=%q, want empty when local fallback is unhealthy", diag.RemediationCommand)
	}
}
