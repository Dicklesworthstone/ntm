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

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestNewEnsembleCompareCmd(t *testing.T) {
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

	t.Log("TEST: TestWriteCompareResult_JSON - assertion: JSON output is valid")
}

func TestWriteCompareResult_Text(t *testing.T) {
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

func TestLoadCompareInput_LoadsCheckpointRunFromProjectRoot(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStore() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "run-checkpoint-1",
		SessionName:  "compare-session",
		Question:     "What changed?",
		Status:       ensemble.EnsembleComplete,
		CompletedIDs: []string{"mode-a"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}

	checkpoint := ensemble.ModeCheckpoint{
		ModeID: "mode-a",
		Status: string(ensemble.AssignmentDone),
		Output: &ensemble.ModeOutput{
			ModeID: "mode-a",
			Thesis: "Checkpoint thesis",
			TopFindings: []ensemble.Finding{{
				Finding:    "Checkpoint finding",
				Impact:     ensemble.ImpactMedium,
				Confidence: 0.8,
			}},
			Confidence:  0.8,
			GeneratedAt: time.Now(),
		},
		CapturedAt: time.Now(),
	}
	if err := store.SaveCheckpoint(meta.RunID, checkpoint); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	input, err := loadCompareInput(meta.RunID)
	if err != nil {
		t.Fatalf("loadCompareInput() error = %v", err)
	}
	if input.RunID != meta.RunID {
		t.Fatalf("RunID = %q, want %q", input.RunID, meta.RunID)
	}
	if len(input.ModeIDs) != 1 || input.ModeIDs[0] != "mode-a" {
		t.Fatalf("ModeIDs = %v, want [mode-a]", input.ModeIDs)
	}
	if len(input.Outputs) != 1 || input.Outputs[0].Thesis != "Checkpoint thesis" {
		t.Fatalf("Outputs = %+v, want checkpoint output", input.Outputs)
	}
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

	store, err := newEnsembleCheckpointStoreForSession("mysession")
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

func TestLoadExportFindingsContextRunIDUsesProvidedSessionProjectDir(t *testing.T) {
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

	store, err := newEnsembleCheckpointStoreForSession("mysession")
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStoreForSession() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "export-run",
		SessionName:  "mysession",
		Question:     "What should we export?",
		Status:       ensemble.EnsembleComplete,
		CompletedIDs: []string{"mode-a"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}

	checkpoint := ensemble.ModeCheckpoint{
		ModeID: "mode-a",
		Status: string(ensemble.AssignmentDone),
		Output: &ensemble.ModeOutput{
			ModeID: "mode-a",
			Thesis: "Export thesis",
			TopFindings: []ensemble.Finding{{
				Finding:    "Export finding",
				Impact:     ensemble.ImpactMedium,
				Confidence: 0.8,
			}},
			Confidence:  0.8,
			GeneratedAt: time.Now(),
		},
		CapturedAt: time.Now(),
	}
	if err := store.SaveCheckpoint(meta.RunID, checkpoint); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	ctx, err := loadExportFindingsContext(io.Discard, "mysession", exportFindingsOptions{RunID: meta.RunID})
	if err != nil {
		t.Fatalf("loadExportFindingsContext() error = %v", err)
	}
	if ctx == nil {
		t.Fatal("expected export findings context")
	}
	if ctx.ProjectDir != sessionProject {
		t.Fatalf("ProjectDir = %q, want %q", ctx.ProjectDir, sessionProject)
	}
	if ctx.RunID != meta.RunID {
		t.Fatalf("RunID = %q, want %q", ctx.RunID, meta.RunID)
	}
	if len(ctx.Outputs) != 1 || ctx.Outputs[0].Thesis != "Export thesis" {
		t.Fatalf("Outputs = %+v, want exported checkpoint output", ctx.Outputs)
	}
}

func TestRunEnsembleCompare_AllowsCheckpointRunsWithoutTmux(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStore() error = %v", err)
	}

	saveRun := func(runID, modeID, thesis, finding string) {
		t.Helper()
		meta := ensemble.CheckpointMetadata{
			RunID:        runID,
			SessionName:  runID + "-session",
			Question:     "What changed?",
			Status:       ensemble.EnsembleComplete,
			CompletedIDs: []string{modeID},
			TotalModes:   1,
		}
		if err := store.SaveMetadata(meta); err != nil {
			t.Fatalf("SaveMetadata(%s) error = %v", runID, err)
		}
		checkpoint := ensemble.ModeCheckpoint{
			ModeID: modeID,
			Status: string(ensemble.AssignmentDone),
			Output: &ensemble.ModeOutput{
				ModeID: modeID,
				Thesis: thesis,
				TopFindings: []ensemble.Finding{{
					Finding:    finding,
					Impact:     ensemble.ImpactMedium,
					Confidence: 0.8,
				}},
				Confidence:  0.8,
				GeneratedAt: time.Now(),
			},
			CapturedAt: time.Now(),
		}
		if err := store.SaveCheckpoint(runID, checkpoint); err != nil {
			t.Fatalf("SaveCheckpoint(%s) error = %v", runID, err)
		}
	}

	saveRun("run-a", "mode-a", "First thesis", "First finding")
	saveRun("run-b", "mode-a", "Second thesis", "Second finding")

	oldInstalled := compareTmuxInstalled
	compareTmuxInstalled = func() bool { return false }
	t.Cleanup(func() {
		compareTmuxInstalled = oldInstalled
	})

	var buf bytes.Buffer
	if err := runEnsembleCompare(&buf, "run-a", "run-b", compareOptions{Format: "json"}); err != nil {
		t.Fatalf("runEnsembleCompare() error = %v", err)
	}

	var parsed compareOutput
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal compare output: %v", err)
	}
	if parsed.RunA != "run-a" || parsed.RunB != "run-b" {
		t.Fatalf("runs = %q/%q, want run-a/run-b", parsed.RunA, parsed.RunB)
	}
	if parsed.Result == nil {
		t.Fatal("expected comparison result")
	}
	if !parsed.Result.ConclusionDiff.SynthesisChanged && len(parsed.Result.ConclusionDiff.ThesisChanges) == 0 {
		t.Fatalf("expected some conclusion difference, got %+v", parsed.Result.ConclusionDiff)
	}
}

