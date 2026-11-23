package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

type ensembleFailureWriter struct {
	err error
}

func (w ensembleFailureWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRenderCheckpointResumeFailureOutputPreservesCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("checkpoint metadata unavailable")
	payload := checkpointResumeOutput{
		GeneratedAt: time.Now().UTC(),
		RunID:       "run-1",
		Success:     false,
		Error:       cause.Error(),
	}
	var buf bytes.Buffer
	err := renderCheckpointResumeFailureOutput(&buf, payload, "json", false, cause)
	if !errors.Is(err, errJSONFailure) || !errors.Is(err, cause) {
		t.Fatalf("render error = %v, want terminal sentinel and original cause", err)
	}

	var decoded checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode exactly one checkpoint failure document: %v; output=%q", err, buf.String())
	}
	if decoded.Success || decoded.Error != cause.Error() {
		t.Fatalf("checkpoint failure payload = %+v", decoded)
	}
}

func TestRenderCheckpointResumeFailureOutputSurfacesEncoderError(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("writer closed")
	cause := errors.New("checkpoint failed")
	err := renderCheckpointResumeFailureOutput(ensembleFailureWriter{err: writeErr}, checkpointResumeOutput{
		RunID:   "run-1",
		Success: false,
		Error:   cause.Error(),
	}, "json", false, cause)
	if !errors.Is(err, writeErr) {
		t.Fatalf("render error = %v, want encoder error", err)
	}
	if errors.Is(err, errJSONFailure) {
		t.Fatalf("render error = %v, must not claim an emitted failure document", err)
	}
}

func TestNewEnsembleCompareCmd(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestNewEnsembleCompareCmd - starting")

	cmd := newEnsembleCompareCmd()

	if cmd == nil {
		t.Fatal("newEnsembleCompareCmd returned nil")
	}
	if cmd.Use != "compare <run1> <run2>" {
		t.Errorf("expected Use='compare <run1> <run2>', got %q", cmd.Use)
	}

	// Check flags exist
	formatFlag := cmd.Flags().Lookup("format")
	if formatFlag == nil {
		t.Error("expected --format flag")
	} else if formatFlag.DefValue != "text" {
		t.Errorf("expected --format default='text', got %q", formatFlag.DefValue)
	}

	verboseFlag := cmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Error("expected --verbose flag")
	}

	t.Log("TEST: TestNewEnsembleCompareCmd - assertion: command created with correct flags")
}

