package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// This intentionally uses the actual workflow schema's shorthand decoders,
// normalization, and strict YAML parser, not a second snapshot-only parser.
func TestWorkflowSnapshotPreservesStructuredSchema(t *testing.T) {
	var workflow Workflow
	err := json.Unmarshal([]byte(`{
		"schema_version":"2.0",
		"name":"structured-snapshot",
		"notes":["saved definition", "resumable"],
		"vars":{"env":{"type":"string","default":"test"}},
		"settings":{"timeout":"30s"},
		"steps":[
			{"id":"first","command":"echo ${vars.env}","timeout":"2s"},
			{"id":"fanout","depends_on":["first"],"parallel":{"steps":[
				{"id":"left","command":"true"},
				{"id":"right","command":"true"}
			]}}
		]
	}`), &workflow)
	if err != nil {
		t.Fatal(err)
	}
	frozen, path, err := SnapshotWorkflow(context.Background(), t.TempDir(), &workflow)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, validation, err := LoadResumeWorkflow(path)
	if err != nil || !validation.Valid {
		t.Fatalf("structured snapshot is not resumable: %+v %v", validation, err)
	}
	if !reflect.DeepEqual(frozen, reloaded) {
		t.Fatal("resume changed normalized variables, durations, parallel steps, or dependencies")
	}
	if len(reloaded.Steps) != 2 || len(reloaded.Steps[1].Parallel.Steps) != 2 || reloaded.Vars["env"].Default != "test" {
		t.Fatalf("snapshot dropped structured workflow fields: %+v", reloaded)
	}
}

func TestWorkflowSnapshotUsesYAMLSchemaMarshalers(t *testing.T) {
	// Encoding this struct as JSON and reading it as YAML is NOT a round trip:
	// PaneSpec and IntOrExpr JSON encode as objects, whereas their YAML
	// decoders accept only scalars. ParallelSpec has the same mismatch.
	workflow := &Workflow{
		SchemaVersion: "2.0", Name: "schema-marshalers",
		Steps: []Step{{
			ID: "prompt", Prompt: "review this project",
			Pane: PaneSpec{Index: 1}, OnFailure: OnFailureSpec{Action: "retry", RetryCount: 2},
		}},
	}
	_, path, err := SnapshotWorkflow(context.Background(), t.TempDir(), workflow)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, validation, err := LoadAndValidate(path)
	if err != nil || !validation.Valid {
		t.Fatalf("native YAML snapshot rejected: %+v %v", validation, err)
	}
	if reloaded.Steps[0].Pane.Index != 1 || reloaded.Steps[0].OnFailure.Action != "retry" || reloaded.Steps[0].OnFailure.RetryCount != 2 {
		t.Fatalf("schema marshalers lost selector or retry policy: %+v", reloaded.Steps[0])
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "pane: 1") {
		t.Fatalf("snapshot is not using native YAML field representations: %s %v", data, err)
	}
}
