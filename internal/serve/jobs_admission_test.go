package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
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
	// Read the handle the way cancelJob and drainJobWorkers do; cancelJob
	// itself refuses this terminal row.
	store.mu.RLock()
	handle := store.cancels[job.ID]
	store.mu.RUnlock()
	if handle == nil {
		t.Fatal("retention removed the real cancellation handle")
	}
	handle()
	if ctx.Err() == nil {
		t.Fatal("retention kept a handle that is not the worker's real cancel func")
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
			t.Errorf("invalid queue capacity %d accepted", limit)
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

func TestJobAdmissionCheckpointKeepsExistingProjectResolution(t *testing.T) {
	srv := NewHermeticServer("pin-test")
	defer srv.Stop()
	for _, kind := range []string{JobTypeCheckpointRestore} {
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

func TestJobAdmissionSwarmProjectPinnedWithoutChangingRequest(t *testing.T) {
	projectA, projectB, override := t.TempDir(), t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name, workingDir, want string
		supplied               bool
	}{
		{name: "omitted", want: projectA},
		{name: "empty", supplied: true, want: projectA},
		{name: "relative", supplied: true, workingDir: "sub/project", want: filepath.Join(projectA, "sub/project")},
		{name: "parent", supplied: true, workingDir: "../other", want: filepath.Join(projectA, "../other")},
		{name: "absolute", supplied: true, workingDir: override, want: override},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{projectDir: projectA}
			req := CreateJobRequest{Type: JobTypeSwarmSpawn, Session: "fleet", Params: map[string]interface{}{
				"operation_id": "same-request", "cc_count": 2, "label": "lane", "dry_run": true,
			}}
			if tc.supplied {
				req.Params["working_dir"] = tc.workingDir
			}
			before, err := jobOperationFingerprint(req)
			if err != nil {
				t.Fatal(err)
			}
			original, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := srv.prepareJobRequest(req)
			if err != nil || frozen.executionProjectDir != tc.want {
				t.Fatalf("admitted project = %q, want %q: %v", frozen.executionProjectDir, tc.want, err)
			}
			after, err := jobOperationFingerprint(frozen)
			if err != nil || after != before {
				t.Fatal("private binding changed operation-ID fingerprint")
			}
			encoded, err := json.Marshal(frozen)
			if err != nil || string(encoded) != string(original) {
				t.Fatalf("admission rewrote the public request: %s, want %s: %v", encoded, original, err)
			}
			// Change both the server selection and the caller-owned request.
			srv.mu.Lock()
			srv.projectDir = projectB
			srv.mu.Unlock()
			req.Params["working_dir"] = projectB
			cause := errors.New("partially launched")
			partial := &robot.SpawnOutput{Session: "fleet--lane", WorkingDir: tc.want}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancel()
			var received robot.SpawnOptions
			calls := 0
			srv.spawnAgents = func(got context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				if got != ctx || !errors.Is(got.Err(), context.Canceled) {
					t.Fatal("execution view detached cancellation or reporter context")
				}
				received = opts
				return partial, cause
			}
			view := srv.jobExecutionServer(frozen)
			if view == srv || view.projectDir != tc.want || view.stateStore != nil || view.jobStore != nil || view.spawnAgents == nil {
				t.Fatal("execution view lost its namespace/service or copied ownership")
			}
			opts := robot.SpawnOptions{Session: "fleet", Label: "lane", WorkingDir: tc.workingDir,
				CCCount: 2, DryRun: true, Safety: true, AssignWork: true, RequireReservation: true,
				LifecycleDeps: &robot.SpawnLifecycleDependencies{},
			}
			result, err := view.spawnAgents(ctx, opts)
			want := opts
			want.WorkingDir = tc.want
			if calls != 1 || result != partial || err != cause || !reflect.DeepEqual(received, want) || opts.WorkingDir != tc.workingDir {
				t.Fatalf("binding changed controls, receipt or error: %+v, %v", received, err)
			}
		})
	}
}

