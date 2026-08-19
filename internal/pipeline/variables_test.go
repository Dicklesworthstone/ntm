package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrepareWorkflowVariablesPrecedenceAndTypes(t *testing.T) {
	workflow := &Workflow{
		Vars: map[string]VarDef{
			"name":    {Default: "default-name", Type: VarTypeString},
			"count":   {Default: 1, Type: VarTypeNumber},
			"enabled": {Default: false, Type: VarTypeBoolean},
			"items":   {Default: []interface{}{"default"}, Type: VarTypeArray},
		},
	}

	got, err := PrepareWorkflowVariables(workflow, map[string]interface{}{
		"name":    "cli-name",
		"count":   "5",
		"enabled": "yes",
		"items":   "a,b,c",
	})
	if err != nil {
		t.Fatalf("PrepareWorkflowVariables() error = %v", err)
	}

	if got["name"] != "cli-name" {
		t.Fatalf("name = %v, want CLI override", got["name"])
	}
	if got["count"] != 5 {
		t.Fatalf("count = %#v, want int 5", got["count"])
	}
	if got["enabled"] != true {
		t.Fatalf("enabled = %#v, want true", got["enabled"])
	}
	wantItems := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got["items"], wantItems) {
		t.Fatalf("items = %#v, want %#v", got["items"], wantItems)
	}
}

func TestPrepareWorkflowVariablesNativeJSONTypes(t *testing.T) {
	workflow := &Workflow{
		Vars: map[string]VarDef{
			"count": {Type: VarTypeNumber},
			"items": {Type: VarTypeArray},
		},
	}

	got, err := PrepareWorkflowVariables(workflow, map[string]interface{}{
		"count": float64(2.5),
		"items": []interface{}{"x", "y"},
	})
	if err != nil {
		t.Fatalf("PrepareWorkflowVariables() error = %v", err)
	}
	if got["count"] != float64(2.5) {
		t.Fatalf("count = %#v, want native float64", got["count"])
	}
	if !reflect.DeepEqual(got["items"], []interface{}{"x", "y"}) {
		t.Fatalf("items = %#v, want native array", got["items"])
	}
}

func TestPrepareWorkflowVariablesDefaultReferences(t *testing.T) {
	workflow := &Workflow{
		Vars: map[string]VarDef{
			"a": {Default: "${vars.b}", Type: VarTypeString},
			"b": {Default: "hello", Type: VarTypeString},
		},
	}

	got, err := PrepareWorkflowVariables(workflow, nil)
	if err != nil {
		t.Fatalf("PrepareWorkflowVariables() error = %v", err)
	}
	if got["a"] != "hello" {
		t.Fatalf("a = %#v, want chained default", got["a"])
	}
}

