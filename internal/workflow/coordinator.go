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

// FileTriggerStates reconciles live filesystem changes and returns a detached
// checkpoint. Runners save this evidence before supplying it to Evaluate;
// events arriving after the snapshot are considered on the following poll.
func (c *RuntimeCoordinator) FileTriggerStates() (map[int]FileTriggerState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil, errors.New("workflow coordinator is not started")
	}
	states := make(map[int]FileTriggerState)
	for _, active := range c.active {
		if !isFileTrigger(active.transition.Trigger.Type) {
			continue
		}
		file, ok := active.trigger.(*fileTrigger)
		if !ok {
			return nil, fmt.Errorf("file trigger %d does not support durable checkpoints", active.index)
		}
		if _, err := file.Check(nil); err != nil {
			return nil, fmt.Errorf("checkpoint file trigger %d: %w", active.index, err)
		}
		states[active.index] = file.fileState()
	}
	return states, nil
}

// ValidateFileTriggerStates checks a complete saved set without starting any
// watchers. A nil set is invalid when the stage has file transitions; callers
// that are deliberately starting a new attempt omit restoration instead.
func ValidateFileTriggerStates(template *WorkflowTemplate, projectRoot, stage string, states map[int]FileTriggerState, excludes []string) error {
	if template == nil {
		return errors.New("workflow template is required for file trigger evidence")
	}
	expected := make(map[int]Transition)
	if template.Flow != nil {
		for index, transition := range template.Flow.Transitions {
			if transition.From == stage && isFileTrigger(transition.Trigger.Type) {
				expected[index] = transition
			}
		}
	}
	if len(states) != len(expected) {
		return fmt.Errorf("workflow stage %q has incomplete or extraneous file trigger evidence", stage)
	}
	if len(expected) == 0 {
		return nil
	}
	normalized, err := normalizeFileTriggerExcludes(excludes)
	if err != nil {
		return err
	}
	root, err := canonicalFileTriggerRoot(projectRoot)
	if err != nil {
		return err
	}
	for index, transition := range expected {
		state, ok := states[index]
		if !ok {
			return fmt.Errorf("workflow stage %q is missing evidence for file trigger %d", stage, index)
		}
		if err := validateFileTriggerState(state, root, stage, index, transition.Trigger.Type, transition.Trigger.Pattern, normalized); err != nil {
			return err
		}
	}
	return nil
}

func isFileTrigger(kind TriggerType) bool {
	return kind == TriggerFileCreated || kind == TriggerFileModified
}

// RestartStage refreshes triggers for a deliberately retried delivery. File
// baselines start again, while elapsed-time triggers keep the original visit
// clock. Routing cursors are untouched. A failed refresh restores the old
// baseline and latched observations rather than losing the waiting work.
func (c *RuntimeCoordinator) RestartStage(ctx *TriggerContext, startedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return errors.New("workflow coordinator is not started")
	}
	if ctx == nil || startedAt.IsZero() || startedAt.After(ctx.now()) {
		return errors.New("workflow stage restart has an invalid start time")
	}
	return c.activateStageLocked(ctx, c.stage, startedAt)
}