func TestWriteCompareResult_JSON(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestWriteCompareResult_JSON - starting")

	result := &ensemble.ComparisonResult{
		RunA:        "session-a",
		RunB:        "session-b",
		GeneratedAt: time.Now(),
		ModeDiff: ensemble.ModeDiff{
			Added:          []string{"mode-c"},
			Removed:        []string{"mode-d"},
			Unchanged:      []string{"mode-a", "mode-b"},
			AddedCount:     1,
			RemovedCount:   1,
			UnchangedCount: 2,
		},
		FindingsDiff: ensemble.FindingsDiff{
			NewCount:       2,
			MissingCount:   1,
			ChangedCount:   0,
			UnchangedCount: 3,
		},
		Summary: "+1 modes, -1 modes, +2 findings, -1 findings",
	}

	var buf bytes.Buffer
	opts := compareOptions{Verbose: false}
	err := writeCompareResult(&buf, result, opts, "json")

	if err != nil {
		t.Fatalf("writeCompareResult returned error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: TestWriteCompareResult_JSON - output: %s", output)

	// Parse JSON to validate structure
	var parsed compareOutput
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if parsed.RunA != "session-a" {
		t.Errorf("expected RunA='session-a', got %q", parsed.RunA)
	}
	if parsed.RunB != "session-b" {
		t.Errorf("expected RunB='session-b', got %q", parsed.RunB)
	}
	if parsed.Result == nil {
		t.Error("expected Result to be present")
	}
	if parsed.Result.ModeDiff.AddedCount != 1 {
		t.Errorf("expected AddedCount=1, got %d", parsed.Result.ModeDiff.AddedCount)
	}
	if !parsed.Success {
		t.Error("expected successful compare output to set success=true")
	}

	t.Log("TEST: TestWriteCompareResult_JSON - assertion: JSON output is valid")
}

func TestWriteCompareResult_Text(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestWriteCompareResult_Text - starting")

	result := &ensemble.ComparisonResult{
		RunA:        "run-alpha",
		RunB:        "run-beta",
		GeneratedAt: time.Now(),
		ModeDiff: ensemble.ModeDiff{
			Added:          []string{"new-mode"},
			Removed:        []string{},
			Unchanged:      []string{"existing-mode"},
			AddedCount:     1,
			RemovedCount:   0,
			UnchangedCount: 1,
		},
		FindingsDiff: ensemble.FindingsDiff{
			NewCount:       1,
			MissingCount:   0,
			ChangedCount:   0,
			UnchangedCount: 2,
		},
		Summary: "+1 modes, +1 findings",
	}

	var buf bytes.Buffer
	opts := compareOptions{Verbose: false}
	err := writeCompareResult(&buf, result, opts, "text")

	if err != nil {
		t.Fatalf("writeCompareResult returned error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: TestWriteCompareResult_Text - output:\n%s", output)

	// Check key sections are present
	if !strings.Contains(output, "run-alpha") {
		t.Error("expected output to contain run-alpha")
	}
	if !strings.Contains(output, "run-beta") {
		t.Error("expected output to contain run-beta")
	}
	if !strings.Contains(output, "Mode Changes") {
		t.Error("expected output to contain Mode Changes section")
	}
	if !strings.Contains(output, "Finding Changes") {
		t.Error("expected output to contain Finding Changes section")
	}

	t.Log("TEST: TestWriteCompareResult_Text - assertion: text output is well-formed")
}

func TestWriteCompareResult_Text_Verbose(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestWriteCompareResult_Text_Verbose - starting")

	result := &ensemble.ComparisonResult{
		RunA:        "run-alpha",
		RunB:        "run-beta",
		GeneratedAt: time.Now(),
		ModeDiff: ensemble.ModeDiff{
			Added:          []string{"new-mode"},
			Removed:        []string{},
			Unchanged:      []string{"existing-mode", "another-mode"},
			AddedCount:     1,
			RemovedCount:   0,
			UnchangedCount: 2,
		},
		FindingsDiff: ensemble.FindingsDiff{
			NewCount:       1,
			MissingCount:   0,
			ChangedCount:   0,
			UnchangedCount: 2,
			Unchanged: []ensemble.FindingDiffEntry{
				{ModeID: "existing-mode", Text: "unchanged finding one"},
				{ModeID: "another-mode", Text: "unchanged finding two"},
			},
		},
		ContributionDiff: ensemble.ContributionDiff{
			OverlapRateA:    0.25,
			OverlapRateB:    0.30,
			DiversityScoreA: 0.75,
			DiversityScoreB: 0.80,
			ScoreDeltas: []ensemble.ScoreDelta{
				{ModeID: "existing-mode", ScoreA: 0.5, ScoreB: 0.6, Delta: 0.1},
			},
		},
		Summary: "+1 modes, +1 findings",
	}

	var buf bytes.Buffer
	opts := compareOptions{Verbose: true}
	err := writeCompareResult(&buf, result, opts, "text")

	if err != nil {
		t.Fatalf("writeCompareResult returned error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: TestWriteCompareResult_Text_Verbose - output:\n%s", output)

	// Check verbose details are present
	if !strings.Contains(output, "Verbose Details") {
		t.Error("expected output to contain Verbose Details section")
	}
	if !strings.Contains(output, "Unchanged Modes") {
		t.Error("expected output to contain Unchanged Modes")
	}
	if !strings.Contains(output, "existing-mode") {
		t.Error("expected output to contain unchanged mode name")
	}
	if !strings.Contains(output, "Unchanged Findings") {
		t.Error("expected output to contain Unchanged Findings")
	}
	if !strings.Contains(output, "Contribution Score Changes") {
		t.Error("expected output to contain Contribution Score Changes")
	}
	if !strings.Contains(output, "Overlap Rate") {
		t.Error("expected output to contain Overlap Rate")
	}
	if !strings.Contains(output, "Diversity Score") {
		t.Error("expected output to contain Diversity Score")
	}

	t.Log("TEST: TestWriteCompareResult_Text_Verbose - assertion: verbose output is complete")
}

func TestNewEnsembleCheckpointStoreForSessionUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	sessionProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(sessionProject, 0o755); err != nil {
		t.Fatalf("mkdir session project: %v", err)
	}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir cwd ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdProject, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	nestedDir := filepath.Join(cwdProject, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStoreForSession(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForSession() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:       "session-scoped-run",
		SessionName: "mysession",
		Question:    "where should this checkpoint go?",
		Status:      ensemble.EnsembleActive,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}

	sessionMetaPath := filepath.Join(sessionProject, ".ntm", "ensemble-checkpoints", meta.RunID, "_meta.json")
	if _, err := os.Stat(sessionMetaPath); err != nil {
		t.Fatalf("expected metadata under session project, got stat error: %v", err)
	}

	cwdMetaPath := filepath.Join(cwdProject, ".ntm", "ensemble-checkpoints", meta.RunID, "_meta.json")
	if _, err := os.Stat(cwdMetaPath); !os.IsNotExist(err) {
		t.Fatalf("expected no metadata under cwd project, stat err = %v", err)
	}
}

func TestRunEnsembleResumeUsesConfiguredRunProjectWhenCWDDiffers(t *testing.T) {
	env := setupIsolatedEnsembleTmuxEnv(t, nil)

	projectsBase := t.TempDir()
	sessionProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(sessionProject, 0o755); err != nil {
		t.Fatalf("mkdir session project: %v", err)
	}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir cwd ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdProject, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	nestedDir := filepath.Join(cwdProject, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldCfg := cfg
	testCfg := config.Default()
	testCfg.ProjectsBase = projectsBase
	testCfg.Agents.Claude = env.AgentScript
	cfg = testCfg
	t.Cleanup(func() { cfg = oldCfg })

	store, err := newEnsembleCheckpointStoreForSession(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForSession() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "resume-cross-project-run",
		SessionName:  "mysession",
		Question:     "Resume me",
		Status:       ensemble.EnsembleActive,
		CompletedIDs: []string{"deductive"},
		PendingIDs:   []string{"inductive"},
		TotalModes:   2,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	seedCompletedModeCheckpoint(t, store, meta.RunID, "deductive", "Deductive thesis", "Deductive finding", 0.85, time.Now().UTC())

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleResume(t.Context(), nil, &buf, ensembleResumeCommandOptions{
		RunID:    meta.RunID,
		Format:   "json",
		Quiet:    false,
		SkipDone: true,
		Timeout:  20 * time.Second,
	}); err != nil {
		t.Fatalf("runEnsembleResume() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal resume output: %v", err)
	}
	if !payload.Success {
		t.Fatalf("expected success, got payload %+v", payload)
	}
	if payload.RunID != meta.RunID || payload.Session != meta.SessionName {
		t.Fatalf("payload = %+v, want run %q session %q", payload, meta.RunID, meta.SessionName)
	}
	if payload.Status != string(ensemble.EnsembleComplete) {
		t.Fatalf("Status = %q, want %q", payload.Status, ensemble.EnsembleComplete)
	}
}

func TestRunEnsembleResumeSkipDoneFalseIncludesCompletedModes(t *testing.T) {
	env := setupIsolatedEnsembleTmuxEnv(t, nil)

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldCfg := cfg
	testCfg := config.Default()
	testCfg.Agents.Claude = env.AgentScript
	cfg = testCfg
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStore() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "resume-include-completed-run",
		SessionName:  "mysession",
		Question:     "Resume me",
		Status:       ensemble.EnsembleActive,
		CompletedIDs: []string{"deductive"},
		PendingIDs:   []string{"inductive"},
		ErrorIDs:     []string{"systems-thinking"},
		TotalModes:   3,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	seedCompletedModeCheckpoint(t, store, meta.RunID, "deductive", "Deductive thesis", "Deductive finding", 0.85, time.Now().UTC())

	var buf bytes.Buffer
	if err := runEnsembleResume(t.Context(), nil, &buf, ensembleResumeCommandOptions{
		RunID:    meta.RunID,
		Format:   "json",
		Quiet:    false,
		SkipDone: false,
		Timeout:  25 * time.Second,
	}); err != nil {
		t.Fatalf("runEnsembleResume() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal resume output: %v", err)
	}
	if !payload.Success {
		t.Fatalf("expected success, got payload %+v", payload)
	}
	if payload.Resumed != 3 {
		t.Fatalf("Resumed = %d, want 3", payload.Resumed)
	}
	if payload.Skipped != 0 {
		t.Fatalf("Skipped = %d, want 0", payload.Skipped)
	}
}

func TestRunEnsembleResume_InvalidMetadataReturnsStructuredFailure(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm", "ensemble-checkpoints", "resume-invalid-meta-run"), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "ensemble-checkpoints", "resume-invalid-meta-run", "_meta.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write invalid metadata: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleResume(t.Context(), nil, &buf, ensembleResumeCommandOptions{
		RunID:    "resume-invalid-meta-run",
		Format:   "json",
		Quiet:    false,
		SkipDone: true,
	}); err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runEnsembleResume() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal resume output: %v", err)
	}
	if payload.Success {
		t.Fatalf("expected failure, got payload %+v", payload)
	}
	if !strings.Contains(payload.Error, "load checkpoint metadata:") {
		t.Fatalf("Error = %q, want structured metadata load failure", payload.Error)
	}
}

func TestRunEnsembleResume_InvalidRunIDReturnsStructuredFailure(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir ntm dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleResume(t.Context(), nil, &buf, ensembleResumeCommandOptions{
		RunID:    "../escape",
		Format:   "json",
		Quiet:    false,
		SkipDone: true,
	}); err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runEnsembleResume() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal resume output: %v", err)
	}
	if payload.Success {
		t.Fatalf("expected failure, got payload %+v", payload)
	}
	if !strings.Contains(payload.Error, "invalid run ID") {
		t.Fatalf("Error = %q, want invalid run ID failure", payload.Error)
	}
}

func TestRunEnsembleRerunModeUsesConfiguredRunProjectWhenCWDDiffers(t *testing.T) {
	env := setupIsolatedEnsembleTmuxEnv(t, nil)

	projectsBase := t.TempDir()
	sessionProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(sessionProject, 0o755); err != nil {
		t.Fatalf("mkdir session project: %v", err)
	}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir cwd ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdProject, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	nestedDir := filepath.Join(cwdProject, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldCfg := cfg
	testCfg := config.Default()
	testCfg.ProjectsBase = projectsBase
	testCfg.Agents.Claude = env.AgentScript
	cfg = testCfg
	t.Cleanup(func() { cfg = oldCfg })

	store, err := newEnsembleCheckpointStoreForSession(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForSession() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "rerun-cross-project-run",
		SessionName:  "mysession",
		Question:     "Rerun me",
		Status:       ensemble.EnsembleActive,
		CompletedIDs: []string{"deductive"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	seedCompletedModeCheckpoint(t, store, meta.RunID, "deductive", "Deductive thesis", "Deductive finding", 0.85, time.Now().UTC())

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleRerunMode(t.Context(), nil, &buf, ensembleRerunCommandOptions{
		RunID:   meta.RunID,
		ModeRef: "deductive",
		Format:  "json",
		Quiet:   false,
		Timeout: 20 * time.Second,
	}); err != nil {
		t.Fatalf("runEnsembleRerunMode() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal rerun output: %v", err)
	}
	if !payload.Success {
		t.Fatalf("expected success, got payload %+v", payload)
	}
	if payload.RunID != meta.RunID || payload.Session != meta.SessionName {
		t.Fatalf("payload = %+v, want run %q session %q", payload, meta.RunID, meta.SessionName)
	}
}

func TestRunEnsembleRerunModeRejectsUnknownModeInCheckpointRun(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStore() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "rerun-missing-mode-run",
		SessionName:  "mysession",
		Question:     "Rerun me",
		Status:       ensemble.EnsembleActive,
		CompletedIDs: []string{"deductive"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}

	var buf bytes.Buffer
	if err := runEnsembleRerunMode(t.Context(), nil, &buf, ensembleRerunCommandOptions{
		RunID:   meta.RunID,
		ModeRef: "bayesian",
		Format:  "json",
		Quiet:   false,
	}); err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runEnsembleRerunMode() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal rerun output: %v", err)
	}
	if payload.Success {
		t.Fatalf("expected failure, got payload %+v", payload)
	}
	if !strings.Contains(payload.Error, "not found in checkpoint run") {
		t.Fatalf("Error = %q, want checkpoint run mode-not-found message", payload.Error)
	}
}

func TestRunEnsembleRerunMode_InvalidMetadataReturnsStructuredFailure(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm", "ensemble-checkpoints", "rerun-invalid-meta-run"), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "ensemble-checkpoints", "rerun-invalid-meta-run", "_meta.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write invalid metadata: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleRerunMode(t.Context(), nil, &buf, ensembleRerunCommandOptions{
		RunID:   "rerun-invalid-meta-run",
		ModeRef: "deductive",
		Format:  "json",
		Quiet:   false,
	}); err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runEnsembleRerunMode() error = %v", err)
	}

	var payload checkpointResumeOutput
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal rerun output: %v", err)
	}
	if payload.Success {
		t.Fatalf("expected failure, got payload %+v", payload)
	}
	if !strings.Contains(payload.Error, "load checkpoint metadata:") {
		t.Fatalf("Error = %q, want structured metadata load failure", payload.Error)
	}
}

func TestLoadExportFindingsFromSession_UsesSavedOutputsWhenSessionOffline(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "export-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "mode-a",
		Thesis: "Export thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Export finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.7,
		}},
		Confidence:  0.7,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-export-session",
		Question:          "Export offline findings",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	ctx, err := loadExportFindingsFromSession(state.SessionName)
	if err != nil {
		t.Fatalf("loadExportFindingsFromSession() error = %v", err)
	}
	if ctx.Session != state.SessionName {
		t.Fatalf("Session = %q, want %q", ctx.Session, state.SessionName)
	}
	if len(ctx.Outputs) != 1 {
		t.Fatalf("len(Outputs) = %d, want 1", len(ctx.Outputs))
	}
	if ctx.Outputs[0].ModeID != "mode-a" {
		t.Fatalf("ModeID = %q, want %q", ctx.Outputs[0].ModeID, "mode-a")
	}
}

func TestRunEnsembleSynthesizeResumeUsesRunProjectForGenericSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	projectsBase := t.TempDir()
	sessionProject := filepath.Join(projectsBase, "session-project")
	if err := os.MkdirAll(sessionProject, 0o755); err != nil {
		t.Fatalf("mkdir session project: %v", err)
	}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir cwd ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdProject, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	nestedDir := filepath.Join(cwdProject, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	outputPath := filepath.Join(t.TempDir(), "resume-synth-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "mode-a",
		Thesis: "Resume synthesis thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Resume synthesis finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.8,
		}},
		Confidence:  0.8,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "review-session-prod",
		Question:          "Resume streaming synthesis",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	store, err := newEnsembleCheckpointStoreForProject(sessionProject)
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForProject() error = %v", err)
	}
	meta := ensemble.CheckpointMetadata{
		RunID:        "synth-stream-cross-project-run",
		SessionName:  state.SessionName,
		Question:     state.Question,
		Status:       state.Status,
		CompletedIDs: []string{"mode-a"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	if err := store.SaveSynthesisCheckpoint(meta.RunID, ensemble.SynthesisCheckpoint{
		RunID:       meta.RunID,
		SessionName: state.SessionName,
		LastIndex:   0,
	}); err != nil {
		t.Fatalf("SaveSynthesisCheckpoint() error = %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	if err := runEnsembleSynthesize(t.Context(), &buf, state.SessionName, synthesizeOptions{
		Format: "json",
		Stream: true,
		Resume: true,
		RunID:  meta.RunID,
	}); err != nil {
		t.Fatalf("runEnsembleSynthesize() error = %v", err)
	}
	if strings.TrimSpace(buf.String()) == "" {
		t.Fatal("streamed synthesis output = empty, want resumed chunks")
	}
}

func TestRunEnsembleSynthesizeResumeRejectsCheckpointSessionMismatch(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "session-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir session project: %v", err)
	}

	cwdProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir cwd ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdProject, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	nestedDir := filepath.Join(cwdProject, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	store, err := newEnsembleCheckpointStoreForProject(projectDir)
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForProject() error = %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "resume-mismatch-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "mode-a",
		Thesis: "Mismatch thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Mismatch finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.8,
		}},
		Confidence:  0.8,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "resume-mismatch-session",
		Question:          "Resume mismatch",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "synth-stream-mismatch-run",
		SessionName:  "different-session",
		Question:     state.Question,
		Status:       state.Status,
		CompletedIDs: []string{"mode-a"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	if err := store.SaveSynthesisCheckpoint(meta.RunID, ensemble.SynthesisCheckpoint{
		RunID:       meta.RunID,
		SessionName: meta.SessionName,
		LastIndex:   0,
	}); err != nil {
		t.Fatalf("SaveSynthesisCheckpoint() error = %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var buf bytes.Buffer
	err = runEnsembleSynthesize(t.Context(), &buf, state.SessionName, synthesizeOptions{
		Format: "json",
		Stream: true,
		Resume: true,
		RunID:  meta.RunID,
	})
	if err == nil {
		t.Fatal("runEnsembleSynthesize() error = nil, want checkpoint/session mismatch")
	}
	if !strings.Contains(err.Error(), "belongs to session") {
		t.Fatalf("error = %v, want session mismatch message", err)
	}
}

func TestWriteCompareError_JSON(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestWriteCompareError_JSON - starting")

	testErr := &testError{msg: "session not found"}

	var buf bytes.Buffer
	err := writeCompareError(&buf, "missing-a", "missing-b", testErr, "json")

	if !errors.Is(err, errJSONFailure) || !errors.Is(err, testErr) {
		t.Fatalf("writeCompareError error = %v, want terminal sentinel and original cause", err)
	}

	output := buf.String()
	t.Logf("TEST: TestWriteCompareError_JSON - output: %s", output)

	// Parse JSON to validate structure
	var parsed compareOutput
	if parseErr := json.Unmarshal([]byte(output), &parsed); parseErr != nil {
		t.Fatalf("failed to parse JSON output: %v", parseErr)
	}

	if parsed.RunA != "missing-a" {
		t.Errorf("expected RunA='missing-a', got %q", parsed.RunA)
	}
	if parsed.Error != "session not found" {
		t.Errorf("expected Error='session not found', got %q", parsed.Error)
	}
	if parsed.Success {
		t.Error("expected failure compare output to set success=false")
	}

	t.Log("TEST: TestWriteCompareError_JSON - assertion: error JSON output is valid")
}

func TestWriteCompareError_JSONSurfacesEncoderError(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("compare writer closed")
	cause := errors.New("compare failed")
	err := writeCompareError(ensembleFailureWriter{err: writeErr}, "run-a", "run-b", cause, "json")
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeCompareError error = %v, want encoder error", err)
	}
	if errors.Is(err, errJSONFailure) {
		t.Fatalf("writeCompareError error = %v, must not claim a written JSON result", err)
	}
}

func TestWriteCompareError_YAMLPreservesCauseAndEncoderError(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("compare yaml writer closed")
	cause := errors.New("compare failed")
	err := writeCompareError(ensembleFailureWriter{err: writeErr}, "run-a", "run-b", cause, "yaml")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), writeErr.Error()) {
		t.Fatalf("writeCompareError error = %v, want YAML encoder error and original cause", err)
	}
}

