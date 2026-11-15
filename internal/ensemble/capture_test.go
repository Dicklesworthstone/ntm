package ensemble

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestOutputCapture_CaptureAll_NilCapture(t *testing.T) {
	var capture *OutputCapture
	_, err := capture.CaptureAll(&EnsembleSession{})
	if err == nil {
		t.Error("expected error for nil capture")
	}
}

func TestOutputCapture_CaptureAll_NilSession(t *testing.T) {
	capture := NewOutputCapture(nil)
	_, err := capture.CaptureAll(nil)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

func TestOutputCapture_ExtractYAML_CodeBlock(t *testing.T) {
	capture := NewOutputCapture(nil)

	tests := []struct {
		name     string
		input    string
		wantYAML bool
		contains string
	}{
		{
			name:     "yaml code block",
			input:    "Some text\n```yaml\nthesis: Test thesis\nconfidence: 0.8\n```\nMore text",
			wantYAML: true,
			contains: "thesis: Test thesis",
		},
		{
			name:     "thesis line fallback",
			input:    "Some output\nthesis: Found thesis here\nconfidence: 0.9",
			wantYAML: true,
			contains: "thesis: Found thesis here",
		},
		{
			name:     "no yaml content",
			input:    "Just plain text without any YAML",
			wantYAML: false,
			contains: "",
		},
		{
			name:     "empty input",
			input:    "",
			wantYAML: false,
			contains: "",
		},
		{
			name:     "multiple yaml blocks picks valid",
			input:    "```yaml\ninvalid: [broken\n```\n```yaml\nthesis: Valid block\nconfidence: 0.5\n```",
			wantYAML: true,
			contains: "thesis: Valid block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml, found := capture.extractYAML(tt.input)
			if found != tt.wantYAML {
				t.Errorf("extractYAML found = %v, want %v", found, tt.wantYAML)
			}
			if tt.contains != "" && !captureContains(yaml, tt.contains) {
				t.Errorf("extractYAML yaml = %q, want contains %q", yaml, tt.contains)
			}
		})
	}
}

func TestOutputCapture_CapturePane_EmptyPane(t *testing.T) {
	capture := NewOutputCapture(nil)
	_, err := capture.capturePane(t.Context(), "")
	if err == nil {
		t.Error("expected error for empty pane")
	}
}

func TestCapturedOutput_Fields(t *testing.T) {
	captured := CapturedOutput{
		ModeID:        "test-mode",
		PaneName:      "test-pane",
		RawOutput:     "test output",
		LineCount:     5,
		TokenEstimate: 100,
	}

	if captured.ModeID != "test-mode" {
		t.Errorf("ModeID = %q, want test-mode", captured.ModeID)
	}
	if captured.PaneName != "test-pane" {
		t.Errorf("PaneName = %q, want test-pane", captured.PaneName)
	}
	if captured.LineCount != 5 {
		t.Errorf("LineCount = %d, want 5", captured.LineCount)
	}
	if captured.TokenEstimate != 100 {
		t.Errorf("TokenEstimate = %d, want 100", captured.TokenEstimate)
	}
}

func TestCountLines_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"a\nb\nc", 3},
		{"\n", 1},   // Single newline = one empty line
		{"\n\n", 2}, // Two newlines = two empty lines
	}

	for _, tt := range tests {
		got := countLines(tt.input)
		if got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEstimateModeOutputTokens_Coverage(t *testing.T) {
	tests := []struct {
		name   string
		output *ModeOutput
		wantGt int // want greater than
	}{
		{
			name:   "nil output",
			output: nil,
			wantGt: -1, // 0
		},
		{
			name: "with raw output",
			output: &ModeOutput{
				RawOutput: "This is raw output text for token estimation",
			},
			wantGt: 0,
		},
		{
			name: "with thesis only",
			output: &ModeOutput{
				Thesis: "This is a thesis statement",
			},
			wantGt: 0,
		},
		{
			name: "with findings",
			output: &ModeOutput{
				Thesis: "Thesis",
				TopFindings: []Finding{
					{Finding: "Finding one with details"},
					{Finding: "Finding two with more details"},
				},
			},
			wantGt: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateModeOutputTokens(tt.output)
			if got <= tt.wantGt {
				t.Errorf("EstimateModeOutputTokens() = %d, want > %d", got, tt.wantGt)
			}
		})
	}
}

