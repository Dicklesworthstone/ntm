package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func TestRobotSequenceExpectedPositionFlag(t *testing.T) {
	flag := rootCmd.Flags().Lookup("sequence-expected-position")
	if flag == nil {
		t.Fatal("retry-safe sequence flag is not registered")
	}
	originalValue, originalChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() {
		_ = flag.Value.Set(originalValue)
		flag.Changed = originalChanged
	})
	flag.Changed = false
	projectDir := t.TempDir()
	run := func(action, pane, steps string) RobotSequenceOutput {
		t.Helper()
		stdout, err := captureStdout(t, func() error {
			return printRobotSequence(projectDir, "review", action, pane, steps)
		})
		if err != nil {
			t.Fatalf("sequence %s: %v", action, err)
		}
		var result RobotSequenceOutput
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("sequence output %q: %v", stdout, err)
		}
		if !result.Success {
			t.Fatalf("sequence reported failure: %s", stdout)
		}
		return result
	}
	run("create", "", `["inspect","challenge","summarize"]`)
	first := run("next", "%1", "")
	if first.Pane == nil || first.Pane.Position != 0 {
		t.Fatalf("initial position = %+v", first.Pane)
	}
	if err := rootCmd.Flags().Set("sequence-expected-position", "0"); err != nil {
		t.Fatal(err)
	}
	advanced := run("advance", "%1", "")
	if advanced.Pane == nil || !advanced.Pane.Advanced || advanced.Pane.Position != 1 {
		t.Fatalf("advance = %+v", advanced.Pane)
	}
	retry := run("advance", "%1", "")
	if retry.Pane == nil || retry.Pane.Advanced || retry.Pane.Position != 1 || retry.Pane.Prompt != "challenge" {
		t.Fatalf("retry skipped a prompt: %+v", retry.Pane)
	}
	for _, tc := range []struct{ action, value, want string }{
		{"advance", "2", "before expected position"},
		{"advance", "-1", "must be non-negative"},
		{"next", "0", "requires --sequence-action=advance"},
		{"create", "0", "requires --sequence-action=advance"},
	} {
		if err := rootCmd.Flags().Set("sequence-expected-position", tc.value); err != nil {
			t.Fatal(err)
		}
		stdout, err := captureStdout(t, func() error {
			return printRobotSequence(projectDir, "review", tc.action, "%1", `["replacement"]`)
		})
		if err == nil || !strings.Contains(err.Error(), tc.want) || stdout != "" {
			t.Errorf("action=%s expected=%s: output=%q error=%v", tc.action, tc.value, stdout, err)
		}
	}
	store, err := workflow.NewPaneSequenceStore(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Next("review", "%1")
	if err != nil || persisted.Position != 1 || persisted.Prompt != "challenge" {
		t.Fatalf("rejected request changed state: %+v, %v", persisted, err)
	}
	// Omitting the guard retains explicitly requested unconditional advancement.
	flag.Changed = false
	unconditional := run("advance", "%1", "")
	if unconditional.Pane == nil || unconditional.Pane.Position != 2 || !unconditional.Pane.Advanced {
		t.Fatalf("unconditional advance = %+v", unconditional.Pane)
	}
	other := run("next", "%2", "")
	if other.Pane == nil || other.Pane.Position != 0 {
		t.Fatalf("other pane inherited progress: %+v", other.Pane)
	}
}

func TestWorkflowsJSONFailuresAreTerminal(t *testing.T) {
	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	t.Run("loader error", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		chdirForTerminalJSONTest(t, t.TempDir())
		workflowDir := filepath.Join(configHome, "ntm", "workflows")
		if err := os.MkdirAll(workflowDir, 0755); err != nil {
			t.Fatalf("create workflow directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workflowDir, "invalid.toml"), []byte("[[workflows]\n"), 0644); err != nil {
			t.Fatalf("write invalid workflow: %v", err)
		}

		stdout, runErr := captureStdout(t, runWorkflowsList)
		assertWorkflowTerminalJSONFailure(t, stdout, runErr, "parsing workflow")
	})

	t.Run("missing name", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		chdirForTerminalJSONTest(t, t.TempDir())

		stdout, runErr := captureStdout(t, func() error {
			return runWorkflowsShow("definitely-missing-workflow")
		})
		assertWorkflowTerminalJSONFailure(t, stdout, runErr, "workflow template not found")
	})
}

func assertWorkflowTerminalJSONFailure(t *testing.T, stdout string, runErr error, want string) {
	t.Helper()
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("workflow error = %v, want errJSONFailure", runErr)
	}
	if !strings.Contains(runErr.Error(), want) {
		t.Fatalf("workflow error = %v, want %q", runErr, want)
	}
	document := decodeSingleTerminalJSONMap(t, stdout)
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("success = %#v, want false", document["success"])
	}
	errorMessage, ok := document["error"].(string)
	if !ok || !strings.Contains(errorMessage, want) {
		t.Fatalf("error = %#v, want %q", document["error"], want)
	}
}

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