func TestWriteCompareError_YAMLReturnsOriginalCauseAfterEncoding(t *testing.T) {
	t.Parallel()
	cause := errors.New("compare failed")
	var buf bytes.Buffer
	err := writeCompareError(&buf, "run-a", "run-b", cause, "yaml")
	if err != cause {
		t.Fatalf("writeCompareError error = %v, want original cause", err)
	}
	var parsed compareOutput
	if err := yaml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("decode YAML compare failure: %v; output=%q", err, buf.String())
	}
	if parsed.Success || parsed.Error != cause.Error() {
		t.Fatalf("YAML compare failure = %+v", parsed)
	}
}

func TestCompareOutput_Struct(t *testing.T) {
	t.Parallel()
	t.Log("TEST: TestCompareOutput_Struct - starting")

	out := compareOutput{
		GeneratedAt: time.Now().Format(time.RFC3339),
		RunA:        "test-a",
		RunB:        "test-b",
		Summary:     "No differences",
		Result: &ensemble.ComparisonResult{
			RunA:    "test-a",
			RunB:    "test-b",
			Summary: "No differences",
		},
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("failed to marshal compareOutput: %v", err)
	}

	t.Logf("TEST: TestCompareOutput_Struct - JSON: %s", string(data))

	// Verify round-trip
	var parsed compareOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal compareOutput: %v", err)
	}

	if parsed.RunA != out.RunA || parsed.RunB != out.RunB {
		t.Error("round-trip mismatch")
	}

	t.Log("TEST: TestCompareOutput_Struct - assertion: compareOutput marshals correctly")
}

