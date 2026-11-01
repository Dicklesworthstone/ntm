package serve

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const jobJournalVersion = 1
const jobJournalMaxRecordBytes = 8 << 20
const jobJournalReadBatchSize = 64

// jobJournal records execution evidence, not executable requests. In particular,
// recovery must never replay a spawn/restore just because its owner disappeared.
// One server owns a journal directory until all its workers have stopped.
type jobJournal struct {
	dir     string
	release func() error
}

type jobJournalRecord struct {
	Version int  `json:"version"`
	Job     *Job `json:"job"`
}

func openJobJournal(dir string) (*jobJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create job journal: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("job journal must be a real directory: %s", dir)
	}
	if err := syncJobJournalDirectory(filepath.Dir(dir)); err != nil {
		return nil, fmt.Errorf("persist job journal directory: %w", err)
	}
	release, err := lockJobJournal(filepath.Join(dir, ".owner.lock"))
	if err != nil {
		return nil, err
	}
	return &jobJournal{dir: dir, release: release}, nil
}

func (j *jobJournal) Close() error {
	if j.release == nil {
		return nil
	}
	return j.release()
}

func (j *jobJournal) path(id string) (string, error) {
	if id == "" || len(id) > 200 || id == "." || id == ".." {
		return "", errors.New("invalid job journal identity")
	}
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			continue
		}
		return "", errors.New("invalid job journal identity")
	}
	return filepath.Join(j.dir, id+".json"), nil
}

