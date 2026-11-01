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

func TestPipelineResumeStepBudgetsPreserveCompletedEffects(t *testing.T) {
	for _, limit := range []struct {
		flag, field string
	}{
		{"max-step-count-total", "max_step_count_total"},
		{"max-materialized-step-nodes", "max_materialized_step_nodes"},
	} {
		for _, background := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/background=%t", limit.flag, background), func(t *testing.T) {
				root := inPipelineControlProject(t)
				pipelineRecoveryTmux(t, root, "2718", "")
				usePipelineResumeTestWorker(t)
				previousJSON := jsonOutput
				jsonOutput = true
				t.Cleanup(func() { jsonOutput = previousJSON })
				limits := pipeline.LimitsConfig{
					MaxCommandStdoutBytes: 64, MaxCommandStderrBytes: 64,
					MaxStepCountTotal: 64, MaxMaterializedStepNodes: 64,
				}
				if limit.field == "max_step_count_total" {
					limits.MaxStepCountTotal = 8
				} else {
					limits.MaxMaterializedStepNodes = 8
				}
				workflow := &pipeline.Workflow{
					SchemaVersion: pipeline.SchemaVersion, Name: "step-budget-cli",
					Settings: pipeline.WorkflowSettings{OnError: pipeline.ErrorActionFail, Limits: limits},
					Steps: []pipeline.Step{
						{ID: "once", Command: "printf 'once\\n' >> admission-once"},
						{ID: "fanout", DependsOn: []string{"once"}, Foreach: &pipeline.ForeachConfig{
							Beads: "$(printf 'source\\n' >> admission-source; printf '[\"a\",\"b\",\"c\"]')",
							Steps: []pipeline.Step{
								{ID: "one", Command: "printf '%s-one\\n' '${item}' >> admission-children"},
								{ID: "two", Command: "printf '%s-two\\n' '${item}' >> admission-children"},
								{ID: "three", Command: "printf '%s-three\\n' '${item}' >> admission-children"},
							},
						}},
						{ID: "finish", DependsOn: []string{"fanout"}, Command: "printf done > admission-done"},
					},
					PostPipelineSteps: []pipeline.Step{{ID: "post", Command: "printf 'post\\n' >> admission-post"}},
				}
				cfg := pipeline.DefaultExecutorConfig("delivery-session")
				cfg.ProjectDir, cfg.RunID = root, pipeline.GenerateRunID()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				first, runErr := pipeline.RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				cancel()
				if runErr == nil || first == nil || first.Status != pipeline.StatusFailed || !strings.Contains(runErr.Error(), limit.field) {
					t.Fatalf("initial execution did not refuse %s: %+v %v", limit.field, first, runErr)
				}
				prior, err := pipeline.LoadState(root, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				originalCommand, originalSource := prior.CommandExecutions["once"], prior.CommandEvaluations["fanout"]
				if originalCommand.Status != "settled" || originalSource.Status != "settled" || string(originalSource.Output) != `["a","b","c"]` {
					t.Fatalf("fixture did not retain genuine completed predecessor and source receipts: %+v %+v", originalCommand, originalSource)
				}
				assertFile := func(name, want string) {
					t.Helper()
					data, err := os.ReadFile(filepath.Join(root, name))
					if err != nil || string(data) != want {
						t.Fatalf("%s = %q, want %q: %v", name, data, want, err)
					}
				}
				assertPost := func(state *pipeline.ExecutionState, attempts int) {
					t.Helper()
					receipt := state.CommandExecutions["post"]
					if receipt.Status != "settled" || receipt.Result.Status != pipeline.StatusCompleted || state.Steps["post"].Status != pipeline.StatusCompleted {
						t.Fatalf("post-pipeline work lacks its settled receipt: %+v", receipt)
					}
					if state.StepAdmissions == nil || !state.StepAdmissions.Finalization["post"] || state.StepAdmissions.Work["post"] || len(state.StepAdmissions.Finalization) != 1 {
						t.Fatalf("post-pipeline identity changed admission phase or was charged again: %+v", state.StepAdmissions)
					}
					// Reopening main work intentionally starts a new finalization
					// attempt, while its already-admitted identity stays free.
					assertFile("admission-post", strings.Repeat("post\n", attempts))
				}
				assertRefused := func(state *pipeline.ExecutionState, attempts int) {
					t.Helper()
					assertPost(state, attempts)
					if !reflect.DeepEqual(originalCommand, state.CommandExecutions["once"]) || !reflect.DeepEqual(originalSource, state.CommandEvaluations["fanout"]) {
						t.Fatal("admission refusal replaced completed execution evidence")
					}
					for id := range state.CommandExecutions {
						if strings.HasPrefix(id, "fanout_") || id == "finish" {
							t.Fatalf("unadmitted work acquired a launch receipt: %s", id)
						}
					}
					assertFile("admission-once", "once\n")
					assertFile("admission-source", "source\n")
					for _, name := range []string{"admission-children", "admission-done"} {
						if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
							t.Fatalf("unadmitted work produced %s: %v", name, err)
						}
					}
				}
				assertRefused(prior, 1)
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
					var latest *pipeline.ExecutionState
					var latestErr error
					for time.Now().Before(deadline) {
						state, err := pipeline.LoadState(root, cfg.RunID)
						latest, latestErr = state, err
						if err == nil && state.Status == want {
							owner, ownerErr := pipeline.AcquireRunControl(context.Background(), root, cfg.RunID)
							if ownerErr == nil {
								// Ownership acquisition can wait for the worker to finish.
								// Re-read under that lock so a prior failed checkpoint
								// cannot conceal this attempt's actual outcome.
								state, err = pipeline.LoadState(root, cfg.RunID)
								owner.Close()
								latest, latestErr = state, err
								if err == nil && state.Status == want {
									return state
								}
							}
						}
						time.Sleep(10 * time.Millisecond)
					}
					if latest != nil {
						result := latest.Steps["fanout"]
						t.Fatalf("resumed pipeline did not settle as %s: status=%s result=%+v step_error=%+v run_errors=%+v load_error=%v", want, latest.Status, result, result.Error, latest.Errors, latestErr)
					}
					t.Fatalf("resumed pipeline did not settle as %s: %v", want, latestErr)
					return nil
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_ = pipeline.RequestRunCancellation(ctx, root, cfg.RunID)
				})
				for _, flag := range []string{"max-step-count-total", "max-materialized-step-nodes"} {
					if raw, err := executeResume("--" + flag + "=-1"); err == nil || !strings.Contains(err.Error(), flag) {
						t.Fatalf("negative %s was accepted: %s %v", flag, raw, err)
					}
					if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(before, after) {
						t.Fatalf("invalid %s changed the checkpoint: %v", flag, err)
					}
				}
				raw, resumeErr := executeResume()
				if (background && resumeErr != nil) || (!background && resumeErr == nil) {
					t.Fatalf("same-budget resume returned the wrong outcome: %s %v", raw, resumeErr)
				}
				refused := awaitState(pipeline.StatusFailed)
				if result := refused.Steps["fanout"]; result.Error == nil || !strings.Contains(result.Error.Message, limit.field) {
					t.Fatalf("same-budget resume lost the admission refusal: result=%+v step_error=%+v run_errors=%+v", result, result.Error, refused.Errors)
				}
				assertRefused(refused, 2)
				if raw, err := executeResume("--" + limit.flag + "=32"); err != nil {
					t.Fatalf("raised-budget resume failed: %s %v", raw, err)
				}
				final := awaitState(pipeline.StatusCompleted)
				assertPost(final, 3)
				if final.Steps["finish"].Status != pipeline.StatusCompleted || !reflect.DeepEqual(originalCommand, final.CommandExecutions["once"]) || !reflect.DeepEqual(originalSource, final.CommandEvaluations["fanout"]) {
					t.Fatalf("raised budget did not preserve the original evidence and complete dependents: %+v", final)
				}
				assertFile("admission-once", "once\n")
				assertFile("admission-source", "source\n")
				assertFile("admission-children", "a-one\na-two\na-three\nb-one\nb-two\nb-three\nc-one\nc-two\nc-three\n")
				assertFile("admission-done", "done")
				if after, err := os.ReadFile(prior.WorkflowFile); err != nil || !bytes.Equal(frozen, after) {
					t.Fatalf("attempt-local override changed the frozen workflow: %v", err)
				}
			})
		}
	}
}
