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
	"time"
)

func writeSnapshotTemplate(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func templateWorkflow() *Workflow {
	return &Workflow{SchemaVersion: "2.0", Name: "frozen-templates", Steps: []Step{{ID: "work", Template: "prompt.md", Agent: "claude"}}}
}

func TestSnapshotTemplatesFreezeSourcePrecedenceAndLifecycle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workflows", "flow.yaml")
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "original {{TASK}} ${steps.previous.output}")
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "wrong project fallback")
	writeSnapshotTemplate(t, filepath.Join(root, "fallback.md"), "fallback")
	workflow := templateWorkflow()
	workflow.PostPipelineSteps = []Step{{ID: "post", Template: "fallback.md"}}
	workflow.Settings.OnCancel = []Step{{ID: "cleanup", Template: "prompt.md"}}
	workflow.Steps = append(workflow.Steps, Step{ID: "parallel"})
	workflow.Steps[1].Parallel.Steps = []Step{{ID: "nested", Template: "prompt.md"}}
	if err := snapshotWorkflowTemplates(context.Background(), root, source, workflow); err != nil {
		t.Fatal(err)
	}
	path := workflow.Steps[0].Template
	if !managedTemplateSnapshot(path) || path != workflow.Settings.OnCancel[0].Template || path != workflow.Steps[1].Parallel.Steps[0].Template {
		t.Fatalf("nested/lifecycle references were not frozen and deduplicated: %+v", workflow)
	}
	writeSnapshotTemplate(t, filepath.Join(root, "workflows", "prompt.md"), "edited after launch")
	data, err := readTemplateSnapshot(path, maxSnapshotTemplateBytes)
	if err != nil || string(data) != "original {{TASK}} ${steps.previous.output}" {
		t.Fatalf("changed or prematurely rendered prompt: %q %v", data, err)
	}
	data, err = readTemplateSnapshot(workflow.PostPipelineSteps[0].Template, maxSnapshotTemplateBytes)
	if err != nil || string(data) != "fallback" {
		t.Fatalf("lost project-root fallback: %q %v", data, err)
	}
	workflowPath := filepath.Join(pipelineStateDir(root), workflowSnapshotDir, "workflow.yaml")
	if err := verifyWorkflowTemplates(workflowPath, workflow); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateSnapshotPublicationIsConcurrentAndNonReplacing(t *testing.T) {
	root := t.TempDir()
	dir, err := prepareTemplateSnapshotDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	paths := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := publishTemplateSnapshot(dir, []byte("same content"))
			if err != nil {
				t.Error(err)
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	path := ""
	count := 0
	for got := range paths {
		count++
		if path != "" && path != got {
			t.Fatal("identical content got different names")
		}
		path = got
	}
	if count != workers {
		t.Fatalf("only %d/%d publishers succeeded", count, workers)
	}
	old := time.Unix(1234567890, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := publishTemplateSnapshot(dir, []byte("same content")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatal("existing artifact was rewritten")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("new template artifact was not private")
	}
	writeSnapshotTemplate(t, path, "damaged")
	if _, err := publishTemplateSnapshot(dir, []byte("same content")); err == nil {
		t.Fatal("corruption was silently repaired")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "damaged" {
		t.Fatal("corrupt evidence was overwritten")
	}
}

func TestSnapshotTemplatesRejectCorruptOrMissingManagedDependencies(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			root := t.TempDir()
			writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "original")
			workflow := templateWorkflow()
			if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
				t.Fatal(err)
			}
			path := workflow.Steps[0].Template
			if missing {
				// Move the test artifact aside rather than removing evidence.
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			} else {
				writeSnapshotTemplate(t, path, "substituted")
			}
			workflowPath := filepath.Join(pipelineStateDir(root), workflowSnapshotDir, "flow.yaml")
			if err := verifyWorkflowTemplates(workflowPath, workflow); err == nil {
				t.Fatal("damaged dependency accepted for resume")
			}
			if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
				t.Fatal("damaged managed dependency reclassified as generated or re-hashed")
			}
		})
	}
}

func TestSnapshotTemplatesGeneratedPathsAndReadFailures(t *testing.T) {
	root := t.TempDir()
	workflow := templateWorkflow()
	workflow.Steps[0].Template = filepath.Join(root, "generated.md")
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatalf("absolute generated path rejected: %v", err)
	}
	if workflow.Steps[0].Template != filepath.Join(root, "generated.md") {
		t.Fatal("generated path was rewritten")
	}
	workflow.Steps[0].Template = "generated.md"
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
		t.Fatal("guessed a future relative template location")
	}
	workflow.Steps[0].Template = root
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
		t.Fatal("directory accepted as template content")
	}
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "too large")
	workflow.Steps[0].Template = "prompt.md"
	workflow.Settings.Limits.MaxTemplateBytes = 3
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
		t.Fatal("workflow template byte limit was bypassed")
	}
}

