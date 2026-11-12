package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type isolatedEnsembleTmuxEnv struct {
	TmuxDir     string
	ResponseDir string
	EventDir    string
	AgentScript string
}

func logEnsembleTestJSON(logger *testutil.TestLogger, step string, fields map[string]any) {
	if logger == nil {
		return
	}
	entry := map[string]any{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"step": step,
	}
	for k, v := range fields {
		entry[k] = v
	}
	data, err := json.Marshal(entry)
	if err != nil {
		logger.Log("JSON_MARSHAL_ERR step=%s err=%v", step, err)
		return
	}
	logger.Log("JSON_EVENT %s", string(data))
}

func setupIsolatedEnsembleTmuxEnv(t *testing.T, logger *testutil.TestLogger) *isolatedEnsembleTmuxEnv {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()

	tmuxDir := testutil.ShortTmuxTempDir(t)
	t.Setenv("TMUX_TMPDIR", tmuxDir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HISTFILE", "/dev/null")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	fixtureRoot := t.TempDir()
	responseDir := filepath.Join(fixtureRoot, "responses")
	eventDir := filepath.Join(fixtureRoot, "events")
	for _, dir := range []string{responseDir, eventDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	scriptPath := filepath.Join(fixtureRoot, "mock-ensemble-agent.sh")
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -u",
		"stty -echo 2>/dev/null || true",
		fmt.Sprintf("RESP_DIR=%q", responseDir),
		fmt.Sprintf("EVENT_DIR=%q", eventDir),
		"printf '❯ \\n'",
		"while IFS= read -r line; do",
		"  clean=\"${line%$'\\r'}\"",
		"  case \"$clean\" in",
		"    \"**ID**: \"*)",
		"      seen_mode=\"${clean#\"**ID**: \"}\"",
		"      seen_mode=\"${seen_mode#\"${seen_mode%%[![:space:]]*}\"}\"",
		"      seen_mode=\"${seen_mode%\"${seen_mode##*[![:space:]]}\"}\"",
		"      while IFS= read -r -t 0.35 _; do :; done",
		"      if [ -n \"$seen_mode\" ]; then",
		"        printf '{\"mode_id\":\"%s\",\"pane\":\"%s\"}\\n' \"$seen_mode\" \"${TMUX_PANE:-}\" >> \"$EVENT_DIR/invocations.jsonl\"",
		"        printf '%s\\n' \"$seen_mode\" >> \"$EVENT_DIR/$seen_mode.runs\"",
		"        printf '\\n```yaml\\n'",
		"        if [ -f \"$RESP_DIR/$seen_mode.yaml\" ]; then",
		"          cat \"$RESP_DIR/$seen_mode.yaml\"",
		"        else",
		"          printf 'mode_id: %s\\n' \"$seen_mode\"",
		"          printf 'thesis: \"Generated thesis for %s\"\\n' \"$seen_mode\"",
		"          printf 'confidence: 0.86\\n'",
		"          printf 'top_findings:\\n'",
		"          printf '  - finding: \"Finding produced in tmux for %s\"\\n' \"$seen_mode\"",
		"          printf '    impact: high\\n'",
		"          printf '    confidence: 0.86\\n'",
		"          printf '    evidence_pointer: \"main.go:10\"\\n'",
		"          printf '    reasoning: \"Deterministic mock agent analysis for %s\"\\n' \"$seen_mode\"",
		"        fi",
		"        printf '\\n```\\n❯ \\n'",
		"      fi",
		"      ;;",
		"    \"You are the SYNTHESIZER for a reasoning ensemble.\"*) ",
		"      synth_findings=()",
		"      in_task=0",
		"      while IFS= read -r -t 0.35 sline; do",
		"        sclean=\"${sline%$'\\r'}\"",
		"        case \"$sclean\" in",
		"          \"## Your Task\"*) in_task=1 ;;",
		"          *'\"finding\": \"'*)",
		"            if [ \"$in_task\" -eq 0 ]; then",
		"              val=\"${sclean#*'\"finding\": \"'}\"",
		"              val=\"${val%%'\"'*}\"",
		"              if [ -n \"$val\" ]; then",
		"                synth_findings+=(\"$val\")",
		"              fi",
		"            fi",
		"            ;;",
		"        esac",
		"      done",
		"      printf '{\"synthesizer\":true,\"pane\":\"%s\"}\\n' \"${TMUX_PANE:-}\" >> \"$EVENT_DIR/invocations.jsonl\"",
		"      printf '\\n```yaml\\n'",
		"      printf 'summary: \"Live lead-agent synthesis across %d ensemble findings\"\\n' \"${#synth_findings[@]}\"",
		"      printf 'confidence: 0.89\\n'",
		"      printf 'findings:\\n'",
		"      if [ \"${#synth_findings[@]}\" -eq 0 ]; then",
		"        printf '  - finding: \"Merged ensemble finding\"\\n'",
		"        printf '    impact: high\\n'",
		"        printf '    confidence: 0.85\\n'",
		"      else",
		"        for f in \"${synth_findings[@]}\"; do",
		"          printf '  - finding: \"%s\"\\n' \"$f\"",
		"          printf '    impact: high\\n'",
		"          printf '    confidence: 0.89\\n'",
		"        done",
		"      fi",
		"      printf '\\n```\\n❯ \\n'",
		"      ;;",
		"  esac",
		"done",
		"sleep 30",
		"",
	}, "\n")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock agent script: %v", err)
	}

	t.Cleanup(func() {
		kill := exec.Command("tmux", "kill-server")
		_ = kill.Run()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			probe := exec.Command("tmux", "list-sessions")
			if err := probe.Run(); err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		ensemble.CloseDefaultStateStore()
	})

	logEnsembleTestJSON(logger, "isolated_tmux_ready", map[string]any{
		"tmux_dir":     tmuxDir,
		"response_dir": responseDir,
		"event_dir":    eventDir,
		"agent_script": scriptPath,
	})

	return &isolatedEnsembleTmuxEnv{
		TmuxDir:     tmuxDir,
		ResponseDir: responseDir,
		EventDir:    eventDir,
		AgentScript: scriptPath,
	}
}

