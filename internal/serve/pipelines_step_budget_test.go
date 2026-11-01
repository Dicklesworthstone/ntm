//go:build unix

package serve

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func TestPipelineResumeStepBudgetsHTTPAndJobs(t *testing.T) {
	for _, field := range []string{"max_step_count_total", "max_materialized_step_nodes"} {
		for _, asJob := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/job=%t", field, asJob), func(t *testing.T) {
				srv := NewHermeticServer("test")
				defer srv.Stop()
				root := t.TempDir()
				srv.mu.Lock()
				srv.projectDir = root
				srv.mu.Unlock()
				limits := pipeline.LimitsConfig{
					MaxCommandStdoutBytes: 64, MaxCommandStderrBytes: 64,
					MaxStepCountTotal: 64, MaxMaterializedStepNodes: 64,
				}
				if field == "max_step_count_total" {
					limits.MaxStepCountTotal = 8
				} else {
					limits.MaxMaterializedStepNodes = 8
				}
				workflow := pipeline.Workflow{
					SchemaVersion: pipeline.SchemaVersion, Name: "step-budget-http",
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
				initial := postPipelineVariantJob(t, srv, JobTypePipelineExec, map[string]interface{}{
					"session": "step-budget", "workflow": workflow,
				})
				failed := pollJobTerminal(t, srv, initial.Job.ID)
				runID, _ := failed.Job.Result["run_id"].(string)
				if failed.Job.Status != string(JobStatusFailed) || runID == "" || !strings.Contains(failed.Job.Error, field) {
					t.Fatalf("initial public execution did not refuse %s: %+v", field, failed.Job)
				}
				prior, err := pipeline.LoadState(root, runID)
				if err != nil {
					t.Fatal(err)
				}
				originalCommand, originalSource := prior.CommandExecutions["once"], prior.CommandEvaluations["fanout"]
				if originalCommand.Status != "settled" || originalSource.Status != "settled" || string(originalSource.Output) != `["a","b","c"]` {
					t.Fatalf("fixture did not retain native predecessor and source receipts: %+v %+v", originalCommand, originalSource)
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
						t.Fatal("admission refusal changed completed execution evidence")
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
				before := pipelineRecoveryHTTPJournal(t, root, runID)
				frozen, err := os.ReadFile(prior.WorkflowFile)
				if err != nil {
					t.Fatal(err)
				}
				resumePath := "/api/v1/pipelines/" + runID + "/resume"
				request := func(body string) *httptest.ResponseRecorder {
					t.Helper()
					req := httptest.NewRequest(http.MethodPost, resumePath, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					srv.Router().ServeHTTP(rec, req)
					return rec
				}
				for _, invalidField := range []string{"max_step_count_total", "max_materialized_step_nodes"} {
					if asJob {
						invalid := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{
							"run_id": runID, invalidField: -1,
						})
						result := pollJobTerminal(t, srv, invalid.Job.ID)
						if result.Job.Status != string(JobStatusFailed) || !strings.Contains(result.Job.Error, invalidField) {
							t.Fatalf("negative job override was accepted: %+v", result.Job)
						}
					} else if rec := request(fmt.Sprintf(`{"%s":-1}`, invalidField)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), invalidField) {
						t.Fatalf("negative HTTP override was accepted: %d %s", rec.Code, rec.Body)
					}
					if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, runID)) {
						t.Fatalf("negative %s changed the checkpoint", invalidField)
					}
				}
				if asJob {
					resuming := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{"run_id": runID})
					result := pollJobTerminal(t, srv, resuming.Job.ID)
					if result.Job.Status != string(JobStatusFailed) || !strings.Contains(result.Job.Error, field) {
						t.Fatalf("same-budget job did not retain admission policy: %+v", result.Job)
					}
				} else if rec := request(`{}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
					t.Fatalf("same-budget HTTP resume did not retain admission policy: %d %s", rec.Code, rec.Body)
				}
				refused, err := pipeline.LoadState(root, runID)
				if err != nil {
					t.Fatal(err)
				}
				assertRefused(refused, 2)
				if asJob {
					resuming := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{"run_id": runID, field: 32})
					result := pollJobTerminal(t, srv, resuming.Job.ID)
					if result.Job.Status != string(JobStatusCompleted) || result.Job.Result["run_id"] != runID {
						t.Fatalf("raised budget did not reach the job executor: %+v", result.Job)
					}
				} else if rec := request(fmt.Sprintf(`{"%s":32}`, field)); rec.Code != http.StatusOK {
					t.Fatalf("raised budget did not reach HTTP resume: %d %s", rec.Code, rec.Body)
				}
				final, err := pipeline.LoadState(root, runID)
				if err != nil || final.Status != pipeline.StatusCompleted || final.Steps["finish"].Status != pipeline.StatusCompleted {
					t.Fatalf("raised budget did not complete original work: %+v %v", final, err)
				}
				assertPost(final, 3)
				if !reflect.DeepEqual(originalCommand, final.CommandExecutions["once"]) || !reflect.DeepEqual(originalSource, final.CommandEvaluations["fanout"]) {
					t.Fatal("resume replaced the completed predecessor or cached source evidence")
				}
				assertFile("admission-once", "once\n")
				assertFile("admission-source", "source\n")
				assertFile("admission-children", "a-one\na-two\na-three\nb-one\nb-two\nb-three\nc-one\nc-two\nc-three\n")
				assertFile("admission-done", "done")
				if after, err := os.ReadFile(prior.WorkflowFile); err != nil || !bytes.Equal(frozen, after) {
					t.Fatalf("resume override changed the frozen workflow: %v", err)
				}
			})
		}
	}
}
