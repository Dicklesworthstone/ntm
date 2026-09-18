package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// These tests are deliberately not parallel: prompt_file has process-CWD
// semantics, so the working directory is part of the input under test.
func promptFileTestCWD(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Error(err)
		}
	})
}

func promptFileWorkflow(path string) *Workflow {
	return &Workflow{SchemaVersion: "2.0", Name: "frozen-prompt-file", Steps: []Step{
		{ID: "work", Agent: "claude", PromptFile: path},
	}}
}

func TestSnapshotPromptFilesPreserveCWDAndRawBytes(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	promptFileTestCWD(t, cwd)
	source := filepath.Join(root, "definitions", "flow.yaml")
	const original = "raw {{PARAM}} ${TASK}\n"
	writeSnapshotTemplate(t, filepath.Join(cwd, "prompt.md"), original)
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "wrong project fallback")
	writeSnapshotTemplate(t, filepath.Join(filepath.Dir(source), "prompt.md"), "wrong workflow fallback")
	workflow := promptFileWorkflow("prompt.md")
	workflow.Steps[0].Params = map[string]interface{}{"PARAM": "must not render"}
	if err := snapshotWorkflowTemplates(context.Background(), root, source, workflow); err != nil {
		t.Fatal(err)
	}
	step := workflow.Steps[0]
	if !managedTemplateSnapshot(step.PromptFile) || step.Template != "" || step.Prompt != "" {
		t.Fatalf("input was not frozen as a prompt_file: %+v", step)
	}
	writeSnapshotTemplate(t, filepath.Join(cwd, "prompt.md"), "edited after launch")
	data, err := readTemplateSnapshot(step.PromptFile, maxSnapshotTemplateBytes)
	if err != nil || string(data) != original {
		t.Fatalf("changed source resolution or rendered raw input: %q %v", data, err)
	}
	if err := verifyWorkflowTemplates(filepath.Join(filepath.Dir(filepath.Dir(step.PromptFile)), "flow.yaml"), workflow); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPromptFilesTraverseEveryStepScope(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "prompt.md")
	writeSnapshotTemplate(t, input, "shared raw input")
	prompt := func(id string) Step { return Step{ID: id, Agent: "claude", PromptFile: input} }
	workflow := promptFileWorkflow(input)
	workflow.Steps[0].OnSuccess = []Step{prompt("success")}
	workflow.Steps = append(workflow.Steps,
		Step{ID: "parallel", Parallel: ParallelSpec{Steps: []Step{prompt("parallel-child")}}},
		Step{ID: "loop", Loop: &LoopConfig{Steps: []Step{prompt("loop-child")}}},
		Step{ID: "foreach", Foreach: &ForeachConfig{Steps: []Step{prompt("foreach-child")}}},
		Step{ID: "panes", ForeachPane: &ForeachConfig{Steps: []Step{prompt("pane-child")}}},
		Step{ID: "branch", Branch: "printf yes", Branches: map[string]interface{}{
			"yes": []Step{prompt("named"), {Agent: "claude", PromptFile: input}},
			"no":  prompt("single"),
		}},
	)
	workflow.PostPipelineSteps = []Step{prompt("post")}
	workflow.Settings.OnCancel = []Step{prompt("cancel")}
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatal(err)
	}
	saved := workflow.Steps[0].PromptFile
	count := 0
	if err := visitWorkflowTemplates(workflow, false, func(step *Step) error {
		count++
		if step.PromptFile != saved || step.Template != "" || !managedTemplateSnapshot(saved) {
			t.Fatalf("unfrozen or retyped nested input at %q: %+v", step.ID, step)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("visited %d inputs, want 11", count)
	}
	branch, ok := workflow.Steps[5].Branches["yes"].([]Step)
	if !ok || len(branch) != 2 || branch[0].ID != "named" || branch[1].ID != "" {
		t.Fatalf("snapshot changed authored/anonymous branch IDs: %+v", branch)
	}
}

func TestSnapshotPromptFilesRejectDamagedManagedInputs(t *testing.T) {
	for _, damage := range []string{"corrupt", "missing", "symlink"} {
		t.Run(damage, func(t *testing.T) {
			if damage == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink privileges vary on Windows")
			}
			root := t.TempDir()
			input := filepath.Join(root, "source.md")
			writeSnapshotTemplate(t, input, "original")
			workflow := promptFileWorkflow(input)
			if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
				t.Fatal(err)
			}
			path := workflow.Steps[0].PromptFile
			if !managedTemplateSnapshot(path) {
				t.Fatal("prompt_file was not frozen")
			}
			if damage == "corrupt" {
				writeSnapshotTemplate(t, path, "damaged evidence")
			} else {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if damage == "symlink" {
					if err := os.Symlink(input, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			locator := filepath.Join(pipelineStateDir(root), workflowSnapshotDir, "flow.yaml")
			if err := verifyWorkflowTemplates(locator, workflow); err == nil {
				t.Fatal("recovery accepted a damaged managed prompt_file")
			}
			if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
				t.Fatal("damaged input was re-hashed or mistaken for a generated file")
			}
			if damage == "corrupt" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "damaged evidence" {
					t.Fatal("corrupt evidence was repaired or overwritten")
				}
			}
		})
	}
}

