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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
			const transcript = "Partial investigation: still checking the failing request path."
			dir := t.TempDir()
			capturePath := filepath.Join(dir, "capture.txt")
			logPath := filepath.Join(dir, "tmux.log")
			killedPath := filepath.Join(dir, "killed")
			if err := os.WriteFile(capturePath, []byte(transcript+"\n"), 0o600); err != nil {
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

func TestEnsembleSynthesizeCommandReconcilesFinishedLiveModes(t *testing.T) {
	const session = "cli-live-synthesis"
	const valid = "\x60\x60\x60yaml\nmode_id: deductive\nthesis: Save completed analysis before synthesis.\nconfidence: 0.8\ntop_findings:\n  - finding: The live result is available without force or stop.\n    impact: high\n    confidence: 0.8\n\x60\x60\x60\n"
	tests := []struct {
		name       string
		transcript string
		status     ensemble.AssignmentStatus
		complete   bool
	}{
		{"finished", valid + "claude> \n", ensemble.AssignmentActive, true},
		{"still working", valid + "Thinking...\n", ensemble.AssignmentActive, false},
		{"queued with old output", valid + "claude> \n", ensemble.AssignmentPending, false},
		{"partial response", "\x60\x60\x60yaml\nthesis: Incomplete\n\x60\x60\x60\nclaude> \n", ensemble.AssignmentActive, false},
		{"wrong mode", strings.Replace(valid, "mode_id: deductive", "mode_id: inductive", 1) + "claude> \n", ensemble.AssignmentActive, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			ensemble.CloseDefaultStateStore()
			t.Cleanup(ensemble.CloseDefaultStateStore)
			oldJSON, oldConfig := jsonOutput, cfg
			jsonOutput, cfg = false, nil
			t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldConfig })

			dir := t.TempDir()
			capturePath := filepath.Join(dir, "capture.txt")
			if err := os.WriteFile(capturePath, []byte(tt.transcript), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_RECONCILE_CAPTURE", capturePath)
			script := strings.Join([]string{
				"#!/bin/sh",
				"case \"$1\" in",
				"  -V) echo 'tmux 3.5a' ;;",
				"  has-session) exit 0 ;;",
				"  list-sessions) echo 'cli-live-synthesis' ;;",
				"  list-panes) printf '%s\\n' '%1_NTM_SEP_0_NTM_SEP_cli-live-synthesis__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]' ;;",
				"  capture-pane) cat \"$NTM_RECONCILE_CAPTURE\" ;;",
				"  *) exit 1 ;;",
				"esac",
			}, "\n") + "\n"
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
				Question:          "Can normal synthesis collect finished live work?",
				Status:            ensemble.EnsembleActive,
				SynthesisStrategy: ensemble.StrategyManual,
				CreatedAt:         now,
				Assignments: []ensemble.ModeAssignment{
					{ModeID: "deductive", PaneName: session + "__cc_1", AgentType: "cc", Status: tt.status, AssignedAt: now},
				},
			}
			if err := ensemble.SaveSession(session, state); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			cmd := newEnsembleSynthesizeCmd()
			cmd.SetOut(&buf)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{session, "--strategy=manual", "--no-cache", "--format=json"})
			err := cmd.ExecuteContext(t.Context())
			if tt.complete {
				if err != nil {
					t.Fatalf("normal synthesis failed: %v\n%s", err, buf.String())
				}
				if !json.Valid(buf.Bytes()) || !strings.Contains(buf.String(), "The live result is available") {
					t.Fatalf("synthesis lost the completed analysis: %s", buf.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), "synthesis not ready") {
				t.Fatalf("unfinished mode became ready: err=%v output=%s", err, buf.String())
			}

			ensemble.CloseDefaultStateStore()
			saved, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			assignment := saved.Assignments[0]
			if tt.complete {
				if assignment.Status != ensemble.AssignmentDone || assignment.CompletedAt == nil || assignment.OutputPath == "" {
					t.Fatalf("completion was not durable: %+v", assignment)
				}
				offline, err := ensemble.CollectModeOutputs(saved, nil)
				if err != nil || offline.Count() != 1 || !strings.Contains(offline.Outputs[0].Thesis, "Save completed") {
					t.Fatalf("saved analysis unavailable after reopen: %+v, %v", offline, err)
				}
			} else if assignment.Status != tt.status || assignment.CompletedAt != nil || assignment.OutputPath != "" {
				t.Fatalf("unfinished assignment changed: %+v", assignment)
			}
		})
	}
}

