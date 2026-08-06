package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CoordinatorAgent is the runtime identity assigned to a workflow role.
type CoordinatorAgent struct {
	ID   string
	Role string
}

// Task is routed to an agent using its path (when present) and workflow routing rules.
type Task struct {
	ID   string
	Path string
}

// Coordinator drives a workflow template through its configured transitions.
type Coordinator interface {
	Start(*TriggerContext) error
	Stop() error
	CurrentStage() string
	Transition(string) error
	GetAgentForTask(Task) (CoordinatorAgent, error)
}

// RuntimeCoordinator owns transition triggers for a single workflow instance.
// It intentionally has no tmux dependency: callers supply current observations
// to Evaluate, keeping the engine usable by the CLI, API, and tests alike.
type RuntimeCoordinator struct {
	template *WorkflowTemplate
	agents   []CoordinatorAgent
	registry *TriggerRegistry

	mu         sync.Mutex
	started    bool
	stage      string
	active     []activeTransition
	nextByRole map[string]int
	triggerCtx *TriggerContext
}

type activeTransition struct {
	transition Transition
	trigger    RuntimeTrigger
}

// NewCoordinator builds the appropriate coordinator for a validated template.
func NewCoordinator(template *WorkflowTemplate, agents []CoordinatorAgent, registry *TriggerRegistry) (Coordinator, error) {
	if template == nil {
		return nil, errors.New("workflow template is required")
	}
	if err := template.Validate(); err != nil {
		return nil, fmt.Errorf("validate workflow template: %w", err)
	}
	if registry == nil {
		registry = NewTriggerRegistry()
	}
	base := &RuntimeCoordinator{template: template, agents: append([]CoordinatorAgent(nil), agents...), registry: registry, nextByRole: make(map[string]int)}
	switch template.Coordination {
	case CoordPingPong:
		return &PingPongCoordinator{RuntimeCoordinator: base}, nil
	case CoordPipeline:
		return &PipelineCoordinator{RuntimeCoordinator: base}, nil
	case CoordParallel:
		return &ParallelCoordinator{RuntimeCoordinator: base}, nil
	case CoordReviewGate:
		return &ReviewGateCoordinator{RuntimeCoordinator: base, approvals: make(map[string]struct{})}, nil
	default:
		return nil, fmt.Errorf("unsupported coordination type %q", template.Coordination)
	}
}

// Start activates the triggers that leave the initial stage.
func (c *RuntimeCoordinator) Start(ctx *TriggerContext) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	if c.template.Flow == nil {
		return errors.New("workflow flow is required")
	}
	stage := c.template.Flow.Initial
	if stage == "" && len(c.template.Flow.Stages) > 0 {
		stage = c.template.Flow.Stages[0]
	}
	if stage == "" {
		return errors.New("workflow has no initial stage")
	}
	c.stage = stage
	c.triggerCtx = cloneTriggerContext(ctx)
	if err := c.startStageLocked(ctx); err != nil {
		c.triggerCtx = nil
		return err
	}
	c.started = true
	return nil
}

// Stop stops all active trigger watchers. It may be called repeatedly.
func (c *RuntimeCoordinator) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.stopTriggersLocked()
	c.started = false
	c.triggerCtx = nil
	return err
}

func (c *RuntimeCoordinator) CurrentStage() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stage
}

