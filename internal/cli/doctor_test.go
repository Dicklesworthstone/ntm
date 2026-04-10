package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
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
