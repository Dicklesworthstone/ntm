package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func TestCoordinationIcon(t *testing.T) {

	tests := []struct {
		name  string
		coord workflow.CoordinationType
		want  string
	}{
		{"ping-pong has bidirectional arrows", workflow.CoordPingPong, "\u21c4"},
		{"pipeline has right arrow", workflow.CoordPipeline, "\u2192"},
		{"parallel has parallel lines", workflow.CoordParallel, "\u2261"},
		{"review-gate has checkmark", workflow.CoordReviewGate, "\u2713"},
		{"unknown has bullet", workflow.CoordinationType("unknown"), "\u2022"},
		{"empty has bullet", workflow.CoordinationType(""), "\u2022"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := coordinationIcon(tc.coord)
			if got != tc.want {
				t.Errorf("coordinationIcon(%q) = %q, want %q", tc.coord, got, tc.want)
			}
		})
	}
}

func TestFormatTrigger(t *testing.T) {

	tests := []struct {
		name    string
		trigger workflow.Trigger
		want    string
	}{
		{
			name:    "file_created with pattern",
			trigger: workflow.Trigger{Type: workflow.TriggerFileCreated, Pattern: "*.go"},
			want:    "file_created: *.go",
		},
		{
			name:    "file_modified with pattern",
			trigger: workflow.Trigger{Type: workflow.TriggerFileModified, Pattern: "*.ts"},
			want:    "file_modified: *.ts",
		},
		{
			name:    "command_success with command",
			trigger: workflow.Trigger{Type: workflow.TriggerCommandSuccess, Command: "go test"},
			want:    "command_success: go test",
		},
		{
			name:    "command_failure with command",
			trigger: workflow.Trigger{Type: workflow.TriggerCommandFailure, Command: "make build"},
			want:    "command_failure: make build",
		},
		{
			name:    "agent_says without role",
			trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "DONE"},
			want:    `agent_says: "DONE"`,
		},
		{
			name:    "agent_says with role",
			trigger: workflow.Trigger{Type: workflow.TriggerAgentSays, Pattern: "READY", Role: "tester"},
			want:    `agent_says: "READY" (role: tester)`,
		},
		{
			name:    "all_idle with minutes",
			trigger: workflow.Trigger{Type: workflow.TriggerAllAgentsIdle, IdleMinutes: 5},
			want:    "all_idle: 5m",
		},
		{
			name:    "manual without label",
			trigger: workflow.Trigger{Type: workflow.TriggerManual},
			want:    "manual",
		},
		{
			name:    "manual with label",
			trigger: workflow.Trigger{Type: workflow.TriggerManual, Label: "Start Review"},
			want:    "manual: Start Review",
		},
		{
			name:    "time_elapsed with minutes",
			trigger: workflow.Trigger{Type: workflow.TriggerTimeElapsed, Minutes: 10},
			want:    "time: 10m",
		},
		{
			name:    "unknown type returns type string",
			trigger: workflow.Trigger{Type: workflow.TriggerType("custom")},
			want:    "custom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTrigger(tc.trigger)
			if got != tc.want {
				t.Errorf("formatTrigger(%v) = %q, want %q", tc.trigger, got, tc.want)
			}
		})
	}
}

func TestFormatTriggerContains(t *testing.T) {

	// Test that output contains expected substrings for complex cases
	tests := []struct {
		name     string
		trigger  workflow.Trigger
		contains []string
	}{
		{
			name:     "file pattern preserved",
			trigger:  workflow.Trigger{Type: workflow.TriggerFileCreated, Pattern: "src/**/*.go"},
			contains: []string{"file_created", "src/**/*.go"},
		},
		{
			name:     "command with spaces preserved",
			trigger:  workflow.Trigger{Type: workflow.TriggerCommandSuccess, Command: "npm run test:unit"},
			contains: []string{"command_success", "npm run test:unit"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTrigger(tc.trigger)
			for _, substr := range tc.contains {
				if !strings.Contains(got, substr) {
					t.Errorf("formatTrigger() = %q, should contain %q", got, substr)
				}
			}
		})
	}
}
