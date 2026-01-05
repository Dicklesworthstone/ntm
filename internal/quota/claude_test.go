package quota

import (
	"fmt"
	"testing"
	"time"
)

func TestParseClaudeUsage(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantSession  float64
		wantWeekly   float64
		wantPeriod   float64
		wantSonnet   float64
		wantResetStr string
		wantLimited  bool
	}{
		{
			name: "standard usage output",
			input: `Usage Information
Session: 45%
Weekly: 72%
Period: 30%
Sonnet: 38%
Resets: Monday 00:00 UTC`,
			wantSession:  45,
			wantWeekly:   72,
			wantPeriod:   30,
			wantSonnet:   38,
			wantResetStr: "Monday 00:00 UTC",
			wantLimited:  false,
		},
		{
			name: "verbose format",
			input: `Session usage: 55.5%
Weekly usage: 82.3%
5-hour period: 40%
Sonnet weekly: 25%`,
			wantSession: 55.5,
			wantWeekly:  82.3,
			wantPeriod:  40,
			wantSonnet:  25,
			wantLimited: false,
		},
		{
			name: "rate limited indicator",
			input: `Session: 100%
Weekly: 98%
Rate limit exceeded. Please wait.`,
			wantSession: 100,
			wantWeekly:  98,
			wantLimited: true,
		},
		{
			name: "limited keyword",
			input: `Session: 90%
You are currently limited`,
			wantSession: 90,
			wantLimited: true,
		},
		{
			name: "decimal percentages",
			input: `Session: 45.7%
Weekly: 72.333%`,
			wantSession: 45.7,
			wantWeekly:  72.333,
		},
		{
			name: "reset with in hours",
			input: `Session: 50%
Resets in 3 hours`,
			wantSession:  50,
			wantResetStr: "in 3 hours",
		},
		{
			name:        "empty input",
			input:       "",
			wantSession: 0,
			wantWeekly:  0,
			wantLimited: false,
		},
		{
			name:        "no matching patterns",
			input:       "Some random output with no quota info",
			wantSession: 0,
			wantWeekly:  0,
			wantLimited: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &QuotaInfo{}
			_, err := parseClaudeUsage(info, tt.input)
			if err != nil {
				t.Errorf("parseClaudeUsage() error = %v", err)
				return
			}

			if info.SessionUsage != tt.wantSession {
				t.Errorf("SessionUsage = %v, want %v", info.SessionUsage, tt.wantSession)
			}
			if info.WeeklyUsage != tt.wantWeekly {
				t.Errorf("WeeklyUsage = %v, want %v", info.WeeklyUsage, tt.wantWeekly)
			}
			if info.PeriodUsage != tt.wantPeriod {
				t.Errorf("PeriodUsage = %v, want %v", info.PeriodUsage, tt.wantPeriod)
			}
			if info.SonnetUsage != tt.wantSonnet {
				t.Errorf("SonnetUsage = %v, want %v", info.SonnetUsage, tt.wantSonnet)
			}
			if info.ResetString != tt.wantResetStr {
				t.Errorf("ResetString = %v, want %v", info.ResetString, tt.wantResetStr)
			}
			if info.IsLimited != tt.wantLimited {
				t.Errorf("IsLimited = %v, want %v", info.IsLimited, tt.wantLimited)
			}
		})
	}
}

func TestParseClaudeStatus(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAccount string
		wantOrg     string
		wantLogin   string
	}{
		{
			name: "full status output",
			input: `Status Information
Logged in as: user@example.com
Organization: Personal
Login method: Google OAuth`,
			wantAccount: "user@example.com",
			wantOrg:     "Personal",
			wantLogin:   "Google OAuth",
		},
		{
			name: "account format",
			input: `Account: test@anthropic.com
Org: Anthropic Inc`,
			wantAccount: "test@anthropic.com",
			wantOrg:     "Anthropic Inc",
		},
		{
			name: "email format",
			input: `Email: dev@company.io
Organization: Company Workspace`,
			wantAccount: "dev@company.io",
			wantOrg:     "Company Workspace",
		},
		{
			name: "authentication format",
			input: `Auth: API Key
Account: api-user@service.com`,
			wantAccount: "api-user@service.com",
			wantLogin:   "API Key",
		},
		{
			name:        "empty input",
			input:       "",
			wantAccount: "",
			wantOrg:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &QuotaInfo{}
			parseClaudeStatus(info, tt.input)

			if info.AccountID != tt.wantAccount {
				t.Errorf("AccountID = %v, want %v", info.AccountID, tt.wantAccount)
			}
			if info.Organization != tt.wantOrg {
				t.Errorf("Organization = %v, want %v", info.Organization, tt.wantOrg)
			}
			if info.LoginMethod != tt.wantLogin {
				t.Errorf("LoginMethod = %v, want %v", info.LoginMethod, tt.wantLogin)
			}
		})
	}
}

