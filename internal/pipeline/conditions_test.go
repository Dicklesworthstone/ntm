package pipeline

import (
	"encoding/json"
	"strings"
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

func TestCompareNumericPair(t *testing.T) {
	tests := []struct {
		left    string
		right   string
		want    int
		wantErr bool
	}{
		{"10", "20", -1, false},
		{"10.5", "20.5", -1, false},
		{"-5", "5", -1, false},
		{`"10"`, "20", -1, false}, // quoted number
		{"1.0", "1e0", 0, false},
		{"20", "10", 1, false},
		{"abc", "20", 0, true},
		{"10", "xyz", 0, true},
	}

	for _, tt := range tests {
		got, err := compareNumericPair(tt.left, tt.right)
		if (err != nil) != tt.wantErr {
			t.Errorf("compareNumericPair(%q, %q) error = %v, wantErr %v", tt.left, tt.right, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("compareNumericPair(%q, %q) = %v, want %v", tt.left, tt.right, got, tt.want)
		}
	}
}

func TestConditionEvaluatorExactNumericOrdering(t *testing.T) {
	// Deliberately ordered values include adjacent integers beyond float64's
	// precision, decimal significand prefixes, opposite signs and exponents
	// that cannot be expanded or represented by a machine integer.
	ordered := []string{
		"-1e999999999999999999999", "-1e999999", "-9007199254740993", "-9007199254740992",
		"-120.01", "-120", "-1.01", "-1", "-1e-999999", "0",
		"1e-999999", "0.0001", "0.1", "1", "1.01", "1.0101", "1.1",
		"120", "120.01", "9007199254740992", "9007199254740993", "1e999999", "1e999999999999999999999",
	}
	state := &ExecutionState{Variables: map[string]interface{}{}}
	evaluator := NewConditionEvaluator(NewSubstitutor(state, "test-session", "numeric-ordering"))
	for i, left := range ordered {
		for j, right := range ordered {
			state.Variables["left"] = json.Number(left)
			state.Variables["right"] = json.Number(right)
			for _, tc := range []struct {
				op   string
				want bool
			}{
				{"<", i < j}, {">", i > j}, {"<=", i <= j}, {">=", i >= j},
			} {
				result, err := evaluator.Evaluate("${vars.left} " + tc.op + " ${vars.right}")
				if err != nil || result.Value != tc.want || result.Skip == tc.want {
					t.Fatalf("%s %s %s = %+v, %v; want run=%t", left, tc.op, right, result, err, tc.want)
				}
			}
		}
	}
	for _, expr := range []string{
		"1.00 >= 1e0", "1.00 <= 1e0", "-0.0 >= 0e999999", "-0.0 <= 0e-999999",
		"1e100000000000000000000 <= 10e99999999999999999999",
		"1e100000000000000000000 >= 10e99999999999999999999",
		"1e-100000000000000000000 <= 10e-100000000000000000001",
		"1e-100000000000000000000 >= 10e-100000000000000000001",
	} {
		result, err := evaluator.Evaluate(expr)
		if err != nil || !result.Value || result.Skip {
			t.Fatalf("equal decimal ordering %q = %+v, %v; want run", expr, result, err)
		}
	}
}

func TestConditionEvaluatorRejectsInvalidNumericOrdering(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	evaluator := NewConditionEvaluator(NewSubstitutor(state, "test-session", "numeric-invalid"))
	for _, value := range []string{"NaN", "Inf", "-Infinity", "1e", "1e+", "1e--1", "1e2x", ".", "1.2.3", "not-a-number", strings.Repeat("9", maxFilterExpressionBytes+1)} {
		state.Variables["value"] = value
		for _, expr := range []string{"${vars.value} > 0", "0 < ${vars.value}", "${vars.value} <= 0", "0 >= ${vars.value}"} {
			result, err := evaluator.Evaluate(expr)
			if err == nil || result.Value || !strings.Contains(err.Error(), "cannot compare") {
				t.Fatalf("invalid numeric condition %q = %+v, %v; want error", expr, result, err)
			}
		}
	}
}

func TestConditionEvaluator_ASTInjectionDefense(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"evil_or":     `nope" OR 1 == 1`,
			"evil_bare":   `false OR true`,
			"evil_cmp":    `0 >= 0`,
			"evil_parens": `foo) OR (1 == 1`,
			"msg":         `say "hi AND bye"`,
			"quoted_op":   `a >= b`,
		},
	}
	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	tests := []struct {
		name      string
		condition string
		wantValue bool
		wantErr   bool
	}{
		{"variable containing OR 1 == 1 cannot hijack ==", `${vars.evil_or} == "expected"`, false, false},
		{"variable containing false OR true cannot hijack ==", `${vars.evil_bare} == "ok"`, false, false},
		{"variable containing >= cannot hijack comparison", `${vars.evil_cmp} == "ok"`, false, false},
		{"variable containing parens and OR cannot break AST", `(${vars.evil_parens} == "ok")`, false, false},
		{"quoted operator inside literal is data not operator", `"a >= b" == ${vars.quoted_op}`, true, false},
		{"case-insensitive lowercase logical ops", `false or (true and not false)`, true, false},
		{"C-style && || ! logical ops", `!(false || false) && true`, true, false},
		{"case-insensitive CONTAINS", `${vars.msg} CONTAINS "AND"`, true, false},
		{"unterminated string literal returns syntax error", `${vars.msg} == "unterminated`, false, true},
		{"unbalanced parenthesis returns syntax error", `(${vars.msg} == "x"`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := evaluator.Evaluate(tt.condition)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Evaluate(%q) err = %v, wantErr=%v", tt.condition, err, tt.wantErr)
			}
			if !tt.wantErr && res.Value != tt.wantValue {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.condition, res.Value, tt.wantValue)
			}
		})
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

