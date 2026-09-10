package robot

// Regression tests for GitHub issue #319, items 4 and 5.
//
// Item 4: `ntm --robot-account-status --provider=codex` reported
// available_accounts=0 and an empty current on a host with three healthy Codex
// seats. ntm stores Codex accounts under the provider id "openai"
// (caamProviderForNTM), canonicalRobotProvider had no case folding "codex"
// onto it, so the filter matched nothing and fell into the
// requested-but-not-found branch. The same surface also reported nothing about
// the seats' live windows.
//
// Item 5: `ntm --robot-quota-status` returned providers:{} even with
// caut_available:true and `caam limits` succeeding, because it read only
// caut's poller cache — which has no row at all for a subscription seat.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// TestCanonicalRobotProviderFoldsCodexOntoOpenAI pins the alias fix. "openai"
// stays the canonical key so existing consumers of the accounts map are
// unaffected.
func TestCanonicalRobotProviderFoldsCodexOntoOpenAI(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"codex", "openai"},
		{"openai", "openai"},
		{"cod", "openai"},
		{"chatgpt", "openai"},
		{"OpenAI-Codex", "openai"},
		{"claude", "claude"},
		{"cc", "claude"},
		{"anthropic", "claude"},
		{"gemini", "gemini"},
	}
	for _, tc := range cases {
		if got := canonicalRobotProvider(tc.input); got != tc.want {
			t.Errorf("canonicalRobotProvider(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func mustTime(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return &parsed
}

// stubLimits installs a fake ranked pool on both robot probes, so no caam
// binary and no credentials are needed.
func stubLimits(t *testing.T, fn func(ctx context.Context, provider string) (*tools.CAAMLimitsResult, error)) {
	t.Helper()
	oldAccount := accountStatusLimitsProbe
	oldQuota := quotaStatusLimitsProbe
	t.Cleanup(func() {
		accountStatusLimitsProbe = oldAccount
		quotaStatusLimitsProbe = oldQuota
	})
	accountStatusLimitsProbe = fn
	quotaStatusLimitsProbe = fn
}

func codexPool(t *testing.T) *tools.CAAMLimitsResult {
	t.Helper()
	spend := tools.CAAMRankedProfile{
		Provider: "codex", Profile: "spend-me", Rank: 1, Eligible: true,
		Tier: "included_headroom", Reason: "included allowance refreshes soonest",
		UsedPercent: 34, HeadroomPercent: 66, GoverningWindow: "weekly",
		ResetsAt: mustTime(t, "2026-09-12T00:00:00Z"),
	}
	reserve := tools.CAAMRankedProfile{
		Provider: "codex", Profile: "reserve", Rank: 2, Eligible: true,
		Tier: "included_headroom", Reason: "included allowance refreshes later; preserved",
		UsedPercent: 0, HeadroomPercent: 100, GoverningWindow: "weekly",
		ResetsAt: mustTime(t, "2026-09-16T00:00:00Z"),
	}
	selected := spend
	return &tools.CAAMLimitsResult{
		Rank:     tools.CAAMRankEarliestResetHeadroom,
		Provider: "codex",
		Selected: &selected,
		Profiles: []tools.CAAMRankedProfile{spend, reserve},
	}
}

// TestApplyLiveSeatQuotaSurfacesUsedPercentAndResetsAt covers the second half
// of item 4: the account surface reported names and nothing about windows.
func TestApplyLiveSeatQuotaSurfacesUsedPercentAndResetsAt(t *testing.T) {
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return codexPool(t), nil
	})

	var status ProviderStatus
	applyLiveSeatQuota(context.Background(), "openai", &status)

	if status.LimitsError != "" {
		t.Fatalf("limits_error = %q, want empty on a readable pool", status.LimitsError)
	}
	if status.RecommendedSeat != "codex/spend-me" {
		t.Errorf("recommended_seat = %q, want codex/spend-me", status.RecommendedSeat)
	}
	if status.RecommendedPersona != "codex-spend-me" {
		t.Errorf("recommended_persona = %q, want codex-spend-me — ntm composes the persona from caam's provider+profile", status.RecommendedPersona)
	}
	if status.UsagePercent != 34 {
		t.Errorf("usage_percent = %d, want 34 (the recommended seat's figure)", status.UsagePercent)
	}
	if status.ResetsAt == "" {
		t.Error("resets_at is empty; the live refresh time must reach the robot surface")
	}
	if status.GoverningWindow != "weekly" {
		t.Errorf("governing_window = %q, want weekly", status.GoverningWindow)
	}

	if len(status.Seats) != 2 {
		t.Fatalf("seats = %d, want both profiles with their own numbers", len(status.Seats))
	}
	if status.Seats[0].Profile != "spend-me" || status.Seats[0].UsedPercent != 34 || status.Seats[0].Rank != 1 {
		t.Errorf("seats[0] = %+v, want the rank-1 34%% seat", status.Seats[0])
	}
	if status.Seats[1].Profile != "reserve" || status.Seats[1].UsedPercent != 0 {
		t.Errorf("seats[1] = %+v, want the preserved 0%% reserve", status.Seats[1])
	}
	if status.Seats[0].Persona != "codex-spend-me" || status.Seats[1].Persona != "codex-reserve" {
		t.Errorf("seat personas = %q / %q, want codex-spend-me / codex-reserve", status.Seats[0].Persona, status.Seats[1].Persona)
	}
	if status.Seats[1].ResetsAt == "" {
		t.Error("the reserve seat's resets_at is empty; a controller needs every seat's window, not just the chosen one")
	}
}

