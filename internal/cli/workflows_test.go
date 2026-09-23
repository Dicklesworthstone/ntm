package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// workflowCommandTmuxFixture records the actual tmux transport used by the
// public workflow command and canonical send service. No runner port is replaced.
func workflowCommandTmuxFixture(t *testing.T, scenario string) (projectRoot, logPath string) {
	t.Helper()
	isolateIdentityDirs(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	oldConfig, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	cfg, jsonOutput, tmux.DefaultClient = config.Default(), true, tmux.NewClient("")
	cfg.AgentMail.Enabled = false
	cfg.CASS.Context.Enabled = false
	t.Cleanup(func() { cfg, jsonOutput, tmux.DefaultClient = oldConfig, oldJSON, oldClient })
	projectRoot = t.TempDir()
	chdirForTerminalJSONTest(t, projectRoot)
	logPath = filepath.Join(projectRoot, "tmux-calls")
	initialOutput := "SHIP-VERDICT\nready\n"
	if scenario == "preparation-verdict" {
		initialOutput = "ready\n"
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "pane-output"), []byte(initialOutput), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_WORKFLOW_TEST_ROOT", projectRoot)
	t.Setenv("NTM_WORKFLOW_TEST_SCENARIO", scenario)
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_WORKFLOW_TEST_ROOT/tmux-calls"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  list-sessions) echo 'evidence-run_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000' ;;
  list-panes)
    pane='%91'; pid=4242; dead=0
    if [ "$NTM_WORKFLOW_TEST_SCENARIO" = zero-pid ]; then pid=0; fi
    if [ -f "$NTM_WORKFLOW_TEST_ROOT/captured" ]; then
      case "$NTM_WORKFLOW_TEST_SCENARIO" in
        changed-pid) pid=4243 ;;
        changed-pane) pane='%92' ;;
        dead-pane) dead=1 ;;
      esac
    fi
    printf '%s_NTM_SEP_1_NTM_SEP_evidence-run__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_%s\n' "$pane" "$pid" "$dead"
    ;;
  capture-pane)
    : > "$NTM_WORKFLOW_TEST_ROOT/captured"
    cat "$NTM_WORKFLOW_TEST_ROOT/pane-output"
    ;;
  display-message)
    if [ "$NTM_WORKFLOW_TEST_SCENARIO" = preparation-verdict ] && [ -f "$NTM_WORKFLOW_TEST_ROOT/captured" ] && [ ! -f "$NTM_WORKFLOW_TEST_ROOT/preparation-verdict" ]; then
      printf 'SHIP-VERDICT\n' >> "$NTM_WORKFLOW_TEST_ROOT/pane-output"
      printf 'old-verdict-before-delivery\n' >> "$NTM_WORKFLOW_TEST_ROOT/tmux-calls"
      : > "$NTM_WORKFLOW_TEST_ROOT/preparation-verdict"
    fi
    printf '%s\n' "$NTM_WORKFLOW_TEST_ROOT"
    ;;
  load-buffer) cat > "$NTM_WORKFLOW_TEST_ROOT/delivered-prompt" ;;
esac
`
	bin := filepath.Join(projectRoot, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	return projectRoot, logPath
}

func runWorkflowEvidenceCommand(t *testing.T, ctx context.Context, projectRoot, description, pattern string) (WorkflowRunResult, error) {
	t.Helper()
	template := fmt.Sprintf(`[[workflows]]
