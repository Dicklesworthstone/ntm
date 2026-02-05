package robot

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseExcludePanes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			want:    nil,
			wantErr: false,
		},
		{
			name:    "single pane",
			input:   "1",
			want:    []int{1},
			wantErr: false,
		},
		{
			name:    "multiple panes",
			input:   "1,2,3",
			want:    []int{1, 2, 3},
			wantErr: false,
		},
		{
			name:    "with spaces",
			input:   "1, 2, 3",
			want:    []int{1, 2, 3},
			wantErr: false,
		},
		{
			name:    "invalid pane",
			input:   "1,abc,3",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "empty parts",
			input:   "1,,3",
			want:    []int{1, 3},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExcludePanes(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExcludePanes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("ParseExcludePanes() = %v, want %v", got, tt.want)
					return
				}
				for i, v := range got {
					if v != tt.want[i] {
						t.Errorf("ParseExcludePanes()[%d] = %d, want %d", i, v, tt.want[i])
					}
				}
			}
		})
	}
}

func TestRouteOptions(t *testing.T) {
	opts := RouteOptions{
		Session:      "test",
		Strategy:     StrategyLeastLoaded,
		AgentType:    "claude",
		ExcludePanes: []int{1, 2},
	}

	if opts.Session != "test" {
		t.Errorf("Session = %s, want 'test'", opts.Session)
	}
	if opts.Strategy != StrategyLeastLoaded {
		t.Errorf("Strategy = %s, want %s", opts.Strategy, StrategyLeastLoaded)
	}
	if opts.AgentType != "claude" {
		t.Errorf("AgentType = %s, want 'claude'", opts.AgentType)
	}
	if len(opts.ExcludePanes) != 2 {
		t.Errorf("ExcludePanes len = %d, want 2", len(opts.ExcludePanes))
	}
}

func TestRouteOutput(t *testing.T) {
	output := RouteOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		Strategy:      StrategyLeastLoaded,
		Candidates:    []RouteCandidate{},
		Excluded:      []RouteExcluded{},
	}

	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Session != "myproject" {
		t.Errorf("Session = %s, want 'myproject'", output.Session)
	}
	if output.Strategy != StrategyLeastLoaded {
		t.Errorf("Strategy = %s, want %s", output.Strategy, StrategyLeastLoaded)
	}
}

func TestRouteRecommendation(t *testing.T) {
	rec := RouteRecommendation{
		PaneID:       "cc_1",
		PaneIndex:    1,
		AgentType:    "cc",
		Score:        85.5,
		Reason:       "highest score",
		ContextUsage: 30.0,
		State:        "WAITING",
	}

	if rec.PaneID != "cc_1" {
		t.Errorf("PaneID = %s, want 'cc_1'", rec.PaneID)
	}
	if rec.PaneIndex != 1 {
		t.Errorf("PaneIndex = %d, want 1", rec.PaneIndex)
	}
	if rec.Score != 85.5 {
		t.Errorf("Score = %f, want 85.5", rec.Score)
	}
}

func TestRouteCandidate(t *testing.T) {
	candidate := RouteCandidate{
		PaneID:       "cc_2",
		PaneIndex:    2,
		AgentType:    "cc",
		Score:        70.0,
		ContextUsage: 50.0,
		State:        "WAITING",
		StateScore:   100.0,
		RecencyScore: 50.0,
	}

	if candidate.StateScore != 100.0 {
		t.Errorf("StateScore = %f, want 100.0", candidate.StateScore)
	}
	if candidate.RecencyScore != 50.0 {
		t.Errorf("RecencyScore = %f, want 50.0", candidate.RecencyScore)
	}
}

func TestRouteExcluded(t *testing.T) {
	excluded := RouteExcluded{
		PaneID:    "cc_3",
		PaneIndex: 3,
		AgentType: "cc",
		Reason:    "agent in ERROR state",
		State:     "ERROR",
	}

	if excluded.Reason != "agent in ERROR state" {
		t.Errorf("Reason = %s, want 'agent in ERROR state'", excluded.Reason)
	}
	if excluded.State != "ERROR" {
		t.Errorf("State = %s, want 'ERROR'", excluded.State)
	}
}