func TestWriteCompareResult_ChangedSectionsAndUnchanged(t *testing.T) {
	t.Parallel()
	empty := &ensemble.ComparisonResult{
		RunA:    "same-a",
		RunB:    "same-b",
		Summary: "No differences",
	}
	var emptyJSON bytes.Buffer
	if err := writeCompareResult(&emptyJSON, empty, compareOptions{}, "json"); err != nil {
		t.Fatalf("writeCompareResult(empty json): %v", err)
	}
	var parsedEmpty compareOutput
	if err := json.Unmarshal(emptyJSON.Bytes(), &parsedEmpty); err != nil {
		t.Fatalf("unmarshal empty compare json: %v", err)
	}
	if !parsedEmpty.Unchanged || len(parsedEmpty.ChangedSections) != 0 {
		t.Fatalf("expected unchanged=true and no changed_sections, got %+v", parsedEmpty)
	}

	var emptyText bytes.Buffer
	if err := writeCompareResult(&emptyText, empty, compareOptions{}, "text"); err != nil {
		t.Fatalf("writeCompareResult(empty text): %v", err)
	}
	if !strings.Contains(emptyText.String(), "Runs are identical") {
		t.Fatalf("expected identical status in text output, got:\n%s", emptyText.String())
	}

	changed := &ensemble.ComparisonResult{
		RunA:         "run-1",
		RunB:         "run-2",
		ModeDiff:     ensemble.ModeDiff{AddedCount: 1, Added: []string{"bayesian"}},
		FindingsDiff: ensemble.FindingsDiff{NewCount: 1},
		ConclusionDiff: ensemble.ConclusionDiff{
			SynthesisChanged: true,
		},
		ContributionDiff: ensemble.ContributionDiff{
			ScoreDeltas: []ensemble.ScoreDelta{{ModeID: "deductive", Delta: 2.5}},
		},
		Summary: "all sections changed",
	}
	var changedJSON bytes.Buffer
	if err := writeCompareResult(&changedJSON, changed, compareOptions{}, "json"); err != nil {
		t.Fatalf("writeCompareResult(changed json): %v", err)
	}
	var parsedChanged compareOutput
	if err := json.Unmarshal(changedJSON.Bytes(), &parsedChanged); err != nil {
		t.Fatalf("unmarshal changed compare json: %v", err)
	}
	if parsedChanged.Unchanged {
		t.Fatal("expected unchanged=false for changed comparison")
	}
	wantSections := "modes,findings,conclusions,contributions"
	if got := strings.Join(parsedChanged.ChangedSections, ","); got != wantSections {
		t.Fatalf("ChangedSections = %q, want %q", got, wantSections)
	}
}

