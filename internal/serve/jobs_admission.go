package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

var (
	errInvalidJobRequest = errors.New("invalid job request")
	errJobNotFound       = errors.New("job not found")
	errJobNotCancellable = errors.New("job cannot be cancelled")
)

const jobHTTPMaxBodyBytes = maxRequestBodyBytes

type jobProjectSelectionKey struct{}

// Capture inputs to project resolution before reading a request. An absolute
// swarm destination and global checkpoint work do not depend on cwd, so defer
// any cwd error until a relative destination actually needs it.
type jobProjectSelection struct {
	projectDir string
	cwd        string
	cwdErr     error
}

func (s *Server) captureJobProjectSelection() jobProjectSelection {
	selection := jobProjectSelection{projectDir: s.projectDirSnapshot()}
	selection.cwd, selection.cwdErr = os.Getwd()
	return selection
}

func (selection jobProjectSelection) absolute(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if selection.cwdErr != nil {
		return "", selection.cwdErr
	}
	return filepath.Join(selection.cwd, path), nil
}

func isCreateJobRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/api/v1/jobs" || r.URL.Path == "/api/v1/jobs/")
}

func (s *Server) jobProjectSelectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCreateJobRequest(r) {
			ctx := context.WithValue(r.Context(), jobProjectSelectionKey{}, s.captureJobProjectSelection())
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

type jobHTTPReplayScopeKey struct{}

type jobHTTPReplayScope struct {
	request     CreateJobRequest
	requestErr  error
	err         error
	project     string
	fingerprint string
}

// Decode the post-redaction body with admission's canonical decoder and bind
// its project using the same resolver. No job is admitted here. Invalid input
// disables cache replay and remains subject to the route's permission check.
// The handler consumes this exact decoded request, including read failures;
// partial valid JSON must never hide a transport error or exceed the size cap.
func (s *Server) jobHTTPReplayScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCreateJobRequest(r) || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
			next.ServeHTTP(w, r)
			return
		}
		var scope jobHTTPReplayScope
		var data []byte
		scope.requestErr = r.Context().Err()
		if scope.requestErr == nil && r.Body != nil {
			data, scope.requestErr = io.ReadAll(io.LimitReader(r.Body, jobHTTPMaxBodyBytes+1))
		}
		if r.Body != nil {
			_ = r.Body.Close()
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		if scope.requestErr == nil && len(data) > jobHTTPMaxBodyBytes {
			scope.requestErr = &http.MaxBytesError{Limit: jobHTTPMaxBodyBytes}
		}
		if scope.requestErr == nil {
			scope.requestErr = r.Context().Err()
		}
		if scope.requestErr == nil {
			digest := sha256.Sum256(data)
			scope.fingerprint = hex.EncodeToString(digest[:])
			scope.request, scope.requestErr = decodeCreateJobRequest(bytes.NewReader(data))
		}
		scope.err = scope.requestErr
		if scope.err == nil && !isImplementedJobType(scope.request.Type) {
			scope.err = errors.New("job type is not implemented")
		}
		if scope.err == nil {
			selection, captured := r.Context().Value(jobProjectSelectionKey{}).(jobProjectSelection)
			if !captured {
				scope.err = errors.New("job project selection was not captured before reading the request")
				scope.requestErr = scope.err
			} else {
				var bound CreateJobRequest
				bound, scope.err = bindJobExecutionProject(scope.request, selection)
				if scope.err == nil {
					scope.project, scope.err = jobOperationScope(bound)
				}
			}
		}
		ctx := context.WithValue(r.Context(), jobHTTPReplayScopeKey{}, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// decodeCreateJobRequest rejects unknown envelope fields and extra JSON values.
// UseNumber prevents rounding integer workflow variables in the untyped params
// map before they reach the operation's strict decoder or durable fingerprint.
func decodeCreateJobRequest(body io.Reader) (CreateJobRequest, error) {
	var req CreateJobRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		return req, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return req, errors.New("request body must contain exactly one JSON object")
	}
	return req, nil
}

// prepareJobRequest detaches queued maps/slices from the caller. It preserves
// the original public request shape, including top-level session and operation
// identity: the existing dispatcher remains responsible for normalization and
// per-operation validation. No executable request is saved in the job journal.
func (s *Server) prepareJobRequest(req CreateJobRequest) (CreateJobRequest, error) {
	return prepareJobRequestAtSelection(req, s.captureJobProjectSelection())
}

func prepareJobRequestAtSelection(req CreateJobRequest, selection jobProjectSelection) (CreateJobRequest, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return req, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
	}
	frozen, err := decodeCreateJobRequest(bytes.NewReader(raw))
	if err != nil {
		return req, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
	}
	return bindJobExecutionProject(frozen, selection)
}

