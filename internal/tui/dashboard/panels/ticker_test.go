package panels

import (
	"strings"
	"testing"
)

func TestNewTickerPanel(t *testing.T) {
	panel := NewTickerPanel()
	if panel == nil {
		t.Fatal("NewTickerPanel returned nil")
	}
}

func TestTickerPanelSetSize(t *testing.T) {
	panel := NewTickerPanel()
	panel.SetSize(100, 1)

	if panel.width != 100 {
		t.Errorf("expected width 100, got %d", panel.width)
	}
	if panel.height != 1 {
		t.Errorf("expected height 1, got %d", panel.height)
	}
}

func TestTickerPanelFocusBlur(t *testing.T) {
	panel := NewTickerPanel()

	panel.Focus()
	if !panel.focused {
		t.Error("expected focused to be true after Focus()")
	}

	panel.Blur()
	if panel.focused {
		t.Error("expected focused to be false after Blur()")
	}
}

func TestTickerPanelSetData(t *testing.T) {
	panel := NewTickerPanel()

	data := TickerData{
		TotalAgents:     5,
		ActiveAgents:    3,
		ClaudeCount:     2,
		CodexCount:      2,
		GeminiCount:     1,
		OllamaCount:     1,
		CriticalAlerts:  1,
		WarningAlerts:   2,
		InfoAlerts:      0,
		ReadyBeads:      10,
		InProgressBeads: 5,
		BlockedBeads:    2,
		UnreadMessages:  3,
		ActiveLocks:     1,
		MailConnected:   true,
	}

	panel.SetData(data)

	if panel.data.TotalAgents != 5 {
		t.Errorf("expected TotalAgents 5, got %d", panel.data.TotalAgents)
	}
	if panel.data.ClaudeCount != 2 {
		t.Errorf("expected ClaudeCount 2, got %d", panel.data.ClaudeCount)
	}
	if panel.data.OllamaCount != 1 {
		t.Errorf("expected OllamaCount 1, got %d", panel.data.OllamaCount)
	}
	if !panel.data.MailConnected {
		t.Error("expected MailConnected to be true")
	}
}

func TestTickerPanelSetAnimTick(t *testing.T) {
	panel := NewTickerPanel()

	panel.SetAnimTick(10)

	if panel.animTick != 10 {
		t.Errorf("expected animTick 10, got %d", panel.animTick)
	}
	// Offset should be tick / 2
	if panel.offset != 5 {
		t.Errorf("expected offset 5, got %d", panel.offset)
	}
}

func TestTickerPanelViewEmptyWidth(t *testing.T) {
	panel := NewTickerPanel()
	panel.SetSize(0, 1)

	view := panel.View()
	if view != "" {
		t.Errorf("expected empty view for zero width, got: %s", view)
	}
}

func TestTickerPanelViewContainsSegments(t *testing.T) {
	panel := NewTickerPanel()
	panel.SetSize(200, 1) // Wide enough to see all content

	data := TickerData{
		TotalAgents:     3,
		ActiveAgents:    2,
		ClaudeCount:     1,
		CodexCount:      1,
		GeminiCount:     1,
		CriticalAlerts:  0,
		WarningAlerts:   0,
		InfoAlerts:      0,
		ReadyBeads:      5,
		InProgressBeads: 2,
		BlockedBeads:    0,
		UnreadMessages:  0,
		ActiveLocks:     0,
		MailConnected:   true,
	}

	panel.SetData(data)
	view := panel.View()

	// The view should contain segment labels
	if !strings.Contains(view, "Fleet") {
		t.Error("expected view to contain 'Fleet'")
	}
	if !strings.Contains(view, "Alerts") {
		t.Error("expected view to contain 'Alerts'")
	}
	if !strings.Contains(view, "Beads") {
		t.Error("expected view to contain 'Beads'")
	}
	if !strings.Contains(view, "Mail") {
		t.Error("expected view to contain 'Mail'")
	}
	if !strings.Contains(view, "Ckpt") {
		t.Error("expected view to contain 'Ckpt'")
	}
}

