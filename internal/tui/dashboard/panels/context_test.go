package panels

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/context"
)

func TestNewContextPanel(t *testing.T) {
	panel := NewContextPanel()

	if panel == nil {
		t.Fatal("NewContextPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "context" {
		t.Errorf("expected ID 'context', got %q", cfg.ID)
	}
	if cfg.Priority != PriorityHigh {
		t.Errorf("expected PriorityHigh, got %v", cfg.Priority)
	}
	if cfg.RefreshInterval != 10*time.Second {
		t.Errorf("expected 10s refresh interval, got %v", cfg.RefreshInterval)
	}
}

func TestContextPanel_SetData(t *testing.T) {
	panel := NewContextPanel()

	data := ContextPanelData{
		Agents: []AgentContextStatus{
			{
				PaneID:              "test__cc_1",
				AgentType:           "cc",
				CurrentUsage:        0.72,
				CurrentTokens:       144000,
				ContextLimit:        200000,
				TokenVelocity:       1000,
				MinutesToExhaustion: 56,
				ShouldWarn:          true,
				ShouldCompact:       false,
			},
		},
		HighUsage:   0,
		Warning:     1,
		NeedCompact: 0,
	}

	panel.SetData(data, nil)

	if !panel.HasWarning() {
		t.Error("expected HasWarning() to return true")
	}
	if panel.HasCritical() {
		t.Error("expected HasCritical() to return false")
	}
	if panel.HasError() {
		t.Error("expected HasError() to return false")
	}
}

func TestContextPanel_SetDataFromPredictions(t *testing.T) {
	panel := NewContextPanel()

	predictions := map[string]*context.Prediction{
		"proj__cc_1": {
			CurrentUsage:        0.80,
			CurrentTokens:       160000,
			ContextLimit:        200000,
			TokenVelocity:       500,
			MinutesToExhaustion: 8,
			ShouldWarn:          true,
			ShouldCompact:       true,
			SampleCount:         10,
		},
		"proj__cod_1": {
			CurrentUsage:        0.30,
			CurrentTokens:       38400,
			ContextLimit:        128000,
			TokenVelocity:       200,
			MinutesToExhaustion: 0, // Stable
			ShouldWarn:          false,
			ShouldCompact:       false,
			SampleCount:         5,
		},
	}

	paneInfo := map[string]PaneContextInfo{
		"proj__cc_1":  {AgentType: "cc", AgentName: "BlueLake"},
		"proj__cod_1": {AgentType: "cod", AgentName: "GreenHill"},
	}

	panel.SetDataFromPredictions(predictions, paneInfo)

	if len(panel.data.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(panel.data.Agents))
	}
	if panel.data.Warning != 1 {
		t.Errorf("expected 1 warning, got %d", panel.data.Warning)
	}
	if panel.data.NeedCompact != 1 {
		t.Errorf("expected 1 needing compaction, got %d", panel.data.NeedCompact)
	}
	if panel.data.HighUsage != 1 {
		t.Errorf("expected 1 high usage, got %d", panel.data.HighUsage)
	}
	if !panel.HasCritical() {
		t.Error("expected HasCritical() to return true")
	}
}

func TestContextPanel_EmptyState(t *testing.T) {
	panel := NewContextPanel()
	panel.SetSize(60, 20)

	// Empty data
	panel.SetData(ContextPanelData{}, nil)

	view := panel.View()
	if view == "" {
		t.Error("View() should not return empty string")
	}
	// Should contain empty state text
	if !contains(view, "No context data") && !contains(view, "predictions") {
		t.Error("empty state should mention no context data or predictions")
	}
}

func TestContextPanel_View(t *testing.T) {
	panel := NewContextPanel()
	panel.SetSize(60, 20)

	data := ContextPanelData{
		Agents: []AgentContextStatus{
			{
				PaneID:              "test__cc_1",
				AgentType:           "cc",
				AgentName:           "TestAgent",
				CurrentUsage:        0.85,
				CurrentTokens:       170000,
				ContextLimit:        200000,
				TokenVelocity:       500,
				MinutesToExhaustion: 6,
				ShouldWarn:          true,
				ShouldCompact:       true,
			},
		},
		Warning:     1,
		NeedCompact: 1,
	}

	panel.SetData(data, nil)
	view := panel.View()

	// Should contain agent name or pane ID
	if !contains(view, "TestAgent") && !contains(view, "test__cc_1") {
		t.Error("view should contain agent name or pane ID")
	}
	// Should contain usage percentage
	if !contains(view, "85%") {
		t.Error("view should contain usage percentage")
	}
}

func TestContextPanel_Keybindings(t *testing.T) {
	panel := NewContextPanel()
	bindings := panel.Keybindings()

	if len(bindings) < 2 {
		t.Errorf("expected at least 2 keybindings, got %d", len(bindings))
	}

	// Check for refresh and compact actions
	hasRefresh := false
	hasCompact := false
	for _, b := range bindings {
		if b.Action == "refresh" {
			hasRefresh = true
		}
		if b.Action == "compact" {
			hasCompact = true
		}
	}

	if !hasRefresh {
		t.Error("expected refresh keybinding")
	}
	if !hasCompact {
		t.Error("expected compact keybinding")
	}
}

func TestContextAgentTypeColor_CanonicalizesAliases(t *testing.T) {
	t.Parallel()

	panel := NewContextPanel()
	aliasPairs := [][2]string{
		{"claude_code", "cc"},
		{"openai-codex", "cod"},
		{"google-gemini", "gmi"},
		{"ws", "windsurf"},
	}

	for _, pair := range aliasPairs {
		got := contextAgentTypeColor(pair[0], panel.theme)
		want := contextAgentTypeColor(pair[1], panel.theme)
		if got != want {
			t.Errorf("contextAgentTypeColor(%q) = %q, want same color as %q (%q)", pair[0], got, pair[1], want)
		}
	}
}

func TestContextAgentTypeColor_User(t *testing.T) {
	t.Parallel()

	panel := NewContextPanel()
	if got := contextAgentTypeColor("user", panel.theme); got != panel.theme.User {
		t.Fatalf("contextAgentTypeColor(user) = %q, want %q", got, panel.theme.User)
	}
}

func TestFormatMinutes(t *testing.T) {
	tests := []struct {
		minutes  float64
		expected string
	}{
		{0.5, "30 sec"},
		{1, "1 min"},
		{30, "30 min"},
		{60, "1.0 hr"},
		{90, "1.5 hr"},
		{1440, "1.0 days"},
		{2880, "2.0 days"},
	}

	for _, tt := range tests {
		result := formatMinutes(tt.minutes)
		if result != tt.expected {
			t.Errorf("formatMinutes(%v): expected %q, got %q", tt.minutes, tt.expected, result)
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsHelper(s, substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
