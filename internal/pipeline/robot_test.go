package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStdout captures stdout during function execution
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// stubPersistedStateRoot pins the persisted-state root used by the ntm#216
// disk fallback (GetPipelineSnapshot / GetAllPipelineSnapshots) for the
// duration of a test. Pass "" to disable the fallback so registry-only
// assertions aren't polluted by .ntm/pipelines files under the test cwd.
func stubPersistedStateRoot(t *testing.T, root string) {
	t.Helper()
	orig := persistedStateRootFn
	persistedStateRootFn = func() string { return root }
	t.Cleanup(func() { persistedStateRootFn = orig })
}

func TestNewRobotResponse(t *testing.T) {

	tests := []struct {
		name    string
		success bool
	}{
		{
			name:    "success response",
			success: true,
		},
		{
			name:    "failure response",
			success: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := NewRobotResponse(tt.success)

			if resp.Success != tt.success {
				t.Errorf("NewRobotResponse(%v).Success = %v, want %v", tt.success, resp.Success, tt.success)
			}

			if resp.Timestamp == "" {
				t.Error("NewRobotResponse().Timestamp is empty")
			}

			// Validate timestamp format
			_, err := time.Parse(time.RFC3339, resp.Timestamp)
			if err != nil {
				t.Errorf("NewRobotResponse().Timestamp = %q, invalid RFC3339: %v", resp.Timestamp, err)
			}
		})
	}
}

func TestNewErrorResponse(t *testing.T) {

	tests := []struct {
		name     string
		err      error
		code     string
		hint     string
		wantErr  string
		wantCode string
		wantHint string
	}{
		{
			name:     "internal error",
			err:      errors.New("something went wrong"),
			code:     ErrCodeInternalError,
			hint:     "try again",
			wantErr:  "something went wrong",
			wantCode: ErrCodeInternalError,
			wantHint: "try again",
		},
		{
			name:     "invalid flag error",
			err:      errors.New("unknown flag"),
			code:     ErrCodeInvalidFlag,
			hint:     "",
			wantErr:  "unknown flag",
			wantCode: ErrCodeInvalidFlag,
			wantHint: "",
		},
		{
			name:     "session not found",
			err:      errors.New("session does not exist"),
			code:     ErrCodeSessionNotFound,
			hint:     "create session first",
			wantErr:  "session does not exist",
			wantCode: ErrCodeSessionNotFound,
			wantHint: "create session first",
		},
		{
			name:     "nil error",
			err:      nil,
			code:     ErrCodeInternalError,
			hint:     "inspect logs",
			wantErr:  "unknown error",
			wantCode: ErrCodeInternalError,
			wantHint: "inspect logs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := NewErrorResponse(tt.err, tt.code, tt.hint)

			if resp.Success {
				t.Error("NewErrorResponse().Success = true, want false")
			}

			if resp.Error != tt.wantErr {
				t.Errorf("NewErrorResponse().Error = %q, want %q", resp.Error, tt.wantErr)
			}

			if resp.ErrorCode != tt.wantCode {
				t.Errorf("NewErrorResponse().ErrorCode = %q, want %q", resp.ErrorCode, tt.wantCode)
			}

			if resp.Hint != tt.wantHint {
				t.Errorf("NewErrorResponse().Hint = %q, want %q", resp.Hint, tt.wantHint)
			}

			if resp.Timestamp == "" {
				t.Error("NewErrorResponse().Timestamp is empty")
			}
		})
	}
}

func TestRobotCalculateProgress(t *testing.T) {

	tests := []struct {
		name  string
		state *ExecutionState
		want  PipelineProgress
	}{
		{
			name:  "nil state",
			state: nil,
			want:  PipelineProgress{},
		},
		{
			name: "empty steps",
			state: &ExecutionState{
				Steps: map[string]StepResult{},
			},
			want: PipelineProgress{
				Percent: 0,
			},
		},
		{
			name: "all pending",
			state: &ExecutionState{
				Steps: map[string]StepResult{
					"step1": {Status: StatusPending},
					"step2": {Status: StatusPending},
				},
			},
			want: PipelineProgress{
				Pending: 2,
				Total:   2,
				Percent: 0,
			},
		},
		{
			name: "mixed statuses",
			state: &ExecutionState{
				Steps: map[string]StepResult{
					"step1": {Status: StatusCompleted},
					"step2": {Status: StatusRunning},
					"step3": {Status: StatusPending},
					"step4": {Status: StatusFailed},
					"step5": {Status: StatusSkipped},
				},
			},
			want: PipelineProgress{
				Completed: 1,
				Running:   1,
				Pending:   1,
				Failed:    1,
				Skipped:   1,
				Total:     5,
				Percent:   60, // (1 completed + 1 failed + 1 skipped) / 5 * 100
			},
		},
		{
			name: "all completed",
			state: &ExecutionState{
				Steps: map[string]StepResult{
					"step1": {Status: StatusCompleted},
					"step2": {Status: StatusCompleted},
					"step3": {Status: StatusCompleted},
				},
			},
			want: PipelineProgress{
				Completed: 3,
				Total:     3,
				Percent:   100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateProgress(tt.state)

			if got.Completed != tt.want.Completed {
				t.Errorf("calculateProgress().Completed = %d, want %d", got.Completed, tt.want.Completed)
			}
			if got.Running != tt.want.Running {
				t.Errorf("calculateProgress().Running = %d, want %d", got.Running, tt.want.Running)
			}
			if got.Pending != tt.want.Pending {
				t.Errorf("calculateProgress().Pending = %d, want %d", got.Pending, tt.want.Pending)
			}
			if got.Failed != tt.want.Failed {
				t.Errorf("calculateProgress().Failed = %d, want %d", got.Failed, tt.want.Failed)
			}
			if got.Skipped != tt.want.Skipped {
				t.Errorf("calculateProgress().Skipped = %d, want %d", got.Skipped, tt.want.Skipped)
			}
			if got.Total != tt.want.Total {
				t.Errorf("calculateProgress().Total = %d, want %d", got.Total, tt.want.Total)
			}
			if got.Percent != tt.want.Percent {
				t.Errorf("calculateProgress().Percent = %f, want %f", got.Percent, tt.want.Percent)
			}
		})
	}
}

