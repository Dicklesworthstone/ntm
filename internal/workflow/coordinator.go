package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	index      int
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
		return &ReviewGateCoordinator{RuntimeCoordinator: base}, nil
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

// StartAt restores a checkpoint without replaying earlier transitions or
// mutating the reusable template. Only the saved stage's watchers start.
// Elapsed-time triggers retain their original start time, including downtime;
// later transitions receive the live clock rather than this startup clock.
func (c *RuntimeCoordinator) StartAt(ctx *TriggerContext, stage string, startedAt time.Time, nextByRole map[string]int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return errors.New("workflow coordinator is already started")
	}
	if ctx == nil || startedAt.IsZero() || startedAt.After(ctx.now()) {
		return errors.New("workflow checkpoint has an invalid stage start time")
	}
	validStage := c.template.Flow == nil && c.template.Coordination == CoordParallel && stage == ""
	if flow := c.template.Flow; flow != nil && stage != "" {
		validStage = flow.Initial == stage
		for _, candidate := range flow.Stages {
			validStage = validStage || candidate == stage
		}
		for _, tr := range flow.Transitions {
			validStage = validStage || tr.From == stage || tr.To == stage
		}
	}
	if !validStage {
		return fmt.Errorf("workflow checkpoint stage %q is not in the template", stage)
	}
	counts := make(map[string]int)
	for _, agent := range c.agents {
		counts[agent.Role]++
	}
	routing := make(map[string]int, len(nextByRole))
	for role, next := range nextByRole {
		if counts[role] == 0 || next < 0 {
			return fmt.Errorf("workflow checkpoint has invalid routing for role %q", role)
		}
		routing[role] = next % counts[role]
	}
	c.stage = stage
	c.nextByRole = routing
	c.triggerCtx = cloneTriggerContext(ctx)
	if c.template.Flow != nil {
		startCtx := cloneTriggerContext(ctx)
		startCtx.Now = func() time.Time { return startedAt }
		if err := c.startStageLocked(startCtx); err != nil {
			c.triggerCtx = nil
			return err
		}
	}
	c.started = true
	return nil
}

// RoutingState snapshots the round-robin cursors after choosing a stage's
// participants, so a resumed run neither changes those participants nor
// unfairly restarts every role at its first pane.
func (c *RuntimeCoordinator) RoutingState() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]int, len(c.nextByRole))
	for role, next := range c.nextByRole {
		result[role] = next
	}
	return result
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
		var fired bool
		if active.transition.Trigger.Type == TriggerAgentSays && ctx != nil && ctx.TransitionEvidence != nil {
			fired = ctx.TransitionEvidence[active.index]
		} else {
			var err error
			fired, err = active.trigger.Check(ctx)
			if err != nil {
				return false, fmt.Errorf("check %s trigger: %w", active.transition.Trigger.Type, err)
			}
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
	previousStage := c.stage
	previousCtx := cloneTriggerContext(c.triggerCtx)
	if err := c.stopTriggersLocked(); err != nil {
		return err
	}
	c.stage = transition.To
	c.triggerCtx = cloneTriggerContext(ctx)
	if err := c.startStageLocked(ctx); err != nil {
		// A failed destination trigger must not leave a coordinator marked as
		// started but without any active transitions. Restore the source stage
		// so an operator can correct the transient failure and retry the same
		// transition.
		c.stage = previousStage
		c.triggerCtx = previousCtx
		if restoreErr := c.startStageLocked(previousCtx); restoreErr != nil {
			c.started = false
			c.triggerCtx = nil
			return fmt.Errorf("activate stage %q: %w", transition.To, errors.Join(err, fmt.Errorf("restore source stage %q: %w", previousStage, restoreErr)))
		}
		return fmt.Errorf("activate stage %q: %w", transition.To, err)
	}
	return nil
}

