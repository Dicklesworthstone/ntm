package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func inPipelineControlProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return root
}

func TestPipelineResumeRejectsLiveOwnerBeforeReadingState(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-held")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(root, ".ntm", "pipelines", "run-held.json")
	const untouched = "not even valid checkpoint JSON"
	if err := os.WriteFile(path, []byte(untouched), 0600); err != nil {
		t.Fatal(err)
	}

	for _, asJSON := range []bool{false, true} {
		jsonOutput = asJSON
		cmd := newPipelineResumeCmd()
		cmd.SetContext(context.Background())
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		previousStdout := os.Stdout
		os.Stdout = write
		err = cmd.RunE(cmd, []string{"run-held"})
		os.Stdout = previousStdout
		_ = write.Close()
		raw, readErr := io.ReadAll(read)
		_ = read.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err == nil || !strings.Contains(err.Error(), "pipeline run has a live owner") {
			t.Fatalf("resume read state or entered executor without ownership (json=%v): %v %s", asJSON, err, raw)
		}
		if asJSON {
			var out struct {
				Success bool   `json:"success"`
				Code    string `json:"error_code"`
			}
			if err := json.Unmarshal(raw, &out); err != nil || out.Success || out.Code != "PIPELINE_RUNNING" {
				t.Fatalf("bad ownership error envelope: %s %v", raw, err)
			}
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != untouched {
		t.Fatal("rejected resume changed checkpoint")
	}
}

func TestPipelineResumeMissingStateReleasesOwnership(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"run-missing"}); err == nil {
		t.Fatal("missing state resumed")
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-missing")
	if err != nil {
		t.Fatalf("failed resume leaked its lock: %v", err)
	}
	owner.Close()
}

func TestPipelineResumeRejectsDamagedManagedWorkflow(t *testing.T) {
	root := inPipelineControlProject(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	// Command-only workflow: no agent is driven. The session probe is a
	// deterministic tmux fixture; the workflow loader and hash check are real.
	fakeTmux := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nif [ \"$1\" = -V ]; then echo 'tmux 3.4'; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", fakeTmux)
	dir := filepath.Join(root, ".ntm", "pipelines", "workflow-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sha256-"+strings.Repeat("0", 64)+".yaml")
	workflow := "schema_version: \"2.0\"\nname: damaged\nsteps:\n  - id: mark\n    command: 'echo unsafe > resumed-marker'\n"
	if err := os.WriteFile(path, []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	prior := &pipeline.ExecutionState{RunID: "run-damaged", WorkflowID: "damaged", WorkflowFile: path, Session: "snapshot-session", Status: pipeline.StatusFailed, Steps: map[string]pipeline.StepResult{}}
	if err := pipeline.SaveState(root, prior); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json")
	before, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	err = cmd.RunE(cmd, []string{prior.RunID})
	if err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("snapshot integrity was not enforced: %v", err)
	}
	if after, err := os.ReadFile(checkpointPath); err != nil || string(after) != string(before) {
		t.Fatal("snapshot rejection changed checkpoint")
	}
	if _, err := os.Stat(filepath.Join(root, "resumed-marker")); !os.IsNotExist(err) {
		t.Fatal("damaged workflow was executed")
	}
}