func TestRenderProvenanceValidation(t *testing.T) {
	t.Parallel()
	validChain := &ensemble.ProvenanceChain{
		FindingID:    "f-valid-001",
		SourceMode:   "deductive",
		OriginalText: "Valid finding",
		CurrentText:  "Valid finding",
		Impact:       ensemble.ImpactHigh,
		Confidence:   0.85,
		Steps: []ensemble.ProvenanceStep{
			{Stage: "discovery", Action: "discovered", Timestamp: time.Now().UTC()},
		},
	}
	invalidChain := &ensemble.ProvenanceChain{
		FindingID:  "f-invalid-002",
		SourceMode: "",
	}

	invalidCount, valErrs := validateProvenanceChains([]*ensemble.ProvenanceChain{validChain, invalidChain})
	if invalidCount != 1 || len(valErrs) != 1 {
		t.Fatalf("validateProvenanceChains = (%d, %v), want 1 invalid chain", invalidCount, valErrs)
	}

	validFlag := false
	var textBuf bytes.Buffer
	err := renderProvenanceOutput(&textBuf, provenanceOutput{
		GeneratedAt:      time.Now().UTC(),
		FindingID:        invalidChain.FindingID,
		Valid:            &validFlag,
		ValidationErrors: valErrs,
		Chain:            invalidChain,
	}, "text")
	if err != nil {
		t.Fatalf("renderProvenanceOutput(text): %v", err)
	}
	if !strings.Contains(textBuf.String(), "Validation: INVALID") {
		t.Fatalf("expected INVALID provenance banner in text output, got:\n%s", textBuf.String())
	}
}

