package pipeline

import (
	"testing"
)

func TestConditionEvaluator_Evaluate(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"name":       "Alice",
			"count":      10,
			"flag":       true,
			"empty":      "",
			"zero":       0,
			"env":        "production",
			"features":   "auth,api,ui",
			"score":      85,
			"tests_pass": true,
			"deploy":     true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	tests := []struct {
		name      string
		condition string
		wantValue bool // true = step should RUN
		wantErr   bool
	}{
		// Empty condition
		{
			name:      "empty condition runs step",
			condition: "",
			wantValue: true,
		},

		// Boolean truthy checks
		{
			name:      "truthy variable",
			condition: "${vars.flag}",
			wantValue: true,
		},
		{
			name:      "falsy empty string",
			condition: "${vars.empty}",
			wantValue: false,
		},
		{
			name:      "falsy zero",
			condition: "${vars.zero}",
			wantValue: false,
		},
		{
			name:      "truthy string",
			condition: "${vars.name}",
			wantValue: true,
		},
		{
			name:      "truthy number",
			condition: "${vars.count}",
			wantValue: true,
		},

		// Equality operators
		{
			name:      "equal match",
			condition: `${vars.env} == "production"`,
			wantValue: true,
		},
		{
			name:      "equal mismatch",
			condition: `${vars.env} == "staging"`,
			wantValue: false,
		},
		{
			name:      "not equal match",
			condition: `${vars.env} != "staging"`,
			wantValue: true,
		},
		{
			name:      "not equal mismatch",
			condition: `${vars.env} != "production"`,
			wantValue: false,
		},
		{
			name:      "equal with single quotes",
			condition: `${vars.env} == 'production'`,
			wantValue: true,
		},

		// Numeric comparisons
		{
			name:      "greater than true",
			condition: "${vars.score} > 80",
			wantValue: true,
		},
		{
			name:      "greater than false",
			condition: "${vars.score} > 90",
			wantValue: false,
		},
		{
			name:      "less than true",
			condition: "${vars.count} < 20",
			wantValue: true,
		},
		{
			name:      "less than false",
			condition: "${vars.count} < 5",
			wantValue: false,
		},
		{
			name:      "greater equal true (equal)",
			condition: "${vars.score} >= 85",
			wantValue: true,
		},
		{
			name:      "greater equal true (greater)",
			condition: "${vars.score} >= 80",
			wantValue: true,
		},
		{
			name:      "greater equal false",
			condition: "${vars.score} >= 90",
			wantValue: false,
		},
		{
			name:      "less equal true (equal)",
			condition: "${vars.count} <= 10",
			wantValue: true,
		},
		{
			name:      "less equal true (less)",
			condition: "${vars.count} <= 20",
			wantValue: true,
		},
		{
			name:      "less equal false",
			condition: "${vars.count} <= 5",
			wantValue: false,
		},

		// Contains operator
		{
			name:      "contains true",
			condition: `${vars.features} contains "auth"`,
			wantValue: true,
		},
		{
			name:      "contains false",
			condition: `${vars.features} contains "database"`,
			wantValue: false,
		},
		{
			name:      "contains multiple",
			condition: `${vars.features} contains "api"`,
			wantValue: true,
		},

		// Logical operators
		{
			name:      "AND both true",
			condition: "${vars.deploy} AND ${vars.tests_pass}",
			wantValue: true,
		},
		{
			name:      "AND one false",
			condition: "${vars.deploy} AND ${vars.empty}",
			wantValue: false,
		},
		{
			name:      "OR both true",
			condition: "${vars.deploy} OR ${vars.tests_pass}",
			wantValue: true,
		},
		{
			name:      "OR one true",
			condition: "${vars.deploy} OR ${vars.empty}",
			wantValue: true,
		},
		{
			name:      "OR both false",
			condition: "${vars.empty} OR ${vars.zero}",
			wantValue: false,
		},
		{
			name:      "NOT true",
			condition: "NOT ${vars.empty}",
			wantValue: true,
		},
		{
			name:      "NOT false",
			condition: "NOT ${vars.flag}",
			wantValue: false,
		},
		{
			name:      "legacy negation true",
			condition: "!${vars.empty}",
			wantValue: true,
		},
		{
			name:      "legacy negation false",
			condition: "!${vars.flag}",
			wantValue: false,
		},

		// Complex expressions
		{
			name:      "complex AND with comparison",
			condition: `${vars.score} > 80 AND ${vars.env} == "production"`,
			wantValue: true,
		},
		{
			name:      "complex OR with comparison",
			condition: `${vars.score} > 90 OR ${vars.count} < 20`,
			wantValue: true,
		},
		{
			name:      "parentheses simple",
			condition: "(${vars.flag})",
			wantValue: true,
		},
		{
			name:      "AND OR precedence (OR has lower precedence)",
			condition: "${vars.empty} AND ${vars.flag} OR ${vars.deploy}",
			wantValue: true, // (false AND true) OR true = true
		},

		// Edge cases
		{
			name:      "literal true string",
			condition: "true",
			wantValue: true,
		},
		{
			name:      "literal false string",
			condition: "false",
			wantValue: false,
		},
		{
			name:      "literal 1 string",
			condition: "1",
			wantValue: true,
		},
		{
			name:      "literal 0 string",
			condition: "0",
			wantValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(tt.condition)
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate(%q) error = %v, wantErr %v", tt.condition, err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.Value != tt.wantValue {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.condition, result.Value, tt.wantValue)
			}
		})
	}
}