func TestConvertSteps(t *testing.T) {

	now := time.Now()
	later := now.Add(5 * time.Second)

	tests := []struct {
		name  string
		state *ExecutionState
		check func(t *testing.T, steps map[string]PipelineStep)
	}{
		{
			name: "empty steps",
			state: &ExecutionState{
				Steps: map[string]StepResult{},
			},
			check: func(t *testing.T, steps map[string]PipelineStep) {
				if len(steps) != 0 {
					t.Errorf("convertSteps() returned %d steps, want 0", len(steps))
				}
			},
		},
		{
			name: "step with all fields",
			state: &ExecutionState{
				Steps: map[string]StepResult{
					"step1": {
						StepID:     "step1",
						Status:     StatusCompleted,
						AgentType:  "claude",
						PaneUsed:   "main:1",
						StartedAt:  now,
						FinishedAt: later,
						Output:     "line1\nline2\nline3",
						Error:      nil,
					},
				},
			},
			check: func(t *testing.T, steps map[string]PipelineStep) {
				step, ok := steps["step1"]
				if !ok {
					t.Fatal("step1 not found in converted steps")
				}
				if step.Status != "completed" {
					t.Errorf("step.Status = %q, want %q", step.Status, "completed")
				}
				if step.Agent != "claude" {
					t.Errorf("step.Agent = %q, want %q", step.Agent, "claude")
				}
				if step.PaneUsed != "main:1" {
					t.Errorf("step.PaneUsed = %q, want %q", step.PaneUsed, "main:1")
				}
				if step.OutputLines != 3 {
					t.Errorf("step.OutputLines = %d, want %d", step.OutputLines, 3)
				}
				if step.DurationMs != 5000 {
					t.Errorf("step.DurationMs = %d, want %d", step.DurationMs, 5000)
				}
			},
		},
		{
			name: "step with error",
			state: &ExecutionState{
				Steps: map[string]StepResult{
					"step1": {
						StepID: "step1",
						Status: StatusFailed,
						Error: &StepError{
							Type:    "timeout",
							Message: "step timed out",
						},
					},
				},
			},
			check: func(t *testing.T, steps map[string]PipelineStep) {
				step, ok := steps["step1"]
				if !ok {
					t.Fatal("step1 not found in converted steps")
				}
				if step.Error != "step timed out" {
					t.Errorf("step.Error = %q, want %q", step.Error, "step timed out")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertSteps(tt.state)
			tt.check(t, got)
		})
	}
}

func TestRobotProgressSkipKindCounts(t *testing.T) {
	state := &ExecutionState{
		Steps: map[string]StepResult{
			"a": {Status: StatusSkipped, SkipKind: SkipKindWhenCondition},
			"b": {Status: StatusSkipped, SkipKind: SkipKindFailedDependency},
			"c": {Status: StatusSkipped, SkipKind: SkipKindFailedDependency},
			"d": {Status: StatusSkipped, SkipKind: SkipKindNone},
			"e": {Status: StatusCompleted},
		},
	}

	got := calculateProgress(state)

	if got.Skipped != 4 {
		t.Fatalf("Skipped = %d, want 4", got.Skipped)
	}
	if got.SkipKindCounts == nil {
		t.Fatal("SkipKindCounts is nil")
	}
	if got.SkipKindCounts[SkipKindWhenCondition] != 1 {
		t.Errorf("when_false count = %d, want 1", got.SkipKindCounts[SkipKindWhenCondition])
	}
	if got.SkipKindCounts[SkipKindFailedDependency] != 2 {
		t.Errorf("failed_dependency count = %d, want 2", got.SkipKindCounts[SkipKindFailedDependency])
	}
	if _, ok := got.SkipKindCounts[SkipKindNone]; ok {
		t.Errorf("SkipKindNone should not appear in distribution")
	}
}

func TestRobotConvertStepsCarriesSkipKind(t *testing.T) {
	state := &ExecutionState{
		Steps: map[string]StepResult{
			"step1": {
				StepID:     "step1",
				Status:     StatusSkipped,
				SkipKind:   SkipKindForeachFilter,
				SkipReason: "filter excluded role==reviewer",
			},
		},
	}

	steps := convertSteps(state)
	step, ok := steps["step1"]
	if !ok {
		t.Fatal("step1 missing")
	}
	if step.SkipKind != SkipKindForeachFilter {
		t.Errorf("SkipKind = %q, want %q", step.SkipKind, SkipKindForeachFilter)
	}
	if step.SkipReason != "filter excluded role==reviewer" {
		t.Errorf("SkipReason = %q, want %q", step.SkipReason, "filter excluded role==reviewer")
	}

	payload, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(payload)
	if !strings.Contains(body, `"skip_kind":"foreach_filter_excluded"`) {
		t.Errorf("json missing skip_kind field: %s", body)
	}
	if !strings.Contains(body, `"skip_reason":"filter excluded role==reviewer"`) {
		t.Errorf("json missing skip_reason field: %s", body)
	}
}

func TestRobotProgressSkipKindCountsJSON(t *testing.T) {
	progress := PipelineProgress{
		Skipped: 2,
		Total:   2,
		SkipKindCounts: map[SkipKind]int{
			SkipKindStartFrom: 2,
		},
	}
	payload, err := json.Marshal(progress)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(payload)
	if !strings.Contains(body, `"skip_kind_counts":{"start_from_excluded":2}`) {
		t.Errorf("json missing skip_kind_counts: %s", body)
	}

	emptyPayload, _ := json.Marshal(PipelineProgress{})
	if strings.Contains(string(emptyPayload), "skip_kind_counts") {
		t.Errorf("empty progress should omit skip_kind_counts: %s", emptyPayload)
	}
}

func TestCountLines(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "empty string",
			input: "",
			want:  0,
		},
		{
			name:  "single line no newline",
			input: "hello",
			want:  1,
		},
		{
			name:  "single line with newline",
			input: "hello\n",
			want:  2,
		},
		{
			name:  "two lines",
			input: "hello\nworld",
			want:  2,
		},
		{
			name:  "three lines",
			input: "line1\nline2\nline3",
			want:  3,
		},
		{
			name:  "multiple trailing newlines",
			input: "hello\n\n\n",
			want:  4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countLines(tt.input)
			if got != tt.want {
				t.Errorf("countLines(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParsePipelineVars(t *testing.T) {

	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantErr bool
		check   func(t *testing.T, vars map[string]interface{})
	}{
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
		{
			name:  "simple object",
			input: `{"key": "value"}`,
			check: func(t *testing.T, vars map[string]interface{}) {
				if vars["key"] != "value" {
					t.Errorf("vars[key] = %v, want %q", vars["key"], "value")
				}
			},
		},
		{
			name:  "numeric value",
			input: `{"count": 42}`,
			check: func(t *testing.T, vars map[string]interface{}) {
				// JSON numbers are float64
				if vars["count"] != float64(42) {
					t.Errorf("vars[count] = %v, want %v", vars["count"], float64(42))
				}
			},
		},
		{
			name:  "boolean value",
			input: `{"enabled": true}`,
			check: func(t *testing.T, vars map[string]interface{}) {
				if vars["enabled"] != true {
					t.Errorf("vars[enabled] = %v, want %v", vars["enabled"], true)
				}
			},
		},
		{
			name:  "nested object",
			input: `{"outer": {"inner": "value"}}`,
			check: func(t *testing.T, vars map[string]interface{}) {
				outer, ok := vars["outer"].(map[string]interface{})
				if !ok {
					t.Fatal("outer is not a map")
				}
				if outer["inner"] != "value" {
					t.Errorf("outer.inner = %v, want %q", outer["inner"], "value")
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   `{invalid}`,
			wantErr: true,
		},
		{
			name:    "not an object",
			input:   `"just a string"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePipelineVars(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("ParsePipelineVars() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("ParsePipelineVars() unexpected error: %v", err)
				return
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("ParsePipelineVars(%q) = %v, want nil", tt.input, got)
				}
				return
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestPipelineRegistry(t *testing.T) {
	// Clear registry before test
	ClearPipelineRegistry()

	// Test registration
	exec := &PipelineExecution{
		RunID:      "test-run-123",
		WorkflowID: "test-workflow",
		Status:     "running",
	}

	RegisterPipeline(exec)

	// Test retrieval
	got := GetPipelineExecution("test-run-123")
	if got == nil {
		t.Fatal("GetPipelineExecution() returned nil after registration")
	}
	if got.RunID != "test-run-123" {
		t.Errorf("GetPipelineExecution().RunID = %q, want %q", got.RunID, "test-run-123")
	}

	// Test not found
	notFound := GetPipelineExecution("nonexistent")
	if notFound != nil {
		t.Error("GetPipelineExecution(nonexistent) should return nil")
	}

	// Test GetAllPipelines
	all := GetAllPipelines()
	if len(all) != 1 {
		t.Errorf("GetAllPipelines() returned %d pipelines, want 1", len(all))
	}

	// Test clear
	ClearPipelineRegistry()
	all = GetAllPipelines()
	if len(all) != 0 {
		t.Errorf("GetAllPipelines() after clear returned %d pipelines, want 0", len(all))
	}
}

func TestUpdatePipelineFromState(t *testing.T) {
	// Clear registry before test
	ClearPipelineRegistry()

	// Register a pipeline
	exec := &PipelineExecution{
		RunID:      "test-run-456",
		WorkflowID: "test-workflow",
		Status:     "running",
		Steps:      make(map[string]PipelineStep),
	}
	RegisterPipeline(exec)

	// Create state update
	state := &ExecutionState{
		RunID:      "test-run-456",
		WorkflowID: "test-workflow",
		Status:     StatusCompleted,
		Steps: map[string]StepResult{
			"step1": {
				StepID: "step1",
				Status: StatusCompleted,
			},
		},
	}

	// Update pipeline
	UpdatePipelineFromState("test-run-456", state)

	// Verify update
	got := GetPipelineExecution("test-run-456")
	if got == nil {
		t.Fatal("GetPipelineExecution() returned nil after update")
	}
	if got.Status != "completed" {
		t.Errorf("GetPipelineExecution().Status = %q, want %q", got.Status, "completed")
	}

	// Clean up
	ClearPipelineRegistry()
}

func TestOutputJSON(t *testing.T) {

	tests := []struct {
		name  string
		input interface{}
		check func(t *testing.T, output string)
	}{
		{
			name:  "simple struct",
			input: struct{ Name string }{"test"},
			check: func(t *testing.T, output string) {
				var result map[string]string
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("Failed to parse JSON: %v", err)
				}
				if result["Name"] != "test" {
					t.Errorf("Name = %q, want %q", result["Name"], "test")
				}
			},
		},
		{
			name:  "robot response",
			input: NewRobotResponse(true),
			check: func(t *testing.T, output string) {
				var result RobotResponse
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("Failed to parse JSON: %v", err)
				}
				if !result.Success {
					t.Error("Expected success=true")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: Not parallel because we capture stdout
			output := captureStdout(t, func() {
				outputJSON(tt.input)
			})
			tt.check(t, output)
		})
	}
}

func TestPrintPipelineRun_ValidationErrors(t *testing.T) {
	// Test validation errors that don't require tmux

	tests := []struct {
		name       string
		opts       PipelineRunOptions
		wantCode   int
		wantErrMsg string
	}{
		{
			name:       "missing workflow file",
			opts:       PipelineRunOptions{},
			wantCode:   1,
			wantErrMsg: "workflow file is required",
		},
		{
			name:       "missing session",
			opts:       PipelineRunOptions{WorkflowFile: "test.yaml"},
			wantCode:   1,
			wantErrMsg: "session is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitCode int
			output := captureStdout(t, func() {
				exitCode = PrintPipelineRun(tt.opts)
			})

			if exitCode != tt.wantCode {
				t.Errorf("PrintPipelineRun() exit code = %d, want %d", exitCode, tt.wantCode)
			}

			var result PipelineRunOutput
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
			}

			if result.Success {
				t.Error("Expected success=false for validation error")
			}

			if result.Error != tt.wantErrMsg {
				t.Errorf("Error = %q, want %q", result.Error, tt.wantErrMsg)
			}

			if result.ErrorCode != ErrCodeInvalidFlag {
				t.Errorf("ErrorCode = %q, want %q", result.ErrorCode, ErrCodeInvalidFlag)
			}
		})
	}
}

func TestPrintPipelineRun_WorkflowLoadErrors(t *testing.T) {
	// Test workflow loading errors that don't require tmux

	tests := []struct {
		name       string
		setupFile  func(t *testing.T) string // returns path to workflow file
		wantCode   int
		wantErrMsg string
	}{
		{
			name: "nonexistent workflow file",
			setupFile: func(t *testing.T) string {
				return "/nonexistent/path/to/workflow.yaml"
			},
			wantCode:   1,
			wantErrMsg: "failed to load workflow",
		},
		{
			name: "invalid YAML syntax",
			setupFile: func(t *testing.T) string {
				f, err := os.CreateTemp(t.TempDir(), "invalid-*.yaml")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				f.WriteString("invalid: yaml: content: [")
				f.Close()
				return f.Name()
			},
			wantCode:   1,
			wantErrMsg: "failed to load workflow",
		},
		{
			name: "missing schema_version",
			setupFile: func(t *testing.T) string {
				f, err := os.CreateTemp(t.TempDir(), "noschema-*.yaml")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				// Valid YAML but missing required 'schema_version' field
				f.WriteString("name: test-workflow\nsteps:\n  - id: step1\n    agent: claude\n    prompt: hello\n")
				f.Close()
				return f.Name()
			},
			wantCode:   1,
			wantErrMsg: "schema_version is required",
		},
		{
			name: "missing name field",
			setupFile: func(t *testing.T) string {
				f, err := os.CreateTemp(t.TempDir(), "noname-*.yaml")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				// Valid YAML but missing required 'name' field (has schema_version)
				f.WriteString("schema_version: \"2.0\"\nsteps:\n  - id: step1\n    agent: claude\n    prompt: hello\n")
				f.Close()
				return f.Name()
			},
			wantCode:   1,
			wantErrMsg: "name is required",
		},
		{
			name: "empty steps array",
			setupFile: func(t *testing.T) string {
				f, err := os.CreateTemp(t.TempDir(), "nosteps-*.yaml")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				f.WriteString("schema_version: \"1.0\"\nname: test-workflow\nsteps: []\n")
				f.Close()
				return f.Name()
			},
			wantCode:   1,
			wantErrMsg: "at least one step is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowPath := tt.setupFile(t)

			opts := PipelineRunOptions{
				WorkflowFile: workflowPath,
				Session:      "test-session",
			}

			var exitCode int
			output := captureStdout(t, func() {
				exitCode = PrintPipelineRun(opts)
			})

			if exitCode != tt.wantCode {
				t.Errorf("PrintPipelineRun() exit code = %d, want %d", exitCode, tt.wantCode)
			}

			var result PipelineRunOutput
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
			}

			if result.Success {
				t.Error("Expected success=false for workflow load error")
			}

			if result.Error == "" {
				t.Error("Expected non-empty error message")
			}

			// Check that error message contains expected text
			if tt.wantErrMsg != "" && !strings.Contains(result.Error, tt.wantErrMsg) {
				t.Errorf("Error = %q, want to contain %q", result.Error, tt.wantErrMsg)
			}
		})
	}
}

func TestPrintPipelineStatus_ValidationErrors(t *testing.T) {
	ClearPipelineRegistry()
	stubPersistedStateRoot(t, "")

	tests := []struct {
		name       string
		runID      string
		wantCode   int
		wantErrMsg string
		errorCode  string
	}{
		{
			name:       "missing run_id",
			runID:      "",
			wantCode:   1,
			wantErrMsg: "run_id is required",
			errorCode:  ErrCodeInvalidFlag,
		},
		{
			name:       "nonexistent run_id",
			runID:      "nonexistent-run-123",
			wantCode:   1,
			wantErrMsg: "pipeline not found: nonexistent-run-123",
			errorCode:  ErrCodeSessionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitCode int
			output := captureStdout(t, func() {
				exitCode = PrintPipelineStatus(tt.runID)
			})

			if exitCode != tt.wantCode {
				t.Errorf("PrintPipelineStatus() exit code = %d, want %d", exitCode, tt.wantCode)
			}

			// Use generic map to handle embedded struct field shadowing
			// RobotResponse.Error is shadowed by PipelineStatusOutput.Error in Go struct
			// but JSON has only one "error" field
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
			}

			if success, _ := result["success"].(bool); success {
				t.Error("Expected success=false for validation error")
			}

			errMsg, _ := result["error"].(string)
			if errMsg != tt.wantErrMsg {
				t.Errorf("Error = %q, want %q", errMsg, tt.wantErrMsg)
			}

			errCode, _ := result["error_code"].(string)
			if errCode != tt.errorCode {
				t.Errorf("ErrorCode = %q, want %q", errCode, tt.errorCode)
			}
		})
	}
}

func TestPrintPipelineStatus_FoundPipeline(t *testing.T) {
	ClearPipelineRegistry()

	// Register a test pipeline
	exec := &PipelineExecution{
		RunID:      "test-status-run",
		WorkflowID: "test-workflow",
		Session:    "test-session",
		Status:     "running",
		StartedAt:  time.Now(),
		Steps:      make(map[string]PipelineStep),
		Progress: PipelineProgress{
			Total:   3,
			Pending: 2,
			Running: 1,
		},
	}
	RegisterPipeline(exec)

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineStatus("test-status-run")
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineStatus() exit code = %d, want 0", exitCode)
	}

	var result PipelineStatusOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}

	if result.RunID != "test-status-run" {
		t.Errorf("RunID = %q, want %q", result.RunID, "test-status-run")
	}

	if result.WorkflowID != "test-workflow" {
		t.Errorf("WorkflowID = %q, want %q", result.WorkflowID, "test-workflow")
	}

	if result.Status != "running" {
		t.Errorf("Status = %q, want %q", result.Status, "running")
	}

	ClearPipelineRegistry()
}

func TestPrintPipelineList_Empty(t *testing.T) {
	ClearPipelineRegistry()
	stubPersistedStateRoot(t, "")

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineList()
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineList() exit code = %d, want 0", exitCode)
	}

	var result PipelineListOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}

	if len(result.Pipelines) != 0 {
		t.Errorf("Pipelines count = %d, want 0", len(result.Pipelines))
	}
}

func TestPrintPipelineList_WithPipelines(t *testing.T) {
	ClearPipelineRegistry()
	stubPersistedStateRoot(t, "")

	// Register some test pipelines
	now := time.Now()
	exec1 := &PipelineExecution{
		RunID:      "list-test-1",
		WorkflowID: "workflow-1",
		Session:    "session-1",
		Status:     "completed",
		StartedAt:  now.Add(-time.Minute),
		Progress:   PipelineProgress{Total: 5, Completed: 5, Percent: 100},
	}
	liveExecutor := NewExecutor(DefaultExecutorConfig("session-2"))
	liveExecutor.state = &ExecutionState{
		RunID:       "list-test-2",
		WorkflowID:  "workflow-2",
		Session:     "session-2",
		Status:      StatusRunning,
		StartedAt:   now,
		CurrentStep: "step-2",
		Steps: map[string]StepResult{
			"step-1": {StepID: "step-1", Status: StatusCompleted},
			"step-2": {StepID: "step-2", Status: StatusRunning},
		},
	}
	exec2 := &PipelineExecution{
		RunID:      "list-test-2",
		WorkflowID: "workflow-2",
		Session:    "session-2",
		Status:     "running",
		StartedAt:  now,
		Progress:   PipelineProgress{Total: 10, Running: 1, Pending: 9, Percent: 0},
		executor:   liveExecutor,
	}
	RegisterPipeline(exec1)
	RegisterPipeline(exec2)

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineList()
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineList() exit code = %d, want 0", exitCode)
	}

	var result PipelineListOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}

	if len(result.Pipelines) != 2 {
		t.Errorf("Pipelines count = %d, want 2", len(result.Pipelines))
	}

	// Verify pipelines are sorted by start time (most recent first)
	if result.Pipelines[0].RunID != "list-test-2" {
		t.Errorf("Pipelines[0].RunID = %q, want %q", result.Pipelines[0].RunID, "list-test-2")
	}
	if result.Pipelines[0].Progress.Total != 10 {
		t.Errorf("Pipelines[0].Progress.Total = %d, want 10", result.Pipelines[0].Progress.Total)
	}
	if result.Pipelines[0].Progress.Completed != 1 {
		t.Errorf("Pipelines[0].Progress.Completed = %d, want 1", result.Pipelines[0].Progress.Completed)
	}
	if result.Pipelines[0].Progress.Running != 1 {
		t.Errorf("Pipelines[0].Progress.Running = %d, want 1", result.Pipelines[0].Progress.Running)
	}
	if result.Pipelines[0].Progress.Percent != 10 {
		t.Errorf("Pipelines[0].Progress.Percent = %f, want 10", result.Pipelines[0].Progress.Percent)
	}
	if result.AgentHints == nil {
		t.Error("AgentHints should not be nil")
	}

	ClearPipelineRegistry()
}

func TestGetPipelineSnapshot_UsesLiveExecutorState(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	executor := NewExecutor(DefaultExecutorConfig("snapshot-session"))
	executor.state = &ExecutionState{
		RunID:       "snapshot-live-test",
		WorkflowID:  "snapshot-workflow",
		Session:     "snapshot-session",
		Status:      StatusRunning,
		CurrentStep: "step-2",
		Steps: map[string]StepResult{
			"step-1": {StepID: "step-1", Status: StatusCompleted},
			"step-2": {StepID: "step-2", Status: StatusRunning},
		},
	}

	RegisterPipeline(&PipelineExecution{
		RunID:      "snapshot-live-test",
		WorkflowID: "snapshot-workflow",
		Session:    "snapshot-session",
		Status:     "running",
		StartedAt:  time.Now(),
		Progress: PipelineProgress{
			Total:   5,
			Pending: 5,
		},
		Steps: map[string]PipelineStep{
			"stale": {ID: "stale", Status: "pending"},
		},
		executor: executor,
	})

	snapshot := GetPipelineSnapshot("snapshot-live-test")
	if snapshot == nil {
		t.Fatal("GetPipelineSnapshot() returned nil")
	}
	if snapshot.CurrentStep != "step-2" {
		t.Errorf("CurrentStep = %q, want %q", snapshot.CurrentStep, "step-2")
	}
	if snapshot.Progress.Total != 5 {
		t.Errorf("Progress.Total = %d, want 5", snapshot.Progress.Total)
	}
	if snapshot.Progress.Completed != 1 {
		t.Errorf("Progress.Completed = %d, want 1", snapshot.Progress.Completed)
	}
	if snapshot.Progress.Running != 1 {
		t.Errorf("Progress.Running = %d, want 1", snapshot.Progress.Running)
	}
	if snapshot.Progress.Percent != 20 {
		t.Errorf("Progress.Percent = %f, want 20", snapshot.Progress.Percent)
	}
	if _, ok := snapshot.Steps["step-2"]; !ok {
		t.Errorf("snapshot steps missing live executor step data: %v", snapshot.Steps)
	}
	if _, ok := snapshot.Steps["stale"]; ok {
		t.Errorf("snapshot should not use stale registry steps when executor state is available: %v", snapshot.Steps)
	}
}

func TestGetPipelineSnapshot_PreservesTerminalRegistryStatus(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	executor := NewExecutor(DefaultExecutorConfig("cancelled-session"))
	executor.state = &ExecutionState{
		RunID:       "snapshot-cancelled-test",
		WorkflowID:  "cancelled-workflow",
		Session:     "cancelled-session",
		Status:      StatusRunning,
		CurrentStep: "step-2",
		Steps: map[string]StepResult{
			"step-1": {StepID: "step-1", Status: StatusCompleted},
			"step-2": {StepID: "step-2", Status: StatusRunning},
		},
	}

	finishedAt := time.Now()
	RegisterPipeline(&PipelineExecution{
		RunID:      "snapshot-cancelled-test",
		WorkflowID: "cancelled-workflow",
		Session:    "cancelled-session",
		Status:     "cancelled",
		StartedAt:  finishedAt.Add(-time.Minute),
		FinishedAt: &finishedAt,
		Progress: PipelineProgress{
			Total:   4,
			Pending: 4,
		},
		executor: executor,
	})

	snapshot := GetPipelineSnapshot("snapshot-cancelled-test")
	if snapshot == nil {
		t.Fatal("GetPipelineSnapshot() returned nil")
	}
	if snapshot.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", snapshot.Status, "cancelled")
	}
	if snapshot.FinishedAt == nil || !snapshot.FinishedAt.Equal(finishedAt) {
		t.Errorf("FinishedAt = %v, want %v", snapshot.FinishedAt, finishedAt)
	}
	if snapshot.Progress.Total != 4 {
		t.Errorf("Progress.Total = %d, want 4", snapshot.Progress.Total)
	}
	if snapshot.Progress.Completed != 1 {
		t.Errorf("Progress.Completed = %d, want 1", snapshot.Progress.Completed)
	}
	if snapshot.Progress.Running != 1 {
		t.Errorf("Progress.Running = %d, want 1", snapshot.Progress.Running)
	}
}

func TestStartBackgroundPipeline_RegistersExecutorAndCancel(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "background-start-test",
		Steps:         nil,
	}
	execCfg := DefaultExecutorConfig("background-session")
	execCfg.DryRun = true
	execCfg.GlobalTimeout = time.Second

	exec := StartBackgroundPipeline(workflow, nil, execCfg)
	if exec == nil {
		t.Fatal("StartBackgroundPipeline() returned nil")
	}

	registered := GetPipelineExecution(exec.RunID)
	if registered == nil {
		t.Fatal("background pipeline was not registered")
	}
	if registered.executor == nil {
		t.Fatal("registered pipeline is missing executor")
	}
	if registered.cancelFn == nil {
		t.Fatal("registered pipeline is missing cancelFn")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := GetPipelineSnapshot(exec.RunID)
		if snapshot != nil && (snapshot.Status == "running" || snapshot.Status == "completed") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("background pipeline %q never became visible through snapshots", exec.RunID)
}

func TestPrintPipelineCancel_ValidationErrors(t *testing.T) {
	ClearPipelineRegistry()

	tests := []struct {
		name       string
		runID      string
		wantCode   int
		wantErrMsg string
		errorCode  string
	}{
		{
			name:       "missing run_id",
			runID:      "",
			wantCode:   1,
			wantErrMsg: "run_id is required",
			errorCode:  ErrCodeInvalidFlag,
		},
		{
			name:       "nonexistent run_id",
			runID:      "cancel-nonexistent-123",
			wantCode:   1,
			wantErrMsg: "pipeline not found: cancel-nonexistent-123",
			errorCode:  ErrCodeSessionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitCode int
			output := captureStdout(t, func() {
				exitCode = PrintPipelineCancel(tt.runID)
			})

			if exitCode != tt.wantCode {
				t.Errorf("PrintPipelineCancel() exit code = %d, want %d", exitCode, tt.wantCode)
			}

			var result PipelineCancelOutput
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
			}

			if result.Success {
				t.Error("Expected success=false for validation error")
			}

			if result.Error != tt.wantErrMsg {
				t.Errorf("Error = %q, want %q", result.Error, tt.wantErrMsg)
			}

			if result.ErrorCode != tt.errorCode {
				t.Errorf("ErrorCode = %q, want %q", result.ErrorCode, tt.errorCode)
			}
		})
	}
}

func TestPrintPipelineCancel_CompletedPipeline(t *testing.T) {
	ClearPipelineRegistry()

	// Register a completed pipeline
	finished := time.Now()
	exec := &PipelineExecution{
		RunID:      "cancel-completed-test",
		WorkflowID: "test-workflow",
		Session:    "test-session",
		Status:     "completed",
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: &finished,
	}
	RegisterPipeline(exec)

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineCancel("cancel-completed-test")
	})

	// Cancelling a completed pipeline should succeed but do nothing
	if exitCode != 0 {
		t.Errorf("PrintPipelineCancel() exit code = %d, want 0", exitCode)
	}

	var result PipelineCancelOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}

	if result.Status != "completed" {
		t.Errorf("Status = %q, want %q", result.Status, "completed")
	}

	ClearPipelineRegistry()
}

func TestCancelPipeline_Running(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	cancelled := false
	exec := &PipelineExecution{
		RunID:      "cancel-test-1",
		WorkflowID: "test-wf",
		Status:     "running",
		StartedAt:  time.Now(),
		cancelFn:   func() { cancelled = true },
	}
	RegisterPipeline(exec)

	CancelPipeline("cancel-test-1")

	got := GetPipelineExecution("cancel-test-1")
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", got.Status, "cancelled")
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should be set after cancel")
	}
	if !cancelled {
		t.Error("cancelFn was not called")
	}
}

func TestCancelPipeline_NotFound(t *testing.T) {
	ClearPipelineRegistry()
	// Should not panic
	CancelPipeline("nonexistent-run")
}

func TestCancelPipeline_NilFuncs(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	exec := &PipelineExecution{
		RunID:    "cancel-nil-test",
		Status:   "running",
		cancelFn: nil,
		executor: nil,
	}
	RegisterPipeline(exec)

	// Should not panic even with nil cancelFn and executor
	CancelPipeline("cancel-nil-test")

	got := GetPipelineExecution("cancel-nil-test")
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", got.Status, "cancelled")
	}
}

func TestPrintPipelineCancel_RunningPipeline(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	cancelled := false
	exec := &PipelineExecution{
		RunID:      "cancel-running-test",
		WorkflowID: "test-workflow",
		Session:    "test-session",
		Status:     "running",
		StartedAt:  time.Now(),
		cancelFn:   func() { cancelled = true },
	}
	RegisterPipeline(exec)

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineCancel("cancel-running-test")
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineCancel() exit code = %d, want 0", exitCode)
	}

	var result PipelineCancelOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}
	if result.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", result.Status, "cancelled")
	}
	if result.RunID != "cancel-running-test" {
		t.Errorf("RunID = %q, want %q", result.RunID, "cancel-running-test")
	}
	if !cancelled {
		t.Error("cancelFn was not called")
	}
}

func TestPrintPipelineStatus_FinishedPipeline(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	now := time.Now()
	finished := now.Add(5 * time.Minute)
	exec := &PipelineExecution{
		RunID:      "status-finished-test",
		WorkflowID: "done-workflow",
		Session:    "done-session",
		Status:     "completed",
		StartedAt:  now,
		FinishedAt: &finished,
		Steps:      make(map[string]PipelineStep),
		Progress: PipelineProgress{
			Total:     3,
			Completed: 3,
			Percent:   100,
		},
	}
	RegisterPipeline(exec)

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineStatus("status-finished-test")
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineStatus() exit code = %d, want 0", exitCode)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if success, _ := result["success"].(bool); !success {
		t.Error("Expected success=true")
	}
	if result["finished_at"] == nil || result["finished_at"] == "" {
		t.Error("finished_at should be set for finished pipeline")
	}
	durationMs, _ := result["duration_ms"].(float64)
	if durationMs <= 0 {
		t.Errorf("duration_ms = %v, want > 0", durationMs)
	}
}

func TestUpdatePipelineFromState_NilState(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	exec := &PipelineExecution{
		RunID:  "nil-state-test",
		Status: "running",
	}
	RegisterPipeline(exec)

	// Should not panic
	UpdatePipelineFromState("nil-state-test", nil)

	got := GetPipelineExecution("nil-state-test")
	if got.Status != "running" {
		t.Errorf("Status should remain unchanged, got %q", got.Status)
	}
}

func TestUpdatePipelineFromState_NonExistent(t *testing.T) {
	ClearPipelineRegistry()
	state := &ExecutionState{
		RunID:  "nonexistent",
		Status: StatusCompleted,
	}
	// Should not panic
	UpdatePipelineFromState("nonexistent", state)
}

func TestUpdatePipelineFromState_TotalPreservation(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	exec := &PipelineExecution{
		RunID:      "total-preserve-test",
		WorkflowID: "big-workflow",
		Status:     "running",
		Steps:      make(map[string]PipelineStep),
		Progress: PipelineProgress{
			Total:   10, // Originally 10 steps from workflow
			Pending: 10,
		},
	}
	RegisterPipeline(exec)

	// State only has 2 steps completed so far
	state := &ExecutionState{
		RunID:  "total-preserve-test",
		Status: StatusRunning,
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusCompleted},
			"step2": {StepID: "step2", Status: StatusCompleted},
		},
	}
	UpdatePipelineFromState("total-preserve-test", state)

	got := GetPipelineExecution("total-preserve-test")
	if got.Progress.Total != 10 {
		t.Errorf("Total = %d, want 10 (should preserve original total)", got.Progress.Total)
	}
	if got.Progress.Completed != 2 {
		t.Errorf("Completed = %d, want 2", got.Progress.Completed)
	}
	if got.Progress.Percent != 20 {
		t.Errorf("Percent = %f, want 20", got.Progress.Percent)
	}
}

func TestUpdatePipelineFromState_WithErrors(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	exec := &PipelineExecution{
		RunID:  "errors-test",
		Status: "running",
		Steps:  make(map[string]PipelineStep),
	}
	RegisterPipeline(exec)

	finishedAt := time.Now()
	state := &ExecutionState{
		RunID:      "errors-test",
		Status:     StatusFailed,
		FinishedAt: finishedAt,
		Steps: map[string]StepResult{
			"step1": {StepID: "step1", Status: StatusFailed, Error: &StepError{Message: "boom"}},
		},
		Errors: []ExecutionError{
			{Message: "first error"},
			{Message: "last error"},
		},
	}
	UpdatePipelineFromState("errors-test", state)

	got := GetPipelineExecution("errors-test")
	if got.Error != "last error" {
		t.Errorf("Error = %q, want %q (should use last error)", got.Error, "last error")
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should be set")
	}
}

func TestPrintPipelineRun_DryRun(t *testing.T) {
	tmpDir := t.TempDir()

	workflowContent := `schema_version: "1.0"
name: dry-run-test
outputs:
  - name: report
    path: artifacts/report.md
settings:
  on_cancel:
    - id: cleanup
      command: "echo cleanup"
steps:
  - id: step1
    prompt: "Hello world"
    agent: claude
  - id: step2
    command: "go test ./internal/pipeline"
    depends_on: [step1]
  - id: notify
    mail_send:
      project_key: /data/projects/ntm
      agent_name: YellowBluff
      to: TealCrane
      subject: "[bd-fxj4f.6] preview"
      body: "dry-run"
      thread_id: bd-fxj4f.6
    depends_on: [step2]
  - id: reserve
    file_reservation_paths:
      project_key: /data/projects/ntm
      agent_name: YellowBluff
      paths: ["internal/pipeline/*.go"]
      exclusive: true
    depends_on: [notify]
post_pipeline_steps:
  - id: release
    file_reservation_release:
      project_key: /data/projects/ntm
      agent_name: YellowBluff
      paths: ["internal/pipeline/*.go"]
`
	workflowPath := tmpDir + "/test.yaml"
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	opts := PipelineRunOptions{
		WorkflowFile: workflowPath,
		Session:      "test-session",
		DryRun:       true,
	}

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineRun(opts)
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineRun() exit code = %d, want 0\nOutput: %s", exitCode, output)
	}

	var result PipelineRunOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if !result.Success {
		t.Errorf("Expected success=true, got error: %s", result.Error)
	}
	if !result.DryRun {
		t.Error("DryRun should be true in response")
	}
	if result.WorkflowID != "dry-run-test" {
		t.Errorf("WorkflowID = %q, want %q", result.WorkflowID, "dry-run-test")
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q, want %q", result.Status, "completed")
	}
	if result.SideEffects == nil {
		t.Fatal("SideEffects should be present in dry-run response")
	}
	if got := result.SideEffects.Summary.ByKind[sideEffectKindTmuxSend]; got != 1 {
		t.Errorf("tmux side-effect count = %d, want 1", got)
	}
	if got := result.SideEffects.Summary.ByKind[sideEffectKindShellCommand]; got != 2 {
		t.Errorf("shell side-effect count = %d, want 2", got)
	}
	if got := result.SideEffects.Summary.ByKind[sideEffectKindAgentMailSend]; got != 1 {
		t.Errorf("mail_send side-effect count = %d, want 1", got)
	}
	if got := result.SideEffects.Summary.ByKind[sideEffectKindAgentMailReservation]; got != 1 {
		t.Errorf("reservation side-effect count = %d, want 1", got)
	}
	if got := result.SideEffects.Summary.ByKind[sideEffectKindFilesystemWrite]; got != 1 {
		t.Errorf("filesystem side-effect count = %d, want 1", got)
	}
	if len(result.SideEffects.RollbackPreview) != 2 {
		t.Fatalf("RollbackPreview length = %d, want 2; preview=%#v", len(result.SideEffects.RollbackPreview), result.SideEffects.RollbackPreview)
	}
}

func TestPrintPipelineRun_DryRunHonorsStartFrom(t *testing.T) {
	tmpDir := t.TempDir()

	workflowContent := `schema_version: "2.0"
name: start-from-json-test
steps:
  - id: step1
    prompt: "first"
  - id: step2
    prompt: "second"
    depends_on: [step1]
  - id: step3
    prompt: "third"
    depends_on: [step2]
`
	workflowPath := tmpDir + "/start-from.yaml"
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	opts := PipelineRunOptions{
		WorkflowFile:  workflowPath,
		Session:       "test-session",
		ProjectDir:    tmpDir,
		DryRun:        true,
		StartFromStep: "step2",
	}

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineRun(opts)
	})
	if exitCode != 0 {
		t.Fatalf("PrintPipelineRun() exit code = %d, want 0\nOutput: %s", exitCode, output)
	}

	var result PipelineRunOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	state, err := LoadState(tmpDir, result.RunID)
	if err != nil {
		t.Fatalf("LoadState(%q) failed: %v", result.RunID, err)
	}
	if got := state.Steps["step1"]; got.Status != StatusSkipped || got.SkipReason != StartFromSkipReason {
		t.Fatalf("step1 result = %#v, want start-from skipped", got)
	}
	if got := state.Steps["step2"]; got.Status != StatusCompleted {
		t.Fatalf("step2 status = %v, want completed", got.Status)
	}
}

func TestPrintPipelineList_StatusCounts(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()
	stubPersistedStateRoot(t, "")

	now := time.Now()
	fin := now.Add(time.Minute)

	// Register pipelines with various statuses
	for _, s := range []struct {
		id, status string
		finished   bool
	}{
		{"list-r1", "running", false},
		{"list-r2", "completed", true},
		{"list-r3", "failed", true},
		{"list-r4", "cancelled", true},
	} {
		exec := &PipelineExecution{
			RunID:     s.id,
			Status:    s.status,
			StartedAt: now,
		}
		if s.finished {
			exec.FinishedAt = &fin
		}
		RegisterPipeline(exec)
	}

	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineList()
	})

	if exitCode != 0 {
		t.Errorf("PrintPipelineList() exit code = %d, want 0", exitCode)
	}

	var result PipelineListOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}

	if len(result.Pipelines) != 4 {
		t.Errorf("Pipelines count = %d, want 4", len(result.Pipelines))
	}

	if result.AgentHints == nil {
		t.Fatal("AgentHints should not be nil")
	}
	if !strings.Contains(result.AgentHints.Summary, "1 running") {
		t.Errorf("Summary = %q, should contain '1 running'", result.AgentHints.Summary)
	}
}

// TestGetPipelineSnapshot_PersistedFallback is the ntm#216 regression guard:
// a run that finished in another ntm process is gone from this process's
// in-memory registry, but its state was persisted to
// .ntm/pipelines/<run-id>.json — status and list must read it from disk
// instead of reporting "pipeline not found".
func TestGetPipelineSnapshot_PersistedFallback(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	dir := t.TempDir()
	stubPersistedStateRoot(t, dir)

	const runID = "run-20260709-120600-2519"
	started := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	finished := started.Add(5 * time.Minute)
	state := &ExecutionState{
		RunID:       runID,
		WorkflowID:  "wf-persisted",
		Session:     "sess-persisted",
		Status:      StatusCompleted,
		StartedAt:   started,
		FinishedAt:  finished,
		CurrentStep: "one",
		Steps: map[string]StepResult{
			"one": {StepID: "one", Status: StatusCompleted},
		},
	}
	if err := SaveState(dir, state); err != nil {
		t.Fatalf("SaveState() failed: %v", err)
	}

	// Registry is empty — the snapshot must come from disk.
	snapshot := GetPipelineSnapshot(runID)
	if snapshot == nil {
		t.Fatal("GetPipelineSnapshot() = nil, want persisted fallback snapshot")
	}
	if snapshot.WorkflowID != "wf-persisted" {
		t.Errorf("WorkflowID = %q, want %q", snapshot.WorkflowID, "wf-persisted")
	}
	if snapshot.Session != "sess-persisted" {
		t.Errorf("Session = %q, want %q", snapshot.Session, "sess-persisted")
	}
	if snapshot.Status != string(StatusCompleted) {
		t.Errorf("Status = %q, want %q", snapshot.Status, StatusCompleted)
	}
	if snapshot.FinishedAt == nil || !snapshot.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", snapshot.FinishedAt, finished)
	}
	if snapshot.Progress.Completed != 1 || snapshot.Progress.Total != 1 {
		t.Errorf("Progress = %+v, want 1/1 completed", snapshot.Progress)
	}

	// The robot status surface must succeed with exit code 0.
	var exitCode int
	output := captureStdout(t, func() {
		exitCode = PrintPipelineStatus(runID)
	})
	if exitCode != 0 {
		t.Errorf("PrintPipelineStatus() exit code = %d, want 0\nOutput: %s", exitCode, output)
	}
	var statusResult PipelineStatusOutput
	if err := json.Unmarshal([]byte(output), &statusResult); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}
	if !statusResult.Success {
		t.Errorf("PrintPipelineStatus() success = false, error: %s", statusResult.Error)
	}
	if statusResult.RunID != runID {
		t.Errorf("RunID = %q, want %q", statusResult.RunID, runID)
	}

	// The list surface must include the persisted run.
	output = captureStdout(t, func() {
		exitCode = PrintPipelineList()
	})
	if exitCode != 0 {
		t.Errorf("PrintPipelineList() exit code = %d, want 0", exitCode)
	}
	var listResult PipelineListOutput
	if err := json.Unmarshal([]byte(output), &listResult); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, output)
	}
	found := false
	for _, p := range listResult.Pipelines {
		if p.RunID == runID {
			found = true
			if p.Status != string(StatusCompleted) {
				t.Errorf("listed Status = %q, want %q", p.Status, StatusCompleted)
			}
		}
	}
	if !found {
		t.Errorf("PrintPipelineList() did not include persisted run %s", runID)
	}

	// A live registry entry with the same run ID must shadow the disk copy.
	RegisterPipeline(&PipelineExecution{
		RunID:      runID,
		WorkflowID: "wf-live",
		Status:     "running",
		StartedAt:  time.Now(),
	})
	snapshot = GetPipelineSnapshot(runID)
	if snapshot == nil || snapshot.WorkflowID != "wf-live" {
		t.Errorf("registry entry should shadow persisted state, got %+v", snapshot)
	}
}

// TestGetPipelineSnapshot_NoPersistedState verifies the fallback returns nil
// (and status surfaces keep their not-found error) when neither the registry
// nor the state directory knows the run.
func TestGetPipelineSnapshot_NoPersistedState(t *testing.T) {
	ClearPipelineRegistry()
	defer ClearPipelineRegistry()

	stubPersistedStateRoot(t, t.TempDir())

	if snapshot := GetPipelineSnapshot("run-never-existed"); snapshot != nil {
		t.Errorf("GetPipelineSnapshot() = %+v, want nil", snapshot)
	}

	// Hostile run IDs must not reach the filesystem.
	if snapshot := GetPipelineSnapshot("../../etc/passwd"); snapshot != nil {
		t.Errorf("GetPipelineSnapshot(traversal) = %+v, want nil", snapshot)
	}
}

// bd-fresh-eyes-audit .26: `ntm serve` drove pipeline.NewExecutor inline and
// never registered the run, so its own returned run_id was unlistable,
// ungettable, and uncancellable, and the detached run had no cancel handle
// stored anywhere. NewTrackedExecution gives an externally-driven run the
// registry entry and cancel handle that list/get/cancel need.
func TestNewTrackedExecution_IsVisibleAndCancellable(t *testing.T) {
	ClearPipelineRegistry()
	t.Cleanup(ClearPipelineRegistry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelled := false
	exec := NewTrackedExecution("run-tracked-1", "wf", "sess", 3, nil, func() {
		cancelled = true
		cancel()
	})
	RegisterPipeline(exec)

	if got := GetPipelineSnapshot("run-tracked-1"); got == nil {
		t.Fatal("GetPipelineSnapshot returned nil for a registered run")
	} else if got.Status != "running" || got.Progress.Total != 3 || got.Progress.Pending != 3 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}

	found := false
	for _, snap := range GetAllPipelineSnapshots() {
		if snap.RunID == "run-tracked-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("registered run is missing from GetAllPipelineSnapshots")
	}

	CancelPipeline("run-tracked-1")
	if !cancelled {
		t.Fatal("CancelPipeline did not invoke the stored cancel handle")
	}
	if ctx.Err() == nil {
		t.Fatal("run context was not cancelled")
	}
	after := GetPipelineSnapshot("run-tracked-1")
	if after == nil || after.Status != "cancelled" {
		t.Fatalf("status after cancel = %+v, want cancelled", after)
	}
}

// bd-kpccr: each registry entry pins an *Executor holding every StepResult,
// including captured pane output, and nothing ever removed one — so a
// long-lived `ntm serve` grew monotonically until restart. Evicting is safe
// because the run is already persisted and both snapshot surfaces fall back to
// the state file.
func TestRegisterPipeline_RetiresLongFinishedRuns(t *testing.T) {
	ClearPipelineRegistry()
	t.Cleanup(ClearPipelineRegistry)

	longAgo := time.Now().Add(-2 * finishedPipelineRetention)
	justNow := time.Now()

	RegisterPipeline(&PipelineExecution{RunID: "old-finished", Status: "completed", FinishedAt: &longAgo})
	RegisterPipeline(&PipelineExecution{RunID: "recent-finished", Status: "completed", FinishedAt: &justNow})
	RegisterPipeline(&PipelineExecution{RunID: "still-running", Status: "running"})

	// Registration sweeps, so a further register triggers the reap.
	RegisterPipeline(&PipelineExecution{RunID: "newest", Status: "running"})

	if got := GetPipelineExecution("old-finished"); got != nil {
		t.Fatal("a run finished well past the retention window kept its live registry entry")
	}
	if got := GetPipelineExecution("recent-finished"); got == nil {
		t.Fatal("a just-finished run lost its live entry; the status poll that follows a run needs it")
	}
	if got := GetPipelineExecution("still-running"); got == nil {
		t.Fatal("a RUNNING pipeline was evicted; its cancel handle would be unreachable")
	}
}

// A nil entry must never survive a sweep: getPipeline would hand callers a nil
// they are not expecting to have to guard.
func TestReapFinishedPipelines_DropsNilEntries(t *testing.T) {
	ClearPipelineRegistry()
	t.Cleanup(ClearPipelineRegistry)

	pipelineMu.Lock()
	pipelineRegistry["nil-entry"] = nil
	reapFinishedPipelinesLocked(time.Now())
	_, present := pipelineRegistry["nil-entry"]
	pipelineMu.Unlock()

	if present {
		t.Fatal("nil registry entry survived the sweep")
	}
}
