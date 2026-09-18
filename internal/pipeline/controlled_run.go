package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// RunControlledPipeline is the foreground CLI/robot entry to the existing
// Executor. Like REST and detached workers, it owns a run until execution and
// cleanup return. It freezes the definition and existing template dependencies
// before dispatch, preserving source-directory-first template lookup. Recovery
// therefore never depends on the caller's subsequently edited workflow file.
// Dry runs bypass ownership and snapshots because they must not write .ntm.
func RunControlledPipeline(ctx context.Context, workflow *Workflow, vars map[string]interface{}, cfg ExecutorConfig, progress chan<- ProgressEvent) (*ExecutionState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if workflow == nil {
		return nil, errors.New("workflow is required")
	}
	if cfg.DryRun {
		return NewExecutor(cfg).Run(ctx, workflow, vars, progress)
	}
	if strings.TrimSpace(cfg.ProjectDir) == "" {
		return nil, errors.New("project directory is required for controlled pipeline execution")
	}
	cfg.ProjectDir = normalizeLockRoot(cfg.ProjectDir)
	if cfg.RunID == "" {
		cfg.RunID = GenerateRunID()
	}
	control, err := AcquireRunControl(ctx, cfg.ProjectDir, cfg.RunID)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	// Run is creation, not recovery. Reusing an ID must never replace a
	// completed or interrupted checkpoint; resume reads it under the lock.
	if _, err := os.Lstat(pipelineStatePath(cfg.ProjectDir, cfg.RunID)); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, fmt.Errorf("pipeline run %q already has state; use resume", cfg.RunID)
		}
		return nil, err
	}
	frozen, path, err := SnapshotWorkflow(control.Context(), cfg.ProjectDir, workflow, cfg.WorkflowFile)
	if err != nil {
		return nil, fmt.Errorf("snapshot foreground pipeline: %w", err)
	}
	cfg.WorkflowFile = path
	if err := control.Context().Err(); err != nil {
		return nil, err
	}
	return NewExecutor(cfg).Run(control.Context(), frozen, vars, progress)
}