const standaloneCLIModeOutput = "Analysis complete.\n" +
	"```yaml\nmode_id: deductive\nthesis: Preserve member reasoning before lead synthesis.\n" +
	"top_findings:\n  - finding: Durable capture must precede lead dispatch.\n" +
	"    impact: high\n    confidence: 0.9\n    evidence_pointer: internal/ensemble/capture.go\n" +
	"    reasoning: Sending the next task replaces the only live result.\nconfidence: 0.9\n```\nclaude>\n"

func setupStandaloneCLISynthesis(t *testing.T, output string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux requires a POSIX shell")
	}
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)
	oldCfg, oldJSON := cfg, jsonOutput
	cfg, jsonOutput = config.Default(), false
	t.Cleanup(func() { cfg, jsonOutput = oldCfg, oldJSON })

	const session = "standalone-cli-synthesis"
	dir := t.TempDir()
	t.Setenv("NTM_SYNTH_ROOT", dir)
	t.Setenv("NTM_SYNTH_SESSION", session)
	t.Setenv("NTM_SYNTH_HOLD", "")
	t.Setenv("NTM_SYNTH_PANES", "%1_NTM_SEP_0_NTM_SEP_"+session+"__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]")
	for name, content := range map[string]string{
		"member.txt": output,
		"lead.txt": "\n```json\n" +
			`{"summary":"Fresh synthesis from the live lead agent.","findings":[{"finding":"Save completed reasoning before dispatch.","impact":"high","confidence":0.94}],"confidence":0.94}` +
			"\n```\nclaude>\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$NTM_SYNTH_ROOT/tmux.log"
case "$1" in
  -V) printf 'tmux 3.5a\n' ;;
  has-session) ;;
  list-sessions) printf '%s_NTM_SEP_1_NTM_SEP_0_NTM_SEP_now\n' "$NTM_SYNTH_SESSION" ;;
  list-panes) printf '%s\n' "$NTM_SYNTH_PANES" ;;
  display-message)
    case "$*" in
      *session_name*) printf '%s\n' "$NTM_SYNTH_SESSION" ;;
      *rotation_token*) printf '[]\n' ;;
      *) exit 1 ;;
    esac
    ;;
  capture-pane)
    cat "$NTM_SYNTH_ROOT/member.txt"
    if [ -e "$NTM_SYNTH_ROOT/submitted" ]; then cat "$NTM_SYNTH_ROOT/lead.txt"; fi
    ;;
  load-buffer)
    cat > "$NTM_SYNTH_ROOT/prompt.txt"
    : > "$NTM_SYNTH_ROOT/before-prompt"
    if [ "$NTM_SYNTH_HOLD" = "1" ]; then
      while [ ! -e "$NTM_SYNTH_ROOT/release" ]; do sleep 0.01; done
    fi
    ;;
  paste-buffer|delete-buffer) ;;
  send-keys)
    case "$*" in
      *Enter*) : > "$NTM_SYNTH_ROOT/submitted" ;;
      *) exit 1 ;;
    esac
    ;;
  *) printf 'unexpected tmux command: %s\n' "$1" >&2; exit 1 ;;