func TestJobAdmissionSwarmMissingServiceRemainsUnavailable(t *testing.T) {
	srv := &Server{projectDir: t.TempDir()}
	frozen, err := srv.prepareJobRequest(CreateJobRequest{Type: JobTypeSwarmSpawn})
	if err != nil {
		t.Fatal(err)
	}
	if srv.jobExecutionServer(frozen).spawnAgents != nil {
		t.Fatal("execution view manufactured a launcher when the service is unavailable")
	}
}

func TestJobAdmissionSwarmRejectsMalformedDirectory(t *testing.T) {
	srv := &Server{projectDir: t.TempDir()}
	for _, value := range []interface{}{nil, 42, true, []string{}, map[string]interface{}{}} {
		_, err := srv.prepareJobRequest(CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{"working_dir": value}})
		if !errors.Is(err, errInvalidJobRequest) || !strings.Contains(err.Error(), "working_dir") {
			t.Fatalf("invalid directory %#v was accepted: %v", value, err)
		}
	}
}

func TestJobAdmissionSwarmProjectHTTPQueuedExecutionAndRecovery(t *testing.T) {
	for _, scenario := range []string{"default", "relative partial failure", "absolute preview", "queued cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
			srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 1, 1
			projectA, projectB, absolute := t.TempDir(), t.TempDir(), t.TempDir()
			srv.projectDir = projectA
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			launched := make(chan robot.SpawnOptions, 1)
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				out := &robot.SpawnOutput{Session: opts.Session, WorkingDir: opts.WorkingDir, DryRun: opts.DryRun}
				if opts.Session == "blocker" {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return out, ctx.Err()
					}
				} else {
					launched <- opts
					if scenario == "relative partial failure" {
						out.Agents = []robot.SpawnedAgent{{Pane: "0.1", Type: "claude"}}
						return out, errors.New("second launch failed")
					}
				}
				out.Success = true
				return out, nil
			}
			admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"blocker","cc_count":1}}`)
			awaitExecutorSignal(t, started)
			params := map[string]interface{}{"session": "fleet", "cc_count": 2}
			want := projectA
			if scenario == "relative partial failure" {
				params["working_dir"] = "child"
				want = filepath.Join(projectA, "child")
			}
			if scenario == "absolute preview" {
				params["working_dir"], params["dry_run"] = absolute, true
				want = absolute
			}
			body, err := json.Marshal(CreateJobRequest{Type: JobTypeSwarmSpawn, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			queued := admissionAcceptedJob(t, srv, string(body))
			if queued.ProjectDir != want || queued.Status != JobStatusPending {
				t.Fatalf("202 lost pending swarm namespace: %+v, want %q", queued, want)
			}
			srv.mu.Lock()
			srv.projectDir = projectB
			srv.mu.Unlock()
			dir, err := srv.jobJournalDir()
			if err != nil {
				t.Fatal(err)
			}
			assertSaved := func(status JobStatus) {
				t.Helper()
				jobs, err := (&jobJournal{dir: dir}).load()
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range jobs {
					if job.ID == queued.ID {
						if job.ProjectDir != want || job.Status != status {
							t.Fatalf("durable swarm namespace/state changed: %+v", job)
						}
						return
					}
				}
				t.Fatal("accepted swarm has no durable receipt")
			}
			assertSaved(JobStatusPending)
			if scenario == "queued cancellation" {
				rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
				if rec.Code != http.StatusOK {
					t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
				}
				assertSaved(JobStatusCancelled)
				select {
				case <-launched:
					t.Fatal("cancelled queued swarm reached the launch service")
				default:
				}
				return
			}
			unblock()
			final := pollJobTerminal(t, srv, queued.ID)
			wantStatus := JobStatusCompleted
			if scenario == "relative partial failure" {
				wantStatus = JobStatusFailed
				if !strings.Contains(final.Job.Error, "second launch failed") || final.Job.Result["agents"] == nil {
					t.Fatalf("project binding discarded partial recovery output: %+v", final.Job)
				}
			}
			if final.Job.Status != string(wantStatus) || final.Job.Result["working_dir"] != want || srv.jobStore.Get(queued.ID).ProjectDir != want {
				t.Fatalf("queued launch did not retain the admitted project: %+v", final.Job)
			}
			select {
			case opts := <-launched:
				if opts.WorkingDir != want || opts.DryRun != (scenario == "absolute preview") {
					t.Fatalf("launch service received the wrong namespace/options: %+v", opts)
				}
			default:
				t.Fatal("completed job never reached spawn")
			}
		})
	}
}

func TestJobAdmissionSwarmRejectsMalformedDirectoryWithoutAdmission(t *testing.T) {
	srv := NewHermeticServer("swarm-project-test")
	defer srv.Stop()
	for _, value := range []string{`null`, `42`, `true`, `[]`, `{}`} {
		rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs",
			fmt.Sprintf(`{"type":"swarm_spawn","params":{"session":"fleet","cc_count":1,"working_dir":%s}}`, value))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "working_dir") {
			t.Fatalf("invalid directory %s = %d %s", value, rec.Code, rec.Body.String())
		}
	}
	if len(srv.jobStore.List()) != 0 || srv.jobExecutor.snapshot().Owned != 0 {
		t.Fatal("invalid project selection consumed a job or capacity")
	}
}

func jobScopeHTTPPost(srv *Server, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func jobScopeHTTPReceipt(t *testing.T, rec *httptest.ResponseRecorder) *Job {
	t.Helper()
	var response struct {
		Job *Job `json:"job"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Job == nil || response.Job.ID == "" {
		t.Fatalf("scoped job was not accepted: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Location") != "/api/v1/jobs/"+response.Job.ID {
		t.Fatalf("scoped job lost its polling location: %v", rec.Header())
	}
	return response.Job
}

