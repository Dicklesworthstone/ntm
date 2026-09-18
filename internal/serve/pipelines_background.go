package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

// startBackgroundPipeline returns only after a separate worker has acquired
// run ownership and published pending state. No parent registry entry or
// Executor is created: either would shadow durable state after a server restart.
func (s *Server) startBackgroundPipeline(ctx context.Context, workflow *pipeline.Workflow, variables map[string]interface{}, config pipeline.ExecutorConfig) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{RunID: config.RunID, Session: config.Session}
	execution, err := pipeline.LaunchBackgroundPipeline(ctx, workflow, variables, config)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "background pipeline was not accepted; inspect its worker log before retrying")
		return output
	}
	if execution == nil || execution.Error != "" {
		err := errors.New("background launcher returned no execution")
		if execution != nil {
			err = errors.New(execution.Error)
		}
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "inspect the worker startup result")
		return output
	}
	output.RobotResponse = pipeline.NewRobotResponse(true)
	output.RunID = execution.RunID
	output.WorkflowID = execution.WorkflowID
	output.Session = execution.Session
	output.Status = execution.Status
	output.Progress = execution.Progress

	// Progress observation is deliberately independent of the request. The
	// worker writes a private ordered stream, so relaying it cannot drive work
	// or keep the worker alive. A missing hub needs no observer goroutine.
	if s.wsHub != nil {
		observerCtx, cancelObserver := detachedRunContext()
		go func() {
			defer cancelObserver()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("background pipeline observer panicked", "run_id", execution.RunID, "panic", recovered)
				}
			}()
			err := pipeline.FollowBackgroundProgress(observerCtx, config.ProjectDir, execution.RunID, func(event pipeline.ProgressEvent) {
				s.publishPipelineProgress(execution.RunID, execution.WorkflowID, execution.Session, event)
			})
			if err != nil {
				slog.Warn("background pipeline event relay stopped", "run_id", execution.RunID, "error", err)
			}
		}()
	}
	return output
}

func (s *Server) publishPipelineProgress(runID, workflowID, session string, event pipeline.ProgressEvent) {
	eventType, ok := pipelineEventTypeFromProgressType(event.Type)
	if !ok {
		return
	}
	payload := map[string]interface{}{
		"run_id": runID, "workflow_id": workflowID, "session": session,
		"step_id": event.StepID, "message": event.Message, "progress": event.Progress,
		"timestamp": event.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	if event.Type == "workflow_error" {
		payload["success"] = false
	}
	if event.Type == "workflow_complete" {
		payload["success"] = true
	}
	s.publishPipelineEvent(session, eventType, payload)
}

// consumePipelineProgress joins the consumer before the executor's run lock is
// released. Foreground, resumed and detached events use the same wire mapping.
func (s *Server) consumePipelineProgress(runID, workflowID, session string) (chan<- pipeline.ProgressEvent, func()) {
	progress := make(chan pipeline.ProgressEvent, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("pipeline event consumer panicked", "run_id", runID, "panic", recovered)
			}
		}()
		for event := range progress {
			s.publishPipelineProgress(runID, workflowID, session, event)
		}
	}()
	var once sync.Once
	return progress, func() { once.Do(func() { close(progress) }); <-done }
}

func (s *Server) projectPipelineSnapshots() ([]*pipeline.PipelineExecution, []string, error) {
	root := s.pipelineProjectDir()
	entries, err := os.ReadDir(filepath.Join(root, ".ntm", "pipelines"))
	if errors.Is(err, os.ErrNotExist) {
		return []*pipeline.PipelineExecution{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	out := make([]*pipeline.PipelineExecution, 0)
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if snapshot := pipeline.LoadPipelineSnapshot(root, id); snapshot != nil {
			out = append(out, snapshot)
		} else {
			warnings = append(warnings, fmt.Sprintf("pipeline state %q could not be read; inspect it before recovery", id))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].RunID < out[j].RunID
	})
	return out, warnings, nil
}
