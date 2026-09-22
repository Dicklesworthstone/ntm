package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func admissionRequest(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func admissionAcceptedJob(t *testing.T, srv *Server, body string) *Job {
	t.Helper()
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", body)
	var response struct {
		Job *Job `json:"job"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Job == nil {
		t.Fatalf("job not accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/api/v1/jobs/"+response.Job.ID {
		t.Fatalf("missing job polling location: %v", rec.Header())
	}
	return response.Job
}

func admissionBlockSpawn(srv *Server) (<-chan struct{}, func(), *atomic.Int32) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	calls := &atomic.Int32{}
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		out := &robot.SpawnOutput{Session: opts.Session}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-release:
			out.Success = true
			return out, nil
		}
	}
	return started, func() { once.Do(func() { close(release) }) }, calls
}

func TestJobAdmissionHTTPOverloadAndQueuedCancellation(t *testing.T) {
	srv := NewHermeticServer("admission-test")
	defer srv.Stop()
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	started, release, calls := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"waiting","cc_count":1}}`)
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", `{"type":"swarm_spawn","params":{"session":"overflow","cc_count":1}}`)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("overload = %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
	if len(srv.jobStore.List()) != 2 || calls.Load() != 1 {
		t.Fatal("overload created another job or started another engine")
	}
	rec = admissionRequest(srv, http.MethodGet, "/api/v1/jobs", "")
	var listing struct {
		Execution JobExecutionStatus `json:"execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Execution.Running != 1 || listing.Execution.Queued != 1 || listing.Execution.Owned != 2 {
		t.Fatalf("HTTP hides saturation: %+v", listing.Execution)
	}
	rec = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if got := srv.jobStore.Get(queued.ID); got.Status != JobStatusCancelled || got.Error != "cancelled by user" {
		t.Fatalf("cancelled queued row: %+v", got)
	}
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"replacement","cc_count":1}}`)
	if calls.Load() != 1 {
		t.Fatal("queued cancellation entered an engine")
	}
}

func TestJobAdmissionHTTPReceiptExistsBeforeAcceptance(t *testing.T) {
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"waiting","cc_count":1,"operation_id":"not-yet-executed"}}`)
	dir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := (&jobJournal{dir: dir}).load()
	if err != nil {
		t.Fatal(err)
	}
	var pending *Job
	for _, job := range jobs {
		if job.ID == queued.ID {
			pending = job
		}
	}
	if pending == nil || pending.Status != JobStatusPending {
		t.Fatalf("202 preceded durable admission: %+v", pending)
	}
	operation, err := readJobOperation(jobOperationPath(dir, "not-yet-executed"))
	if err != nil || operation != nil {
		t.Fatalf("queue entry invoked the operation guard: %+v %v", operation, err)
	}
	rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	jobs, err = (&jobJournal{dir: dir}).load()
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ID == queued.ID && (job.Status != JobStatusCancelled || job.Error != "cancelled by user") {
			t.Fatalf("cancellation acknowledged before its checkpoint: %+v", job)
		}
	}
}

func TestJobAdmissionPreservesLiveProgressOnCancellation(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.jobStore.SetCancel(job.ID, cancel)
	defer srv.jobStore.ClearCancel(job.ID)
	srv.jobStore.Update(job.ID, JobStatusRunning, 0, map[string]interface{}{
		"_execution_in_progress": true, "pane_id": "%17",
	}, "")
	got, err := srv.cancelJob(job.ID)
	if err != nil || ctx.Err() == nil || got.Result["pane_id"] != "%17" || got.Status != JobStatusCancelled {
		t.Fatalf("cancellation erased effects or missed worker: %+v %v", got, err)
	}
	if _, err := srv.cancelJob(job.ID); !errors.Is(err, errJobNotCancellable) {
		t.Fatalf("repeated cancellation: %v", err)
	}
	if _, err := srv.cancelJob("missing"); !errors.Is(err, errJobNotFound) {
		t.Fatalf("missing cancellation: %v", err)
	}
}

func TestJobAdmissionOwnedTerminalJobsSurviveRetention(t *testing.T) {
	store := NewJobStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := store.Create(JobTypeSwarmSpawn, jobOwnership{cancel: cancel})
	store.Update(job.ID, JobStatusCancelled, 0, map[string]interface{}{"pane_id": "%17"}, "cancelled by user")
	store.mu.Lock()
	store.evictTerminalLocked(0)
	store.mu.Unlock()
	if got := store.Get(job.ID); got == nil || got.Result["pane_id"] != "%17" {
		t.Fatal("retention evicted an unwinding worker's recovery evidence")
	}
	store.Cancel(job.ID)
	if ctx.Err() == nil {
		t.Fatal("retention removed the real cancellation handle")
	}
	store.ClearCancel(job.ID)
	store.mu.Lock()
	store.evictTerminalLocked(0)
	store.mu.Unlock()
	if store.Get(job.ID) != nil {
		t.Fatal("completed ownership never became evictable")
	}
}

func TestJobAdmissionLateCancelRegistrationObservesTerminalState(t *testing.T) {
	for _, status := range []JobStatus{JobStatusCancelled, JobStatusCompleted, JobStatusFailed} {
		store := NewJobStore()
		job := store.Create(JobTypeSwarmSpawn)
		store.Update(job.ID, status, 0, nil, "terminal")
		ctx, cancel := context.WithCancel(context.Background())
		store.SetCancel(job.ID, cancel)
		if ctx.Err() == nil {
			t.Errorf("late handle revived %s execution", status)
		}
		cancel()
		store.ClearCancel(job.ID)
	}
}

func TestJobAdmissionFreezePreservesOperationIdentityAndNumbers(t *testing.T) {
	input := `{"type":"pipeline_exec","session":"outer","params":{"operation_id":"once","workflow":{"steps":[{"value":9007199254740993}]},"variables":{"n":9223372036854775807}}}`
	req, err := decodeCreateJobRequest(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	before, err := jobOperationFingerprint(req)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{}
	frozen, err := srv.prepareJobRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	after, err := jobOperationFingerprint(frozen)
	if err != nil || before != after {
		t.Fatalf("queue changed durable request identity: %s %s %v", before, after, err)
	}
	n := frozen.Params["variables"].(map[string]interface{})["n"]
	if n != json.Number("9223372036854775807") {
		t.Fatalf("integer rounded during queue admission: %#v", n)
	}
	frozen.Params["variables"].(map[string]interface{})["n"] = json.Number("1")
	frozen.Params["workflow"].(map[string]interface{})["steps"].([]interface{})[0].(map[string]interface{})["value"] = "changed"
	unchanged, err := jobOperationFingerprint(req)
	if err != nil || unchanged != before {
		t.Fatal("queued request aliases caller maps or slices")
	}
	if frozen.Session != "outer" || frozen.Params["operation_id"] != "once" || frozen.Params["session"] != nil {
		t.Fatal("normalization changed submitted identity before the operation guard")
	}
}

func TestJobAdmissionHTTPRejectsBadEnvelopeWithoutJob(t *testing.T) {
	srv := NewHermeticServer("admission-test")
	defer srv.Stop()
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"type":"swarm_spawn","dry_rnu":true}`, `{"type":"swarm_spawn"} {}`,
		`{"type":"swarm_spawn","params":[]}`, `{"type":"swarm_spawn"} junk`,
		`{"type":"swarm_spawn","executionContext":{}}`,
	} {
		rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("accepted bad envelope %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(srv.jobStore.List()) != 0 || srv.jobExecutor.snapshot().Owned != 0 {
		t.Fatal("invalid envelope created queued work")
	}
}

