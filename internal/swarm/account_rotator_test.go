package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeFakeCAAM(t *testing.T, dir, stateFile string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake caam helper uses /bin/sh")
	}

	path := filepath.Join(dir, "caam")
	script := fmt.Sprintf(`#!/bin/sh
set -eu

STATE_FILE=%q

# Current caam emits {"profiles":[{tool,name,active,system,health}],"count":N}.
# claude-b is held in cooldown so rate-limit filtering has something to exclude,
# and a system profile is included because caam always reports those.
if [ "${1:-}" = "list" ] && [ "${2:-}" = "--json" ]; then
  active="$(cat "$STATE_FILE" 2>/dev/null || true)"
  if [ "$active" = "claude-b" ]; then
    a_active=false
    b_active=true
  else
    a_active=true
    b_active=false
  fi
  printf '{"profiles":[{"tool":"claude","name":"_original","active":false,"system":true,"health":{"status":"warning"}},{"tool":"claude","name":"claude-a","active":%%s,"system":false,"health":{"status":"ok"}},{"tool":"claude","name":"claude-b","active":%%s,"system":false,"health":{"status":"cooldown"}}],"count":3}\n' "$a_active" "$b_active"
  exit 0
fi

# Current caam CLI: activate <tool> [profile] [--auto] [--json]. The old
# "switch <provider> --next --json" form no longer exists, and caam names the
# tool "codex"/"gemini", never "openai"/"google" -- it rejects those outright.
if [ "${1:-}" = "activate" ] && [ "${2:-}" = "claude" ] && [ "${3:-}" = "--auto" ] && [ "${4:-}" = "--json" ]; then
  prev="$(cat "$STATE_FILE" 2>/dev/null || true)"
  if [ "$prev" = "claude-b" ]; then
    next="claude-a"
  else
    next="claude-b"
  fi
  echo "$next" > "$STATE_FILE"
  echo "{\"success\":true,\"profile\":\"$next\",\"previous_profile\":\"$prev\"}"
  exit 0
fi

if [ "${1:-}" = "activate" ]; then
  tool="${2:-}"
  case "$tool" in
    codex|claude|gemini) ;;
    *) echo "unknown tool: $tool" >&2; exit 1 ;;
  esac
  acct="${3:-}"
  echo "$acct" > "$STATE_FILE"
  exit 0
fi

echo "unexpected args" >&2
exit 2
`, stateFile)

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}

	return path
}