// TestApplyLiveSeatQuotaFailsVisibly: an unreadable pool must be visibly
// distinct from a healthy one. Silence here is what let a static reserve-seat
// pin look like success.
func TestApplyLiveSeatQuotaFailsVisibly(t *testing.T) {
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return nil, errors.New("caam: no seat selectable: every included allowance is at its cap")
	})

	var status ProviderStatus
	applyLiveSeatQuota(context.Background(), "openai", &status)

	if status.LimitsError == "" {
		t.Fatal("limits_error is empty on an unreadable pool — the failure must be visible")
	}
	if status.RecommendedSeat != "" || status.RecommendedPersona != "" {
		t.Errorf("status = %+v, want no recommendation when limits could not be read", status)
	}
	if status.UsagePercent != 0 {
		t.Errorf("usage_percent = %d, want 0 rather than an invented figure", status.UsagePercent)
	}
}

// TestApplyLiveSeatQuotaKeepsProfilesOnARefusal: caam's non-zero exit still
// carries the per-seat reasons, and an operator needs to see them.
func TestApplyLiveSeatQuotaKeepsProfilesOnARefusal(t *testing.T) {
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return &tools.CAAMLimitsResult{
			Rank:     tools.CAAMRankEarliestResetHeadroom,
			Provider: "codex",
			Selected: nil,
			Profiles: []tools.CAAMRankedProfile{{
				Provider: "codex", Profile: "burned", Rank: 0, Eligible: false,
				Tier: "exhausted", Reason: "included allowance at cap", UsedPercent: 100,
			}},
			Error: "nothing selectable",
		}, errors.New("caam: no seat selectable: nothing selectable")
	})

	var status ProviderStatus
	applyLiveSeatQuota(context.Background(), "openai", &status)

	if status.LimitsError == "" {
		t.Fatal("a refusal must set limits_error")
	}
	if len(status.Seats) != 1 || status.Seats[0].Reason != "included allowance at cap" {
		t.Fatalf("seats = %+v, want the exhausted seat and its reason preserved", status.Seats)
	}
	if status.Seats[0].Eligible {
		t.Error("an exhausted seat must not be reported eligible")
	}
}

