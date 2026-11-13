package robot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestGetQuotaStatus(t *testing.T) {
	tests := []struct {
		name     string
		percent  float64
		expected string
	}{
		{"ok_low", 0.0, "ok"},
		{"ok_mid", 50.0, "ok"},
		{"ok_high", 79.9, "ok"},
		{"warning_threshold", 80.0, "warning"},
		{"warning_mid", 90.0, "warning"},
		{"warning_high", 94.9, "warning"},
		{"critical_threshold", 95.0, "critical"},
		{"critical_high", 100.0, "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getQuotaStatus(tt.percent)
			if got != tt.expected {
				t.Errorf("getQuotaStatus(%v) = %q, want %q", tt.percent, got, tt.expected)
			}
		})
	}
}

func TestQuotaStatusOutput_Struct(t *testing.T) {
	// Verify the struct embeds RobotResponse correctly
	output := QuotaStatusOutput{
		RobotResponse: NewRobotResponse(true),
		Quota: QuotaInfo{
			LastUpdated:   "2026-01-15T10:30:00Z",
			CautAvailable: true,
			Providers: map[string]ProviderQuota{
				"claude": {
					UsagePercent: 45.0,
					RequestsUsed: 450,
					TokensUsed:   45000,
					CostUSD:      12.50,
					Status:       "ok",
				},
			},
			TotalCostToday: 12.50,
			HasWarning:     false,
			HasCritical:    false,
		},
	}

	if !output.Success {
		t.Error("Expected Success to be true")
	}

	if output.Timestamp == "" {
		t.Error("Expected Timestamp to be set")
	}

	if !output.Quota.CautAvailable {
		t.Error("Expected CautAvailable to be true")
	}

	claude, ok := output.Quota.Providers["claude"]
	if !ok {
		t.Fatal("Expected claude provider in Providers map")
	}

	if claude.UsagePercent != 45.0 {
		t.Errorf("Expected UsagePercent 45.0, got %v", claude.UsagePercent)
	}

	if claude.Status != "ok" {
		t.Errorf("Expected Status 'ok', got %q", claude.Status)
	}
}

func TestQuotaCheckOutput_Struct(t *testing.T) {
	output := QuotaCheckOutput{
		RobotResponse: NewRobotResponse(true),
		Provider:      "openai",
		Quota: ProviderQuota{
			UsagePercent: 85.0,
			RequestsUsed: 850,
			TokensUsed:   170000,
			CostUSD:      25.00,
			Status:       "warning",
		},
	}

	if !output.Success {
		t.Error("Expected Success to be true")
	}

	if output.Provider != "openai" {
		t.Errorf("Expected Provider 'openai', got %q", output.Provider)
	}

	if output.Quota.Status != "warning" {
		t.Errorf("Expected Status 'warning', got %q", output.Quota.Status)
	}
}

func TestQuotaInfo_Warning(t *testing.T) {
	// Test that HasWarning is set correctly based on provider quotas
	qi := QuotaInfo{
		Providers: map[string]ProviderQuota{
			"claude": {UsagePercent: 45.0, Status: "ok"},
			"openai": {UsagePercent: 82.0, Status: "warning"},
		},
	}

	// Check warning detection (would be set by PrintQuotaStatus)
	hasWarning := false
	for _, p := range qi.Providers {
		if p.UsagePercent >= 80.0 && p.UsagePercent < 95.0 {
			hasWarning = true
		}
	}

	if !hasWarning {
		t.Error("Expected to detect warning when provider at 82%")
	}
}

func TestQuotaInfo_Critical(t *testing.T) {
	// Test that HasCritical is set correctly
	qi := QuotaInfo{
		Providers: map[string]ProviderQuota{
			"claude": {UsagePercent: 96.0, Status: "critical"},
		},
	}

	hasCritical := false
	for _, p := range qi.Providers {
		if p.UsagePercent >= 95.0 {
			hasCritical = true
		}
	}

	if !hasCritical {
		t.Error("Expected to detect critical when provider at 96%")
	}
}

