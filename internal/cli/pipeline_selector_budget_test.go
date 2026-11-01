//go:build unix

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func TestPipelineResumeSelectorBudgetOverridePreservesOriginalEffects(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			root := inPipelineControlProject(t)
			pipelineRecoveryTmux(t, root, "2718", "")
			usePipelineResumeTestWorker(t)
			previousJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = previousJSON })
			workflow := &pipeline.Workflow{
				SchemaVersion: pipeline.SchemaVersion, Name: "selector-budget-cli",
				Settings: pipeline.WorkflowSettings{OnError: pipeline.ErrorActionFail, Limits: pipeline.LimitsConfig{
					MaxCommandStdoutBytes: 4, MaxCommandStderrBytes: 64, MaxSelectorOutputBytesTotal: 4,
				}},
				Steps: []pipeline.Step{
					{ID: "first", Branch: "$(printf 'first\\n' >> selector-budget-effects; printf aaaa)",
						Branches: map[string]interface{}{"aaaa": map[string]interface{}{"id": "first_body", "command": "true"}}},
					{ID: "second", DependsOn: []string{"first"}, Branch: "$(printf 'second\\n' >> selector-budget-effects; printf bbbb)",
						Branches: map[string]interface{}{"bbbb": map[string]interface{}{"id": "second_body", "command": "true"}}},
					{ID: "finish", DependsOn: []string{"second"}, Command: "printf done > selector-budget-done"},
				},
			}
			cfg := pipeline.DefaultExecutorConfig("delivery-session")
			cfg.ProjectDir, cfg.RunID = root, pipeline.GenerateRunID()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			first, runErr := pipeline.RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			cancel()
			if runErr == nil || first == nil || first.Status != pipeline.StatusFailed {
				t.Fatalf("initial run did not refuse aggregate selector output: %+v %v", first, runErr)
			}
			prior, err := pipeline.LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			original := prior.CommandEvaluations["first"]
			if original.Status != "settled" || string(original.Output) != "aaaa" || len(prior.CommandEvaluations) != 1 {
				t.Fatalf("refusal lost the completed source or journaled unlaunched work: %+v", prior.CommandEvaluations)
			}
			assertEffects := func(want string) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, "selector-budget-effects"))
				if err != nil || string(data) != want {
					t.Fatalf("selector effects = %q, want %q: %v", data, want, err)
				}
			}
			assertEffects("first\n")
			if _, err := os.Stat(filepath.Join(root, "selector-budget-done")); !os.IsNotExist(err) {
				t.Fatalf("dependent ran despite prelaunch refusal: %v", err)
			}
			statePath := filepath.Join(root, ".ntm", "pipelines", cfg.RunID+".json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := os.ReadFile(prior.WorkflowFile)
			if err != nil {
				t.Fatal(err)
			}
			executeResume := func(flags ...string) (string, error) {
				t.Helper()
				cmd := newPipelineResumeCmd()
				args := append([]string{cfg.RunID}, flags...)
				if background {
					args = append(args, "--background")
				}
				cmd.SetArgs(args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
			}
			awaitState := func(want pipeline.ExecutionStatus) *pipeline.ExecutionState {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					state, err := pipeline.LoadState(root, cfg.RunID)
					if err == nil && state.Status == want {
						owner, ownerErr := pipeline.AcquireRunControl(context.Background(), root, cfg.RunID)
						if ownerErr == nil {
							owner.Close()
							return state
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("resumed pipeline did not settle as %s", want)
				return nil
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = pipeline.RequestRunCancellation(ctx, root, cfg.RunID)
			})
			if raw, err := executeResume("--max-selector-output-bytes-total=-1"); err == nil || !strings.Contains(err.Error(), "max-selector-output-bytes-total") {
				t.Fatalf("negative budget override was accepted: %s %v", raw, err)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("invalid override changed the checkpoint: %v", err)
			}
			raw, resumeErr := executeResume()
			if (background && resumeErr != nil) || (!background && resumeErr == nil) {
				t.Fatalf("default-budget resume returned the wrong outcome: %s %v", raw, resumeErr)
			}
			refused := awaitState(pipeline.StatusFailed)
			if result := refused.Steps["second"]; result.Error == nil || !strings.Contains(result.Error.Message, "max_selector_output_bytes_total") {
				t.Fatalf("resume did not report aggregate admission refusal: %+v", result)
			}
			if _, exists := refused.CommandEvaluations["second"]; exists || !reflect.DeepEqual(original, refused.CommandEvaluations["first"]) {
				t.Fatalf("refused resume altered selector evidence: %+v", refused.CommandEvaluations)
			}
			assertEffects("first\n")
			if raw, err := executeResume("--max-selector-output-bytes-total=8"); err != nil {
				t.Fatalf("raised-budget resume failed: %s %v", raw, err)
			}
			final := awaitState(pipeline.StatusCompleted)
			if !reflect.DeepEqual(original, final.CommandEvaluations["first"]) || string(final.CommandEvaluations["second"].Output) != "bbbb" || final.Steps["finish"].Status != pipeline.StatusCompleted {
				t.Fatalf("raised budget replaced completed evidence or failed to admit new work: %+v", final)
			}
			assertEffects("first\nsecond\n")
			if data, err := os.ReadFile(filepath.Join(root, "selector-budget-done")); err != nil || string(data) != "done" {
				t.Fatalf("raised budget did not release dependent work: %q %v", data, err)
			}
			if after, err := os.ReadFile(prior.WorkflowFile); err != nil || !bytes.Equal(frozen, after) {
				t.Fatalf("attempt-local override changed the frozen workflow: %v", err)
			}
		})
	}
}
