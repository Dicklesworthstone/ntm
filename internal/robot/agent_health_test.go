package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/caut"
)

func TestCalculateHealthScore(t *testing.T) {
	tests := []struct {
		name         string
		localState   *PaneWorkStatus
		providerUsage *caut.ProviderPayload
		wantMin      int
		wantMax      int
	}{
		{
			name: "perfect health - working agent, no issues",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.95,
			},
			providerUsage: nil,
			wantMin:       100,
			wantMax:       100,
		},
		{
			name: "rate limited - major deduction",
			localState: &PaneWorkStatus{
				AgentType:     "cc",
				IsRateLimited: true,
				Confidence:    0.9,
			},
			providerUsage: nil,
			wantMin:       50,
			wantMax:       50,
		},
		{
			name: "context low while idle",
			localState: &PaneWorkStatus{
				AgentType:    "cc",
				IsIdle:       true,
				IsContextLow: true,
				Confidence:   0.85,
			},
			providerUsage: nil,
			wantMin:       75,
			wantMax:       75,
		},
		{
			name: "context low while working",
			localState: &PaneWorkStatus{
				AgentType:    "cc",
				IsWorking:    true,
				IsContextLow: true,
				Confidence:   0.85,
			},
			providerUsage: nil,
			wantMin:       90,
			wantMax:       90,
		},
		{
			name: "unknown agent type",
			localState: &PaneWorkStatus{
				AgentType:  "unknown",
				IsIdle:     true,
				Confidence: 0.7,
			},
			providerUsage: nil,
			wantMin:       85,
			wantMax:       85,
		},
		{
			name: "low confidence",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsIdle:     true,
				Confidence: 0.3,
			},
			providerUsage: nil,
			wantMin:       90,
			wantMax:       90,
		},
		{
			name: "provider at moderate usage",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(65.0),
			wantMin:       95,
			wantMax:       95,
		},
		{
			name: "provider approaching limit",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(85.0),
			wantMin:       85,
			wantMax:       85,
		},
		{
			name: "provider near cap",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(96.0),
			wantMin:       70,
			wantMax:       70,
		},
		{
			name: "multiple issues - rate limited + provider near cap",
			localState: &PaneWorkStatus{
				AgentType:     "cc",
				IsRateLimited: true,
				Confidence:    0.9,
			},
			providerUsage: makeProviderUsage(98.0),
			wantMin:       20,
			wantMax:       20,
		},
		{
			name: "error state",
			localState: &PaneWorkStatus{
				AgentType:      "cc",
				Recommendation: "ERROR_STATE",
				Confidence:     0.8,
			},
			providerUsage: nil,
			wantMin:       60,
			wantMax:       60,
		},
		{
			name: "floor at zero",
			localState: &PaneWorkStatus{
				AgentType:      "unknown",
				IsRateLimited:  true,
				IsContextLow:   true,
				Recommendation: "ERROR_STATE",
				Confidence:     0.2,
			},
			providerUsage: makeProviderUsage(99.0),
			wantMin:       0,
			wantMax:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateHealthScore(tt.localState, tt.providerUsage)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("CalculateHealthScore() = %d, want [%d, %d]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestHealthGrade(t *testing.T) {
	tests := []struct {
		score int
		want  string
	}{
		{100, "A"},
		{95, "A"},
		{90, "A"},
		{89, "B"},
		{80, "B"},
		{79, "C"},
		{70, "C"},
		{69, "D"},
		{50, "D"},
		{49, "F"},
		{0, "F"},
	}

	for _, tt := range tests {
		got := HealthGrade(tt.score)
		if got != tt.want {
			t.Errorf("HealthGrade(%d) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

func TestCollectIssues(t *testing.T) {
	tests := []struct {
		name          string
		localState    *PaneWorkStatus
		providerUsage *caut.ProviderPayload
		wantContains  []string
		wantMinCount  int
	}{
		{
			name: "no issues",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: nil,
			wantContains:  []string{},
			wantMinCount:  0,
		},
		{
			name: "rate limited",
			localState: &PaneWorkStatus{
				AgentType:     "cc",
				IsRateLimited: true,
				Confidence:    0.9,
			},
			providerUsage: nil,
			wantContains:  []string{"Rate limited"},
			wantMinCount:  1,
		},
		{
			name: "context low",
			localState: &PaneWorkStatus{
				AgentType:        "cc",
				IsContextLow:     true,
				ContextRemaining: floatPtr(15.0),
				Confidence:       0.9,
			},
			providerUsage: nil,
			wantContains:  []string{"Context remaining"},
			wantMinCount:  1,
		},
		{
			name: "idle agent",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsIdle:     true,
				Confidence: 0.9,
			},
			providerUsage: nil,
			wantContains:  []string{"idle"},
			wantMinCount:  1,
		},
		{
			name: "unknown agent",
			localState: &PaneWorkStatus{
				AgentType:  "unknown",
				Confidence: 0.7,
			},
			providerUsage: nil,
			wantContains:  []string{"agent type"},
			wantMinCount:  1,
		},
		{
			name: "low confidence",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				Confidence: 0.3,
			},
			providerUsage: nil,
			wantContains:  []string{"Low confidence"},
			wantMinCount:  1,
		},
		{
			name: "provider near cap",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(97.0),
			wantContains:  []string{"97%", "near cap"},
			wantMinCount:  1,
		},
		{
			name: "multiple issues",
			localState: &PaneWorkStatus{
				AgentType:     "unknown",
				IsRateLimited: true,
				IsContextLow:  true,
				Confidence:    0.3,
			},
			providerUsage: makeProviderUsage(95.0),
			wantMinCount:  4, // rate limited, context low, unknown type, low confidence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectIssues(tt.localState, tt.providerUsage)

			if len(got) < tt.wantMinCount {
				t.Errorf("CollectIssues() returned %d issues, want at least %d", len(got), tt.wantMinCount)
			}

			for _, want := range tt.wantContains {
				found := false
				for _, issue := range got {
					if healthContainsSubstr(issue, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("CollectIssues() missing issue containing %q, got %v", want, got)
				}
			}
		})
	}
}

func TestDeriveHealthRecommendation(t *testing.T) {
	tests := []struct {
		name          string
		localState    *PaneWorkStatus
		providerUsage *caut.ProviderPayload
		healthScore   int
		wantRec       HealthRecommendation
	}{
		{
			name: "healthy working agent",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.95,
			},
			providerUsage: nil,
			healthScore:   100,
			wantRec:       RecommendHealthy,
		},
		{
			name: "rate limited",
			localState: &PaneWorkStatus{
				AgentType:     "cc",
				IsRateLimited: true,
				Confidence:    0.9,
			},
			providerUsage: nil,
			healthScore:   50,
			wantRec:       RecommendWaitForReset,
		},
		{
			name: "error state",
			localState: &PaneWorkStatus{
				AgentType:      "cc",
				Recommendation: "ERROR_STATE",
				Confidence:     0.8,
			},
			providerUsage: nil,
			healthScore:   60,
			wantRec:       RecommendRestartUrgent,
		},
		{
			name: "provider at 90%+",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(92.0),
			healthScore:   85,
			wantRec:       RecommendSwitchAccount,
		},
		{
			name: "low context and idle",
			localState: &PaneWorkStatus{
				AgentType:        "cc",
				IsIdle:           true,
				IsContextLow:     true,
				ContextRemaining: floatPtr(10.0),
				Confidence:       0.85,
			},
			providerUsage: nil,
			healthScore:   75,
			wantRec:       RecommendRestartRecommended,
		},
		{
			name: "moderate health score",
			localState: &PaneWorkStatus{
				AgentType:  "cc",
				IsWorking:  true,
				Confidence: 0.9,
			},
			providerUsage: makeProviderUsage(75.0),
			healthScore:   55,
			wantRec:       RecommendMonitor,
		},
		{
			name: "unknown stuck agent",
			localState: &PaneWorkStatus{
				AgentType:  "unknown",
				Confidence: 0.2,
			},
			providerUsage: nil,
			healthScore:   65,
			wantRec:       RecommendRestartUrgent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRec, _ := DeriveHealthRecommendation(tt.localState, tt.providerUsage, tt.healthScore)
			if gotRec != tt.wantRec {
				t.Errorf("DeriveHealthRecommendation() = %q, want %q", gotRec, tt.wantRec)
			}
		})
	}
}

func TestHealthRecommendationConstants(t *testing.T) {
	// Verify all recommendation constants are distinct
	recs := []HealthRecommendation{
		RecommendHealthy,
		RecommendMonitor,
		RecommendRestartRecommended,
		RecommendRestartUrgent,
		RecommendWaitForReset,
		RecommendSwitchAccount,
	}

	seen := make(map[HealthRecommendation]bool)
	for _, r := range recs {
		if seen[r] {
			t.Errorf("duplicate recommendation: %q", r)
		}
		seen[r] = true
	}
}

func TestDefaultAgentHealthOptions(t *testing.T) {
	opts := DefaultAgentHealthOptions()

	if opts.LinesCaptured != 100 {
		t.Errorf("LinesCaptured = %d, want 100", opts.LinesCaptured)
	}
	if !opts.IncludeCaut {
		t.Error("IncludeCaut should default to true")
	}
	if opts.CautTimeout == 0 {
		t.Error("CautTimeout should have a non-zero default")
	}
	if opts.Verbose {
		t.Error("Verbose should default to false")
	}
}

func TestConvertProviderUsage(t *testing.T) {
	// Test nil input
	if got := convertProviderUsage(nil); got != nil {
		t.Error("convertProviderUsage(nil) should return nil")
	}

	// Test basic conversion
	usedPct := 67.5
	windowMins := 480
	payload := &caut.ProviderPayload{
		Provider: "claude",
		Source:   "web",
		Usage: caut.UsageSnapshot{
			PrimaryRateWindow: &caut.RateWindow{
				UsedPercent:   &usedPct,
				WindowMinutes: &windowMins,
			},
		},
	}

	got := convertProviderUsage(payload)
	if got == nil {
		t.Fatal("convertProviderUsage returned nil for valid payload")
	}

	if got.Provider != "claude" {
		t.Errorf("Provider = %q, want 'claude'", got.Provider)
	}
	if got.Source != "web" {
		t.Errorf("Source = %q, want 'web'", got.Source)
	}
	if got.PrimaryWindow == nil {
		t.Fatal("PrimaryWindow should not be nil")
	}
	if got.PrimaryWindow.UsedPercent == nil || *got.PrimaryWindow.UsedPercent != 67.5 {
		t.Errorf("UsedPercent = %v, want 67.5", got.PrimaryWindow.UsedPercent)
	}
	if got.PrimaryWindow.WindowMinutes == nil || *got.PrimaryWindow.WindowMinutes != 480 {
		t.Errorf("WindowMinutes = %v, want 480", got.PrimaryWindow.WindowMinutes)
	}
}

func TestUpdateProviderSummary(t *testing.T) {
	summary := make(map[string]ProviderStats)

	// First update
	payload1 := makeProviderUsage(60.0)
	updateProviderSummary(summary, "claude", payload1, 2)

	if stats, ok := summary["claude"]; !ok {
		t.Error("claude should be in summary")
	} else {
		if stats.Accounts != 1 {
			t.Errorf("Accounts = %d, want 1", stats.Accounts)
		}
		if stats.AvgUsedPercent != 60.0 {
			t.Errorf("AvgUsedPercent = %f, want 60.0", stats.AvgUsedPercent)
		}
		if len(stats.PanesUsing) != 1 || stats.PanesUsing[0] != 2 {
			t.Errorf("PanesUsing = %v, want [2]", stats.PanesUsing)
		}
	}

	// Second update - same provider, different pane
	payload2 := makeProviderUsage(80.0)
	updateProviderSummary(summary, "claude", payload2, 3)

	stats := summary["claude"]
	if stats.Accounts != 2 {
		t.Errorf("Accounts = %d, want 2", stats.Accounts)
	}
	if stats.AvgUsedPercent != 70.0 { // (60 + 80) / 2
		t.Errorf("AvgUsedPercent = %f, want 70.0", stats.AvgUsedPercent)
	}
	if len(stats.PanesUsing) != 2 {
		t.Errorf("PanesUsing = %v, want [2, 3]", stats.PanesUsing)
	}

	// Duplicate pane should not add
	updateProviderSummary(summary, "claude", payload1, 2)
	if len(summary["claude"].PanesUsing) != 2 {
		t.Errorf("Duplicate pane should not be added, got %v", summary["claude"].PanesUsing)
	}
}

// Helper functions for tests

func makeProviderUsage(usedPercent float64) *caut.ProviderPayload {
	return &caut.ProviderPayload{
		Provider: "claude",
		Source:   "web",
		Usage: caut.UsageSnapshot{
			PrimaryRateWindow: &caut.RateWindow{
				UsedPercent: &usedPercent,
			},
		},
	}
}

func floatPtr(f float64) *float64 {
	return &f
}

func healthContainsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
