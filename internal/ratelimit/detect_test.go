package ratelimit

import (
	"strings"
	"testing"
)

// Realistic banner fixtures captured from (or modeled directly on) actual
// agent CLI output for bd-wtm0w.
const (
	codexHitUsageLimitBanner = `■ You've hit your usage limit. Upgrade to Pro (https://openai.com/chatgpt/pricing)
or try again at 7:00 PM.

› Type a message
  47% context left · ? for shortcuts`

	codexHaveHitUsageLimitBanner = `■ You have hit your usage limit. To get more access now, send a new request
after your limit resets at 11:30 AM (America/New_York).`

	codexUsageLimitReachedBanner = `error: usage limit reached — resets at 3:00 AM (UTC). Upgrade to continue
using Codex, or wait for your limit to reset.`

	codexPlanLimitReachedBanner = `Plan limit reached. Upgrade to continue, or try again at 9:15 pm.`

	claudeHitLimitBanner = `Claude usage limit reached. Your limit will reset at 7pm (America/New_York).

  > continue where we left off`

	geminiQuotaBanner = `[API Error: 429 RESOURCE_EXHAUSTED: Quota exceeded for quota metric
'Generate Content API requests per minute' ... please retry later]`
)

func TestDetectRateLimit_UsageLimitBanners(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantLimited bool
	}{
		{"codex_hit_usage_limit", codexHitUsageLimitBanner, true},
		{"codex_have_hit_usage_limit", codexHaveHitUsageLimitBanner, true},
		{"codex_usage_limit_reached", codexUsageLimitReachedBanner, true},
		{"codex_plan_limit_reached", codexPlanLimitReachedBanner, true},
		{"claude_hit_limit", claudeHitLimitBanner, true},
		{"gemini_quota_exhausted", geminiQuotaBanner, true},
		{"benign_retry_limit", "increase the limit of retries to 5 before failing the request", false},
		{"benign_speed_limit", "the speed limit is 65 mph on this stretch", false},
		{"benign_memory_limit", "set the container memory limit to 512MB in the deployment spec", false},
		{"benign_upgrade", "run brew upgrade to continue with the install", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			det := DetectRateLimit(tc.output)
			if det.RateLimited != tc.wantLimited {
				t.Errorf("DetectRateLimit(%q).RateLimited = %v, want %v",
					tc.output, det.RateLimited, tc.wantLimited)
			}
			if tc.wantLimited && det.Source != "output" {
				t.Errorf("Source = %q, want %q", det.Source, "output")
			}
		})
	}
}

func TestDetectRateLimitForAgent_CodexUsageBanner(t *testing.T) {
	for _, banner := range []string{
		codexHitUsageLimitBanner,
		codexHaveHitUsageLimitBanner,
		codexUsageLimitReachedBanner,
		codexPlanLimitReachedBanner,
	} {
		det := DetectRateLimitForAgent(banner, "cod")
		if !det.RateLimited {
			t.Errorf("DetectRateLimitForAgent(cod) missed banner: %q", banner)
		}
	}

	// The banners must also be caught for panes whose agent type hint is
	// wrong or missing (field evidence: codex banners on mis-hinted panes).
	if det := DetectRateLimitForAgent(codexHitUsageLimitBanner, "claude"); !det.RateLimited {
		t.Error("codex usage-limit banner should be detected regardless of agent-type hint")
	}
}

func TestDetectCodexRateLimit_NewPhrasings(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{"You've hit your usage limit.", true},
		{"You have hit your usage limit.", true},
		{"usage limit reached", true},
		{"Plan limit reached", true},
		{"plan limits reached for this workspace", true},
		{"increase the limit of retries", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := DetectCodexRateLimit(tc.output); got != tc.want {
			t.Errorf("DetectCodexRateLimit(%q) = %v, want %v", tc.output, got, tc.want)
		}
	}
}

func TestExtractResetHint(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "codex_try_again_at",
			output: codexHitUsageLimitBanner,
			want:   "try again at 7:00 PM",
		},
		{
			name:   "codex_resets_at_tz",
			output: codexHaveHitUsageLimitBanner,
			want:   "resets at 11:30 AM (America/New_York)",
		},
		{
			name:   "claude_reset_at_7pm",
			output: claudeHitLimitBanner,
			want:   "reset at 7pm (America/New_York)",
		},
		{
			name:   "bare_resets_ampm",
			output: "You've hit your limit · resets 4pm",
			want:   "resets 4pm",
		},
		{
			name:   "available_again_at",
			output: "Service capacity reached; available again at 10:30 AM.",
			want:   "available again at 10:30 AM",
		},
		{
			name:   "no_hint",
			output: "increase the limit of retries to 5",
			want:   "",
		},
		{
			name:   "empty",
			output: "",
			want:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractResetHint(tc.output)
			if tc.want == "" {
				if got != "" {
					t.Errorf("ExtractResetHint(%q) = %q, want empty", tc.output, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("ExtractResetHint(%q) = %q, want it to contain %q", tc.output, got, tc.want)
			}
		})
	}
}

func TestDetectRateLimit_SurfacesResetHint(t *testing.T) {
	det := DetectRateLimit(codexHitUsageLimitBanner)
	if !det.RateLimited {
		t.Fatal("expected RateLimited for codex usage-limit banner")
	}
	if !strings.Contains(det.ResetHint, "try again at 7:00 PM") {
		t.Errorf("ResetHint = %q, want it to contain %q", det.ResetHint, "try again at 7:00 PM")
	}
}
