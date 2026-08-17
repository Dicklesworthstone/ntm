package caut

import (
	"testing"
	"time"
)

// TestGetResetTime_NonNil verifies the non-nil PrimaryRateWindow branch.
func TestGetResetTime_NonNil(t *testing.T) {
	t.Parallel()

	now := time.Now()
	p := &ProviderPayload{
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{
				ResetsAt: &now,
			},
		},
	}

	got := p.GetResetTime()
	if got == nil {
		t.Fatal("expected non-nil reset time")
	}
	if !got.Equal(now) {
		t.Errorf("GetResetTime() = %v, want %v", got, now)
	}
}

// TestGetWindowMinutes_NonNil tests the non-nil path returning the value.
func TestGetWindowMinutes_NonNil(t *testing.T) {
	t.Parallel()

	mins := 480
	p := &ProviderPayload{
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{
				WindowMinutes: &mins,
			},
		},
	}

	got := p.GetWindowMinutes()
	if got == nil || *got != 480 {
		t.Errorf("GetWindowMinutes() = %v, want 480", got)
	}
}

// TestGetWindowMinutes_NilRateWindow verifies nil return when PrimaryRateWindow is nil.
func TestGetWindowMinutes_NilRateWindow(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{Usage: UsageSnapshot{}}
	if got := p.GetWindowMinutes(); got != nil {
		t.Errorf("GetWindowMinutes() = %v, want nil", got)
	}
}

// TestGetResetDescription_NilSubFields tests the intermediate nil branches.
func TestGetResetDescription_NilSubFields(t *testing.T) {
	t.Parallel()

	// PrimaryRateWindow non-nil but ResetDescription nil
	p := &ProviderPayload{
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{},
		},
	}
	if got := p.GetResetDescription(); got != "" {
		t.Errorf("GetResetDescription() = %q, want empty", got)
	}
}

// TestGetAccountEmail_IdentityNonNil_EmailNil tests Identity present but email nil.
func TestGetAccountEmail_IdentityNonNil_EmailNil(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{
		Usage: UsageSnapshot{
			Identity: &Identity{},
		},
	}
	if got := p.GetAccountEmail(); got != "" {
		t.Errorf("GetAccountEmail() = %q, want empty", got)
	}
}

// TestGetAccountEmail_NilIdentity verifies both nil paths.
func TestGetAccountEmail_NilIdentity(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{Usage: UsageSnapshot{}}
	if got := p.GetAccountEmail(); got != "" {
		t.Errorf("GetAccountEmail() = %q, want empty", got)
	}
}

// TestGetPlanName_IdentityNonNil_PlanNil tests Identity present but plan nil.
func TestGetPlanName_IdentityNonNil_PlanNil(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{
		Usage: UsageSnapshot{
			Identity: &Identity{},
		},
	}
	if got := p.GetPlanName(); got != "" {
		t.Errorf("GetPlanName() = %q, want empty", got)
	}
}

// TestGetPlanName_NilIdentity verifies nil Identity path.
func TestGetPlanName_NilIdentity(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{Usage: UsageSnapshot{}}
	if got := p.GetPlanName(); got != "" {
		t.Errorf("GetPlanName() = %q, want empty", got)
	}
}

// TestIsOperational_NonNilStatus tests the non-nil Status branch.
func TestIsOperational_NonNilStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		operational bool
		want        bool
	}{
		{"operational true", true, true},
		{"operational false", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &ProviderPayload{
				Status: &StatusInfo{Operational: tt.operational},
			}
			if got := p.IsOperational(); got != tt.want {
				t.Errorf("IsOperational() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAgentTypeToProvider_AllCases covers all switch cases including cursor/windsurf/aider.
func TestAgentTypeToProvider_AllCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		want      string
	}{
		{"cc", "claude"},
		{"claude", "claude"},
		{"claude-code", "claude"},
		{"claude_code", "claude"},
		{"cod", "codex"},
		{"codex", "codex"},
		{"codex-cli", "codex"},
		{"openai", "codex"},
		{"openai-codex", "codex"},
		{"gmi", "gemini"},
		{"gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"google", "gemini"},
		{"google-gemini", "gemini"},
		{"cursor", "cursor"},
		{"ws", "windsurf"},
		{"windsurf", "windsurf"},
		{"aider", "aider"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			if got := AgentTypeToProvider(tt.agentType); got != tt.want {
				t.Errorf("AgentTypeToProvider(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestIsRateLimited_NilUsedPercent(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{},
		},
	}
	if p.IsRateLimited(50) {
		t.Error("IsRateLimited should return false when UsedPercent is nil")
	}
}

// TestUsedPercent_NilUsedPercent tests the non-nil RateWindow but nil UsedPercent path.
func TestUsedPercent_NilUsedPercent(t *testing.T) {
	t.Parallel()

	p := &ProviderPayload{
		Usage: UsageSnapshot{
			PrimaryRateWindow: &RateWindow{},
		},
	}
	if got := p.UsedPercent(); got != nil {
		t.Errorf("UsedPercent() = %v, want nil", got)
	}
}

// TestHasUsageData_SecondaryAndTertiary tests non-primary rate window branches.
func TestHasUsageData_SecondaryAndTertiary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    ProviderPayload
		want bool
	}{
		{
			name: "secondary only",
			p: ProviderPayload{
				Usage: UsageSnapshot{
					SecondaryRateWindow: &RateWindow{},
				},
			},
			want: true,
		},
		{
			name: "tertiary only",
			p: ProviderPayload{
				Usage: UsageSnapshot{
					TertiaryRateWindow: &RateWindow{},
				},
			},
			want: true,
		},
		{
			name: "all nil",
			p:    ProviderPayload{Usage: UsageSnapshot{}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.p.HasUsageData(); got != tt.want {
				t.Errorf("HasUsageData() = %v, want %v", got, tt.want)
			}
		})
	}
}