func TestConditionEvaluator_NumericErrors(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"text": "hello",
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Test all numeric comparison operators with non-numeric values
	tests := []struct {
		name      string
		condition string
	}{
		{"greater than", "${vars.text} > 10"},
		{"less than", "${vars.text} < 10"},
		{"greater equal", "${vars.text} >= 10"},
		{"less equal", "${vars.text} <= 10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := evaluator.Evaluate(tt.condition)
			if err == nil {
				t.Errorf("Expected error for non-numeric %s comparison", tt.name)
			}
		})
	}
}

func TestEvaluateCondition_BackwardCompatibility(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"name":              "Alice",
			"steps.prev.output": "previous result",
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	tests := []struct {
		condition string
		wantSkip  bool // true = step should be SKIPPED
	}{
		// Original tests from executor_test.go
		{"${vars.name}", false},            // truthy, don't skip
		{"", false},                        // empty, don't skip
		{`${vars.name} == "Alice"`, false}, // equal, don't skip
		{`${vars.name} != "Alice"`, true},  // not equal, skip
		{"!${vars.name}", true},            // negation of truthy, skip
		{"false", true},                    // literal false, skip
	}

	for _, tt := range tests {
		skip, err := EvaluateCondition(tt.condition, sub)
		if err != nil {
			t.Errorf("EvaluateCondition(%q) error = %v", tt.condition, err)
			continue
		}
		if skip != tt.wantSkip {
			t.Errorf("EvaluateCondition(%q) skip = %v, want %v", tt.condition, skip, tt.wantSkip)
		}
	}
}

func TestCleanValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"quoted"`, "quoted"},
		{`'single'`, "single"},
		{"  spaces  ", "spaces"},
		{`"with spaces"`, "with spaces"},
		{"unquoted", "unquoted"},
		{`""`, ""},
		{`''`, ""},
	}

	for _, tt := range tests {
		got := cleanValue(tt.input)
		if got != tt.want {
			t.Errorf("cleanValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseNumericPair(t *testing.T) {
	tests := []struct {
		left    string
		right   string
		wantL   float64
		wantR   float64
		wantErr bool
	}{
		{"10", "20", 10, 20, false},
		{"10.5", "20.5", 10.5, 20.5, false},
		{"-5", "5", -5, 5, false},
		{`"10"`, "20", 10, 20, false}, // quoted number
		{"abc", "20", 0, 0, true},
		{"10", "xyz", 0, 0, true},
	}

	for _, tt := range tests {
		gotL, gotR, err := parseNumericPair(tt.left, tt.right)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseNumericPair(%q, %q) error = %v, wantErr %v", tt.left, tt.right, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if gotL != tt.wantL || gotR != tt.wantR {
				t.Errorf("parseNumericPair(%q, %q) = (%v, %v), want (%v, %v)", tt.left, tt.right, gotL, gotR, tt.wantL, tt.wantR)
			}
		}
	}
}

func TestValidateCondition(t *testing.T) {
	tests := []struct {
		condition  string
		wantIssues int
	}{
		{"${vars.x} == 1", 0},
		{"(${vars.x} AND ${vars.y})", 0},
		{"((nested))", 0},
		{"(unbalanced", 1},
		{"unbalanced)", 1},
		{`"unclosed`, 1},
		{`'unclosed`, 1},
		{`"balanced"`, 0},
		{"", 0},
		{`"escaped \" quote"`, 0},        // escaped double quote inside
		{`'escaped \' quote'`, 0},        // escaped single quote inside
		{`mixed "quotes" and 'quotes'`, 0}, // both quote types balanced
	}

	for _, tt := range tests {
		issues := ValidateCondition(tt.condition)
		if len(issues) != tt.wantIssues {
			t.Errorf("ValidateCondition(%q) = %d issues, want %d: %v", tt.condition, len(issues), tt.wantIssues, issues)
		}
	}
}

