package panels

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"zero", 0, "now"},
		{"negative", -5 * time.Second, "now"},
		{"1 second", 1 * time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"1 minute", 60 * time.Second, "1m 00s"},
		{"1 minute 30 seconds", 90 * time.Second, "1m 30s"},
		{"2 minutes", 2 * time.Minute, "2m 00s"},
		{"5 minutes 45 seconds", 5*time.Minute + 45*time.Second, "5m 45s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDuration(tc.duration)
			if got != tc.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.duration, got, tc.want)
			}
		})
	}
}

func TestSpawnConfig(t *testing.T) {
	t.Parallel()

	cfg := spawnConfig()

	if cfg.ID != "spawn" {
		t.Errorf("ID = %q, want %q", cfg.ID, "spawn")
	}
	if cfg.Title != "Spawn Progress" {
		t.Errorf("Title = %q, want %q", cfg.Title, "Spawn Progress")
	}
	if cfg.Priority != PriorityHigh {
		t.Errorf("Priority = %v, want %v", cfg.Priority, PriorityHigh)
	}
	if cfg.RefreshInterval != 1*time.Second {
		t.Errorf("RefreshInterval = %v, want %v", cfg.RefreshInterval, 1*time.Second)
	}
	if cfg.MinWidth != 30 {
		t.Errorf("MinWidth = %d, want %d", cfg.MinWidth, 30)
	}
	if cfg.MinHeight != 5 {
		t.Errorf("MinHeight = %d, want %d", cfg.MinHeight, 5)
	}
	if !cfg.Collapsible {
		t.Error("Collapsible should be true")
	}
}

func TestSpawnData_IsComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		completedAt time.Time
		want        bool
	}{
		{"zero time", time.Time{}, false},
		{"non-zero time", time.Now(), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := SpawnData{CompletedAt: tc.completedAt}
			got := d.IsComplete()
			if got != tc.want {
				t.Errorf("IsComplete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpawnData_SentCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prompts []SpawnPromptStatus
		want    int
	}{
		{"empty", []SpawnPromptStatus{}, 0},
		{"none sent", []SpawnPromptStatus{
			{Sent: false},
			{Sent: false},
		}, 0},
		{"all sent", []SpawnPromptStatus{
			{Sent: true},
			{Sent: true},
		}, 2},
		{"mixed", []SpawnPromptStatus{
			{Sent: true},
			{Sent: false},
			{Sent: true},
			{Sent: false},
		}, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := SpawnData{Prompts: tc.prompts}
			got := d.SentCount()
			if got != tc.want {
				t.Errorf("SentCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSpawnData_PendingCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prompts []SpawnPromptStatus
		want    int
	}{
		{"empty", []SpawnPromptStatus{}, 0},
		{"all pending", []SpawnPromptStatus{
			{Sent: false},
			{Sent: false},
			{Sent: false},
		}, 3},
		{"none pending", []SpawnPromptStatus{
			{Sent: true},
			{Sent: true},
		}, 0},
		{"mixed", []SpawnPromptStatus{
			{Sent: true},
			{Sent: false},
			{Sent: true},
		}, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := SpawnData{Prompts: tc.prompts}
			got := d.PendingCount()
			if got != tc.want {
				t.Errorf("PendingCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewSpawnPanel(t *testing.T) {
	t.Parallel()

	p := NewSpawnPanel()

	if p == nil {
		t.Fatal("NewSpawnPanel returned nil")
	}
	if p.now == nil {
		t.Error("new panel should have non-nil now function")
	}

	cfg := p.Config()
	if cfg.ID != "spawn" {
		t.Errorf("Config ID = %q, want %q", cfg.ID, "spawn")
	}
}

func TestSpawnPanel_IsActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data SpawnData
		want bool
	}{
		{"not active", SpawnData{Active: false}, false},
		{"active and incomplete", SpawnData{Active: true, CompletedAt: time.Time{}}, true},
		{"active but complete", SpawnData{Active: true, CompletedAt: time.Now()}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSpawnPanel()
			p.SetData(tc.data)
			got := p.IsActive()
			if got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpawnPanel_SetData(t *testing.T) {
	t.Parallel()

	p := NewSpawnPanel()

	data := SpawnData{
		Active:         true,
		BatchID:        "test-batch",
		TotalAgents:    3,
		StaggerSeconds: 10,
	}

	p.SetData(data)

	if p.data.BatchID != "test-batch" {
		t.Errorf("BatchID = %q, want %q", p.data.BatchID, "test-batch")
	}
	if p.data.TotalAgents != 3 {
		t.Errorf("TotalAgents = %d, want 3", p.data.TotalAgents)
	}
}

func TestSpawnPanel_Keybindings(t *testing.T) {
	t.Parallel()

	p := NewSpawnPanel()
	bindings := p.Keybindings()

	if bindings != nil {
		t.Errorf("Keybindings() = %v, want nil", bindings)
	}
}

func TestSpawnPanel_ViewShowsPendingCount(t *testing.T) {
	t.Parallel()

	now := time.Now()
	p := NewSpawnPanel()
	p.now = func() time.Time { return now }
	p.SetSize(50, 10)
	p.SetData(SpawnData{
		Active:         true,
		BatchID:        "batch-1",
		StartedAt:      now.Add(-5 * time.Second),
		StaggerSeconds: 10,
		TotalAgents:    3,
		Prompts: []SpawnPromptStatus{
			{Pane: "cc_1", Sent: true, SentAt: now},
			{Pane: "cod_1", Sent: false, ScheduledAt: now.Add(10 * time.Second)},
			{Pane: "agy_1", Sent: false, ScheduledAt: now.Add(20 * time.Second)},
		},
	})

	view := p.View()
	if !strings.Contains(view, "Sent: 1/3") || !strings.Contains(view, "Pending: 2") {
		t.Fatalf("expected SpawnPanel.View() to display Sent: 1/3 and Pending: 2, got %q", view)
	}
}
