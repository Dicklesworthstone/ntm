package robot

import (
	"testing"
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