func TestNewAccountRotator(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator == nil {
		t.Fatal("NewAccountRotator returned nil")
	}

	if rotator.caamPath != "caam" {
		t.Errorf("expected caamPath 'caam', got %q", rotator.caamPath)
	}

	if rotator.CommandTimeout != 5*time.Second {
		t.Errorf("expected CommandTimeout 5s, got %v", rotator.CommandTimeout)
	}

	if rotator.Logger == nil {
		t.Error("expected non-nil Logger")
	}

	if rotator.rotationHistory == nil {
		t.Error("expected rotationHistory to be initialized")
	}
}

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"cc", "claude"},
		{"claude", "claude"},
		{"claude-code", "claude"},
		{"claude_code", "claude"},
		{"anthropic", "claude"},
		{"cod", "openai"},
		{"codex", "openai"},
		{"codex-cli", "openai"},
		{"openai-codex", "openai"},
		{"gmi", "google"},
		{"gemini", "google"},
		{"gemini-cli", "google"},
		{"google-gemini", "google"},
		{"google-ai", "google"},
		{"unknown", "unknown"},
		{"openai", "openai"},
		{"google", "google"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := normalizeProvider(tt.agentType)
			if result != tt.expected {
				t.Errorf("normalizeProvider(%q) = %q, want %q", tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestAccountRotatorLogger(t *testing.T) {
	rotator := NewAccountRotator()
	logger := rotator.logger()

	if logger == nil {
		t.Error("expected non-nil logger from logger()")
	}
}

func TestAccountInfoFields(t *testing.T) {
	now := time.Now()
	info := AccountInfo{
		Provider:    "claude",
		AccountName: "personal",
		IsActive:    true,
		LastUsed:    now,
	}

	if info.Provider != "claude" {
		t.Errorf("unexpected Provider: %s", info.Provider)
	}
	if info.AccountName != "personal" {
		t.Errorf("unexpected AccountName: %s", info.AccountName)
	}
	if !info.IsActive {
		t.Error("expected IsActive to be true")
	}
	if !info.LastUsed.Equal(now) {
		t.Errorf("unexpected LastUsed: %v", info.LastUsed)
	}
}

func TestRotationRecordFields(t *testing.T) {
	now := time.Now()
	record := RotationRecord{
		Provider:    "openai",
		FromAccount: "work",
		ToAccount:   "personal",
		RotatedAt:   now,
		SessionPane: "test:1.1",
		TriggeredBy: "limit_hit",
	}

	if record.Provider != "openai" {
		t.Errorf("unexpected Provider: %s", record.Provider)
	}
	if record.FromAccount != "work" {
		t.Errorf("unexpected FromAccount: %s", record.FromAccount)
	}
	if record.ToAccount != "personal" {
		t.Errorf("unexpected ToAccount: %s", record.ToAccount)
	}
	if record.SessionPane != "test:1.1" {
		t.Errorf("unexpected SessionPane: %s", record.SessionPane)
	}
	if record.TriggeredBy != "limit_hit" {
		t.Errorf("unexpected TriggeredBy: %s", record.TriggeredBy)
	}
}

func TestNormalizeProviderSharedAliases(t *testing.T) {
	expectedMappings := map[string]string{
		"cc":            "claude",
		"claude":        "claude",
		"claude-code":   "claude",
		"claude_code":   "claude",
		"anthropic":     "claude",
		"cod":           "openai",
		"codex":         "openai",
		"codex-cli":     "openai",
		"openai-codex":  "openai",
		"gmi":           "google",
		"gemini":        "google",
		"gemini-cli":    "google",
		"google-gemini": "google",
		"google-ai":     "google",
	}

	for agent, expected := range expectedMappings {
		if provider := normalizeProvider(agent); provider != expected {
			t.Errorf("normalizeProvider(%q) = %q, want %q", agent, provider, expected)
		}
	}
}

func TestCaamStatusStruct(t *testing.T) {
	status := caamStatus{
		Provider:      "claude",
		ActiveAccount: "personal",
		AccountCount:  3,
	}

	if status.Provider != "claude" {
		t.Errorf("unexpected Provider: %s", status.Provider)
	}
	if status.ActiveAccount != "personal" {
		t.Errorf("unexpected ActiveAccount: %s", status.ActiveAccount)
	}
	if status.AccountCount != 3 {
		t.Errorf("unexpected AccountCount: %d", status.AccountCount)
	}
}

func TestCaamAccountStruct(t *testing.T) {
	account := caamAccount{
		Name:   "work",
		Active: false,
	}

	if account.Name != "work" {
		t.Errorf("unexpected Name: %s", account.Name)
	}
	if account.Active {
		t.Error("expected Active to be false")
	}
}

func TestAccountRotatorDefaultCooldown(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator.CooldownDuration != 60*time.Second {
		t.Errorf("expected default CooldownDuration 60s, got %v", rotator.CooldownDuration)
	}
}

func TestAccountRotatorRotationStatesInitialized(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator.rotationStates == nil {
		t.Error("expected rotationStates to be initialized")
	}
	if len(rotator.rotationStates) != 0 {
		t.Errorf("expected empty rotationStates, got %d", len(rotator.rotationStates))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAccountRotatorIsAvailableWithInvalidPath(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/path/to/caam")

	// Should return false for invalid path
	if rotator.IsAvailable() {
		t.Error("expected IsAvailable to return false for invalid path")
	}

	// Should cache the result
	if rotator.IsAvailable() {
		t.Error("expected cached result to be false")
	}
}

func TestAccountRotatorListAvailableAccounts_FiltersRateLimited(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state")
	if err := os.WriteFile(stateFile, []byte("claude-a"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	caamPath := writeFakeCAAM(t, dir, stateFile)
	rotator := NewAccountRotator().WithCaamPath(caamPath)

	available, err := rotator.ListAvailableAccounts("cc")
	if err != nil {
		t.Fatalf("ListAvailableAccounts error: %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("available len = %d, want 1", len(available))
	}
	if available[0].AccountName != "claude-a" {
		t.Fatalf("available[0].AccountName = %q, want claude-a", available[0].AccountName)
	}
	if available[0].RateLimited {
		t.Fatalf("available[0].RateLimited = true, want false")
	}
}