// captureContains is a helper to check substring
func captureContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestOutputCapture_DefaultsAndLineCount(t *testing.T) {
	capture := &OutputCapture{}
	capture.ensureDefaults()
	if capture.tmuxClient == nil {
		t.Fatal("expected tmux client to be set")
	}
	if capture.validator == nil {
		t.Fatal("expected validator to be set")
	}
	if capture.maxLines != defaultCaptureLines {
		t.Fatalf("maxLines = %d, want %d", capture.maxLines, defaultCaptureLines)
	}

	if countLines("") != 0 {
		t.Fatal("countLines should be 0 for empty string")
	}
	if countLines("a\n") != 1 {
		t.Fatal("countLines should ignore trailing newline")
	}
	if countLines("a\nb") != 2 {
		t.Fatal("countLines should count lines")
	}
}

func TestSaveCapturedOutputs_PreservesRecoveryAcrossReopenAndRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetDefaultStateStoreForTest()
	t.Cleanup(CloseDefaultStateStore)

	session := &EnsembleSession{
		SessionName: "capture-recovery",
		Question:    "Which results survive stopping the agents?",
		Status:      EnsembleActive,
		Assignments: []ModeAssignment{
			{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: AssignmentActive},
			{ModeID: "inductive", PaneName: "%2", AgentType: "cod", Status: AssignmentActive},
		},
	}
	if err := SaveSession(session.SessionName, session); err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Now().UTC()
	captured := []CapturedOutput{
		{
			ModeID:     "deductive",
			PaneName:   "%1",
			RawOutput:  "complete raw transcript\n",
			CapturedAt: capturedAt,
			Parsed: &ModeOutput{
				ModeID:      "deductive",
				Thesis:      "Keep recoverable outputs before stopping.",
				Confidence:  0.8,
				TopFindings: []Finding{{Finding: "Agent output is recoverable.", Impact: ImpactHigh, Confidence: 0.8}},
			},
		},
		{ModeID: "inductive", PaneName: "%2", RawOutput: "unfinished response\n", CapturedAt: capturedAt},
	}
	if err := saveCapturedOutputs(session, captured); err != nil {
		t.Fatalf("save captures: %v", err)
	}
	if session.Status != EnsembleActive {
		t.Fatalf("capture changed session status to %s", session.Status)
	}
	for i, output := range captured {
		if !filepath.IsAbs(output.OutputPath) {
			t.Fatalf("capture %d has no absolute recovery path: %q", i, output.OutputPath)
		}
		data, err := os.ReadFile(output.OutputPath)
		if err != nil || string(data) != output.RawOutput {
			t.Fatalf("raw capture %d = %q, %v", i, data, err)
		}
		info, err := os.Stat(output.OutputPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("capture %d is not private: %v, %v", i, info, err)
		}
		dirInfo, err := os.Stat(filepath.Dir(output.OutputPath))
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("capture directory is not private: %v, %v", dirInfo, err)
		}
	}
	validPath := session.Assignments[0].OutputPath
	if validPath == captured[0].OutputPath {
		t.Fatal("valid assignment must reference structured output, not the raw transcript")
	}
	data, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved ModeOutput
	if err := json.Unmarshal(data, &saved); err != nil || saved.RawOutput != captured[0].RawOutput {
		t.Fatalf("structured output lost the original transcript: %+v, %v", saved, err)
	}
	if session.Assignments[0].Status != AssignmentDone || session.Assignments[0].CompletedAt == nil {
		t.Fatal("valid output did not complete its assignment")
	}
	if session.Assignments[1].Status != AssignmentActive || session.Assignments[1].CompletedAt != nil ||
		session.Assignments[1].OutputPath != captured[1].OutputPath {
		t.Fatalf("partial output was incorrectly completed or lost: %+v", session.Assignments[1])
	}

	CloseDefaultStateStore()
	reloaded, err := LoadSession(session.SessionName)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Assignments[0].OutputPath != validPath || reloaded.Assignments[0].Status != AssignmentDone ||
		reloaded.Assignments[1].OutputPath != captured[1].OutputPath {
		t.Fatalf("reopened store lost recovery metadata: %+v", reloaded.Assignments)
	}
	collector, err := CollectModeOutputs(reloaded, nil)
	if err != nil || collector.Count() != 1 || collector.Outputs[0].ModeID != "deductive" {
		t.Fatalf("offline collection = %+v, %v", collector, err)
	}

	retry := []CapturedOutput{
		{ModeID: "deductive", PaneName: "%1", RawOutput: "later incomplete response", ParseErrors: []error{errors.New("incomplete")}},
		{ModeID: "inductive", PaneName: "%2", RawOutput: "more partial work"},
	}
	if err := saveCapturedOutputs(reloaded, retry); err != nil {
		t.Fatal(err)
	}
	if reloaded.Assignments[0].OutputPath != validPath || reloaded.Assignments[0].Status != AssignmentDone {
		t.Fatal("incomplete retry replaced the last valid output")
	}
	for i := range retry {
		if retry[i].OutputPath == captured[i].OutputPath {
			t.Fatal("retry overwrote an earlier capture")
		}
		data, err := os.ReadFile(captured[i].OutputPath)
		if err != nil || string(data) != captured[i].RawOutput {
			t.Fatalf("retry damaged original capture %d: %q, %v", i, data, err)
		}
	}
}

