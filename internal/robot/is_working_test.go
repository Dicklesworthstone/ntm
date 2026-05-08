package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestDefaultIsWorkingOptions(t *testing.T) {
	opts := DefaultIsWorkingOptions()

	if opts.LinesCaptured != 100 {
		t.Errorf("expected LinesCaptured=100, got %d", opts.LinesCaptured)
	}
	if opts.Verbose {
		t.Error("expected Verbose=false")
	}
	if opts.Session != "" {
		t.Errorf("expected empty Session, got %q", opts.Session)
	}
	if len(opts.Panes) != 0 {
		t.Errorf("expected empty Panes, got %v", opts.Panes)
	}
}

func TestParsePanesArg(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  []int
		expectErr bool
	}{
		{
			name:     "empty string returns empty slice",
			input:    "",
			expected: []int{},
		},
		{
			name:     "all keyword returns empty slice",
			input:    "all",
			expected: []int{},
		},
		{
			name:     "ALL uppercase returns empty slice",
			input:    "ALL",
			expected: []int{},
		},
		{
			name:     "single pane",
			input:    "2",
			expected: []int{2},
		},
		{
			name:     "multiple panes",
			input:    "1,2,3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "panes with spaces",
			input:    "1, 2, 3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "pane zero is valid",
			input:    "0",
			expected: []int{0},
		},
		{
			name:      "negative pane is invalid",
			input:     "-1",
			expectErr: true,
		},
		{
			name:      "non-numeric is invalid",
			input:     "abc",
			expectErr: true,
		},
		{
			name:      "mixed valid and invalid",
			input:     "1,abc,3",
			expectErr: true,
		},
		{
			name:     "trailing comma",
			input:    "1,2,",
			expected: []int{1, 2},
		},
		{
			name:     "leading comma",
			input:    ",1,2",
			expected: []int{1, 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParsePanesArg(tc.input)

			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tc.input)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error for input %q: %v", tc.input, err)
				return
			}

			if len(result) != len(tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, result)
				return
			}

			for i, v := range tc.expected {
				if result[i] != v {
					t.Errorf("at index %d: expected %d, got %d", i, v, result[i])
				}
			}
		})
	}
}

func TestGetRecommendationReason(t *testing.T) {
	tests := []struct {
		name     string
		state    *agent.AgentState
		contains string // substring that should be in the reason
	}{
		{
			name: "working agent",
			state: &agent.AgentState{
				IsWorking: true,
			},
			contains: "actively producing",
		},
		{
			name: "idle agent",
			state: &agent.AgentState{
				IsIdle: true,
			},
			contains: "idle",
		},
		{
			name: "rate limited agent",
			state: &agent.AgentState{
				IsRateLimited: true,
			},
			contains: "rate limit",
		},
		{
			name: "context low with percentage",
			state: &agent.AgentState{
				IsWorking:    true,
				IsContextLow: true,
				ContextRemaining: func() *float64 {
					v := 15.0
					return &v
				}(),
			},
			contains: "15%",
		},
		{
			name: "context low without percentage",
			state: &agent.AgentState{
				IsWorking:    true,
				IsContextLow: true,
			},
			contains: "low context",
		},
		{
			name: "error state",
			state: &agent.AgentState{
				IsInError: true,
			},
			contains: "error",
		},
		{
			name:     "unknown state",
			state:    &agent.AgentState{},
			contains: "Could not determine",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := getRecommendationReason(tc.state)
			if reason == "" {
				t.Error("expected non-empty reason")
			}
			if !containsSubstring(reason, tc.contains) {
				t.Errorf("reason %q does not contain %q", reason, tc.contains)
			}
		})
	}
}

func TestWorkIndicatorsInitialization(t *testing.T) {
	// Ensure WorkIndicators has proper defaults for JSON marshaling
	indicators := WorkIndicators{}

	// After initialization, Work and Limit should be nil
	// But we need to ensure they're set to empty slices in the code
	if indicators.Work != nil {
		t.Error("expected Work to be nil by default")
	}
	if indicators.Limit != nil {
		t.Error("expected Limit to be nil by default")
	}
}

