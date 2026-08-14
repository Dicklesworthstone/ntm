package robot

import "testing"

// Realistic provider usage-limit banner fixtures (bd-wtm0w). The Codex CLI
// prints these with no "rate limit" wording, which previously left
// detectRateLimit (and everything downstream: --robot-status enrichment,
// --robot-activity, wait conditions) blind to walled codex panes.
const (
	testCodexUsageBanner = `■ You've hit your usage limit. Upgrade to Pro (https://openai.com/chatgpt/pricing)
or try again at 7:00 PM.

› Type a message
  47% context left · ? for shortcuts`

	testCodexHaveHitBanner = `■ You have hit your usage limit. Send a new request after your limit resets at 11:30 AM.`

	testCodexUsageLimitReached = `error: usage limit reached — resets at 3:00 AM (UTC).`

	testCodexPlanLimitReached = `Plan limit reached. Upgrade to continue, or try again at 9:15 pm.`

	testClaudeUsageBanner = `Claude usage limit reached. Your limit will reset at 7pm (America/New_York).`

	testGeminiQuotaBanner = `[API Error: RESOURCE_EXHAUSTED: Quota exceeded for quota metric 'Generate Content API requests']`
)

// TestDetectRateLimit_UsageLimitBanners verifies that the status-enrichment
// detector recognizes provider usage-limit banners for every agent type, so
// RateLimitDetected / is_rate_limited surfaces flip for walled panes.
func TestDetectRateLimit_UsageLimitBanners(t *testing.T) {
	tests := []struct {
		name         string
		agentType    string
		content      string
		wantDetected bool
	}{
		{"codex_hit_usage_limit", "codex", testCodexUsageBanner, true},
		{"codex_have_hit_usage_limit", "codex", testCodexHaveHitBanner, true},
		{"codex_usage_limit_reached", "codex", testCodexUsageLimitReached, true},
		{"codex_plan_limit_reached", "codex", testCodexPlanLimitReached, true},
		// The banner must be caught even when the pane's agent-type hint is
		// wrong (field evidence: codex banners on mis-hinted panes).
		{"codex_banner_wrong_hint", "claude", testCodexUsageBanner, true},
		{"claude_usage_limit_reached", "claude", testClaudeUsageBanner, true},
		{"gemini_quota_exceeded", "gemini", testGeminiQuotaBanner, true},
		{"benign_retry_limit", "codex", "increase the limit of retries to 5 before failing", false},
		{"benign_memory_limit", "claude", "set the container memory limit to 512MB", false},
		{"benign_upgrade", "codex", "run brew upgrade to continue with the install", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detected, match := detectRateLimit(tc.content, tc.agentType)
			t.Logf("detected=%v match=%q", detected, match)
			if detected != tc.wantDetected {
				t.Errorf("detectRateLimit(%q, %q) = %v, want %v",
					tc.content, tc.agentType, detected, tc.wantDetected)
			}
			if tc.wantDetected && match == "" {
				t.Error("detected but match string is empty")
			}
		})
	}
}

// TestPatternLibrary_UsageLimitBanners verifies the shared robot pattern
// library (used by --robot-activity, probes, and wait conditions) classifies
// usage-limit banners as rate-limit errors.
func TestPatternLibrary_UsageLimitBanners(t *testing.T) {
	lib := NewPatternLibrary()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"codex_hit_usage_limit", testCodexUsageBanner, true},
		{"codex_have_hit_usage_limit", testCodexHaveHitBanner, true},
		{"codex_usage_limit_reached", testCodexUsageLimitReached, true},
		{"codex_plan_limit_reached", testCodexPlanLimitReached, true},
		{"claude_usage_limit_reached", testClaudeUsageBanner, true},
		{"benign_retry_limit", "increase the limit of retries to 5", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := lib.Match(tc.content, "codex")
			got := isRateLimitPatternMatch(matches)
			if got != tc.want {
				t.Errorf("isRateLimitPatternMatch(Match(%q)) = %v, want %v",
					tc.content, got, tc.want)
			}
		})
	}
}
