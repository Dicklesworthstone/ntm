package ensemble

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

func TestEnsembleManager_RunStage3CanceledNeverReportsMechanicalSuccess(t *testing.T) {
	for _, strategy := range []SynthesisStrategy{StrategyManual, StrategyConsensus} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			manager := NewEnsembleManager()
			result, err := manager.RunStage3(ctx, &EnsembleConfig{
				SessionName: "canceled-stage3",
				Question:    "Should this cancellation produce a report?",
				Synthesis:   SynthesisConfig{Strategy: strategy},
			}, stage3TestOutputs())
			if result != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled stage 3 reported result=%+v, err=%v", result, err)
			}
		})
	}
}

func TestCollectAssignedModeOutputsPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := NewEnsembleManager()
	outputs, _, err := manager.collectAssignedModeOutputs(ctx, "canceled-collection", []ModeAssignment{
		{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: AssignmentActive},
	}, time.Minute)
	if len(outputs) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled output collection lost context: outputs=%+v, err=%v", outputs, err)
	}
}

func installSynthesisLeadTmux(t *testing.T, panes, capture string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
case "$1" in
  list-panes) printf '%s\n' "$NTM_TEST_LEAD_PANES" ;;
  capture-pane)
    if [ "$NTM_TEST_LEAD_CAPTURE_FAIL" = "1" ]; then
      printf '%s\n' 'capture unavailable' >&2
      exit 1
    fi
    printf '%s\n' "$NTM_TEST_LEAD_CAPTURE"
    ;;
  *) printf 'unexpected tmux mutation: %s\n' "$*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", path)
	t.Setenv("NTM_TEST_LEAD_PANES", panes)
	t.Setenv("NTM_TEST_LEAD_CAPTURE", capture)
	t.Setenv("NTM_TEST_LEAD_CAPTURE_FAIL", "")
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
}

func synthesisLeadPaneLine(id, title, command, agentType, service, dead, rotation string) string {
	return strings.Join([]string{id, "1", title, command, "120", "40", "0", "0", "0", agentType, "", service, dead, "[" + rotation + "]"}, tmux.FieldSeparator)
}

func TestConfigureSessionSynthesisUsesLiveIdleLeadAndBaseline(t *testing.T) {
	const sessionName = "lead-session"
	baseline := leadAgentResponse + "\nclaude>"
	installSynthesisLeadTmux(t, synthesisLeadPaneLine("%7", sessionName+"__cc_1", "claude", "cc", "", "0", ""), baseline)
	manager := NewEnsembleManager()
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyConsensus})
	if err != nil {
		t.Fatal(err)
	}
	session := &EnsembleSession{SessionName: sessionName, Assignments: []ModeAssignment{
		{ModeID: "deductive", PaneName: sessionName + "__cc_1", AgentType: "cc", Status: AssignmentDone},
	}}
	if err := manager.ConfigureSessionSynthesis(context.Background(), session, synth, time.Second); err != nil {
		t.Fatal(err)
	}
	if synth.Lead == nil || synth.Lead.PaneTarget != "%7" || synth.Lead.AgentType != "cc" || synth.Lead.Timeout != time.Second {
		t.Fatalf("live lead was not wired: %+v; fallback=%q", synth.Lead, synth.leadUnavailableReason)
	}
	collector, ok := synth.Lead.Collector.(*PaneSynthesisCollector)
	if !ok || collector.Baseline != baseline {
		t.Fatalf("lead lost pre-dispatch scrollback baseline: %#v", synth.Lead.Collector)
	}
	if _, ok := synth.Lead.Dispatcher.(promptInjectorDispatcher); !ok {
		t.Fatalf("lead bypasses gated delivery: %T", synth.Lead.Dispatcher)
	}
}