func writeModeYAMLResponse(t *testing.T, responseDir, modeID, thesis, finding string, confidence float64) {
	t.Helper()
	body := fmt.Sprintf(strings.Join([]string{
		"mode_id: %s",
		"thesis: %q",
		"confidence: %.2f",
		"top_findings:",
		"  - finding: %q",
		"    impact: high",
		"    confidence: %.2f",
		"    evidence_pointer: \"internal/ensemble/pipeline.go:891\"",
		"    reasoning: %q",
		"",
	}, "\n"), modeID, thesis, confidence, finding, confidence, "Verified via live isolated tmux pane for "+modeID)
	path := filepath.Join(responseDir, modeID+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write mode YAML response %s: %v", path, err)
	}
}

func seedCompletedModeCheckpoint(t *testing.T, store *ensemble.CheckpointStore, runID, modeID, thesis, finding string, confidence float64, capturedAt time.Time) {
	t.Helper()
	out := &ensemble.ModeOutput{
		ModeID: modeID,
		Thesis: thesis,
		TopFindings: []ensemble.Finding{
			{
				Finding:         finding,
				Impact:          ensemble.ImpactHigh,
				Confidence:      ensemble.Confidence(confidence),
				EvidencePointer: "internal/cli/ensemble.go:3018",
				Reasoning:       "Seeded completed checkpoint for " + modeID,
			},
		},
		Confidence:  ensemble.Confidence(confidence),
		GeneratedAt: capturedAt,
	}
	cp := ensemble.ModeCheckpoint{
		ModeID:     modeID,
		Output:     out,
		Status:     string(ensemble.AssignmentDone),
		CapturedAt: capturedAt,
		TokensUsed: ensemble.EstimateModeOutputTokens(out),
	}
	if err := store.SaveCheckpoint(runID, cp); err != nil {
		t.Fatalf("SaveCheckpoint(%s, %s) error = %v", runID, modeID, err)
	}
}

func executeEnsembleCLICmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prevJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = prevJSON })

	root := &cobra.Command{
		Use:           "ntm",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	root.AddCommand(newEnsembleCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

func reportContainsFinding(report *ensemble.SynthesisReport, substring string) bool {
	if report == nil {
		return false
	}
	for _, f := range report.TopFindings {
		if strings.Contains(f.Finding, substring) {
			return true
		}
	}
	return false
}

func TestEnsembleResumeAndRerunModeRealTmux(t *testing.T) {
	logger := testutil.NewTestLogger(t, filepath.Join(t.TempDir(), "logs"))
	env := setupIsolatedEnsembleTmuxEnv(t, logger)

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("# Ensemble Resume Integration Test\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	oldCfg := cfg
	testCfg := config.Default()
	testCfg.Agents.Claude = env.AgentScript
	cfg = testCfg
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir projectDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	store, err := newEnsembleCheckpointStoreForProject(projectDir)
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForProject() error = %v", err)
	}

	const (
		runID       = "run-real-3mode"
		sessionName = "ens-real-resume"
	)
	seedTime := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Millisecond)

	logger.LogSection("1. Seed 3-mode ensemble run (2 completed, 1 pending)")
	meta := ensemble.CheckpointMetadata{
		RunID:        runID,
		SessionName:  sessionName,
		Question:     "What reliability risks remain in ensemble checkpoint recovery?",
		Status:       ensemble.EnsembleActive,
		CreatedAt:    seedTime,
		CompletedIDs: []string{"deductive", "inductive"},
		PendingIDs:   []string{"systems-thinking"},
		TotalModes:   3,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	seedCompletedModeCheckpoint(
		t, store, runID,
		"deductive",
		"Deductive initial thesis",
		"Deductive seeded finding alpha",
		0.88,
		seedTime,
	)
	seedCompletedModeCheckpoint(
		t, store, runID,
		"inductive",
		"Inductive initial thesis",
		"Inductive seeded finding beta",
		0.84,
		seedTime,
	)
	writeModeYAMLResponse(
		t, env.ResponseDir,
		"systems-thinking",
		"Systems-thinking live thesis",
		"Systems-thinking live tmux finding gamma",
		0.91,
	)
	logEnsembleTestJSON(logger, "seeded_checkpoint_run", map[string]any{
		"run_id":        runID,
		"session":       sessionName,
		"completed_ids": meta.CompletedIDs,
		"pending_ids":   meta.PendingIDs,
	})

	logger.LogSection("2. Execute `ensemble resume <run-id> --json`")
	resumeStdout, err := executeEnsembleCLICmd(t, "ensemble", "resume", runID, "--timeout=20s", "--json")
	logEnsembleTestJSON(logger, "resume_cli_completed", map[string]any{
		"err":    fmt.Sprintf("%v", err),
		"stdout": resumeStdout,
	})
	if err != nil {
		t.Fatalf("ensemble resume error = %v\nstdout:\n%s", err, resumeStdout)
	}

	var resumePayload checkpointResumeOutput
	if err := json.Unmarshal([]byte(resumeStdout), &resumePayload); err != nil {
		t.Fatalf("unmarshal resume JSON: %v\nstdout:\n%s", err, resumeStdout)
	}
	if !resumePayload.Success {
		t.Fatalf("expected resume success, got %+v", resumePayload)
	}
	if resumePayload.Status != string(ensemble.EnsembleComplete) {
		t.Fatalf("resume status = %q, want %q", resumePayload.Status, ensemble.EnsembleComplete)
	}
	if resumePayload.Resumed != 1 || resumePayload.Skipped != 2 {
		t.Fatalf("resume counts resumed=%d skipped=%d, want resumed=1 skipped=2", resumePayload.Resumed, resumePayload.Skipped)
	}
	if len(resumePayload.ModesExecuted) != 1 || resumePayload.ModesExecuted[0] != "systems-thinking" {
		t.Fatalf("ModesExecuted = %v, want [systems-thinking]", resumePayload.ModesExecuted)
	}
	if !resumePayload.Synthesized || resumePayload.Synthesis == nil {
		t.Fatalf("expected Synthesis report in resume payload, got Synthesized=%v Synthesis=%v", resumePayload.Synthesized, resumePayload.Synthesis)
	}

	if !tmux.SessionExists(sessionName) {
		t.Fatalf("expected tmux session %q to exist in isolated server", sessionName)
	}
	panesAfterResume, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("tmux.GetPanes(%q) error = %v", sessionName, err)
	}
	if len(panesAfterResume) == 0 {
		t.Fatalf("expected live panes in tmux session %q", sessionName)
	}
	if _, err := os.Stat(filepath.Join(env.EventDir, "systems-thinking.runs")); err != nil {
		t.Fatalf("expected mock agent invocation log for systems-thinking in tmux: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.EventDir, "deductive.runs")); !os.IsNotExist(err) {
		t.Fatalf("did not expect completed mode deductive to run during --skip-done resume, stat err=%v", err)
	}

	thirdCP, err := store.LoadCheckpoint(runID, "systems-thinking")
	if err != nil {
		t.Fatalf("LoadCheckpoint(systems-thinking) error = %v", err)
	}
	if thirdCP.Status != string(ensemble.AssignmentDone) || thirdCP.Output == nil {
		t.Fatalf("systems-thinking checkpoint = %+v, want done with output", thirdCP)
	}
	if len(thirdCP.Output.TopFindings) == 0 || thirdCP.Output.TopFindings[0].Finding != "Systems-thinking live tmux finding gamma" {
		t.Fatalf("systems-thinking output findings = %+v", thirdCP.Output.TopFindings)
	}

	metaAfterResume, err := store.LoadMetadata(runID)
	if err != nil {
		t.Fatalf("LoadMetadata() after resume error = %v", err)
	}
	if metaAfterResume.Status != ensemble.EnsembleComplete {
		t.Fatalf("_meta.json status = %q, want %q", metaAfterResume.Status, ensemble.EnsembleComplete)
	}
	if len(metaAfterResume.PendingIDs) != 0 || len(metaAfterResume.ErrorIDs) != 0 || len(metaAfterResume.CompletedIDs) != 3 {
		t.Fatalf("_meta.json after resume = %+v, want 3 completed and 0 pending/errors", metaAfterResume)
	}

	diskSynthAfterResume, err := store.LoadSynthesisReport(runID)
	if err != nil {
		t.Fatalf("LoadSynthesisReport() after resume error = %v", err)
	}
	for _, expectedFinding := range []string{
		"Deductive seeded finding alpha",
		"Inductive seeded finding beta",
		"Systems-thinking live tmux finding gamma",
	} {
		if !reportContainsFinding(resumePayload.Synthesis, expectedFinding) {
			t.Fatalf("resume JSON synthesis missing %q; findings=%+v", expectedFinding, resumePayload.Synthesis.TopFindings)
		}
		if !reportContainsFinding(diskSynthAfterResume, expectedFinding) {
			t.Fatalf("persisted synthesis.json missing %q; findings=%+v", expectedFinding, diskSynthAfterResume.TopFindings)
		}
	}
	logEnsembleTestJSON(logger, "resume_verified", map[string]any{
		"meta_status":     metaAfterResume.Status,
		"completed_modes": metaAfterResume.CompletedIDs,
		"findings_count":  len(diskSynthAfterResume.TopFindings),
		"tmux_panes":      len(panesAfterResume),
	})

	logger.LogSection("3. Execute `ensemble rerun-mode <run-id> deductive --synthesize --json`")
	writeModeYAMLResponse(
		t, env.ResponseDir,
		"deductive",
		"Deductive rerun updated thesis",
		"Deductive rerun updated finding delta",
		0.95,
	)

	rerunStdout, err := executeEnsembleCLICmd(t, "ensemble", "rerun-mode", runID, "deductive", "--synthesize", "--timeout=20s", "--json")
	logEnsembleTestJSON(logger, "rerun_mode_cli_completed", map[string]any{
		"err":    fmt.Sprintf("%v", err),
		"stdout": rerunStdout,
	})
	if err != nil {
		t.Fatalf("ensemble rerun-mode error = %v\nstdout:\n%s", err, rerunStdout)
	}

	var rerunPayload checkpointResumeOutput
	if err := json.Unmarshal([]byte(rerunStdout), &rerunPayload); err != nil {
		t.Fatalf("unmarshal rerun-mode JSON: %v\nstdout:\n%s", err, rerunStdout)
	}
	if !rerunPayload.Success {
		t.Fatalf("expected rerun-mode success, got %+v", rerunPayload)
	}
	if rerunPayload.ModeID != "deductive" {
		t.Fatalf("rerun ModeID = %q, want deductive", rerunPayload.ModeID)
	}
	if !rerunPayload.Synthesized || rerunPayload.Synthesis == nil {
		t.Fatalf("expected updated synthesis in rerun-mode payload, got %+v", rerunPayload)
	}
	if _, err := os.Stat(filepath.Join(env.EventDir, "deductive.runs")); err != nil {
		t.Fatalf("expected mock agent invocation log for deductive rerun: %v", err)
	}

	deductiveCPAfterRerun, err := store.LoadCheckpoint(runID, "deductive")
	if err != nil {
		t.Fatalf("LoadCheckpoint(deductive) after rerun error = %v", err)
	}
	if deductiveCPAfterRerun.Output == nil || len(deductiveCPAfterRerun.Output.TopFindings) == 0 {
		t.Fatalf("deductive checkpoint output nil/empty after rerun: %+v", deductiveCPAfterRerun)
	}
	if got := deductiveCPAfterRerun.Output.TopFindings[0].Finding; got != "Deductive rerun updated finding delta" {
		t.Fatalf("deductive checkpoint finding = %q, want %q", got, "Deductive rerun updated finding delta")
	}
	if !deductiveCPAfterRerun.CapturedAt.After(seedTime) {
		t.Fatalf("expected deductive CapturedAt (%v) to advance past seedTime (%v)", deductiveCPAfterRerun.CapturedAt, seedTime)
	}

	diskSynthAfterRerun, err := store.LoadSynthesisReport(runID)
	if err != nil {
		t.Fatalf("LoadSynthesisReport() after rerun error = %v", err)
	}
	if !reportContainsFinding(rerunPayload.Synthesis, "Deductive rerun updated finding delta") {
		t.Fatalf("rerun JSON synthesis missing updated deductive finding: %+v", rerunPayload.Synthesis.TopFindings)
	}
	if !reportContainsFinding(diskSynthAfterRerun, "Deductive rerun updated finding delta") {
		t.Fatalf("persisted synthesis.json missing updated deductive finding: %+v", diskSynthAfterRerun.TopFindings)
	}
	if reportContainsFinding(diskSynthAfterRerun, "Deductive seeded finding alpha") {
		t.Fatalf("persisted synthesis.json still contains stale finding alpha: %+v", diskSynthAfterRerun.TopFindings)
	}
	logEnsembleTestJSON(logger, "rerun_mode_verified", map[string]any{
		"mode_id":         rerunPayload.ModeID,
		"captured_at":     deductiveCPAfterRerun.CapturedAt,
		"findings_count":  len(diskSynthAfterRerun.TopFindings),
		"updated_finding": deductiveCPAfterRerun.Output.TopFindings[0].Finding,
	})
}

func TestEnsembleTOMLConfigKeys_AllSixAndCLIOverrides(t *testing.T) {
	logger := testutil.NewTestLogger(t, filepath.Join(t.TempDir(), "logs"))

	tomlCfg := config.Default()
	tomlCfg.Ensemble.Synthesis.Strategy = "consensus"
	tomlCfg.Ensemble.Synthesis.MinConfidence = 0.70
	tomlCfg.Ensemble.Synthesis.MaxFindings = 1
	tomlCfg.Ensemble.Synthesis.IncludeRawOutputs = true
	tomlCfg.Ensemble.Synthesis.ConflictResolution = "drop"
	tomlCfg.Ensemble.Budget.PerAgent = 3200
	tomlCfg.Ensemble.Budget.Total = 24000
	tomlCfg.Ensemble.Budget.Synthesis = 75
	tomlCfg.Ensemble.Budget.ContextPack = 95

	oldCfg := cfg
	cfg = tomlCfg
	t.Cleanup(func() { cfg = oldCfg })

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte(strings.Repeat("ensemble context line for token cap verification\n", 80)), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	t.Run("SpawnDryRun_InheritsAllSixTOMLKeysAndCLIOverrides", func(t *testing.T) {
		stdoutTOML, err := executeEnsembleCLICmd(
			t,
			"ensemble", "spawn", "cfg-dryrun-toml",
			"--question=Verify all six TOML keys",
			"--modes=deductive,inductive",
			"--project="+projectDir,
			"--dry-run",
			"--json",
		)
		if err != nil {
			t.Fatalf("spawn --dry-run (TOML defaults) error = %v\nstdout:\n%s", err, stdoutTOML)
		}
		var planTOML ensembleDryRunOutput
		if err := json.Unmarshal([]byte(stdoutTOML), &planTOML); err != nil {
			t.Fatalf("unmarshal dry-run TOML JSON: %v\nstdout:\n%s", err, stdoutTOML)
		}
		if planTOML.Synthesis.MinConfidence != 0.70 {
			t.Fatalf("MinConfidence = %v, want 0.70", planTOML.Synthesis.MinConfidence)
		}
		if planTOML.Synthesis.MaxFindings != 1 {
			t.Fatalf("MaxFindings = %d, want 1", planTOML.Synthesis.MaxFindings)
		}
		if !planTOML.Synthesis.IncludeRawOutputs {
			t.Fatalf("IncludeRawOutputs = false, want true")
		}
		if planTOML.Synthesis.ConflictResolution != "drop" {
			t.Fatalf("ConflictResolution = %q, want %q", planTOML.Synthesis.ConflictResolution, "drop")
		}
		if planTOML.Budget.SynthesisReserveTokens != 75 {
			t.Fatalf("SynthesisReserveTokens = %d, want 75", planTOML.Budget.SynthesisReserveTokens)
		}
		if planTOML.Budget.ContextReserveTokens != 95 {
			t.Fatalf("ContextReserveTokens = %d, want 95", planTOML.Budget.ContextReserveTokens)
		}

		stdoutCLI, err := executeEnsembleCLICmd(
			t,
			"ensemble", "spawn", "cfg-dryrun-cli",
			"--question=Verify CLI overrides all six TOML keys",
			"--modes=deductive,inductive",
			"--project="+projectDir,
			"--min-confidence=0.35",
			"--max-findings=8",
			"--include-raw-outputs=false",
			"--conflict-resolution=highlight",
			"--budget-synthesis=1800",
			"--budget-context-pack=900",
			"--dry-run",
			"--json",
		)
		if err != nil {
			t.Fatalf("spawn --dry-run (CLI overrides) error = %v\nstdout:\n%s", err, stdoutCLI)
		}
		var planCLI ensembleDryRunOutput
		if err := json.Unmarshal([]byte(stdoutCLI), &planCLI); err != nil {
			t.Fatalf("unmarshal dry-run CLI JSON: %v\nstdout:\n%s", err, stdoutCLI)
		}
		if planCLI.Synthesis.MinConfidence != 0.35 {
			t.Fatalf("CLI override MinConfidence = %v, want 0.35", planCLI.Synthesis.MinConfidence)
		}
		if planCLI.Synthesis.MaxFindings != 8 {
			t.Fatalf("CLI override MaxFindings = %d, want 8", planCLI.Synthesis.MaxFindings)
		}
		if planCLI.Synthesis.IncludeRawOutputs {
			t.Fatalf("CLI override IncludeRawOutputs = true, want false")
		}
		if planCLI.Synthesis.ConflictResolution != "highlight" {
			t.Fatalf("CLI override ConflictResolution = %q, want %q", planCLI.Synthesis.ConflictResolution, "highlight")
		}
		if planCLI.Budget.SynthesisReserveTokens != 1800 {
			t.Fatalf("CLI override SynthesisReserveTokens = %d, want 1800", planCLI.Budget.SynthesisReserveTokens)
		}
		if planCLI.Budget.ContextReserveTokens != 900 {
			t.Fatalf("CLI override ContextReserveTokens = %d, want 900", planCLI.Budget.ContextReserveTokens)
		}
		logEnsembleTestJSON(logger, "spawn_dryrun_toml_and_cli_verified", map[string]any{
			"toml_synthesis": planTOML.Synthesis,
			"toml_budget":    planTOML.Budget,
			"cli_synthesis":  planCLI.Synthesis,
			"cli_budget":     planCLI.Budget,
		})
	})

	t.Run("Estimate_InheritsBudgetReservesAndCLIOverrides", func(t *testing.T) {
		estTOMLOut, err := executeEnsembleCLICmd(
			t,
			"ensemble", "estimate",
			"--modes=deductive,inductive",
			"--format=json",
		)
		if err != nil {
			t.Fatalf("estimate (TOML) error = %v\nstdout:\n%s", err, estTOMLOut)
		}
		var estTOML ensembleEstimateOutput
		if err := json.Unmarshal([]byte(estTOMLOut), &estTOML); err != nil {
			t.Fatalf("unmarshal estimate TOML JSON: %v\nstdout:\n%s", err, estTOMLOut)
		}
		if estTOML.Budget.SynthesisReserveTokens != 75 {
			t.Fatalf("estimate SynthesisReserveTokens = %d, want 75", estTOML.Budget.SynthesisReserveTokens)
		}
		if estTOML.Budget.ContextReserveTokens != 95 {
			t.Fatalf("estimate ContextReserveTokens = %d, want 95", estTOML.Budget.ContextReserveTokens)
		}

		estCLIOut, err := executeEnsembleCLICmd(
			t,
			"ensemble", "estimate",
			"--modes=deductive,inductive",
			"--budget-synthesis=1600",
			"--budget-context-pack=400",
			"--format=json",
		)
		if err != nil {
			t.Fatalf("estimate (CLI override) error = %v\nstdout:\n%s", err, estCLIOut)
		}
		var estCLI ensembleEstimateOutput
		if err := json.Unmarshal([]byte(estCLIOut), &estCLI); err != nil {
			t.Fatalf("unmarshal estimate CLI JSON: %v\nstdout:\n%s", err, estCLIOut)
		}
		if estCLI.Budget.SynthesisReserveTokens != 1600 {
			t.Fatalf("estimate CLI SynthesisReserveTokens = %d, want 1600", estCLI.Budget.SynthesisReserveTokens)
		}
		if estCLI.Budget.ContextReserveTokens != 400 {
			t.Fatalf("estimate CLI ContextReserveTokens = %d, want 400", estCLI.Budget.ContextReserveTokens)
		}
	})

	t.Run("SynthesizeAndContextPack_EnforcesAllSixTOMLKeysAndCLIOverrides", func(t *testing.T) {
		isolateSessionAgentStorage(t)
		ensemble.CloseDefaultStateStore()
		t.Cleanup(ensemble.CloseDefaultStateStore)

		gen := ensemble.NewContextPackGenerator(projectDir, nil, nil)
		gen.MaxTokens = cfg.Ensemble.Budget.ContextPack
		pack, err := gen.Generate("Verify context pack token cap", "", ensemble.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("ContextPackGenerator.Generate() error = %v", err)
		}
		if pack.TokenEstimate > cfg.Ensemble.Budget.ContextPack {
			t.Fatalf("ContextPack.TokenEstimate = %d, want <= %d", pack.TokenEstimate, cfg.Ensemble.Budget.ContextPack)
		}

		outDir := t.TempDir()
		out1Path := filepath.Join(outDir, "deductive.json")
		out2Path := filepath.Join(outDir, "inductive.json")
		now := time.Now().UTC()

		out1 := ensemble.ModeOutput{
			ModeID: "deductive",
			Thesis: "We must strictly enforce synchronous schema validation on every checkpoint write.",
			TopFindings: []ensemble.Finding{
				{
					Finding:    "High confidence finding one",
					Impact:     ensemble.ImpactHigh,
					Confidence: 0.94,
					Reasoning:  strings.Repeat("detailed deductive proof ", 8),
				},
				{
					Finding:    "Low confidence finding filtered by TOML min_confidence",
					Impact:     ensemble.ImpactMedium,
					Confidence: 0.45,
					Reasoning:  "tentative signal",
				},
			},
			Risks: []ensemble.Risk{
				{Risk: "Schema drift risk", Impact: ensemble.ImpactHigh, Likelihood: 0.8},
			},
			Recommendations: []ensemble.Recommendation{
				{Recommendation: "Validate checkpoint JSON atomically", Priority: ensemble.ImpactHigh},
			},
			Confidence:  0.92,
			RawOutput:   "RAW_DEDUCTIVE_PANE_CAPTURE",
			GeneratedAt: now,
		}
		out2 := ensemble.ModeOutput{
			ModeID: "inductive",
			Thesis: "We must never enforce synchronous schema validation; reject blocking writes completely.",
			TopFindings: []ensemble.Finding{
				{
					Finding:    "Second high confidence finding capped when max_findings=1",
					Impact:     ensemble.ImpactHigh,
					Confidence: 0.82,
					Reasoning:  strings.Repeat("detailed inductive critique ", 8),
				},
			},
			Confidence:  0.82,
			RawOutput:   "RAW_INDUCTIVE_PANE_CAPTURE",
			GeneratedAt: now,
		}

		for path, val := range map[string]ensemble.ModeOutput{out1Path: out1, out2Path: out2} {
			raw, marshalErr := json.Marshal(val)
			if marshalErr != nil {
				t.Fatalf("marshal %s: %v", path, marshalErr)
			}
			if writeErr := os.WriteFile(path, raw, 0o644); writeErr != nil {
				t.Fatalf("write %s: %v", path, writeErr)
			}
		}

		const synthSession = "offline-toml-synth-session"
		state := &ensemble.EnsembleSession{
			SessionName:       synthSession,
			Question:          "Should checkpoint writes enforce synchronous validation?",
			Status:            ensemble.EnsembleStopped,
			SynthesisStrategy: ensemble.StrategyConsensus,
			CreatedAt:         now,
			Assignments: []ensemble.ModeAssignment{
				{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: out1Path},
				{ModeID: "inductive", PaneName: "pane-2", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: out2Path},
			},
		}
		if err := ensemble.SaveSession("", state); err != nil {
			t.Fatalf("SaveSession() error = %v", err)
		}

		type synthesizeJSONEnvelope struct {
			Synthesis *ensemble.SynthesisResult `json:"synthesis"`
			Audit     *ensemble.AuditReport     `json:"audit"`
		}

		// 1. Unset CLI flags -> TOML settings govern synthesis behavior.
		tomlSynthStdout, err := executeEnsembleCLICmd(t, "ensemble", "synthesize", synthSession, "--format=json")
		if err != nil {
			t.Fatalf("synthesize (TOML defaults) error = %v\nstdout:\n%s", err, tomlSynthStdout)
		}
		var tomlEnv synthesizeJSONEnvelope
		if err := json.Unmarshal([]byte(tomlSynthStdout), &tomlEnv); err != nil {
			t.Fatalf("unmarshal synthesize TOML JSON: %v\nstdout:\n%s", err, tomlSynthStdout)
		}
		if tomlEnv.Synthesis == nil {
			t.Fatalf("expected non-nil Synthesis in TOML synthesis result")
		}
		// max_findings = 1 and min_confidence = 0.70 -> exactly 1 finding ("High confidence finding one").
		if len(tomlEnv.Synthesis.Findings) != 1 {
			t.Fatalf("TOML Findings len = %d, want 1 (max_findings=1); findings=%+v", len(tomlEnv.Synthesis.Findings), tomlEnv.Synthesis.Findings)
		}
		if !strings.Contains(tomlEnv.Synthesis.Findings[0].Finding, "High confidence finding one") {
			t.Fatalf("TOML Findings[0] = %q, want High confidence finding one", tomlEnv.Synthesis.Findings[0].Finding)
		}
		// include_raw_outputs = true -> RawOutputs populated for both modes (truncated to fit budget.synthesis = 75).
		if len(tomlEnv.Synthesis.RawOutputs) != 2 || !strings.Contains(tomlEnv.Synthesis.RawOutputs["deductive"], "deductive") {
			t.Fatalf("TOML RawOutputs = %+v, want 2 raw mode outputs", tomlEnv.Synthesis.RawOutputs)
		}
		// conflict_resolution = "drop" -> audit conflicts dropped.
		if tomlEnv.Audit != nil && len(tomlEnv.Audit.Conflicts) != 0 {
			t.Fatalf("TOML Audit.Conflicts = %+v, want empty when conflict_resolution=drop", tomlEnv.Audit.Conflicts)
		}
		// budget.synthesis = 75 -> TokensUsed <= 75.
		if tomlEnv.Synthesis.TokensUsed > 75 {
			t.Fatalf("TOML TokensUsed = %d, want <= 75 (budget.synthesis)", tomlEnv.Synthesis.TokensUsed)
		}

		// 2. Explicit CLI flags -> override all 6 TOML keys!
		cliSynthStdout, err := executeEnsembleCLICmd(
			t,
			"ensemble", "synthesize", synthSession,
			"--format=json",
			"--min-confidence=0.40",
			"--max-findings=5",
			"--include-raw-outputs=false",
			"--conflict-resolution=highlight",
			"--budget-synthesis=5000",
			"--budget-context-pack=1500",
		)
		if err != nil {
			t.Fatalf("synthesize (CLI overrides) error = %v\nstdout:\n%s", err, cliSynthStdout)
		}
		var cliEnv synthesizeJSONEnvelope
		if err := json.Unmarshal([]byte(cliSynthStdout), &cliEnv); err != nil {
			t.Fatalf("unmarshal synthesize CLI JSON: %v\nstdout:\n%s", err, cliSynthStdout)
		}
		if cliEnv.Synthesis == nil {
			t.Fatalf("expected non-nil Synthesis in CLI synthesis result")
		}
		if len(cliEnv.Synthesis.Findings) != 3 {
			t.Fatalf("CLI override Findings len = %d, want 3 (0.94, 0.82, 0.45); findings=%+v", len(cliEnv.Synthesis.Findings), cliEnv.Synthesis.Findings)
		}
		if len(cliEnv.Synthesis.RawOutputs) != 0 {
			t.Fatalf("CLI override RawOutputs = %+v, want empty when --include-raw-outputs=false", cliEnv.Synthesis.RawOutputs)
		}
		if cliEnv.Audit == nil || len(cliEnv.Audit.Conflicts) == 0 {
			t.Fatalf("CLI override Audit.Conflicts = %+v, expected conflict retained when --conflict-resolution=highlight", cliEnv.Audit)
		}
		if cliEnv.Synthesis.TokensUsed <= 75 {
			t.Fatalf("CLI override TokensUsed = %d, expected uncapped (> 75) when --budget-synthesis=5000", cliEnv.Synthesis.TokensUsed)
		}
		logEnsembleTestJSON(logger, "synthesize_toml_and_cli_verified", map[string]any{
			"toml_findings":    len(tomlEnv.Synthesis.Findings),
			"toml_tokens_used": tomlEnv.Synthesis.TokensUsed,
			"cli_findings":     len(cliEnv.Synthesis.Findings),
			"cli_tokens_used":  cliEnv.Synthesis.TokensUsed,
		})
	})
}
