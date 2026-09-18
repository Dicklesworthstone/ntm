package pipeline

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	backgroundRequestVersion  = 1
	maxBackgroundRequestBytes = 8 << 20
	backgroundStartupTimeout  = 15 * time.Second
)

// BackgroundWorkerFlags supplies CLI-owned global options to the re-exec.
// The CLI installs this once during initialization, before any launch. Keeping
// the callback here avoids an import cycle and preserves --config/--ssh rather
// than silently running the worker against a different tmux server.
var BackgroundWorkerFlags func() ([]string, error)

type backgroundRequest struct {
	Version   int                      `json:"version"`
	Token     string                   `json:"token"`
	ExpiresAt time.Time                `json:"expires_at"`
	Config    backgroundExecutorConfig `json:"config"`
	Variables map[string]interface{}   `json:"variables,omitempty"`
	Resume    bool                     `json:"resume,omitempty"`
}

// List the serializable execution options explicitly: ExecutorConfig also has
// an in-process function hook, which must never be silently lost on re-exec.
type backgroundExecutorConfig struct {
	Session          string          `json:"session"`
	ProjectDir       string          `json:"project_dir"`
	WorkflowFile     string          `json:"workflow_file"`
	RunID            string          `json:"run_id"`
	DefaultTimeout   time.Duration   `json:"default_timeout"`
	GlobalTimeout    time.Duration   `json:"global_timeout"`
	ProgressInterval time.Duration   `json:"progress_interval"`
	PaneLockWait     time.Duration   `json:"pane_lock_wait"`
	Verbose          bool            `json:"verbose,omitempty"`
	StartFromStep    string          `json:"start_from_step,omitempty"`
	StartFromState   *ExecutionState `json:"start_from_state,omitempty"`
	ResumeOptions    ResumeOptions   `json:"resume_options,omitempty"`
}

func (c backgroundExecutorConfig) executorConfig() ExecutorConfig {
	return ExecutorConfig{
		Session: c.Session, ProjectDir: c.ProjectDir, WorkflowFile: c.WorkflowFile, RunID: c.RunID,
		DefaultTimeout: c.DefaultTimeout, GlobalTimeout: c.GlobalTimeout,
		ProgressInterval: c.ProgressInterval, PaneLockWait: c.PaneLockWait,
		Verbose: c.Verbose, StartFromStep: c.StartFromStep, StartFromState: c.StartFromState,
		ResumeOptions: c.ResumeOptions,
	}
}

type backgroundReady struct {
	Token      string `json:"token"`
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
	Session    string `json:"session,omitempty"`
	Total      int    `json:"total,omitempty"`
}

// LaunchBackgroundPipeline gives REST callers the same detached worker as the
// CLI. The context owns startup only; after authorization the worker owns its
// lifetime and is cancelled through run control, not an HTTP connection.
func LaunchBackgroundPipeline(ctx context.Context, workflow *Workflow, vars map[string]interface{}, cfg ExecutorConfig) (*PipelineExecution, error) {
	if cfg.RunID == "" {
		cfg.RunID = GenerateRunID()
	}
	return startDetachedPipeline(workflow, vars, cfg, backgroundCommand, ctx)
}