esac
`
	binPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binPath)
	oldClient := tmux.DefaultClient
	oldLeadPoll := ensemble.DefaultLeadPollInterval
	oldCollectorPoll := ensemble.DefaultOutputCollectorPollInterval
	fastClient := tmux.NewClient("")
	fastClient.MaxSendDelay = 1 * time.Millisecond
	tmux.DefaultClient = fastClient
	ensemble.DefaultLeadPollInterval = 5 * time.Millisecond
	ensemble.DefaultOutputCollectorPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		tmux.DefaultClient = oldClient
		ensemble.DefaultLeadPollInterval = oldLeadPoll
		ensemble.DefaultOutputCollectorPollInterval = oldCollectorPoll
	})
	now := time.Now().UTC()
	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "How should completed ensemble reasoning be preserved?",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: session + "__cc_1", AgentType: "cc", Status: ensemble.AssignmentActive, AssignedAt: now},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatal(err)
	}
	return session, dir
}

func TestEnsembleSynthesizeCommandCapturesActiveAgentsBeforeLeadDispatch(t *testing.T) {
	session, dir := setupStandaloneCLISynthesis(t, standaloneCLIModeOutput)
	t.Setenv("NTM_SYNTH_HOLD", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	var output bytes.Buffer
	cmd := newEnsembleSynthesizeCmd()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{session, "--format=json", "--no-cache"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = cmd.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("synthesis command did not stop after cancellation")
		}
	})

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "before-prompt")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-done:
			t.Fatalf("command ended before lead dispatch: %v; output=%s", runErr, output.String())
		case <-ctx.Done():
			t.Fatal("command never reached lead dispatch")
		case <-ticker.C:
		}
	}

	saved, err := ensemble.LoadSession(session)
	if err != nil {
		t.Fatal(err)
	}
	assignment := saved.Assignments[0]
	if assignment.Status != ensemble.AssignmentDone || assignment.OutputPath == "" || assignment.CompletedAt == nil {
		t.Fatalf("member result was not durable before lead dispatch: %+v", assignment)
	}
	recovered, err := ensemble.CollectModeOutputs(saved, nil)
	if err != nil || len(recovered.Outputs) != 1 || recovered.Outputs[0].Thesis != "Preserve member reasoning before lead synthesis." {
		t.Fatalf("saved member result cannot be recovered before dispatch: %+v, err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "submitted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lead prompt submitted before release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("command did not finish after lead reply")
	}
	if runErr != nil {
		t.Fatalf("synthesize without --force: %v; output=%s", runErr, output.String())
	}
	var decoded struct {
		Synthesis *ensemble.SynthesisResult `json:"synthesis"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode synthesis: %v; output=%s", err, output.String())
	}
	if decoded.Synthesis == nil || decoded.Synthesis.SynthesizedBy != "agent" ||
		decoded.Synthesis.FallbackReason != "" || decoded.Synthesis.Summary != "Fresh synthesis from the live lead agent." {
		t.Fatalf("expected a fresh lead synthesis, got %+v", decoded.Synthesis)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil || !strings.Contains(string(prompt), saved.Question) ||
		!strings.Contains(string(prompt), "Durable capture must precede lead dispatch.") {
		t.Fatalf("lead did not receive the question and captured reasoning: %q, err=%v", prompt, err)
	}
}

