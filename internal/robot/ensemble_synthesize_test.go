package robot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func TestGetEnsembleSynthesize_ValidationAndMissingSession(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	cfg := config.Default()

	emptyOut, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{}, cfg)
	if err != nil {
		t.Fatalf("empty session returned Go error: %v", err)
	}
	if emptyOut.Success || emptyOut.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("empty session = (%v, %q), want (false, %q)", emptyOut.Success, emptyOut.ErrorCode, ErrCodeInvalidFlag)
	}

	badFmt, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session: "synth-bad-fmt",
		Format:  "xml",
	}, cfg)
	if err != nil {
		t.Fatalf("bad format returned Go error: %v", err)
	}
	if badFmt.Success || badFmt.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("bad format = (%v, %q), want (false, %q)", badFmt.Success, badFmt.ErrorCode, ErrCodeInvalidFlag)
	}

	badStrategy, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session:  "synth-bad-strategy",
		Strategy: "nonexistent-strategy",
	}, cfg)
	if err != nil {
		t.Fatalf("bad strategy returned Go error: %v", err)
	}
	if badStrategy.Success || badStrategy.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("bad strategy = (%v, %q), want (false, %q)", badStrategy.Success, badStrategy.ErrorCode, ErrCodeInvalidFlag)
	}

	missing, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session: "missing-ensemble-synth-session",
	}, cfg)
	if err != nil {
		t.Fatalf("missing session returned Go error: %v", err)
	}
	if missing.Success || missing.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("missing session = (%v, %q), want (false, %q)", missing.Success, missing.ErrorCode, ErrCodeSessionNotFound)
	}
}

