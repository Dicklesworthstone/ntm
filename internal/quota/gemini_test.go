package quota

import "testing"

// Fixture strings representing plausible Gemini CLI output formats.
// These are heuristic guesses — update when real output is captured.
const (
	geminiFixtureUsageFull = `Gemini API Status
Usage: 37.2%
Quota: 70%
Warning: quota exceeded threshold`

	geminiFixtureUsageOnly = `Usage: 5.5%`

	geminiFixtureQuotaOnly = `Quota: 90%`

	geminiFixtureRateLimited = `Error: rate limit exceeded
Retry after 300 seconds.`

	geminiFixtureQuotaExceeded = `Request failed: quota exceeded
Please upgrade your plan or wait for reset.`

	geminiFixtureStatusFull = `Account: dev@company.io
Project: my-gemini-project
Region: us-central1`
)

func TestParseGeminiUsage_FixtureFull(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, geminiFixtureUsageFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for full fixture; fail-closed would treat this as unknown")
	}
	if info.SessionUsage != 37.2 {
		t.Errorf("SessionUsage = %v, want 37.2", info.SessionUsage)
	}
	if info.WeeklyUsage != 70 {
		t.Errorf("WeeklyUsage = %v, want 70", info.WeeklyUsage)
	}
	if !info.IsLimited {
		t.Error("expected IsLimited=true (fixture contains 'quota exceeded')")
	}
}

func TestParseGeminiUsage_FixtureUsageOnly(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, geminiFixtureUsageOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for usage-only fixture")
	}
	if info.SessionUsage != 5.5 {
		t.Errorf("SessionUsage = %v, want 5.5", info.SessionUsage)
	}
	if info.WeeklyUsage != 0 {
		t.Errorf("WeeklyUsage = %v, want 0 (not present in fixture)", info.WeeklyUsage)
	}
}

func TestParseGeminiUsage_FixtureQuotaOnly(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, geminiFixtureQuotaOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for quota-only fixture")
	}
	if info.SessionUsage != 0 {
		t.Errorf("SessionUsage = %v, want 0 (not present in fixture)", info.SessionUsage)
	}
	if info.WeeklyUsage != 90 {
		t.Errorf("WeeklyUsage = %v, want 90", info.WeeklyUsage)
	}
}

func TestParseGeminiUsage_FixtureRateLimited(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, geminiFixtureRateLimited)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true when rate limited indicator present")
	}
	if !info.IsLimited {
		t.Error("expected IsLimited=true for rate-limited fixture")
	}
}

func TestParseGeminiUsage_FixtureQuotaExceeded(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, geminiFixtureQuotaExceeded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true when quota exceeded indicator present")
	}
	if !info.IsLimited {
		t.Error("expected IsLimited=true for quota-exceeded fixture")
	}
}

// --- Fail-closed behavior tests ---
// When the parser cannot match any patterns, found must be false so callers
// treat the result as "unknown" rather than "zero usage" (which would be
// dangerously optimistic).

func TestParseGeminiUsage_FailClosed_GarbageInput(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, "completely irrelevant text with no patterns whatsoever")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for garbage input (fail-closed: unknown, not zero)")
	}
	if info.SessionUsage != 0 || info.WeeklyUsage != 0 {
		t.Errorf("numeric fields should be zero when nothing parsed: session=%v weekly=%v",
			info.SessionUsage, info.WeeklyUsage)
	}
	if info.IsLimited {
		t.Error("IsLimited should be false when nothing matched")
	}
}

func TestParseGeminiUsage_FailClosed_EmptyString(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseGeminiUsage(info, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for empty string (fail-closed: unknown, not zero)")
	}
	if info.SessionUsage != 0 || info.WeeklyUsage != 0 {
		t.Errorf("numeric fields should be zero for empty input: session=%v weekly=%v",
			info.SessionUsage, info.WeeklyUsage)
	}
	if info.IsLimited {
		t.Error("IsLimited should be false for empty input")
	}
}

func TestParseGeminiUsage_FailClosed_NearMissPatterns(t *testing.T) {
	t.Parallel()

	nearMisses := []struct {
		name  string
		input string
	}{
		{"percentage without label", "37%"},
		{"usage without percentage", "Usage: moderate"},
		{"numeric without context", "70.5"},
		{"json-like", `{"usage": 50, "quota": 80}`},
	}

	for _, nm := range nearMisses {
		t.Run(nm.name, func(t *testing.T) {
			t.Parallel()
			info := &QuotaInfo{}
			found, err := parseGeminiUsage(info, nm.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found {
				t.Errorf("expected found=false for near-miss %q (fail-closed)", nm.input)
			}
		})
	}
}

// --- Status parsing tests ---

func TestParseGeminiStatus_FixtureFull(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseGeminiStatus(info, geminiFixtureStatusFull)
	if info.AccountID != "dev@company.io" {
		t.Errorf("AccountID = %q, want %q", info.AccountID, "dev@company.io")
	}
	if info.Organization != "my-gemini-project" {
		t.Errorf("Organization = %q, want %q", info.Organization, "my-gemini-project")
	}
}

func TestParseGeminiStatus_EmptyInput(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseGeminiStatus(info, "")
	if info.AccountID != "" {
		t.Errorf("AccountID should be empty for empty input, got %q", info.AccountID)
	}
	if info.Organization != "" {
		t.Errorf("Organization should be empty for empty input, got %q", info.Organization)
	}
}
