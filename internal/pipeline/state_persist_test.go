package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaveStateWritesVersionedStateAtContractPath(t *testing.T) {
	tmpDir := t.TempDir()
	state := &ExecutionState{
		RunID:      "run-20260507-071500-abcd",
		WorkflowID: "workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Variables:  map[string]interface{}{"env": "test"},
		Steps:      map[string]StepResult{"start": {Status: StatusCompleted}},
	}

	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState returned error: %v", err)
	}

	path := filepath.Join(tmpDir, ".ntm", "pipelines", state.RunID+".json")
	if path != pipelineStatePath(tmpDir, state.RunID) {
		t.Fatalf("pipelineStatePath returned %q, want %q", pipelineStatePath(tmpDir, state.RunID), path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if got := int(doc["state_schema_version"].(float64)); got != PipelineStateSchemaVersion {
		t.Fatalf("state_schema_version = %d, want %d", got, PipelineStateSchemaVersion)
	}
	if got := doc["run_id"]; got != state.RunID {
		t.Fatalf("run_id = %v, want %q", got, state.RunID)
	}
	if got := doc["steps"]; got == nil {
		t.Fatal("steps missing from persisted state")
	}
}

func TestLoadStateRejectsNewerSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	dir := pipelineStateDir(tmpDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}

	runID := "future-run"
	payload := `{"state_schema_version":999,"run_id":"future-run","status":"running"}`
	if err := os.WriteFile(pipelineStatePath(tmpDir, runID), []byte(payload), 0o644); err != nil {
		t.Fatalf("write future state: %v", err)
	}

	_, err := LoadState(tmpDir, runID)
	if err == nil {
		t.Fatal("LoadState returned nil error for future schema version")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("LoadState error = %q, want newer-version message", err.Error())
	}
}

func TestLoadStateAcceptsLegacyUnversionedState(t *testing.T) {
	tmpDir := t.TempDir()
	dir := pipelineStateDir(tmpDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}

	runID := "legacy-run"
	payload := `{"run_id":"legacy-run","workflow_id":"wf","status":"completed"}`
	if err := os.WriteFile(pipelineStatePath(tmpDir, runID), []byte(payload), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	state, err := LoadState(tmpDir, runID)
	if err != nil {
		t.Fatalf("LoadState returned error for legacy state: %v", err)
	}
	if state.RunID != runID {
		t.Fatalf("RunID = %q, want %q", state.RunID, runID)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q", state.Status, StatusCompleted)
	}
}

func TestLoadStateRejectsMismatchedStoredRunID(t *testing.T) {
	tmpDir := t.TempDir()
	dir := pipelineStateDir(tmpDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}

	runID := "requested-run"
	payload := `{"run_id":"different-run","workflow_id":"wf","status":"running"}`
	if err := os.WriteFile(pipelineStatePath(tmpDir, runID), []byte(payload), 0o644); err != nil {
		t.Fatalf("write mismatched state: %v", err)
	}

	_, err := LoadState(tmpDir, runID)
	if err == nil {
		t.Fatal("LoadState returned nil error for a mismatched stored run id")
	}
	if !strings.Contains(err.Error(), "does not match requested run id") {
		t.Fatalf("LoadState error = %q, want mismatched-run-id message", err.Error())
	}
}

func TestSaveStateMarshalFailurePreservesPreviousFile(t *testing.T) {
	tmpDir := t.TempDir()
	runID := "atomic-preserve"
	original := &ExecutionState{
		RunID:  runID,
		Status: StatusRunning,
		Steps:  map[string]StepResult{"old": {Status: StatusCompleted}},
	}
	if err := SaveState(tmpDir, original); err != nil {
		t.Fatalf("initial SaveState returned error: %v", err)
	}

	bad := &ExecutionState{
		RunID:     runID,
		Status:    StatusFailed,
		Variables: map[string]interface{}{"bad": make(chan int)},
	}
	if err := SaveState(tmpDir, bad); err == nil {
		t.Fatal("SaveState returned nil error for unmarshalable state")
	}

	loaded, err := LoadState(tmpDir, runID)
	if err != nil {
		t.Fatalf("LoadState returned error after failed overwrite: %v", err)
	}
	if loaded.Status != StatusRunning {
		t.Fatalf("Status = %q, want original %q after failed overwrite", loaded.Status, StatusRunning)
	}
	if _, ok := loaded.Steps["old"]; !ok {
		t.Fatal("original step missing after failed overwrite")
	}
}

func TestSaveStateConcurrentRunsUseSeparateFiles(t *testing.T) {
	tmpDir := t.TempDir()
	runIDs := []string{
		"run-20260507-071501-0001",
		"run-20260507-071501-0002",
	}

	for _, runID := range runIDs {
		state := &ExecutionState{RunID: runID, WorkflowID: "wf", Status: StatusRunning}
		if err := SaveState(tmpDir, state); err != nil {
			t.Fatalf("SaveState(%s) returned error: %v", runID, err)
		}
	}

	for _, runID := range runIDs {
		path := pipelineStatePath(tmpDir, runID)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("state file for %s missing: %v", runID, err)
		}
		loaded, err := LoadState(tmpDir, runID)
		if err != nil {
			t.Fatalf("LoadState(%s) returned error: %v", runID, err)
		}
		if loaded.RunID != runID {
			t.Fatalf("loaded RunID = %q, want %q", loaded.RunID, runID)
		}
	}
}

func TestSaveStateConcurrentWritersLeaveValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	runID := "same-run"
	const writers = 8

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state := &ExecutionState{
				RunID:       runID,
				WorkflowID:  "wf",
				Status:      StatusRunning,
				CurrentStep: string(rune('a' + i)),
			}
			if err := SaveState(tmpDir, state); err != nil {
				t.Errorf("SaveState writer %d returned error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	loaded, err := LoadState(tmpDir, runID)
	if err != nil {
		t.Fatalf("LoadState returned error after concurrent writes: %v", err)
	}
	if loaded.RunID != runID {
		t.Fatalf("RunID = %q, want %q", loaded.RunID, runID)
	}

	data, err := os.ReadFile(pipelineStatePath(tmpDir, runID))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("final state is not valid JSON: %v", err)
	}
	if got := int(doc["state_schema_version"].(float64)); got != PipelineStateSchemaVersion {
		t.Fatalf("state_schema_version = %d, want %d", got, PipelineStateSchemaVersion)
	}
}

func TestSaveStateRejectsTraversalRunID(t *testing.T) {
	// bd-2zi98: a hostile or malformed RunID containing path separators or
	// "../" components must not let SaveState write outside the pipeline
	// state directory. Validate every escape shape we can think of.
	tmpDir := t.TempDir()
	hostile := []string{
		"../escape",
		"sub/escape",
		"../../etc/passwd",
		"a\\b",
		".",
		"..",
		"with\x00null",
	}
	for _, runID := range hostile {
		state := &ExecutionState{
			RunID:      runID,
			WorkflowID: "wf",
			Status:     StatusRunning,
		}
		if err := SaveState(tmpDir, state); err == nil {
			t.Errorf("SaveState(runID=%q) succeeded, want validation error", runID)
		}
	}

	// Sanity: nothing leaked outside the project's .ntm/pipelines tree.
	if entries, err := os.ReadDir(tmpDir); err == nil {
		for _, entry := range entries {
			if entry.Name() != ".ntm" {
				t.Fatalf("unexpected sibling %q created in project dir", entry.Name())
			}
		}
	}
}

func TestLoadStateRejectsTraversalRunID(t *testing.T) {
	tmpDir := t.TempDir()
	hostile := []string{"../escape", "sub/escape", "..", ".", "with\x00null", "a\\b"}
	for _, runID := range hostile {
		if _, err := LoadState(tmpDir, runID); err == nil {
			t.Errorf("LoadState(runID=%q) returned nil error", runID)
		}
	}
}

func TestValidateRunIDAcceptsGeneratedShape(t *testing.T) {
	// Generated run IDs from GenerateRunID() (run-<timestamp>-<hex>) and
	// other plausible basename shapes must remain valid.
	good := []string{
		"run-20260507-143015-deadbeef",
		"my-custom-run",
		"run_42",
		"v1.2.3",
		"a",
	}
	for _, runID := range good {
		if err := validateRunID(runID); err != nil {
			t.Errorf("validateRunID(%q) returned error: %v", runID, err)
		}
	}

	bad := []string{"", "..", ".", "a/b", "a\\b", "a\x00b", "../x"}
	for _, runID := range bad {
		if err := validateRunID(runID); err == nil {
			t.Errorf("validateRunID(%q) returned nil, want error", runID)
		}
	}
}