// startDetachedPipeline implements the CLI and REST background lifetime. No
// parent registry entry is installed: it would shadow the child's durable updates.
// The optional context bounds startup; legacy CLI callers use the same deadline.
func startDetachedPipeline(workflow *Workflow, vars map[string]interface{}, cfg ExecutorConfig, command func(string, string) (*exec.Cmd, error), startup ...context.Context) (*PipelineExecution, error) {
	if len(startup) > 1 {
		return nil, errors.New("at most one startup context is allowed")
	}
	parent := context.Background()
	if len(startup) == 1 && startup[0] != nil {
		parent = startup[0]
	}
	ctx, cancel := context.WithTimeout(parent, backgroundStartupTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if workflow == nil {
		return nil, errors.New("workflow is required")
	}
	if strings.TrimSpace(cfg.Session) == "" {
		return nil, errors.New("session is required")
	}
	if cfg.BeadQueryRunBr != nil {
		return nil, errors.New("background execution cannot transfer an in-process BeadQueryRunBr hook")
	}
	if err := validateRunID(cfg.RunID); err != nil {
		return nil, err
	}
	if cfg.DryRun {
		return nil, errors.New("a dry run must not launch a background worker")
	}
	root := normalizeLockRoot(cfg.ProjectDir)
	if _, err := os.Lstat(pipelineStatePath(root, cfg.RunID)); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, fmt.Errorf("pipeline run %q already has state; use resume", cfg.RunID)
		}
		return nil, err
	}
	prepared, err := PrepareWorkflowVariables(workflow, vars)
	if err != nil {
		return nil, err
	}
	_, snapshot, err := snapshotBackgroundWorkflow(ctx, root, workflow, cfg)
	if err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	req := backgroundRequest{
		Version: backgroundRequestVersion, Token: hex.EncodeToString(token[:]), ExpiresAt: deadline,
		Config: backgroundExecutorConfig{
			Session: cfg.Session, ProjectDir: root, WorkflowFile: snapshot, RunID: cfg.RunID,
			DefaultTimeout: cfg.DefaultTimeout, GlobalTimeout: cfg.GlobalTimeout,
			ProgressInterval: cfg.ProgressInterval, PaneLockWait: cfg.PaneLockWait,
			Verbose: cfg.Verbose, StartFromStep: cfg.StartFromStep, StartFromState: cfg.StartFromState,
			ResumeOptions: cfg.ResumeOptions,
		}, Variables: prepared,
	}
	return launchBackgroundWorker(ctx, root, cfg.RunID, req, command)
}

// launchBackgroundWorker is the single detached transport for new runs and
// recovery attempts. requestID names an immutable launch request, not necessarily
// the run being resumed. Only the worker may read that run's checkpoint, after
// acquiring its ownership lock; the parent never transports a stale state copy.
func launchBackgroundWorker(ctx context.Context, root, requestID string, req backgroundRequest, command func(string, string) (*exec.Cmd, error)) (*PipelineExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRunID(requestID); err != nil {
		return nil, err
	}
	dir, err := prepareBackgroundDirectory(root)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode background request: %w", err)
	}
	if len(raw) > maxBackgroundRequestBytes {
		return nil, errors.New("background request exceeds 8 MiB")
	}
	// Never reuse a run's request, even after a failed launch. That would let a
	// delayed worker execute a different request under the same identity.
	requestPath := filepath.Join(dir, requestID+".json")
	if err := writeNewBackgroundFile(requestPath, raw); err != nil {
		return nil, err
	}
	cmd, err := command(root, requestID)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, errors.New("background command builder returned nil")
	}
	if err := detachBackgroundProcess(cmd); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, requestID+".log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("open background log: %w", err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start background worker: %w", err)
	}
	// Reap while the caller lives. Once its CLI exits, the detached process
	// belongs to the OS, not to this goroutine or an HTTP request context.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	committed := false
	defer func() {
		if !committed {
			// No token has been sent, so the child is forbidden to execute any
			// workflow step. Closing stdin also aborts a worker still booting.
			_ = input.Close()
			_ = cmd.Process.Kill()
		}
	}()
	readyPath := filepath.Join(dir, requestID+".ready")
	ticker := time.NewTicker(runControlPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("background startup stopped before authorization: %w", err)
		}
		var ready backgroundReady
		if err := readBackgroundJSON(readyPath, 4096, &ready); err == nil && ready.Token == req.Token {
			if ready.Error != "" {
				return nil, backgroundStartupError(ready, logPath)
			}
			// Two-phase startup: the worker owns the run and has prepared its
			// definition/state, but cannot dispatch until this token reaches it.
			// Parent death/timeout before this point closes the pipe: no work.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err := io.WriteString(input, req.Token+"\n"); err != nil {
				return nil, fmt.Errorf("authorize background worker: %w", err)
			}
			committed = true
			return &PipelineExecution{
				RunID: req.Config.RunID, WorkflowID: ready.WorkflowID, Session: ready.Session,
				Status: "pending", StartedAt: time.Now(), Steps: make(map[string]PipelineStep),
				Progress: PipelineProgress{Total: ready.Total, Pending: ready.Total},
			}, nil
		}
		select {
		case err := <-exited:
			// The worker can publish a typed refusal and exit between polls.
			// Read its final receipt before reducing the failure to an exit code.
			if readBackgroundJSON(readyPath, 4096, &ready) == nil && ready.Token == req.Token && ready.Error != "" {
				return nil, backgroundStartupError(ready, logPath)
			}
			return nil, fmt.Errorf("background worker exited before startup acknowledgment: %v (log: %s)", err, logPath)
		case <-ctx.Done():
			return nil, fmt.Errorf("background worker startup timed out; no execution authorized (log: %s): %w", logPath, ctx.Err())
		case <-ticker.C:
		}
	}
}

