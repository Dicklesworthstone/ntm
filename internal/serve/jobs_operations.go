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
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const jobOperationVersion = 1

// Job operation IDs identify work, not HTTP requests. The transport may create
// another job after a lost response or restart; this receipt prevents that job
// from executing the work again. Never expire an uncertain execution marker.
// Request bodies (including inline commands and secrets) are hashed, not saved.
type jobOperationReceipt struct {
	Version      int                    `json:"version"`
	OperationKey string                 `json:"operation_key"`
	Fingerprint  string                 `json:"fingerprint"`
	JobID        string                 `json:"job_id"`
	Type         string                 `json:"type"`
	Status       JobStatus              `json:"status"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
	Result       map[string]interface{} `json:"result,omitempty"`
	Error        string                 `json:"error,omitempty"`
	ErrorKind    string                 `json:"error_kind,omitempty"`
}

// executeJobOperation consumes only the common operation_id control. Every
// remaining parameter still passes through the operation's strict decoder.
func (s *Server) executeJobOperation(ctx context.Context, jobID string, req CreateJobRequest) (map[string]interface{}, error) {
	operationID, request, err := splitJobOperationRequest(req)
	if err != nil {
		return nil, err
	}
	if operationID == "" {
		return s.executeJobRequest(ctx, request)
	}
	dir, err := s.jobJournalDir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errors.New("operation_id requires a persistent job journal; refusing non-durable execution")
	}
	return runJobOperation(ctx, dir, operationID, jobID, request, s.executeJobRequest)
}

func splitJobOperationRequest(req CreateJobRequest) (string, CreateJobRequest, error) {
	raw, present := req.Params["operation_id"]
	if !present {
		return "", req, nil
	}
	id, ok := raw.(string)
	if !ok || id == "" || len(id) > 200 || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", req, errors.New("operation_id must be a non-empty string of at most 200 bytes without whitespace or control characters")
	}
	params := make(map[string]interface{}, len(req.Params)-1)
	for key, value := range req.Params {
		if key != "operation_id" {
			params[key] = value
		}
	}
	req.Params = params
	return id, req, nil
}

// jobExecutionParams binds the public job envelope's session to the engine's
// request. Two explicit targets must agree exactly; silently preferring either
// can run a pipeline or restore against the wrong session. The original request
// remains untouched for durable fingerprinting and caller-owned map safety.
func jobExecutionParams(req CreateJobRequest) (map[string]interface{}, error) {
	if req.Session == "" {
		return req.Params, nil // Resume may intentionally retain its saved session.
	}
	if strings.TrimSpace(req.Session) == "" {
		return nil, errors.New("job session must not be whitespace")
	}
	if raw, present := req.Params["session"]; present {
		session, ok := raw.(string)
		if !ok {
			return nil, errors.New("params.session must be a string when job session is specified")
		}
		if session != req.Session {
			return nil, fmt.Errorf("job session conflict: top-level session %q does not match params.session %q", req.Session, session)
		}
	}
	params := make(map[string]interface{}, len(req.Params)+1)
	for key, value := range req.Params {
		params[key] = value
	}
	params["session"] = req.Session
	return params, nil
}

func jobOperationFingerprint(req CreateJobRequest) (string, error) {
	// encoding/json orders map keys. The complete parameter set is covered,
	// not an 8 MiB prefix; changes late in a large inline workflow conflict.
	// Hash the actual request type so future top-level execution controls
	// cannot accidentally be omitted from operation identity.
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("fingerprint job operation: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func jobOperationPath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, "operations", hex.EncodeToString(sum[:])+".json")
}

// runJobOperation holds a cross-process fence through the final receipt save.
// It records an uncertain marker BEFORE invoking the engine. A process crash,
// panic, or failed final save leaves that marker in place and forbids replay
// of side effects. A completed receipt replays data, never executable params.
func runJobOperation(ctx context.Context, dir, id, jobID string, req CreateJobRequest,
	run func(context.Context, CreateJobRequest) (map[string]interface{}, error),
) (map[string]interface{}, error) {
	if ctx == nil {
		return nil, errors.New("job operation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fingerprint, err := jobOperationFingerprint(req)
	if err != nil {
		return nil, err
	}
	path := jobOperationPath(dir, id)
	directory := filepath.Dir(path)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create operation journal: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("operation journal must be a real directory: %s", directory)
	}
	// Sync even if another worker created the directory. Otherwise a crash
	// can lose its parent entry after the receipt itself was successfully
	// synced, making a previously executed operation appear unseen.
	if err := syncJobJournalDirectory(dir); err != nil {
		return nil, fmt.Errorf("persist operation journal directory: %w", err)
	}
	lockPath := strings.TrimSuffix(path, ".json") + ".lock"
	if err := checkJobOperationFile(lockPath); err != nil {
		return nil, err
	}
	release, err := lockJobJournal(lockPath)
	if err != nil {
		// An atomic receipt can be read without owning its writer's fence.
		// Use it only to identify the active job, never to authorize execution.
		prior, _ := readJobOperation(path)
		if prior == nil || prior.Fingerprint != fingerprint {
			prior = &jobOperationReceipt{}
		}
		return jobOperationResult(prior.Result, id, prior.JobID, "in_progress", false), fmt.Errorf("job operation ownership unavailable; inspect the original job before retrying: %w", err)
	}
	defer release()
	prior, err := readJobOperation(path)
	if err != nil {
		return nil, fmt.Errorf("read operation receipt; refusing re-execution: %w", err)
	}
	if prior != nil {
		if prior.Fingerprint != fingerprint {
			return jobOperationResult(nil, id, prior.JobID, "conflict", false), errors.New("operation_id conflicts with a different job type or parameter set")
		}
		if prior.Status == JobStatusRunning {
			return jobOperationResult(prior.Result, id, prior.JobID, "outcome_unknown", false), errors.New("previous operation outcome is unknown; inspect the original job, sessions, and pipeline state before starting any new operation; work was not re-executed")
		}
		return jobOperationResult(prior.Result, id, prior.JobID, string(prior.Status), true), prior.executionError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	receipt := &jobOperationReceipt{
		Version: jobOperationVersion, Fingerprint: fingerprint, JobID: jobID,
		OperationKey: strings.TrimSuffix(filepath.Base(path), ".json"),
		Type:         req.Type, Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now,
	}
	if err := receipt.save(path); err != nil {
		return nil, fmt.Errorf("checkpoint operation before execution: %w", err)
	}
	var result map[string]interface{}
	runErr := ctx.Err()
	if runErr == nil {
		result, runErr = run(ctx, req)
	}
	if ctx.Err() != nil {
		runErr = errors.Join(runErr, ctx.Err())
	}
	receipt.Result = result
	receipt.Status = JobStatusCompleted
	if runErr != nil {
		receipt.Status = JobStatusFailed
		receipt.Error = runErr.Error()
		if errors.Is(runErr, context.Canceled) {
			receipt.Status = JobStatusCancelled
			receipt.ErrorKind = "cancelled"
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			receipt.ErrorKind = "deadline"
		}
	}
	receipt.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := receipt.save(path); err != nil {
		return jobOperationResult(result, id, jobID, "outcome_unknown", false), errors.Join(runErr, fmt.Errorf("checkpoint operation outcome: %w", err))
	}
	return jobOperationResult(result, id, jobID, string(receipt.Status), false), runErr
}

func checkJobOperationFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("operation journal entry must be a regular file: %s", path)
	}
	return nil
}

func readJobOperation(path string) (*jobOperationReceipt, error) {
	if err := checkJobOperationFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, jobJournalMaxRecordBytes+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > jobJournalMaxRecordBytes {
		return nil, errors.New("operation receipt exceeds record limit")
	}
	var receipt jobOperationReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("operation receipt contains trailing data")
	}
	if receipt.Version != jobOperationVersion || receipt.JobID == "" || receipt.Type == "" || len(receipt.Fingerprint) != sha256.Size*2 ||
		receipt.OperationKey != strings.TrimSuffix(filepath.Base(path), ".json") {
		return nil, errors.New("operation receipt has invalid version or identity")
	}
	switch receipt.Status {
	case JobStatusRunning, JobStatusCompleted:
		if receipt.Error != "" || receipt.ErrorKind != "" {
			return nil, errors.New("operation receipt has inconsistent outcome")
		}
	case JobStatusFailed, JobStatusCancelled:
		if receipt.Error == "" || (receipt.ErrorKind != "" && receipt.ErrorKind != "cancelled" && receipt.ErrorKind != "deadline") ||
			(receipt.Status == JobStatusCancelled) != (receipt.ErrorKind == "cancelled") {
			return nil, errors.New("operation receipt has inconsistent error")
		}
	default:
		return nil, fmt.Errorf("operation receipt has unknown status %q", receipt.Status)
	}
	return &receipt, nil
}

func (r *jobOperationReceipt) save(path string) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeJobJournalRecord(filepath.Dir(path), path, data)
}

// jobOperationError preserves cancellation/deadline identity across JSON
// persistence without changing the original error's diagnostic text.
type jobOperationError struct {
	message string
	cause   error
}

func (e *jobOperationError) Error() string { return e.message }
func (e *jobOperationError) Unwrap() error { return e.cause }

func (r *jobOperationReceipt) executionError() error {
	if r.Error == "" {
		return nil
	}
	var cause error
	switch r.ErrorKind {
	case "cancelled":
		cause = context.Canceled
	case "deadline":
		cause = context.DeadlineExceeded
	}
	return &jobOperationError{message: r.Error, cause: cause}
}

func jobOperationResult(result map[string]interface{}, id, originalJobID, status string, replayed bool) map[string]interface{} {
	out := make(map[string]interface{}, len(result)+1)
	for key, value := range result {
		out[key] = value
	}
	out["_operation"] = map[string]interface{}{
		"id": id, "original_job_id": originalJobID, "status": status, "replayed": replayed,
	}
	return out
}
