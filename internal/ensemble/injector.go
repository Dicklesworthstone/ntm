package ensemble

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// InjectionResult tracks the result of a single prompt injection.
// This mirrors swarm.InjectionResult but avoids the import cycle.
type InjectionResult struct {
	SessionPane string        `json:"session_pane"`
	AgentType   string        `json:"agent_type"`
	Success     bool          `json:"success"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
	SentAt      time.Time     `json:"sent_at"`
}

// BatchInjectionResult tracks the results of a batch injection operation.
type BatchInjectionResult struct {
	TotalPanes int               `json:"total_panes"`
	Successful int               `json:"successful"`
	Failed     int               `json:"failed"`
	Results    []InjectionResult `json:"results"`
	Duration   time.Duration     `json:"duration"`
}

// BasicInjector defines the interface for basic prompt injection.
// This is implemented by swarm.PromptInjector.
type BasicInjector interface {
	// InjectPromptContext sends a prompt to a single pane with caller cancellation.
	InjectPromptContext(ctx context.Context, sessionPane, agentType, prompt string) error
	// GetDelayForAgent returns the appropriate delay for an agent type.
	GetDelayForAgent(agentType string) time.Duration
}

// EnsembleInjector handles mode-specific prompt injection for ensembles.
// It wraps a BasicInjector and adds ensemble-specific logic.
type EnsembleInjector struct {
	// BasicInjector for sending prompts to panes.
	BasicInjector BasicInjector

	// PreambleEngine renders mode-specific preambles.
	PreambleEngine *PreambleEngine

	// Logger for structured logging.
	Logger *slog.Logger
}

// NewEnsembleInjector creates a new EnsembleInjector with default settings.
func NewEnsembleInjector() *EnsembleInjector {
	return &EnsembleInjector{
		PreambleEngine: NewPreambleEngine(),
		Logger:         slog.Default(),
	}
}

// WithBasicInjector sets the basic injector and returns the EnsembleInjector for chaining.
func (e *EnsembleInjector) WithBasicInjector(injector BasicInjector) *EnsembleInjector {
	e.BasicInjector = injector
	return e
}

// WithLogger sets a custom logger and returns the EnsembleInjector for chaining.
func (e *EnsembleInjector) WithLogger(logger *slog.Logger) *EnsembleInjector {
	e.Logger = logger
	return e
}

// logger returns the configured logger or the default logger.
func (e *EnsembleInjector) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// InjectWithMode sends a mode-specific preamble and question to a single pane.
// The preamble is rendered from the reasoning mode metadata and schema contract.
func (e *EnsembleInjector) InjectWithMode(
	ctx context.Context,
	sessionPane string,
	mode *ReasoningMode,
	question string,
	agentType string,
	contextPack *ContextPack,
	tokenCap int,
	additionalContext string,
) (*InjectionResult, error) {
	start := time.Now()
	result := &InjectionResult{
		SessionPane: sessionPane,
		AgentType:   agentType,
		SentAt:      start,
	}

	if ctx == nil {
		err := fmt.Errorf("prompt injection context is required")
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}
	if mode == nil {
		err := fmt.Errorf("mode is required")
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}
	if e.PreambleEngine == nil {
		err := fmt.Errorf("preamble engine is not configured")
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}
	if e.BasicInjector == nil {
		err := fmt.Errorf("basic injector is not configured")
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}

	e.logger().Info("[EnsembleInjector] mode_inject_start",
		"session_pane", sessionPane,
		"mode_id", mode.ID,
		"mode_code", mode.Code,
		"agent_type", agentType)

	data := &PreambleData{
		Problem:      question,
		ContextPack:  contextPack,
		Mode:         mode,
		TokenCap:     tokenCap,
		OutputSchema: GetSchemaContract(),
	}

	preamble, err := e.PreambleEngine.Render(data)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		e.logger().Error("[EnsembleInjector] mode_render_error",
			"mode_id", mode.ID,
			"mode_code", mode.Code,
			"agent_type", agentType,
			"error", err)
		return result, fmt.Errorf("render preamble for %s: %w", mode.ID, err)
	}

	parts := []string{preamble}
	if additionalContext != "" {
		parts = append(parts, additionalContext)
	}
	if question != "" {
		parts = append(parts, question)
	}
	prompt := strings.Join(parts, "\n\n")

	if err := e.BasicInjector.InjectPromptContext(ctx, sessionPane, agentType, prompt); err != nil {
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		e.logger().Error("[EnsembleInjector] mode_inject_error",
			"session_pane", sessionPane,
			"mode_id", mode.ID,
			"mode_code", mode.Code,
			"agent_type", agentType,
			"error", err)
		return result, err
	}

	result.Success = true
	result.Duration = time.Since(start)

	e.logger().Info("[EnsembleInjector] mode_inject_complete",
		"session_pane", sessionPane,
		"mode_id", mode.ID,
		"mode_code", mode.Code,
		"agent_type", agentType,
		"success", true,
		"duration", result.Duration)

	return result, nil
}

// InjectEnsemble sends mode-specific preambles to all assigned panes.
// It continues on per-pane errors, but cancellation stops the batch and retains
// receipts only for the panes whose delivery was attempted.
func (e *EnsembleInjector) InjectEnsemble(
	ctx context.Context,
	assignments []ModeAssignment,
	question string,
	catalog *ModeCatalog,
	contextPack *ContextPack,
	tokenCap int,
) (*BatchInjectionResult, error) {
	start := time.Now()

	if ctx == nil {
		return nil, fmt.Errorf("prompt injection context is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog cannot be nil")
	}
	if e.BasicInjector == nil {
		return nil, fmt.Errorf("basic injector is not configured")
	}

	result := &BatchInjectionResult{
		TotalPanes: len(assignments),
		Results:    make([]InjectionResult, 0, len(assignments)),
	}
	defer func() { result.Duration = time.Since(start) }()
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if len(assignments) == 0 {
		return result, nil
	}

	e.logger().Info("[EnsembleInjector] ensemble_inject_start",
		"total_assignments", len(assignments))

	for i, assignment := range assignments {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if i > 0 {
			delay := e.BasicInjector.GetDelayForAgent(assignment.AgentType)
			if delay > 0 {
				e.logger().Debug("[EnsembleInjector] ensemble_stagger_delay",
					"pane", assignment.PaneName,
					"delay_ms", delay.Milliseconds(),
					"index", i,
					"total", len(assignments))
				select {
				case <-ctx.Done():
					return result, ctx.Err()
				case <-time.After(delay):
				}
			}
		}

		mode := catalog.GetMode(assignment.ModeID)
		if mode == nil {
			err := fmt.Errorf("mode not found: %s", assignment.ModeID)
			result.Failed++
			result.Results = append(result.Results, InjectionResult{
				SessionPane: assignment.PaneName,
				AgentType:   assignment.AgentType,
				Success:     false,
				Error:       err.Error(),
				SentAt:      time.Now(),
				Duration:    time.Since(start),
			})
			e.logger().Error("[EnsembleInjector] ensemble_mode_missing",
				"mode_id", assignment.ModeID,
				"session_pane", assignment.PaneName,
				"agent_type", assignment.AgentType)
			continue
		}

		injResult, err := e.InjectWithMode(
			ctx,
			assignment.PaneName,
			mode,
			question,
			assignment.AgentType,
			contextPack,
			tokenCap,
			"",
		)
		if err != nil {
			result.Failed++
		} else {
			result.Successful++
		}
		if injResult != nil {
			result.Results = append(result.Results, *injResult)
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}

		e.logger().Info("[EnsembleInjector] ensemble_inject_progress",
			"sent", i+1,
			"total", len(assignments),
			"mode_id", assignment.ModeID,
			"session_pane", assignment.PaneName)
	}

	result.Duration = time.Since(start)

	e.logger().Info("[EnsembleInjector] ensemble_inject_complete",
		"successful", result.Successful,
		"failed", result.Failed,
		"duration", result.Duration)

	return result, nil
}
