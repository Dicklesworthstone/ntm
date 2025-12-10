package tmux

import (
	"testing"
	"time"
)

func TestPaneActivityStruct(t *testing.T) {
	// Test that the PaneActivity struct works correctly
	pane := Pane{
		ID:      "%1",
		Index:   0,
		Title:   "test__cc",
		Type:    AgentClaude,
		Command: "bash",
		Width:   80,
		Height:  24,
		Active:  true,
	}

	activity := PaneActivity{
		Pane:         pane,
		LastActivity: time.Unix(1234567890, 0),
	}

	if activity.Pane.ID != "%1" {
		t.Errorf("Expected pane ID %%1, got %s", activity.Pane.ID)
	}

	if activity.LastActivity.Unix() != 1234567890 {
		t.Errorf("Expected timestamp 1234567890, got %d", activity.LastActivity.Unix())
	}

	if activity.Pane.Type != AgentClaude {
		t.Errorf("Expected AgentClaude, got %s", activity.Pane.Type)
	}
}

func TestAgentTypeConstants(t *testing.T) {
	// Verify agent type constants are correct
	tests := []struct {
		agentType AgentType
		expected  string
	}{
		{AgentClaude, "cc"},
		{AgentCodex, "cod"},
		{AgentGemini, "gmi"},
		{AgentUser, "user"},
	}

	for _, tt := range tests {
		if string(tt.agentType) != tt.expected {
			t.Errorf("AgentType constant mismatch: got %s, want %s", tt.agentType, tt.expected)
		}
	}
}

// Note: Functions like GetPaneActivity, GetPanesWithActivity, IsRecentlyActive,
// and GetPaneLastActivityAge require an actual tmux session to test properly.
// Integration tests would be needed for full coverage.
// The unit tests here verify the types and basic structure.

func TestPaneActivityTimestamp(t *testing.T) {
	// Test that we can work with the LastActivity time
	now := time.Now()
	activity := PaneActivity{
		Pane:         Pane{ID: "%1"},
		LastActivity: now,
	}

	// Activity from now should be recent (within a second)
	if time.Since(activity.LastActivity) > time.Second {
		t.Error("Expected activity to be recent")
	}
}

func TestPaneActivityAgeCalculation(t *testing.T) {
	// Test age calculation logic (simulating what GetPaneLastActivityAge does)
	pastTime := time.Now().Add(-5 * time.Minute)
	activity := PaneActivity{
		Pane:         Pane{ID: "%1"},
		LastActivity: pastTime,
	}

	age := time.Since(activity.LastActivity)

	// Should be approximately 5 minutes (allow some tolerance)
	if age < 4*time.Minute || age > 6*time.Minute {
		t.Errorf("Expected age around 5 minutes, got %v", age)
	}
}

func TestIsRecentlyActiveLogic(t *testing.T) {
	// Test the threshold logic (simulating what IsRecentlyActive does)
	threshold := 30 * time.Second

	tests := []struct {
		name     string
		offset   time.Duration
		expected bool
	}{
		{
			name:     "recent activity",
			offset:   -10 * time.Second,
			expected: true,
		},
		{
			name:     "old activity",
			offset:   -60 * time.Second,
			expected: false,
		},
		{
			name:     "well within threshold",
			offset:   -25 * time.Second,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture time at the start of the test to avoid timing issues
			now := time.Now()
			lastActivity := now.Add(tt.offset)
			age := now.Sub(lastActivity)
			isRecent := age <= threshold
			if isRecent != tt.expected {
				t.Errorf("Expected %v, got %v (age: %v, threshold: %v)", tt.expected, isRecent, age, threshold)
			}
		})
	}
}
