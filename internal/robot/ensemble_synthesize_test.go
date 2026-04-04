package robot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func TestGetEnsembleSynthesize_EmptySession(t *testing.T) {
	t.Log("TEST: TestGetEnsembleSynthesize_EmptySession - starting")

	output, err := GetEnsembleSynthesize(EnsembleSynthesizeOptions{
		Session: "",
	})
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize failed: %v", err)
	}

	if output.Success {
		t.Error("expected success=false for empty session")
	}

	if output.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code=%s, got %s", ErrCodeInvalidFlag, output.ErrorCode)
	}

	if output.Action != "ensemble_synthesize" {
		t.Errorf("expected action=ensemble_synthesize, got %s", output.Action)
	}

	t.Log("TEST: TestGetEnsembleSynthesize_EmptySession - assertion: empty session handled correctly")
}

func TestGetEnsembleSynthesize_NonexistentSession(t *testing.T) {
	t.Log("TEST: TestGetEnsembleSynthesize_NonexistentSession - starting")

	output, err := GetEnsembleSynthesize(EnsembleSynthesizeOptions{
		Session: "nonexistent-session-xyz",
	})
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize failed: %v", err)
	}

	if output.Success {
		t.Error("expected success=false for nonexistent session")
	}

	if output.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("expected error_code=%s, got %s", ErrCodeSessionNotFound, output.ErrorCode)
	}

	t.Log("TEST: TestGetEnsembleSynthesize_NonexistentSession - assertion: nonexistent session handled correctly")
}

func TestGetEnsembleSynthesize_InvalidFormat(t *testing.T) {
	t.Log("TEST: TestGetEnsembleSynthesize_InvalidFormat - starting")

	// This test will fail at session check, but we verify options are parsed
	output, err := GetEnsembleSynthesize(EnsembleSynthesizeOptions{
		Session: "test-session",
		Format:  "invalid-format",
	})
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize failed: %v", err)
	}

	// Should fail at session check before format validation
	if output.Success {
		t.Error("expected success=false")
	}

	t.Log("TEST: TestGetEnsembleSynthesize_InvalidFormat - assertion: invalid format handled")
}

func TestGetEnsembleSynthesize_UsesSavedOutputsWhenSessionOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "robot-offline-output.yaml")
	modeOutput := strings.TrimSpace(`
thesis: Robot offline thesis
top_findings:
  - finding: Robot offline finding
    impact: medium
    confidence: 0.8
confidence: 0.8
`)
	if err := os.WriteFile(outputPath, []byte(modeOutput), 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "robot-offline-synth",
		Question:          "Synthesize saved outputs",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	output, err := GetEnsembleSynthesize(EnsembleSynthesizeOptions{
		Session: state.SessionName,
		Format:  "json",
	})
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize failed: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success, got error=%q code=%q", output.Error, output.ErrorCode)
	}
	if output.Status != "complete" {
		t.Fatalf("status = %q, want complete", output.Status)
	}
	if output.Report == nil || output.Report.FindingsCount != 1 {
		t.Fatalf("report = %+v, want 1 finding", output.Report)
	}
}

func TestBuildSynthesizeHints_Complete(t *testing.T) {
	t.Log("TEST: TestBuildSynthesizeHints_Complete - starting")

	output := &EnsembleSynthesizeOutput{
		Status: "complete",
		Report: &SynthesisReport{
			FindingsCount:        5,
			RecommendationsCount: 3,
			OutputPath:           "/tmp/report.md",
		},
		Audit: &SynthesisAudit{
			UnresolvedCount: 2,
		},
	}

	hints := buildSynthesizeHints(output)
	if hints == nil {
		t.Fatal("expected non-nil hints")
	}

	if hints.Summary == "" {
		t.Error("expected non-empty summary")
	}

	if len(hints.Notes) == 0 {
		t.Error("expected notes about output path")
	}

	if len(hints.Warnings) == 0 {
		t.Error("expected warning about unresolved conflicts")
	}

	t.Logf("TEST: hints summary: %s", hints.Summary)
	t.Log("TEST: TestBuildSynthesizeHints_Complete - assertion: complete status hints generated correctly")
}

func TestBuildSynthesizeHints_NotReady(t *testing.T) {
	t.Log("TEST: TestBuildSynthesizeHints_NotReady - starting")

	output := &EnsembleSynthesizeOutput{
		Status: "not_ready",
	}

	hints := buildSynthesizeHints(output)
	if hints == nil {
		t.Fatal("expected non-nil hints")
	}

	if len(hints.SuggestedActions) == 0 {
		t.Error("expected suggested actions for not_ready status")
	}

	foundWaitAction := false
	for _, action := range hints.SuggestedActions {
		if action.Action == "wait" {
			foundWaitAction = true
			break
		}
	}

	if !foundWaitAction {
		t.Error("expected 'wait' action for not_ready status")
	}

	t.Log("TEST: TestBuildSynthesizeHints_NotReady - assertion: not_ready status hints generated correctly")
}

func TestBuildSynthesizeHints_Error(t *testing.T) {
	t.Log("TEST: TestBuildSynthesizeHints_Error - starting")

	output := &EnsembleSynthesizeOutput{
		Status:  "error",
		Session: "test-session",
	}

	hints := buildSynthesizeHints(output)
	if hints == nil {
		t.Fatal("expected non-nil hints")
	}

	if len(hints.SuggestedActions) == 0 {
		t.Error("expected suggested actions for error status")
	}

	foundCheckAction := false
	for _, action := range hints.SuggestedActions {
		if action.Action == "check_status" {
			foundCheckAction = true
			break
		}
	}

	if !foundCheckAction {
		t.Error("expected 'check_status' action for error status")
	}

	t.Log("TEST: TestBuildSynthesizeHints_Error - assertion: error status hints generated correctly")
}

func TestBuildSynthesizeHints_Nil(t *testing.T) {
	t.Log("TEST: TestBuildSynthesizeHints_Nil - starting")

	hints := buildSynthesizeHints(nil)
	if hints != nil {
		t.Error("expected nil hints for nil output")
	}

	t.Log("TEST: TestBuildSynthesizeHints_Nil - assertion: nil input handled correctly")
}

func TestEnsembleSynthesizeOutput_FieldsInitialized(t *testing.T) {
	t.Log("TEST: TestEnsembleSynthesizeOutput_FieldsInitialized - starting")

	output := &EnsembleSynthesizeOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_synthesize",
		Status:        "complete",
		Report: &SynthesisReport{
			Summary:              "Test summary",
			Strategy:             "manual",
			Format:               "markdown",
			FindingsCount:        3,
			RecommendationsCount: 2,
			RisksCount:           1,
			Confidence:           0.85,
		},
		Audit: &SynthesisAudit{
			ConflictCount:     1,
			UnresolvedCount:   0,
			HighConflictPairs: []string{},
		},
	}

	// Verify required fields
	if output.Action == "" {
		t.Error("action should not be empty")
	}
	if output.Status == "" {
		t.Error("status should not be empty")
	}
	if output.Report.Strategy == "" {
		t.Error("strategy should not be empty")
	}
	if output.Report.Format == "" {
		t.Error("format should not be empty")
	}

	// Verify audit arrays initialized
	if output.Audit.HighConflictPairs == nil {
		t.Error("high_conflict_pairs should be empty array, not nil")
	}

	t.Log("TEST: TestEnsembleSynthesizeOutput_FieldsInitialized - assertion: all fields properly initialized")
}