func TestSnapshotPromptFilesShareTemplateBudgetAndDeduplication(t *testing.T) {
	root := t.TempDir()
	input, second := filepath.Join(root, "first.md"), filepath.Join(root, "second.md")
	data := strings.Repeat("x", maxSnapshotTemplateBytes/2+1)
	writeSnapshotTemplate(t, input, data)
	workflow := promptFileWorkflow(input)
	workflow.Settings.Limits.MaxTemplateBytes = maxSnapshotTemplateBytes
	workflow.Steps = append(workflow.Steps, Step{ID: "template", Template: input})
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatalf("same canonical input counted twice: %v", err)
	}
	if workflow.Steps[0].PromptFile != workflow.Steps[1].Template {
		t.Fatal("prompt_file and template did not share their immutable artifact")
	}
	writeSnapshotTemplate(t, second, strings.Repeat("y", len(data)))
	other := promptFileWorkflow(input)
	other.Settings.Limits.MaxTemplateBytes = maxSnapshotTemplateBytes
	other.Steps = append(other.Steps, Step{ID: "second", Template: second})
	if err := snapshotWorkflowTemplates(context.Background(), root, "", other); err == nil {
		t.Fatal("mixed input kinds bypassed the aggregate byte limit")
	}
	// The resume verifier also shares one budget across both field kinds.
	path, err := publishTemplateSnapshot(filepath.Dir(workflow.Steps[0].PromptFile), []byte(strings.Repeat("y", len(data))))
	if err != nil {
		t.Fatal(err)
	}
	workflow.Steps[1].Template = path
	if err := verifyWorkflowTemplates(filepath.Join(pipelineStateDir(root), workflowSnapshotDir, "flow.yaml"), workflow); err == nil {
		t.Fatal("resume verification bypassed the combined input budget")
	}
}

func TestSnapshotPromptFilesPreserveGeneratedAndVariablePaths(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	promptFileTestCWD(t, cwd)
	workflow := promptFileWorkflow("generated.md")
	workflow.Steps = append(workflow.Steps, Step{ID: "dynamic", PromptFile: "${item.path}"})
	// A file with the literal placeholder's name must not freeze a foreach
	// input whose actual path is chosen only during materialization.
	writeSnapshotTemplate(t, filepath.Join(cwd, "${item.path}"), "not the runtime input")
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Steps[0].PromptFile != filepath.Join(cwd, "generated.md") || workflow.Steps[1].PromptFile != "${item.path}" {
		t.Fatalf("changed generated or variable path semantics: %+v", workflow.Steps)
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("live inputs created fake snapshot artifacts")
	}
}

func TestSnapshotPromptFilesEnforceCancellationAndRegularFileLimits(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "prompt.md")
	writeSnapshotTemplate(t, input, "12345")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshotWorkflowTemplates(ctx, root, "", promptFileWorkflow(input)); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	workflow := promptFileWorkflow(input)
	workflow.Settings.Limits.MaxTemplateBytes = 4
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
		t.Fatal("prompt_file bypassed the per-input limit")
	}
	if err := snapshotWorkflowTemplates(context.Background(), root, "", promptFileWorkflow(root)); err == nil {
		t.Fatal("directory accepted as prompt bytes")
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected inputs created artifacts")
	}
}

func TestSnapshotPromptFilesConcurrentPublication(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "prompt.md")
	writeSnapshotTemplate(t, input, "shared")
	const workers = 16
	var wg sync.WaitGroup
	paths := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workflow := promptFileWorkflow(input)
			if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
				t.Error(err)
				return
			}
			paths <- workflow.Steps[0].PromptFile
		}()
	}
	wg.Wait()
	close(paths)
	first, count := "", 0
	for path := range paths {
		count++
		if !managedTemplateSnapshot(path) || (first != "" && path != first) {
			t.Fatalf("concurrent runs did not share frozen bytes: %q %q", first, path)
		}
		first = path
	}
	if count != workers {
		t.Fatalf("only %d/%d publishers succeeded", count, workers)
	}
}

func TestSnapshotPromptFilesRejectForeignBundleOnResume(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	input := filepath.Join(other, "source.md")
	writeSnapshotTemplate(t, input, "foreign bundle")
	workflow := promptFileWorkflow(input)
	if err := snapshotWorkflowTemplates(context.Background(), other, "", workflow); err != nil {
		t.Fatal(err)
	}
	locator := filepath.Join(pipelineStateDir(root), workflowSnapshotDir, "flow.yaml")
	if err := os.MkdirAll(filepath.Dir(locator), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkflowTemplates(locator, workflow); err == nil {
		t.Fatal("foreign managed prompt_file escaped its workflow bundle")
	}
}
