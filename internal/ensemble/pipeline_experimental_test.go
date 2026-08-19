//go:build ensemble_experimental
// +build ensemble_experimental

package ensemble

import (
	"strings"
	"testing"
)

func TestValidatePipelineConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *EnsembleConfig
		wantErr bool
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: true,
		},
		{
			name:    "missing session name",
			cfg:     &EnsembleConfig{Question: "test?", Ensemble: "test"},
			wantErr: true,
		},
		{
			name:    "missing question",
			cfg:     &EnsembleConfig{SessionName: "test", Ensemble: "test"},
			wantErr: true,
		},
		{
			name:    "missing modes and ensemble",
			cfg:     &EnsembleConfig{SessionName: "test", Question: "test?"},
			wantErr: true,
		},
		{
			name:    "both modes and ensemble",
			cfg:     &EnsembleConfig{SessionName: "test", Question: "test?", Ensemble: "test", Modes: []string{"A1"}},
			wantErr: true,
		},
		{
			name:    "valid with ensemble",
			cfg:     &EnsembleConfig{SessionName: "test", Question: "test?", Ensemble: "test"},
			wantErr: false,
		},
		{
			name:    "valid with modes",
			cfg:     &EnsembleConfig{SessionName: "test", Question: "test?", Modes: []string{"A1", "B1"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePipelineConfig(tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestEnsembleManager_RunStage1_NilConfig(t *testing.T) {
	m := &EnsembleManager{}
	_, err := m.RunStage1(nil, nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestEnsembleManager_RunStage2_NilConfig(t *testing.T) {
	m := &EnsembleManager{}
	_, err := m.RunStage2(nil, nil, nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestEnsembleManager_RunStage3_NilConfig(t *testing.T) {
	m := &EnsembleManager{}
	_, err := m.RunStage3(nil, nil, nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func stage3TestOutputs() []ModeOutput {
	return []ModeOutput{
		{
			ModeID:     "deductive",
			Thesis:     "Thesis A",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Finding A", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "systems-thinking",
			Thesis:     "Thesis B",
			Confidence: 0.6,
			TopFindings: []Finding{
				{Finding: "Finding B", Impact: ImpactMedium, Confidence: 0.7},
			},
		},
	}
}

func TestEnsembleManager_RunStage3_ManualStrategyStaysMechanical(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetDefaultStateStoreForTest()
	defer resetDefaultStateStoreForTest()
	m := &EnsembleManager{}
	cfg := &EnsembleConfig{
		SessionName: "test-manual",
		Question:    "test?",
		Ensemble:    "test",
		Synthesis:   SynthesisConfig{Strategy: StrategyManual},
	}
	result, err := m.RunStage3(nil, cfg, stage3TestOutputs())
	if err != nil {
		t.Fatalf("RunStage3: %v", err)
	}
	if result.Report.Strategy != "mechanical" {
		t.Errorf("strategy = %q, want mechanical", result.Report.Strategy)
	}
	for _, entry := range result.Report.AuditLog {
		if entry.Action == "fallback_to_mechanical" {
			t.Errorf("manual strategy must not record fallback: %+v", entry)
		}
	}
}

func TestEnsembleManager_RunStage3_AgentStrategyRecordsExplicitFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetDefaultStateStoreForTest()
	defer resetDefaultStateStoreForTest()
	m := &EnsembleManager{}
	cfg := &EnsembleConfig{
		SessionName: "test-agent-fallback",
		Question:    "test?",
		Ensemble:    "test",
		Synthesis:   SynthesisConfig{Strategy: StrategyConsensus},
	}
	// No session state / no live panes: the lead-agent path is unavailable,
	// so stage 3 must degrade to mechanical with an explicit audit entry.
	result, err := m.RunStage3(nil, cfg, stage3TestOutputs())
	if err != nil {
		t.Fatalf("RunStage3: %v", err)
	}
	if result.Report == nil {
		t.Fatal("expected mechanical report")
	}
	var found bool
	for _, entry := range result.Report.AuditLog {
		if entry.Action == "fallback_to_mechanical" {
			found = true
			if !strings.HasPrefix(entry.Details, "fell back to mechanical: ") {
				t.Errorf("fallback details missing prefix: %q", entry.Details)
			}
		}
	}
	if !found {
		t.Error("agent strategy without a lead pane must record an explicit fallback_to_mechanical audit entry")
	}
}

func TestSynthesisResultToReport(t *testing.T) {
	result := &SynthesisResult{
		Summary:    "Unified thesis.",
		Findings:   []Finding{{Finding: "F1", Impact: ImpactHigh, Confidence: 0.9}},
		Risks:      []Risk{{Risk: "R1", Impact: ImpactMedium}},
		Confidence: 0.75,
	}
	strategy, err := GetStrategy(string(StrategyConsensus))
	if err != nil {
		t.Fatalf("GetStrategy: %v", err)
	}
	report := synthesisResultToReport(result, strategy)
	if report.Strategy != "agent:consensus" {
		t.Errorf("strategy = %q, want agent:consensus", report.Strategy)
	}
	if report.ConsolidatedThesis != "Unified thesis." {
		t.Errorf("thesis = %q", report.ConsolidatedThesis)
	}
	if len(report.TopFindings) != 1 || len(report.UnifiedRisks) != 1 {
		t.Errorf("report content not mapped: %+v", report)
	}
	if len(report.AuditLog) != 1 || report.AuditLog[0].Action != "agent_synthesis" {
		t.Errorf("audit log missing agent_synthesis entry: %+v", report.AuditLog)
	}
}

func TestEnsembleManager_RunStage3_EmptyOutputs(t *testing.T) {
	m := &EnsembleManager{}
	cfg := &EnsembleConfig{
		SessionName: "test",
		Question:    "test?",
		Ensemble:    "test",
	}
	_, err := m.RunStage3(nil, cfg, []ModeOutput{})
	if err == nil {
		t.Error("expected error for empty outputs")
	}
}
