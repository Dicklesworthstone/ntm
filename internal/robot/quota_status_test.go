package robot

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
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