func TestPaneWorkStatusDefaults(t *testing.T) {
	status := PaneWorkStatus{
		AgentType:      "cc",
		Recommendation: "DO_NOT_INTERRUPT",
		Indicators:     WorkIndicators{Work: []string{}, Limit: []string{}},
	}

	if status.AgentType != "cc" {
		t.Errorf("expected AgentType='cc', got %q", status.AgentType)
	}
	if status.IsWorking {
		t.Error("expected IsWorking=false by default")
	}
	if status.IsIdle {
		t.Error("expected IsIdle=false by default")
	}
	if len(status.Indicators.Work) != 0 {
		t.Errorf("expected empty Work indicators, got %v", status.Indicators.Work)
	}
	if len(status.Indicators.Limit) != 0 {
		t.Errorf("expected empty Limit indicators, got %v", status.Indicators.Limit)
	}
}

func TestIsWorkingSummaryInitialization(t *testing.T) {
	summary := IsWorkingSummary{
		ByRecommendation: make(map[string][]int),
	}

	if summary.TotalPanes != 0 {
		t.Errorf("expected TotalPanes=0, got %d", summary.TotalPanes)
	}
	if summary.WorkingCount != 0 {
		t.Errorf("expected WorkingCount=0, got %d", summary.WorkingCount)
	}
	if summary.ByRecommendation == nil {
		t.Error("ByRecommendation should not be nil")
	}
}

func TestIsWorkingQueryFields(t *testing.T) {
	query := IsWorkingQuery{
		PanesRequested: []int{1, 2, 3},
		LinesCaptured:  100,
	}

	if len(query.PanesRequested) != 3 {
		t.Errorf("expected 3 panes, got %d", len(query.PanesRequested))
	}
	if query.LinesCaptured != 100 {
		t.Errorf("expected LinesCaptured=100, got %d", query.LinesCaptured)
	}
}

func TestIsWorkingOutputStructure(t *testing.T) {
	output := IsWorkingOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Query: IsWorkingQuery{
			PanesRequested: []int{1, 2},
			LinesCaptured:  50,
		},
		Panes: make(map[string]PaneWorkStatus),
		Summary: IsWorkingSummary{
			TotalPanes:       2,
			WorkingCount:     1,
			IdleCount:        1,
			ByRecommendation: map[string][]int{"DO_NOT_INTERRUPT": {1}, "SAFE_TO_RESTART": {2}},
		},
	}

	if !output.Success {
		t.Error("expected Success=true")
	}
	if output.Session != "test-session" {
		t.Errorf("expected Session='test-session', got %q", output.Session)
	}
	if output.Query.LinesCaptured != 50 {
		t.Errorf("expected LinesCaptured=50, got %d", output.Query.LinesCaptured)
	}
	if output.Summary.TotalPanes != 2 {
		t.Errorf("expected TotalPanes=2, got %d", output.Summary.TotalPanes)
	}
}

// TestIsLiveBusyOverridesIdleVerdict_Codex pins the predicate that drives the
// #133 fix: when a Codex pane shows live "Working …" + "esc to interrupt"
// chrome, IsLiveBusy must return true so GetIsWorking forces IsWorking=true /
// IsIdle=false and re-derives the recommendation from the corrected state.
// Without this, the same scrollback that --robot-activity classifies as
// THINKING was being marked SAFE_TO_RESTART by --robot-is-working.
//
// The negative case pins that an idle codex prompt does not trip the override
// — otherwise every pane would be locked into the working bucket after any
// ambient match.
func TestIsLiveBusyOverridesIdleVerdict_Codex(t *testing.T) {
	scrollback := `> previous user prompt

• Working (4m 51s • esc to interrupt)
  Reading src/main.rs

`
	if !IsLiveBusy(scrollback, agent.AgentTypeCodex.String()) {
		t.Fatalf("IsLiveBusy(<codex working scrollback>, %q) = false, expected true; the live-window override would not fire and SAFE_TO_RESTART would leak through", agent.AgentTypeCodex.String())
	}

	idleScrollback := `> previous user prompt

  Done.

codex>
`
	if IsLiveBusy(idleScrollback, agent.AgentTypeCodex.String()) {
		t.Fatalf("IsLiveBusy(<idle codex prompt>, %q) = true, expected false; this would falsely keep idle panes out of the SAFE_TO_RESTART bucket", agent.AgentTypeCodex.String())
	}
}

// Helper function for substring matching
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && hasSubstr(s, substr)
}

func hasSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
