package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestQuotaCLI_RateLimitsAndCooldownManagement(t *testing.T) {
	projectDir := t.TempDir()
	tracker := ratelimit.NewRateLimitTracker(projectDir)
	tracker.RecordRateLimitWithCooldown("anthropic", "spawn", 180)
	tracker.RecordRateLimitWithCooldown("openai", "send", 90)
	if err := tracker.SaveToDir(projectDir); err != nil {
		t.Fatalf("SaveToDir: %v", err)
	}
	t.Chdir(projectDir)

	inspectOut, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "quota", "--rate-limits", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("quota --rate-limits --json: %v", err)
	}
	var inspectPayload struct {
		Success   bool                         `json:"success"`
		Action    string                       `json:"action"`
		Providers []quotaRateLimitProviderView `json:"providers"`
	}
	if err := json.Unmarshal([]byte(inspectOut), &inspectPayload); err != nil {
		t.Fatalf("unmarshal inspect JSON: %v\nout=%s", err, inspectOut)
	}
	if !inspectPayload.Success || len(inspectPayload.Providers) != 2 || !inspectPayload.Providers[0].InCooldown {
		t.Fatalf("unexpected rate-limits inspect payload: %+v", inspectPayload)
	}

	clearOut, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "quota", "--clear-cooldown", "anthropic", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("quota --clear-cooldown anthropic --json: %v", err)
	}
	if err := json.Unmarshal([]byte(clearOut), &inspectPayload); err != nil {
		t.Fatalf("unmarshal clear-cooldown JSON: %v", err)
	}
	if inspectPayload.Providers[0].Provider != "anthropic" || inspectPayload.Providers[0].InCooldown {
		t.Fatalf("expected anthropic cooldown cleared, got %+v", inspectPayload.Providers)
	}

	resetOneOut, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "quota", "--reset-rate-limit", "openai", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("quota --reset-rate-limit openai --json: %v", err)
	}
	if err := json.Unmarshal([]byte(resetOneOut), &inspectPayload); err != nil {
		t.Fatalf("unmarshal reset-rate-limit JSON: %v", err)
	}
	if len(inspectPayload.Providers) != 1 || inspectPayload.Providers[0].Provider != "anthropic" {
		t.Fatalf("expected only anthropic remaining after resetting openai, got %+v", inspectPayload.Providers)
	}

	resetAllOut, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "quota", "--reset-all-rate-limits", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("quota --reset-all-rate-limits --json: %v", err)
	}
	if err := json.Unmarshal([]byte(resetAllOut), &inspectPayload); err != nil {
		t.Fatalf("unmarshal reset-all JSON: %v", err)
	}
	if len(inspectPayload.Providers) != 0 {
		t.Fatalf("expected zero providers after reset-all, got %+v", inspectPayload.Providers)
	}

	cache := caut.GetGlobalPoller().GetCache()
	cache.UpdateStatus(&tools.CautStatus{Running: true, ProviderCount: 1})
	t.Cleanup(cache.Clear)
	if _, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "quota", "--clear-cache", "--json")
		return runErr
	}); err != nil {
		t.Fatalf("quota --clear-cache --json: %v", err)
	}
	if cache.GetStatus() != nil || cache.GetUpdateCount() != 0 {
		t.Fatalf("expected caut cache cleared, got status=%+v updates=%d", cache.GetStatus(), cache.GetUpdateCount())
	}
}

func TestFormatQuotaTable_HealthAndStaleness(t *testing.T) {
	now := time.Now()
	results := []*quota.QuotaInfo{
		{
			PaneIndex:    1,
			Provider:     quota.ProviderClaude,
			SessionUsage: 24.5,
			WeeklyUsage:  40.0,
			AccountID:    "alice@example.com",
			FetchedAt:    now,
		},
		{
			PaneIndex:    2,
			Provider:     quota.ProviderCodex,
			SessionUsage: 94.0,
			IsLimited:    true,
			FetchedAt:    now.Add(-10 * time.Minute),
		},
		{
			PaneIndex: 3,
			Provider:  quota.ProviderGemini,
			Error:     "probe timeout",
			FetchedAt: now,
		},
	}

	var buf bytes.Buffer
	formatQuotaTable(&buf, results)
	got := buf.String()

	if !strings.Contains(got, "healthy") ||
		!strings.Contains(got, "unhealthy (stale)") ||
		!strings.Contains(got, "unknown") ||
		!strings.Contains(got, "Healthy panes: 1/3 (1 stale)") {
		t.Fatalf("unexpected formatted quota table:\n%s", got)
	}
}
