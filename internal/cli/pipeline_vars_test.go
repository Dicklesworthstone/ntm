package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func TestParsePipelineRunVariablesPrecedenceAndJSONTypes(t *testing.T) {
	varFile := filepath.Join(t.TempDir(), "vars.json")
	if err := os.WriteFile(varFile, []byte(`{"foo":"file","n":5,"arr":["x","y"],"flag":false}`), 0o644); err != nil {
		t.Fatalf("write var file: %v", err)
	}

	got, err := parsePipelineRunVariables(varFile, []string{
		"foo=cli",
		"flag=true",
		"items=a,b,c",
	})
	if err != nil {
		t.Fatalf("parsePipelineRunVariables() error = %v", err)
	}

	if got["foo"] != "cli" {
		t.Fatalf("foo = %#v, want CLI override", got["foo"])
	}
	if got["flag"] != "true" {
		t.Fatalf("flag = %#v, want CLI string override", got["flag"])
	}
	if got["n"] != float64(5) {
		t.Fatalf("n = %#v, want JSON float64", got["n"])
	}
	if !reflect.DeepEqual(got["arr"], []interface{}{"x", "y"}) {
		t.Fatalf("arr = %#v, want JSON array", got["arr"])
	}
	if got["items"] != "a,b,c" {
		t.Fatalf("items = %#v, want raw CLI comma string", got["items"])
	}
}

func TestParsePipelineRunVariablesErrors(t *testing.T) {
	if _, err := parsePipelineRunVariables("", []string{"missing_equals"}); err == nil {
		t.Fatal("parsePipelineRunVariables() error = nil, want invalid format error")
	}

	badJSON := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badJSON, []byte(`{"unterminated"`), 0o644); err != nil {
		t.Fatalf("write bad var file: %v", err)
	}
	_, err := parsePipelineRunVariables(badJSON, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to parse var file") {
		t.Fatalf("parsePipelineRunVariables() error = %v, want parse var file error", err)
	}
}

func TestParsePipelineRunVariablesRejectsNullVarFile(t *testing.T) {
	// A var file containing top-level "null" decodes into a nil map without
	// returning a JSON error, which would panic on subsequent --var writes.
	// Reject it with a user-facing validation error instead of crashing the
	// CLI / robot path.
	nullFile := filepath.Join(t.TempDir(), "null.json")
	if err := os.WriteFile(nullFile, []byte(`null`), 0o644); err != nil {
		t.Fatalf("write null var file: %v", err)
	}

	_, err := parsePipelineRunVariables(nullFile, []string{"foo=bar"})
	if err == nil {
		t.Fatal("parsePipelineRunVariables() error = nil, want null-var-file rejection")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Fatalf("error = %v, want it to mention the null shape", err)
	}
}

func TestParsePipelineRunVariablesRejectsNonObjectVarFile(t *testing.T) {
	// A non-object var file (array, number, string at top level) cannot be
	// merged with --var key=value flags. Surface a clear error instead of
	// allowing it through the legacy json.Unmarshal-into-map path.
	cases := []struct {
		name string
		body string
	}{
		{name: "array", body: `[1,2,3]`},
		{name: "number", body: `42`},
		{name: "string", body: `"hello"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vars.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write var file: %v", err)
			}
			_, err := parsePipelineRunVariables(path, nil)
			if err == nil {
				t.Fatal("parsePipelineRunVariables() error = nil, want non-object rejection")
			}
			if !strings.Contains(err.Error(), "JSON object") {
				t.Fatalf("error = %v, want it to mention the expected JSON object shape", err)
			}
		})
	}
}

func TestPipelineRunVariablesValidateDeclaredTypes(t *testing.T) {
	varFile := filepath.Join(t.TempDir(), "vars.json")
	if err := os.WriteFile(varFile, []byte(`{"n":5,"arr":["x","y"],"flag":false}`), 0o644); err != nil {
		t.Fatalf("write var file: %v", err)
	}

	vars, err := parsePipelineRunVariables(varFile, []string{
		"flag=true",
		"required=present",
		"extra=available",
	})
	if err != nil {
		t.Fatalf("parsePipelineRunVariables() error = %v", err)
	}

	workflow := &pipeline.Workflow{Vars: map[string]pipeline.VarDef{
		"n":        {Type: pipeline.VarTypeNumber},
		"arr":      {Type: pipeline.VarTypeArray},
		"flag":     {Type: pipeline.VarTypeBoolean},
		"required": {Type: pipeline.VarTypeString, Required: true},
	}}
	result, parseErr := pipeline.ValidateWorkflowVariables(workflow, vars)
	if parseErr != nil {
		t.Fatalf("ValidateWorkflowVariables() error = %v", parseErr)
	}
	if result.Variables["flag"] != true {
		t.Fatalf("flag = %#v, want normalized bool true", result.Variables["flag"])
	}
	if !reflect.DeepEqual(result.Variables["arr"], []interface{}{"x", "y"}) {
		t.Fatalf("arr = %#v, want JSON array", result.Variables["arr"])
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0].Message, "undeclared variable \"extra\"") {
		t.Fatalf("warnings = %#v, want undeclared extra warning", result.Warnings)
	}
}

func TestPipelineLintOutputsExecutionOrderAndParallelSubsteps(t *testing.T) {
	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	wfPath := filepath.Join(t.TempDir(), "workflow.yaml")
	wfContent := `schema_version: "2.0"
name: parallel-lint-check
steps:
  - id: design
    prompt: "Draft design"
    agent: claude
  - id: fanout
    depends_on: [design]
    parallel:
      - id: impl_a
        prompt: "Implement module A"
        agent: codex
      - id: impl_b
        prompt: "Implement module B"
        agent: codex
  - id: verify
    depends_on: [fanout]
    prompt: "Verify combined changes"
    agent: claude
`
	if err := os.WriteFile(wfPath, []byte(wfContent), 0o644); err != nil {
		t.Fatalf("write workflow yaml: %v", err)
	}

	var outBuf, errBuf bytes.Buffer
	if err := runPipelineLint(wfPath, &outBuf, &errBuf); err != nil {
		t.Fatalf("runPipelineLint error: %v (stderr=%s)", err, errBuf.String())
	}

	var got pipelineLintOutput
	if err := json.Unmarshal(outBuf.Bytes(), &got); err != nil {
		t.Fatalf("decode pipelineLintOutput: %v\nraw=%s", err, outBuf.String())
	}
	if !got.Success || !got.Valid {
		t.Fatalf("expected valid lint result, got %+v", got)
	}
	if got.ParallelSubsteps != 2 {
		t.Fatalf("ParallelSubsteps = %d, want 2", got.ParallelSubsteps)
	}
	wantOrder := []string{"design", "impl_a", "impl_b", "fanout", "verify"}
	if !reflect.DeepEqual(got.ExecutionOrder, wantOrder) {
		t.Fatalf("ExecutionOrder = %v, want %v", got.ExecutionOrder, wantOrder)
	}
	wantLevels := [][]string{{"design", "impl_a", "impl_b"}, {"fanout"}, {"verify"}}
	if !reflect.DeepEqual(got.ExecutionLevels, wantLevels) {
		t.Fatalf("ExecutionLevels = %v, want %v", got.ExecutionLevels, wantLevels)
	}
}
