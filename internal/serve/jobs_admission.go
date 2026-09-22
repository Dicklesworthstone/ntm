package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

var (
	errInvalidJobRequest = errors.New("invalid job request")
	errJobNotFound       = errors.New("job not found")
	errJobNotCancellable = errors.New("job cannot be cancelled")
)

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
	raw, err := json.Marshal(req)
	if err != nil {
		return req, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
	}
	frozen, err := decodeCreateJobRequest(bytes.NewReader(raw))
	if err != nil {
		return req, fmt.Errorf("%w: %v", errInvalidJobRequest, err)
	}
	// A queued pipeline must not follow a later PATCH /config to another
	// project. Freeze the absolute selection, not mutable workflow contents.
	// Keep this private so it cannot alter the operation-ID fingerprint.
	switch frozen.Type {
	case JobTypePipelineRun, JobTypePipelineExec, JobTypePipelineResume:
		frozen.executionProjectDir, err = filepath.Abs(s.pipelineProjectDir())
		if err != nil {
			return req, fmt.Errorf("%w: resolve job project: %v", errInvalidJobRequest, err)
		}
	}
	return frozen, nil
}

// jobExecutionServer selects the admitted pipeline namespace without copying
// live mutexes or rereading mutable server configuration. Operation ownership
// and journal callbacks stay on the original server/context; only the existing
// pipeline engine and its event publisher use this execution view.
func (s *Server) jobExecutionServer(req CreateJobRequest) *Server {
	if req.executionProjectDir == "" {
		return s
	}
	return &Server{projectDir: req.executionProjectDir, wsHub: s.wsHub}
}

// submitJob is the only admission path for generic async jobs. Capacity is
// reserved before preparation and the pending receipt is flushed before 202 or
// dispatch. Execution deadlines start at dispatch, not while waiting in queue.
func (s *Server) submitJob(ctx context.Context, req CreateJobRequest) (*Job, error) {
	var receipt *Job
	err := s.jobExecutor.submit(ctx, func(owner context.Context) (*scheduledJob, error) {
		frozen, err := s.prepareJobRequest(req)
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
		if err := owner.Err(); err != nil {
			return nil, errJobAdmissionClosed
		}
		jobCtx, cancel := context.WithCancel(owner)
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
		frozen.executionContext = jobCtx
		if err := s.persistJobHistory(receipt.ID); err != nil {
			s.jobStore.Update(receipt.ID, JobStatusFailed, 0, nil,
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
	s.jobStore.Update(id, JobStatusCancelled, 0, nil, "job cancelled before execution")
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
