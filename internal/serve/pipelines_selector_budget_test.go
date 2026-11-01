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

func TestPipelineResumeSelectorBudgetHTTPAndJobs(t *testing.T) {
	for _, asJob := range []bool{false, true} {
		t.Run(fmt.Sprintf("job=%t", asJob), func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			root := t.TempDir()
			srv.mu.Lock()
			srv.projectDir = root
			srv.mu.Unlock()
			workflow := pipeline.Workflow{
				SchemaVersion: pipeline.SchemaVersion, Name: "selector-budget-http",
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
			initial := postPipelineVariantJob(t, srv, JobTypePipelineExec, map[string]interface{}{
				"session": "selector-budget", "workflow": workflow,
			})
			failed := pollJobTerminal(t, srv, initial.Job.ID)
			runID, _ := failed.Job.Result["run_id"].(string)
			if failed.Job.Status != string(JobStatusFailed) || runID == "" || !strings.Contains(failed.Job.Error, "max_selector_output_bytes_total") {
				t.Fatalf("initial public execution did not report aggregate refusal: %+v", failed.Job)
			}
			prior, err := pipeline.LoadState(root, runID)
			if err != nil {
				t.Fatal(err)
			}
			original := prior.CommandEvaluations["first"]
			if original.Status != "settled" || string(original.Output) != "aaaa" || len(prior.CommandEvaluations) != 1 {
				t.Fatalf("refusal changed the completed source or created unlaunched evidence: %+v", prior.CommandEvaluations)
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
				t.Fatalf("dependent executed despite aggregate refusal: %v", err)
			}
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
			if asJob {
				invalid := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{
					"run_id": runID, "max_selector_output_bytes_total": -1,
				})
				result := pollJobTerminal(t, srv, invalid.Job.ID)
				if result.Job.Status != string(JobStatusFailed) || !strings.Contains(result.Job.Error, "max_selector_output_bytes_total") {
					t.Fatalf("negative job budget was accepted: %+v", result.Job)
				}
			} else if rec := request(`{"max_selector_output_bytes_total":-1}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_selector_output_bytes_total") {
				t.Fatalf("negative HTTP budget was accepted: %d %s", rec.Code, rec.Body)
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, runID)) {
				t.Fatal("negative override changed the original checkpoint")
			}
			if asJob {
				resuming := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{"run_id": runID})
				result := pollJobTerminal(t, srv, resuming.Job.ID)
				if result.Job.Status != string(JobStatusFailed) || !strings.Contains(result.Job.Error, "max_selector_output_bytes_total") {
					t.Fatalf("default-budget job did not retain admission policy: %+v", result.Job)
				}
			} else if rec := request(`{}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_selector_output_bytes_total") {
				t.Fatalf("default-budget HTTP resume did not retain admission policy: %d %s", rec.Code, rec.Body)
			}
			refused, err := pipeline.LoadState(root, runID)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := refused.CommandEvaluations["second"]; exists || !reflect.DeepEqual(original, refused.CommandEvaluations["first"]) {
				t.Fatalf("refused resume changed selector evidence: %+v", refused.CommandEvaluations)
			}
			assertEffects("first\n")
			if asJob {
				resuming := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{
					"run_id": runID, "max_selector_output_bytes_total": 8,
				})
				result := pollJobTerminal(t, srv, resuming.Job.ID)
				if result.Job.Status != string(JobStatusCompleted) || result.Job.Result["run_id"] != runID {
					t.Fatalf("raised budget did not reach the job executor: %+v", result.Job)
				}
			} else if rec := request(`{"max_selector_output_bytes_total":8}`); rec.Code != http.StatusOK {
				t.Fatalf("raised budget did not reach HTTP resume: %d %s", rec.Code, rec.Body)
			}
			final, err := pipeline.LoadState(root, runID)
			if err != nil || final.Status != pipeline.StatusCompleted || final.Steps["finish"].Status != pipeline.StatusCompleted {
				t.Fatalf("raised budget did not complete original work: %+v %v", final, err)
			}
			if !reflect.DeepEqual(original, final.CommandEvaluations["first"]) || string(final.CommandEvaluations["second"].Output) != "bbbb" {
				t.Fatalf("resume changed the original outcome or omitted the new one: %+v", final.CommandEvaluations)
			}
			assertEffects("first\nsecond\n")
			if data, err := os.ReadFile(filepath.Join(root, "selector-budget-done")); err != nil || string(data) != "done" {
				t.Fatalf("raised budget did not release dependent work: %q %v", data, err)
			}
			if after, err := os.ReadFile(prior.WorkflowFile); err != nil || !bytes.Equal(frozen, after) {
				t.Fatalf("resume override changed the frozen workflow: %v", err)
			}
		})
	}
}