func TestSnapshotTemplatesCancellationAndReadBounds(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "input")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshotWorkflowTemplates(ctx, root, "", templateWorkflow()); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled snapshot created artifacts: %v", err)
	}
	if _, err := readTemplateFile(filepath.Join(root, "prompt.md"), 4); err == nil {
		t.Fatal("bounded reader accepted an oversized template")
	}
	if data, err := readTemplateFile(filepath.Join(root, "prompt.md"), 5); err != nil || string(data) != "input" {
		t.Fatalf("exact size bound rejected: %q %v", data, err)
	}
}

func TestSnapshotTemplatesRejectStorageAndDependencySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(pipelineStateDir(root), workflowSnapshotDir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, templateSnapshotDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareTemplateSnapshotDirectory(root); err == nil {
		t.Fatal("symlinked template storage accepted")
	}
	root = t.TempDir()
	dir, err := prepareTemplateSnapshotDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := publishTemplateSnapshot(dir, []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if _, err := readTemplateSnapshot(path, maxSnapshotTemplateBytes); err == nil {
		t.Fatal("managed dependency symlink accepted")
	}
}

func TestSnapshotTemplatesBoundAggregateDistinctInputs(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", maxSnapshotTemplateBytes/2+1)
	for _, name := range []string{"a", "b"} {
		writeSnapshotTemplate(t, filepath.Join(root, name), content)
	}
	workflow := templateWorkflow()
	workflow.Settings.Limits.MaxTemplateBytes = maxSnapshotTemplateBytes
	workflow.Steps = []Step{{ID: "first", Template: "a"}, {ID: "second", Template: "b"}}
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err == nil {
		t.Fatal("aggregate byte budget was bypassed")
	}
}

func TestSnapshotTemplatesVisitEveryExecutableTemplateLocation(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "shared input")
	leaf := func(id string) Step { return Step{ID: id, Template: "prompt.md"} }
	workflow := templateWorkflow()
	workflow.Steps[0].OnSuccess = []Step{leaf("success")}
	workflow.Steps[0].OnFailure.Fallback = map[string]interface{}{"template": "prompt.md", "params": map[string]interface{}{"template": "not-a-file"}}
	workflow.Steps = append(workflow.Steps,
		Step{ID: "loop", Loop: &LoopConfig{Steps: []Step{leaf("loop-body")}}},
		Step{ID: "foreach", Foreach: &ForeachConfig{Template: "prompt.md", Steps: []Step{leaf("foreach-body")}}},
		Step{ID: "panes", ForeachPane: &ForeachConfig{Template: "prompt.md", Steps: []Step{leaf("pane-body")}}},
		Step{ID: "choose", Branch: "left", Branches: map[string]interface{}{
			"left":    map[string]interface{}{"template": "prompt.md", "on_success": []interface{}{map[string]interface{}{"template": "prompt.md"}}},
			"default": []interface{}{map[string]interface{}{"template": "prompt.md"}},
		}},
	)
	workflow.PostPipelineSteps = []Step{leaf("post")}
	workflow.Settings.OnCancel = []Step{leaf("cancel")}
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := visitWorkflowTemplates(workflow, false, func(step *Step) error {
		count++
		if !managedTemplateSnapshot(step.Template) {
			t.Fatalf("live dependency left at %s: %q", step.ID, step.Template)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 13 {
		t.Fatalf("visited %d dependencies, want 13", count)
	}
	params := workflow.Steps[0].OnFailure.Fallback["params"].(map[string]interface{})
	if params["template"] != "not-a-file" {
		t.Fatal("ordinary template parameter was mistaken for an executable dependency")
	}
}

func TestSnapshotTemplateBranchesKeepAuthoredIDs(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTemplate(t, filepath.Join(root, "prompt.md"), "input")
	workflow := templateWorkflow()
	workflow.Steps[0].Template = ""
	workflow.Steps[0].Branches = map[string]interface{}{
		"anonymous": map[string]interface{}{"template": "prompt.md"},
		"named":     []interface{}{map[string]interface{}{"id": "authored", "template": "prompt.md"}},
	}
	if err := snapshotWorkflowTemplates(context.Background(), root, "", workflow); err != nil {
		t.Fatal(err)
	}
	anonymous, err := parseBranchSteps(workflow.Steps[0].Branches["anonymous"], "new-parent", "anonymous")
	if err != nil || anonymous[0].ID != "new-parent.anonymous.0" {
		t.Fatalf("froze generated child IDs: %+v %v", anonymous, err)
	}
	named, err := parseBranchSteps(workflow.Steps[0].Branches["named"], "new-parent", "named")
	if err != nil || named[0].ID != "authored" {
		t.Fatalf("changed authored child ID: %+v %v", named, err)
	}
}
