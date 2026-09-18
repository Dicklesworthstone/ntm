package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These use the real YAML/schema/snapshot entry points in the repository.
func TestWorkflowSnapshotIncludesImmutableTemplateInputs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workflows", "flow.yaml")
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "Original {{TASK}} ${vars.input}")
	original := templateWorkflow()
	original.Steps[0].Params = map[string]interface{}{"TASK": "review"}
	frozen, path, err := SnapshotWorkflow(context.Background(), root, original, source)
	if err != nil {
		t.Fatal(err)
	}
	if original.Steps[0].Template != "prompt.md" {
		t.Fatal("snapshot mutated caller-owned workflow")
	}
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "Changed after dispatch")
	loaded, validation, err := LoadResumeWorkflow(path)
	if err != nil || !validation.Valid || !reflect.DeepEqual(frozen, loaded) {
		t.Fatalf("bundle did not round-trip through native schema: %v %+v", err, validation)
	}
	data, err := os.ReadFile(loaded.Steps[0].Template)
	if err != nil || string(data) != "Original {{TASK}} ${vars.input}" {
		t.Fatalf("resumed prompt changed: %q %v", data, err)
	}
	writeSnapshotTemplate(t, loaded.Steps[0].Template, "corrupt artifact")
	if _, _, err := LoadResumeWorkflow(path); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("resume accepted corrupt template artifact: %v", err)
	}
}

func TestWorkflowSnapshotRetainsBranchIdentityAndNativeSelectors(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "Review")
	workflow, err := ParseString(`schema_version: "2.0"
name: branch-template-snapshot
steps:
  - id: choose
    branch: left
    branches:
      left:
        pane: 2
        template: prompt.md
        on_failure: retry:1
      default:
        - id: named
          agent: claude
          template: prompt.md
`, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	frozen, path, err := SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	loaded, validation, err := LoadResumeWorkflow(path)
	if err != nil || !validation.Valid || !reflect.DeepEqual(frozen, loaded) {
		t.Fatalf("branch snapshot changed on resume: %v %+v", err, validation)
	}
	// The runtime chooses the scoped ID; snapshotting must not insert the
	// unexpanded author's parent ID into anonymous loop/foreach children.
	branch, err := parseBranchSteps(loaded.Steps[0].Branches["left"], "live-iteration-parent", "left")
	if err != nil || branch[0].ID != "live-iteration-parent.left.0" || branch[0].Pane.Index != 2 || branch[0].OnFailure.RetryCount != 1 {
		t.Fatalf("snapshot changed branch identity or schema values: %+v %v", branch, err)
	}
}

func TestBackgroundWorkflowSnapshotFreezesTemplateContents(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workflows", "flow.yaml")
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "worker input")
	cfg := DefaultExecutorConfig("frozen-worker")
	cfg.ProjectDir, cfg.WorkflowFile = root, source
	_, path, err := snapshotBackgroundWorkflow(context.Background(), root, templateWorkflow(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "changed before worker boot")
	loaded, validation, err := LoadResumeWorkflow(path)
	if err != nil || !validation.Valid {
		t.Fatalf("worker could not verify bundle: %v %+v", err, validation)
	}
	data, err := os.ReadFile(loaded.Steps[0].Template)
	if err != nil || string(data) != "worker input" {
		t.Fatalf("worker consumed mutable source: %q %v", data, err)
	}
}