func TestGetEnsembleSynthesize_ForceAndOutputPersistence(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outDir := t.TempDir()
	outPathA := filepath.Join(outDir, "deductive.json")
	outPathB := filepath.Join(outDir, "bayesian.json")
	now := time.Now().UTC()

	writeRobotTestModeOutput(t, outPathA, ensemble.ModeOutput{
		ModeID: "deductive",
		Thesis: "Authentication middleware must validate audience claims centrally.",
		TopFindings: []ensemble.Finding{
			{
				Finding:         "Audience validation is duplicated across legacy handlers",
				Impact:          ensemble.ImpactHigh,
				Confidence:      0.91,
				EvidencePointer: "internal/serve/auth.go:42",
				Reasoning:       "Static route audit",
			},
		},
		Risks: []ensemble.Risk{
			{
				Risk:       "Token replay across environments",
				Impact:     ensemble.ImpactHigh,
				Likelihood: 0.65,
				Mitigation: "Validate audience in shared middleware",
			},
		},
		Recommendations: []ensemble.Recommendation{
			{
				Recommendation: "Consolidate audience checks in authMiddlewareFunc",
				Priority:       ensemble.ImpactHigh,
				Rationale:      "Single enforcement point",
			},
		},
		Confidence:  0.89,
		GeneratedAt: now,
	})

	writeRobotTestModeOutput(t, outPathB, ensemble.ModeOutput{
		ModeID: "bayesian",
		Thesis: "Historical auth defects concentrate in unshared route wrappers.",
		TopFindings: []ensemble.Finding{
			{
				Finding:         "Worktree path validation should reject path traversal segments",
				Impact:          ensemble.ImpactMedium,
				Confidence:      0.83,
				EvidencePointer: "internal/worktrees/manager.go:116",
				Reasoning:       "Posterior defect density analysis",
			},
		},
		Confidence:  0.84,
		GeneratedAt: now,
	})

	sessName := "robot-ensemble-synth-demo"
	completedAt := now
	state := &ensemble.EnsembleSession{
		SessionName:       sessName,
		Question:          "Audit authentication and worktree boundaries",
		PresetUsed:        "security-review",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "deductive",
				PaneName:    sessName + "__cc_1",
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				OutputPath:  outPathA,
				AssignedAt:  now,
				CompletedAt: &completedAt,
			},
			{
				ModeID:     "bayesian",
				PaneName:   sessName + "__cod_1",
				AgentType:  "cod",
				Status:     ensemble.AssignmentActive,
				OutputPath: outPathB,
				AssignedAt: now,
			},
		},
	}
	if err := ensemble.SaveSession(sessName, state); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	cfg := config.Default()

	// Without Force=true, active assignment refuses with SYNTHESIS_NOT_READY.
	notReady, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session: sessName,
	}, cfg)
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize(notReady): %v", err)
	}
	if notReady.Success || notReady.ErrorCode != ErrCodeSynthesisNotReady {
		t.Fatalf("notReady = (%v, %q), want (false, %q)", notReady.Success, notReady.ErrorCode, ErrCodeSynthesisNotReady)
	}

	// With Force=true and Output set, synthesis succeeds, invokes OnProgress stages, and persists output atomically.
	savedReportPath := filepath.Join(outDir, "reports", "synthesis.md")
	var stages []string
	forcedOut, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session:  sessName,
		Strategy: "consensus",
		Format:   "markdown",
		Output:   savedReportPath,
		Force:    true,
		OnProgress: func(stage string) error {
			stages = append(stages, stage)
			return nil
		},
	}, cfg)
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize(force): %v", err)
	}
	if !forcedOut.Success {
		t.Fatalf("forced synthesis failed: code=%q err=%q", forcedOut.ErrorCode, forcedOut.Error)
	}
	if forcedOut.Status != "complete" || forcedOut.Report == nil || forcedOut.Result == nil || forcedOut.Audit == nil {
		t.Fatalf("unexpected forced synthesis output: %+v", forcedOut)
	}
	if forcedOut.Report.FindingsCount == 0 || forcedOut.Report.OutputPath != savedReportPath {
		t.Fatalf("unexpected synthesis report metadata: %+v", forcedOut.Report)
	}
	if len(stages) != 2 || stages[0] != "collecting_outputs" || stages[1] != "synthesizing" {
		t.Fatalf("progress stages = %v, want [collecting_outputs synthesizing]", stages)
	}
	persistedBytes, err := os.ReadFile(savedReportPath)
	if err != nil {
		t.Fatalf("read persisted synthesis file: %v", err)
	}
	if !strings.Contains(string(persistedBytes), "Audience validation") {
		t.Fatalf("persisted synthesis markdown missing finding text: %s", string(persistedBytes))
	}

	// Relative Output resolves under opts.ProjectDir rather than process CWD.
	projectDir := t.TempDir()
	relOut, err := GetEnsembleSynthesize(context.Background(), EnsembleSynthesizeOptions{
		Session:    sessName,
		ProjectDir: projectDir,
		Strategy:   "consensus",
		Format:     "markdown",
		Output:     filepath.Join("reports", "relative-synthesis.md"),
		Force:      true,
	}, cfg)
	if err != nil {
		t.Fatalf("GetEnsembleSynthesize(relative output): %v", err)
	}
	wantRelPath := filepath.Join(projectDir, "reports", "relative-synthesis.md")
	if !relOut.Success || relOut.Report == nil || relOut.Report.OutputPath != wantRelPath {
		t.Fatalf("relative output = (%v, %+v), want OutputPath %q", relOut.Success, relOut.Report, wantRelPath)
	}
	if _, err := os.Stat(wantRelPath); err != nil {
		t.Fatalf("expected relative synthesis output under ProjectDir %q: %v", wantRelPath, err)
	}

	// Schema registry includes ensemble_synthesize.
	schemaOut, err := GetSchema("ensemble_synthesize")
	if err != nil {
		t.Fatalf("GetSchema(ensemble_synthesize): %v", err)
	}
	if !schemaOut.Success || schemaOut.Schema == nil {
		t.Fatalf("expected valid JSON schema for ensemble_synthesize, got %+v", schemaOut)
	}
}

func writeRobotTestModeOutput(t *testing.T, path string, out ensemble.ModeOutput) {
	t.Helper()
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write mode output %s: %v", path, err)
	}
}