func TestParseResetTime(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantValid bool
	}{
		{
			name:      "in hours",
			input:     "in 3 hours",
			wantValid: true,
		},
		{
			name:      "in minutes",
			input:     "in 30 minutes",
			wantValid: true,
		},
		{
			name:      "monday",
			input:     "Monday 00:00 UTC",
			wantValid: true,
		},
		{
			name:      "tomorrow",
			input:     "tomorrow at midnight",
			wantValid: true,
		},
		{
			name:      "unparseable",
			input:     "some random string",
			wantValid: false,
		},
		{
			name:      "empty",
			input:     "",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseResetTime(tt.input)

			if tt.wantValid {
				if result.IsZero() {
					t.Errorf("Expected valid time for input %q, got zero time", tt.input)
				}
				// Note: Reset times are generally in the future,
				// but we don't assert on that for edge cases
			} else {
				if !result.IsZero() {
					t.Errorf("Expected zero time for input %q, got %v", tt.input, result)
				}
			}
		})
	}
}

func TestParseResetTimeInHours(t *testing.T) {
	now := time.Now().UTC()

	result := parseResetTime("in 5 hours")
	if result.IsZero() {
		t.Fatal("Expected valid time")
	}

	// Should be approximately 5 hours from now (within 1 minute tolerance)
	expected := now.Add(5 * time.Hour)
	diff := result.Sub(expected)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("Reset time %v should be ~5 hours from now, diff = %v", result, diff)
	}
}

func TestParseResetTimeInMinutes(t *testing.T) {
	now := time.Now().UTC()

	result := parseResetTime("in 45 mins")
	if result.IsZero() {
		t.Fatal("Expected valid time")
	}

	expected := now.Add(45 * time.Minute)
	diff := result.Sub(expected)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("Reset time should be ~45 mins from now, diff = %v", diff)
	}
}

func TestParseClaudeUsageString(t *testing.T) {
	input := `Session: 45%
Weekly: 72%
Sonnet: 38%
Resets: Monday 00:00 UTC
Logged in as: test@example.com
Organization: TestOrg`

	quota := ParseClaudeUsageString(input)

	if quota.SessionUsage != 45 {
		t.Errorf("SessionUsage = %v, want 45", quota.SessionUsage)
	}
	if quota.WeeklyUsage != 72 {
		t.Errorf("WeeklyUsage = %v, want 72", quota.WeeklyUsage)
	}
	if quota.SonnetUsage != 38 {
		t.Errorf("SonnetUsage = %v, want 38", quota.SonnetUsage)
	}
	if quota.ResetTime != "Monday 00:00 UTC" {
		t.Errorf("ResetTime = %v, want Monday 00:00 UTC", quota.ResetTime)
	}
	if quota.AccountEmail != "test@example.com" {
		t.Errorf("AccountEmail = %v, want test@example.com", quota.AccountEmail)
	}
	if quota.Organization != "TestOrg" {
		t.Errorf("Organization = %v, want TestOrg", quota.Organization)
	}
}

func TestParseLimitedIndicators(t *testing.T) {
	limitedInputs := []string{
		"Rate limit exceeded",
		"You are rate limited",
		"Please retry later",
		"Quota exceeded, please wait",
		"Session: 100%\nLimited until reset",
	}

	for i, input := range limitedInputs {
		name := input
		if len(name) > 30 {
			name = name[:30]
		}
		t.Run(fmt.Sprintf("%d_%s", i, name), func(t *testing.T) {
			info := &QuotaInfo{}
			_, _ = parseClaudeUsage(info, input)

			if !info.IsLimited {
				t.Errorf("Expected IsLimited=true for input containing limited indicator")
			}
		})
	}
}