func TestProviderQuota_Fields(t *testing.T) {
	pq := ProviderQuota{
		UsagePercent:  75.5,
		RequestsUsed:  1000,
		RequestsLimit: 2000,
		TokensUsed:    150000,
		TokensLimit:   200000,
		CostUSD:       50.00,
		ResetAt:       "2026-01-16T00:00:00Z",
		Status:        "ok",
	}

	if pq.UsagePercent != 75.5 {
		t.Errorf("Expected UsagePercent 75.5, got %v", pq.UsagePercent)
	}

	if pq.RequestsUsed != 1000 {
		t.Errorf("Expected RequestsUsed 1000, got %d", pq.RequestsUsed)
	}

	if pq.TokensLimit != 200000 {
		t.Errorf("Expected TokensLimit 200000, got %d", pq.TokensLimit)
	}

	if pq.ResetAt == "" {
		t.Error("Expected ResetAt to be set")
	}
}

func TestCanonicalRobotProvider(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "claude"},
		{"anthropic", "claude"},
		{"openai", "openai"},
		{"gemini", "gemini"},
		{"google", "gemini"},
		{"google-ai", "gemini"},
		{"GEMINI", "gemini"},
	}

	for _, tt := range tests {
		if got := canonicalRobotProvider(tt.input); got != tt.want {
			t.Fatalf("canonicalRobotProvider(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetQuotaCheck_NormalizesProviderAlias(t *testing.T) {
	poller := caut.GetGlobalPoller()
	cache := poller.GetCache()
	snapshot := cache.Snapshot()
	t.Cleanup(func() {
		cache.Clear()
		if snapshot.Status != nil {
			cache.UpdateStatus(snapshot.Status)
		}
		if len(snapshot.Usage) > 0 {
			cache.UpdateAllUsage(snapshot.Usage)
		}
		if snapshot.HasError {
			cache.SetError(errors.New(snapshot.ErrorMessage))
		}
	})

	cache.Clear()
	cache.UpdateStatus(&tools.CautStatus{
		Running:       true,
		Tracking:      true,
		ProviderCount: 1,
		Providers: []tools.CautProvider{
			{Name: "anthropic", Enabled: true, HasQuota: true, QuotaUsed: 62.5},
		},
	})
	cache.UpdateUsage("anthropic", &tools.CautUsage{
		Provider:     "anthropic",
		RequestCount: 12,
		TokensIn:     100,
		TokensOut:    50,
		Cost:         1.25,
		Period:       "day",
	})

	output, err := GetQuotaCheck("claude")
	if err != nil {
		t.Fatalf("GetQuotaCheck() error = %v", err)
	}
	if !output.Success {
		t.Fatalf("GetQuotaCheck() success = false, error=%q", output.Error)
	}
	if output.Provider != "claude" {
		t.Fatalf("Provider = %q, want claude", output.Provider)
	}
	if output.Quota.RequestsUsed != 12 {
		t.Fatalf("RequestsUsed = %d, want 12", output.Quota.RequestsUsed)
	}
	if output.Quota.UsagePercent != 62.5 {
		t.Fatalf("UsagePercent = %v, want 62.5", output.Quota.UsagePercent)
	}
}

func TestGetQuotaStatus_CanonicalizesProviderNames(t *testing.T) {
	poller := caut.GetGlobalPoller()
	cache := poller.GetCache()
	snapshot := cache.Snapshot()
	t.Cleanup(func() {
		cache.Clear()
		if snapshot.Status != nil {
			cache.UpdateStatus(snapshot.Status)
		}
		if len(snapshot.Usage) > 0 {
			cache.UpdateAllUsage(snapshot.Usage)
		}
		if snapshot.HasError {
			cache.SetError(errors.New(snapshot.ErrorMessage))
		}
	})

	cache.Clear()
	cache.UpdateStatus(&tools.CautStatus{
		Running:       true,
		Tracking:      true,
		ProviderCount: 2,
		Providers: []tools.CautProvider{
			{Name: "anthropic", Enabled: true, HasQuota: true, QuotaUsed: 45.0},
			{Name: "gemini", Enabled: true, HasQuota: true, QuotaUsed: 12.0},
		},
	})
	cache.UpdateAllUsage([]tools.CautUsage{
		{Provider: "anthropic", RequestCount: 3, Cost: 0.75},
		{Provider: "gemini", RequestCount: 1, Cost: 0.25},
	})

	output, err := GetQuotaStatus()
	if err != nil {
		t.Fatalf("GetQuotaStatus() error = %v", err)
	}
	if !output.Success {
		t.Fatalf("GetQuotaStatus() success = false, error=%q", output.Error)
	}
	if _, exists := output.Quota.Providers["anthropic"]; exists {
		t.Fatalf("providers should expose canonical claude key, got %+v", output.Quota.Providers)
	}
	if _, exists := output.Quota.Providers["claude"]; !exists {
		t.Fatalf("providers missing canonical claude key: %+v", output.Quota.Providers)
	}
	if _, exists := output.Quota.Providers["gemini"]; !exists {
		t.Fatalf("providers missing gemini key: %+v", output.Quota.Providers)
	}
}

// --robot-quota-status read caut's usage cache, which nothing had filled since
// its background poller was removed: with caut installed it reported
// caut_available and no caut usage at all. A stale cache is now refilled from
// caut itself, here a stand-in answering in caut's v1 contract.
func TestGetQuotaStatusReadsCautUsage(t *testing.T) {
	cache := caut.GetGlobalPoller().GetCache()
	cache.Clear()
	t.Cleanup(cache.Clear)

	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "args")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "caut 0.6.0"; exit 0; fi
echo "$*" >> "` + argsLog + `"
provider=
while [ $# -gt 0 ]; do
  case "$1" in --provider) provider=$2; shift 2 ;; *) shift ;; esac
done
case "$provider" in
  claude) pct=82.5 ;;
  codex) pct=30 ;;
  *) printf '{"schemaVersion":"caut.v1","generatedAt":"2026-10-06T00:00:00Z","command":"usage","data":[],"errors":["%s: not configured"],"meta":{"format":"json","flags":[],"runtime":"cli"}}\n' "$provider"; exit 0 ;;
