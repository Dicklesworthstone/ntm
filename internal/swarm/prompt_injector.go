package swarm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Default prompt templates for marching orders.
const (
	// DefaultMarchingOrders is the standard prompt sent to agents when spawning a swarm.
	DefaultMarchingOrders = `First read ALL of the AGENTS.md file super carefully and understand ALL of it!
Then use your code investigation agent mode to fully understand the code,
and technical architecture and purpose of the project.
Then use bv --robot-triage to find the most important work to focus on.
Then claim a bead with br update <id> --status in_progress and get to work!`

	// ReviewTemplate is used for code review tasks.
	ReviewTemplate = `Please review the recent changes in this project:
1. Run git log --oneline -10 to see recent commits
2. Run git diff HEAD~3 to see the changes
3. Provide feedback on code quality, potential bugs, and improvements`

	// TestTemplate is used for running tests.
	TestTemplate = `Please run the test suite and report any failures:
1. Identify the test command (go test, npm test, pytest, etc.)
2. Run the full test suite
3. Report failures with context and suggested fixes`
)

// InjectionTarget describes where to send a prompt.
type InjectionTarget struct {
	SessionPane string `json:"session_pane"` // e.g., "myproject:1.2"
	AgentType   string `json:"agent_type"`   // cc, cod, or gmi
}

// InjectionResult tracks the result of a single prompt injection.
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

// PromptInjector sends prompts (marching orders) to agent panes.
// It handles staggered sending to avoid rate limits and agent-specific quirks.
//
// For ensemble-specific mode injection, use ensemble.EnsembleInjector which
// wraps this basic injector and adds preamble rendering capabilities.
type PromptInjector struct {
	// TmuxClient for sending keys to panes.
	// If nil, the default tmux client is used.
	TmuxClient *tmux.Client

	// StaggerDelay is the delay between sends to avoid rate limits.
	// Default: 300ms
	// Only used when UseAdaptiveDelay is false.
	StaggerDelay time.Duration

	// EnterDelay is the delay before sending Enter after the prompt.
	// Default: 100ms
	EnterDelay time.Duration

	// DoubleEnterDelay is the delay between first and second Enter for agents
	// that need double-Enter (like Codex).
	// Default: 500ms
	DoubleEnterDelay time.Duration

	// Logger for structured logging.
	Logger *slog.Logger

	// Templates holds named prompt templates.
	Templates map[string]string

	// RateLimitTracker for adaptive delay learning.
	// If set and UseAdaptiveDelay is true, delays are learned from rate limit events.
	RateLimitTracker *ratelimit.RateLimitTracker

	// UseAdaptiveDelay enables adaptive delay learning via RateLimitTracker.
	// When true, StaggerDelay is ignored and GetOptimalDelay() is used instead.
	// Default: false
	UseAdaptiveDelay bool
}

// NewPromptInjector creates a new PromptInjector with default settings.
func NewPromptInjector() *PromptInjector {
	return &PromptInjector{
		TmuxClient:       nil,
		StaggerDelay:     300 * time.Millisecond,
		EnterDelay:       100 * time.Millisecond,
		DoubleEnterDelay: 500 * time.Millisecond,
		Logger:           slog.Default(),
		Templates: map[string]string{
			"default": DefaultMarchingOrders,
			"review":  ReviewTemplate,
			"test":    TestTemplate,
		},
	}
}

// NewPromptInjectorWithClient creates a PromptInjector with a custom tmux client.
func NewPromptInjectorWithClient(client *tmux.Client) *PromptInjector {
	p := NewPromptInjector()
	p.TmuxClient = client
	return p
}

// WithLogger sets a custom logger and returns the PromptInjector for chaining.
func (p *PromptInjector) WithLogger(logger *slog.Logger) *PromptInjector {
	p.Logger = logger
	return p
}

// WithStaggerDelay sets a custom stagger delay and returns the PromptInjector for chaining.
func (p *PromptInjector) WithStaggerDelay(delay time.Duration) *PromptInjector {
	p.StaggerDelay = delay
	return p
}

// WithRateLimitTracker sets a rate limit tracker for adaptive delays.
func (p *PromptInjector) WithRateLimitTracker(tracker *ratelimit.RateLimitTracker) *PromptInjector {
	p.RateLimitTracker = tracker
	return p
}

// WithAdaptiveDelay enables or disables adaptive delay learning.
// When enabled, the RateLimitTracker must be set.
func (p *PromptInjector) WithAdaptiveDelay(enabled bool) *PromptInjector {
	p.UseAdaptiveDelay = enabled
	return p
}

// tmuxClient returns the configured tmux client or the default client.
func (p *PromptInjector) tmuxClient() *tmux.Client {
	if p.TmuxClient != nil {
		return p.TmuxClient
	}
	return tmux.DefaultClient
}

// logger returns the configured logger or the default logger.
func (p *PromptInjector) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// GetDelayForAgent returns the appropriate delay for an agent type.
// If adaptive delay is enabled and a tracker is configured, it uses the learned optimal delay.
// Otherwise, it uses the fixed StaggerDelay.
// This method is exported to satisfy the ensemble.BasicInjector interface.
func (p *PromptInjector) GetDelayForAgent(agentType string) time.Duration {
	if p.UseAdaptiveDelay && p.RateLimitTracker != nil {
		return p.RateLimitTracker.GetOptimalDelay(agentType)
	}
	return p.StaggerDelay
}

