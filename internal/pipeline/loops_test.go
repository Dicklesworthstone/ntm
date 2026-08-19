package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestLoopResultStruct(t *testing.T) {
	result := LoopResult{
		Status:      StatusCompleted,
		Iterations:  5,
		Results:     []StepResult{{StepID: "s1", Status: StatusCompleted}},
		Collected:   []interface{}{"a", "b", "c"},
		BreakReason: "",
		FinishedAt:  time.Now(),
	}

	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %v", result.Status)
	}
	if result.Iterations != 5 {
		t.Errorf("expected 5 iterations, got %d", result.Iterations)
	}
	if len(result.Collected) != 3 {
		t.Errorf("expected 3 collected, got %d", len(result.Collected))
	}
}

func TestToInterfaceSlice(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		wantLen int
		wantErr bool
	}{
		{
			name:    "[]interface{}",
			input:   []interface{}{"a", "b", "c"},
			wantLen: 3,
			wantErr: false,
		},
		{
			name:    "[]string",
			input:   []string{"x", "y"},
			wantLen: 2,
			wantErr: false,
		},
		{
			name:    "[]int",
			input:   []int{1, 2, 3, 4},
			wantLen: 4,
			wantErr: false,
		},
		{
			name:    "[]float64",
			input:   []float64{1.1, 2.2},
			wantLen: 2,
			wantErr: false,
		},
		{
			name:    "comma-separated string",
			input:   "a, b, c",
			wantLen: 3,
			wantErr: false,
		},
		{
			name:    "unsupported type",
			input:   42,
			wantLen: 0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toInterfaceSlice(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("toInterfaceSlice() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(result) != tt.wantLen {
				t.Errorf("toInterfaceSlice() len = %d, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestParseItemsString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
	}{
		{"empty", "", 0},
		{"single", "item", 1},
		{"comma separated", "a, b, c", 3},
		{"json array", `["x", "y", "z"]`, 3},
		{"json mixed", `[1, "two", 3]`, 3},
		{"with spaces", "  a  ,  b  ,  c  ", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseItemsString(tt.input)
			if err != nil {
				t.Errorf("parseItemsString() error = %v", err)
				return
			}
			if len(result) != tt.wantLen {
				t.Errorf("parseItemsString() len = %d, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestLoopExecutorNoLoopConfig(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	loopExec := NewLoopExecutor(executor)

	step := &Step{ID: "test-step", Loop: nil}
	result := loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusFailed {
		t.Errorf("expected StatusFailed for nil loop config, got %v", result.Status)
	}
	if result.Error == nil {
		t.Error("expected error for nil loop config")
	}
}

func TestLoopExecutorEmptyLoopConfig(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
	}
	loopExec := NewLoopExecutor(executor)

	step := &Step{
		ID:   "test-step",
		Loop: &LoopConfig{}, // No items, while, or times defaults to times: 0
	}
	result := loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	// Empty loop config defaults to times: 0, which completes immediately with zero iterations
	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted for empty loop config (defaults to times: 0), got %v", result.Status)
	}
	if result.Iterations != 0 {
		t.Errorf("expected 0 iterations for empty loop config, got %d", result.Iterations)
	}
}

func TestLoopContextStruct(t *testing.T) {
	ctx := LoopContext{
		VarName: "file",
		Item:    "test.go",
		Index:   2,
		Count:   5,
		First:   false,
		Last:    false,
	}

	if ctx.VarName != "file" {
		t.Errorf("expected VarName 'file', got %q", ctx.VarName)
	}
	if ctx.Index != 2 {
		t.Errorf("expected Index 2, got %d", ctx.Index)
	}
	if ctx.First {
		t.Error("expected First to be false")
	}
}

func TestLoopControlConstants(t *testing.T) {
	if LoopControlNone != "" {
		t.Errorf("expected LoopControlNone to be empty, got %q", LoopControlNone)
	}
	if LoopControlBreak != "break" {
		t.Errorf("expected LoopControlBreak to be 'break', got %q", LoopControlBreak)
	}
	if LoopControlContinue != "continue" {
		t.Errorf("expected LoopControlContinue to be 'continue', got %q", LoopControlContinue)
	}
}

func TestDefaultMaxIterations(t *testing.T) {
	if DefaultMaxIterations != 100 {
		t.Errorf("expected DefaultMaxIterations to be 100, got %d", DefaultMaxIterations)
	}
}

func TestLoopConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config LoopConfig
		valid  bool
	}{
		{
			name:   "for-each loop",
			config: LoopConfig{Items: "${vars.files}", As: "file"},
			valid:  true,
		},
		{
			name:   "while loop",
			config: LoopConfig{While: "${vars.count} > 0", MaxIterations: IntOrExpr{Value: 50}},
			valid:  true,
		},
		{
			name:   "times loop",
			config: LoopConfig{Times: 5},
			valid:  true,
		},
		{
			name:   "with collect",
			config: LoopConfig{Times: 3, Collect: "results"},
			valid:  true,
		},
		{
			name:   "with delay",
			config: LoopConfig{Times: 3, Delay: Duration{Duration: time.Second}},
			valid:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify struct creation doesn't panic
			_ = tt.config
		})
	}
}

func TestSetAndClearLoopVars(t *testing.T) {
	state := &ExecutionState{
		Variables: make(map[string]interface{}),
	}

	// Set loop vars
	SetLoopVars(state, "item", "test-value", 2, 5)

	// Verify vars are set
	if state.Variables["loop.item"] != "test-value" {
		t.Errorf("expected loop.item to be 'test-value', got %v", state.Variables["loop.item"])
	}
	if state.Variables["loop.index"] != 2 {
		t.Errorf("expected loop.index to be 2, got %v", state.Variables["loop.index"])
	}
	if state.Variables["loop.count"] != 5 {
		t.Errorf("expected loop.count to be 5, got %v", state.Variables["loop.count"])
	}
	if state.Variables["loop.first"] != false {
		t.Error("expected loop.first to be false")
	}
	if state.Variables["loop.last"] != false {
		t.Error("expected loop.last to be false")
	}

	// Test first/last flags
	SetLoopVars(state, "item", "first-value", 0, 5)
	if state.Variables["loop.first"] != true {
		t.Error("expected loop.first to be true for index 0")
	}

	SetLoopVars(state, "item", "last-value", 4, 5)
	if state.Variables["loop.last"] != true {
		t.Error("expected loop.last to be true for last index")
	}

	// Clear loop vars
	ClearLoopVars(state, "item")

	if _, exists := state.Variables["loop.item"]; exists {
		t.Error("expected loop.item to be cleared")
	}
	if _, exists := state.Variables["loop.index"]; exists {
		t.Error("expected loop.index to be cleared")
	}
}

func TestLoopTimesExceedsMaxIterations(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
	}
	loopExec := NewLoopExecutor(executor)

	step := &Step{
		ID: "test-step",
		Loop: &LoopConfig{
			Times:         200,                  // Exceeds default 100
			MaxIterations: IntOrExpr{Value: 50}, // Explicit limit
		},
	}

	result := loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusFailed {
		t.Errorf("expected StatusFailed when times exceeds max_iterations, got %v", result.Status)
	}
	if result.Error == nil || result.Error.Type != "loop" {
		t.Error("expected loop error for exceeding max_iterations")
	}
}

func TestLoopMaxIterationsExprResolvesDefaults(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.defaults = map[string]interface{}{
		"hard_caps": map[string]interface{}{
			"foo": 10,
		},
	}
	executor.state = &ExecutionState{
		RunID:      "run-max-expr",
		WorkflowID: "workflow-max-expr",
		Variables: map[string]interface{}{
			"items": []interface{}{"a", "b", "c"},
		},
		Steps: make(map[string]StepResult),
	}

	step := &Step{
		ID: "max-expr-step",
		Loop: &LoopConfig{
			Items:         "${vars.items}",
			MaxIterations: IntOrExpr{Expr: "${defaults.hard_caps.foo}"},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Fatalf("Status = %v, want %v; error=%v", result.Status, StatusCompleted, result.Error)
	}
	if result.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3", result.Iterations)
	}
	if got := step.Loop.MaxIterations.Value; got != 10 {
		t.Fatalf("MaxIterations.Value = %d, want 10", got)
	}
}

// TestLoopMaxIterations_ExprResolvedAboveCapClampsToDefault covers
// bd-c2l8s: a max_iterations expression that resolves above
// DefaultMaxIterations is clamped to that cap, mirroring bd-ltghx's
// resolveForeachMaxRounds. Without the clamp, a misconfigured
// `max_iterations: ${vars.from_external}` (or a typoed defaults reference
// that lands on a giant integer) would drive the loop body unbounded.
// Literal max_iterations values are NOT clamped — the workflow author
// chose them explicitly.
//
// Test strategy: use a `times` loop where times exceeds DefaultMaxIterations
// but is far below the unclamped expression value. With the clamp, the
// resolved cap collapses to DefaultMaxIterations and `times > max_iterations`
// triggers the existing fail-closed guard with a deterministic error
// message. The recorded `step.Loop.MaxIterations.Value` also reflects the
// clamped value, which is the externally observable contract.
func TestLoopMaxIterations_ExprResolvedAboveCapClampsToDefault(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.defaults = map[string]interface{}{
		"hard_caps": map[string]interface{}{
			"crazy": 999999,
		},
	}
	executor.state = &ExecutionState{
		RunID:      "run-max-clamp",
		WorkflowID: "workflow-max-clamp",
		Variables:  map[string]interface{}{},
		Steps:      make(map[string]StepResult),
	}

	step := &Step{
		ID: "max-clamp-step",
		Loop: &LoopConfig{
			Times:         500,
			MaxIterations: IntOrExpr{Expr: "${defaults.hard_caps.crazy}"},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	// Times(500) > clamped max_iterations(100) → fail-closed at the
	// times-vs-cap guard with the clamped value in the error message.
	if result.Status != StatusFailed {
		t.Fatalf("Status = %v, want %v (clamped cap should reject Times=500); error=%+v", result.Status, StatusFailed, result.Error)
	}
	if result.Error == nil || !strings.Contains(result.Error.Message, "max_iterations (100)") {
		t.Fatalf("Error.Message = %v, want it to mention max_iterations (100) (the clamped cap)", result.Error)
	}
	if got := step.Loop.MaxIterations.Value; got != DefaultMaxIterations {
		t.Fatalf("MaxIterations.Value = %d, want %d (must record clamped value)", got, DefaultMaxIterations)
	}
}

func TestLoopMaxIterationsLiteralStillWorks(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.state = &ExecutionState{
		RunID:      "run-max-literal",
		WorkflowID: "workflow-max-literal",
		Variables:  make(map[string]interface{}),
		Steps:      make(map[string]StepResult),
	}

	step := &Step{
		ID: "max-literal-step",
		Loop: &LoopConfig{
			Times:         6,
			MaxIterations: IntOrExpr{Value: 6},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Fatalf("Status = %v, want %v; error=%v", result.Status, StatusCompleted, result.Error)
	}
	if result.Iterations != 6 {
		t.Fatalf("Iterations = %d, want 6", result.Iterations)
	}
	if got := step.Loop.MaxIterations.Value; got != 6 {
		t.Fatalf("MaxIterations.Value = %d, want 6", got)
	}
}

func TestExecuteIterationLoopControlBodyRunsBeforeBreak(t *testing.T) {
	// A loop body step that combines real work (command/template/prompt)
	// with loop_control: break must execute the body first and only break
	// AFTER the body completes. The legacy behaviour applied loop_control
	// before dispatch and silently dropped the body's side effects, which
	// foreach already fixed via bd-9yuk0; loops.go must match that contract.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "loop-ctrl-body",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "outer",
				Loop: &LoopConfig{
					Times: 5,
					Steps: []Step{
						{
							ID:          "do_then_break",
							Command:     "true",
							LoopControl: LoopControlBreak,
						},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := state.Steps["outer_iter0_do_then_break"].Status; got != StatusCompleted {
		t.Errorf("body status = %v, want %v (body must run before break)", got, StatusCompleted)
	}
	// Loop should break after the first iteration (only iter0 body, no iter1).
	if _, ran := state.Steps["outer_iter1_do_then_break"]; ran {
		t.Error("iter1 body executed; expected break after iter0")
	}
}

func TestExecuteIterationControlOnlyStepStillBreaks(t *testing.T) {
	// A pure control-only step (no command/template/prompt body, only
	// loop_control + optional when) must still apply the directive
	// without trying to dispatch a non-existent body. This case ensures
	// the new "execute body first" path doesn't inadvertently dispatch
	// empty steps as commands.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "loop-ctrl-only",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "outer",
				Loop: &LoopConfig{
					Times: 3,
					Steps: []Step{
						{
							ID:          "guard",
							LoopControl: LoopControlBreak,
						},
					},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("test-session")
	cfg.DryRun = true
	executor := NewExecutor(cfg)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Control-only step should not produce a recorded body result.
	if _, ran := state.Steps["outer_iter0_guard"]; ran {
		t.Error("control-only guard body executed; expected pure-control fast path")
	}
	// Outer loop step itself should be completed (broke on iter0).
	if got := state.Steps["outer"].Status; got != StatusCompleted {
		t.Errorf("outer loop status = %v, want %v", got, StatusCompleted)
	}
}

func TestLoopMaxIterationsExprFailsWhenUnresolvable(t *testing.T) {
	// Safety caps must fail closed: an explicit max_iterations expression that
	// cannot resolve to a positive integer (typo, missing default, malformed
	// reference) must fail the loop step rather than silently substituting
	// DefaultMaxIterations. Falling back would let an intended lower cap turn
	// into 100 iterations — the opposite of the safety guarantee.
	var buf bytes.Buffer
	restore := capturePipelineLogs(t, &buf)
	defer restore()

	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.defaults = map[string]interface{}{}
	executor.state = &ExecutionState{
		RunID:      "run-max-fail",
		WorkflowID: "workflow-max-fail",
		Variables:  make(map[string]interface{}),
		Steps:      make(map[string]StepResult),
	}

	step := &Step{
		ID: "max-fail-step",
		Loop: &LoopConfig{
			Times:         3,
			MaxIterations: IntOrExpr{Expr: "${defaults.unknown}"},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusFailed {
		t.Fatalf("Status = %v, want %v", result.Status, StatusFailed)
	}
	if result.Error == nil {
		t.Fatalf("Error = nil, want structured StepError")
	}
	if result.Error.Type != "loop" {
		t.Errorf("Error.Type = %q, want %q", result.Error.Type, "loop")
	}
	if !strings.Contains(result.Error.Message, "loop.max_iterations") {
		t.Errorf("Error.Message = %q, want it to mention loop.max_iterations", result.Error.Message)
	}
	if result.Iterations != 0 {
		t.Errorf("Iterations = %d, want 0 (loop must not run on unresolved cap)", result.Iterations)
	}

	events := parseJSONLEvents(t, &buf)
	assertEvent(t, events, EventSubstWarn,
		FieldStepID, "max-fail-step",
		FieldSubstitutionKey, "loop.max_iterations",
	)
}

func TestLoopMaxIterationsAbsentUsesDefault(t *testing.T) {
	// Sanity check: when max_iterations is omitted entirely (zero value), the
	// default safety cap still applies and the loop runs normally.
	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.defaults = map[string]interface{}{}
	executor.state = &ExecutionState{
		RunID:      "run-max-absent",
		WorkflowID: "workflow-max-absent",
		Variables:  make(map[string]interface{}),
		Steps:      make(map[string]StepResult),
	}

	step := &Step{
		ID: "max-absent-step",
		Loop: &LoopConfig{
			Times: 3,
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %v, want %v; error=%v", result.Status, StatusCompleted, result.Error)
	}
	if result.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3", result.Iterations)
	}
	if got := step.Loop.MaxIterations.Value; got != DefaultMaxIterations {
		t.Fatalf("MaxIterations.Value = %d, want %d", got, DefaultMaxIterations)
	}
}

func TestLoopCancelledContext(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
	}
	loopExec := NewLoopExecutor(executor)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	step := &Step{
		ID: "test-step",
		Loop: &LoopConfig{
			Times: 10,
		},
	}

	result := loopExec.ExecuteLoop(ctx, step, &Workflow{})

	if result.Status != StatusCancelled {
		t.Errorf("expected StatusCancelled, got %v", result.Status)
	}
}

func TestExecuteLoopIntegration(t *testing.T) {
	config := ExecutorConfig{
		Session:       "test-session",
		DryRun:        true, // Don't actually execute
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)

	// Initialize state
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
		Steps:     make(map[string]StepResult),
	}

	step := &Step{
		ID: "loop-step",
		Loop: &LoopConfig{
			Times: 0, // Zero iterations = immediate completion
		},
	}

	result := executor.executeLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted for zero iterations, got %v", result.Status)
	}
}

func TestNewLoopExecutor(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	loopExec := NewLoopExecutor(executor)

	if loopExec == nil {
		t.Fatal("expected non-nil LoopExecutor")
	}
	if loopExec.executor != executor {
		t.Error("expected LoopExecutor to reference the original executor")
	}
}

func TestStoreCollected(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
	}
	loopExec := NewLoopExecutor(executor)

	collected := []interface{}{"result1", "result2", "result3"}
	loopExec.storeCollected("my_results", collected)

	val, ok := executor.state.Variables["my_results"]
	if !ok {
		t.Fatal("expected my_results variable to be set")
	}
	stored, ok := val.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", val)
	}
	if len(stored) != 3 {
		t.Errorf("expected 3 collected items, got %d", len(stored))
	}
}

func TestStoreCollected_Empty(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test"})
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
	}
	loopExec := NewLoopExecutor(executor)

	loopExec.storeCollected("empty_results", []interface{}{})

	val, ok := executor.state.Variables["empty_results"]
	if !ok {
		t.Fatal("expected empty_results variable to be set")
	}
	stored, ok := val.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", val)
	}
	if len(stored) != 0 {
		t.Errorf("expected 0 collected items, got %d", len(stored))
	}
}

func TestExecuteWhile_DryRun_ImmediateFalse(t *testing.T) {
	config := ExecutorConfig{
		Session:       "test-session",
		DryRun:        true,
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{
			"condition": "false",
		},
		Steps: make(map[string]StepResult),
	}

	step := &Step{
		ID:     "while-step",
		Prompt: "While iteration",
		Loop: &LoopConfig{
			While:         "${vars.condition}",
			MaxIterations: IntOrExpr{Value: 10},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %v", result.Status)
	}
	if result.Iterations != 0 {
		t.Errorf("expected 0 iterations (condition immediately false), got %d", result.Iterations)
	}
}

func TestExecuteWhile_Cancelled(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{Session: "test", DryRun: true})
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{
			"running": "true",
		},
		Steps: make(map[string]StepResult),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	step := &Step{
		ID:     "while-cancel-step",
		Prompt: "While cancel test",
		Loop: &LoopConfig{
			While:         "${vars.running}",
			MaxIterations: IntOrExpr{Value: 100},
		},
	}

	result := executor.loopExec.ExecuteLoop(ctx, step, &Workflow{})

	if result.Status != StatusCancelled {
		t.Errorf("expected StatusCancelled, got %v", result.Status)
	}
}

func TestExecuteLoop_ForEachDryRun(t *testing.T) {
	config := ExecutorConfig{
		Session:       "test-session",
		DryRun:        true,
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{
			"files": []interface{}{"a.go", "b.go", "c.go"},
		},
		Steps: make(map[string]StepResult),
	}

	step := &Step{
		ID:     "foreach-step",
		Prompt: "Process ${loop.item}",
		Loop: &LoopConfig{
			Items: "${vars.files}",
			As:    "file",
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %v", result.Status)
	}
	if result.Iterations != 3 {
		t.Errorf("expected 3 iterations, got %d", result.Iterations)
	}
}

func TestExecuteLoop_TimesWithCollect(t *testing.T) {
	config := ExecutorConfig{
		Session:       "test-session",
		DryRun:        true,
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)
	executor.state = &ExecutionState{
		Variables: make(map[string]interface{}),
		Steps:     make(map[string]StepResult),
	}

	step := &Step{
		ID:     "collect-step",
		Prompt: "Iteration ${loop.index}",
		Loop: &LoopConfig{
			Times:   3,
			Collect: "outputs",
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})

	if result.Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %v", result.Status)
	}
	if result.Iterations != 3 {
		t.Errorf("expected 3 iterations, got %d", result.Iterations)
	}
}

// TestExecuteForEach_RecordsAndVerifiesItemsFingerprint covers bd-3awat:
// fresh foreach runs persist a fingerprint of the resolved items, and a
// resume against a divergent items list fails fast instead of silently
// applying old completion records to different items.
func TestExecuteForEach_RecordsAndVerifiesItemsFingerprint(t *testing.T) {
	config := ExecutorConfig{
		Session:       "fp-session",
		DryRun:        true,
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{
			"items": []interface{}{"a", "b", "c"},
		},
		Steps: make(map[string]StepResult),
	}

	step := &Step{
		ID:     "fanout",
		Prompt: "Process ${loop.item}",
		Loop: &LoopConfig{
			Items: "${vars.items}",
			As:    "item",
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusCompleted {
		t.Fatalf("first run status = %v, want completed", result.Status)
	}
	state, ok := executor.state.ForeachState["fanout"]
	if !ok {
		t.Fatalf("ForeachState[\"fanout\"] missing after first run")
	}
	if state.ItemsFingerprint == "" {
		t.Fatal("ItemsFingerprint empty after first run; expected fingerprint to be recorded")
	}
	if len(state.CompletedIterationIDs) != 3 {
		t.Fatalf("CompletedIterationIDs after first run = %#v, want 3 entries", state.CompletedIterationIDs)
	}
	originalFingerprint := state.ItemsFingerprint

	// Resume scenario: items unchanged. The legacy loop executor's resume
	// path picks up where the prior run left off; here the prior run
	// completed all iterations so no body re-runs, but verifyForeachItems
	// must accept the unchanged fingerprint.
	executor.state.Variables["items"] = []interface{}{"a", "b", "c"}
	result = executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusCompleted {
		t.Fatalf("unchanged-items resume status = %v, want completed", result.Status)
	}
	if got := executor.state.ForeachState["fanout"].ItemsFingerprint; got != originalFingerprint {
		t.Fatalf("ItemsFingerprint changed across unchanged-items resume: was %s, got %s", originalFingerprint, got)
	}

	// Drift scenario: items reordered. Index-keyed completion records
	// would silently apply iteration 0's record to a different items[0],
	// so resume must fail with a clear error.
	executor.state.Variables["items"] = []interface{}{"c", "b", "a"}
	result = executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusFailed {
		t.Fatalf("reordered-items resume status = %v, want failed", result.Status)
	}
	if result.Error == nil || !strings.Contains(result.Error.Message, "items changed since prior run") {
		t.Fatalf("reordered-items resume error = %#v, want items-changed message", result.Error)
	}

	// Drift scenario: items list shrunk to a different content. Same
	// failure path — fingerprint mismatch.
	executor.state.Variables["items"] = []interface{}{"a", "b"}
	result = executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusFailed {
		t.Fatalf("shrunk-items resume status = %v, want failed", result.Status)
	}
}

// TestExecuteForEach_ResumePreservesCollectedFromPriorIterations covers
// bd-t3q8a: a mid-loop resume must reconstruct loop.Collect outputs from
// iterations completed in the prior run before continuing, otherwise
// storeCollected at loop end overwrites the variable with only the
// resumed iterations' outputs and silently drops the prior ones.
//
// This test simulates the post-completion resume case (all iterations
// already done in a prior run): the current run should not re-execute
// any iteration body but must still write the full collected variable.
// The single-iteration-body test path requires a fully-initialized
// executor graph and is exercised separately at the helper level via
// the appendForeachCollectedOutput / loadForeachCollectedOutputs
// round-trip in TestForeachCollectedOutputsPersistRoundTrip below.
func TestExecuteForEach_ResumePreservesCollectedFromPriorIterations(t *testing.T) {
	config := ExecutorConfig{
		Session:       "collect-resume-session",
		DryRun:        true,
		GlobalTimeout: 30 * time.Second,
	}
	executor := NewExecutor(config)
	items := []interface{}{"a", "b", "c"}
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{
			"items": items,
		},
		Steps: make(map[string]StepResult),
	}

	step := &Step{
		ID: "fanout",
		Loop: &LoopConfig{
			Items:   "${vars.items}",
			As:      "item",
			Collect: "outputs",
		},
	}

	// All iterations completed in prior run; only the fingerprint check,
	// the prepopulate-from-state path, and storeCollected should run.
	fp := computeForeachItemsFingerprint(items)
	executor.state.ForeachState = map[string]ForeachIterationState{
		"fanout": {
			StepID:                "fanout",
			Total:                 3,
			CurrentIteration:      3,
			CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2"},
			ItemsFingerprint:      fp,
			CollectedOutputs:      []interface{}{"prior-out-0", "prior-out-1", "prior-out-2"},
		},
	}

	result := executor.loopExec.ExecuteLoop(context.Background(), step, &Workflow{})
	if result.Status != StatusCompleted {
		t.Fatalf("resume status = %v, want completed", result.Status)
	}
	if got := len(result.Collected); got != 3 {
		t.Fatalf("result.Collected length = %d, want 3 (all prior preserved): %#v", got, result.Collected)
	}
	want := []string{"prior-out-0", "prior-out-1", "prior-out-2"}
	for i, v := range want {
		if result.Collected[i] != v {
			t.Fatalf("result.Collected[%d] = %v, want %v", i, result.Collected[i], v)
		}
	}
	stored, ok := executor.state.Variables["outputs"].([]interface{})
	if !ok {
		t.Fatalf("vars[\"outputs\"] type = %T, want []interface{}", executor.state.Variables["outputs"])
	}
	if len(stored) != 3 {
		t.Fatalf("stored outputs length = %d, want 3: %#v", len(stored), stored)
	}
}

// TestForeachCollectedOutputsPersistRoundTrip exercises the bd-t3q8a
// persistence helpers directly (append + load + restore-from-prior).
// Direct helper coverage avoids the executor graph wiring required by a
// full executeStep path while still proving that successive resumes can
// rebuild the collected variable losslessly.
func TestForeachCollectedOutputsPersistRoundTrip(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("collect-helper-session"))
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{},
		Steps:     map[string]StepResult{},
	}

	if got := executor.loadForeachCollectedOutputs("fanout"); got != nil {
		t.Fatalf("load before any append = %#v, want nil", got)
	}

	executor.appendForeachCollectedOutput("fanout", "out-0")
	executor.appendForeachCollectedOutput("fanout", map[string]interface{}{"k": "v"})
	executor.appendForeachCollectedOutput("fanout", []interface{}{1.0, 2.0})

	got := executor.loadForeachCollectedOutputs("fanout")
	if len(got) != 3 {
		t.Fatalf("load after appends = %#v, want 3 entries", got)
	}
	if got[0] != "out-0" {
		t.Fatalf("entry 0 = %v, want \"out-0\"", got[0])
	}

	// load returns a copy: mutating it must not corrupt the persisted state.
	got[0] = "mutated"
	persisted := executor.state.ForeachState["fanout"].CollectedOutputs
	if persisted[0] != "out-0" {
		t.Fatalf("persisted entry 0 mutated through load() copy: %v", persisted[0])
	}

	// Other steps stay isolated.
	if got := executor.loadForeachCollectedOutputs("other"); got != nil {
		t.Fatalf("load for unknown step = %#v, want nil", got)
	}
}

// TestForceResumeIteration_TruncatesCollectedOutputs covers the
// bd-t3q8a interaction with bd-a3fwf force-iter resume: rewinding to
// iteration N must drop persisted CollectedOutputs[N:] so the rerun's
// fresh entries don't sit alongside the stale prior ones in the final
// stored variable.
func TestForceResumeIteration_TruncatesCollectedOutputs(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("force-collect-session"))
	executor.state = &ExecutionState{
		Variables: map[string]interface{}{},
		Steps:     map[string]StepResult{},
		ForeachState: map[string]ForeachIterationState{
			"fanout": {
				StepID:                "fanout",
				Total:                 4,
				CurrentIteration:      4,
				CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3"},
				CollectedOutputs:      []interface{}{"out0", "out1", "out2", "out3"},
			},
		},
	}

	executor.forceResumeIteration("fanout", 2)

	got := executor.state.ForeachState["fanout"].CollectedOutputs
	want := []interface{}{"out0", "out1"}
	if len(got) != len(want) {
		t.Fatalf("CollectedOutputs after force-iter to 2 = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CollectedOutputs[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// Rewinding to iteration 0 should clear all collected entries.
	executor.state.ForeachState["fanout"] = ForeachIterationState{
		StepID:                "fanout",
		Total:                 4,
		CurrentIteration:      4,
		CompletedIterationIDs: []string{"fanout_iter0", "fanout_iter1", "fanout_iter2", "fanout_iter3"},
		CollectedOutputs:      []interface{}{"out0", "out1", "out2", "out3"},
	}
	executor.forceResumeIteration("fanout", 0)
	if got := executor.state.ForeachState["fanout"].CollectedOutputs; len(got) != 0 {
		t.Fatalf("CollectedOutputs after force-iter to 0 = %#v, want empty", got)
	}
}

// TestExecuteIteration_CompletedAllStepsSignal covers bd-vq8bc: the
// fourth return value from executeIteration must report whether every
// step in loop.Steps was processed. shouldCompleteForeachIteration uses
// it to distinguish a fully-completed iteration from a partial iteration
// cancelled mid-body — without this signal, partial iterations get
// silently marked complete and resume drops the unfinished body steps.
func TestExecuteIteration_CompletedAllStepsSignal(t *testing.T) {
	// Pre-cancelled context: executeIteration's top-of-loop ctx check
	// triggers immediately, so completedAllSteps must be false even
	// though zero body steps have been observed.
	t.Run("cancelled before any step runs", func(t *testing.T) {
		cfg := DefaultExecutorConfig("vq8bc-test-session")
		cfg.DryRun = true
		executor := NewExecutor(cfg)
		executor.state = &ExecutionState{
			Variables: map[string]interface{}{},
			Steps:     map[string]StepResult{},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		step := &Step{ID: "fanout"}
		loop := &LoopConfig{
			Steps: []Step{
				{ID: "first", Command: "true"},
				{ID: "second", Command: "true"},
			},
		}
		results, shouldBreak, shouldContinue, completedAll := executor.loopExec.executeIteration(ctx, step, loop, &Workflow{Settings: DefaultWorkflowSettings()}, 0)
		if completedAll {
			t.Fatal("completedAllSteps = true after pre-cancelled ctx; want false")
		}
		if shouldBreak || shouldContinue {
			t.Fatalf("shouldBreak=%v shouldContinue=%v after pre-cancellation; want both false", shouldBreak, shouldContinue)
		}
		if len(results) != 0 {
			t.Fatalf("results = %#v after pre-cancellation; want empty", results)
		}
	})

	// Clean context: every body step processes naturally so the signal
	// must report completion.
	t.Run("clean run sets completedAllSteps", func(t *testing.T) {
		cfg := DefaultExecutorConfig("vq8bc-test-session")
		cfg.DryRun = true
		executor := NewExecutor(cfg)
		executor.state = &ExecutionState{
			Variables: map[string]interface{}{},
			Steps:     map[string]StepResult{},
		}
		// graph required by executeStep; build from the workflow.
		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          "vq8bc-clean",
			Settings:      DefaultWorkflowSettings(),
			Steps: []Step{
				{
					ID: "fanout",
					Loop: &LoopConfig{
						Times: 1,
						Steps: []Step{
							{ID: "first", Command: "true"},
							{ID: "second", Command: "true"},
						},
					},
				},
			},
		}
		executor.graph = NewDependencyGraph(workflow)
		step := &workflow.Steps[0]
		results, shouldBreak, shouldContinue, completedAll := executor.loopExec.executeIteration(context.Background(), step, step.Loop, workflow, 0)
		if !completedAll {
			t.Fatalf("completedAllSteps = false after clean run; want true (results=%#v)", results)
		}
		if shouldBreak || shouldContinue {
			t.Fatalf("shouldBreak=%v shouldContinue=%v after clean run; want both false", shouldBreak, shouldContinue)
		}
		if len(results) != 2 {
			t.Fatalf("results = %#v; want 2 entries (one per body step)", results)
		}
	})
}

// TestComputeForeachItemsFingerprint_StableAndDivergent verifies the
// fingerprint is deterministic for equivalent inputs and diverges for
// reordered or modified inputs (bd-3awat).
func TestComputeForeachItemsFingerprint_StableAndDivergent(t *testing.T) {
	a := computeForeachItemsFingerprint([]interface{}{"x", "y", "z"})
	b := computeForeachItemsFingerprint([]interface{}{"x", "y", "z"})
	if a != b {
		t.Fatalf("fingerprint not stable for identical inputs: %s vs %s", a, b)
	}

	reordered := computeForeachItemsFingerprint([]interface{}{"z", "y", "x"})
	if reordered == a {
		t.Fatalf("fingerprint identical for reordered inputs: %s", reordered)
	}

	shrunk := computeForeachItemsFingerprint([]interface{}{"x", "y"})
	if shrunk == a {
		t.Fatalf("fingerprint identical for shrunk inputs: %s", shrunk)
	}

	emptyA := computeForeachItemsFingerprint(nil)
	emptyB := computeForeachItemsFingerprint([]interface{}{})
	if emptyA != emptyB {
		t.Fatalf("nil and empty-slice fingerprints differ: %s vs %s", emptyA, emptyB)
	}
}