func TestLoadCompareInputRejectsAmbiguousLiveSessionAndCheckpointRun(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		t.Fatalf("newEnsembleCheckpointStore() error = %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		RunID:        "ambiguous-run",
		SessionName:  "compare-session",
		Question:     "What changed?",
		Status:       ensemble.EnsembleComplete,
		CompletedIDs: []string{"mode-a"},
		TotalModes:   1,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}

	oldInstalled := compareTmuxInstalled
	oldSessionExists := compareSessionExists
	compareTmuxInstalled = func() bool { return true }
	compareSessionExists = func(name string) bool { return name == meta.RunID }
	t.Cleanup(func() {
		compareTmuxInstalled = oldInstalled
		compareSessionExists = oldSessionExists
	})

	_, err = loadCompareInput(meta.RunID)
	if err == nil {
		t.Fatal("expected ambiguous identifier error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous identifier error, got %v", err)
	}
}

func TestWriteCompareError_JSON(t *testing.T) {
	t.Log("TEST: TestWriteCompareError_JSON - starting")

	testErr := &testError{msg: "session not found"}

	var buf bytes.Buffer
	err := writeCompareError(&buf, "missing-a", "missing-b", testErr, "json")

	// Should return the original error
	if err == nil {
		t.Error("expected error to be returned")
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

	t.Log("TEST: TestWriteCompareError_JSON - assertion: error JSON output is valid")
}

func TestCompareOutput_Struct(t *testing.T) {
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
