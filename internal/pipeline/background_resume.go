package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// StartBackgroundResume continues a saved run in a detached, same-binary worker.
// The run ID stays stable for status and cancellation, but every launch gets a
// new immutable request identity. It never transports a parent-read checkpoint:
// the child acquires RunControl before loading and verifying recovery inputs.
// A successful return acknowledges startup, not completion of the workflow.
func StartBackgroundResume(ctx context.Context, projectDir, runID, session string, opts ResumeOptions) (*PipelineExecution, error) {
	return startDetachedResume(ctx, projectDir, runID, session, opts, backgroundCommand)
}

func startDetachedResume(ctx context.Context, projectDir, runID, session string, opts ResumeOptions, command func(string, string) (*exec.Cmd, error)) (*PipelineExecution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil, errors.New("project directory is required for background resume")
	}
	opts, err := normalizeResumeOptions(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundStartupTimeout)
	defer cancel()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	root := normalizeLockRoot(projectDir)
	cfg := DefaultExecutorConfig(strings.TrimSpace(session))
	deadline, _ := ctx.Deadline()
	req := backgroundRequest{
		Version: backgroundRequestVersion, Token: hex.EncodeToString(token[:]),
		ExpiresAt: deadline, Resume: true,
		Config: backgroundExecutorConfig{
			Session: cfg.Session, ProjectDir: root, RunID: runID,
			DefaultTimeout: cfg.DefaultTimeout, GlobalTimeout: cfg.GlobalTimeout,
			ProgressInterval: cfg.ProgressInterval, PaneLockWait: cfg.PaneLockWait,
			ResumeOptions: opts,
		},
	}
	return launchBackgroundWorker(ctx, root, "resume-"+req.Token, req, command)
}

// prepareBackgroundResume runs only while the child holds exclusive run
// ownership. Validation uses a private state copy and the executor's existing
// policy implementation; it neither rewrites the checkpoint nor re-snapshots
// the workflow, so damaged recovery evidence cannot become newly trusted.
func prepareBackgroundResume(root string, req backgroundRequest) (*Workflow, *ExecutionState, ExecutorConfig, error) {
	cfg := req.Config.executorConfig()
	fail := func(err error) (*Workflow, *ExecutionState, ExecutorConfig, error) {
		return nil, nil, cfg, err
	}
	if cfg.WorkflowFile != "" || cfg.StartFromStep != "" || cfg.StartFromState != nil || len(req.Variables) != 0 {
		return fail(errors.New("background resume must use the owned checkpoint, not replacement workflow, state or variables"))
	}
	opts, err := normalizeResumeOptions(cfg.ResumeOptions)
	if err != nil {
		return fail(err)
	}
	cfg.ResumeOptions = opts
	prior, err := LoadState(root, cfg.RunID)
	if err != nil {
		return fail(fmt.Errorf("load background resume checkpoint: %w", err))
	}
	switch prior.Status {
	case StatusPending, StatusRunning, StatusFailed, StatusCancelled:
	case StatusCompleted:
		return fail(fmt.Errorf("pipeline %q already completed; background resume starts no work", cfg.RunID))
	default:
		return fail(fmt.Errorf("pipeline %q has unsupported resume status %q", cfg.RunID, prior.Status))
	}
	if cfg.Session == "" {
		cfg.Session = prior.Session
	}
	if strings.TrimSpace(cfg.Session) == "" {
		return fail(errors.New("background resume requires a saved session or an explicit session"))
	}
	path := strings.TrimSpace(prior.WorkflowFile)
	if path == "" {
		return fail(errors.New("background resume checkpoint is missing its workflow file"))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	workflow, validation, err := LoadResumeWorkflow(path)
	if err != nil {
		return fail(fmt.Errorf("verify background resume workflow: %w", err))
	}
	if !validation.Valid {
		return fail(fmt.Errorf("invalid background resume workflow: %v", validation.Errors))
	}
	if prior.WorkflowID != "" && prior.WorkflowID != workflow.Name {
		return fail(fmt.Errorf("background resume workflow %q does not match saved workflow %q", workflow.Name, prior.WorkflowID))
	}
	cfg.WorkflowFile = path
	checker := NewExecutor(cfg)
	checker.state = prior
	checker.state = checker.snapshotState()
	if err := checker.applyResumeOptions(workflow, opts); err != nil {
		return fail(err)
	}
	prior.WorkflowFile = path
	return workflow, prior, cfg, nil
}