func jobScopeHTTPPatchProject(t *testing.T, srv *Server, project string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"project_dir": project})
	if err != nil {
		t.Fatal(err)
	}
	rec := admissionRequest(srv, http.MethodPatch, "/api/v1/config", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("project patch failed: %d %s", rec.Code, rec.Body)
	}
}

type jobScopeHTTPBodyBarrier struct {
	*strings.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *jobScopeHTTPBodyBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.Reader.Read(p)
}

func TestJobHTTPProjectScopePipelineAdmissionAndReplay(t *testing.T) {
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	projectA, projectB := t.TempDir(), t.TempDir()
	jobScopeHTTPPatchProject(t, srv, projectA)
	srv.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}
	body, err := json.Marshal(CreateJobRequest{Type: JobTypePipelineExec, Params: map[string]interface{}{
		"session": "scope-job", "workflow": pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "scope-job", Steps: []pipeline.Step{{
			ID: "effect", Command: "printf 'once\\n' >> effects; pwd",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	const key = "same-job-transport-key"
	barrier := &jobScopeHTTPBodyBarrier{Reader: strings.NewReader(string(body)), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer unblock()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", io.NopCloser(barrier))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Router().ServeHTTP(rec, req.WithContext(ctx))
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("job request never reached its body read")
	}
	jobScopeHTTPPatchProject(t, srv, projectB)
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job admission did not finish after the project switch")
	}
	first := jobScopeHTTPReceipt(t, rec)
	if first.ProjectDir != projectA || rec.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatalf("body admission redirected the job receipt: %+v %v", first, rec.Header())
	}
	original := pollJobTerminal(t, srv, first.ID)
	if original.Job.Status != string(JobStatusCompleted) {
		t.Fatalf("original inline workflow failed: %+v", original.Job)
	}
	runID, _ := original.Job.Result["run_id"].(string)
	state, err := pipeline.LoadState(projectA, runID)
	if err != nil || state.Status != pipeline.StatusCompleted || state.Steps["effect"].Output != projectA {
		t.Fatalf("admitted project and real execution diverged: %+v %v", state, err)
	}
	statePath := filepath.Join(projectA, ".ntm", "pipelines", runID+".json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectB, "effects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("A's admitted job executed in B: %v", err)
	}
	rec = jobScopeHTTPPost(srv, string(body), key)
	second := jobScopeHTTPReceipt(t, rec)
	if second.ID == first.ID || second.ProjectDir != projectB || rec.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatalf("B replayed A's job receipt: %+v %v", second, rec.Header())
	}
	other := pollJobTerminal(t, srv, second.ID)
	otherRun, _ := other.Job.Result["run_id"].(string)
	state, err = pipeline.LoadState(projectB, otherRun)
	if other.Job.Status != string(JobStatusCompleted) || err != nil || state.Steps["effect"].Output != projectB {
		t.Fatalf("B did not execute its own admitted job: %+v %+v %v", other.Job, state, err)
	}
	jobScopeHTTPPatchProject(t, srv, projectA)
	rec = jobScopeHTTPPost(srv, string(body), key)
	replay := jobScopeHTTPReceipt(t, rec)
	if replay.ID != first.ID || rec.Header().Get("X-Idempotent-Replay") != "true" || len(srv.jobStore.List()) != 2 {
		t.Fatalf("returning to A admitted duplicate work: %+v %v", replay, rec.Header())
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP receipt replay rewrote the original execution")
	}
	for _, project := range []string{projectA, projectB} {
		if data, err := os.ReadFile(filepath.Join(project, "effects")); err != nil || string(data) != "once\n" {
			t.Fatalf("project-scoped job replay repeated effects: %q %v", data, err)
		}
	}
}

func TestJobHTTPProjectScopeSwarmExplicitAndRelativeTargets(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprintf("absolute=%v", absolute), func(t *testing.T) {
			srv := NewHermeticServer("job-scope")
			defer srv.Stop()
			projectA, projectB := t.TempDir(), t.TempDir()
			jobScopeHTTPPatchProject(t, srv, projectA)
			directory := "child"
			if absolute {
				directory = t.TempDir()
			}
			var calls atomic.Int32
			srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				out := &robot.SpawnOutput{Session: opts.Session, WorkingDir: opts.WorkingDir}
				out.Success = true
				return out, nil
			}
			body, err := json.Marshal(CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{
				"session": "scoped-swarm", "cc_count": 1, "working_dir": directory,
			}})
			if err != nil {
				t.Fatal(err)
			}
			const key = "same-swarm-transport-key"
			first := jobScopeHTTPReceipt(t, jobScopeHTTPPost(srv, string(body), key))
			want := filepath.Join(projectA, directory)
			if absolute {
				want = directory
			}
			original := pollJobTerminal(t, srv, first.ID)
			if first.ProjectDir != want || original.Job.Status != string(JobStatusCompleted) || original.Job.Result["working_dir"] != want || calls.Load() != 1 {
				t.Fatalf("swarm lost its admitted destination: %+v %+v calls=%d", first, original.Job, calls.Load())
			}
			jobScopeHTTPPatchProject(t, srv, projectB)
			rec := jobScopeHTTPPost(srv, string(body), key)
			second := jobScopeHTTPReceipt(t, rec)
			if absolute {
				if second.ID != first.ID || rec.Header().Get("X-Idempotent-Replay") != "true" || calls.Load() != 1 || len(srv.jobStore.List()) != 1 {
					t.Fatalf("default-project change repeated explicit-target swarm: %+v %v calls=%d", second, rec.Header(), calls.Load())
				}
			} else {
				want = filepath.Join(projectB, directory)
				other := pollJobTerminal(t, srv, second.ID)
				if second.ID == first.ID || rec.Header().Get("X-Idempotent-Replay") != "" || second.ProjectDir != want || other.Job.Result["working_dir"] != want || other.Job.Status != string(JobStatusCompleted) || calls.Load() != 2 {
					t.Fatalf("relative swarm replay escaped B's destination: %+v %+v calls=%d", second, other.Job, calls.Load())
				}
			}
			jobScopeHTTPPatchProject(t, srv, projectA)
			rec = jobScopeHTTPPost(srv, string(body), key)
			if replay := jobScopeHTTPReceipt(t, rec); replay.ID != first.ID || rec.Header().Get("X-Idempotent-Replay") != "true" {
				t.Fatalf("original swarm lost its replay: %+v %v", replay, rec.Header())
			}
		})
	}
}