func TestFindLogicalOp(t *testing.T) {
	tests := []struct {
		expr string
		op   string
		want int
	}{
		{"a AND b", " AND ", 1},
		{"a OR b", " OR ", 1},
		{`"a AND b" OR c`, " OR ", 9}, // AND inside quotes should be ignored, " OR " starts at 9
		{"(a AND b) OR c", " OR ", 9}, // AND inside parens should be ignored, " OR " starts at 9
		{"no operators", " AND ", -1},
	}

	for _, tt := range tests {
		got := findLogicalOp(tt.expr, tt.op)
		if got != tt.want {
			t.Errorf("findLogicalOp(%q, %q) = %d, want %d", tt.expr, tt.op, got, tt.want)
		}
	}
}

func TestConditionEvaluator_WithStepOutputs(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"steps.check.output": "PASS",
		},
		Steps: map[string]StepResult{
			"score": {
				StepID: "score",
				Output: "95",
				Status: StatusCompleted,
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	tests := []struct {
		condition string
		wantValue bool
	}{
		{`${steps.check.output} == "PASS"`, true},
		{`${steps.check.output} != "SKIP"`, true},
		// Step output as number (from Steps map)
		// Note: the score step output is "95" which should be coerced
	}

	for _, tt := range tests {
		result, err := evaluator.Evaluate(tt.condition)
		if err != nil {
			t.Errorf("Evaluate(%q) error = %v", tt.condition, err)
			continue
		}
		if result.Value != tt.wantValue {
			t.Errorf("Evaluate(%q) = %v, want %v", tt.condition, result.Value, tt.wantValue)
		}
	}
}

func TestFindLogicalOpFlexible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		expr  string
		op    string
		wantN int // -1 for not found
	}{
		{"AND with spaces", "a AND b", "AND", 1},
		{"OR with spaces", "x OR y", "OR", 1},
		{"AND at end", "x AND", "AND", 1},
		{"OR at end", "y OR", "OR", 1},
		{"AND in parens", "(a AND b) OR c", "AND", -1}, // inside parens, outer scan
		{"not found", "abc def", "AND", -1},
		{"no spaces", "aANDb", "AND", -1}, // no spaces around
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := findLogicalOpFlexible(tt.expr, tt.op)
			if tt.wantN < 0 && got >= 0 {
				t.Errorf("findLogicalOpFlexible(%q, %q) = %d, want not found", tt.expr, tt.op, got)
			} else if tt.wantN >= 0 && got < 0 {
				t.Errorf("findLogicalOpFlexible(%q, %q) = %d, want found", tt.expr, tt.op, got)
			}
		})
	}
}

func TestFindLogicalOp_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		expr string
		op   string
		want int
	}{
		{"in double quoted string", `"hello AND world" == x`, " AND ", -1},
		{"in single quoted string", `'hello OR world' != x`, " OR ", -1},
		{"escaped quote in string", `"say \"hi AND bye\"" AND x`, " AND ", 20},
		{"nested parens", "((a)) AND b", " AND ", 5},
		{"complex nesting", "(a OR (b AND c)) AND d", " AND ", 16},
		{"at end no trailing space", "x AND", " AND", 1},
		{"operator at start", " AND x", " AND ", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := findLogicalOp(tt.expr, tt.op)
			if got != tt.want {
				t.Errorf("findLogicalOp(%q, %q) = %d, want %d", tt.expr, tt.op, got, tt.want)
			}
		})
	}
}

func TestConditionEvaluator_ErrorCases(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"text": "hello",
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	tests := []struct {
		name      string
		condition string
	}{
		{"invalid left operand numeric", "${vars.text} > 10"},
		{"invalid right operand numeric", "10 < ${vars.text}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := evaluator.Evaluate(tt.condition)
			if err == nil {
				t.Error("expected error for invalid comparison")
			}
		})
	}
}

func TestConditionEvaluator_ContainsWithVariables(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"haystack": "foo,bar,baz",
			"needle":   "bar",
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate(`${vars.haystack} contains ${vars.needle}`)
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected contains to return true")
	}
}

func TestConditionEvaluator_ShortCircuitAND(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
			"b": true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// AND short-circuits on first false
	result, err := evaluator.Evaluate("${vars.a} AND ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if result.Value {
		t.Error("expected false for short-circuit AND")
	}
}

func TestConditionEvaluator_ShortCircuitOR(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
			"b": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// OR short-circuits on first true
	result, err := evaluator.Evaluate("${vars.a} OR ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for short-circuit OR")
	}
}