func TestJobAdmissionHTTPStopClosesAdmission(t *testing.T) {
	srv := NewHermeticServer("admission-test")
	srv.Stop()
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", `{"type":"swarm_spawn","params":{"session":"late","cc_count":1}}`)
	if rec.Code != http.StatusServiceUnavailable || len(srv.jobStore.List()) != 0 || srv.jobExecutor.snapshot().Accepting {
		t.Fatalf("stopped server accepted work: %d %s", rec.Code, rec.Body.String())
	}
	srv.Stop() // Closing admission and draining are idempotent.
}

func TestJobAdmissionShutdownDeadlineWhileStoreLocked(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	srv.jobStore.mu.Lock()
	defer srv.jobStore.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- srv.drainJobWorkers(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("deadline lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("store lock defeated shutdown deadline")
	}
	if srv.jobExecutor.snapshot().Accepting {
		t.Fatal("shutdown did not close admission first")
	}
}

func TestJobAdmissionPreparationFailureReturnsCapacity(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	for i := 0; i < 10; i++ {
		job, err := srv.submitJob(context.Background(), CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{"bad": make(chan int)}})
		if job != nil || !errors.Is(err, errInvalidJobRequest) || !srv.jobExecutor.idle() {
			t.Fatalf("round %d leaked admission: %+v %v", i, job, err)
		}
	}
	if !reflect.DeepEqual(srv.jobStore.List(), []*Job{}) {
		t.Fatal("failed preparation created rows")
	}
}

