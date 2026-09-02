package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAgentType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "cc"},
		{AgentTypeCodex, "cod"},
		{AgentTypeGemini, "gmi"},
		{AgentTypeGrok, "grok"},
		{AgentTypeOllama, "ollama"},
		{AgentTypeCursor, "cursor"},
		{AgentTypeWindsurf, "windsurf"},
		{AgentTypeAider, "aider"},
		{AgentTypeUser, "user"},
		{AgentTypeUnknown, "unknown"},
	}

	for _, tt := range tests {
		if got := tt.at.String(); got != tt.want {
			t.Errorf("AgentType.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestAgentType_DisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "Claude Code"},
		{AgentTypeCodex, "Codex CLI"},
		{AgentTypeGemini, "Gemini CLI"},
		{AgentTypeGrok, "Grok Build"},
		{AgentTypeOllama, "Ollama"},
		{AgentTypeCursor, "Cursor"},
		{AgentTypeWindsurf, "Windsurf"},
		{AgentTypeAider, "Aider"},
		{AgentTypeUser, "User"},
		{AgentType("codex"), "Codex CLI"},
		{AgentType(" gemini "), "Gemini CLI"},
		{AgentTypeUnknown, "Unknown"},
		{AgentType("anything-else"), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.at.DisplayName(); got != tt.want {
			t.Errorf("AgentType(%q).DisplayName() = %q, want %q", tt.at, got, tt.want)
		}
	}
}

func TestAgentType_Canonical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want AgentType
	}{
		{AgentTypeClaudeCode, AgentTypeClaudeCode},
		{AgentType("claudecode"), AgentTypeClaudeCode},
		{AgentType("claude"), AgentTypeClaudeCode},
		{AgentType(" Claude-Code "), AgentTypeClaudeCode},
		{AgentType("claude_code"), AgentTypeClaudeCode},
		{AgentType("codex"), AgentTypeCodex},
		{AgentType("openaicodex"), AgentTypeCodex},
		{AgentType("codex-cli"), AgentTypeCodex},
		{AgentType("openai"), AgentTypeCodex},
		{AgentType("openai-codex"), AgentTypeCodex},
		{AgentType(" GMI "), AgentTypeGemini},
		{AgentType("google"), AgentTypeGemini},
		{AgentType("googlegemini"), AgentTypeGemini},
		{AgentType("gemini_cli"), AgentTypeGemini},
		{AgentType("google-gemini"), AgentTypeGemini},
		{AgentType(" Grok-Build "), AgentTypeGrok},
		{AgentType("xai_grok_build"), AgentTypeGrok},
		{AgentType("ws"), AgentTypeWindsurf},
		{AgentType("unknown"), AgentTypeUnknown},
		{AgentType(" custom "), AgentType("custom")},
		{AgentType(""), AgentType("")},
	}

	for _, tt := range tests {
		if got := tt.at.Canonical(); got != tt.want {
			t.Errorf("AgentType(%q).Canonical() = %q, want %q", tt.at, got, tt.want)
		}
	}
}

func TestAgentType_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want bool
	}{
		{AgentTypeClaudeCode, true},
		{AgentTypeCodex, true},
		{AgentTypeGemini, true},
		{AgentTypeGrok, true},
		{AgentTypeOllama, true},
		{AgentTypeCursor, true},
		{AgentTypeWindsurf, true},
		{AgentTypeAider, true},
		{AgentTypeUser, true},
		{AgentType("codex"), true},
		{AgentType(" gemini "), true},
		{AgentTypeUnknown, false},
		{AgentType("invalid"), false},
		{AgentType(""), false},
	}

	for _, tt := range tests {
		if got := tt.at.IsValid(); got != tt.want {
			t.Errorf("AgentType(%q).IsValid() = %v, want %v", tt.at, got, tt.want)
		}
	}
}

func TestAgentType_NeedsDoubleEnter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want bool
	}{
		{AgentTypeClaudeCode, false},
		{AgentType("claude"), false},
		{AgentTypeCodex, true},
		{AgentType("codex"), true},
		{AgentType(" CodEx "), true},
		{AgentTypeGemini, true},
		{AgentType("gemini"), true},
		{AgentTypeGrok, false},
		{AgentType("unknown"), false},
	}

	for _, tt := range tests {
		if got := tt.at.NeedsDoubleEnter(); got != tt.want {
			t.Errorf("AgentType(%q).NeedsDoubleEnter() = %v, want %v", tt.at, got, tt.want)
		}
	}
}

