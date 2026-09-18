//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func TestPipelineBackgroundResumeWorker(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) > i+3 && os.Args[i+1] == "__pipeline-worker" {
			root, id := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
			if err := pipeline.RunBackgroundWorker(context.Background(), root, id, os.Stdin); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func usePipelineResumeTestWorker(t *testing.T) {
	t.Helper()
	previous := pipeline.BackgroundWorkerFlags
	pipeline.BackgroundWorkerFlags = func() ([]string, error) {
		return []string{"-test.run=^TestPipelineBackgroundResumeWorker$", "--"}, nil
	}
	t.Cleanup(func() { pipeline.BackgroundWorkerFlags = previous })
}

func TestPipelineResumeBackgroundFlagDefaultsToForeground(t *testing.T) {
	cmd := newPipelineResumeCmd()
	value, err := cmd.Flags().GetBool("background")
	if err != nil || value {
		t.Fatalf("background flag missing or changed the foreground default: %v %v", value, err)
	}
}

func TestPipelineResumeBackgroundCommandRetainsRecoveryPolicy(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%v", reset), func(t *testing.T) {
			root := inPipelineControlProject(t)
			usePipelineResumeTestWorker(t)
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "cli-background-resume", Steps: []pipeline.Step{
				{ID: "once", Command: "printf duplicate >> once"},
				{ID: "finish", DependsOn: []string{"once"}, Command: "while [ ! -f release ]; do sleep 0.01; done; printf done > marker"},
			}}
			_, locator, err := pipeline.SnapshotWorkflow(context.Background(), root, workflow)
			if err != nil {
				t.Fatal(err)
			}
			prior := &pipeline.ExecutionState{
				RunID: "run-cli-background", WorkflowID: workflow.Name, WorkflowFile: locator,
				Session: "saved-session", Status: pipeline.StatusFailed, UpdatedAt: time.Now(),
				Steps: map[string]pipeline.StepResult{"once": {Status: pipeline.StatusCompleted, Output: "saved"}},
			}
			if err := pipeline.SaveState(root, prior); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "once"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(root, "release"), []byte("finish"), 0600)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = pipeline.RequestRunCancellation(ctx, root, prior.RunID)
			})
			cmd := newPipelineResumeCmd()
			cmd.SetContext(context.Background())
			if err := cmd.Flags().Set("background", "true"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Flags().Set("mode", "restart-failed"); err != nil {
				t.Fatal(err)
			}
			if reset {
				if err := cmd.Flags().Set("keep-state", "false"); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			if err := cmd.RunE(cmd, []string{prior.RunID}); err != nil {
				t.Fatalf("CLI did not hand off recovery to the worker: %v", err)
			}
			var response struct {
				Success    bool   `json:"success"`
				Background bool   `json:"background"`
				RunID      string `json:"run_id"`
				Session    string `json:"session"`
				Status     string `json:"status"`
				Mode       string `json:"mode"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil || !response.Success || !response.Background || response.RunID != prior.RunID || response.Session != prior.Session || response.Status != "pending" || response.Mode != "restart-failed" {
				t.Fatalf("bad CLI startup envelope: %s %v", output.String(), err)
			}
			if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			completed := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				st, err := pipeline.LoadState(root, prior.RunID)
				if err == nil && st.Status == pipeline.StatusCompleted {
					owner, err := pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
					if err == nil {
						owner.Close()
						completed = true
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !completed {
				t.Fatal("detached CLI resume did not finish")
			}
			want := "x"
			if reset {
				want += "duplicate"
			}
			data, err := os.ReadFile(filepath.Join(root, "once"))
			if err != nil || string(data) != want {
				t.Fatalf("CLI lost explicit keep/reset policy: %q want %q: %v", data, want, err)
			}
		})
	}
}

func TestPipelineResumeBackgroundRejectsLiveOwner(t *testing.T) {
	root := inPipelineControlProject(t)
	usePipelineResumeTestWorker(t)
	previous := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previous })
	owner, err := pipeline.AcquireRunControl(context.Background(), root, "run-held")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	path := filepath.Join(root, ".ntm", "pipelines", "run-held.json")
	if err := os.WriteFile(path, []byte("checkpoint must not be parsed"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newPipelineResumeCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("background", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"run-held"}); !errors.Is(err, pipeline.ErrRunAlreadyOwned) {
		t.Fatalf("CLI lost the worker ownership conflict: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "checkpoint must not be parsed" {
		t.Fatal("rejected background resume changed the checkpoint")
	}
}
