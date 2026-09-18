package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
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