// recordSuccess records a successful send to the rate limit tracker.
func (p *PromptInjector) recordSuccess(agentType string) {
	if p.UseAdaptiveDelay && p.RateLimitTracker != nil {
		p.RateLimitTracker.RecordSuccess(agentType)
	}
}

// GetTemplate returns the prompt template by name.
// Returns the default template if the name is not found.
func (p *PromptInjector) GetTemplate(name string) string {
	if tmpl, ok := p.Templates[name]; ok {
		return tmpl
	}
	return p.Templates["default"]
}

// SetTemplate sets a named template.
func (p *PromptInjector) SetTemplate(name, template string) {
	p.Templates[name] = template
}

// InjectPrompt sends a prompt to a single pane.
// agentType is used to handle agent-specific quirks (e.g., Codex needs double-Enter).
// This method satisfies the ensemble.BasicInjector interface.
func (p *PromptInjector) InjectPrompt(sessionPane, agentType, prompt string) error {
	p.logger().Info("[PromptInjector] inject_start",
		"session_pane", sessionPane,
		"agent_type", agentType,
		"prompt_len", len(prompt))

	if err := p.sendToPane(sessionPane, agentType, prompt); err != nil {
		p.logger().Error("[PromptInjector] inject_error",
			"session_pane", sessionPane,
			"agent_type", agentType,
			"error", err)
		return err
	}

	// Record success for adaptive delay learning
	p.recordSuccess(agentType)

	p.logger().Info("[PromptInjector] inject_complete",
		"session_pane", sessionPane,
		"agent_type", agentType,
		"success", true)

	return nil
}

// InjectPromptWithResult sends a prompt to a single pane and returns detailed result.
// agentType is used to handle agent-specific quirks (e.g., Codex needs double-Enter).
func (p *PromptInjector) InjectPromptWithResult(sessionPane, agentType, prompt string) (*InjectionResult, error) {
	start := time.Now()
	result := &InjectionResult{
		SessionPane: sessionPane,
		AgentType:   agentType,
		SentAt:      start,
	}

	if err := p.InjectPrompt(sessionPane, agentType, prompt); err != nil {
		result.Success = false
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}

	result.Success = true
	result.Duration = time.Since(start)
	return result, nil
}

// sendToPane sends a prompt to a specific pane, handling agent-specific quirks.
func (p *PromptInjector) sendToPane(sessionPane, agentType, prompt string) error {
	client := p.tmuxClient()

	// Use PasteKeys for reliable multi-line prompt delivery
	// Send without Enter first
	if err := client.PasteKeys(sessionPane, prompt, false); err != nil {
		return fmt.Errorf("send prompt text: %w", err)
	}

	// Wait before sending Enter
	time.Sleep(p.EnterDelay)

	// Send first Enter
	if err := client.SendKeys(sessionPane, "", true); err != nil {
		return fmt.Errorf("send first enter: %w", err)
	}

	// AGENT QUIRK: Codex and some other agents need double-Enter
	// The first Enter may not be recognized immediately
	if needsDoubleEnter(agentType) {
		time.Sleep(p.DoubleEnterDelay)
		if err := client.SendKeys(sessionPane, "", true); err != nil {
			return fmt.Errorf("send second enter: %w", err)
		}
	}

	return nil
}

// needsDoubleEnter returns true if the agent type requires double-Enter.
func needsDoubleEnter(agentType string) bool {
	switch agentType {
	case "cod", "codex":
		return true
	case "gmi", "gemini":
		// Gemini may also benefit from double-Enter in some cases
		return true
	default:
		return false
	}
}

// InjectBatch sends prompts to multiple panes with staggering.
// All targets receive the same prompt.
func (p *PromptInjector) InjectBatch(targets []InjectionTarget, prompt string) (*BatchInjectionResult, error) {
	return p.InjectBatchWithContext(context.Background(), targets, prompt)
}