func TestAgentTypeGrokAliasesAreExact(t *testing.T) {
	for _, alias := range []string{"grok", "grok-build", "grok_build", "grokbuild", "xai-grok-build", "xai_grok_build", "xaigrokbuild"} {
		if got := AgentType(alias).Canonical(); got != AgentTypeGrok {
			t.Errorf("AgentType(%q).Canonical() = %q, want %q", alias, got, AgentTypeGrok)
		}
	}
	for _, unrelated := range []string{"xai", "ngrok", "grok-cli", "grok-exporter"} {
		if got := AgentType(unrelated).Canonical(); got == AgentTypeGrok {
			t.Errorf("unrelated AgentType(%q) canonicalized to Grok", unrelated)
		}
	}
	if got := AgentTypeGrok.ProfileName(); got != "Grok" {
		t.Errorf("AgentTypeGrok.ProfileName() = %q, want Grok", got)
	}
}

func TestAgentTypeValidateAutomatedRelaunch(t *testing.T) {
	// GH#251 phase 2: Grok Build relaunch is implemented with the named
	// least-privilege template and live-verified TUI markers, so the phase-one sentinel no
	// longer fires for any grok alias.
	for _, agentType := range []AgentType{AgentTypeGrok, "grok-build", "xai_grok_build", AgentTypeClaudeCode, AgentTypeCodex, AgentTypeGemini, AgentTypeUser, AgentTypeUnknown} {
		if err := agentType.ValidateAutomatedRelaunch(); err != nil {
			t.Errorf("AgentType(%q).ValidateAutomatedRelaunch() error = %v, want nil", agentType, err)
		}
	}
	if err := AgentTypeZAI.ValidateAutomatedRelaunch(); !errors.Is(err, ErrZAIProfileRelaunchRequired) {
		t.Fatalf("AgentTypeZAI.ValidateAutomatedRelaunch() error = %v, want ErrZAIProfileRelaunchRequired", err)
	}
}

func TestAgentTypeValidateAutomatedPromptDelivery(t *testing.T) {
	// GH#251 phase 2: automated Grok Build prompt delivery is implemented
	// (composer-gated, submission-verified), so every alias now passes.
	for _, agentType := range []AgentType{
		AgentTypeGrok,
		" Grok-Build ",
		"GROK_BUILD",
		"xai-grok-build",
		"XAI_GROK_BUILD",
		AgentTypeClaudeCode,
		AgentTypeCodex,
		AgentTypeGemini,
		AgentTypeAntigravity,
		AgentTypeUser,
		AgentTypeUnknown,
		"grok-cli",
	} {
		if err := agentType.ValidateAutomatedPromptDelivery(); err != nil {
			t.Errorf("AgentType(%q).ValidateAutomatedPromptDelivery() error = %v, want nil", agentType, err)
		}
	}
}

