package pipeline

import (
	"context"
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

func TestErrLoopBreak(t *testing.T) {
	err := &ErrLoopBreak{Reason: "condition met"}
	expected := "loop break: condition met"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}

	err2 := &ErrLoopBreak{}
	if err2.Error() != "loop break" {
		t.Errorf("expected 'loop break', got %q", err2.Error())
	}
}

func TestErrLoopContinue(t *testing.T) {
	err := &ErrLoopContinue{}
	if err.Error() != "loop continue" {
		t.Errorf("expected 'loop continue', got %q", err.Error())
	}
}

func TestErrMaxIterations(t *testing.T) {
	err := &ErrMaxIterations{Limit: 100}
	expected := "max iterations limit reached (100)"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
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
			config: LoopConfig{While: "${vars.count} > 0", MaxIterations: 50},
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
			Times:         200, // Exceeds default 100
			MaxIterations: 50,  // Explicit limit
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
			MaxIterations: 10,
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
			MaxIterations: 100,
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