func TestTickerPanelBuildFleetSegment(t *testing.T) {
	panel := NewTickerPanel()

	tests := []struct {
		name     string
		data     TickerData
		contains []string
	}{
		{
			name: "with user pane",
			data: TickerData{
				TotalAgents:  3,
				ActiveAgents: 1,
				ClaudeCount:  2,
				UserCount:    1,
			},
			contains: []string{"Fleet", "1 active / 3 total", "C:2", "U:1"},
		},
		{
			name: "with all agent types",
			data: TickerData{
				TotalAgents:   10,
				ActiveAgents:  8,
				ClaudeCount:   2,
				CodexCount:    2,
				GeminiCount:   2,
				CursorCount:   1,
				WindsurfCount: 1,
				AiderCount:    1,
				OllamaCount:   1,
			},
			contains: []string{"Fleet", "8 active / 10 total", "C:2", "X:2", "G:2", "Cur:1", "W:1", "A:1", "Oll:1"},
		},
		{
			name: "with only Claude",
			data: TickerData{
				TotalAgents:  2,
				ActiveAgents: 1,
				ClaudeCount:  2,
			},
			contains: []string{"Fleet", "1 active / 2 total", "C:2"},
		},
		{
			name: "empty fleet",
			data: TickerData{
				TotalAgents:  0,
				ActiveAgents: 0,
			},
			contains: []string{"Fleet", "0 active / 0 total"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.data)
			segment := panel.buildPlainFleetSegment()

			for _, expected := range tc.contains {
				if !strings.Contains(segment, expected) {
					t.Errorf("expected segment to contain %q, got: %s", expected, segment)
				}
			}
		})
	}
}

func TestTickerPanelBuildAlertsSegment(t *testing.T) {
	panel := NewTickerPanel()

	tests := []struct {
		name     string
		data     TickerData
		contains []string
	}{
		{
			name: "no alerts",
			data: TickerData{
				CriticalAlerts: 0,
				WarningAlerts:  0,
				InfoAlerts:     0,
			},
			contains: []string{"Alerts", "OK"},
		},
		{
			name: "critical alerts",
			data: TickerData{
				CriticalAlerts: 2,
				WarningAlerts:  1,
				InfoAlerts:     0,
			},
			contains: []string{"Alerts", "2!", "1w"},
		},
		{
			name: "only info alerts",
			data: TickerData{
				CriticalAlerts: 0,
				WarningAlerts:  0,
				InfoAlerts:     3,
			},
			contains: []string{"Alerts", "3i"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.data)
			segment := panel.buildPlainAlertsSegment()

			for _, expected := range tc.contains {
				if !strings.Contains(segment, expected) {
					t.Errorf("expected segment to contain %q, got: %s", expected, segment)
				}
			}
		})
	}
}

func TestTickerPanelBuildBeadsSegment(t *testing.T) {
	panel := NewTickerPanel()

	tests := []struct {
		name     string
		data     TickerData
		contains []string
	}{
		{
			name: "mixed beads",
			data: TickerData{
				ReadyBeads:      5,
				InProgressBeads: 3,
				BlockedBeads:    2,
			},
			contains: []string{"Beads", "R:5", "I:3", "B:2"},
		},
		{
			name: "only ready beads",
			data: TickerData{
				ReadyBeads:      10,
				InProgressBeads: 0,
				BlockedBeads:    0,
			},
			contains: []string{"Beads", "R:10"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.data)
			segment := panel.buildPlainBeadsSegment()

			for _, expected := range tc.contains {
				if !strings.Contains(segment, expected) {
					t.Errorf("expected segment to contain %q, got: %s", expected, segment)
				}
			}
		})
	}
}