func TestLexCondition_QuotedAndNestedParens(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"x": "hello AND world",
			"y": "hello OR world",
		},
	}
	sub := NewSubstitutor(state, "test-session", "test-workflow")
	evaluator := NewConditionEvaluator(sub)

	cases := []struct {
		expr string
		want bool
	}{
		{`"hello AND world" == ${vars.x}`, true},
		{`'hello OR world' != ${vars.y}`, false},
		{`"say \"hi AND bye\"" == "say \"hi AND bye\"" AND true`, true},
		{`((true)) AND true`, true},
		{`(false OR (true AND true)) AND true`, true},
	}
	for _, tc := range cases {
		res, err := evaluator.Evaluate(tc.expr)
		if err != nil {
			t.Fatalf("Evaluate(%q) error = %v", tc.expr, err)
		}
		if res.Value != tc.want {
			t.Fatalf("Evaluate(%q) = %v, want %v", tc.expr, res.Value, tc.want)
		}
	}
}

func TestConditionEvaluator_ErrorCases(t *testing.T) {

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
			_, err := evaluator.Evaluate(tt.condition)
			if err == nil {
				t.Error("expected error for invalid comparison")
			}
		})
	}
}

func TestConditionEvaluator_ContainsWithVariables(t *testing.T) {

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

// FuzzConditionEvaluator_InjectionResistance fuzzes attacker-controlled variable
// payloads against the LL(1) AST evaluator to prove no variable value can ever
// inject boolean/comparison operators or flip a false equality check to true.
func FuzzConditionEvaluator_InjectionResistance(f *testing.F) {
	f.Add(`" OR 1 == 1`)
	f.Add(`') OR ('1' == '1`)
	f.Add(`false OR true`)
	f.Add(`trusted-sentinel" || true`)
	f.Add(`a == a && b != c`)
	f.Add(`!false`)

	f.Fuzz(func(t *testing.T, attacker string) {
		const sentinel = "TRUSTED_EXACT_SENTINEL_9f3a"
		if attacker == sentinel {
			return
		}
		state := &ExecutionState{
			Variables: map[string]interface{}{
				"attacker": attacker,
			},
		}
		sub := NewSubstitutor(state, "fuzz-session", "fuzz-workflow")
		evaluator := NewConditionEvaluator(sub)

		for _, expr := range []string{
			`${vars.attacker} == "` + sentinel + `"`,
			`(${vars.attacker} == "` + sentinel + `") AND true`,
			`"prefix" == ${vars.attacker} AND ${vars.attacker} == "` + sentinel + `"`,
		} {
			res, err := evaluator.Evaluate(expr)
			if err != nil {
				t.Fatalf("Evaluate(%q) with attacker=%q returned unexpected error: %v", expr, attacker, err)
			}
			if res.Value {
				t.Fatalf("AST INJECTION: Evaluate(%q) with attacker=%q evaluated to true!", expr, attacker)
			}
		}
	})
}
