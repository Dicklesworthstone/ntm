package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestLogsOptionsDefaults(t *testing.T) {
	opts := LogsOptions{
		Session: "test",
	}

	if opts.Session != "test" {
		t.Errorf("expected session 'test', got %s", opts.Session)
	}

	if opts.Limit != 0 {
		t.Errorf("expected zero limit by default, got %d", opts.Limit)
	}
}

func TestLogsOutput_EmptyPanes(t *testing.T) {
	output := &LogsOutput{
		RobotResponse: NewRobotResponse(true),
		Panes:         []PaneLogs{},
		Summary:       LogsSummary{},
	}

	if !output.Success {
		t.Error("expected Success to be true")
	}

	if len(output.Panes) != 0 {
		t.Errorf("expected empty panes, got %d", len(output.Panes))
	}
}

func TestDefaultLogsLimit(t *testing.T) {
	if DefaultLogsLimit != 100 {
		t.Errorf("DefaultLogsLimit = %d, want 100", DefaultLogsLimit)
	}
}

func TestLogsStreamer_Creation(t *testing.T) {
	opts := StreamLogsOptions{
		Session: "test",
	}

	streamer, err := NewLogsStreamer(opts)
	if err != nil {
		t.Fatalf("NewLogsStreamer failed: %v", err)
	}

	if streamer == nil {
		t.Error("expected non-nil streamer")
	}
}

func TestLogsStreamer_InvalidFilter(t *testing.T) {
	opts := StreamLogsOptions{
		Session: "test",
		Filter:  "[invalid",
	}

	_, err := NewLogsStreamer(opts)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestLogsStreamer_MultiWindowPaneKeyDistinct(t *testing.T) {
	t.Parallel()
	w0p1 := tmux.Pane{ID: "%1", WindowIndex: 0, Index: 1}
	w1p1 := tmux.Pane{ID: "%9", WindowIndex: 1, Index: 1}
	if logsPaneKey(w0p1) == logsPaneKey(w1p1) {
		t.Fatalf("distinct multi-window panes collided on logsPaneKey: %q", logsPaneKey(w0p1))
	}
	w0NoID := tmux.Pane{WindowIndex: 0, Index: 1}
	w1NoID := tmux.Pane{WindowIndex: 1, Index: 1}
	if logsPaneKey(w0NoID) == logsPaneKey(w1NoID) {
		t.Fatalf("distinct multi-window panes without IDs collided on logsPaneKey: %q", logsPaneKey(w0NoID))
	}
}