func TestSaveCapturedOutputs_FailureDoesNotPublishAssignmentChanges(t *testing.T) {
	for _, failure := range []string{"output-directory", "state-save", "missing-capture", "wrong-pane"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("NTM_CONFIG", "")
			resetDefaultStateStoreForTest()
			t.Cleanup(CloseDefaultStateStore)

			session := &EnsembleSession{
				SessionName: "capture-failure",
				Question:    "Preserve state if capture cannot be saved.",
				Status:      EnsembleActive,
				Assignments: []ModeAssignment{
					{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: AssignmentActive},
				},
			}
			if err := SaveSession(session.SessionName, session); err != nil {
				t.Fatal(err)
			}
			before := append([]ModeAssignment(nil), session.Assignments...)
			captured := []CapturedOutput{{
				ModeID:    "deductive",
				PaneName:  "%1",
				RawOutput: "recoverable work",
				Parsed: &ModeOutput{
					ModeID:      "deductive",
					Thesis:      "A completed result.",
					Confidence:  0.8,
					TopFindings: []Finding{{Finding: "A finding.", Impact: ImpactHigh, Confidence: 0.8}},
				},
			}}
			switch failure {
			case "output-directory":
				store, err := defaultSQLiteStore()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(filepath.Dir(store.store.Path()), "ensemble-outputs")
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "state-save":
				session.Question = ""
			case "missing-capture":
				captured = nil
			case "wrong-pane":
				captured[0].PaneName = "%99"
			}
			if err := saveCapturedOutputs(session, captured); err == nil {
				t.Fatal("expected persistence failure")
			}
			if session.Status != EnsembleActive || !reflect.DeepEqual(session.Assignments, before) {
				t.Fatalf("failure published assignment changes: %+v", session.Assignments)
			}
			reloaded, err := LoadSession(session.SessionName)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Status != EnsembleActive || !reflect.DeepEqual(reloaded.Assignments, before) {
				t.Fatalf("failure changed durable assignment state: %+v", reloaded.Assignments)
			}
		})
	}
}

func TestOutputCaptureCanceledBeforeAnyObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	capture := NewOutputCapture(nil)
	session := &EnsembleSession{
		SessionName: "not-observed",
		Assignments: []ModeAssignment{{ModeID: "deductive", PaneName: "%1", Status: AssignmentActive}},
	}
	for _, run := range []func(context.Context, *EnsembleSession) ([]CapturedOutput, error){
		capture.CaptureAllContext,
		capture.CaptureAndSaveContext,
		capture.CaptureForSynthesis,
	} {
		captured, err := run(ctx, session)
		if !errors.Is(err, context.Canceled) || len(captured) != 0 {
			t.Fatalf("canceled capture = %+v, %v", captured, err)
		}
		if session.Assignments[0].Status != AssignmentActive || session.Assignments[0].OutputPath != "" {
			t.Fatalf("canceled observation changed assignment: %+v", session.Assignments[0])
		}
	}
}

func savedSynthesisCaptureOutput(t *testing.T, modeID string) (ModeOutput, string) {
	t.Helper()
	output := ModeOutput{
		ModeID:      modeID,
		Thesis:      "Preserve this completed analysis.",
		Confidence:  0.8,
		TopFindings: []Finding{{Finding: "The result remains recoverable.", Impact: ImpactHigh, Confidence: 0.8}},
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "saved.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return output, path
}

// synthesisCaptureWithLivePane exposes only %2, so touching saved, queued, or
// failed assignments with other pane identities causes the test to fail.
func synthesisCaptureWithLivePane(t *testing.T, transcript string) *OutputCapture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux requires a POSIX shell")
	}
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "transcript.txt")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_SYNTHESIS_CAPTURE_TRANSCRIPT", transcriptPath)
	script := strings.Join([]string{
		"#!/bin/sh",
		"case \"$1\" in",
		"  list-panes) printf '%s\\n' '%2_NTM_SEP_1_NTM_SEP_capture-subset__cc_2_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]' ;;",
		"  capture-pane) cat \"$NTM_SYNTHESIS_CAPTURE_TRANSCRIPT\" ;;",
		"  *) exit 1 ;;",
		"esac",
	}, "\n") + "\n"
	binaryPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binaryPath)
	return NewOutputCapture(tmux.NewClient(""))
}

