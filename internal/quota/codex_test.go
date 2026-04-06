package quota

import "testing"

// Fixture strings representing plausible Codex CLI output formats.
// These are heuristic guesses — update when real output is captured.
const (
	codexFixtureUsageFull = `Codex CLI v0.9.1
Usage: 42.7%
Limit: 85%
Rate limit warning: approaching threshold`

	codexFixtureUsageOnly = `Usage: 18.3%`

	codexFixtureLimitOnly = `Limit: 60%`

	codexFixtureRateLimited = `Error: rate limit exceeded
Please wait before retrying.`

	codexFixtureStatusFull = `Account: user-abc123
Organization: Acme Corp`
)

func TestParseCodexUsage_FixtureFull(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, codexFixtureUsageFull)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for full fixture; fail-closed would treat this as unknown")
	}
	if info.SessionUsage != 42.7 {
		t.Errorf("SessionUsage = %v, want 42.7", info.SessionUsage)
	}
	if info.WeeklyUsage != 85 {
		t.Errorf("WeeklyUsage = %v, want 85", info.WeeklyUsage)
	}
	if !info.IsLimited {
		t.Error("expected IsLimited=true (fixture contains 'rate limit')")
	}
}

func TestParseCodexUsage_FixtureUsageOnly(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, codexFixtureUsageOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for usage-only fixture")
	}
	if info.SessionUsage != 18.3 {
		t.Errorf("SessionUsage = %v, want 18.3", info.SessionUsage)
	}
	if info.WeeklyUsage != 0 {
		t.Errorf("WeeklyUsage = %v, want 0 (not present in fixture)", info.WeeklyUsage)
	}
}

func TestParseCodexUsage_FixtureLimitOnly(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, codexFixtureLimitOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for limit-only fixture")
	}
	if info.SessionUsage != 0 {
		t.Errorf("SessionUsage = %v, want 0 (not present in fixture)", info.SessionUsage)
	}
	if info.WeeklyUsage != 60 {
		t.Errorf("WeeklyUsage = %v, want 60", info.WeeklyUsage)
	}
}

func TestParseCodexUsage_FixtureRateLimited(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, codexFixtureRateLimited)
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

// --- Fail-closed behavior tests ---
// When the parser cannot match any patterns, found must be false so callers
// treat the result as "unknown" rather than "zero usage" (which would be
// dangerously optimistic).

func TestParseCodexUsage_FailClosed_GarbageInput(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, "absolutely no recognizable content here 🚫🤖")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for garbage input (fail-closed: unknown, not zero)")
	}
	// All numeric fields must remain at zero-value — nothing was parsed.
	if info.SessionUsage != 0 || info.WeeklyUsage != 0 {
		t.Errorf("numeric fields should be zero when nothing parsed: session=%v weekly=%v",
			info.SessionUsage, info.WeeklyUsage)
	}
	if info.IsLimited {
		t.Error("IsLimited should be false when nothing matched")
	}
}

func TestParseCodexUsage_FailClosed_EmptyString(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	found, err := parseCodexUsage(info, "")
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

func TestParseCodexUsage_FailClosed_NearMissPatterns(t *testing.T) {
	t.Parallel()

	// Strings that look similar to real patterns but should NOT match.
	nearMisses := []struct {
		name  string
		input string
	}{
		{"percentage without label", "42%"},
		{"usage without percentage", "Usage: high"},
		{"numeric without context", "12.5"},
		{"html-like", "<usage>50</usage>"},
	}

	for _, nm := range nearMisses {
		t.Run(nm.name, func(t *testing.T) {
			t.Parallel()
			info := &QuotaInfo{}
			found, err := parseCodexUsage(info, nm.input)
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

func TestParseCodexStatus_FixtureFull(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseCodexStatus(info, codexFixtureStatusFull)
	if info.AccountID != "user-abc123" {
		t.Errorf("AccountID = %q, want %q", info.AccountID, "user-abc123")
	}
	if info.Organization != "Acme Corp" {
		t.Errorf("Organization = %q, want %q", info.Organization, "Acme Corp")
	}
}

func TestParseCodexStatus_EmptyInput(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseCodexStatus(info, "")
	if info.AccountID != "" {
		t.Errorf("AccountID should be empty for empty input, got %q", info.AccountID)
	}
	if info.Organization != "" {
		t.Errorf("Organization should be empty for empty input, got %q", info.Organization)
	}
}
