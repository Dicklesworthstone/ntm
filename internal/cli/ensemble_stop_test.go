package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestEnsembleStopOutput_JSON(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	// Test JSON marshaling of stop output
	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "test-session",
		Success:     true,
		Message:     "Ensemble stopped: 3 panes terminated, 2 outputs captured",
		Captured:    2,
		Stopped:     3,
		Errors:      0,
		FinalStatus: "stopped",
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	t.Logf("TEST: %s - JSON output:\n%s", t.Name(), string(data))

	if !strings.Contains(string(data), "test-session") {
		t.Error("JSON should contain session name")
	}
	if !strings.Contains(string(data), "stopped") {
		t.Error("JSON should contain final status")
	}

	t.Logf("TEST: %s - assertion: JSON marshaling works", t.Name())
}

func TestRenderEnsembleStopOutput_Text(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "test-session",
		Success:     true,
		Message:     "Ensemble stopped: 3 panes terminated",
		Stopped:     3,
		FinalStatus: "stopped",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "text", false)
	if err != nil {
		t.Fatalf("renderEnsembleStopOutput failed: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - text output:\n%s", t.Name(), output)

	if !strings.Contains(output, "test-session") {
		t.Error("Output should contain session name")
	}
	if !strings.Contains(output, "stopped") {
		t.Error("Output should contain status")
	}
	if !strings.Contains(output, "3 panes") {
		t.Error("Output should contain pane count")
	}

	t.Logf("TEST: %s - assertion: text rendering works", t.Name())
}

func TestRenderEnsembleStopOutput_Quiet(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "test-session",
		Success:     true,
		FinalStatus: "stopped",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "text", true)
	if err != nil {
		t.Fatalf("renderEnsembleStopOutput failed: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - quiet output: %q", t.Name(), output)

	if output != "stopped\n" {
		t.Errorf("Quiet output should be 'stopped\\n', got %q", output)
	}

	t.Logf("TEST: %s - assertion: quiet mode works", t.Name())
}

func TestRenderEnsembleStopOutput_Error(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "test-session",
		Success:     false,
		Error:       "failed to kill session",
		FinalStatus: "active",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "text", true)
	if err != nil {
		t.Fatalf("renderEnsembleStopOutput failed: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - error output: %q", t.Name(), output)

	if !strings.Contains(output, "error:") {
		t.Error("Error output should contain 'error:'")
	}

	t.Logf("TEST: %s - assertion: error handling works", t.Name())
}

func TestNewEnsembleStopCmd(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	cmd := newEnsembleStopCmd()
	if cmd == nil {
		t.Fatal("newEnsembleStopCmd returned nil")
	}

	if cmd.Use != "stop [session]" {
		t.Errorf("Use = %q, want 'stop [session]'", cmd.Use)
	}

	// Check flags exist
	flags := []string{"force", "no-collect", "quiet", "format"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("Flag --%s should exist", name)
		}
	}

	t.Logf("TEST: %s - assertion: command created with correct flags", t.Name())
}

func TestEnsembleStopCommandPreservesPartialOutputFromLiveSessions(t *testing.T) {
	for _, status := range []ensemble.EnsembleStatus{ensemble.EnsembleActive, ensemble.EnsembleError} {
		t.Run(status.String(), func(t *testing.T) {
			isolateSessionAgentStorage(t)
			ensemble.CloseDefaultStateStore()
			t.Cleanup(ensemble.CloseDefaultStateStore)
			oldJSON := jsonOutput
			jsonOutput = false
			t.Cleanup(func() { jsonOutput = oldJSON })

			const session = "cli-durable-stop"
			const transcript = "Partial investigation: still checking the failing request path.\n"
			dir := t.TempDir()
			capturePath := filepath.Join(dir, "capture.txt")
			logPath := filepath.Join(dir, "tmux.log")
			killedPath := filepath.Join(dir, "killed")
			if err := os.WriteFile(capturePath, []byte(transcript), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_CLI_STOP_CAPTURE", capturePath)
			t.Setenv("NTM_CLI_STOP_LOG", logPath)
			t.Setenv("NTM_CLI_STOP_KILLED", killedPath)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_CLI_STOP_LOG"
case "$1" in
  -V) echo 'tmux 3.5a' ;;
  has-session) if [ -e "$NTM_CLI_STOP_KILLED" ]; then exit 1; fi ;;
  list-sessions) echo 'cli-durable-stop' ;;
  list-panes)
    printf '%s\n' '%1_NTM_SEP_0_NTM_SEP_cli-durable-stop__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0'
    ;;
  capture-pane) cat "$NTM_CLI_STOP_CAPTURE" ;;
  kill-session) touch "$NTM_CLI_STOP_KILLED" ;;
esac
exit 0
`
			binPath := filepath.Join(dir, "tmux")
			if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", binPath)
			oldClient := tmux.DefaultClient
			tmux.DefaultClient = tmux.NewClient("")
			t.Cleanup(func() { tmux.DefaultClient = oldClient })

			now := time.Now().UTC()
			state := &ensemble.EnsembleSession{
				SessionName:       session,
				Question:          "Keep partial work after a stopped or interrupted run",
				Status:            status,
				SynthesisStrategy: ensemble.StrategyConsensus,
				CreatedAt:         now,
				Assignments: []ensemble.ModeAssignment{
					{ModeID: "deductive", PaneName: session + "__cc_1", AgentType: "cc", Status: ensemble.AssignmentActive, AssignedAt: now},
				},
			}
			if err := ensemble.SaveSession(session, state); err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			cmd := newEnsembleStopCmd()
			cmd.SetOut(&buf)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{session, "--force", "--format=json"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("ensemble stop command failed: %v\n%s", err, buf.String())
			}
			var out ensembleStopOutput
			if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
				t.Fatalf("decode stop output: %v\n%s", err, buf.String())
			}
			if !out.Success || out.FinalStatus != ensemble.EnsembleStopped.String() || out.Stopped != 1 || out.Captured != 1 || len(out.SavedOutputPaths) != 1 {
				t.Fatalf("stop did not preserve the live ensemble: %+v", out)
			}
			raw, err := os.ReadFile(out.SavedOutputPaths[0])
			if err != nil || string(raw) != transcript {
				t.Fatalf("saved transcript=%q err=%v, want %q", raw, err, transcript)
			}
			if _, err := os.Stat(killedPath); err != nil {
				t.Fatalf("live session was not killed: %v", err)
			}

			ensemble.CloseDefaultStateStore()
			saved, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != ensemble.EnsembleStopped || saved.Assignments[0].Status != ensemble.AssignmentError || saved.Assignments[0].OutputPath != out.SavedOutputPaths[0] {
				t.Fatalf("reopened state lost the unfinished output: %+v", saved)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			captureIndex := strings.Index(string(calls), "capture-pane")
			killIndex := strings.Index(string(calls), "kill-session")
			if captureIndex < 0 || killIndex < captureIndex || strings.Count(string(calls), "kill-session") != 1 {
				t.Fatalf("expected capture before one session kill:\n%s", calls)
			}
		})
	}
}