name = "evidence-test"
description = %q
coordination = "review-gate"
[[workflows.agents]]
profile = "reviewer"
role = "reviewer"
[workflows.flow]
initial = "review"
require_approval = true
approval_mode = "any"
[[workflows.flow.transitions]]
from = "review"
to = "complete"
[workflows.flow.transitions.trigger]
type = "agent_says"
role = "reviewer"
pattern = %q
`, description, pattern)
	path := filepath.Join(projectRoot, "flow.toml")
	if err := os.WriteFile(path, []byte(template), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newWorkflowsRunCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{path, "--session", "evidence-run", "--project-root", projectRoot, "--interval", "10ms", "--timeout", "10s"})
	stdout, runErr := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
	var result WorkflowRunResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode workflow command result: %v, output=%q, command error=%v", err, stdout, runErr)
	}
	return result, runErr
}

func TestWorkflowsRunRejectsChangedPaneLifetimeBeforeDelivery(t *testing.T) {
	for _, scenario := range []string{"zero-pid", "changed-pid", "changed-pane", "dead-pane"} {
		t.Run(scenario, func(t *testing.T) {
			projectRoot, logPath := workflowCommandTmuxFixture(t, scenario)
			result, err := runWorkflowEvidenceCommand(t, t.Context(), projectRoot, "Review the proposed change", "SHIP-VERDICT")
			if err == nil || !strings.Contains(err.Error(), "changed process lifetime or is unavailable") || result.Success || result.Completed || result.Transitions != 0 {
				t.Fatalf("workflow accepted invalid pane lifetime: result=%+v error=%v", result, err)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(log), operation) {
					t.Fatalf("invalid pane received %s: %s", operation, log)
				}
			}
			if scenario != "zero-pid" && !strings.Contains(string(log), "capture-pane -t %91") {
				t.Fatalf("fixture did not exercise replacement during capture: %s", log)
			}
		})
	}
}

func TestWorkflowsRunDoesNotTrustPreexistingVerdict(t *testing.T) {
	assertWorkflowCommandDoesNotTrustOldVerdict(t, "stable")
}

func TestWorkflowsRunExcludesVerdictArrivingDuringSendPreparation(t *testing.T) {
	assertWorkflowCommandDoesNotTrustOldVerdict(t, "preparation-verdict")
}

func assertWorkflowCommandDoesNotTrustOldVerdict(t *testing.T, scenario string) {
	t.Helper()
	projectRoot, logPath := workflowCommandTmuxFixture(t, scenario)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log, _ := os.ReadFile(logPath)
				// Count only captures after submission: the preparation path
				// may refresh the baseline several times before delivery.
				lastEnter := strings.LastIndex(string(log), "send-keys -t %91 Enter")
				if lastEnter >= 0 && strings.Count(string(log[lastEnter:]), "capture-pane -t %91 -p -S -200") >= 2 {
					close(observed)
					cancel()
					return
				}
			}
		}
	}()
	result, runErr := runWorkflowEvidenceCommand(t, ctx, projectRoot, "Review the proposed change", "SHIP-VERDICT")
	cancel()
	<-watcherDone
	if !errors.Is(runErr, context.Canceled) || result.Completed || result.Transitions != 0 || result.Reason != "canceled" {
		t.Fatalf("old output advanced or interrupted delivery: result=%+v error=%v", result, runErr)
	}
	select {
	case <-observed:
	default:
		t.Fatal("workflow did not observe the old verdict after delivering its prompt")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	baseline := strings.Index(string(log), "capture-pane -t %91 -p -S -200")
	delivery := strings.Index(string(log), "load-buffer")
	if baseline < 0 || delivery <= baseline || !strings.Contains(string(log), "paste-buffer") || strings.Count(string(log), "send-keys -t %91 Enter") != 2 {
		t.Fatalf("baseline did not precede complete canonical delivery: %s", log)
	}
	if scenario == "preparation-verdict" {
		oldVerdict := strings.Index(string(log), "old-verdict-before-delivery")
		if oldVerdict <= baseline || oldVerdict >= delivery {
			t.Fatalf("fixture did not introduce the old verdict during send preparation: %s", log)
		}
		if !strings.Contains(string(log[oldVerdict:delivery]), "capture-pane -t %91 -p -S -200") {
			t.Fatalf("prepared send never refreshed its baseline after the old verdict arrived: %s", log)
		}
	}
	state, err := (&workflow.StateStore{Dir: filepath.Join(projectRoot, ".ntm", "workflows", "state")}).Load("evidence-run")
	if err != nil {
		t.Fatal(err)
	}
	if state.Completed || state.CurrentStage != "review" || len(state.Dispatches) != 1 || state.Dispatches[0].Status != "delivered" || state.Evidence == nil || len(state.Evidence.Matches) != 0 {
		t.Fatalf("checkpoint confused old output with new approval: %+v", state)
	}
	wantCapture := "SHIP-VERDICT\nready"
	if scenario == "preparation-verdict" {
		wantCapture = "ready\nSHIP-VERDICT"
	}
	if pane := state.Evidence.Panes["%91"]; pane.PID != 4242 || pane.Capture != wantCapture || pane.Fresh != "" {
		t.Fatalf("preexisting output escaped its saved baseline: %+v", pane)
	}
}

func TestWorkflowsRunRejectsPromptVerdictBeforeTransport(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		pattern     string
		stamp       bool
	}{
		{name: "template echoes verdict", description: "Respond with SHIP-VERDICT", pattern: "SHIP-VERDICT"},
		{name: "final stamping introduces verdict", description: "Review the proposed change", pattern: "NTM-Pane:", stamp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, logPath := workflowCommandTmuxFixture(t, "stable")
			cfg.Robot.Semantic.Stamp = tc.stamp
			result, err := runWorkflowEvidenceCommand(t, t.Context(), projectRoot, tc.description, tc.pattern)
			if err == nil || !strings.Contains(err.Error(), "prompt itself matches agent_says pattern") || result.Success || result.Completed || result.Transitions != 0 {
				t.Fatalf("ambiguous prompt accepted: result=%+v error=%v", result, err)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(log), operation) {
					t.Fatalf("ambiguous prompt reached %s: %s", operation, log)
				}
			}
		})
	}
}

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