func TestJobHTTPProjectScopeKeepsCheckpointReplayGlobal(t *testing.T) {
	logPath := installFakeTmux(t)
	params := saveJobRestoreFixture(t, 1)
	srv := NewHermeticServer("job-scope")
	defer srv.Stop()
	projectA, projectB := t.TempDir(), t.TempDir()
	jobScopeHTTPPatchProject(t, srv, projectA)
	body, err := json.Marshal(CreateJobRequest{Type: JobTypeCheckpointRestore, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	const key = "same-restore-transport-key"
	first := jobScopeHTTPReceipt(t, jobScopeHTTPPost(srv, string(body), key))
	original := pollJobTerminal(t, srv, first.ID)
	if original.Job.Status != string(JobStatusCompleted) || original.Job.Result["panes_restored"] != float64(1) || first.ProjectDir != "" {
		t.Fatalf("global checkpoint job did not restore its own saved target: %+v %+v", first, original.Job)
	}
	before, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(before), "new-session") != 1 {
		t.Fatalf("checkpoint fixture did not restore exactly one session: %s %v", before, err)
	}
	jobScopeHTTPPatchProject(t, srv, projectB)
	rec := jobScopeHTTPPost(srv, string(body), key)
	replay := jobScopeHTTPReceipt(t, rec)
	if replay.ID != first.ID || rec.Header().Get("X-Idempotent-Replay") != "true" || len(srv.jobStore.List()) != 1 {
		t.Fatalf("default-project change admitted another global restore: %+v %v", replay, rec.Header())
	}
	if after, err := os.ReadFile(logPath); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("global restore replay touched the target session again: %s %v", after, err)
	}
}