func TestEnsembleSynthesizeCommandRefusesUnfinishedLiveOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "complete schema while still busy", output: strings.TrimSuffix(standaloneCLIModeOutput, "claude>\n") + "Continuing analysis...\n"},
		{name: "idle with incomplete output", output: "Partial analysis with no structured result.\nclaude>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, dir := setupStandaloneCLISynthesis(t, tc.output)
			var output bytes.Buffer
			cmd := newEnsembleSynthesizeCmd()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{session, "--format=json", "--no-cache"})
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err := cmd.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), "synthesis not ready") {
				t.Fatalf("unfinished output must remain not ready: err=%v output=%s", err, output.String())
			}
			saved, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Assignments[0].Status != ensemble.AssignmentActive || saved.Assignments[0].OutputPath != "" {
				t.Fatalf("unfinished output was promoted to completed: %+v", saved.Assignments[0])
			}
			if _, err := os.Stat(filepath.Join(dir, "prompt.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unfinished member received a synthesis prompt: %v", err)
			}
		})
	}
}

func TestEnsembleSynthesizeCommandCancellationDoesNotDispatch(t *testing.T) {
	session, dir := setupStandaloneCLISynthesis(t, standaloneCLIModeOutput)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output bytes.Buffer
	cmd := newEnsembleSynthesizeCmd()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{session, "--format=json", "--no-cache"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command returned %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompt.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command dispatched a prompt: %v", err)
	}
	saved, err := ensemble.LoadSession(session)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Assignments[0].Status != ensemble.AssignmentActive {
		t.Fatalf("canceled command changed assignment state: %+v", saved.Assignments[0])
	}
}