// Only private execution identity is changed. HTTP replay classification and
// actual admission use this same function against the same captured selection.
func bindJobExecutionProject(frozen CreateJobRequest, selection jobProjectSelection) (CreateJobRequest, error) {
	var err error
	// Queued project-scoped work must not follow PATCH /config to another
	// project. Freeze the absolute selection, not mutable workflow contents.
	// Keep this private so it cannot alter the operation-ID fingerprint.
	switch frozen.Type {
	case jobTypeBeadClose:
		if _, err := decodeBeadCloseJob(frozen.Params); err != nil {
			return frozen, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
		}
		if frozen.Session != "" {
			return frozen, fmt.Errorf("%w: bead close does not accept a session", errInvalidJobRequest)
		}
		frozen.executionProjectDir, err = selection.absolute(selection.projectDir)
		if err != nil {
			return frozen, fmt.Errorf("%w: resolve bead project: %v", errInvalidJobRequest, err)
		}
	case JobTypePipelineRun, JobTypePipelineExec, JobTypePipelineResume:
		projectDir := selection.projectDir
		if strings.TrimSpace(projectDir) == "" {
			projectDir = selection.cwd
		}
		frozen.executionProjectDir, err = selection.absolute(projectDir)
		if err != nil {
			return frozen, fmt.Errorf("%w: resolve job project: %v", errInvalidJobRequest, err)
		}
	case JobTypeSwarmSpawn:
		// The HTTP server's selected project is the default for accepted
		// swarms, not the process CWD. Resolve relative overrides against that
		// selection now, before queuing; never rewrite the public params.
		workingDir := ""
		if value, supplied := frozen.Params["working_dir"]; supplied {
			var ok bool
			workingDir, ok = value.(string)
			if !ok {
				return frozen, fmt.Errorf("%w: working_dir must be a string", errInvalidJobRequest)
			}
		}
		if !filepath.IsAbs(workingDir) {
			workingDir = filepath.Join(selection.projectDir, workingDir)
		}
		frozen.executionProjectDir, err = selection.absolute(workingDir)
		if err != nil {
			return frozen, fmt.Errorf("%w: resolve swarm project: %v", errInvalidJobRequest, err)
		}
	}
	return frozen, nil
}

// jobExecutionServer selects the admitted project namespace without copying
// live mutexes or rereading mutable server configuration. Operation ownership
// and journal callbacks stay on the original server/context; only the existing
// project-scoped engine and its event publisher use this execution view.
func (s *Server) jobExecutionServer(req CreateJobRequest) *Server {
	if req.executionProjectDir == "" {
		return s
	}
	view := &Server{projectDir: req.executionProjectDir, wsHub: s.wsHub}
	if req.Type == JobTypeSwarmSpawn && s.spawnAgents != nil {
		// Keep the same spawn service and execution context (including
		// cancellation, progress and operation ownership). Only its admitted
		// directory is authoritative; neither config changes nor a caller's
		// relative path may redirect the queued launch at dispatch time.
		launch, project := s.spawnAgents, req.executionProjectDir
		view.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
			opts.WorkingDir = project
			return launch(ctx, opts)
		}
	}
	return view
}

