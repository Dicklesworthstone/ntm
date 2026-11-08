package workflow

import (
	"context"
	"fmt"
	"strings"
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

// WorkflowError is one error raised into a template's error_handling policy.
// AgentID is the faulted pane for agent_crash/agent_error and empty for
// stage-level errors (timeout, trigger_failed). Runners record raised errors
// in the checkpoint's errors list.
type WorkflowError struct {
	Type      ErrorType `json:"type"`
	Stage     string    `json:"stage"`
	AgentID   string    `json:"agent_id,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Retryable bool      `json:"retryable,omitempty"`
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
	// MaxRestartsPerAgent bounds restart_agent per agent for the handler's
	// lifetime (one workflow run). A fault past the budget aborts instead,
	// so a crash loop cannot restart an agent forever. Zero permits none.
	MaxRestartsPerAgent int `toml:"-"`
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
	config   ErrorHandlingConfig
	actions  WorkflowErrorActions
	mu       sync.Mutex
	retries  map[string]int // by stage
	restarts map[string]int // by agent
	persist  func(RecoveryBudgets) error
}

func NewErrorHandler(config ErrorHandlingConfig, actions WorkflowErrorActions) *ErrorHandler {
	return &ErrorHandler{config: config, actions: actions, retries: make(map[string]int), restarts: make(map[string]int)}
}

// ConfigureRecovery restores consumed retry and restart budgets for a resumed
// workflow. persist must save the supplied snapshot before returning success;
// it runs under the handler lock and must not call back into the handler.
// Both restored and published snapshots are detached from the live counters.
func (h *ErrorHandler) ConfigureRecovery(state RecoveryBudgets, persist func(RecoveryBudgets) error) error {
	for _, budget := range []struct {
		name   string
		counts map[string]int
	}{{"retry", state.Retries}, {"restart", state.Restarts}} {
		for key, count := range budget.counts {
			if strings.TrimSpace(key) == "" || count < 0 {
				return fmt.Errorf("invalid workflow %s budget for %q: %d", budget.name, key, count)
			}
		}
	}
	copy := copyRecoveryBudgets(state)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retries = copy.Retries
	h.restarts = copy.Restarts
	h.persist = persist
	return nil
}

func copyRecoveryBudgets(state RecoveryBudgets) RecoveryBudgets {
	copy := RecoveryBudgets{
		Retries:  make(map[string]int, len(state.Retries)),
		Restarts: make(map[string]int, len(state.Restarts)),
	}
	for stage, count := range state.Retries {
		copy.Retries[stage] = count
	}
	for agent, count := range state.Restarts {
		copy.Restarts[agent] = count
	}
	return copy
}

// consumeRecovery reserves one attempt durably before its side effect. A failed
// save leaves the current counters untouched and never authorizes the action.
func (h *ErrorHandler) consumeRecovery(ctx context.Context, key string, restart bool) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(key) == "" {
		return false, fmt.Errorf("workflow recovery requires a nonempty stage or agent identity")
	}
	counts, limit := h.retries, h.config.MaxRetriesPerStage
	if restart {
		counts, limit = h.restarts, h.config.MaxRestartsPerAgent
	}
	if counts[key] >= limit {
		return false, nil
	}
	proposed := copyRecoveryBudgets(RecoveryBudgets{Retries: h.retries, Restarts: h.restarts})
	if restart {
		proposed.Restarts[key]++
	} else {
		proposed.Retries[key]++
	}
	if h.persist != nil {
		if err := h.persist(copyRecoveryBudgets(proposed)); err != nil {
			return false, fmt.Errorf("persist workflow recovery budget before action: %w", err)
		}
	}
	h.retries, h.restarts = proposed.Retries, proposed.Restarts
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (h *ErrorHandler) Handle(ctx context.Context, err *WorkflowError) error {
	if err == nil {
		return nil
	}
	if cause := ctx.Err(); cause != nil {
		return cause
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
		if err.AgentID == "" {
			return fmt.Errorf("restart_agent needs the faulted agent, but %s names none", err.Type)
		}
		allowed, reserveErr := h.consumeRecovery(ctx, err.AgentID, true)
		if reserveErr != nil {
			return reserveErr
		}
		if allowed {
			return h.actions.RestartAgent(ctx, err.AgentID)
		}
		return h.actions.Abort(ctx, fmt.Errorf("agent %s exhausted its restart budget (%d restart(s)): %w", err.AgentID, h.config.MaxRestartsPerAgent, err))
	case ErrorActionPause:
		return h.actions.Pause(ctx, err.Message)
	case ErrorActionSkipStage:
		return h.actions.SkipStage(ctx)
	case ErrorActionAbort:
		return h.actions.Abort(ctx, err)
	case ErrorActionNotify:
		return h.actions.Notify(ctx, err, "workflow error")
	case ErrorActionRetry:
		allowed, reserveErr := h.consumeRecovery(ctx, err.Stage, false)
		if reserveErr != nil {
			return reserveErr
		}
		if allowed {
			return h.actions.RetryStage(ctx)
		}
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
