package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func TestBuildEnsembleAssignmentsCounts(t *testing.T) {
	state := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "m1", AgentType: "cc", Status: ensemble.AssignmentPending},
			{ModeID: "m2", AgentType: "cod", Status: ensemble.AssignmentInjecting},
			{ModeID: "m3", AgentType: "gmi", Status: ensemble.AssignmentActive},
			{ModeID: "m4", AgentType: "cc", Status: ensemble.AssignmentDone},
			{ModeID: "m5", AgentType: "cod", Status: ensemble.AssignmentError},
		},
	}

	rows, counts := buildEnsembleAssignments(state, nil, 1234)

	if len(rows) != 5 {
		t.Fatalf("expected 5 assignments, got %d", len(rows))
	}
	if counts.Pending != 2 {
		t.Errorf("pending count = %d, want 2", counts.Pending)
	}
	if counts.Working != 1 {
		t.Errorf("working count = %d, want 1", counts.Working)
	}
	if counts.Done != 1 {
		t.Errorf("done count = %d, want 1", counts.Done)
	}
	if counts.Error != 1 {
		t.Errorf("error count = %d, want 1", counts.Error)
	}
	if rows[0].TokenEstimate != 1234 {
		t.Errorf("token estimate = %d, want 1234", rows[0].TokenEstimate)
	}
}

func TestMergeBudgetDefaults(t *testing.T) {
	defaults := ensemble.BudgetConfig{
		MaxTokensPerMode: 4000,
		MaxTotalTokens:   50000,
		TimeoutPerMode:   5 * time.Minute,
		TotalTimeout:     30 * time.Minute,
		MaxRetries:       2,
	}

	current := ensemble.BudgetConfig{
		MaxTotalTokens: 12345,
	}

	merged := mergeBudgetDefaults(current, defaults)

	if merged.MaxTokensPerMode != defaults.MaxTokensPerMode {
		t.Errorf("MaxTokensPerMode = %d, want %d", merged.MaxTokensPerMode, defaults.MaxTokensPerMode)
	}
	if merged.MaxTotalTokens != current.MaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", merged.MaxTotalTokens, current.MaxTotalTokens)
	}
	if merged.TimeoutPerMode != defaults.TimeoutPerMode {
		t.Errorf("TimeoutPerMode = %s, want %s", merged.TimeoutPerMode, defaults.TimeoutPerMode)
	}
	if merged.TotalTimeout != defaults.TotalTimeout {
		t.Errorf("TotalTimeout = %s, want %s", merged.TotalTimeout, defaults.TotalTimeout)
	}
	if merged.MaxRetries != defaults.MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", merged.MaxRetries, defaults.MaxRetries)
	}
}

func TestRenderEnsembleStatusNoSession(t *testing.T) {
	var buf bytes.Buffer
	err := renderEnsembleStatus(&buf, ensembleStatusOutput{
		Session: "demo",
		Exists:  false,
	}, "table")
	if err != nil {
		t.Fatalf("renderEnsembleStatus error: %v", err)
	}
	if !strings.Contains(buf.String(), "No ensemble running") {
		t.Errorf("expected no-ensemble message, got %q", buf.String())
	}
}