func TestCaptureForSynthesisUsesSavedOutputsWithoutTmux(t *testing.T) {
	_, firstPath := savedSynthesisCaptureOutput(t, "deductive")
	_, secondPath := savedSynthesisCaptureOutput(t, "inductive")
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(t.TempDir(), "missing-tmux"))
	capture := NewOutputCapture(tmux.NewClient(""))
	session := &EnsembleSession{
		SessionName: "saved-only",
		Assignments: []ModeAssignment{
			{ModeID: "deductive", PaneName: "%91", Status: AssignmentDone, OutputPath: firstPath},
			{ModeID: "inductive", PaneName: "%92", Status: AssignmentDone, OutputPath: secondPath},
		},
	}
	before := append([]ModeAssignment(nil), session.Assignments...)
	captured, err := capture.CaptureForSynthesis(t.Context(), session)
	if err != nil || len(captured) != 0 {
		t.Fatalf("saved-only synthesis tried to capture panes: %+v, %v", captured, err)
	}
	collected, err := CollectModeOutputs(session, captured)
	if err != nil || collected.Count() != 2 {
		t.Fatalf("saved results unavailable: %+v, %v", collected, err)
	}
	if !reflect.DeepEqual(session.Assignments, before) {
		t.Fatal("saved-only capture changed completed assignments")
	}

	for _, assignmentStatus := range []AssignmentStatus{AssignmentPending, AssignmentInjecting, AssignmentError} {
		session.Assignments = append(session.Assignments, ModeAssignment{
			ModeID: string(assignmentStatus), PaneName: "%missing", Status: assignmentStatus,
		})
	}
	if captured, err := capture.CaptureForSynthesis(t.Context(), session); err != nil || len(captured) != 0 {
		t.Fatalf("unstarted or failed assignments triggered capture: %+v, %v", captured, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := capture.CaptureForSynthesis(ctx, session); !errors.Is(err, context.Canceled) {
		t.Fatalf("saved output concealed cancellation: %v", err)
	}
	if _, err := capture.CaptureForSynthesis(nil, session); err == nil {
		t.Fatal("nil context accepted through saved-output fallback")
	}
}

func TestCaptureForSynthesisPreservesSavedAssignmentsWhileCompletingLiveSubset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetDefaultStateStoreForTest()
	t.Cleanup(CloseDefaultStateStore)
	_, savedPath := savedSynthesisCaptureOutput(t, "deductive")
	live, _ := savedSynthesisCaptureOutput(t, "inductive")
	data, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	capture := synthesisCaptureWithLivePane(t, "\x60\x60\x60json\n"+string(data)+"\n\x60\x60\x60\nclaude> \n")
	session := &EnsembleSession{
		SessionName: "capture-subset",
		Question:    "Combine durable and live work without losing either.",
		Status:      EnsembleActive,
		Assignments: []ModeAssignment{
			{ModeID: "deductive", PaneName: "%91", AgentType: "cc", Status: AssignmentDone, OutputPath: savedPath},
			{ModeID: "inductive", PaneName: "%2", AgentType: "cc", Status: AssignmentActive},
			{ModeID: "abductive", PaneName: "%92", AgentType: "cc", Status: AssignmentPending},
			{ModeID: "creative", PaneName: "%93", AgentType: "cc", Status: AssignmentInjecting},
			{ModeID: "analytical", PaneName: "%94", AgentType: "cc", Status: AssignmentError, Error: "earlier failure"},
		},
	}
	if err := SaveSession(session.SessionName, session); err != nil {
		t.Fatal(err)
	}
	before := append([]ModeAssignment(nil), session.Assignments...)
	captured, err := capture.CaptureForSynthesis(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || captured[0].ModeID != "inductive" || captured[0].PaneName != "%2" || captured[0].OutputPath == "" {
		t.Fatalf("unexpected capture subset: %+v", captured)
	}
	if session.Assignments[1].Status != AssignmentDone || session.Assignments[1].CompletedAt == nil {
		t.Fatalf("live assignment was not completed: %+v", session.Assignments[1])
	}
	for _, i := range []int{0, 2, 3, 4} {
		if !reflect.DeepEqual(session.Assignments[i], before[i]) {
			t.Fatalf("uncaptured assignment %d changed: %+v", i, session.Assignments[i])
		}
	}
	CloseDefaultStateStore()
	reloaded, err := LoadSession(session.SessionName)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Assignments, session.Assignments) {
		t.Fatalf("subset state did not survive reopen: %+v", reloaded.Assignments)
	}
	collected, err := CollectModeOutputs(reloaded, captured)
	if err != nil || collected.Count() != 2 || collected.ErrorCount() != 0 {
		t.Fatalf("saved and live merge = %+v, %v", collected, err)
	}
}

func TestCaptureForSynthesisRecoversInvalidSavedOutputOnlyWhenIdle(t *testing.T) {
	for _, idle := range []bool{false, true} {
		name := "working"
		if idle {
			name = "idle"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("NTM_CONFIG", "")
			resetDefaultStateStoreForTest()
			t.Cleanup(CloseDefaultStateStore)
			output, savedPath := savedSynthesisCaptureOutput(t, "inductive")
			if err := os.WriteFile(savedPath, []byte("invalid structured output"), 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			transcript := "\x60\x60\x60json\n" + string(data) + "\n\x60\x60\x60\nThinking...\n"
			if idle {
				transcript = "\x60\x60\x60json\n" + string(data) + "\n\x60\x60\x60\nclaude> \n"
			}
			capture := synthesisCaptureWithLivePane(t, transcript)
			session := &EnsembleSession{
				SessionName: "capture-subset",
				Question:    "Repair a missing durable result.",
				Status:      EnsembleActive,
				Assignments: []ModeAssignment{
					{ModeID: "inductive", PaneName: "%2", AgentType: "cc", Status: AssignmentDone, OutputPath: savedPath},
				},
			}
			if err := SaveSession(session.SessionName, session); err != nil {
				t.Fatal(err)
			}
			captured, err := capture.CaptureForSynthesis(t.Context(), session)
			if err != nil || len(captured) != 1 {
				t.Fatalf("capture = %+v, %v", captured, err)
			}
			if idle {
				if len(captured[0].ParseErrors) != 0 || session.Assignments[0].OutputPath == savedPath || session.Assignments[0].CompletedAt == nil {
					t.Fatalf("valid idle result did not repair saved output: %+v", session.Assignments[0])
				}
			} else if len(captured[0].ParseErrors) == 0 || session.Assignments[0].OutputPath != savedPath || session.Assignments[0].CompletedAt != nil {
				t.Fatalf("working result replaced saved state: %+v, %+v", captured, session.Assignments[0])
			}
		})
	}
}