func TestConfigureSessionSynthesisRefusesUnsafeLeadPanes(t *testing.T) {
	const sessionName = "unsafe-lead"
	for _, tc := range []struct {
		name        string
		command     string
		agentType   string
		service     string
		dead        string
		rotation    string
		capture     string
		status      AssignmentStatus
		captureFail bool
		duplicate   bool
	}{
		{name: "active work", command: "claude", agentType: "cc", dead: "0", capture: "Analyzing the results...", status: AssignmentActive},
		{name: "exited shell", command: "bash", agentType: "cc", dead: "0", capture: "claude>", status: AssignmentDone},
		{name: "dead pane", command: "claude", agentType: "cc", dead: "1", capture: "claude>", status: AssignmentDone},
		{name: "service pane", command: "claude", agentType: "", service: "cm", dead: "0", capture: "claude>", status: AssignmentDone},
		{name: "changed provider", command: "codex", agentType: "cod", dead: "0", capture: "claude>", status: AssignmentDone},
		{name: "rotating pane", command: "claude", agentType: "cc", rotation: "handoff-42", dead: "0", capture: "claude>", status: AssignmentDone},
		{name: "pending assignment", command: "claude", agentType: "cc", dead: "0", capture: "claude>", status: AssignmentPending},
		{name: "injecting assignment", command: "claude", agentType: "cc", dead: "0", capture: "claude>", status: AssignmentInjecting},
		{name: "missing baseline", command: "claude", agentType: "cc", dead: "0", capture: "claude>", status: AssignmentDone, captureFail: true},
		{name: "ambiguous title", command: "claude", agentType: "cc", dead: "0", capture: "claude>", status: AssignmentDone, duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panes := synthesisLeadPaneLine("%1", sessionName+"__cc_1", tc.command, tc.agentType, tc.service, tc.dead, tc.rotation)
			if tc.duplicate {
				panes += "\n" + synthesisLeadPaneLine("%2", sessionName+"__cc_1", tc.command, tc.agentType, tc.service, tc.dead, tc.rotation)
			}
			installSynthesisLeadTmux(t, panes, tc.capture)
			if tc.captureFail {
				t.Setenv("NTM_TEST_LEAD_CAPTURE_FAIL", "1")
			}
			manager := NewEnsembleManager()
			synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyConsensus})
			if err != nil {
				t.Fatal(err)
			}
			session := &EnsembleSession{SessionName: sessionName, Assignments: []ModeAssignment{
				{ModeID: "deductive", PaneName: sessionName + "__cc_1", AgentType: "cc", Status: tc.status},
			}}
			if err := manager.ConfigureSessionSynthesis(context.Background(), session, synth, time.Second); err != nil {
				t.Fatal(err)
			}
			if synth.Lead != nil || !strings.Contains(synth.leadUnavailableReason, "lead pane available") {
				t.Fatalf("unsafe lead selected: lead=%+v; fallback=%q", synth.Lead, synth.leadUnavailableReason)
			}
			result, err := synth.SynthesizeContext(context.Background(), leadTestInput())
			if err != nil || result.SynthesizedBy != SynthesisPathMechanical || !strings.Contains(result.FallbackReason, synth.leadUnavailableReason) {
				t.Fatalf("unavailable lead lacks explicit usable fallback: result=%+v, err=%v", result, err)
			}
		})
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

type pipelineInterruptLogHandler struct {
	message   string
	interrupt func()
}

func (h pipelineInterruptLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h pipelineInterruptLogHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == h.message {
		h.interrupt()
	}
	return nil
}
func (h pipelineInterruptLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h pipelineInterruptLogHandler) WithGroup(string) slog.Handler      { return h }

func TestCollectAssignedModeOutputsCancellationAfterFinalCapture(t *testing.T) {
	const sessionName = "final-capture-canceled"
	raw := "```yaml\nmode_id: deductive\nthesis: Keep the completed reasoning.\n" +
		"top_findings:\n  - finding: Cancellation must not report a completed stage.\n" +
		"    impact: high\n    confidence: 0.9\nconfidence: 0.9\n```\nclaude>\n"
	installSynthesisLeadTmux(t, synthesisLeadPaneLine("%1", sessionName+"__cc_1", "claude", "cc", "", "0", ""), raw)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	oldLogger := slog.Default()
	// Capture logs synchronously after parsing and before returning its final
	// result, so this cancels exactly when a valid last output is available.
	slog.SetDefault(slog.New(pipelineInterruptLogHandler{message: "ensemble output captured", interrupt: cancel}))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	manager := NewEnsembleManager()
	outputs, byMode, err := manager.collectAssignedModeOutputs(ctx, sessionName, []ModeAssignment{
		{ModeID: "deductive", PaneName: sessionName + "__cc_1", AgentType: "cc", Status: AssignmentActive},
	}, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("last valid capture hid caller cancellation: outputs=%+v, err=%v", outputs, err)
	}
	if len(outputs) != 1 || len(byMode) != 1 || outputs[0].Thesis != "Keep the completed reasoning." {
		t.Fatalf("cancellation discarded captured evidence: outputs=%+v, byMode=%+v", outputs, byMode)
	}
}