// InjectBatchWithContext sends prompts to multiple panes with staggering and context support.
// All targets receive the same prompt. The operation can be cancelled via context.
// When UseAdaptiveDelay is true, delays are obtained from the RateLimitTracker.
func (p *PromptInjector) InjectBatchWithContext(ctx context.Context, targets []InjectionTarget, prompt string) (*BatchInjectionResult, error) {
	start := time.Now()
	result := &BatchInjectionResult{
		TotalPanes: len(targets),
		Results:    make([]InjectionResult, 0, len(targets)),
	}

	if len(targets) == 0 {
		return result, nil
	}

	p.logger().Info("[PromptInjector] batch_start",
		"total_targets", len(targets),
		"prompt_len", len(prompt),
		"adaptive_delay", p.UseAdaptiveDelay)

	for i, target := range targets {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			p.logger().Info("[PromptInjector] batch_cancelled",
				"sent", i,
				"total", len(targets),
				"reason", ctx.Err())
			return result, ctx.Err()
		default:
		}

		// Stagger sends (skip delay for first send)
		if i > 0 {
			delay := p.GetDelayForAgent(target.AgentType)
			if delay > 0 {
				p.logger().Debug("[PromptInjector] stagger_delay",
					"pane", target.SessionPane,
					"delay_ms", delay.Milliseconds(),
					"index", i,
					"total", len(targets),
					"adaptive", p.UseAdaptiveDelay)

				select {
				case <-ctx.Done():
					result.Duration = time.Since(start)
					return result, ctx.Err()
				case <-time.After(delay):
				}
			}
		}

		injResult, err := p.InjectPromptWithResult(target.SessionPane, target.AgentType, prompt)
		if err != nil {
			// Error already logged in InjectPrompt
			result.Failed++
		} else {
			result.Successful++
		}
		result.Results = append(result.Results, *injResult)

		p.logger().Info("[PromptInjector] batch_progress",
			"sent", i+1,
			"total", len(targets))
	}

	result.Duration = time.Since(start)

	p.logger().Info("[PromptInjector] batch_complete",
		"successful", result.Successful,
		"failed", result.Failed,
		"duration", result.Duration)

	return result, nil
}

// InjectSwarm sends marching orders to all panes in a SwarmPlan.
// Each pane receives the same prompt.
func (p *PromptInjector) InjectSwarm(plan *SwarmPlan, prompt string) (*BatchInjectionResult, error) {
	return p.InjectSwarmWithContext(context.Background(), plan, prompt)
}

// InjectSwarmWithContext sends marching orders to all panes in a SwarmPlan with context support.
// Each pane receives the same prompt. The operation can be cancelled via context.
func (p *PromptInjector) InjectSwarmWithContext(ctx context.Context, plan *SwarmPlan, prompt string) (*BatchInjectionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	// Build targets from plan
	client := p.tmuxClient()
	targetingCache := make(map[string]swarmSessionTargeting, len(plan.Sessions))
	var targets []InjectionTarget
	for _, sessionSpec := range plan.Sessions {
		targeting, targetingOK := targetingCache[sessionSpec.Name]
		if !targetingOK && ctx.Err() == nil {
			resolved, err := resolveSwarmSessionTargeting(ctx, client, sessionSpec.Name)
			if err != nil {
				p.logger().Warn("[PromptInjector] session_targeting_resolve_failed",
					"session", sessionSpec.Name,
					"error", err)
			} else {
				targeting = resolved
				targetingCache[sessionSpec.Name] = resolved
				targetingOK = true
			}
		}

		for _, paneSpec := range sessionSpec.Panes {
			sessionPane := formatPaneTarget(sessionSpec.Name, paneSpec.Index)
			if targetingOK {
				if resolved, err := swarmPaneTargetFromPlanIndex(sessionSpec.Name, targeting, paneSpec.Index); err == nil {
					sessionPane = resolved
				} else {
					p.logger().Warn("[PromptInjector] pane_target_resolve_failed",
						"session", sessionSpec.Name,
						"pane_index", paneSpec.Index,
						"error", err)
				}
			}

			target := InjectionTarget{
				SessionPane: sessionPane,
				AgentType:   paneSpec.AgentType,
			}
			targets = append(targets, target)
		}
	}

	p.logger().Info("[PromptInjector] swarm_inject_start",
		"total_sessions", len(plan.Sessions),
		"total_panes", len(targets))

	result, err := p.InjectBatchWithContext(ctx, targets, prompt)

	p.logger().Info("[PromptInjector] swarm_inject_complete",
		"successful", result.Successful,
		"failed", result.Failed,
		"duration", result.Duration)

	return result, err
}

// InjectSwarmWithTemplate sends marching orders using a named template.
func (p *PromptInjector) InjectSwarmWithTemplate(plan *SwarmPlan, templateName string) (*BatchInjectionResult, error) {
	prompt := p.GetTemplate(templateName)
	return p.InjectSwarm(plan, prompt)
}

// InjectToSession sends a prompt to all agent panes in a session.
// It fetches pane information from tmux and sends to all non-user panes.
func (p *PromptInjector) InjectToSession(session, prompt string) (*BatchInjectionResult, error) {
	return p.InjectToSessionWithContext(context.Background(), session, prompt)
}

// InjectToSessionWithContext sends a prompt to all agent panes in a session with context support.
// It fetches pane information from tmux and sends to all non-user panes.
func (p *PromptInjector) InjectToSessionWithContext(ctx context.Context, session, prompt string) (*BatchInjectionResult, error) {
	client := p.tmuxClient()

	panes, err := client.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("get panes for session %q: %w", session, err)
	}

	var targets []InjectionTarget
	for _, pane := range panes {
		// Skip user panes
		if pane.Type == tmux.AgentUser {
			continue
		}

		targets = append(targets, InjectionTarget{
			SessionPane: pane.ID,
			AgentType:   string(pane.Type),
		})
	}

	p.logger().Info("[PromptInjector] session_inject_start",
		"session", session,
		"agent_panes", len(targets))

	return p.InjectBatchWithContext(ctx, targets, prompt)
}