// StopWithFileTriggerStates closes the last checkpoint race: reconcile while
// watchers run, stop and join all of them, then copy their final sticky latches.
// A notification already observed after the scan remains durable even if the
// file has disappeared. Events never observed while offline are not invented.
func (c *RuntimeCoordinator) StopWithFileTriggerStates() (map[int]FileTriggerState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.active
	var errs []error
	for _, transition := range active {
		if file, ok := transition.trigger.(*fileTrigger); ok {
			if _, err := file.Check(nil); err != nil {
				errs = append(errs, fmt.Errorf("checkpoint file trigger %d: %w", transition.index, err))
			}
		} else if isFileTrigger(transition.transition.Trigger.Type) {
			errs = append(errs, fmt.Errorf("file trigger %d does not support durable checkpoints", transition.index))
		}
	}
	if err := c.stopTriggersLocked(); err != nil {
		errs = append(errs, err)
	}
	c.started = false
	c.triggerCtx = nil
	var states map[int]FileTriggerState
	for _, transition := range active {
		if file, ok := transition.trigger.(*fileTrigger); ok {
			if states == nil {
				states = make(map[int]FileTriggerState)
			}
			states[transition.index] = file.fileState()
		}
	}
	return states, errors.Join(errs...)
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
	if err := ctx.checkErr(); err != nil {
		return false, err
	}
	checkCtx := ctx
	if ctx != nil && ctx.CheckContext != nil {
		observation := *ctx
		observation.Context = ctx.CheckContext
		observation.CheckContext = nil
		checkCtx = &observation
	}
	if ctx != nil && ctx.FileTriggerStates != nil {
		if err := ValidateFileTriggerStates(c.template, ctx.ProjectRoot, c.stage, ctx.FileTriggerStates, ctx.FileTriggerExcludes); err != nil {
			return false, err
		}
	}
	for _, active := range c.active {
		var fired bool
		if active.transition.Trigger.Type == TriggerAgentSays && ctx != nil && ctx.TransitionEvidence != nil {
			fired = ctx.TransitionEvidence[active.index]
		} else if isFileTrigger(active.transition.Trigger.Type) && ctx != nil && ctx.FileTriggerStates != nil {
			fired = ctx.FileTriggerStates[active.index].Fired
		} else {
			var err error
			fired, err = active.trigger.Check(checkCtx)
			if err != nil {
				if cause := ctx.checkErr(); cause != nil {
					return false, cause
				}
				return false, fmt.Errorf("check %s trigger: %w", active.transition.Trigger.Type, err)
			}
		}
		// A trigger may finish at its deadline or ignore cancellation. Its
		// result cannot authorize a late transition, including when a saved
		// receipt bypassed Check entirely.
		if err := ctx.checkErr(); err != nil {
			return false, err
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

// AdvanceManual selects one exact active manual edge. It cannot acknowledge an
// agent_says verdict or fall through to a similarly labelled automatic edge.
func (c *RuntimeCoordinator) AdvanceManual(ctx *TriggerContext, transitionIndex int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return errors.New("workflow coordinator is not started")
	}
	if ctx == nil {
		return errors.New("workflow manual transition context is required")
	}
	if err := ctx.checkErr(); err != nil {
		return err
	}
	for _, active := range c.active {
		if active.index != transitionIndex {
			continue
		}
		if active.transition.Trigger.Type != TriggerManual {
			return fmt.Errorf("transition %d is not a manual transition", transitionIndex)
		}
		return c.transitionLocked(ctx, active.transition)
	}
	return fmt.Errorf("transition %d is not active at workflow stage %q", transitionIndex, c.stage)
}

func (c *RuntimeCoordinator) transitionLocked(ctx *TriggerContext, transition Transition) error {
	return c.activateStageLocked(ctx, transition.To, time.Time{})
}

func (c *RuntimeCoordinator) activateStageLocked(ctx *TriggerContext, stage string, startedAt time.Time) error {
	previousStage := c.stage
	previousCtx := cloneTriggerContext(c.triggerCtx)
	previousActive := c.active
	if err := c.stopTriggersLocked(); err != nil {
		return err
	}
	// Snapshot after Stop has joined every notification reader, so its final
	// latches survive a failed destination startup. No new filesystem scan is
	// needed here; the preserved baseline reconciles changes during rollback.
	previousFiles := make(map[int]FileTriggerState)
	for _, active := range previousActive {
		if file, ok := active.trigger.(*fileTrigger); ok {
			previousFiles[active.index] = file.fileState()
		}
	}
	if previousCtx != nil {
		previousCtx.FileTriggerStates = previousFiles
	}
	c.stage = stage
	nextCtx := cloneTriggerContext(ctx)
	if nextCtx != nil {
		nextCtx.CheckContext = nil
		nextCtx.FileTriggerStates = nil
		nextCtx.TransitionEvidence = nil
	}
	c.triggerCtx = cloneTriggerContext(nextCtx)
	startCtx := cloneTriggerContext(nextCtx)
	if !startedAt.IsZero() {
		startCtx.Now = func() time.Time { return startedAt }
	}
	if err := c.startStageLocked(startCtx); err != nil {
		// A failed destination trigger must not leave a coordinator marked as
		// started but without any active transitions. Restore the source stage
		// so an operator can correct the transient failure and retry the same
		// transition.
		c.stage = previousStage
		c.triggerCtx = previousCtx
		if restoreErr := c.startStageLocked(previousCtx); restoreErr != nil {
			c.started = false
			c.triggerCtx = nil
			return fmt.Errorf("activate stage %q: %w", stage, errors.Join(err, fmt.Errorf("restore source stage %q: %w", previousStage, restoreErr)))
		}
		return fmt.Errorf("activate stage %q: %w", stage, err)
	}
	return nil
}

func (c *RuntimeCoordinator) startStageLocked(ctx *TriggerContext) error {
	if ctx != nil && ctx.FileTriggerStates != nil {
		if err := ValidateFileTriggerStates(c.template, ctx.ProjectRoot, c.stage, ctx.FileTriggerStates, ctx.FileTriggerExcludes); err != nil {
			return err
		}
	}
	if c.template.Flow == nil {
		return nil
	}
	for index, transition := range c.template.Flow.Transitions {
		if transition.From != c.stage {
			continue
		}
		trigger, err := c.registry.Create(transition.Trigger)
		if err != nil {
			_ = c.stopTriggersLocked()
			return fmt.Errorf("create %s trigger: %w", transition.Trigger.Type, err)
		}
		if file, ok := trigger.(*fileTrigger); ok {
			file.stage, file.transitionIndex = c.stage, index
			if ctx != nil && ctx.FileTriggerStates != nil {
				state := ctx.FileTriggerStates[index]
				state.Baseline = cloneFileTriggerBaseline(state.Baseline)
				state.Excludes = append([]string(nil), state.Excludes...)
				file.restore = &state
			}
		} else if isFileTrigger(transition.Trigger.Type) && ctx != nil && ctx.FileTriggerStates != nil {
			_ = c.stopTriggersLocked()
			return fmt.Errorf("file trigger %d does not support checkpoint restoration", index)
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
	clone.FileTriggerStates = cloneFileTriggerStates(ctx.FileTriggerStates)
	clone.FileTriggerExcludes = append([]string(nil), ctx.FileTriggerExcludes...)
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
