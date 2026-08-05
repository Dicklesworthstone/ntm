package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ErrorType string

const (
	ErrorAgentCrash    ErrorType = "agent_crash"
	ErrorAgentError    ErrorType = "agent_error"
	ErrorTriggerFailed ErrorType = "trigger_failed"
	ErrorTimeout       ErrorType = "timeout"
	ErrorValidation    ErrorType = "validation"
)

type WorkflowError struct {
	Type                    ErrorType
	Stage, AgentID, Message string
	Timestamp               time.Time
	Retryable               bool
}

func (e *WorkflowError) Error() string {
	return fmt.Sprintf("workflow %s at %s: %s", e.Type, e.Stage, e.Message)
}

type ErrorHandlingConfig struct {
	OnAgentCrash       ErrorAction `toml:"on_agent_crash"`
	OnAgentError       ErrorAction `toml:"on_agent_error"`
	OnTriggerFailed    ErrorAction `toml:"on_trigger_failed"`
	StageTimeoutMin    int         `toml:"stage_timeout_minutes"`
	OnTimeout          ErrorAction `toml:"on_timeout"`
	MaxRetriesPerStage int         `toml:"max_retries_per_stage"`
}

// WorkflowErrorActions is supplied by a coordinator or CLI adapter.
type WorkflowErrorActions interface {
	RestartAgent(context.Context, string) error
	Pause(context.Context, string) error
	SkipStage(context.Context) error
	Abort(context.Context, error) error
	RetryStage(context.Context) error
	Notify(context.Context, *WorkflowError, string) error
}
type ErrorHandler struct {
	config  ErrorHandlingConfig
	actions WorkflowErrorActions
	mu      sync.Mutex
	retries map[string]int
}

func NewErrorHandler(config ErrorHandlingConfig, actions WorkflowErrorActions) *ErrorHandler {
	return &ErrorHandler{config: config, actions: actions, retries: make(map[string]int)}
}
func (h *ErrorHandler) Handle(ctx context.Context, err *WorkflowError) error {
	if err == nil {
		return nil
	}
	if err.Timestamp.IsZero() {
		err.Timestamp = time.Now()
	}
	if h.actions == nil {
		return fmt.Errorf("workflow error actions are not configured")
	}
	action := h.action(err.Type)
	switch action {
	case ErrorActionRestartAgent:
		return h.actions.RestartAgent(ctx, err.AgentID)
	case ErrorActionPause:
		return h.actions.Pause(ctx, err.Message)
	case ErrorActionSkipStage:
		return h.actions.SkipStage(ctx)
	case ErrorActionAbort:
		return h.actions.Abort(ctx, err)
	case ErrorActionNotify:
		return h.actions.Notify(ctx, err, "workflow error")
	case ErrorActionRetry:
		h.mu.Lock()
		retries := h.retries[err.Stage]
		if retries < h.config.MaxRetriesPerStage {
			h.retries[err.Stage] = retries + 1
			h.mu.Unlock()
			return h.actions.RetryStage(ctx)
		}
		h.mu.Unlock()
		return h.actions.Abort(ctx, err)
	default:
		return fmt.Errorf("no error action configured for %s", err.Type)
	}
}
func (h *ErrorHandler) action(kind ErrorType) ErrorAction {
	switch kind {
	case ErrorAgentCrash:
		return h.config.OnAgentCrash
	case ErrorAgentError:
		return h.config.OnAgentError
	case ErrorTriggerFailed:
		return h.config.OnTriggerFailed
	case ErrorTimeout:
		return h.config.OnTimeout
	default:
		return ErrorActionAbort
	}
}

// TimeoutMonitor invokes its handler once when the named stage remains current at expiry.
type TimeoutMonitor struct {
	timeout time.Duration
	handler *ErrorHandler
	current func() string
	cancel  context.CancelFunc
}

func NewTimeoutMonitor(timeout time.Duration, handler *ErrorHandler, current func() string) *TimeoutMonitor {
	return &TimeoutMonitor{timeout: timeout, handler: handler, current: current}
}
func (m *TimeoutMonitor) Start(ctx context.Context, stage string) {
	m.Stop()
	if m.timeout <= 0 || m.handler == nil || m.current == nil {
		return
	}
	run, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	go func() {
		timer := time.NewTimer(m.timeout)
		defer timer.Stop()
		select {
		case <-run.Done():
			return
		case <-timer.C:
			if m.current() == stage {
				_ = m.handler.Handle(run, &WorkflowError{Type: ErrorTimeout, Stage: stage, Message: "stage timeout"})
			}
		}
	}()
}
func (m *TimeoutMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}