esac
printf '{"schemaVersion":"caut.v1","generatedAt":"2026-10-06T00:00:00Z","command":"usage","data":[{"provider":"%s","source":"cli","usage":{"primary":{"usedPercent":%s,"windowMinutes":300},"updatedAt":"2026-10-06T00:00:00Z"}}],"errors":[],"meta":{"format":"json","flags":[],"runtime":"cli"}}\n' "$provider" "$pct"
`
	if err := os.WriteFile(filepath.Join(binDir, "caut"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := tools.NewCautAdapter()
	adapter.InvalidateAvailabilityCache()
	t.Cleanup(adapter.InvalidateAvailabilityCache)

	output, err := GetQuotaStatus()
	if err != nil || !output.Success {
		t.Fatalf("GetQuotaStatus() = %+v, %v", output, err)
	}
	quota := output.Quota
	if !quota.CautAvailable || quota.LastUpdated == "" {
		t.Fatalf("caut_available=%v last_updated=%q, want a caut read", quota.CautAvailable, quota.LastUpdated)
	}
	if claude := quota.Providers["claude"]; claude.UsagePercent != 82.5 || claude.Status != "warning" {
		t.Fatalf("claude quota = %+v, want caut's 82.5%% as a warning", claude)
	}
	if codex := quota.Providers["openai"]; codex.UsagePercent != 30 || codex.Status != "ok" {
		t.Fatalf("openai quota = %+v, want caut's codex 30%%", codex)
	}
	if !quota.HasWarning || quota.HasCritical {
		t.Fatalf("has_warning=%v has_critical=%v, want the 82.5%% warning only", quota.HasWarning, quota.HasCritical)
	}

	check, err := GetQuotaCheck("codex")
	if err != nil || !check.Success || check.Quota.UsagePercent != 30 {
		t.Fatalf("GetQuotaCheck(codex) = %+v, %v; want caut's 30%%", check, err)
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "usage --format json --provider claude") {
		t.Fatalf("caut was called as %q, want usage --format json --provider <name>", logged)
	}
}

// TestGetQuotaCheck_OmpReportsNoQuotaAPI pins that omp (and its aliases) gets
// an explicit NOT_IMPLEMENTED answer naming the provider-error fallback, not
// "provider not found".
func TestGetQuotaCheck_OmpReportsNoQuotaAPI(t *testing.T) {
	for _, provider := range []string{"omp", "oh-my-pi", " OMP "} {
		out, err := GetQuotaCheck(provider)
		if err != nil {
			t.Fatalf("GetQuotaCheck(%q): %v", provider, err)
		}
		if out.Success || out.ErrorCode != ErrCodeNotImplemented || ExitCodeForResponse(out.RobotResponse) != 2 {
			t.Fatalf("GetQuotaCheck(%q) = success:%v code:%s, want NOT_IMPLEMENTED (exit 2)", provider, out.Success, out.ErrorCode)
		}
		if out.Provider != "omp" || out.Quota.Status != "unsupported" ||
			!strings.Contains(out.Hint, "no quota API") || !strings.Contains(out.Hint, "provider-error classification") {
			t.Fatalf("GetQuotaCheck(%q) = provider:%q status:%q hint:%q", provider, out.Provider, out.Quota.Status, out.Hint)
		}
	}
}

func TestGetQuotaStatus_PayloadMetadataAndRateLimitOverlay(t *testing.T) {
	cache := caut.GetGlobalPoller().GetCache()
	cache.Clear()
	t.Cleanup(cache.Clear)

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "caut 0.6.0"; exit 0; fi
provider=
while [ $# -gt 0 ]; do
  case "$1" in --provider) provider=$2; shift 2 ;; *) shift ;; esac
done
case "$provider" in
  claude)
    echo '{"schemaVersion":"caut.v1","generatedAt":"2026-10-06T00:00:00Z","command":"usage","data":[{"provider":"claude","source":"cli","usage":{"primary":{"usedPercent":96.0,"windowMinutes":300,"resetDescription":"in 2h"},"identity":{"accountEmail":"dev@example.com"}}}],"errors":[]}'
    ;;
  *)
    printf '{"schemaVersion":"caut.v1","generatedAt":"2026-10-06T00:00:00Z","command":"usage","data":[],"errors":["%s: not configured"]}\n' "$provider"
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "caut"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub caut: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := tools.NewCautAdapter()
	adapter.InvalidateAvailabilityCache()
	t.Cleanup(adapter.InvalidateAvailabilityCache)

	projectDir := t.TempDir()
	tracker := ratelimit.NewRateLimitTracker(projectDir)
	tracker.RecordRateLimitWithCooldown("anthropic", "send", 120)
	if err := tracker.SaveToDir(projectDir); err != nil {
		t.Fatalf("SaveToDir: %v", err)
	}
	t.Chdir(projectDir)

	out, err := GetQuotaStatus()
	if err != nil || !out.Success {
		t.Fatalf("GetQuotaStatus: %+v, %v", out, err)
	}
	if out.Quota.CacheUpdates < 1 {
		t.Fatalf("expected CacheUpdates >= 1, got %d", out.Quota.CacheUpdates)
	}
	claude := out.Quota.Providers["claude"]
	if claude.AccountEmail != "dev@example.com" || claude.WindowMinutes != 300 || !claude.HasUsageData || !claude.RateLimited {
		t.Fatalf("claude provider metadata = %+v, want email=dev@example.com window=300 has_usage=true rate_limited=true", claude)
	}
	if claude.RateLimit == nil || !claude.RateLimit.InCooldown || claude.RateLimit.TotalRateLimits != 1 || len(claude.RateLimit.RecentEvents) != 1 {
		t.Fatalf("claude rate_limit overlay = %+v, want active cooldown with 1 event", claude.RateLimit)
	}
}