func backgroundStartupError(ready backgroundReady, logPath string) error {
	err := fmt.Errorf("background worker: %s (log: %s)", ready.Error, logPath)
	if ready.ErrorCode == "PIPELINE_RUNNING" {
		return errors.Join(ErrRunAlreadyOwned, err)
	}
	return err
}

// Snapshotting moves the workflow file, but the template executor resolves
// relative paths beside that file before searching ProjectDir. Freeze the
// actual existing template paths in a private workflow copy before relocating
// it. Generated templates need absolute paths: guessing which search root will
// contain a future file could silently select the wrong prompt.
func snapshotBackgroundWorkflow(ctx context.Context, root string, workflow *Workflow, cfg ExecutorConfig) (*Workflow, string, error) {
	data, err := marshalWorkflowSnapshot(workflow)
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxWorkflowSnapshotBytes {
		return nil, "", errors.New("background workflow snapshot exceeds size limit")
	}
	copy, validation, err := parseWorkflowSnapshot(data)
	if err != nil {
		return nil, "", err
	}
	if !validation.Valid {
		return nil, "", fmt.Errorf("invalid background workflow: %v", validation.Errors)
	}
	cfg.ProjectDir = root
	if err := resolveBackgroundTemplates(copy, NewExecutor(cfg)); err != nil {
		return nil, "", err
	}
	return SnapshotWorkflow(ctx, root, copy, cfg.WorkflowFile)
}

func resolveBackgroundTemplates(workflow *Workflow, executor *Executor) error {
	var walk func([]Step) error
	walk = func(steps []Step) error {
		for i := range steps {
			step := &steps[i]
			if step.Template != "" && !filepath.IsAbs(step.Template) {
				resolved := executor.resolveTemplatePath(step.Template)
				if resolved == "" {
					return fmt.Errorf("background step %q cannot resolve template %q; use an absolute template path for files generated during execution", step.ID, step.Template)
				}
				absolute, err := filepath.Abs(resolved)
				if err != nil {
					return err
				}
				step.Template = absolute
			}
			if err := walk(step.Parallel.Steps); err != nil {
				return err
			}
			if step.Loop != nil {
				if err := walk(step.Loop.Steps); err != nil {
					return err
				}
			}
			if step.Foreach != nil {
				if err := walk(step.Foreach.Steps); err != nil {
					return err
				}
			}
			if step.ForeachPane != nil {
				if err := walk(step.ForeachPane.Steps); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(workflow.Steps); err != nil {
		return err
	}
	if err := walk(workflow.PostPipelineSteps); err != nil {
		return err
	}
	return walk(workflow.Settings.OnCancel)
}

func backgroundCommand(projectDir, runID string) (*exec.Cmd, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var flags []string
	if BackgroundWorkerFlags != nil {
		flags, err = BackgroundWorkerFlags()
		if err != nil {
			return nil, err
		}
	}
	args := append(append([]string(nil), flags...), "__pipeline-worker", "--", projectDir, runID)
	return exec.Command(binary, args...), nil
}

func prepareBackgroundDirectory(root string) (string, error) {
	dir := filepath.Join(pipelineStateDir(root), "background")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("background directory escapes project")
	}
	return resolved, nil
}

func writeNewBackgroundFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create background request: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return util.SyncDirectory(filepath.Dir(path))
}

func readBackgroundJSON(path string, limit int64, into interface{}) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("background record is not a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("background record exceeds size limit")
	}
	return json.Unmarshal(data, into)
}