func TestCaptureForSynthesisDoesNotHideMissingActivePane(t *testing.T) {
	_, savedPath := savedSynthesisCaptureOutput(t, "deductive")
	capture := synthesisCaptureWithLivePane(t, "claude> \n")
	session := &EnsembleSession{
		SessionName: "capture-subset",
		Assignments: []ModeAssignment{
			{ModeID: "deductive", PaneName: "%91", Status: AssignmentDone, OutputPath: savedPath},
			{ModeID: "inductive", PaneName: "%99", AgentType: "cc", Status: AssignmentActive},
		},
	}
	before := append([]ModeAssignment(nil), session.Assignments...)
	captured, err := capture.CaptureForSynthesis(t.Context(), session)
	if err == nil || !strings.Contains(err.Error(), "0 matches") || len(captured) != 1 {
		t.Fatalf("missing active pane was hidden by saved output: %+v, %v", captured, err)
	}
	if !reflect.DeepEqual(session.Assignments, before) {
		t.Fatal("failed capture changed assignment state")
	}
}

func TestSaveCapturedAssignmentOutputsRejectsInvalidSubsetWithoutPublishing(t *testing.T) {
	for _, failure := range []string{"state-save", "wrong-pane", "duplicate-capture", "duplicate-assignment"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("NTM_CONFIG", "")
			resetDefaultStateStoreForTest()
			t.Cleanup(CloseDefaultStateStore)
			_, savedPath := savedSynthesisCaptureOutput(t, "deductive")
			output, _ := savedSynthesisCaptureOutput(t, "inductive")
			session := &EnsembleSession{
				SessionName: "capture-subset-failure",
				Question:    "Preserve all previous recoverable state.",
				Status:      EnsembleActive,
				Assignments: []ModeAssignment{
					{ModeID: "deductive", PaneName: "%91", Status: AssignmentDone, OutputPath: savedPath},
					{ModeID: "inductive", PaneName: "%2", Status: AssignmentActive},
				},
			}
			if err := SaveSession(session.SessionName, session); err != nil {
				t.Fatal(err)
			}
			persistedBefore := append([]ModeAssignment(nil), session.Assignments...)
			captured := []CapturedOutput{{ModeID: "inductive", PaneName: "%2", RawOutput: "completed transcript", Parsed: &output}}
			switch failure {
			case "state-save":
				session.Question = ""
			case "wrong-pane":
				captured[0].PaneName = "%99"
			case "duplicate-capture":
				captured = append(captured, captured[0])
			case "duplicate-assignment":
				session.Assignments = append(session.Assignments, session.Assignments[1])
			}
			before := append([]ModeAssignment(nil), session.Assignments...)
			capturesBefore := append([]CapturedOutput(nil), captured...)
			if err := saveCapturedAssignmentOutputs(session, captured); err == nil {
				t.Fatal("expected subset persistence failure")
			}
			if !reflect.DeepEqual(session.Assignments, before) || !reflect.DeepEqual(captured, capturesBefore) {
				t.Fatalf("failure published caller state: %+v, %+v", session.Assignments, captured)
			}
			reloaded, err := LoadSession(session.SessionName)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded.Assignments, persistedBefore) {
				t.Fatalf("failure changed durable assignments: %+v", reloaded.Assignments)
			}
		})
	}
}