func TestJobHTTPProjectScopeHashesBodyBeyondFormerPrefixLimit(t *testing.T) {
	srv := NewHermeticServer("job-scope")
	defer srv.Stop()
	jobScopeHTTPPatchProject(t, srv, t.TempDir())
	var calls atomic.Int32
	srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		calls.Add(1)
		out := &robot.SpawnOutput{Session: opts.Session}
		out.Success = true
		return out, nil
	}
	// Valid JSON whitespace exceeds the old 8 MiB prefix hash without putting
	// a large value into commands, environment variables, or durable receipts.
	prefix := strings.Repeat(" ", (8<<20)+1)
	const key = "same-prefix-different-job"
	first := jobScopeHTTPReceipt(t, jobScopeHTTPPost(srv, prefix+`{"type":"swarm_spawn","params":{"session":"original","cc_count":1}}`, key))
	original := pollJobTerminal(t, srv, first.ID)
	if original.Job.Status != string(JobStatusCompleted) || original.Job.Result["session"] != "original" || calls.Load() != 1 {
		t.Fatalf("large valid request did not execute once: %+v calls=%d", original.Job, calls.Load())
	}
	rec := jobScopeHTTPPost(srv, prefix+`{"type":"swarm_spawn","params":{"session":"changed","cc_count":1}}`, key)
	var refused struct {
		ErrorCode string `json:"error_code"`
	}
	if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &refused) != nil || refused.ErrorCode != ErrCodeIdempotentReplay || rec.Header().Get("X-Idempotent-Replay") != "" || calls.Load() != 1 || len(srv.jobStore.List()) != 1 {
		t.Fatalf("late-body change reused an unrelated job: %d %s calls=%d", rec.Code, rec.Body, calls.Load())
	}
}