func TestJobAdmissionConfigLimitsAreValidated(t *testing.T) {
	for _, limit := range []int{-1, maxJobConcurrency + 1} {
		if err := ValidateConfig(Config{JobConcurrency: limit}); err == nil {
			t.Fatalf("invalid concurrency %d accepted", limit)
		}
		srv := New(Config{JobConcurrency: limit})
		if err := srv.validate(); err == nil {
			t.Errorf("server start accepted invalid concurrency %d", limit)
		}
		srv.Stop()
	}
	for _, limit := range []int{-1, maxJobQueueCapacity + 1} {
		if err := ValidateConfig(Config{JobQueueCapacity: limit}); err == nil {
			t.Fatalf("invalid queue capacity %d accepted", limit)
		}
		srv := New(Config{JobQueueCapacity: limit})
		if err := srv.validate(); err == nil {
			t.Errorf("server start accepted invalid queue capacity %d", limit)
		}
		srv.Stop()
	}
	for _, limit := range []int{0, 1, maxJobConcurrency} {
		srv := New(Config{JobConcurrency: limit, JobQueueCapacity: 1})
		want := limit
		if want == 0 {
			want = DefaultJobConcurrency
		}
		if got := srv.jobExecutor.snapshot(); got.MaxConcurrent != want || got.QueueCapacity != 1 {
			t.Errorf("configuration not wired: %s", fmt.Sprint(got))
		}
		srv.Stop()
	}
}

func TestJobAdmissionCancellationReceiptSurvivesConcurrentEviction(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.jobStore.SetCancel(job.ID, func() {
		// Model completion releasing its handle just as another admission
		// evicts the now-terminal row. The HTTP reply still needs its receipt.
		srv.jobStore.ClearCancel(job.ID)
		srv.jobStore.mu.Lock()
		srv.jobStore.evictTerminalLocked(0)
		srv.jobStore.mu.Unlock()
	})
	got, err := srv.cancelJob(job.ID)
	if err != nil || got == nil || got.ID != job.ID || got.Status != JobStatusCancelled || got.Error != "cancelled by user" {
		t.Fatalf("cancellation reply lost its evicted receipt: %+v %v", got, err)
	}
}

func TestJobAdmissionPipelineProjectPinnedBeforeDispatch(t *testing.T) {
	projectA, projectB := t.TempDir(), t.TempDir()
	for _, kind := range []string{JobTypePipelineRun, JobTypePipelineExec, JobTypePipelineResume} {
		t.Run(kind, func(t *testing.T) {
			srv := NewHermeticServer("pin-test")
			defer srv.Stop()
			srv.projectDir = projectA
			req := CreateJobRequest{Type: kind, Params: map[string]interface{}{"operation_id": "same-request"}}
			fingerprint, err := jobOperationFingerprint(req)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := srv.prepareJobRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			srv.mu.Lock()
			srv.projectDir = projectB
			srv.mu.Unlock()
			view := srv.jobExecutionServer(frozen)
			if view == srv || view.pipelineProjectDir() != projectA || srv.pipelineProjectDir() != projectB || view.wsHub != srv.wsHub {
				t.Fatalf("queued %s followed mutable server selection or lost event publisher", kind)
			}
			if view.stateStore != nil || view.jobStore != nil {
				t.Fatal("execution view copied ownership state instead of retaining the original context")
			}
			got, err := jobOperationFingerprint(frozen)
			if err != nil || fingerprint != got {
				t.Fatal("admission project changed public operation identity")
			}
		})
	}
}

func TestJobAdmissionHTTPPendingReceiptKeepsSelectedProject(t *testing.T) {
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
	projectA, projectB := t.TempDir(), t.TempDir()
	srv.projectDir = projectA
	started, release, _ := admissionBlockSpawn(srv)
	defer release()
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"active","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := admissionAcceptedJob(t, srv, `{"type":"pipeline_run","params":{"session":"waiting","workflow_file":"work.yaml"}}`)
	if queued.ProjectDir != projectA {
		t.Fatalf("202 omitted admission namespace: %+v", queued)
	}
	srv.mu.Lock()
	srv.projectDir = projectB
	srv.mu.Unlock()
	dir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := (&jobJournal{dir: dir}).load()
	if err != nil {
		t.Fatal(err)
	}
	var saved *Job
	for _, job := range jobs {
		if job.ID == queued.ID {
			saved = job
		}
	}
	if saved == nil || saved.ProjectDir != projectA || saved.Status != JobStatusPending {
		t.Fatalf("queued namespace was not durable: %+v", saved)
	}
	rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
}

func TestJobAdmissionNonPipelineKeepsExistingProjectResolution(t *testing.T) {
	srv := NewHermeticServer("pin-test")
	defer srv.Stop()
	for _, kind := range []string{JobTypeSwarmSpawn, JobTypeCheckpointRestore} {
		req := CreateJobRequest{Type: kind, Params: map[string]interface{}{"working_dir": "custom-project"}}
		frozen, err := srv.prepareJobRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.executionProjectDir != "" || srv.jobExecutionServer(frozen) != srv || frozen.Params["working_dir"] != "custom-project" {
			t.Fatalf("pipeline pin changed %s resolution", kind)
		}
	}
}