// RunBackgroundWorker is invoked by the hidden CLI re-exec command. It uses
// the existing Executor and run-control protocol, not a second workflow engine.
// The caller must supply its actual stdin so parent death before authorization
// produces EOF. A bounded startup wait cannot strand an owner indefinitely.
func RunBackgroundWorker(ctx context.Context, projectDir, requestID string, input io.ReadCloser) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil {
		return errors.New("background worker requires an authorization pipe")
	}
	defer input.Close()
	if err := validateRunID(requestID); err != nil {
		return err
	}
	root := normalizeLockRoot(projectDir)
	dir := filepath.Join(pipelineStateDir(root), "background")
	var req backgroundRequest
	if err := readBackgroundJSON(filepath.Join(dir, requestID+".json"), maxBackgroundRequestBytes, &req); err != nil {
		return err
	}
	if req.Version != backgroundRequestVersion || len(req.Token) != 32 || req.Config.ProjectDir != root {
		return errors.New("invalid background request version or identity")
	}
	runID := req.Config.RunID
	if err := validateRunID(runID); err != nil {
		return err
	}
	if (!req.Resume && runID != requestID) || (req.Resume && requestID != "resume-"+req.Token) {
		return errors.New("background request does not match its launch identity")
	}
	if req.ExpiresAt.IsZero() || !time.Now().Before(req.ExpiresAt) {
		return errors.New("background request expired before startup")
	}
	var st *ExecutionState
	var control *RunControl
	publishReady := !req.Resume
	defer func() {
		if recovered := recover(); recovered != nil {
			retErr = fmt.Errorf("background worker panic: %v", recovered)
		}
		if retErr != nil && publishReady {
			ready := backgroundReady{Token: req.Token, Error: retErr.Error()}
			if errors.Is(retErr, ErrRunAlreadyOwned) {
				ready.ErrorCode = "PIPELINE_RUNNING"
			}
			data, _ := json.Marshal(ready)
			_ = util.AtomicWriteFile(filepath.Join(dir, requestID+".ready"), data, 0600)
		}
		// Retire ownership only after every worker-side state/receipt write.
		if control != nil {
			control.Close()
		}
	}()
	var err error
	if req.Resume {
		// A failed/abandoned resume can be retried with a NEW request, never
		// by replaying this request after its original owner has gone away.
		if err := writeNewBackgroundFile(filepath.Join(dir, requestID+".claimed"), []byte(req.Token)); err != nil {
			return fmt.Errorf("claim background resume attempt: %w", err)
		}
		publishReady = true
	}
	control, err = AcquireRunControl(ctx, root, runID)
	if err != nil {
		return err
	}
	runCtx := control.Context()
	var workflow *Workflow
	cfg := req.Config.executorConfig()
	if req.Resume {
		// No checkpoint writes before authorization, including policy failures,
		// EOF and malformed tokens. Recovery reads only under run ownership.
		workflow, st, cfg, err = prepareBackgroundResume(root, req)
		if err != nil {
			return err
		}
	} else {
		// Creation must not replace an interrupted or completed checkpoint.
		if _, err := os.Lstat(pipelineStatePath(root, runID)); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return errors.New("background run already has state; use resume")
			}
			return err
		}
		var validation ValidationResult
		workflow, validation, err = LoadResumeWorkflow(cfg.WorkflowFile)
		if err != nil {
			return err
		}
		if !validation.Valid {
			return fmt.Errorf("invalid background workflow: %v", validation.Errors)
		}
		st = &ExecutionState{
			RunID: runID, WorkflowID: workflow.Name, WorkflowFile: cfg.WorkflowFile,
			Session: cfg.Session, Status: StatusPending, StartedAt: time.Now(), UpdatedAt: time.Now(),
			Steps: make(map[string]StepResult), Variables: req.Variables,
		}
	}
	// New runs retain their run-ID journal. Recovery attempts get a distinct
	// journal so repeated resumes cannot overwrite or collide with old events.
	progress, finishProgress, err := openBackgroundProgress(dir, requestID)
	if err != nil {
		return err
	}
	defer func() {
		if err := finishProgress(); err != nil {
			slog.Warn("background pipeline progress incomplete", "run_id", runID, "error", err)
		}
	}()
	dispatched := false
	// Persist failure while still holding ownership, including panics and
	// pre-execution pipe failures. Executor.Run itself owns normal cleanup.
	defer func() {
		if recovered := recover(); recovered != nil {
			retErr = fmt.Errorf("background worker panic: %v", recovered)
		}
		if retErr != nil && (!req.Resume || dispatched) {
			// A panic may interrupt Run before it returns its state. Preserve
			// any completed steps it already checkpointed instead of replacing
			// them with our initial pending record.
			if latest, err := LoadState(root, runID); err == nil {
				st = latest
			}
			if st.Status != StatusPending && st.Status != StatusRunning {
				return
			}
			st.Status = StatusFailed
			if runCtx.Err() != nil {
				st.Status = StatusCancelled
			}
			st.FinishedAt, st.UpdatedAt = time.Now(), time.Now()
			st.Errors = append(st.Errors, ExecutionError{Type: "background", Message: retErr.Error(), Timestamp: time.Now(), Fatal: true})
			retErr = errors.Join(retErr, SaveState(root, st))
		}
	}()
	if !req.Resume {
		if err := SaveState(root, st); err != nil {
			return err
		}
	}
	ready, _ := json.Marshal(backgroundReady{Token: req.Token, WorkflowID: workflow.Name, Session: cfg.Session, Total: len(workflow.Steps)})
	if err := util.AtomicWriteFile(filepath.Join(dir, requestID+".ready"), ready, 0600); err != nil {
		return err
	}
	deadline := req.ExpiresAt
	if bound := time.Now().Add(backgroundStartupTimeout); deadline.After(bound) {
		deadline = bound
	}
	startupCtx, cancel := context.WithDeadline(runCtx, deadline)
	err = awaitBackgroundAuthorization(startupCtx, input, req.Token)
	cancel()
	if err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	executor := NewExecutor(cfg)
	dispatched = true
	var final *ExecutionState
	if req.Resume {
		final, err = executor.Resume(runCtx, workflow, st, progress)
	} else {
		final, err = executor.Run(runCtx, workflow, req.Variables, progress)
	}
	if final != nil {
		st = final
	}
	if final == nil && err == nil {
		err = errors.New("background executor returned no state")
	}
	if err == nil && st.Status != StatusCompleted {
		err = fmt.Errorf("background pipeline ended with status %q", st.Status)
	}
	if final != nil {
		err = errors.Join(err, SaveState(root, final))
	}
	return err
}

func awaitBackgroundAuthorization(ctx context.Context, input io.ReadCloser, token string) error {
	var closeOnce sync.Once
	closeInput := func() { closeOnce.Do(func() { _ = input.Close() }) }
	defer closeInput()
	result := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(input, 128)).ReadString('\n')
		if err == nil && line != token+"\n" {
			err = errors.New("invalid background authorization token")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("background execution was not authorized: %w", err)
		}
		return ctx.Err()
	case <-ctx.Done():
		closeInput()
		return ctx.Err()
	}
}