func TestEnsembleSynthesizeCommandUsesPersistedSettings(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)
	oldJSON, oldConfig := jsonOutput, cfg
	jsonOutput, cfg = false, nil
	t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldConfig })
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binPath)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })

	outputPath := filepath.Join(dir, "deductive.json")
	modeOutput := ensemble.ModeOutput{
		ModeID:     "deductive",
		Thesis:     "The saved run controls synthesis.",
		Confidence: 0.9,
		TopFindings: []ensemble.Finding{
			{Finding: "Use bounded retries for network delivery.", Impact: ensemble.ImpactHigh, Confidence: 0.95},
			{Finding: "Separate audit storage from status rendering.", Impact: ensemble.ImpactMedium, Confidence: 0.1},
		},
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	state := &ensemble.EnsembleSession{
		SessionName:       "cli-saved-synthesis-settings",
		Question:          "Preserve settings while allowing an explicit new override.",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyManual,
		Budget:            &ensemble.BudgetConfig{SynthesisReserveTokens: 2000},
		Synthesis: &ensemble.SynthesisConfig{
			Strategy:      ensemble.StrategyManual,
			MinConfidence: 0.75,
			MaxFindings:   1,
		},
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatal(err)
	}
	ensemble.CloseDefaultStateStore()

	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"saved settings", nil, 1},
		{"explicit override", []string{"--min-confidence=0.1", "--max-findings=4"}, 2},
		{"explicit zero", []string{"--min-confidence=0", "--max-findings=0"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := newEnsembleSynthesizeCmd()
			cmd.SetOut(&buf)
			cmd.SetErr(io.Discard)
			args := append([]string{state.SessionName, "--no-cache", "--format=json"}, tt.args...)
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("synthesize: %v\n%s", err, buf.String())
			}
			var payload struct {
				Synthesis *ensemble.SynthesisResult `json:"synthesis"`
			}
			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil || payload.Synthesis == nil {
				t.Fatalf("invalid synthesis response: %v\n%s", err, buf.String())
			}
			if len(payload.Synthesis.Findings) != tt.want {
				t.Fatalf("finding count = %d, want %d: %+v", len(payload.Synthesis.Findings), tt.want, payload.Synthesis.Findings)
			}
		})
	}

	// A synthesis cap is distinct from the allocation reserved for the step.
	// The saved cap must survive until an operator explicitly overrides it.
	state.Synthesis.MaxSynthesisTokens = 50
	modeOutput.TopFindings[0].Finding = strings.Repeat("Detailed independent evidence. ", 40)
	data, err = json.Marshal(modeOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatal(err)
	}
	ensemble.CloseDefaultStateStore()

	for _, tt := range []struct {
		name    string
		args    []string
		trimmed bool
	}{
		{"saved cap differs from reserve", nil, true},
		{"explicit unlimited cap", []string{"--budget-synthesis=0"}, false},
		{"explicit larger cap", []string{"--budget-synthesis=2000"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := newEnsembleSynthesizeCmd()
			cmd.SetOut(&buf)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{state.SessionName, "--no-cache", "--format=json"}, tt.args...))
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("synthesize: %v\n%s", err, buf.String())
			}
			var payload struct {
				Synthesis *ensemble.SynthesisResult `json:"synthesis"`
			}
			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil || payload.Synthesis == nil {
				t.Fatalf("invalid synthesis response: %v\n%s", err, buf.String())
			}
			if len(payload.Synthesis.Findings) != 1 {
				t.Fatalf("saved findings limit changed: %+v", payload.Synthesis.Findings)
			}
			finding := payload.Synthesis.Findings[0].Finding
			if tt.trimmed {
				if len(finding) > 64 || payload.Synthesis.TokensUsed > state.Synthesis.MaxSynthesisTokens {
					t.Fatalf("saved cap was replaced by reserve: %+v", payload.Synthesis)
				}
			} else if finding != modeOutput.TopFindings[0].Finding {
				t.Fatalf("explicit cap override did not preserve the finding: %q", finding)
			}
		})
	}
}
