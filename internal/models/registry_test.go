package models

import "testing"

func TestGetContextLimit_ExactMatch(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4", 200000},
		{"claude-sonnet-4-5", 200000},
		{"gpt-4", 128000},
		{"gpt-5", 256000},
		{"gpt-5-codex", 256000},
		{"gemini-2.0-flash", 1000000},
		{"gemini-pro", 32000},
		{"o3-mini", 200000},
		{"o4-mini", 128000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_Alias(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"opus", 200000},
		{"sonnet", 200000},
		{"haiku", 200000},
		{"gpt4", 128000},
		{"codex", 256000},
		{"gemini", 1000000},
		{"pro", 1000000},
		{"flash", 1000000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_DateSuffix(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-20260101", 200000},
		{"gpt-4o-20250901", 128000},
		{"claude-sonnet-4-5-20260315", 200000},
	}
	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestGetContextLimit_CaseInsensitive(t *testing.T) {
	got := GetContextLimit("Claude-Opus-4")
	if got != 200000 {
		t.Errorf("GetContextLimit(\"Claude-Opus-4\") = %d, want 200000", got)
	}
}

func TestGetContextLimit_PrefixMatch(t *testing.T) {
	// "gpt-5.3-codex" should prefix-match "gpt-5"
	got := GetContextLimit("gpt-5.3-codex")
	if got != 256000 {
		t.Errorf("GetContextLimit(\"gpt-5.3-codex\") = %d, want 256000", got)
	}
}

func TestGetContextLimit_Unknown(t *testing.T) {
	got := GetContextLimit("unknown-model-xyz")
	if got != DefaultContextLimit {
		t.Errorf("GetContextLimit(\"unknown-model-xyz\") = %d, want %d", got, DefaultContextLimit)
	}
}

func TestGetContextLimit_Empty(t *testing.T) {
	got := GetContextLimit("")
	if got != DefaultContextLimit {
		t.Errorf("GetContextLimit(\"\") = %d, want %d", got, DefaultContextLimit)
	}
}

func TestGetTokenBudget(t *testing.T) {
	tests := []struct {
		agentType string
		wantMin   int
		wantMax   int
	}{
		{"cc", 170000, 190000},   // 90% of 200K = 180K
		{"cod", 220000, 260000},  // 94% of 256K ≈ 240K
		{"gmi", 90000, 110000},   // 10% of 1M = 100K
		{"unknown", 90000, 110000}, // Default
	}
	for _, tt := range tests {
		got := GetTokenBudget(tt.agentType)
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("GetTokenBudget(%q) = %d, want [%d, %d]", tt.agentType, got, tt.wantMin, tt.wantMax)
		}
	}
}