func (c *RuntimeCoordinator) startStageLocked(ctx *TriggerContext) error {
	for index, transition := range c.template.Flow.Transitions {
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
		c.active = append(c.active, activeTransition{index: index, transition: transition, trigger: trigger})
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
	if ctx.TransitionEvidence != nil {
		clone.TransitionEvidence = make(map[int]bool, len(ctx.TransitionEvidence))
		for index, matched := range ctx.TransitionEvidence {
			clone.TransitionEvidence[index] = matched
		}
	}
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

// Start initializes a flowless parallel workflow. Parallel templates are
// intentionally valid without a FlowConfig: all declared participants start
// together, so there is no stage or trigger state machine to activate.
func (c *ParallelCoordinator) Start(ctx *TriggerContext) error {
	if c.template.Flow != nil {
		return c.RuntimeCoordinator.Start(ctx)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	c.stage = ""
	c.triggerCtx = cloneTriggerContext(ctx)
	c.started = true
	return nil
}

// Agents returns a copy so callers cannot mutate coordinator state.
func (c *ParallelCoordinator) Agents() []CoordinatorAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CoordinatorAgent(nil), c.agents...)
}

// ReviewGateCoordinator checks whether the approvals for one transition meet
// its configured any/all/quorum threshold.
type ReviewGateCoordinator struct {
	*RuntimeCoordinator
}

// CheckApprovals validates the exact set of agents whose output matched a
// transition during the current stage visit. transitionIndex refers to the
// template's complete Flow.Transitions slice, not only its outgoing transitions.
// The caller owns the stage-specific evidence; this method never retains votes
// between calls. Duplicate agent IDs count once, and an empty trigger role makes
// every workflow agent eligible.
func (c *ReviewGateCoordinator) CheckApprovals(transitionIndex int, agentIDs []string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return false, errors.New("workflow coordinator is not started")
	}
	flow := c.template.Flow
	if flow == nil || !flow.RequireApproval {
		return false, errors.New("workflow review gate does not require approval")
	}
	if transitionIndex < 0 || transitionIndex >= len(flow.Transitions) {
		return false, fmt.Errorf("workflow approval transition index %d is out of range", transitionIndex)
	}
	transition := flow.Transitions[transitionIndex]
	if transition.From != c.stage {
		return false, fmt.Errorf("workflow approval transition %d leaves stage %q, not current stage %q", transitionIndex, transition.From, c.stage)
	}
	if transition.Trigger.Type != TriggerAgentSays {
		return false, fmt.Errorf("workflow approval transition %d is not an agent_says trigger", transitionIndex)
	}

	role := transition.Trigger.Role
	reviewers := make(map[string]struct{})
	for _, agent := range c.agents {
		if role == "" || agent.Role == role {
			if strings.TrimSpace(agent.ID) == "" {
				return false, errors.New("workflow approver agent ID is required")
			}
			reviewers[agent.ID] = struct{}{}
		}
	}
	reviewerCount := len(reviewers)
	if reviewerCount == 0 {
		return false, errors.New("workflow review gate has no reviewer agents")
	}
	required := 1
	switch flow.ApprovalMode {
	case "", "any":
	case "all":
		required = reviewerCount
	case "quorum":
		required = flow.Quorum
		if required < 1 {
			return false, errors.New("workflow review gate quorum must be at least 1")
		}
	default:
		return false, fmt.Errorf("invalid workflow approval mode %q", flow.ApprovalMode)
	}
	if required > reviewerCount {
		return false, fmt.Errorf("workflow review gate requires %d approvals but has only %d reviewer agents", required, reviewerCount)
	}
	approvals := make(map[string]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		if strings.TrimSpace(agentID) == "" {
			return false, errors.New("reviewer agent ID is required")
		}
		if _, ok := reviewers[agentID]; !ok {
			if role == "" {
				return false, fmt.Errorf("agent %q is not part of this workflow", agentID)
			}
			return false, fmt.Errorf("agent %q does not hold approver role %q for this workflow", agentID, role)
		}
		approvals[agentID] = struct{}{}
	}
	return len(approvals) >= required, nil
}