func TestPrepareWorkflowVariablesErrors(t *testing.T) {
	tests := []struct {
		name      string
		workflow  *Workflow
		overrides map[string]interface{}
		want      string
	}{
		{
			name: "number mismatch",
			workflow: &Workflow{Vars: map[string]VarDef{
				"n": {Type: VarTypeNumber},
			}},
			overrides: map[string]interface{}{"n": "abc"},
			want:      "variable n: expected number, got 'abc'",
		},
		{
			name: "cyclic defaults",
			workflow: &Workflow{Vars: map[string]VarDef{
				"a": {Default: "${vars.b}"},
				"b": {Default: "${vars.a}"},
			}},
			want: "cyclic default reference",
		},
		{
			// A typo'd reference must fail closed rather than passing through
			// as the literal "${vars.projet_name}" placeholder. Otherwise the
			// unresolved marker enters execution and dispatches to prompts.
			name: "default with typo'd vars reference",
			workflow: &Workflow{Vars: map[string]VarDef{
				"name": {Type: VarTypeString, Default: "${vars.projet_name}"},
			}},
			want: "default \"${vars.projet_name}\"",
		},
		{
			name: "default with missing env reference",
			workflow: &Workflow{Vars: map[string]VarDef{
				"endpoint": {Type: VarTypeString, Default: "${env.NTM_TEST_DEFINITELY_UNSET_VAR_4o0rn}"},
			}},
			want: "environment variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareWorkflowVariables(tt.workflow, tt.overrides)
			if err == nil {
				t.Fatal("PrepareWorkflowVariables() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PrepareWorkflowVariables() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPrepareWorkflowVariablesDefaultWithExplicitFallback(t *testing.T) {
	// An explicit | fallback in the default keeps the existing escape hatch:
	// authors who intentionally reference a variable that may be undefined can
	// still provide a literal fallback. This case must continue to succeed.
	workflow := &Workflow{
		Vars: map[string]VarDef{
			"name": {Type: VarTypeString, Default: `${vars.projet_name | "fallback-name"}`},
		},
	}

	got, err := PrepareWorkflowVariables(workflow, nil)
	if err != nil {
		t.Fatalf("PrepareWorkflowVariables() error = %v, want nil for default with explicit fallback", err)
	}
	if got["name"] != "fallback-name" {
		t.Fatalf("name = %#v, want %q", got["name"], "fallback-name")
	}
}

func TestValidateWorkflowVariablesWarningsAndHints(t *testing.T) {
	workflow := &Workflow{
		Vars: map[string]VarDef{
			"n":        {Type: VarTypeNumber},
			"flag":     {Type: VarTypeBoolean},
			"items":    {Type: VarTypeArray},
			"required": {Type: VarTypeString, Required: true},
		},
	}

	result, parseErr := ValidateWorkflowVariables(workflow, map[string]interface{}{
		"n":        "5",
		"flag":     "true",
		"items":    "a,b,c",
		"required": "ok",
		"extra":    "available",
	})
	if parseErr != nil {
		t.Fatalf("ValidateWorkflowVariables() error = %v", parseErr)
	}
	if got := result.Variables["n"]; got != 5 {
		t.Fatalf("n = %#v, want int 5", got)
	}
	if got := result.Variables["flag"]; got != true {
		t.Fatalf("flag = %#v, want true", got)
	}
	if !reflect.DeepEqual(result.Variables["items"], []string{"a", "b", "c"}) {
		t.Fatalf("items = %#v, want []string", result.Variables["items"])
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0].Message, "undeclared variable \"extra\"") {
		t.Fatalf("warnings = %#v, want undeclared variable warning", result.Warnings)
	}
}

func TestValidateWorkflowVariablesErrors(t *testing.T) {
	tests := []struct {
		name      string
		workflow  *Workflow
		overrides map[string]interface{}
		wantMsg   string
		wantHint  string
	}{
		{
			name: "number mismatch",
			workflow: &Workflow{Vars: map[string]VarDef{
				"n": {Type: VarTypeNumber},
			}},
			overrides: map[string]interface{}{"n": "abc"},
			wantMsg:   "variable n: expected number, got 'abc'",
			wantHint:  "use --var n=5 or --var n=5.0",
		},
		{
			name: "array mismatch",
			workflow: &Workflow{Vars: map[string]VarDef{
				"items": {Type: VarTypeArray},
			}},
			overrides: map[string]interface{}{"items": 12},
			wantMsg:   "variable items: expected array",
			wantHint:  "provide \"items\" as a JSON array",
		},
		{
			name: "required missing",
			workflow: &Workflow{Vars: map[string]VarDef{
				"token": {Type: VarTypeString, Required: true},
			}},
			wantMsg:  "variable token: required value missing",
			wantHint: "provide --var token=value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, parseErr := ValidateWorkflowVariables(tt.workflow, tt.overrides)
			if parseErr == nil {
				t.Fatal("ValidateWorkflowVariables() error = nil, want error")
			}
			if !strings.Contains(parseErr.Message, tt.wantMsg) {
				t.Fatalf("Message = %q, want %q", parseErr.Message, tt.wantMsg)
			}
			if !strings.Contains(parseErr.Hint, tt.wantHint) {
				t.Fatalf("Hint = %q, want %q", parseErr.Hint, tt.wantHint)
			}
		})
	}
}

func TestSubstitutor_Substitute(t *testing.T) {
	state := &ExecutionState{
		RunID:      "run-123",
		WorkflowID: "test-workflow",
		Variables: map[string]interface{}{
			"name":  "Alice",
			"count": 42,
			"flag":  true,
			"nested": map[string]interface{}{
				"deep": map[string]interface{}{
					"value": "found",
				},
				"items": []interface{}{"a", "b", "c"},
			},
		},
		Steps: map[string]StepResult{
			"step1": {
				StepID:     "step1",
				Status:     StatusCompleted,
				Output:     "step1 output",
				PaneUsed:   "pane-1",
				AgentType:  "claude",
				StartedAt:  time.Now().Add(-time.Minute),
				FinishedAt: time.Now(),
				ParsedData: map[string]interface{}{
					"result": "parsed value",
					"count":  100,
				},
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "my-workflow")

	tests := []struct {
		name     string
		template string
		want     string
		wantErr  bool
	}{
		{
			name:     "simple var",
			template: "Hello ${vars.name}!",
			want:     "Hello Alice!",
		},
		{
			name:     "numeric var",
			template: "Count: ${vars.count}",
			want:     "Count: 42",
		},
		{
			name:     "boolean var",
			template: "Flag: ${vars.flag}",
			want:     "Flag: true",
		},
		{
			name:     "nested var",
			template: "Value: ${vars.nested.deep.value}",
			want:     "Value: found",
		},
		{
			name:     "array access",
			template: "Second: ${vars.nested.items.1}",
			want:     "Second: b",
		},
		{
			name:     "step output",
			template: "Output: ${steps.step1.output}",
			want:     "Output: step1 output",
		},
		{
			name:     "step status",
			template: "Status: ${steps.step1.status}",
			want:     "Status: completed",
		},
		{
			name:     "step pane",
			template: "Pane: ${steps.step1.pane}",
			want:     "Pane: pane-1",
		},
		{
			name:     "step agent",
			template: "Agent: ${steps.step1.agent}",
			want:     "Agent: claude",
		},
		{
			name:     "step parsed data",
			template: "Result: ${steps.step1.data.result}",
			want:     "Result: parsed value",
		},
		{
			name:     "session context",
			template: "Session: ${session}",
			want:     "Session: test-session",
		},
		{
			name:     "run_id context",
			template: "Run: ${run_id}",
			want:     "Run: run-123",
		},
		{
			name:     "workflow context",
			template: "Workflow: ${workflow}",
			want:     "Workflow: my-workflow",
		},
		{
			name:     "default value (var undefined)",
			template: "User: ${vars.undefined | \"default\"}",
			want:     "User: default",
		},
		{
			name:     "default value (var defined)",
			template: "Name: ${vars.name | \"fallback\"}",
			want:     "Name: Alice",
		},
		{
			name:     "default single quotes",
			template: "X: ${vars.missing | 'single'}",
			want:     "X: single",
		},
		{
			name:     "default no quotes",
			template: "Y: ${vars.missing | bare}",
			want:     "Y: bare",
		},
		{
			name:     "escaped variable",
			template: "Literal: \\${vars.name}",
			want:     "Literal: ${vars.name}",
		},
		{
			name:     "mixed escaped and real",
			template: "Real: ${vars.name}, Literal: \\${vars.count}",
			want:     "Real: Alice, Literal: ${vars.count}",
		},
		{
			name:     "multiple vars",
			template: "${vars.name} has ${vars.count} items",
			want:     "Alice has 42 items",
		},
		{
			name:     "no vars",
			template: "Plain text",
			want:     "Plain text",
		},
		{
			name:     "timestamp exists",
			template: "Time: ${timestamp}",
			want:     "", // Will check it matches pattern
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sub.Substitute(tt.template)
			if (err != nil) != tt.wantErr {
				t.Errorf("Substitute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.name == "timestamp exists" {
				// Just verify it's a valid timestamp
				if got == "" || got == "Time: ${timestamp}" {
					t.Errorf("Substitute() timestamp not resolved")
				}
				return
			}

			if got != tt.want {
				t.Errorf("Substitute() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubstitutor_EnvVars(t *testing.T) {
	t.Setenv("TEST_VAR", "test_value")

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Env: ${env.TEST_VAR}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "Env: test_value" {
		t.Errorf("Substitute() = %q, want %q", got, "Env: test_value")
	}
}

func TestSubstitutor_EnvHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/ntm-home")

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Home: ${env.HOME}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "Home: /tmp/ntm-home" {
		t.Errorf("Substitute() = %q, want %q", got, "Home: /tmp/ntm-home")
	}
}

func TestSubstitutor_EnvMissingErrors(t *testing.T) {
	t.Setenv("NONEXISTENT_VAR_123", "")
	os.Unsetenv("NONEXISTENT_VAR_123")

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Missing: ${env.NONEXISTENT_VAR_123}")
	if err == nil {
		t.Fatal("expected missing environment variable error")
	}
	if got != "Missing: ${env.NONEXISTENT_VAR_123}" {
		t.Errorf("Substitute() = %q, want unresolved env reference", got)
	}
	subErr, ok := err.(*SubstitutionError)
	if !ok {
		t.Fatalf("error = %T, want *SubstitutionError", err)
	}
	if subErr.VarRef != "env.NONEXISTENT_VAR_123" {
		t.Errorf("VarRef = %q, want env.NONEXISTENT_VAR_123", subErr.VarRef)
	}
	if !strings.Contains(subErr.Message, "environment variable NONEXISTENT_VAR_123 not set") {
		t.Errorf("Message = %q, want missing env explanation", subErr.Message)
	}
}

func TestSubstitutor_EnvMissingDefault(t *testing.T) {
	t.Setenv("OPTIONAL_ENV_VAR_123", "")
	os.Unsetenv("OPTIONAL_ENV_VAR_123")

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute("Optional: ${env.OPTIONAL_ENV_VAR_123 | fallback}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "Optional: fallback" {
		t.Errorf("Substitute() = %q, want %q", got, "Optional: fallback")
	}
}

// bd-wwmo9: when a default fallback contains a variable reference, the
// substitutor must recurse into it. Earlier the outer loop saw resolved == 0
// after the fallback expanded and exited with the literal ${...} text in
// place. Counting the default-replacement as progress lets the next pass
// resolve the fallback.
func TestSubstitutor_DefaultFallbackRecursesIntoVariable(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"present": "ok",
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	got, err := sub.Substitute(`${vars.missing | ${vars.present}}`)
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("Substitute() = %q, want %q", got, "ok")
	}

	// Nested two-deep default chain — outer fallback is itself a variable
	// whose own default is yet another variable.
	state.Variables["fallback_target"] = "ok"
	got, err = sub.Substitute(`${vars.missing_a | ${vars.missing_b | ${vars.fallback_target}}}`)
	if err != nil {
		t.Fatalf("two-deep Substitute() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("two-deep Substitute() = %q, want %q", got, "ok")
	}
}

func TestSubstitutor_LoopVars(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	// Set loop context
	SetLoopVars(state, "file", "test.txt", 2, 5)

	sub := NewSubstitutor(state, "sess", "wf")

	tests := []struct {
		template string
		want     string
	}{
		{"File: ${loop.file}", "File: test.txt"},
		{"Item: ${loop.item}", "Item: test.txt"},
		{"Index: ${loop.index}", "Index: 2"},
		{"Count: ${loop.count}", "Count: 5"},
		{"First: ${loop.first}", "First: false"},
		{"Last: ${loop.last}", "Last: false"},
	}

	for _, tt := range tests {
		got, err := sub.Substitute(tt.template)
		if err != nil {
			t.Errorf("Substitute(%q) error = %v", tt.template, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Substitute(%q) = %q, want %q", tt.template, got, tt.want)
		}
	}

	// Test clear
	ClearLoopVars(state, "file")
	got, _ := sub.Substitute("After clear: ${loop.file | \"cleared\"}")
	if got != "After clear: cleared" {
		t.Errorf("After clear should use default, got %q", got)
	}
}

func TestSubstitutor_SubstituteStrict(t *testing.T) {
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"defined": "value",
		},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	// Should succeed for defined var
	got, err := sub.SubstituteStrict("Value: ${vars.defined}")
	if err != nil {
		t.Errorf("SubstituteStrict() unexpected error: %v", err)
	}
	if got != "Value: value" {
		t.Errorf("SubstituteStrict() = %q, want %q", got, "Value: value")
	}

	// Should fail for undefined var without default
	_, err = sub.SubstituteStrict("Value: ${vars.undefined}")
	if err == nil {
		t.Error("SubstituteStrict() should error for undefined var")
	}

	// Should succeed for undefined var with default
	got, err = sub.SubstituteStrict("Value: ${vars.undefined | \"default\"}")
	if err != nil {
		t.Errorf("SubstituteStrict() with default unexpected error: %v", err)
	}
	if got != "Value: default" {
		t.Errorf("SubstituteStrict() = %q, want %q", got, "Value: default")
	}
}

func TestOutputParser_ParseFirstLine(t *testing.T) {
	parser := NewOutputParser()

	tests := []struct {
		output string
		want   string
	}{
		{"first\nsecond\nthird", "first"},
		{"\n\nthird", "third"},
		{"single", "single"},
		{"  trimmed  \nnext", "trimmed"},
		{"", ""},
	}

	for _, tt := range tests {
		got, err := parser.Parse(tt.output, OutputParse{Type: "first_line"})
		if err != nil {
			t.Errorf("Parse(%q, first_line) error = %v", tt.output, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Parse(%q, first_line) = %q, want %q", tt.output, got, tt.want)
		}
	}
}

func TestOutputParser_ParseLines(t *testing.T) {
	parser := NewOutputParser()

	output := "line1\n\nline2\n  line3  \n"
	got, err := parser.Parse(output, OutputParse{Type: "lines"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	lines, ok := got.([]string)
	if !ok {
		t.Fatalf("Parse() returned %T, want []string", got)
	}

	want := []string{"line1", "line2", "line3"}
	if len(lines) != len(want) {
		t.Fatalf("Parse() returned %d lines, want %d", len(lines), len(want))
	}

	for i, line := range lines {
		if line != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, line, want[i])
		}
	}
}

func TestOutputParser_ParseJSON(t *testing.T) {
	parser := NewOutputParser()

	tests := []struct {
		name   string
		output string
		check  func(interface{}) bool
	}{
		{
			name:   "simple object",
			output: `{"key": "value", "count": 42}`,
			check: func(v interface{}) bool {
				m, ok := v.(map[string]interface{})
				return ok && m["key"] == "value" && m["count"] == float64(42)
			},
		},
		{
			name:   "array",
			output: `[1, 2, 3]`,
			check: func(v interface{}) bool {
				a, ok := v.([]interface{})
				return ok && len(a) == 3
			},
		},
		{
			name:   "json with prefix",
			output: `Some text here {"key": "value"}`,
			check: func(v interface{}) bool {
				m, ok := v.(map[string]interface{})
				return ok && m["key"] == "value"
			},
		},
		{
			name:   "json with suffix",
			output: `{"key": "value"} and more text`,
			check: func(v interface{}) bool {
				m, ok := v.(map[string]interface{})
				return ok && m["key"] == "value"
			},
		},
		{
			name:   "array before object",
			output: `[1, 2] {"key": "value"}`,
			check: func(v interface{}) bool {
				// Should parse the array since it comes first
				a, ok := v.([]interface{})
				return ok && len(a) == 2
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.Parse(tt.output, OutputParse{Type: "json"})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !tt.check(got) {
				t.Errorf("Parse() = %v, check failed", got)
			}
		})
	}
}

func TestOutputParser_ParseYAML(t *testing.T) {
	parser := NewOutputParser()

	output := `
name: test
items:
  - one
  - two
count: 10
`

	got, err := parser.Parse(output, OutputParse{Type: "yaml"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("Parse() returned %T, want map", got)
	}

	if m["name"] != "test" {
		t.Errorf("name = %v, want test", m["name"])
	}
	if m["count"] != 10 {
		t.Errorf("count = %v, want 10", m["count"])
	}

	items, ok := m["items"].([]interface{})
	if !ok || len(items) != 2 {
		t.Errorf("items = %v, want [one, two]", m["items"])
	}
}

func TestOutputParser_ParseRegex(t *testing.T) {
	parser := NewOutputParser()

	tests := []struct {
		name    string
		output  string
		pattern string
		check   func(interface{}) bool
	}{
		{
			name:    "named groups",
			output:  "Count: 42, Name: Alice",
			pattern: `Count: (?P<count>\d+), Name: (?P<name>\w+)`,
			check: func(v interface{}) bool {
				m, ok := v.(map[string]interface{})
				return ok && m["count"] == "42" && m["name"] == "Alice"
			},
		},
		{
			name:    "single capture group",
			output:  "The value is 123",
			pattern: `value is (\d+)`,
			check: func(v interface{}) bool {
				// Returns []string for backward compatibility
				a, ok := v.([]string)
				return ok && len(a) == 1 && a[0] == "123"
			},
		},
		{
			name:    "multiple capture groups",
			output:  "X=10 Y=20",
			pattern: `X=(\d+) Y=(\d+)`,
			check: func(v interface{}) bool {
				// Returns []string for backward compatibility
				a, ok := v.([]string)
				return ok && len(a) == 2 && a[0] == "10" && a[1] == "20"
			},
		},
		{
			name:    "no match",
			output:  "no numbers here",
			pattern: `\d+`,
			check: func(v interface{}) bool {
				return v == nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.Parse(tt.output, OutputParse{Type: "regex", Pattern: tt.pattern})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !tt.check(got) {
				t.Errorf("Parse() = %v, check failed", got)
			}
		})
	}
}

func TestNavigateNested(t *testing.T) {
	data := map[string]interface{}{
		"level1": map[string]interface{}{
			"level2": map[string]interface{}{
				"value": "deep",
			},
			"array": []interface{}{"a", "b", "c"},
		},
	}

	tests := []struct {
		parts   []string
		want    interface{}
		wantErr bool
	}{
		{[]string{"level1", "level2", "value"}, "deep", false},
		{[]string{"level1", "array", "1"}, "b", false},
		{[]string{"level1", "array", "5"}, nil, true},          // out of bounds
		{[]string{"level1", "missing"}, nil, true},             // field not found
		{[]string{"level1", "array", "notanumber"}, nil, true}, // invalid index
	}

	for _, tt := range tests {
		got, err := navigateNested(data, tt.parts)
		if (err != nil) != tt.wantErr {
			t.Errorf("navigateNested(%v) error = %v, wantErr %v", tt.parts, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("navigateNested(%v) = %v, want %v", tt.parts, got, tt.want)
		}
	}
}

func TestNavigateNested_EdgeCases(t *testing.T) {

	tests := []struct {
		name    string
		value   interface{}
		parts   []string
		want    interface{}
		wantErr bool
	}{
		{
			name:    "nil value",
			value:   nil,
			parts:   []string{"foo"},
			wantErr: true,
		},
		{
			name: "map[interface{}]interface{} - YAML style",
			value: map[interface{}]interface{}{
				"key1": "value1",
				"key2": map[interface{}]interface{}{
					"nested": "nestedvalue",
				},
			},
			parts: []string{"key1"},
			want:  "value1",
		},
		{
			name: "map[interface{}]interface{} - nested",
			value: map[interface{}]interface{}{
				"outer": map[interface{}]interface{}{
					"inner": "deep",
				},
			},
			parts: []string{"outer", "inner"},
			want:  "deep",
		},
		{
			name: "map[interface{}]interface{} - missing key",
			value: map[interface{}]interface{}{
				"key1": "value1",
			},
			parts:   []string{"missing"},
			wantErr: true,
		},
		{
			name:  "[]string array",
			value: []string{"alpha", "beta", "gamma"},
			parts: []string{"1"},
			want:  "beta",
		},
		{
			name:    "[]string array - out of bounds",
			value:   []string{"alpha", "beta"},
			parts:   []string{"5"},
			wantErr: true,
		},
		{
			name:    "[]string array - invalid index",
			value:   []string{"alpha", "beta"},
			parts:   []string{"abc"},
			wantErr: true,
		},
		{
			name:    "[]string array - negative index",
			value:   []string{"alpha", "beta"},
			parts:   []string{"-1"},
			wantErr: true,
		},
		{
			name: "map[string]string - parallel collect output",
			value: map[string]string{
				"left":  "left output",
				"right": "right output",
			},
			parts: []string{"left"},
			want:  "left output",
		},
		{
			name: "map[string]string - missing key",
			value: map[string]string{
				"left": "left output",
			},
			parts:   []string{"missing"},
			wantErr: true,
		},
		{
			name:    "[]interface{} array - negative index",
			value:   []interface{}{"a", "b"},
			parts:   []string{"-1"},
			wantErr: true,
		},
		{
			name:    "unsupported type (struct)",
			value:   struct{ Field string }{"value"},
			parts:   []string{"Field"},
			wantErr: true,
		},
		{
			name:    "access field on string",
			value:   "just a string",
			parts:   []string{"field"},
			wantErr: true,
		},
		{
			name:  "empty parts returns original",
			value: "original",
			parts: []string{},
			want:  "original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := navigateNested(tt.value, tt.parts)
			if tt.wantErr {
				if err == nil {
					t.Errorf("navigateNested() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("navigateNested() unexpected error: %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("navigateNested() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {

	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{
			name:  "nil",
			value: nil,
			want:  "",
		},
		{
			name:  "string",
			value: "hello world",
			want:  "hello world",
		},
		{
			name:  "bool true",
			value: true,
			want:  "true",
		},
		{
			name:  "bool false",
			value: false,
			want:  "false",
		},
		{
			name:  "int positive",
			value: 42,
			want:  "42",
		},
		{
			name:  "int negative",
			value: -10,
			want:  "-10",
		},
		{
			name:  "int zero",
			value: 0,
			want:  "0",
		},
		{
			name:  "int64",
			value: int64(9223372036854775807),
			want:  "9223372036854775807",
		},
		{
			name:  "float64 integer-like",
			value: 42.0,
			want:  "42",
		},
		{
			name:  "float64 with decimals",
			value: 3.14159,
			want:  "3.14159",
		},
		{
			name:  "[]byte",
			value: []byte("byte slice"),
			want:  "byte slice",
		},
		{
			name:  "map - JSON marshaled",
			value: map[string]string{"key": "value"},
			want:  `{"key":"value"}`,
		},
		{
			name:  "slice - JSON marshaled",
			value: []int{1, 2, 3},
			want:  "[1,2,3]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.value)
			if got != tt.want {
				t.Errorf("formatValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestFormatValue_TimeTypes(t *testing.T) {
	// Time tests aren't parallel because they use fixed time values

	// Test time.Time
	testTime := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	got := formatValue(testTime)
	want := "2025-06-15T10:30:00Z"
	if got != want {
		t.Errorf("formatValue(time.Time) = %q, want %q", got, want)
	}

	// Test time.Duration
	duration := 5*time.Hour + 30*time.Minute + 15*time.Second
	got = formatValue(duration)
	want = "5h30m15s"
	if got != want {
		t.Errorf("formatValue(time.Duration) = %q, want %q", got, want)
	}
}

func TestFormatValue_JSONMarshalFailure(t *testing.T) {

	// Create a channel, which cannot be JSON marshaled
	ch := make(chan int)
	got := formatValue(ch)

	// Should fall back to fmt.Sprintf("%v")
	if got == "" {
		t.Error("expected non-empty result for channel")
	}
}

func TestStoreStepOutput(t *testing.T) {
	state := &ExecutionState{}

	StoreStepOutput(state, "step1", "raw output", map[string]interface{}{"key": "value"})

	if state.Variables["steps.step1.output"] != "raw output" {
		t.Errorf("output not stored correctly")
	}

	if state.Variables["steps.step1.data"] == nil {
		t.Errorf("parsed data not stored")
	}
}

func TestParseDefault(t *testing.T) {
	tests := []struct {
		expr       string
		wantPath   string
		wantDef    string
		wantHasDef bool
	}{
		{"vars.name", "vars.name", "", false},
		{"vars.x | \"default\"", "vars.x", "default", true},
		{"vars.x | 'single'", "vars.x", "single", true},
		{"vars.x | bare", "vars.x", "bare", true},
		{"vars.x|compact", "vars.x", "compact", true},
		{"vars.x  |  spaced  ", "vars.x", "spaced", true},
	}

	for _, tt := range tests {
		path, def, hasDef := parseDefault(tt.expr)
		if path != tt.wantPath {
			t.Errorf("parseDefault(%q) path = %q, want %q", tt.expr, path, tt.wantPath)
		}
		if def != tt.wantDef {
			t.Errorf("parseDefault(%q) default = %q, want %q", tt.expr, def, tt.wantDef)
		}
		if hasDef != tt.wantHasDef {
			t.Errorf("parseDefault(%q) hasDefault = %v, want %v", tt.expr, hasDef, tt.wantHasDef)
		}
	}
}

func TestExtractJSONBlock(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"key": "value"}`, `{"key": "value"}`},
		{`{"key": "value"} extra`, `{"key": "value"}`},
		{`[1, 2, 3]`, `[1, 2, 3]`},
		{`{"nested": {"a": 1}}`, `{"nested": {"a": 1}}`},
		{`{"quoted": "with } brace"}`, `{"quoted": "with } brace"}`},
		{`not json`, `not json`},
	}

	for _, tt := range tests {
		got := extractJSONBlock(tt.input)
		if got != tt.want {
			t.Errorf("extractJSONBlock(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveSteps_AllFields(t *testing.T) {

	now := time.Now()
	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"build": {
				StepID:     "build",
				Status:     StatusCompleted,
				Output:     "build ok",
				PaneUsed:   "%5",
				AgentType:  "cc",
				StartedAt:  now.Add(-30 * time.Second),
				FinishedAt: now,
				ParsedData: map[string]interface{}{
					"artifact": "output.zip",
					"nested":   map[string]interface{}{"key": "val"},
				},
			},
			"noparsed": {
				StepID:     "noparsed",
				Status:     StatusCompleted,
				Output:     "raw",
				StartedAt:  now,
				FinishedAt: time.Time{}, // zero
			},
		},
	}

	sub := NewSubstitutor(state, "sess", "wf")

	tests := []struct {
		name     string
		template string
		want     string
		wantErr  bool
	}{
		{"step duration", "${steps.build.duration}", "", false}, // non-empty duration
		{"step pane", "${steps.build.pane}", "%5", false},
		{"step agent", "${steps.build.agent}", "cc", false},
		{"step status", "${steps.build.status}", "completed", false},
		{"step output", "${steps.build.output}", "build ok", false},
		{"step data field", "${steps.build.data.artifact}", "output.zip", false},
		{"step data nested", "${steps.build.data.nested.key}", "val", false},
		{"step output parsed field", "${steps.build.output.artifact}", "output.zip", false},
		{"step no parsed data", "${steps.noparsed.data}", "", true},
		{"step zero duration", "${steps.noparsed.duration}", "0s", false},
		{"step unknown field", "${steps.build.unknown}", "", true},
		{"step not found", "${steps.missing.output}", "", true},
		{"steps too few parts", "${steps.build}", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sub.Substitute(tt.template)
			if tt.wantErr {
				if err == nil {
					// For wantErr, the template stays unsubstituted (no error for first err on same call)
					// Check that the raw template is unchanged or err is set
				}
				return
			}
			if err != nil {
				t.Errorf("Substitute(%q) error = %v", tt.template, err)
				return
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("Substitute(%q) = %q, want %q", tt.template, got, tt.want)
			}
			// For duration, just check it's non-empty and not the template
			if tt.name == "step duration" && (got == "" || got == tt.template) {
				t.Errorf("duration should be resolved, got %q", got)
			}
		})
	}
}

func TestResolveSteps_FlatKeyLookup(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"steps.legacy.output": "flat value",
		},
		Steps: map[string]StepResult{},
	}

	sub := NewSubstitutor(state, "sess", "wf")
	got, err := sub.Substitute("${steps.legacy.output}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "flat value" {
		t.Errorf("Substitute() = %q, want %q (flat key lookup)", got, "flat value")
	}
}

func TestResolveLoop_Nested(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"loop.item": map[string]interface{}{
				"name": "test.txt",
				"size": float64(100),
			},
		},
	}

	sub := NewSubstitutor(state, "sess", "wf")
	got, err := sub.Substitute("Name: ${loop.item.name}")
	if err != nil {
		t.Fatalf("Substitute() error = %v", err)
	}
	if got != "Name: test.txt" {
		t.Errorf("Substitute() = %q, want %q", got, "Name: test.txt")
	}
}

func TestResolveVar_UnknownNamespace(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.Substitute("${badns.var}")
	if err == nil {
		t.Error("expected error for unknown namespace")
	}
}

func TestResolveVar_EmptyReference(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "sess", "wf")
	// ${} has an empty expression, which after split becomes [""] - parts[0]=""
	// The varPattern matches ${} and resolveVar returns error for empty reference
	got, err := sub.Substitute("test: ${}")
	// Substitute returns the original if substitution fails
	// There should be an error AND the raw template may remain
	if err == nil && !strings.Contains(got, "${}") {
		t.Error("expected either error or unsubstituted template for empty reference")
	}
}

func TestResolveVars_NilState(t *testing.T) {

	sub := NewSubstitutor(nil, "sess", "wf")
	got, _ := sub.Substitute("Run: ${run_id}")
	if got != "Run: " {
		t.Errorf("run_id with nil state = %q, want 'Run: '", got)
	}
}

func TestResolveVars_NilVariables(t *testing.T) {

	state := &ExecutionState{
		Variables: nil,
	}
	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.Substitute("${vars.x}")
	if err == nil {
		t.Error("expected error for nil variables")
	}
}

func TestResolveLoop_NoParts(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}
	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.SubstituteStrict("${loop}")
	if err == nil {
		t.Error("expected error for loop with no field name")
	}
}

func TestResolveVars_RequiresName(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}
	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.SubstituteStrict("${vars}")
	if err == nil {
		t.Error("expected error for vars with no variable name")
	}
}

func TestResolveEnv_NoParts(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}
	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.SubstituteStrict("${env}")
	if err == nil {
		t.Error("expected error for env with no variable name")
	}
}

func TestSubstituteStrict_RemainingUnsubstituted(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	// vars.x is undefined and has no default - Substitute returns the raw ${vars.x} with an error,
	// but SubstituteStrict should catch remaining unsubstituted vars
	_, err := sub.SubstituteStrict("${vars.undefined}")
	if err == nil {
		t.Error("SubstituteStrict should error for undefined vars")
	}
}

func TestSubstituteStrict_SubstituteError(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	// A bad namespace causes Substitute() to return an error (not just leave var unsubstituted)
	_, err := sub.SubstituteStrict("${badnamespace.var}")
	if err == nil {
		t.Error("SubstituteStrict should return error from Substitute")
	}
}

func TestSubstituteStrict_ValueContainsUnresolvedRef(t *testing.T) {

	// A variable whose value contains an unresolved variable reference
	// This tests the path where Substitute succeeds but leaves ${...} in result
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"foo": "${unresolved.ref}", // Value contains unresolved ref
		},
	}
	sub := NewSubstitutor(state, "sess", "wf")

	// Substitute will succeed (replacing ${vars.foo} with "${unresolved.ref}")
	// But SubstituteStrict should detect the remaining ${...} in the result
	_, err := sub.SubstituteStrict("Value: ${vars.foo}")
	if err == nil {
		t.Error("SubstituteStrict should error when result contains unresolved references")
	}
}

func TestParseYAML_InvalidSyntax(t *testing.T) {

	parser := NewOutputParser()
	_, err := parser.Parse("invalid: yaml: [[[", OutputParse{Type: "yaml"})
	if err == nil {
		t.Error("parseYAML should error on invalid YAML syntax")
	}
}

func TestOutputParser_ParseUnknownType(t *testing.T) {

	parser := NewOutputParser()
	_, err := parser.Parse("output", OutputParse{Type: "unknown_type"})
	if err == nil {
		t.Error("expected error for unknown parse type")
	}
}

func TestOutputParser_ParseNone(t *testing.T) {

	parser := NewOutputParser()
	got, err := parser.Parse("  hello  ", OutputParse{Type: ""})
	if err != nil {
		t.Fatalf("Parse(none) error = %v", err)
	}
	if got != "hello" {
		t.Errorf("Parse(none) = %q, want %q", got, "hello")
	}
}

func TestOutputParser_ParseJSON_NoJSON(t *testing.T) {

	parser := NewOutputParser()
	_, err := parser.Parse("no json here", OutputParse{Type: "json"})
	if err == nil {
		t.Error("expected error for no JSON in output")
	}
}

func TestOutputParser_ParseRegex_EmptyPattern(t *testing.T) {

	parser := NewOutputParser()
	_, err := parser.Parse("output", OutputParse{Type: "regex", Pattern: ""})
	if err == nil {
		t.Error("expected error for empty regex pattern")
	}
}

func TestOutputParser_ParseRegex_InvalidPattern(t *testing.T) {

	parser := NewOutputParser()
	_, err := parser.Parse("output", OutputParse{Type: "regex", Pattern: "[invalid"})
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestOutputParser_ParseRegex_FullMatchNoGroups(t *testing.T) {

	parser := NewOutputParser()
	got, err := parser.Parse("abc 123 def", OutputParse{Type: "regex", Pattern: `\d+`})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got != "123" {
		t.Errorf("Parse() = %v, want %q (full match, no groups)", got, "123")
	}
}

// ClearLoopVars is a test-only helper retained after the production function
// was removed as dead code; loop-variable tests use it to reset loop context
// between assertions.
func ClearLoopVars(state *ExecutionState, varName string) {
	if state.Variables == nil {
		return
	}

	delete(state.Variables, "loop."+varName)
	delete(state.Variables, "loop.item")
	delete(state.Variables, "loop.index")
	delete(state.Variables, "loop.count")
	delete(state.Variables, "loop.first")
	delete(state.Variables, "loop.last")
}

func TestStoreStepOutput_NoParsedData(t *testing.T) {

	state := &ExecutionState{}
	StoreStepOutput(state, "s1", "raw output", nil)

	if state.Variables["steps.s1.output"] != "raw output" {
		t.Error("output not stored")
	}
	if _, exists := state.Variables["steps.s1.data"]; exists {
		t.Error("data should not be stored when parsedData is nil")
	}
}

func TestSubstitutionError_Error(t *testing.T) {

	tests := []struct {
		name    string
		err     SubstitutionError
		wantMsg string
	}{
		{
			name: "simple variable",
			err: SubstitutionError{
				VarRef:  "myvar",
				Message: "variable not defined",
			},
			wantMsg: "variable substitution error for 'myvar': variable not defined",
		},
		{
			name: "nested variable",
			err: SubstitutionError{
				VarRef:  "steps.step1.output",
				Message: "step not executed",
			},
			wantMsg: "variable substitution error for 'steps.step1.output': step not executed",
		},
		{
			name: "env variable",
			err: SubstitutionError{
				VarRef:  "env.MY_VAR",
				Message: "environment variable not set",
			},
			wantMsg: "variable substitution error for 'env.MY_VAR': environment variable not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.wantMsg {
				t.Errorf("SubstitutionError.Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestExtractJSONBlock_EdgeCases(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"escaped quote in string", `{"key": "value with \" quote"}`, `{"key": "value with \" quote"}`},
		{"unclosed JSON object", `{"key": "value"`, `{"key": "value"`},
		{"unclosed JSON array", `[1, 2, 3`, `[1, 2, 3`},
		{"nested arrays", `[[1, 2], [3, 4]]`, `[[1, 2], [3, 4]]`},
		{"mixed nested", `{"arr": [1, 2], "obj": {"a": 1}}`, `{"arr": [1, 2], "obj": {"a": 1}}`},
		{"backslash not in string", `{"key":1}\ extra`, `{"key":1}`},
		{"just opening brace no close", `{`, `{`},
		{"just opening bracket no close", `[`, `[`},
		{"starts with space", ` {"key": 1}`, ` {"key": 1}`}, // doesn't start with { or [
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONBlock(tt.input)
			if got != tt.want {
				t.Errorf("extractJSONBlock(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveSteps_Agent(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"build": {
				StepID:    "build",
				Status:    StatusCompleted,
				AgentType: "cc",
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Test agent field resolution
	result, err := sub.Substitute("${steps.build.agent}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	if result != "cc" {
		t.Errorf("expected 'cc', got %q", result)
	}
}

func TestSetLoopVars_NilVariables(t *testing.T) {

	// State with nil Variables map
	state := &ExecutionState{
		Variables: nil,
	}

	// SetLoopVars should initialize the Variables map
	SetLoopVars(state, "item", "value", 0, 3)

	if state.Variables == nil {
		t.Fatal("Variables should be initialized")
	}
	if state.Variables["loop.item"] != "value" {
		t.Errorf("loop.item = %v, want 'value'", state.Variables["loop.item"])
	}
	if state.Variables["loop.first"] != true {
		t.Errorf("loop.first = %v, want true", state.Variables["loop.first"])
	}
	if state.Variables["loop.last"] != false {
		t.Errorf("loop.last = %v, want false", state.Variables["loop.last"])
	}
}

func TestStoreStepOutput_NilVariables(t *testing.T) {

	state := &ExecutionState{
		Variables: nil,
	}

	StoreStepOutput(state, "test-step", "output value", map[string]string{"key": "val"})

	if state.Variables == nil {
		t.Fatal("Variables should be initialized")
	}
	if state.Variables["steps.test-step.output"] != "output value" {
		t.Errorf("unexpected output value: %v", state.Variables["steps.test-step.output"])
	}
	if state.Variables["steps.test-step.data"] == nil {
		t.Error("steps.test-step.data should be set")
	}
}

func TestResolveVar_NestedLoop(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"loop.item":  "current_item",
			"loop.index": 5,
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	result, err := sub.Substitute("Item: ${loop.item}, Index: ${loop.index}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	if result != "Item: current_item, Index: 5" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestResolveSteps_OutputNestedField(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"api": {
				StepID: "api",
				Status: StatusCompleted,
				Output: `{"user": {"name": "Alice"}}`,
				ParsedData: map[string]interface{}{
					"user": map[string]interface{}{
						"name": "Alice",
					},
				},
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Test nested output field
	result, err := sub.Substitute("${steps.api.output.user.name}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	if result != "Alice" {
		t.Errorf("expected 'Alice', got %q", result)
	}
}

func TestResolveSteps_OutputBracketAccessOnDeeperSegment(t *testing.T) {
	// bd-et0k5: bracket suffixes must work on every path segment, not just
	// the first field. The documented shapes are
	// ${steps.fetch.output.items[0].title} and
	// ${steps.fetch.data[user][profile][name]}.
	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"fetch": {
				StepID: "fetch",
				Status: StatusCompleted,
				ParsedData: map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"title": "ok"},
						map[string]interface{}{"title": "second"},
					},
					"user": map[string]interface{}{
						"profile": map[string]interface{}{
							"name": "Alice",
						},
					},
					"list": []interface{}{"a", "b", "c", "d"},
				},
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	cases := []struct {
		template string
		want     string
	}{
		{"${steps.fetch.output.items[0].title}", "ok"},
		{"${steps.fetch.output.items[1].title}", "second"},
		{"${steps.fetch.data[user][profile][name]}", "Alice"},
		{"${steps.fetch.parsed_data.list[3]}", "d"},
	}
	for _, tc := range cases {
		got, err := sub.Substitute(tc.template)
		if err != nil {
			t.Errorf("Substitute(%q) error = %v", tc.template, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Substitute(%q) = %q, want %q", tc.template, got, tc.want)
		}
	}
}

func TestResolveSteps_OutputNestedNoParsedData(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"raw": {
				StepID:     "raw",
				Status:     StatusCompleted,
				Output:     "raw output",
				ParsedData: nil,
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Accessing nested field when there's no parsed data should fail
	_, err := sub.Substitute("${steps.raw.output.field}")
	if err == nil {
		t.Error("expected error for nested access without parsed data")
	}
}

func TestResolveSteps_DataNoParsedData(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"step": {
				StepID:     "step",
				Status:     StatusCompleted,
				Output:     "output",
				ParsedData: nil,
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Accessing data when there's no parsed data should fail
	_, err := sub.Substitute("${steps.step.data}")
	if err == nil {
		t.Error("expected error for data access without parsed data")
	}
}

func TestResolveSteps_DataNestedField(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"api": {
				StepID: "api",
				Status: StatusCompleted,
				ParsedData: map[string]interface{}{
					"config": map[string]interface{}{
						"timeout": 30,
					},
				},
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	result, err := sub.Substitute("${steps.api.data.config.timeout}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	if result != "30" {
		t.Errorf("expected '30', got %q", result)
	}
}

func TestResolveSteps_DataWithoutNesting(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"api": {
				StepID:     "api",
				Status:     StatusCompleted,
				ParsedData: map[string]interface{}{"result": "value", "count": 10},
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Access data without nested field - should return the full ParsedData
	result, err := sub.Substitute("${steps.api.data}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	// The result should be a string representation of the map
	if result == "" {
		t.Error("expected non-empty result for data access")
	}
}

func TestResolveSteps_NilState(t *testing.T) {

	sub := NewSubstitutor(nil, "test-session", "test-workflow")

	_, err := sub.Substitute("${steps.step1.output}")
	if err == nil {
		t.Error("expected error when state is nil")
	}
	if !strings.Contains(err.Error(), "no execution state") {
		t.Errorf("expected 'no execution state' error, got: %v", err)
	}
}

func TestResolveSteps_DurationZero(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"pending": {
				StepID: "pending",
				Status: StatusPending,
				// FinishedAt is zero
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	result, err := sub.Substitute("${steps.pending.duration}")
	if err != nil {
		t.Fatalf("Substitute error: %v", err)
	}
	if result != "0s" {
		t.Errorf("expected '0s', got %q", result)
	}
}

func TestResolveSteps_UnknownField(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"step1": {
				StepID: "step1",
				Status: StatusCompleted,
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	_, err := sub.Substitute("${steps.step1.unknownfield}")
	if err == nil {
		t.Error("expected error for unknown step field")
	}
}

func TestResolveSteps_MissingStepID(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps: map[string]StepResult{
			"step1": {StepID: "step1"},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Only "steps" without step ID and field - should error
	_, err := sub.Substitute("${steps}")
	if err == nil {
		t.Error("expected error for steps with no step ID")
	}
}

func TestResolveSteps_NilStepsMap(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
		Steps:     nil, // nil Steps map
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Step not in variables, and Steps map is nil
	_, err := sub.Substitute("${steps.missing.output}")
	if err == nil {
		t.Error("expected error for nil Steps map")
	}
}

func TestResolveVar_EmptyPath(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Empty variable reference should error
	_, err := sub.Substitute("${}") // Empty reference
	// This might succeed or fail depending on regex
	_ = err // Just testing the path

	// Test with just whitespace
	_, err = sub.Substitute("${  }")
	_ = err // Just testing the path
}

func TestOutputParser_ParseJSON_ExtractJSONBlock(t *testing.T) {

	parser := NewOutputParser()

	// Test JSON with trailing garbage - first unmarshal fails, extractJSONBlock succeeds
	output := `{"name": "test"}extra garbage here`
	result, err := parser.Parse(output, OutputParse{Type: "json"})
	if err != nil {
		t.Errorf("Parse should succeed after extractJSONBlock: %v", err)
	}
	if m, ok := result.(map[string]interface{}); ok {
		if m["name"] != "test" {
			t.Errorf("got name=%v, want 'test'", m["name"])
		}
	} else {
		t.Errorf("expected map result, got %T", result)
	}

	// Test array JSON with trailing garbage
	output2 := `[1, 2, 3]and some text after`
	result2, err := parser.Parse(output2, OutputParse{Type: "json"})
	if err != nil {
		t.Errorf("Parse should succeed for array with trailing garbage: %v", err)
	}
	if arr, ok := result2.([]interface{}); ok {
		if len(arr) != 3 {
			t.Errorf("got array length %d, want 3", len(arr))
		}
	} else {
		t.Errorf("expected array result, got %T", result2)
	}
}

func TestOutputParser_ParseJSON_BothUnmarshalsFail(t *testing.T) {

	parser := NewOutputParser()

	// Test invalid JSON with trailing garbage - both unmarshals fail
	// The first unmarshal fails, extractJSONBlock extracts the invalid JSON,
	// and the second unmarshal also fails
	output := `{bad json syntax}trailing stuff`
	_, err := parser.Parse(output, OutputParse{Type: "json"})
	if err == nil {
		t.Error("expected error when both unmarshals fail")
	}
	if !strings.Contains(err.Error(), "failed to parse JSON") {
		t.Errorf("expected 'failed to parse JSON' error, got: %v", err)
	}
}

func TestResolveLoop_NilVariables(t *testing.T) {

	state := &ExecutionState{
		Variables: nil, // explicitly nil
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	_, err := sub.Substitute("${loop.item}")
	if err == nil {
		t.Error("expected error when state.Variables is nil")
	}
	if !strings.Contains(err.Error(), "no loop context") {
		t.Errorf("expected 'no loop context' error, got: %v", err)
	}
}

func TestResolveSteps_FlatKeyNestedNavigation(t *testing.T) {

	// Test flat key lookup with nested navigation (parts > 2)
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"steps.step1.output": map[string]interface{}{
				"nested": map[string]interface{}{
					"value": "deep_value",
				},
			},
		},
		Steps: nil, // Force use of flat key lookup
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// This should use flat key lookup and then navigate nested
	result, err := sub.Substitute("${steps.step1.output.nested.value}")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "deep_value" {
		t.Errorf("got %q, want 'deep_value'", result)
	}
}

func TestResolveSteps_FlatKeySimple(t *testing.T) {

	// Test flat key lookup without nested navigation
	state := &ExecutionState{
		Variables: map[string]interface{}{
			"steps.step1.output": "simple_output",
		},
		Steps: nil, // Force use of flat key lookup
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")
	result, err := sub.Substitute("${steps.step1.output}")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "simple_output" {
		t.Errorf("got %q, want 'simple_output'", result)
	}
}

func TestResolveLoop_NestedAccess(t *testing.T) {

	state := &ExecutionState{
		Variables: map[string]interface{}{
			"loop.item": map[string]interface{}{
				"name":  "test_item",
				"value": 42,
			},
		},
	}

	sub := NewSubstitutor(state, "test-session", "test-workflow")

	// Test nested access with more than one part
	result, err := sub.Substitute("${loop.item.name}")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "test_item" {
		t.Errorf("got %q, want 'test_item'", result)
	}
}

// ---------------------------------------------------------------------------
// ${defaults.X} resolution tests (bd-6lkqr.2)
// ---------------------------------------------------------------------------

func TestSubstitute_DefaultsFlatString(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"model_mix": "cc:3,cod:1,gmi:1",
	})

	result, err := sub.Substitute("mix=${defaults.model_mix}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "mix=cc:3,cod:1,gmi:1" {
		t.Errorf("got %q, want %q", result, "mix=cc:3,cod:1,gmi:1")
	}
}

func TestSubstitute_DefaultsDottedPath(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"hard_caps": map[string]interface{}{
			"phase_4_max_rounds": 6,
		},
	})

	result, err := sub.Substitute("max=${defaults.hard_caps.phase_4_max_rounds}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "max=6" {
		t.Errorf("got %q, want %q", result, "max=6")
	}
}

func TestSubstitute_DefaultsDeepNested(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"a": map[string]interface{}{
			"b": map[string]interface{}{
				"c": "deep",
			},
		},
	})

	result, err := sub.Substitute("${defaults.a.b.c}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "deep" {
		t.Errorf("got %q, want %q", result, "deep")
	}
}

func TestSubstitute_DefaultsUnknownPathError(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"known": "yes",
	})

	_, err := sub.Substitute("${defaults.unknown}")
	if err == nil {
		t.Fatal("expected error for unknown default key")
	}
	subErr, ok := err.(*SubstitutionError)
	if !ok {
		t.Fatalf("expected SubstitutionError, got %T", err)
	}
	if subErr.VarRef != "defaults.unknown" {
		t.Errorf("VarRef=%q, want %q", subErr.VarRef, "defaults.unknown")
	}
}

func TestSubstitute_DefaultsNilMapError(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")

	_, err := sub.Substitute("${defaults.anything}")
	if err == nil {
		t.Fatal("expected error when defaults is nil")
	}
}

func TestSubstitute_DefaultsIntValue(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"retries": 3,
	})

	result, err := sub.Substitute("retries=${defaults.retries}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "retries=3" {
		t.Errorf("got %q, want %q", result, "retries=3")
	}
}

func TestSubstitute_DefaultsFloatValue(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"threshold": 0.75,
	})

	result, err := sub.Substitute("t=${defaults.threshold}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "t=0.75" {
		t.Errorf("got %q, want %q", result, "t=0.75")
	}
}

func TestSubstitute_DefaultsBoolValue(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{
		"verbose": true,
	})

	result, err := sub.Substitute("verbose=${defaults.verbose}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "verbose=true" {
		t.Errorf("got %q, want %q", result, "verbose=true")
	}
}

func TestSubstitute_DefaultsWithDefault(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{})

	result, err := sub.Substitute(`${defaults.missing | "fallback"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "fallback" {
		t.Errorf("got %q, want %q", result, "fallback")
	}
}

func TestSubstitute_DefaultsNoKeyError(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")
	sub.SetDefaults(map[string]interface{}{"x": "y"})

	_, err := sub.Substitute("${defaults}")
	if err == nil {
		t.Fatal("expected error for bare defaults reference")
	}
}

// ---------------------------------------------------------------------------
// ${item} / ${item.X} loop-local binding tests (bd-6lkqr.3)
// ---------------------------------------------------------------------------

func TestSubstitute_ItemScalarInForeach(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	items := []interface{}{1, 2, 3}

	for i, item := range items {
		SetLoopVars(state, "item", item, i, len(items))
		sub := NewSubstitutor(state, "sess", "wf")
		result, err := sub.Substitute("val=${item}")
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		want := fmt.Sprintf("val=%d", item)
		if result != want {
			t.Errorf("iteration %d: got %q, want %q", i, result, want)
		}
	}
	ClearLoopVars(state, "item")
}

func TestSubstitute_ItemStringScalar(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	SetLoopVars(state, "item", "hello", 0, 1)
	sub := NewSubstitutor(state, "sess", "wf")

	result, err := sub.Substitute("${item}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestSubstitute_ItemMapFields(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	record := map[string]interface{}{
		"id":    "bd-123",
		"title": "Fix the thing",
		"priority": map[string]interface{}{
			"level": 1,
		},
	}
	SetLoopVars(state, "item", record, 0, 1)
	sub := NewSubstitutor(state, "sess", "wf")

	result, err := sub.Substitute("${item.id}: ${item.title} (P${item.priority.level})")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "bd-123: Fix the thing (P1)"
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestSubstitute_ItemOutsideForeachError(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")

	_, err := sub.Substitute("${item}")
	if err == nil {
		t.Fatal("expected error for item reference outside foreach")
	}
	subErr, ok := err.(*SubstitutionError)
	if !ok {
		t.Fatalf("expected SubstitutionError, got %T", err)
	}
	if subErr.VarRef != "item" {
		t.Errorf("VarRef=%q, want %q", subErr.VarRef, "item")
	}
}

func TestSubstitute_ItemFieldOutsideForeachError(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")

	_, err := sub.Substitute("${item.id}")
	if err == nil {
		t.Fatal("expected error for item.id reference outside foreach")
	}
}

func TestSubstitute_ItemNilStateError(t *testing.T) {
	sub := NewSubstitutor(nil, "sess", "wf")

	_, err := sub.Substitute("${item}")
	if err == nil {
		t.Fatal("expected error for item reference with nil state")
	}
}

func TestSubstitute_ItemClearedAfterLoop(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	SetLoopVars(state, "item", "value", 0, 1)
	ClearLoopVars(state, "item")

	sub := NewSubstitutor(state, "sess", "wf")
	_, err := sub.Substitute("${item}")
	if err == nil {
		t.Fatal("expected error after loop vars cleared")
	}
}

func TestSubstitute_ItemWithAsAlias(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	SetLoopVars(state, "file", "/tmp/test.txt", 0, 1)
	sub := NewSubstitutor(state, "sess", "wf")

	result, err := sub.Substitute("${item}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "/tmp/test.txt" {
		t.Errorf("got %q, want %q", result, "/tmp/test.txt")
	}
}

func TestSubstitute_ItemWithDefaultFallback(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	sub := NewSubstitutor(state, "sess", "wf")

	result, err := sub.Substitute(`${item | "none"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "none" {
		t.Errorf("got %q, want %q", result, "none")
	}
}

// TestNormalizeStringVar covers bd-q84t9: declared `type: string` must
// reject non-string values from --var-file (which can be native JSON
// booleans, numbers, arrays, or objects). Plain --var input is always a
// string and passes through.
func TestNormalizeStringVar(t *testing.T) {
	t.Run("string passes", func(t *testing.T) {
		got, err := normalizeStringVar("name", "hello")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "hello" {
			t.Errorf("got %v, want hello", got)
		}
	})

	rejects := []struct {
		name  string
		value interface{}
		want  string
	}{
		{"bool", true, "expected string, got boolean true"},
		{"number_int", json.Number("42"), "expected string, got number 42"},
		{"number_float", json.Number("3.14"), "expected string, got number 3.14"},
		{"array", []interface{}{"a", "b"}, "expected string, got array of length 2"},
		{"object", map[string]interface{}{"k": "v"}, "expected string, got object"},
		{"nil", nil, "expected string, got null"},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeStringVar("name", tc.value)
			if err == nil {
				t.Fatalf("normalizeStringVar(%v) err = nil, want %q", tc.value, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestValidateWorkflowVariables_RejectsNonStringForDeclaredString is a
// higher-level bd-q84t9 regression: a workflow declaring vars.foo type
// "string" must reject a JSON number override from a --var-file.
func TestValidateWorkflowVariables_RejectsNonStringForDeclaredString(t *testing.T) {
	wf := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "string-var-typed",
		Vars: map[string]VarDef{
			"label": {Type: VarTypeString, Default: "foo"},
		},
	}
	overrides := map[string]interface{}{"label": json.Number("42")}
	_, err := ValidateWorkflowVariables(wf, overrides)
	if err == nil {
		t.Fatal("ValidateWorkflowVariables() err = nil, want type-mismatch error")
	}
	if !strings.Contains(err.Message, "expected string") {
		t.Errorf("err = %q, want type-mismatch", err.Message)
	}
}