// TestApplyLiveProviderQuotaPopulatesSubscriptionWindows is item 5: quota
// status returned providers:{} because caut has no row for a subscription
// seat.
func TestApplyLiveProviderQuotaPopulatesSubscriptionWindows(t *testing.T) {
	stubLimits(t, func(_ context.Context, provider string) (*tools.CAAMLimitsResult, error) {
		if canonicalRobotProvider(provider) != "openai" {
			return nil, errors.New("no claude seats configured")
		}
		return codexPool(t), nil
	})

	quotaInfo := QuotaInfo{Providers: make(map[string]ProviderQuota)}
	applyLiveProviderQuota(context.Background(), &quotaInfo)

	quota, ok := quotaInfo.Providers["openai"]
	if !ok {
		t.Fatalf("providers = %+v, want a codex window even with an empty caut cache", quotaInfo.Providers)
	}
	if quota.UsagePercent != 34 {
		t.Errorf("usage_percent = %v, want 34 from the governing seat", quota.UsagePercent)
	}
	if quota.ResetAt == "" {
		t.Error("reset_at is empty; the live refresh time must reach the quota surface")
	}
	if quota.Status != "ok" {
		t.Errorf("status = %q, want ok at 34%% used", quota.Status)
	}
	if _, exists := quotaInfo.Providers["claude"]; exists {
		t.Error("a provider with no readable seats must not be invented into the map")
	}
}

// TestApplyLiveProviderQuotaLeavesCautNumbersAlone: caut owns the figure where
// it has one; caam only fills what caut left blank.
func TestApplyLiveProviderQuotaLeavesCautNumbersAlone(t *testing.T) {
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return codexPool(t), nil
	})

	quotaInfo := QuotaInfo{Providers: map[string]ProviderQuota{
		"openai": {UsagePercent: 71, ResetAt: "2026-09-11T00:00:00Z", Status: "ok"},
	}}
	applyLiveProviderQuota(context.Background(), &quotaInfo)

	if got := quotaInfo.Providers["openai"].UsagePercent; got != 71 {
		t.Errorf("usage_percent = %v, want caut's 71 preserved", got)
	}
	if got := quotaInfo.Providers["openai"].ResetAt; got != "2026-09-11T00:00:00Z" {
		t.Errorf("reset_at = %q, want caut's value preserved", got)
	}
}

// TestApplyLiveProviderQuotaFlagsAnExhaustedPool: every seat at its cap is the
// single most important thing this surface can report, so it must be visible
// even though nothing is eligible.
func TestApplyLiveProviderQuotaFlagsAnExhaustedPool(t *testing.T) {
	stubLimits(t, func(_ context.Context, provider string) (*tools.CAAMLimitsResult, error) {
		if canonicalRobotProvider(provider) != "openai" {
			return nil, errors.New("no claude seats configured")
		}
		return &tools.CAAMLimitsResult{
			Provider: "codex",
			Selected: nil,
			Profiles: []tools.CAAMRankedProfile{
				{Provider: "codex", Profile: "burned", Eligible: false, Tier: "exhausted", UsedPercent: 100},
				{Provider: "codex", Profile: "also-burned", Eligible: false, Tier: "exhausted", UsedPercent: 98},
			},
			Error: "nothing selectable",
		}, errors.New("caam: no seat selectable")
	})

	quotaInfo := QuotaInfo{Providers: make(map[string]ProviderQuota)}
	applyLiveProviderQuota(context.Background(), &quotaInfo)

	quota, ok := quotaInfo.Providers["openai"]
	if !ok {
		t.Fatal("an exhausted pool must still be reported, not omitted")
	}
	if quota.UsagePercent != 100 {
		t.Errorf("usage_percent = %v, want the worst seat's 100", quota.UsagePercent)
	}
	if !quotaInfo.HasCritical {
		t.Error("has_critical must be set when every seat is at its cap")
	}
}

// TestApplyLiveProviderQuotaIsSilentWithoutCAAM: most hosts have no caam seats
// at all, and an error row per provider would make a normal install look
// broken. --robot-account-status carries limits_error for operators who do use
// caam.
func TestApplyLiveProviderQuotaIsSilentWithoutCAAM(t *testing.T) {
	stubLimits(t, func(context.Context, string) (*tools.CAAMLimitsResult, error) {
		return nil, tools.ErrToolNotInstalled
	})

	quotaInfo := QuotaInfo{Providers: make(map[string]ProviderQuota)}
	applyLiveProviderQuota(context.Background(), &quotaInfo)

	if len(quotaInfo.Providers) != 0 {
		t.Errorf("providers = %+v, want empty when caam is not installed", quotaInfo.Providers)
	}
	if quotaInfo.HasCritical || quotaInfo.HasWarning {
		t.Error("a missing caam must not raise a quota alarm")
	}
}
