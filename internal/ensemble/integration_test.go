package ensemble

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateStore_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	store, err := NewStateStore(path)
	if err != nil {
		t.Fatalf("NewStateStore error: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	createdAt := time.Now().UTC().Truncate(time.Second)
	synthAt := createdAt.Add(2 * time.Minute)
	completedAt := createdAt.Add(5 * time.Minute)

	session := &EnsembleSession{
		SessionName:       "test-session",
		Question:          "What is the issue?",
		PresetUsed:        "diagnosis",
		Status:            EnsembleActive,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         createdAt,
		SynthesizedAt:     &synthAt,
		SynthesisOutput:   "summary",
		Error:             "",
		Assignments: []ModeAssignment{
			{
				ModeID:      "deductive",
				PaneName:    "pane-1",
				AgentType:   "cc",
				Status:      AssignmentActive,
				OutputPath:  "/tmp/out.txt",
				AssignedAt:  createdAt,
				CompletedAt: &completedAt,
			},
		},
	}

	if err := store.Save(session); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := store.Load("test-session")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded session, got nil")
	}

	if loaded.Question != session.Question {
		t.Errorf("Question = %q, want %q", loaded.Question, session.Question)
	}
	if loaded.PresetUsed != session.PresetUsed {
		t.Errorf("PresetUsed = %q, want %q", loaded.PresetUsed, session.PresetUsed)
	}
	if loaded.Status != session.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, session.Status)
	}
	if loaded.SynthesisStrategy != session.SynthesisStrategy {
		t.Errorf("SynthesisStrategy = %q, want %q", loaded.SynthesisStrategy, session.SynthesisStrategy)
	}
	if !loaded.CreatedAt.Equal(session.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, session.CreatedAt)
	}
	if loaded.SynthesizedAt == nil || !loaded.SynthesizedAt.Equal(*session.SynthesizedAt) {
		t.Errorf("SynthesizedAt = %v, want %v", loaded.SynthesizedAt, session.SynthesizedAt)
	}
	if loaded.SynthesisOutput != session.SynthesisOutput {
		t.Errorf("SynthesisOutput = %q, want %q", loaded.SynthesisOutput, session.SynthesisOutput)
	}

	if len(loaded.Assignments) != 1 {
		t.Fatalf("Assignments len = %d, want 1", len(loaded.Assignments))
	}
	assignment := loaded.Assignments[0]
	if assignment.ModeID != "deductive" {
		t.Errorf("Assignment ModeID = %q, want deductive", assignment.ModeID)
	}
	if assignment.PaneName != "pane-1" {
		t.Errorf("Assignment PaneName = %q, want pane-1", assignment.PaneName)
	}
	if assignment.AgentType != "cc" {
		t.Errorf("Assignment AgentType = %q, want cc", assignment.AgentType)
	}
	if assignment.Status != AssignmentActive {
		t.Errorf("Assignment Status = %q, want %q", assignment.Status, AssignmentActive)
	}
	if assignment.OutputPath != "/tmp/out.txt" {
		t.Errorf("Assignment OutputPath = %q, want /tmp/out.txt", assignment.OutputPath)
	}
	if assignment.AssignedAt.IsZero() {
		t.Errorf("Assignment AssignedAt should be set")
	}
	if assignment.CompletedAt == nil || !assignment.CompletedAt.Equal(completedAt) {
		t.Errorf("Assignment CompletedAt = %v, want %v", assignment.CompletedAt, completedAt)
	}
}

func TestOutputCapture_ExtractYAML_PrefersValidBlock(t *testing.T) {
	capture := NewOutputCapture(nil)
	raw := strings.Join([]string{
		"noise before",
		"```yaml",
		": bad yaml",
		"```",
		"more noise",
		"```yaml",
		"mode_id: deductive",
		"thesis: something",
		"```",
	}, "\n")

	block, ok := capture.extractYAML(raw)
	if !ok {
		t.Fatal("expected YAML block to be found")
	}
	if !strings.Contains(block, "mode_id: deductive") {
		t.Fatalf("expected valid YAML block, got: %q", block)
	}
}

func TestOutputCapture_CapturePane_Empty(t *testing.T) {
	capture := NewOutputCapture(nil)
	if _, err := capture.capturePane(""); err == nil {
		t.Fatal("expected error for empty pane")
	}
}

// =============================================================================
// Integration Tests for Ensemble Dependency Chain
// These tests verify that components integrate correctly without real tmux or model calls.
// =============================================================================

// TestIntegration_OrchestratorToSynthesis verifies the flow:
// ModeOutputs → synthesis strategy lookup → strategy config
func TestIntegration_OrchestratorToSynthesis(t *testing.T) {
	// Step 1: Create mock mode outputs (as if from agents)
	outputs := []ModeOutput{
		{
			ModeID: "deductive",
			Thesis: "The authentication layer has a potential race condition",
			TopFindings: []Finding{
				{
					Finding:         "Race condition in token refresh",
					Impact:          ImpactHigh,
					Confidence:      0.85,
					EvidencePointer: "internal/auth/token.go:142",
					Reasoning:       "Multiple goroutines access shared state",
				},
			},
			Confidence:  0.85,
			GeneratedAt: time.Now(),
		},
		{
			ModeID: "systems-thinking",
			Thesis: "The system has tight coupling between auth and session management",
			TopFindings: []Finding{
				{
					Finding:         "Tight coupling increases change risk",
					Impact:          ImpactMedium,
					Confidence:      0.7,
					EvidencePointer: "internal/session/manager.go:55",
					Reasoning:       "Direct imports create dependency",
				},
			},
			Confidence:  0.7,
			GeneratedAt: time.Now(),
		},
	}

	// Step 2: Validate outputs
	for i, output := range outputs {
		if err := output.Validate(); err != nil {
			t.Fatalf("output[%d] validation error: %v", i, err)
		}
	}

	// Step 3: Get synthesis strategy config
	strategyName := "consensus"
	strategy, err := GetStrategy(strategyName)
	if err != nil {
		t.Fatalf("GetStrategy error: %v", err)
	}

	// Step 4: Verify strategy config
	if strategy.Name != StrategyConsensus {
		t.Errorf("strategy name = %q, want consensus", strategy.Name)
	}
	if !strategy.RequiresAgent {
		t.Error("consensus strategy should require agent")
	}
	if strategy.SynthesizerMode == "" {
		t.Error("consensus strategy should have synthesizer mode")
	}

	// Step 5: Verify deprecated strategies migrate correctly
	migrated, wasMigrated := MigrateStrategy("debate")
	if !wasMigrated {
		t.Error("expected 'debate' to be deprecated")
	}
	if migrated != "dialectical" {
		t.Errorf("debate migration = %q, want dialectical", migrated)
	}

	// Step 6: Test strategy validation
	_, err = ValidateOrMigrateStrategy("invalid-strategy")
	if err == nil {
		t.Error("expected error for invalid strategy")
	}

	// Step 7: Estimate tokens for outputs
	totalTokens := EstimateModeOutputsTokens(outputs)
	if totalTokens <= 0 {
		t.Error("expected positive token estimate for outputs")
	}
}