func TestRouteAgentHints(t *testing.T) {
	hints := RouteAgentHints{
		Summary:     "Route to cc (pane 1) with score 85.5 - WAITING",
		SendCommand: "ntm --robot-send=test --panes=1 --msg='YOUR_MESSAGE'",
		Suggestions: []string{"Primary strategy succeeded"},
	}

	if hints.Summary == "" {
		t.Error("Summary should not be empty")
	}
	if hints.SendCommand == "" {
		t.Error("SendCommand should not be empty")
	}
	if len(hints.Suggestions) != 1 {
		t.Errorf("Suggestions len = %d, want 1", len(hints.Suggestions))
	}
}

func TestGenerateRouteHints(t *testing.T) {
	t.Run("with recommendation", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			Recommendation: &RouteRecommendation{
				PaneID:    "cc_1",
				PaneIndex: 1,
				AgentType: "cc",
				Score:     85.5,
				State:     "WAITING",
			},
		}

		hints := generateRouteHints(opts, output)
		if hints.Summary == "" {
			t.Error("Summary should not be empty")
		}
		if hints.SendCommand == "" {
			t.Error("SendCommand should not be empty")
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			Candidates: []RouteCandidate{},
			Excluded: []RouteExcluded{
				{PaneID: "cc_1", Reason: "excluded"},
			},
		}

		hints := generateRouteHints(opts, output)
		if hints.Summary == "" {
			t.Error("Summary should not be empty")
		}
		if len(hints.Suggestions) == 0 {
			t.Error("Suggestions should not be empty for no agents")
		}
	})

	t.Run("fallback used", func(t *testing.T) {
		opts := RouteOptions{Session: "test"}
		output := RouteOutput{
			FallbackUsed: true,
			Candidates:   []RouteCandidate{{PaneID: "cc_1"}},
		}

		hints := generateRouteHints(opts, output)
		found := false
		for _, s := range hints.Suggestions {
			if s == "Primary strategy failed - fallback was used" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Should mention fallback was used")
		}
	})
}

func TestRouteStrategyNames(t *testing.T) {
	names := strategyNames()
	if len(names) != 7 {
		t.Errorf("strategyNames() returned %d names, want 7", len(names))
	}

	expected := map[string]bool{
		"least-loaded":          true,
		"first-available":       true,
		"round-robin":           true,
		"round-robin-available": true,
		"random":                true,
		"sticky":                true,
		"explicit":              true,
	}

	for _, name := range names {
		if !expected[name] {
			t.Errorf("Unexpected strategy name: %s", name)
		}
	}
}

func TestGetRoute_MissingSession(t *testing.T) {
	out, code := GetRoute(RouteOptions{Strategy: StrategyLeastLoaded})
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing session")
	}
	if out.Success {
		t.Error("expected Success=false for missing session")
	}
	if out.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeInvalidFlag)
	}
	if !strings.Contains(out.Error, "session name") {
		t.Errorf("Error = %q", out.Error)
	}
}

func TestGetRoute_InvalidStrategy(t *testing.T) {
	out, code := GetRoute(RouteOptions{
		Session:  "fake-session",
		Strategy: StrategyName("bogus"),
	})
	if code == 0 {
		t.Fatal("expected non-zero exit code for invalid strategy")
	}
	if out.Success {
		t.Error("expected Success=false for invalid strategy")
	}
	if out.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeInvalidFlag)
	}
	if !strings.Contains(out.Error, "invalid strategy") {
		t.Errorf("Error = %q", out.Error)
	}
}

func TestGetRoute_SessionNotFound(t *testing.T) {
	session := fmt.Sprintf("ntm-missing-%d", time.Now().UnixNano())
	out, code := GetRoute(RouteOptions{
		Session:  session,
		Strategy: StrategyLeastLoaded,
	})
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing session")
	}
	if out.Success {
		t.Error("expected Success=false for missing session")
	}
	if out.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, ErrCodeSessionNotFound)
	}
	if !strings.Contains(out.Error, session) {
		t.Errorf("Error should mention session name, got %q", out.Error)
	}
}

func TestGetRouteRecommendation_Errors(t *testing.T) {
	if _, err := GetRouteRecommendation(RouteOptions{}); err == nil {
		t.Fatal("expected error for missing session")
	}

	if _, err := GetRouteRecommendation(RouteOptions{
		Session:  "fake-session",
		Strategy: StrategyName("bogus"),
	}); err == nil {
		t.Fatal("expected error for invalid strategy")
	}

	session := fmt.Sprintf("ntm-missing-%d", time.Now().UnixNano())
	if _, err := GetRouteRecommendation(RouteOptions{
		Session:  session,
		Strategy: StrategyLeastLoaded,
	}); err == nil {
		t.Fatal("expected error for missing session")
	}
}