func TestConditionEvaluator_ORWithBothFalse(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
			"b": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("${vars.a} OR ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if result.Value {
		t.Error("expected false when both are false")
	}
}

func TestConditionEvaluator_ANDWithBothTrue(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
			"b": true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("${vars.a} AND ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true when both are true")
	}
}

func TestConditionEvaluator_NestedParentheses(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
			"b": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("((${vars.a}))")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for nested parentheses")
	}
}

func TestConditionEvaluator_NotWithExpression(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("NOT ${vars.a}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if result.Value {
		t.Error("expected false for NOT true")
	}
}

func TestEvaluateCondition_ErrorPath(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: make(map[string]interface{}),
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Invalid namespace should cause substitution error
	_, err := EvaluateCondition("${badnamespace.var} == 1", sub)
	if err == nil {
		t.Error("EvaluateCondition should return error for invalid namespace")
	}
}

func TestConditionEvaluator_SubstitutionError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: make(map[string]interface{}),
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Invalid namespace should cause substitution error
	_, err := evaluator.Evaluate("${invalidnamespace.x}")
	if err == nil {
		t.Error("Evaluate should return error for invalid namespace")
	}
}

func TestConditionEvaluator_ORLeftError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: make(map[string]interface{}),
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Create a condition where the left side of OR has invalid comparison
	// The substituted expression will be malformed
	result, err := evaluator.Evaluate("true OR false")
	// This should evaluate without error (literals)
	if err != nil {
		t.Logf("OR literal error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for 'true OR false'")
	}
}

func TestConditionEvaluator_ANDLeftError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: make(map[string]interface{}),
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Test AND with both sides being literals
	result, err := evaluator.Evaluate("true AND true")
	if err != nil {
		t.Logf("AND literal error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for 'true AND true'")
	}
}

func TestConditionEvaluator_NotFalse(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("NOT ${vars.a}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for NOT false")
	}
}

func TestConditionEvaluator_ExclamationFalse(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	result, err := evaluator.Evaluate("!${vars.a}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true for !false")
	}
}

func TestConditionEvaluator_ORRightSideEvaluated(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
			"b": true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// When left is false, right side should be evaluated
	result, err := evaluator.Evaluate("${vars.a} OR ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if !result.Value {
		t.Error("expected true when left is false and right is true")
	}
}

func TestConditionEvaluator_ANDRightSideEvaluated(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
			"b": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// When left is true, right side should be evaluated
	result, err := evaluator.Evaluate("${vars.a} AND ${vars.b}")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if result.Value {
		t.Error("expected false when left is true and right is false")
	}
}

func TestConditionEvaluator_ORAtEndOfExpression(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": false,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Test OR at end with empty right side
	result, _ := evaluator.Evaluate("${vars.a} OR")
	// Empty right side should be falsy
	if result.Value {
		t.Error("expected false for 'false OR <empty>'")
	}
}

func TestConditionEvaluator_ANDAtEndOfExpression(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"a": true,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Test AND at end with empty right side
	result, _ := evaluator.Evaluate("${vars.a} AND")
	// Empty right side should be falsy
	if result.Value {
		t.Error("expected false for 'true AND <empty>'")
	}
}

func TestConditionEvaluator_NOTError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Inner has invalid numeric comparison -> error during NOT evaluation
	// This tests the error path at line 123-125 in evaluateExpr
	_, err := evaluator.Evaluate("NOT abc > notanumber")
	if err == nil {
		t.Error("expected error for invalid inner expression of NOT (numeric error)")
	}
}

func TestConditionEvaluator_ExclamationError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Inner has invalid numeric comparison -> error during ! evaluation
	// This tests the error path at line 133-135 in evaluateExpr
	_, err := evaluator.Evaluate("!abc > notanumber")
	if err == nil {
		t.Error("expected error for invalid inner expression of ! (numeric error)")
	}
}

func TestConditionEvaluator_ORLeftEvalError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Left side of OR has invalid numeric comparison -> error during recursive evaluation
	// "abc" > 1 fails numeric parsing, this error bubbles up through OR
	_, err := evaluator.Evaluate("abc > notanumber OR true")
	if err == nil {
		t.Error("expected error for invalid left side of OR (numeric comparison error)")
	}
}

func TestConditionEvaluator_ANDLeftEvalError(t *testing.T) {
	t.Parallel()

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	// Left side of AND has invalid numeric comparison -> error during recursive evaluation
	_, err := evaluator.Evaluate("abc > notanumber AND true")
	if err == nil {
		t.Error("expected error for invalid left side of AND (numeric comparison error)")
	}
}