// submitJob is the shared admission path for managed async jobs. Capacity is
// reserved before preparation and the pending receipt is flushed before 202 or
// dispatch. Execution deadlines start at dispatch, not while waiting in queue.
func (s *Server) submitJob(ctx context.Context, req CreateJobRequest) (*Job, error) {
	var receipt *Job
	err := s.jobExecutor.submit(ctx, func(owner context.Context) (*scheduledJob, error) {
		var frozen CreateJobRequest
		var err error
		if selection, captured := ctx.Value(jobProjectSelectionKey{}).(jobProjectSelection); captured {
			frozen, err = prepareJobRequestAtSelection(req, selection)
		} else {
			frozen, err = s.prepareJobRequest(req)
		}
		if err != nil {
			return nil, err
		}
		// Use the same top-level/params session binding as execution. Choosing
		// a different precedence here would lock one session and mutate another.
		params, err := jobExecutionParams(frozen)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
		}
		resources := jobExecutionResources(frozen.Type, params)
		if frozen.Type == jobTypeBeadClose {
			// Hold ownership across show/close/reconciliation for one bead.
			// Unrelated sessions must not wait on a global sessionless barrier.
			resources = []string{"bead:" + frozen.executionProjectDir + ":" + params["bead_id"].(string)}
		}
		var resumeTarget *jobResumeTarget
		if frozen.Type == JobTypePipelineResume {
			resumeTarget = resolveJobResumeTarget(frozen.executionProjectDir, params, loadJobResumeSession)
			if resumeTarget != nil {
				frozen.executionProjectDir = resumeTarget.ProjectDir
				resources = resumeTarget.resources()
			}
		}
		if err := owner.Err(); err != nil {
			return nil, errJobAdmissionClosed
		}
		jobCtx, cancel := context.WithCancel(owner)
		if resumeTarget != nil {
			jobCtx = context.WithValue(jobCtx, jobResumeTargetKey{}, *resumeTarget)
		}
		if frozen.Type == JobTypeCheckpointRestore {
			// The observer receives the eventual execution context, including
			// the worker and operation-ID journal reporters installed at dispatch.
			jobCtx = checkpoint.WithRestoreProgress(jobCtx, restoreJobProgressObserver())
		}
		ready := false
		defer func() {
			if !ready {
				cancel()
				if receipt != nil {
					s.discardQueuedJob(receipt.ID)
				}
			}
		}()
		receipt = s.jobStore.Create(frozen.Type, jobOwnership{cancel: cancel, projectDir: frozen.executionProjectDir})
		if frozen.Type == jobTypeBeadClose {
			// Persist target identity, not an executable request. A queued or
			// interrupted close must remain inspectable after a server restart.
			s.jobStore.Update(receipt.ID, JobStatusPending, 0, map[string]interface{}{
				"bead_id": params["bead_id"], "project_dir": frozen.executionProjectDir,
			}, "")
			receipt = s.jobStore.Get(receipt.ID)
		}
		frozen.executionContext = jobCtx
		if err := s.persistJobHistory(receipt.ID); err != nil {
			s.jobStore.Update(receipt.ID, JobStatusFailed, 0, receipt.Result,
				fmt.Sprintf("checkpoint job before acceptance: %v", err))
			return nil, fmt.Errorf("checkpoint job before acceptance: %w", err)
		}
		id := receipt.ID
		work := &scheduledJob{
			id: id, ctx: jobCtx, cancel: cancel, resources: resources,
			run:     func() { s.dispatchJob(id, frozen) },
			discard: func() { s.discardQueuedJob(id) },
		}
		ready = true
		return work, nil
	})
	if err != nil && receipt != nil {
		receipt = s.jobStore.Get(receipt.ID)
	}
	return receipt, err
}

// discardQueuedJob finalizes accepted work that never reached an engine.
// Ownership survives through the final write, even if DELETE already made the
// row terminal. Do not overwrite the operator's cancellation reason or progress.
func (s *Server) discardQueuedJob(id string) {
	defer s.jobStore.ClearCancel(id)
	defer func() {
		if value := recover(); value != nil {
			s.jobStore.Update(id, JobStatusFailed, 0, nil, fmt.Sprintf("finalize queued job: %v", value))
		}
		if err := s.persistJobHistory(id); err != nil {
			s.recordJobJournalError(id, err)
		}
	}()
	var result map[string]interface{}
	if job := s.jobStore.Get(id); job != nil {
		result = job.Result
	}
	s.jobStore.Update(id, JobStatusCancelled, 0, result, "job cancelled before execution")
}

// cancelJob transitions atomically against completion. Signal the worker even
// if persistence fails, but do not acknowledge durable cancellation on a failed
// write. Pending cancellation frees capacity only after finalizing its receipt.
func (s *Server) cancelJob(id string) (*Job, error) {
	s.jobStore.mu.Lock()
	job := s.jobStore.jobs[id]
	if job == nil {
		s.jobStore.mu.Unlock()
		return nil, errJobNotFound
	}
	if job.Status != JobStatusPending && job.Status != JobStatusRunning {
		copy := s.jobStore.cloneJob(job)
		s.jobStore.mu.Unlock()
		return copy, errJobNotCancellable
	}
	job.Status = JobStatusCancelled
	job.Error = "cancelled by user"
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	cancel := s.jobStore.cancels[id]
	cancelled := s.jobStore.cloneJob(job)
	s.jobStore.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := s.persistJobHistory(id)
	s.jobExecutor.cancelQueued(id)
	// Once finalization releases ownership, another admission may evict this
	// terminal row. The cancellation response must still identify its outcome.
	if latest := s.jobStore.Get(id); latest != nil {
		cancelled = latest
	}
	return cancelled, err
}

// jobOwnership installs the cancellation handle atomically with a pending row,
// so retention and shutdown never observe an accepted but unowned job.
type jobOwnership struct {
	cancel     context.CancelFunc
	projectDir string
}