func TestTickerPanelBuildMailSegment(t *testing.T) {
	panel := NewTickerPanel()

	tests := []struct {
		name     string
		data     TickerData
		contains []string
	}{
		{
			name: "offline",
			data: TickerData{
				MailConnected:    false,
				MailArchiveFound: false,
			},
			contains: []string{"Mail", "offline"},
		},
		{
			name: "detected via archive (MCP-only mode)",
			data: TickerData{
				MailConnected:    false,
				MailArchiveFound: true,
			},
			contains: []string{"Mail", "detected"},
		},
		{
			name: "connected with unread",
			data: TickerData{
				MailConnected:  true,
				UnreadMessages: 5,
				ActiveLocks:    2,
			},
			contains: []string{"Mail", "5 unread", "2 locks"},
		},
		{
			name: "connected no unread",
			data: TickerData{
				MailConnected:  true,
				UnreadMessages: 0,
				ActiveLocks:    0,
			},
			contains: []string{"Mail", "0 unread"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.data)
			segment := panel.buildPlainMailSegment()

			for _, expected := range tc.contains {
				if !strings.Contains(segment, expected) {
					t.Errorf("expected segment to contain %q, got: %s", expected, segment)
				}
			}
		})
	}
}

func TestTickerPanelBuildCheckpointSegment(t *testing.T) {
	panel := NewTickerPanel()

	tests := []struct {
		name     string
		data     TickerData
		contains []string
	}{
		{
			name: "no checkpoints",
			data: TickerData{
				CheckpointCount:  0,
				CheckpointStatus: "none",
			},
			contains: []string{"Ckpt", "none"},
		},
		{
			name: "recent checkpoint",
			data: TickerData{
				CheckpointCount:  3,
				CheckpointStatus: "recent",
			},
			contains: []string{"Ckpt", "3", "recent"},
		},
		{
			name: "stale checkpoint",
			data: TickerData{
				CheckpointCount:  1,
				CheckpointStatus: "stale",
			},
			contains: []string{"Ckpt", "1", "stale"},
		},
		{
			name: "old checkpoint",
			data: TickerData{
				CheckpointCount:  5,
				CheckpointStatus: "old",
			},
			contains: []string{"Ckpt", "5", "old"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panel.SetData(tc.data)
			segment := panel.buildPlainCheckpointSegment()

			for _, expected := range tc.contains {
				if !strings.Contains(segment, expected) {
					t.Errorf("expected segment to contain %q, got: %s", expected, segment)
				}
			}
		})
	}
}

func TestTickerPanelGetHeight(t *testing.T) {
	panel := NewTickerPanel()
	if panel.GetHeight() != 1 {
		t.Errorf("expected GetHeight to return 1, got %d", panel.GetHeight())
	}
}

func TestTickerPanelScrollText(t *testing.T) {
	panel := NewTickerPanel()
	panel.SetSize(20, 1)

	// Text shorter than width should be centered
	shortText := "Hello"
	result := panel.scrollPlainText(shortText)
	if len(result) != 20 {
		t.Errorf("expected result length 20, got %d", len(result))
	}
	if !strings.Contains(result, "Hello") {
		t.Error("expected result to contain 'Hello'")
	}

	// Text longer than width should scroll
	longText := "This is a very long text that should scroll horizontally"
	panel.SetAnimTick(0)
	result1 := panel.scrollPlainText(longText)

	panel.SetAnimTick(10)
	result2 := panel.scrollPlainText(longText)

	// Different offsets should produce different visible portions
	if result1 == result2 {
		t.Error("expected different scroll positions to produce different results")
	}
}

func TestTickerPanelViewShowsOverflowBadge(t *testing.T) {
	panel := NewTickerPanel()
	panel.SetSize(32, 1)
	panel.SetAnimTick(14)
	panel.SetData(TickerData{
		TotalAgents:      9,
		ActiveAgents:     7,
		ClaudeCount:      3,
		CodexCount:       3,
		GeminiCount:      3,
		CriticalAlerts:   2,
		WarningAlerts:    4,
		ReadyBeads:       11,
		InProgressBeads:  7,
		BlockedBeads:     3,
		UnreadMessages:   5,
		ActiveLocks:      2,
		MailConnected:    true,
		CheckpointCount:  4,
		CheckpointStatus: "recent",
		BugsCritical:     2,
		BugsWarning:      5,
		BugsScanned:      true,
	})

	view := panel.View()
	if !strings.Contains(view, "<>") || !strings.Contains(view, "%") {
		t.Fatalf("expected overflowing ticker to include progress badge, got %q", view)
	}
}