func (j *jobJournal) save(job *Job) error {
	if job == nil {
		return errors.New("cannot persist a nil job")
	}
	path, err := j.path(job.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(jobJournalRecord{Version: jobJournalVersion, Job: job})
	if err != nil {
		return fmt.Errorf("encode job %s: %w", job.ID, err)
	}
	if len(data) > jobJournalMaxRecordBytes {
		return fmt.Errorf("job %s exceeds journal record limit", job.ID)
	}
	return writeJobJournalRecord(j.dir, path, data)
}

// writeJobJournalRecord is the single publication path for execution history
// and durable operation receipts. Callers own the appropriate journal fence.
func writeJobJournalRecord(directory, path string, data []byte) error {
	if len(data) > jobJournalMaxRecordBytes {
		return errors.New("job journal record exceeds size limit")
	}
	// Never truncate the last known checkpoint. The rename publishes a fully
	// flushed record; syncing the directory makes that publication durable.
	file, err := os.CreateTemp(directory, ".job-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncJobJournalDirectory(directory)
}

func syncJobJournalDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// walkEntries bounds directory metadata as well as decoded receipt payloads.
// Journal history is retained on disk, so even entry names can outgrow memory.
func (j *jobJournal) walkEntries(visit func(os.DirEntry) error) (err error) {
	directory, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	for {
		entries, readErr := directory.ReadDir(jobJournalReadBatchSize)
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (j *jobJournal) readJob(entry os.DirEntry) (*Job, error) {
	id := strings.TrimSuffix(entry.Name(), ".json")
	path, err := j.path(id)
	if err != nil || !entry.Type().IsRegular() {
		return nil, fmt.Errorf("invalid job journal entry %q", entry.Name())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, jobJournalMaxRecordBytes+1))
	readErr = errors.Join(readErr, file.Close())
	if readErr != nil {
		return nil, readErr
	}
	if len(data) > jobJournalMaxRecordBytes {
		return nil, fmt.Errorf("job journal entry %q exceeds record limit", entry.Name())
	}
	var record jobJournalRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode job journal entry %q: %w", entry.Name(), err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("job journal entry %q has trailing data", entry.Name())
	}
	if record.Version != jobJournalVersion || record.Job == nil || record.Job.ID != id {
		return nil, fmt.Errorf("job journal entry %q has an unsupported version or mismatched identity", entry.Name())
	}
	switch record.Job.Status {
	case JobStatusPending, JobStatusRunning, JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
	default:
		return nil, fmt.Errorf("job journal entry %q has unknown status %q", entry.Name(), record.Job.Status)
	}
	return record.Job, nil
}

func (j *jobJournal) walkJobs(visit func(*Job) error) error {
	return j.walkEntries(func(entry os.DirEntry) error {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return nil // Temporary files and operation receipts are separate namespaces.
		}
		job, err := j.readJob(entry)
		if err != nil {
			return err
		}
		return visit(job)
	})
}

// The oldest retained receipt is the heap root. Match evictTerminalLocked:
// at a shared timestamp, the smaller IDs are evicted first. Presentation order
// is separate and sorts the surviving IDs ascending.
type jobJournalHistory []*Job

func olderJournalJob(a, b *Job) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

func (h jobJournalHistory) Len() int           { return len(h) }
func (h jobJournalHistory) Less(i, k int) bool { return olderJournalJob(h[i], h[k]) }
func (h jobJournalHistory) Swap(i, k int)      { h[i], h[k] = h[k], h[i] }
func (h *jobJournalHistory) Push(value any)    { *h = append(*h, value.(*Job)) }
func (h *jobJournalHistory) Pop() any {
	last := len(*h) - 1
	job := (*h)[last]
	(*h)[last] = nil
	*h = (*h)[:last]
	return job
}

func jobNeedsRecovery(job *Job) bool {
	return job.Status == JobStatusPending || job.Status == JobStatusRunning || job.Result["_execution_in_progress"] == true
}

func (j *jobJournal) load(validate ...func(*Job) error) ([]*Job, error) {
	history := make(jobJournalHistory, 0)
	if err := j.walkJobs(func(job *Job) error {
		for _, check := range validate {
			if err := check(job); err != nil {
				return err
			}
		}
		if len(history) < maxRetainedJobs {
			heap.Push(&history, job)
		} else if olderJournalJob(history[0], job) {
			history[0] = job
			heap.Fix(&history, 0)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(history, func(a, b int) bool {
		if history[a].CreatedAt != history[b].CreatedAt {
			return history[a].CreatedAt > history[b].CreatedAt
		}
		return history[a].ID < history[b].ID
	})
	return history, nil
}

// recover records an explicit uncertain outcome for interrupted executions.
// A pending checkpoint was saved before dispatch; a crash could have happened
// anywhere after it, including after external effects but before the final save.
func (j *jobJournal) recover(now time.Time) ([]*Job, error) {
	// A bounded shutdown may have returned while an old worker is still
	// unwinding. Its individual fence survives even if its in-memory row was
	// evicted. Preflight EVERY worker fence and decode EVERY receipt before
	// rewriting anything, including evidence outside the retained window.
	unfinished := 0
	if err := j.walkEntries(func(entry os.DirEntry) error {
		if strings.HasSuffix(entry.Name(), ".worker.lock") {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("invalid job worker fence %q", entry.Name())
			}
			release, err := lockJobJournal(filepath.Join(j.dir, entry.Name()))
			if err != nil {
				return fmt.Errorf("previous job worker is still active: %w", err)
			}
			return release()
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			job, err := j.readJob(entry)
			if err != nil {
				return err
			}
			if jobNeedsRecovery(job) {
				unfinished++
			}
		}
		return nil
	}); err != nil {
		return nil, err // Never turn unreadable history into an empty, healthy view.
	}
	for {
		if unfinished > 0 {
			if err := j.walkJobs(func(job *Job) error {
				if !jobNeedsRecovery(job) {
					return nil
				}
				// A cancelled row can still belong to a worker that was unwinding
				// when the process died. Its progress is evidence, not a final result.
				if job.Status == JobStatusPending || job.Status == JobStatusRunning {
					job.Status = JobStatusFailed
					job.Error = "server stopped before recording the execution outcome; inspect existing sessions, panes, and pipeline state before retrying; work was not automatically replayed"
				}
				job.UpdatedAt = now.UTC().Format(time.RFC3339)
				if job.Result == nil {
					job.Result = make(map[string]interface{})
				}
				delete(job.Result, "_execution_in_progress")
				job.Result["interrupted"] = true
				job.Result["outcome_unknown"] = true
				if err := j.save(job); err != nil {
					return fmt.Errorf("record interrupted job %s: %w", job.ID, err)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
		// Atomic rewrites may make a directory iterator omit or repeat entries.
		// Inspect every receipt again without mutation before publishing the
		// bounded view. Retry omissions only while the unfinished count falls,
		// so recovery cannot spin or discard unresolved execution evidence.
		remaining := 0
		jobs, err := j.load(func(job *Job) error {
			if jobNeedsRecovery(job) {
				remaining++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if remaining == 0 {
			return jobs, nil
		}
		if remaining >= unfinished {
			return nil, fmt.Errorf("job journal recovery made no progress: %d unfinished receipts remain", remaining)
		}
		unfinished = remaining
	}
}

type jobProgressContextKey struct{}
type jobProgressReporter func(map[string]interface{}) error

var errJobProgressCheckpoint = errors.New("job progress checkpoint failed")

func reportJobProgress(ctx context.Context, result map[string]interface{}) error {
	if report, ok := ctx.Value(jobProgressContextKey{}).(jobProgressReporter); ok && report != nil {
		// Finished side effects must be recorded even when ctx was cancelled.
		if err := report(result); err != nil {
			return fmt.Errorf("%w: %w", errJobProgressCheckpoint, err)
		}
	}
	return nil
}

// checkpointJobProgress publishes an immutable snapshot while the worker owns
// its fence. Cancellation may change status/reason but must not prevent late
// evidence from being recorded. An absent/finished row is never resurrected.
func (s *Server) checkpointJobProgress(id string, result map[string]interface{}) (map[string]interface{}, error) {
	snapshot, err := toJSONMap(result)
	if err != nil {
		return nil, fmt.Errorf("encode progress: %w", err)
	}
	if snapshot == nil {
		snapshot = make(map[string]interface{})
	}
	snapshot["_execution_in_progress"] = true
	dir, err := s.jobJournalDir()
	if err != nil {
		return snapshot, err
	}
	s.jobStore.mu.Lock()
	defer s.jobStore.mu.Unlock()
	job := s.jobStore.jobs[id]
	if job == nil {
		return snapshot, fmt.Errorf("job %s disappeared before progress was checkpointed", id)
	}
	if job.Status != JobStatusRunning && job.Status != JobStatusPending && job.Status != JobStatusCancelled {
		return snapshot, fmt.Errorf("job %s already has a terminal outcome", id)
	}
	checkpoint := *job
	checkpoint.Result = snapshot
	checkpoint.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if dir != "" {
		if err := (&jobJournal{dir: dir}).save(&checkpoint); err != nil {
			return snapshot, err
		}
	}
	job.Result = snapshot
	job.UpdatedAt = checkpoint.UpdatedAt
	return snapshot, nil
}

// finishJobProgress keeps evidence if an engine returns nil or panics. An
// actual final result wins over an earlier progress value, without mutating
// either input; nested snapshots are immutable after publication.
func finishJobProgress(progress, result map[string]interface{}) map[string]interface{} {
	if progress == nil && result == nil {
		return nil
	}
	out := make(map[string]interface{}, len(progress)+len(result))
	for key, value := range progress {
		out[key] = value
	}
	for key, value := range result {
		out[key] = value
	}
	delete(out, "_execution_in_progress")
	return out
}

// checkpointJobOutcome retains the worker fence while storing and publishing
// its terminal result. An engine's success is not a durable job completion
// until this write succeeds. Operation-ID receipts have their own publication
// boundary and remain authoritative even if this transport row cannot be saved.
func (s *Server) checkpointJobOutcome(id string, status JobStatus, progress float64, result map[string]interface{}, message string) error {
	dir, namespaceErr := s.jobJournalDir()
	return s.jobStore.checkpointOutcome(id, status, progress, result, message, func(job *Job) error {
		if namespaceErr != nil || dir == "" {
			return namespaceErr
		}
		return (&jobJournal{dir: dir}).save(job)
	})
}

// checkpointOutcome serializes cancellation against the complete terminal
// publication, just as progress checkpointing does. The writer sees a detached
// candidate; readers cannot see it until persistence has finished. Results are
// immutable once supplied, including nested progress and operation evidence.
func (s *JobStore) checkpointOutcome(id string, status JobStatus, progress float64, result map[string]interface{}, message string, persist func(*Job) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return fmt.Errorf("job %s disappeared before its outcome was checkpointed", id)
	}
	if job.Status == JobStatusCompleted || job.Status == JobStatusFailed {
		return nil // Never replace an already published terminal outcome.
	}
	candidate := *job
	if job.Status == JobStatusCancelled {
		// Cancellation owns its original status and reason. A worker may
		// still supply the final effects without clearing an existing result.
		retainCancelledResult(&candidate, result)
	} else {
		candidate.Status, candidate.Progress = status, progress
		candidate.Result, candidate.Error = result, message
	}
	candidate.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := persist(&candidate); err != nil {
		// Keep the observed outcome separate from the failed durable job.
		// Neither this status nor a storage error authorizes another engine
		// invocation. The last durable receipt and operation fence stay intact.
		candidate.Result = s.cloneJob(&candidate).Result
		if candidate.Result == nil {
			candidate.Result = make(map[string]interface{})
		}
		candidate.Result["observed_status"] = string(status)
		candidate.Result["outcome_unknown"] = true
		candidate.Result["_journal_error"] = err.Error()
		if candidate.Status != JobStatusCancelled {
			candidate.Status, candidate.Progress = JobStatusFailed, 0
			checkpointError := "final job receipt could not be persisted; inspect existing work before retrying: " + err.Error()
			if candidate.Error != "" {
				checkpointError = candidate.Error + "; " + checkpointError
			}
			candidate.Error = checkpointError
		}
		*job = candidate
		return err
	}
	*job = candidate
	return nil
}

// RestoreJobHistory is called before serving requests. The returned shutdown
// function must be called AFTER stopping HTTP admission and BEFORE releasing
// the state store. It keeps ownership until the dispatchers have checkpointed
// their final results. New remains side-effect-free.
func (s *Server) RestoreJobHistory() (func(context.Context) error, error) {
	dir, err := s.jobJournalDir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return s.drainJobWorkers, nil
	}
	journal, err := openJobJournal(dir)
	if err != nil {
		return nil, err
	}
	jobs, err := journal.recover(time.Now())
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	s.jobStore.mu.Lock()
	for _, job := range jobs {
		s.jobStore.jobs[job.ID] = job
	}
	s.jobStore.evictTerminalLocked(maxRetainedJobs)
	s.jobStore.mu.Unlock()
	var once sync.Once
	var closeErr error
	return func(ctx context.Context) error {
		once.Do(func() {
			closeErr = s.drainJobWorkers(ctx)
			if closeErr == nil {
				closeErr = journal.Close()
				return
			}
			// Do not release the ownership fence while a slow worker may still
			// write. The kernel also releases it if the whole process exits.
			go func() {
				_ = s.drainJobWorkers(context.Background())
				if err := journal.Close(); err != nil {
					slog.Warn("close job journal after worker drain", "error", err)
				}
			}()
		})
		return closeErr
	}, nil
}

func (s *Server) jobJournalDir() (string, error) {
	if s.stateStore == nil || s.stateStore.Path() == ":memory:" {
		return "", nil // Explicitly in-memory servers retain their hermetic behavior.
	}
	path, err := filepath.EvalSymlinks(s.stateStore.Path())
	if err != nil {
		return "", fmt.Errorf("resolve job state namespace: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return path + ".jobs", nil
}

// persistJobHistory serializes snapshot-and-save with store updates: a late
// cancellation cannot be overwritten by an older completed/running snapshot.
func (s *Server) persistJobHistory(id string) error {
	dir, err := s.jobJournalDir()
	if err != nil || dir == "" {
		return err
	}
	s.jobStore.mu.Lock()
	defer s.jobStore.mu.Unlock()
	job := s.jobStore.jobs[id]
	if job == nil {
		return fmt.Errorf("job %s disappeared before its outcome was checkpointed", id)
	}
	return (&jobJournal{dir: dir}).save(job)
}

func (s *Server) fenceJobWorker(id string) (func() error, error) {
	dir, err := s.jobJournalDir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return func() error { return nil }, nil
	}
	path, err := (&jobJournal{dir: dir}).path(id)
	if err != nil {
		return nil, err
	}
	return lockJobJournal(strings.TrimSuffix(path, ".json") + ".worker.lock")
}

func (s *Server) recordJobJournalError(id string, err error) {
	slog.Error("job outcome not persisted", "job_id", id, "error", err)
	s.jobStore.mu.Lock()
	defer s.jobStore.mu.Unlock()
	if job := s.jobStore.jobs[id]; job != nil {
		if job.Result == nil {
			job.Result = make(map[string]interface{})
		}
		job.Result["_journal_error"] = err.Error()
	}
}

// drainJobWorkers closes admission before cancelling execution. Preparation
// and finalizing workers remain owned even when no runnable row remains.
func (s *Server) drainJobWorkers(ctx context.Context) error {
	s.jobExecutor.closeAdmission()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		// A slow fsync holds the store lock. Do not let that mutex defeat
		// the caller's shutdown deadline; executor cancellation is already sent.
		if !s.jobStore.mu.TryLock() {
			select {
			case <-ctx.Done():
				return fmt.Errorf("job checkpoint still draining: %w", ctx.Err())
			case <-ticker.C:
				continue
			}
		}
		for _, job := range s.jobStore.jobs {
			if job.Status == JobStatusPending || job.Status == JobStatusRunning {
				job.Status = JobStatusCancelled
				job.Error = "server shutting down"
				job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
		cancels := make([]context.CancelFunc, 0, len(s.jobStore.cancels))
		for _, cancel := range s.jobStore.cancels {
			cancels = append(cancels, cancel)
		}
		s.jobStore.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		if len(cancels) == 0 && s.jobExecutor.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("job workers still draining: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
