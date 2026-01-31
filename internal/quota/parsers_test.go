package quota

import "testing"

func TestParseCodexUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		wantFound   bool
		wantSession float64
		wantWeekly  float64
		wantLimited bool
	}{
		{
			name:        "usage percentage",
			output:      "Usage: 12.5%",
			wantFound:   true,
			wantSession: 12.5,
		},
		{
			name:       "limit percentage maps to weekly usage",
			output:     "Limit: 80%",
			wantFound:  true,
			wantWeekly: 80,
		},
		{
			name:        "limited keyword",
			output:      "Rate limit exceeded. Please wait.",
			wantFound:   true,
			wantLimited: true,
		},
		{
			name:      "no matching patterns",
			output:    "some random output",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := &QuotaInfo{}
			found, err := parseCodexUsage(info, tt.output)
			if err != nil {
				t.Fatalf("parseCodexUsage() error: %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if info.SessionUsage != tt.wantSession {
				t.Fatalf("SessionUsage = %v, want %v", info.SessionUsage, tt.wantSession)
			}
			if info.WeeklyUsage != tt.wantWeekly {
				t.Fatalf("WeeklyUsage = %v, want %v", info.WeeklyUsage, tt.wantWeekly)
			}
			if info.IsLimited != tt.wantLimited {
				t.Fatalf("IsLimited = %v, want %v", info.IsLimited, tt.wantLimited)
			}
		})
	}
}

func TestParseCodexStatus(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseCodexStatus(info, "Account: user123\nOrg: MyOrg\n")

	if info.AccountID != "user123" {
		t.Fatalf("AccountID = %q, want %q", info.AccountID, "user123")
	}
	if info.Organization != "MyOrg" {
		t.Fatalf("Organization = %q, want %q", info.Organization, "MyOrg")
	}
}

func TestParseGeminiUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		wantFound   bool
		wantSession float64
		wantWeekly  float64
		wantLimited bool
	}{
		{
			name:        "usage percentage",
			output:      "Usage: 10%",
			wantFound:   true,
			wantSession: 10,
		},
		{
			name:       "quota percentage maps to weekly usage",
			output:     "Quota: 50%",
			wantFound:  true,
			wantWeekly: 50,
		},
		{
			name:        "limited keyword",
			output:      "quota exceeded",
			wantFound:   true,
			wantLimited: true,
		},
		{
			name:      "no matching patterns",
			output:    "some random output",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := &QuotaInfo{}
			found, err := parseGeminiUsage(info, tt.output)
			if err != nil {
				t.Fatalf("parseGeminiUsage() error: %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if info.SessionUsage != tt.wantSession {
				t.Fatalf("SessionUsage = %v, want %v", info.SessionUsage, tt.wantSession)
			}
			if info.WeeklyUsage != tt.wantWeekly {
				t.Fatalf("WeeklyUsage = %v, want %v", info.WeeklyUsage, tt.wantWeekly)
			}
			if info.IsLimited != tt.wantLimited {
				t.Fatalf("IsLimited = %v, want %v", info.IsLimited, tt.wantLimited)
			}
		})
	}
}

func TestParseGeminiStatus(t *testing.T) {
	t.Parallel()

	info := &QuotaInfo{}
	parseGeminiStatus(info, "Account: user@example.com\nProject: MyProj\nRegion: us-east1\n")

	if info.AccountID != "user@example.com" {
		t.Fatalf("AccountID = %q, want %q", info.AccountID, "user@example.com")
	}
	if info.Organization != "MyProj" {
		t.Fatalf("Organization = %q, want %q", info.Organization, "MyProj")
	}
}

func TestParseUsageOutput_RoutesAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("routes to claude parser", func(t *testing.T) {
		info := &QuotaInfo{}
		found, err := parseUsageOutput(info, "Session: 45%\nWeekly: 10%", ProviderClaude)
		if err != nil {
			t.Fatalf("parseUsageOutput() error: %v", err)
		}
		if !found {
			t.Fatalf("expected found=true")
		}
		if info.SessionUsage != 45 {
			t.Fatalf("SessionUsage = %v, want 45", info.SessionUsage)
		}
	})

	t.Run("routes to codex parser", func(t *testing.T) {
		info := &QuotaInfo{}
		found, err := parseUsageOutput(info, "Usage: 12%", ProviderCodex)
		if err != nil {
			t.Fatalf("parseUsageOutput() error: %v", err)
		}
		if !found {
			t.Fatalf("expected found=true")
		}
		if info.SessionUsage != 12 {
			t.Fatalf("SessionUsage = %v, want 12", info.SessionUsage)
		}
	})

	t.Run("routes to gemini parser", func(t *testing.T) {
		info := &QuotaInfo{}
		found, err := parseUsageOutput(info, "Usage: 9%", ProviderGemini)
		if err != nil {
			t.Fatalf("parseUsageOutput() error: %v", err)
		}
		if !found {
			t.Fatalf("expected found=true")
		}
		if info.SessionUsage != 9 {
			t.Fatalf("SessionUsage = %v, want 9", info.SessionUsage)
		}
	})

	t.Run("unknown provider returns error", func(t *testing.T) {
		info := &QuotaInfo{}
		found, err := parseUsageOutput(info, "whatever", Provider("other"))
		if err == nil {
			t.Fatalf("expected error")
		}
		if found {
			t.Fatalf("expected found=false")
		}
	})
}

func TestParseStatusOutput_Routes(t *testing.T) {
	t.Parallel()

	t.Run("claude status", func(t *testing.T) {
		info := &QuotaInfo{}
		parseStatusOutput(info, "Logged in as: user@example.com\nOrganization: Personal\n", ProviderClaude)
		if info.AccountID != "user@example.com" {
			t.Fatalf("AccountID = %q, want %q", info.AccountID, "user@example.com")
		}
	})

	t.Run("codex status", func(t *testing.T) {
		info := &QuotaInfo{}
		parseStatusOutput(info, "Account: user123\nOrg: MyOrg\n", ProviderCodex)
		if info.AccountID != "user123" {
			t.Fatalf("AccountID = %q, want %q", info.AccountID, "user123")
		}
	})

	t.Run("gemini status", func(t *testing.T) {
		info := &QuotaInfo{}
		parseStatusOutput(info, "Account: user@example.com\nProject: MyProj\n", ProviderGemini)
		if info.AccountID != "user@example.com" {
			t.Fatalf("AccountID = %q, want %q", info.AccountID, "user@example.com")
		}
	})

	t.Run("unknown provider is no-op", func(t *testing.T) {
		info := &QuotaInfo{}
		parseStatusOutput(info, "Account: user123\n", Provider("other"))
		if info.AccountID != "" {
			t.Fatalf("AccountID = %q, want empty (no-op)", info.AccountID)
		}
	})
}