func TestAgentState_GetRecommendation(t *testing.T) {
	tests := []struct {
		name  string
		state AgentState
		want  Recommendation
	}{
		{
			name:  "rate limited takes highest priority",
			state: AgentState{IsRateLimited: true, IsWorking: true, IsInError: true},
			want:  RecommendRateLimitedWait,
		},
		{
			name:  "error state when not rate limited",
			state: AgentState{IsInError: true, IsWorking: true},
			want:  RecommendErrorState,
		},
		{
			name:  "working with low context",
			state: AgentState{IsWorking: true, IsContextLow: true},
			want:  RecommendContextLowContinue,
		},
		{
			name:  "working without low context",
			state: AgentState{IsWorking: true, IsContextLow: false},
			want:  RecommendDoNotInterrupt,
		},
		{
			name:  "idle is safe to restart",
			state: AgentState{IsIdle: true},
			want:  RecommendSafeToRestart,
		},
		{
			name:  "unknown when no flags set",
			state: AgentState{},
			want:  RecommendUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.GetRecommendation(); got != tt.want {
				t.Errorf("AgentState.GetRecommendation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecommendation_IsActionable(t *testing.T) {
	tests := []struct {
		r    Recommendation
		want bool
	}{
		{RecommendSafeToRestart, true},
		{RecommendErrorState, true},
		{RecommendDoNotInterrupt, false},
		{RecommendRateLimitedWait, false},
		{RecommendContextLowContinue, false},
		{RecommendUnknown, false},
	}

	for _, tt := range tests {
		if got := tt.r.IsActionable(); got != tt.want {
			t.Errorf("Recommendation(%q).IsActionable() = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestRecommendation_RequiresWaiting(t *testing.T) {
	tests := []struct {
		r    Recommendation
		want bool
	}{
		{RecommendRateLimitedWait, true},
		{RecommendContextLowContinue, true},
		{RecommendDoNotInterrupt, true},
		{RecommendSafeToRestart, false},
		{RecommendErrorState, false},
		{RecommendUnknown, false},
	}

	for _, tt := range tests {
		if got := tt.r.RequiresWaiting(); got != tt.want {
			t.Errorf("Recommendation(%q).RequiresWaiting() = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestAgentState_JSONSerialization(t *testing.T) {
	// Test that nil pointers serialize as absent (not null)
	state := AgentState{
		Type:       AgentTypeClaudeCode,
		ParsedAt:   time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC),
		IsWorking:  true,
		Confidence: 0.85,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Verify optional fields are omitted when nil
	jsonStr := string(data)
	if contains(jsonStr, "context_remaining") {
		t.Error("Expected context_remaining to be omitted when nil")
	}
	if contains(jsonStr, "tokens_used") {
		t.Error("Expected tokens_used to be omitted when nil")
	}

	// Verify required fields are present
	if !contains(jsonStr, `"agent_type":"cc"`) {
		t.Error("Expected agent_type to be present")
	}
	if !contains(jsonStr, `"is_working":true`) {
		t.Error("Expected is_working to be present")
	}

	// Test with values set
	contextPct := 45.5
	tokensUsed := int64(150000)
	stateWithValues := AgentState{
		Type:             AgentTypeCodex,
		ContextRemaining: &contextPct,
		TokensUsed:       &tokensUsed,
		IsIdle:           true,
		WorkIndicators:   []string{"code block", "writing"},
	}

	data, err = json.Marshal(stateWithValues)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	jsonStr = string(data)
	if !contains(jsonStr, `"context_remaining":45.5`) {
		t.Errorf("Expected context_remaining to be 45.5, got: %s", jsonStr)
	}
	if !contains(jsonStr, `"tokens_used":150000`) {
		t.Errorf("Expected tokens_used to be 150000, got: %s", jsonStr)
	}
}

func TestAgentState_JSONDeserialization(t *testing.T) {
	// Test round-trip
	contextPct := 30.0
	original := AgentState{
		Type:             AgentTypeGemini,
		ParsedAt:         time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC),
		ContextRemaining: &contextPct,
		IsWorking:        true,
		IsContextLow:     false,
		WorkIndicators:   []string{"thinking", "searching"},
		Confidence:       0.9,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded AgentState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, original.Type)
	}
	if decoded.ContextRemaining == nil || *decoded.ContextRemaining != *original.ContextRemaining {
		t.Errorf("ContextRemaining = %v, want %v", decoded.ContextRemaining, original.ContextRemaining)
	}
	if decoded.IsWorking != original.IsWorking {
		t.Errorf("IsWorking = %v, want %v", decoded.IsWorking, original.IsWorking)
	}
	if len(decoded.WorkIndicators) != len(original.WorkIndicators) {
		t.Errorf("WorkIndicators len = %d, want %d", len(decoded.WorkIndicators), len(original.WorkIndicators))
	}
}

func TestDefaultParserConfig(t *testing.T) {
	cfg := DefaultParserConfig()

	if cfg.ContextLowThreshold != 20.0 {
		t.Errorf("ContextLowThreshold = %f, want 20.0", cfg.ContextLowThreshold)
	}
	if cfg.SampleLength != 500 {
		t.Errorf("SampleLength = %d, want 500", cfg.SampleLength)
	}
}

func TestAgentType_ProfileName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "Claude"},
		{AgentTypeCodex, "Codex"},
		{AgentTypeGemini, "Gemini"},
		{AgentTypeCursor, "Cursor"},
		{AgentTypeWindsurf, "Windsurf"},
		{AgentTypeAider, "Aider"},
		{AgentTypeUser, "User"},
		{AgentTypeUnknown, "Unknown"},
		{AgentType("custom"), "Custom"}, // default: capitalize first letter
	}

	for _, tt := range tests {
		t.Run(string(tt.at), func(t *testing.T) {
			t.Parallel()
			if got := tt.at.ProfileName(); got != tt.want {
				t.Errorf("AgentType(%q).ProfileName() = %q, want %q", tt.at, got, tt.want)
			}
		})
	}
}

func TestAgentType_ProfileName_Empty(t *testing.T) {
	t.Parallel()
	// Empty AgentType should return empty string from default branch
	at := AgentType("")
	if got := at.ProfileName(); got != "" {
		t.Errorf("AgentType(\"\").ProfileName() = %q, want \"\"", got)
	}
}

func TestRecommendation_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		r    Recommendation
		want string
	}{
		{RecommendDoNotInterrupt, "DO_NOT_INTERRUPT"},
		{RecommendSafeToRestart, "SAFE_TO_RESTART"},
		{RecommendContextLowContinue, "CONTEXT_LOW_CONTINUE"},
		{RecommendRateLimitedWait, "RATE_LIMITED_WAIT"},
		{RecommendErrorState, "ERROR_STATE"},
		{RecommendUnknown, "UNKNOWN"},
	}

	for _, tt := range tests {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("Recommendation(%q).String() = %q, want %q", tt.r, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
