package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func acceptedBeadClose(t *testing.T, srv *Server) *Job {
	t.Helper()
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/beads/bd-task/close?async=true", "")
	var response struct {
		Job *Job `json:"job"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Job == nil {
		t.Fatalf("close not accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/api/v1/jobs/"+response.Job.ID {
		t.Fatalf("missing polling URL: %v", rec.Header())
	}
	return response.Job
}

func awaitBeadCloseFinished(t *testing.T, srv *Server, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job := srv.jobStore.Get(id)
		srv.jobStore.mu.RLock()
		_, owned := srv.jobStore.cancels[id]
		srv.jobStore.mu.RUnlock()
		if job != nil && !owned && job.Status != JobStatusPending && job.Status != JobStatusRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("bead close did not finish: %+v, owned=%t", job, owned)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBeadCloseSharesAdmissionLimitsAndQueuedCancellation(t *testing.T) {
	project, logPath := installCloseTestBR(t, "normal")
	srv := NewHermeticServer("bead-admission-test")
	defer srv.Stop()
	srv.projectDir = project
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := acceptedBeadClose(t, srv)
	if queued.Status != JobStatusPending || queued.ProjectDir != project || queued.Result["bead_id"] != "bd-task" {
		t.Fatalf("missing pending identity: %+v", queued)
	}
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/beads/bd-task/close?async=true", "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("overload = %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
	if len(srv.jobStore.List()) != 2 {
		t.Fatal("overload created an unowned receipt")
	}
	rec = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	release()
	job := awaitBeadCloseFinished(t, srv, queued.ID)
	if job.Status != JobStatusCancelled || job.Result["bead_id"] != "bd-task" {
		t.Fatalf("queued cancellation lost its target: %+v", job)
	}
	if data, _ := os.ReadFile(logPath); len(data) != 0 {
		t.Fatalf("queued or rejected close entered the tracker: %s", data)
	}
}

func TestBeadCloseQueueFreezesProjectAndSurvivesRequestCancellation(t *testing.T) {
	project, logPath := installCloseTestBR(t, "normal")
	srv := NewHermeticServer("bead-project-test")
	defer srv.Stop()
	srv.projectDir = project
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-task/close?async=true", nil).WithContext(ctx))
	var accepted struct {
		Job *Job `json:"job"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.Job == nil {
		t.Fatalf("close = %d %s", rec.Code, rec.Body.String())
	}
	cancel() // The accepted work belongs to the server, not the HTTP connection.
	other := t.TempDir()
	srv.mu.Lock()
	srv.projectDir = other
	srv.mu.Unlock()
	release()
	job := awaitBeadCloseFinished(t, srv, accepted.Job.ID)
	if job.Status != JobStatusCompleted || job.ProjectDir != project || job.Result["project_dir"] != project || job.Result["closed"] != true {
		t.Fatalf("accepted execution moved or was tied to the request: %+v", job)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Contains(string(data), other) || strings.Count(string(data), project+":close") != 1 {
		t.Fatalf("wrong workspace or duplicate close: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(other, ".close-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutated the replacement project: %v", err)
	}
}

func TestBeadClosePendingIdentityIsDurableBeforeAcceptance(t *testing.T) {
	project, logPath := installCloseTestBR(t, "normal")
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	defer srv.Stop()
	srv.projectDir = project
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := acceptedBeadClose(t, srv)
	dir, err := srv.jobJournalDir()
	if err != nil || dir == "" {
		t.Fatalf("no journal: %q %v", dir, err)
	}
	readReceipt := func(want JobStatus) {
		t.Helper()
		jobs, err := (&jobJournal{dir: dir}).load()
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.ID == queued.ID {
				if job.Status != want || job.ProjectDir != project || job.Result["bead_id"] != "bd-task" || job.Result["project_dir"] != project {
					t.Fatalf("receipt lost recovery identity: %+v", job)
				}
				return
			}
		}
		t.Fatal("accepted close was not persisted")
	}
	readReceipt(JobStatusPending)
	rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	readReceipt(JobStatusCancelled)
	if data, _ := os.ReadFile(logPath); len(data) != 0 {
		t.Fatalf("journal acceptance or queued cancellation ran tracker work: %s", data)
	}
}

func TestBeadCloseUsesPerBeadExclusionNotGlobalBarrier(t *testing.T) {
	project, logPath := installCloseTestBR(t, "block")
	srv := NewHermeticServer("bead-resources-test")
	defer srv.Stop()
	defer func() { _ = os.WriteFile(logPath+".release", nil, 0o600) }()
	srv.projectDir = project
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 2, 3
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	first := acceptedBeadClose(t, srv)
	second := acceptedBeadClose(t, srv)
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"independent","cc_count":1}}`)
	// Independent work must pass a queued same-bead request even while the
	// first close blocks. A sessionless global barrier would starve this spawn.
	awaitExecutorSignal(t, started)
	if job := srv.jobStore.Get(second.ID); job.Status != JobStatusPending {
		t.Fatalf("same-bead read/close sequences overlap: %+v", job)
	}
	if err := os.WriteFile(logPath+".release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := awaitBeadCloseFinished(t, srv, first.ID)
	b := awaitBeadCloseFinished(t, srv, second.ID)
	if a.Status != JobStatusCompleted || b.Status != JobStatusCompleted || b.Result["already_closed"] != true {
		t.Fatalf("duplicate close was not reconciled: first=%+v second=%+v", a, b)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(data), ":close") != 1 {
		t.Fatalf("expected exactly one mutation: %s, %v", data, err)
	}
}

func TestBeadCloseCannotBypassPermissionRouteOrStoppedAdmission(t *testing.T) {
	project, logPath := installCloseTestBR(t, "normal")
	srv := NewHermeticServer("bead-route-test")
	defer srv.Stop()
	srv.projectDir = project
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", `{"type":"bead_close","params":{"bead_id":"bd-task"}}`)
	if rec.Code != http.StatusNotImplemented || len(srv.jobStore.List()) != 0 {
		t.Fatalf("private operation exposed by generic jobs: %d %s", rec.Code, rec.Body.String())
	}
	srv.Stop()
	rec = admissionRequest(srv, http.MethodPost, "/api/v1/beads/bd-task/close?async=true", "")
	if rec.Code != http.StatusServiceUnavailable || len(srv.jobStore.List()) != 0 {
		t.Fatalf("stopped server accepted a close: %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(logPath); len(data) != 0 {
		t.Fatalf("rejected close touched the tracker: %s", data)
	}
}

func TestBeadCloseAdmissionRejectsInvalidExecutionIdentity(t *testing.T) {
	srv := &Server{projectDir: t.TempDir()}
	for _, req := range []CreateJobRequest{
		{Type: jobTypeBeadClose},
		{Type: jobTypeBeadClose, Params: map[string]interface{}{"bead_id": "--db=other"}},
		{Type: jobTypeBeadClose, Params: map[string]interface{}{"bead_id": 7}},
		{Type: jobTypeBeadClose, Params: map[string]interface{}{"bead_id": "bd-task", "project_dir": "/other"}},
		{Type: jobTypeBeadClose, Session: "other", Params: map[string]interface{}{"bead_id": "bd-task"}},
	} {
		if _, err := srv.prepareJobRequest(req); !errors.Is(err, errInvalidJobRequest) {
			t.Errorf("invalid private request accepted: %+v err=%v", req, err)
		}
	}
}

func TestBeadCloseProgressFailurePreventsTrackerExecution(t *testing.T) {
	project, logPath := installCloseTestBR(t, "normal")
	srv := &Server{projectDir: project}
	checkpointErr := errors.New("journal write failed")
	ctx := context.WithValue(context.Background(), jobProgressContextKey{}, jobProgressReporter(func(result map[string]interface{}) error {
		if result["bead_id"] != "bd-task" || result["project_dir"] != project {
			t.Fatalf("checkpoint lacks recovery identity: %#v", result)
		}
		return checkpointErr
	}))
	result, err := srv.executeBeadCloseJob(ctx, map[string]interface{}{"bead_id": "bd-task"})
	if !errors.Is(err, checkpointErr) || result["bead_id"] != "bd-task" {
		t.Fatalf("checkpoint failure was hidden: %#v %v", result, err)
	}
	if data, _ := os.ReadFile(logPath); len(data) != 0 {
		t.Fatalf("tracker touched after failed checkpoint: %s", data)
	}
}