func TestCollectAssignedModeOutputsRejectsInvalidCapturedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{
			name: "wrong mode",
			output: "```yaml\nmode_id: bayesian\nthesis: A result for a different assignment.\n" +
				"top_findings:\n  - finding: Valid schema is insufficient without matching identity.\n" +
				"    impact: high\n    confidence: 0.9\nconfidence: 0.9\n```\nclaude>\n",
		},
		{
			name: "invalid schema",
			output: "```yaml\nmode_id: deductive\nthesis: \"\"\n" +
				"top_findings:\n  - finding: An empty thesis does not satisfy the mode schema.\n" +
				"    impact: high\n    confidence: 0.9\nconfidence: 0.9\n```\nclaude>\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sessionName = "invalid-capture"
			installSynthesisLeadTmux(t, synthesisLeadPaneLine("%1", sessionName+"__cc_1", "claude", "cc", "", "0", ""), tc.output)
			manager := NewEnsembleManager()
			outputs, byMode, err := manager.collectAssignedModeOutputs(t.Context(), sessionName, []ModeAssignment{
				{ModeID: "deductive", PaneName: sessionName + "__cc_1", AgentType: "cc", Status: AssignmentActive},
			}, 300*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) || len(outputs) != 0 || len(byMode) != 0 {
				t.Fatalf("invalid capture completed a pipeline stage: outputs=%+v, byMode=%+v, err=%v", outputs, byMode, err)
			}
		})
	}
}

func TestFinishPipelineSynthesisPreservesCallerInterruption(t *testing.T) {
	for _, tc := range []struct {
		name       string
		logMessage string
		skip       bool
		noOutputs  bool
		deadline   bool
		wantStage  PipelineStage
	}{
		{name: "after collection before skipped synthesis", skip: true, wantStage: StageModeRun},
		{name: "after collection without outputs", noOutputs: true, wantStage: StageModeRun},
		{name: "caller deadline before synthesis", deadline: true, wantStage: StageModeRun},
		{name: "during stage3", logMessage: "stage3: synthesis starting", wantStage: StageSynthesis},
		{name: "after stage3 returns output", logMessage: "stage3: synthesis complete", wantStage: StageSynthesis},
		{name: "during finalization", logMessage: "ensemble pipeline complete", wantStage: StageSynthesis},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			wantErr := error(context.Canceled)
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				wantErr = context.DeadlineExceeded
			} else if tc.logMessage == "" {
				cancel()
			}
			defer cancel()
			manager := NewEnsembleManager()
			if tc.logMessage != "" {
				manager.Logger = slog.New(pipelineInterruptLogHandler{message: tc.logMessage, interrupt: cancel})
			}
			outputs := stage3TestOutputs()
			if tc.noOutputs {
				outputs = nil
			}
			result := &EnsembleResult{
				SessionName: "interrupted-pipeline",
				Question:    "Can cancellation complete a pipeline?",
				Stage:       StageSynthesis,
				StartedAt:   time.Now().UTC(),
				ModeOutputs: outputs,
				Metrics:     &PipelineMetrics{},
			}
			cfg := &EnsembleConfig{
				SessionName: result.SessionName,
				Question:    result.Question,
				Synthesis:   SynthesisConfig{Strategy: StrategyManual},
			}
			got, err := manager.finishPipelineSynthesis(ctx, cfg, RunConfig{SkipStage3: tc.skip}, result)
			var pipelineErr *PipelineError
			if !errors.Is(err, wantErr) || !errors.As(err, &pipelineErr) || pipelineErr.Stage != tc.wantStage {
				t.Fatalf("pipeline interruption lost cause or stage: result=%+v, err=%v", got, err)
			}
			if got == nil || got.Stage != StageFailed || got.CompletedAt.IsZero() || got.Error == "" {
				t.Fatalf("interrupted pipeline reported success: %+v", got)
			}
			if len(got.ModeOutputs) != len(outputs) {
				t.Fatalf("interruption discarded member output: got=%+v, want=%+v", got.ModeOutputs, outputs)
			}
		})
	}
}

func TestFinishPipelineSynthesisUnavailableLeadRetainsMechanicalResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetDefaultStateStoreForTest()
	t.Cleanup(resetDefaultStateStoreForTest)
	manager := NewEnsembleManager()
	result := &EnsembleResult{
		SessionName: "missing-pipeline-lead",
		Question:    "Can saved reasoning remain useful without a lead?",
		Stage:       StageSynthesis,
		StartedAt:   time.Now().UTC(),
		ModeOutputs: stage3TestOutputs(),
		Metrics:     &PipelineMetrics{},
	}
	cfg := &EnsembleConfig{
		SessionName: result.SessionName,
		Question:    result.Question,
		Synthesis:   SynthesisConfig{Strategy: StrategyConsensus},
	}
	got, err := manager.finishPipelineSynthesis(t.Context(), cfg, RunConfig{}, result)
	if err != nil || got.Stage != StageComplete || got.Synthesis == nil || got.Synthesis.Strategy != "mechanical" {
		t.Fatalf("unavailable lead should retain the completed mechanical result: result=%+v, err=%v", got, err)
	}
	foundFallback := false
	for _, entry := range got.Synthesis.AuditLog {
		if entry.Action == "fallback_to_mechanical" && strings.Contains(entry.Details, "no lead pane available") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatalf("mechanical fallback lost its reason: %+v", got.Synthesis.AuditLog)
	}
}
