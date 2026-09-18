package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowSnapshotRoundTripsAndVerifiesPromptFiles(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := map[bool]string{false: "direct", true: "background"}[background]
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "instructions.md")
			const original = "raw ${TASK} {{PARAM}}"
			writeSnapshotTemplate(t, input, original)
			workflow := promptFileWorkflow(input)
			var frozen *Workflow
			var locator string
			var err error
			if background {
				cfg := DefaultExecutorConfig("snapshot-prompt")
				cfg.ProjectDir = root
				frozen, locator, err = snapshotBackgroundWorkflow(context.Background(), root, workflow, cfg)
			} else {
				frozen, locator, err = SnapshotWorkflow(context.Background(), root, workflow)
			}
			if err != nil {
				t.Fatal(err)
			}
			if workflow.Steps[0].PromptFile != input || workflow.Steps[0].Template != "" {
				t.Fatal("snapshot mutated the caller's input definition")
			}
			if !managedTemplateSnapshot(frozen.Steps[0].PromptFile) || frozen.Steps[0].Template != "" {
				t.Fatal("YAML snapshot lost or retyped the frozen prompt_file")
			}
			writeSnapshotTemplate(t, input, "edited original")
			loaded, validation, err := LoadResumeWorkflow(locator)
			if err != nil || !validation.Valid {
				t.Fatalf("load frozen prompt_file workflow: %v %+v", err, validation)
			}
			path := loaded.Steps[0].PromptFile
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("recovery depends on edited input: %q %v", data, err)
			}
			writeSnapshotTemplate(t, path, "tampered managed input")
			if _, _, err := LoadResumeWorkflow(locator); err == nil {
				t.Fatal("native resume loader accepted tampered prompt_file bytes")
			}
		})
	}
}

func TestControlledForegroundFreezesPromptFiles(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "instructions.md")
	writeSnapshotTemplate(t, input, "original foreground input")
	workflow := promptFileWorkflow(input)
	workflow.Steps[0].When = "false" // No external agent is needed for this run.
	cfg := DefaultExecutorConfig("foreground-prompt")
	cfg.ProjectDir, cfg.RunID = root, "run-prompt-file"
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("foreground prompt_file snapshot: %+v %v", state, err)
	}
	writeSnapshotTemplate(t, input, "changed after execution")
	loaded, validation, err := LoadResumeWorkflow(state.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("foreground run has no verified prompt bundle: %v %+v", err, validation)
	}
	if !managedTemplateSnapshot(loaded.Steps[0].PromptFile) || loaded.Steps[0].Template != "" {
		t.Fatal("foreground state retained a mutable or retyped prompt input")
	}
	data, err := os.ReadFile(loaded.Steps[0].PromptFile)
	if err != nil || string(data) != "original foreground input" {
		t.Fatalf("foreground recovery input changed: %q %v", data, err)
	}
}