// Evaluate checks active triggers against a point-in-time observation and
// advances at most one transition. Calling it after Stop is an error.
func (c *RuntimeCoordinator) Evaluate(ctx *TriggerContext) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return false, errors.New("workflow coordinator is not started")
	}
	for _, active := range c.active {
		fired, err := active.trigger.Check(ctx)
		if err != nil {
			return false, fmt.Errorf("check %s trigger: %w", active.transition.Trigger.Type, err)
		}
		if fired {
			if err := c.transitionLocked(ctx, active.transition); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// Transition advances using a configured trigger type or manual trigger label.
func (c *RuntimeCoordinator) Transition(trigger string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return errors.New("workflow coordinator is not started")
	}
	for _, active := range c.active {
		if trigger == string(active.transition.Trigger.Type) || (active.transition.Trigger.Type == TriggerManual && trigger == active.transition.Trigger.Label) {
			return c.transitionLocked(c.triggerCtx, active.transition)
		}
	}
	return fmt.Errorf("no transition from %q is triggered by %q", c.stage, trigger)
}

func (c *RuntimeCoordinator) transitionLocked(ctx *TriggerContext, transition Transition) error {
	if err := c.stopTriggersLocked(); err != nil {
		return err
	}
	c.stage = transition.To
	c.triggerCtx = cloneTriggerContext(ctx)
	return c.startStageLocked(ctx)
}

func (c *RuntimeCoordinator) startStageLocked(ctx *TriggerContext) error {
	for _, transition := range c.template.Flow.Transitions {
		if transition.From != c.stage {
			continue
		}
		trigger, err := c.registry.Create(transition.Trigger)
		if err != nil {
			_ = c.stopTriggersLocked()
			return fmt.Errorf("create %s trigger: %w", transition.Trigger.Type, err)
		}
		if err := trigger.Start(ctx); err != nil {
			_ = trigger.Stop()
			_ = c.stopTriggersLocked()
			return fmt.Errorf("start %s trigger: %w", transition.Trigger.Type, err)
		}
		c.active = append(c.active, activeTransition{transition: transition, trigger: trigger})
	}
	return nil
}

func cloneTriggerContext(ctx *TriggerContext) *TriggerContext {
	if ctx == nil {
		return nil
	}
	clone := *ctx
	clone.Outputs = append([]AgentOutput(nil), ctx.Outputs...)
	clone.Activities = append([]AgentActivity(nil), ctx.Activities...)
	return &clone
}

func (c *RuntimeCoordinator) stopTriggersLocked() error {
	var first error
	for _, active := range c.active {
		if err := active.trigger.Stop(); err != nil && first == nil {
			first = err
		}
	}
	c.active = nil
	return first
}

// GetAgentForTask applies routing patterns, then assigns an agent from the
// current stage role using round-robin selection.
func (c *RuntimeCoordinator) GetAgentForTask(task Task) (CoordinatorAgent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	role := c.stage
	patterns := make([]string, 0, len(c.template.Routing))
	for pattern := range c.template.Routing {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if matchesTaskPath(pattern, task.Path) {
			role = c.template.Routing[pattern]
			break
		}
	}
	var candidates []CoordinatorAgent
	for _, agent := range c.agents {
		if agent.Role == role {
			candidates = append(candidates, agent)
		}
	}
	if len(candidates) == 0 {
		return CoordinatorAgent{}, fmt.Errorf("no agent assigned to workflow role %q", role)
	}
	next := c.nextByRole[role] % len(candidates)
	c.nextByRole[role]++
	return candidates[next], nil
}

func matchesTaskPath(pattern, path string) bool {
	if path == "" {
		return false
	}
	matched, err := filepath.Match(pattern, path)
	if err == nil && matched {
		return true
	}
	matched, _ = filepath.Match(pattern, filepath.Base(path))
	return matched || strings.EqualFold(pattern, path)
}

// PingPongCoordinator alternates between roles according to configured transitions.
type PingPongCoordinator struct{ *RuntimeCoordinator }

// PipelineCoordinator advances ordered workflow stages through their transitions.
type PipelineCoordinator struct{ *RuntimeCoordinator }

// ParallelCoordinator exposes all participants for a parallel workflow.
type ParallelCoordinator struct{ *RuntimeCoordinator }

// Agents returns a copy so callers cannot mutate coordinator state.
func (c *ParallelCoordinator) Agents() []CoordinatorAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CoordinatorAgent(nil), c.agents...)
}

// ReviewGateCoordinator records approvals and transitions once the configured
// any/all/quorum threshold is reached.
type ReviewGateCoordinator struct {
	*RuntimeCoordinator
	approvals map[string]struct{}
}

// Approve records a reviewer approval. Duplicate approvals are idempotent.
func (c *ReviewGateCoordinator) Approve(agentID string) (bool, error) {
	if strings.TrimSpace(agentID) == "" {
		return false, errors.New("reviewer agent ID is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return false, errors.New("workflow coordinator is not started")
	}
	c.approvals[agentID] = struct{}{}
	mode := c.template.Flow.ApprovalMode
	if mode == "" {
		mode = "any"
	}
	reviewerCount := 0
	for _, agent := range c.agents {
		if agent.Role == "reviewer" {
			reviewerCount++
		}
	}
	required := 1
	if mode == "all" {
		required = reviewerCount
	} else if mode == "quorum" {
		required = c.template.Flow.Quorum
	}
	return len(c.approvals) >= required, nil
}
