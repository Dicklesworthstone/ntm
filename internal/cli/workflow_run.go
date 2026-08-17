package cli

// workflow_run.go wires the workflow RuntimeCoordinator (internal/workflow)
// to a live tmux session: `ntm workflow run <name-or-path>` resolves a
// template (builtin, user/project dir, or explicit TOML path), maps workflow
// roles onto the session's agent panes, and drives the coordinator's trigger
// loop, delivering every stage prompt through the same gated dispatch path
// `ntm send` uses (liveness gate + composer-verified delivery). This is the
// C2 wire (bd-ws2-wire-or-delete-ykmcz.2): the engine may not exist without
// this surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// workflowRunCaptureLines is how much pane scrollback feeds agent_says and
// idle observation on each poll.
const workflowRunCaptureLines = 200

// WorkflowRunAgent reports one role→pane assignment in the run result.
type WorkflowRunAgent struct {
	Role string `json:"role"`
	Pane string `json:"pane"`
}

// WorkflowRunResult is the JSON output for `ntm workflow run`.
type WorkflowRunResult struct {
	Success      bool               `json:"success"`
	Workflow     string             `json:"workflow"`
	Source       string             `json:"source"`
	Session      string             `json:"session"`
	Coordination string             `json:"coordination"`
	Agents       []WorkflowRunAgent `json:"agents"`
	Stages       []string           `json:"stages"`
	Transitions  int                `json:"transitions"`
	Completed    bool               `json:"completed"`
	Reason       string             `json:"reason"`
	Error        string             `json:"error,omitempty"`
}

// workflowRunPorts isolates the runner's side effects so unit tests can drive
// the coordinator loop with fake dispatch/capture implementations.
type workflowRunPorts struct {
	// dispatch delivers one prompt to one pane and fails when delivery or
	// submission verification fails.
	dispatch func(ctx context.Context, session, paneID, prompt string) error
	// capture returns recent pane output for trigger observation.
	capture func(paneID string, lines int) (string, error)
	// notify surfaces progress/warnings on the human path.
	notify func(format string, args ...any)
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration)
}

func (p *workflowRunPorts) fillDefaults() {
	if p.notify == nil {
		p.notify = func(string, ...any) {}
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.sleep == nil {
		p.sleep = func(ctx context.Context, d time.Duration) {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
	}
}

// workflowRunOptions configures one workflow run.
type workflowRunOptions struct {
	Session        string
	ProjectRoot    string
	Vars           map[string]string
	MaxTransitions int
	Interval       time.Duration
	FireManual     bool
	TriggerTimeout time.Duration
	StateDir       string // defaults to <ProjectRoot>/.ntm/workflows/state
}

// workflowRunner drives one coordinator instance against a live session.
type workflowRunner struct {
	template    *workflow.WorkflowTemplate
	agents      []workflow.CoordinatorAgent
	coordinator workflow.Coordinator
	opts        workflowRunOptions
	ports       workflowRunPorts

	store *workflow.StateStore
	state *workflow.WorkflowState

	errorHandler   *workflow.ErrorHandler
	timeoutMonitor *workflow.TimeoutMonitor

	mu           sync.Mutex
	manuals      []*workflow.ManualTrigger
	lastCapture  map[string]string
	lastActivity map[string]time.Time
	turn         int
	stopReason   string
	stopErr      error
	cancel       context.CancelFunc

	result WorkflowRunResult
}

// newWorkflowRunner validates the template against the pane assignment and
// builds the coordinator with a trigger registry that tracks manual triggers
// (for --fire-manual) and applies the configured command-trigger timeout.
func newWorkflowRunner(template *workflow.WorkflowTemplate, agents []workflow.CoordinatorAgent, opts workflowRunOptions, ports workflowRunPorts) (*workflowRunner, error) {
	ports.fillDefaults()
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.MaxTransitions <= 0 {
		opts.MaxTransitions = 8
	}
	r := &workflowRunner{
		template:     template,
		agents:       append([]workflow.CoordinatorAgent(nil), agents...),
		opts:         opts,
		ports:        ports,
		lastCapture:  make(map[string]string),
		lastActivity: make(map[string]time.Time),
	}

	defaults := workflow.NewTriggerRegistry()
	registry := workflow.NewTriggerRegistry()
	registry.Register(workflow.TriggerManual, func(cfg workflow.Trigger) (workflow.RuntimeTrigger, error) {
		trig, err := defaults.Create(cfg)
		if err != nil {
			return nil, err
		}
		if manual, ok := trig.(*workflow.ManualTrigger); ok {
			r.mu.Lock()
			r.manuals = append(r.manuals, manual)
			r.mu.Unlock()
		}
		return trig, nil
	})
	if opts.TriggerTimeout > 0 {
		for _, kind := range []workflow.TriggerType{workflow.TriggerCommandSuccess, workflow.TriggerCommandFailure} {
			kind := kind
			registry.Register(kind, func(cfg workflow.Trigger) (workflow.RuntimeTrigger, error) {
				trig, err := defaults.Create(cfg)
				if err != nil {
					return nil, err
				}
				if limiter, ok := trig.(interface{ SetTimeout(time.Duration) }); ok {
					limiter.SetTimeout(opts.TriggerTimeout)
				}
				return trig, nil
			})
		}
	}

	coord, err := workflow.NewCoordinator(template, agents, registry)
	if err != nil {
		return nil, err
	}
	r.coordinator = coord

	if dir := strings.TrimSpace(opts.StateDir); dir != "" {
		r.store = &workflow.StateStore{Dir: dir}
	}
	return r, nil
}

// stageRole resolves which workflow role acts in a stage. Resolution order
// (documented on the command): exact role match, template routing keyed by
// the stage name, the role named by an outgoing transition trigger, then the
// first declared agent role.
func (r *workflowRunner) stageRole(stage string) string {
	for _, agent := range r.template.Agents {
		if agent.Role == stage {
			return stage
		}
	}
	if role, ok := r.template.Routing[stage]; ok {
		return role
	}
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage && tr.Trigger.Role != "" {
				return tr.Trigger.Role
			}
		}
	}
	if len(r.template.Agents) > 0 {
		return r.template.Agents[0].Role
	}
	return stage
}

// stageIsTerminal reports whether no transition leaves the stage.
func (r *workflowRunner) stageIsTerminal(stage string) bool {
	if r.template.Flow == nil {
		return true
	}
	for _, tr := range r.template.Flow.Transitions {
		if tr.From == stage {
			return false
		}
	}
	return true
}

// stagePanes selects the panes a stage dispatches to. When the acting role
// matches the stage name and the stage is not parallel, selection goes
// through the coordinator's routing-aware round-robin (GetAgentForTask);
// otherwise every agent holding the acting role participates.
func (r *workflowRunner) stagePanes(stage string) ([]workflow.CoordinatorAgent, error) {
	role := r.stageRole(stage)
	parallelStage := r.template.Coordination == workflow.CoordParallel ||
		(r.template.Flow != nil && r.template.Flow.ParallelWithinStage)
	if role == stage && !parallelStage {
		agent, err := r.coordinator.GetAgentForTask(workflow.Task{ID: stage, Path: stage})
		if err != nil {
			return nil, err
		}
		return []workflow.CoordinatorAgent{agent}, nil
	}
	var members []workflow.CoordinatorAgent
	for _, agent := range r.agents {
		if agent.Role == role {
			members = append(members, agent)
		}
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("workflow stage %q resolves to role %q but no pane holds that role", stage, role)
	}
	return members, nil
}

// triggerHint describes how a transition advances, WITHOUT quoting agent_says
// patterns: quoting the pattern into a pane transcript would satisfy the
// trigger with our own dispatch echo.
func triggerHint(tr workflow.Transition) string {
	switch tr.Trigger.Type {
	case workflow.TriggerFileCreated:
		return fmt.Sprintf("create a file matching %s to advance to %s", tr.Trigger.Pattern, tr.To)
	case workflow.TriggerFileModified:
		return fmt.Sprintf("modify a file matching %s to advance to %s", tr.Trigger.Pattern, tr.To)
	case workflow.TriggerCommandSuccess:
		return fmt.Sprintf("make `%s` succeed to advance to %s", tr.Trigger.Command, tr.To)
	case workflow.TriggerCommandFailure:
		return fmt.Sprintf("`%s` failing advances to %s", tr.Trigger.Command, tr.To)
	case workflow.TriggerAgentSays:
		who := "an agent"
		if tr.Trigger.Role != "" {
			who = "the " + tr.Trigger.Role + " agent"
		}
		return fmt.Sprintf("%s stating the configured verdict phrase advances to %s", who, tr.To)
	case workflow.TriggerAllAgentsIdle:
		return fmt.Sprintf("%dm of idle advances to %s", tr.Trigger.IdleMinutes, tr.To)
	case workflow.TriggerManual:
		label := tr.Trigger.Label
		if label == "" {
			label = "manual"
		}
		return fmt.Sprintf("operator trigger %q advances to %s", label, tr.To)
	case workflow.TriggerTimeElapsed:
		return fmt.Sprintf("%dm elapsed advances to %s", tr.Trigger.Minutes, tr.To)
	default:
		return string(tr.Trigger.Type)
	}
}

// stagePrompt composes the prompt delivered when a stage begins.
func (r *workflowRunner) stagePrompt(stage string, agent workflow.CoordinatorAgent, turn int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[ntm workflow %s] stage %s turn %d\n", r.template.Name, stageLabel(stage), turn)
	fmt.Fprintf(&b, "role: %s\n", agent.Role)
	if r.template.Description != "" {
		fmt.Fprintf(&b, "goal: %s\n", r.template.Description)
	}
	for _, wa := range r.template.Agents {
		if wa.Role == agent.Role && wa.Description != "" {
			fmt.Fprintf(&b, "your job: %s\n", wa.Description)
			break
		}
	}
	if len(r.opts.Vars) > 0 {
		keys := make([]string, 0, len(r.opts.Vars))
		for key := range r.opts.Vars {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("context:\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", key, r.opts.Vars[key])
		}
	}
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage {
				fmt.Fprintf(&b, "advance: %s\n", triggerHint(tr))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func stageLabel(stage string) string {
	if stage == "" {
		return "(parallel)"
	}
	return stage
}

// dispatchStage delivers the stage prompt through the gated dispatch port.
func (r *workflowRunner) dispatchStage(ctx context.Context, stage string, targets []workflow.CoordinatorAgent) error {
	for _, agent := range targets {
		r.mu.Lock()
		r.turn++
		turn := r.turn
		r.mu.Unlock()
		prompt := r.stagePrompt(stage, agent, turn)
		r.ports.notify("workflow %s: stage %s → %s (%s), turn %d", r.template.Name, stageLabel(stage), agent.ID, agent.Role, turn)
		if err := r.ports.dispatch(ctx, r.opts.Session, agent.ID, prompt); err != nil {
			return fmt.Errorf("dispatch stage %q to pane %s (%s): %w", stageLabel(stage), agent.ID, agent.Role, err)
		}
	}
	return nil
}

// observe builds the trigger context from live pane captures. Activity
// timestamps derive from observed content changes between polls.
func (r *workflowRunner) observe(ctx context.Context) *workflow.TriggerContext {
	tctx := &workflow.TriggerContext{
		Context:     ctx,
		ProjectRoot: r.opts.ProjectRoot,
		Session:     r.opts.Session,
		Now:         r.ports.now,
	}
	now := r.ports.now()
	for _, agent := range r.agents {
		text, err := r.ports.capture(agent.ID, workflowRunCaptureLines)
		if err != nil {
			r.ports.notify("workflow %s: capture pane %s: %v", r.template.Name, agent.ID, err)
			continue
		}
		r.mu.Lock()
		if r.lastCapture[agent.ID] != text {
			r.lastCapture[agent.ID] = text
			r.lastActivity[agent.ID] = now
		}
		last := r.lastActivity[agent.ID]
		r.mu.Unlock()
		tctx.Outputs = append(tctx.Outputs, workflow.AgentOutput{Role: agent.Role, Text: text})
		tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: last})
	}
	return tctx
}

// applyReviewGate wires ReviewGateCoordinator.Approve as the approval
// threshold: reviewer outputs matching an approval transition's pattern are
// recorded as approvals, and until the configured any/all/quorum threshold is
// met those matching outputs are withheld from trigger evaluation so the
// approval transition cannot fire early.
func (r *workflowRunner) applyReviewGate(tctx *workflow.TriggerContext) *workflow.TriggerContext {
	gate, ok := r.coordinator.(*workflow.ReviewGateCoordinator)
	if !ok || r.template.Flow == nil || !r.template.Flow.RequireApproval {
		return tctx
	}
	stage := r.coordinator.CurrentStage()
	var approvalTriggers []workflow.Trigger
	for _, tr := range r.template.Flow.Transitions {
		if tr.From == stage && tr.Trigger.Type == workflow.TriggerAgentSays && tr.Trigger.Role == "reviewer" && r.stageIsTerminal(tr.To) {
			approvalTriggers = append(approvalTriggers, tr.Trigger)
		}
	}
	if len(approvalTriggers) == 0 {
		return tctx
	}
	met := false
	registry := workflow.NewTriggerRegistry()
	for _, agent := range r.agents {
		if agent.Role != "reviewer" {
			continue
		}
		text := ""
		r.mu.Lock()
		text = r.lastCapture[agent.ID]
		r.mu.Unlock()
		for _, trigger := range approvalTriggers {
			probe, err := registry.Create(trigger)
			if err != nil {
				continue
			}
			fired, err := probe.Check(&workflow.TriggerContext{Outputs: []workflow.AgentOutput{{Role: "reviewer", Text: text}}})
			if err != nil || !fired {
				continue
			}
			ok, err := gate.Approve(agent.ID)
			if err != nil {
				r.ports.notify("workflow %s: approval from %s rejected: %v", r.template.Name, agent.ID, err)
				continue
			}
			r.ports.notify("workflow %s: recorded approval from reviewer %s", r.template.Name, agent.ID)
			if ok {
				met = true
			}
		}
	}
	if met {
		return tctx
	}
	// Threshold not reached: withhold reviewer outputs that would satisfy an
	// approval trigger so Evaluate cannot advance past the gate.
	filtered := *tctx
	filtered.Outputs = nil
	for _, output := range tctx.Outputs {
		blocked := false
		if output.Role == "reviewer" {
			for _, trigger := range approvalTriggers {
				probe, err := registry.Create(trigger)
				if err != nil {
					continue
				}
				if fired, err := probe.Check(&workflow.TriggerContext{Outputs: []workflow.AgentOutput{output}}); err == nil && fired {
					blocked = true
					break
				}
			}
		}
		if !blocked {
			filtered.Outputs = append(filtered.Outputs, output)
		}
	}
	return &filtered
}

// fireManualTriggers fires every manual trigger the registry has created.
// Stale instances from completed stages are inert: the coordinator never
// re-checks a stopped stage's triggers.
func (r *workflowRunner) fireManualTriggers() {
	r.mu.Lock()
	manuals := append([]*workflow.ManualTrigger(nil), r.manuals...)
	r.mu.Unlock()
	for _, manual := range manuals {
		if manual.Fire() {
			label := manual.Label()
			if label == "" {
				label = "manual"
			}
			r.ports.notify("workflow %s: fired manual trigger %q (--fire-manual)", r.template.Name, label)
		}
	}
}

// workflowRunActions adapts the template's error_handling policy to this
// runner. Actions the runner cannot honestly perform (restart_agent,
// skip-with-no-transition) degrade to a notification.
type workflowRunActions struct{ r *workflowRunner }

func (a workflowRunActions) RestartAgent(_ context.Context, agentID string) error {
	a.r.ports.notify("workflow %s: restart_agent is not supported by `ntm workflow run`; pane %s left as-is (use `ntm restart`)", a.r.template.Name, agentID)
	return nil
}

func (a workflowRunActions) Pause(_ context.Context, reason string) error {
	a.r.mu.Lock()
	state, store := a.r.state, a.r.store
	a.r.mu.Unlock()
	if store != nil && state != nil {
		if err := store.Pause(state, reason, a.r.ports.now()); err != nil {
			a.r.ports.notify("workflow %s: persist pause: %v", a.r.template.Name, err)
		}
	}
	a.r.stop("paused", fmt.Errorf("workflow paused: %s", reason))
	return nil
}

func (a workflowRunActions) SkipStage(_ context.Context) error {
	stage := a.r.coordinator.CurrentStage()
	if a.r.template.Flow != nil {
		for _, tr := range a.r.template.Flow.Transitions {
			if tr.From == stage {
				trigger := string(tr.Trigger.Type)
				if tr.Trigger.Type == workflow.TriggerManual && tr.Trigger.Label != "" {
					trigger = tr.Trigger.Label
				}
				if err := a.r.coordinator.Transition(trigger); err == nil {
					a.r.ports.notify("workflow %s: skipped stage %s via %s", a.r.template.Name, stage, trigger)
					return nil
				}
			}
		}
	}
	a.r.ports.notify("workflow %s: skip_stage found no outgoing transition from %s", a.r.template.Name, stage)
	return nil
}

func (a workflowRunActions) Abort(_ context.Context, err error) error {
	a.r.stop("aborted", err)
	return nil
}

func (a workflowRunActions) RetryStage(ctx context.Context) error {
	stage := a.r.coordinator.CurrentStage()
	targets, err := a.r.stagePanes(stage)
	if err != nil {
		return err
	}
	a.r.ports.notify("workflow %s: retrying stage %s", a.r.template.Name, stage)
	return a.r.dispatchStage(ctx, stage, targets)
}

func (a workflowRunActions) Notify(_ context.Context, werr *workflow.WorkflowError, subject string) error {
	a.r.ports.notify("workflow %s: %s: %v", a.r.template.Name, subject, werr)
	return nil
}

// stop records the terminal reason once and cancels the run loop.
func (r *workflowRunner) stop(reason string, err error) {
	r.mu.Lock()
	if r.stopReason == "" {
		r.stopReason = reason
		r.stopErr = err
	}
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *workflowRunner) stopped() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopReason, r.stopErr
}

// recordTransition persists the stage change to the state store.
func (r *workflowRunner) recordTransition(newStage string) {
	if r.store == nil || r.state == nil {
		return
	}
	now := r.ports.now()
	if err := r.store.RecordStage(r.state, "advanced", "trigger", now); err != nil {
		r.ports.notify("workflow %s: record stage: %v", r.template.Name, err)
	}
	r.state.CurrentStage = newStage
	r.state.StageStartedAt = now
	if err := r.store.Save(r.state); err != nil {
		r.ports.notify("workflow %s: save state: %v", r.template.Name, err)
	}
}

// Run drives the workflow until completion, the transition budget, a
// configured pause/abort, or context expiry. It always returns a populated
// WorkflowRunResult mirror of what happened.
func (r *workflowRunner) Run(ctx context.Context) (WorkflowRunResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	result := &r.result
	result.Workflow = r.template.Name
	result.Source = r.template.Source
	result.Session = r.opts.Session
	result.Coordination = string(r.template.Coordination)
	for _, agent := range r.agents {
		result.Agents = append(result.Agents, WorkflowRunAgent{Role: agent.Role, Pane: agent.ID})
	}

	fail := func(reason string, err error) (WorkflowRunResult, error) {
		result.Success = false
		result.Reason = reason
		if err != nil {
			result.Error = err.Error()
		}
		return *result, err
	}

	// Error handling per template policy.
	if eh := r.template.ErrorHandling; eh != nil {
		r.errorHandler = workflow.NewErrorHandler(workflow.ErrorHandlingConfig{
			OnAgentCrash:       eh.OnAgentCrash,
			OnAgentError:       eh.OnAgentError,
			OnTimeout:          eh.OnTimeout,
			MaxRetriesPerStage: eh.MaxRetriesPerStage,
		}, workflowRunActions{r: r})
		if eh.StageTimeoutMinutes > 0 {
			r.timeoutMonitor = workflow.NewTimeoutMonitor(
				time.Duration(eh.StageTimeoutMinutes)*time.Minute, r.errorHandler, r.coordinator.CurrentStage)
			defer r.timeoutMonitor.Stop()
		}
	}

	tctx := &workflow.TriggerContext{
		Context:     runCtx,
		ProjectRoot: r.opts.ProjectRoot,
		Session:     r.opts.Session,
		Now:         r.ports.now,
	}
	if err := r.coordinator.Start(tctx); err != nil {
		return fail("start-failed", err)
	}
	defer func() {
		if err := r.coordinator.Stop(); err != nil {
			r.ports.notify("workflow %s: stop coordinator: %v", r.template.Name, err)
		}
	}()

	stage := r.coordinator.CurrentStage()
	result.Stages = append(result.Stages, stageLabel(stage))

	// Durable state checkpoint.
	if r.store != nil {
		paneRoles := make(map[string]string, len(r.agents))
		for _, agent := range r.agents {
			paneRoles[agent.ID] = agent.Role
		}
		r.state = &workflow.WorkflowState{
			WorkflowName:   r.template.Name,
			SessionName:    r.opts.Session,
			CurrentStage:   stage,
			StageStartedAt: r.ports.now(),
			Agents:         paneRoles,
			Variables:      r.opts.Vars,
		}
		if err := r.store.Save(r.state); err != nil {
			r.ports.notify("workflow %s: save state: %v", r.template.Name, err)
		}
	}

	// Parallel workflows without a flow dispatch everyone and are done.
	if parallel, ok := r.coordinator.(*workflow.ParallelCoordinator); ok && r.template.Flow == nil {
		if err := r.dispatchStage(runCtx, stage, parallel.Agents()); err != nil {
			return fail("dispatch-failed", err)
		}
		result.Success = true
		result.Completed = true
		result.Reason = "completed"
		return *result, nil
	}

	targets, err := r.stagePanes(stage)
	if err != nil {
		return fail("stage-role-unresolved", err)
	}
	if err := r.dispatchStage(runCtx, stage, targets); err != nil {
		return fail("dispatch-failed", err)
	}
	if r.timeoutMonitor != nil {
		r.timeoutMonitor.Start(runCtx, stage)
	}

	for {
		if r.stageIsTerminal(stage) {
			result.Success = true
			result.Completed = true
			result.Reason = "completed"
			return *result, nil
		}
		if reason, stopErr := r.stopped(); reason != "" {
			return fail(reason, stopErr)
		}
		if runCtx.Err() != nil {
			if reason, stopErr := r.stopped(); reason != "" {
				return fail(reason, stopErr)
			}
			return fail("timeout", fmt.Errorf("workflow run canceled: %w", context.Cause(ctx)))
		}

		r.ports.sleep(runCtx, r.opts.Interval)
		if runCtx.Err() != nil {
			continue // loop once more to classify via stopped()/timeout
		}

		if r.opts.FireManual {
			r.fireManualTriggers()
		}

		observed := r.applyReviewGate(r.observe(runCtx))
		fired, err := r.evaluate(observed)
		if err != nil {
			if r.errorHandler != nil {
				if handleErr := r.errorHandler.Handle(runCtx, &workflow.WorkflowError{
					Type: workflow.ErrorTriggerFailed, Stage: stage, Message: err.Error(), Timestamp: r.ports.now(),
				}); handleErr != nil {
					r.ports.notify("workflow %s: error handler: %v", r.template.Name, handleErr)
				}
				continue
			}
			return fail("trigger-failed", err)
		}

		newStage := r.coordinator.CurrentStage()
		if !fired && newStage == stage {
			continue
		}
		result.Transitions++
		r.ports.notify("workflow %s: stage %s → %s (transition %d)", r.template.Name, stageLabel(stage), stageLabel(newStage), result.Transitions)
		r.recordTransition(newStage)
		stage = newStage
		result.Stages = append(result.Stages, stageLabel(stage))
		if r.timeoutMonitor != nil {
			r.timeoutMonitor.Start(runCtx, stage)
		}
		if r.stageIsTerminal(stage) {
			continue // terminal handling at loop top
		}
		targets, err := r.stagePanes(stage)
		if err != nil {
			return fail("stage-role-unresolved", err)
		}
		if err := r.dispatchStage(runCtx, stage, targets); err != nil {
			return fail("dispatch-failed", err)
		}
		if result.Transitions >= r.opts.MaxTransitions {
			result.Success = true
			result.Reason = "max-transitions"
			return *result, nil
		}
	}
}

// evaluate advances via the coordinator's Evaluate (all concrete coordinator
// types embed RuntimeCoordinator) or, failing that, reports no progress.
func (r *workflowRunner) evaluate(tctx *workflow.TriggerContext) (bool, error) {
	evaluator, ok := r.coordinator.(interface {
		Evaluate(*workflow.TriggerContext) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("coordinator for %q does not support trigger evaluation", r.template.Name)
	}
	return evaluator.Evaluate(tctx)
}

// =============================================================================
// Template resolution (name vs path) and the cobra surface
// =============================================================================

// workflowNotFoundError is the documented failure for an unresolvable
// workflow reference: it lists the builtin names and never falls back.
func workflowNotFoundError(ref string) error {
	return fmt.Errorf("workflow %q not found: not a builtin (%s), no user (~/.config/ntm/workflows/) or project (.ntm/workflows/) template has that name, and it is not a path to a workflow TOML file",
		ref, strings.Join(workflow.BuiltinNames(), ", "))
}

// looksLikeWorkflowPath reports whether the argument addresses the filesystem
// rather than a template name.
func looksLikeWorkflowPath(ref string) bool {
	return strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".toml") || ref == "." || strings.HasPrefix(ref, "~")
}

// resolveWorkflowForRun loads a template by explicit TOML path or by name
// (builtin < user < project precedence via the loader).
func resolveWorkflowForRun(ref string) (*workflow.WorkflowTemplate, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("workflow name or path is required")
	}
	if looksLikeWorkflowPath(ref) {
		content, err := os.ReadFile(ref)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, workflowNotFoundError(ref)
			}
			return nil, fmt.Errorf("read workflow file %s: %w", ref, err)
		}
		// Files may use the [[workflows]] array format or a single-workflow
		// document; both are accepted, and the first workflow runs.
		if templates, err := workflow.ParseWorkflows(string(content)); err == nil && len(templates) > 0 {
			tmpl := templates[0]
			if err := tmpl.Validate(); err != nil {
				return nil, fmt.Errorf("invalid workflow %s: %w", ref, err)
			}
			tmpl.Source = "file:" + ref
			return &tmpl, nil
		}
		tmpl, err := workflow.ParseAndValidateWorkflow(string(content))
		if err != nil {
			return nil, fmt.Errorf("invalid workflow %s: %w", ref, err)
		}
		tmpl.Source = "file:" + ref
		return tmpl, nil
	}
	loader := workflow.NewLoader()
	tmpl, err := loader.Get(ref)
	if err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			return nil, workflowNotFoundError(ref)
		}
		// A malformed user TOML aborts LoadAll wholesale; surface the parse
		// error instead of lying that the requested workflow doesn't exist.
		return nil, fmt.Errorf("loading workflow templates: %w", err)
	}
	return tmpl, nil
}

// resolveWorkflowVars validates --var assignments against the template's
// setup prompts: required prompts without a default must be supplied.
func resolveWorkflowVars(template *workflow.WorkflowTemplate, assignments []string) (map[string]string, error) {
	vars := make(map[string]string)
	for _, assignment := range assignments {
		key, value, ok := strings.Cut(assignment, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --var %q: want key=value", assignment)
		}
		vars[key] = value
	}
	var missing []string
	for _, prompt := range template.Prompts {
		if _, ok := vars[prompt.Key]; ok {
			continue
		}
		if prompt.Default != "" {
			vars[prompt.Key] = prompt.Default
			continue
		}
		if prompt.Required {
			missing = append(missing, fmt.Sprintf("%s (%s)", prompt.Key, prompt.Question))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("workflow %s requires --var for: %s", template.Name, strings.Join(missing, "; "))
	}
	return vars, nil
}

// assignWorkflowPanes maps template roles onto the session's agent panes in
// topology order. Every declared agent instance claims the next agent pane.
func assignWorkflowPanes(template *workflow.WorkflowTemplate, panes []tmux.Pane) ([]workflow.CoordinatorAgent, error) {
	var agentPanes []tmux.Pane
	for _, pane := range tmux.SortPanesByTopology(panes) {
		if pane.Type == tmux.AgentUser {
			continue
		}
		agentPanes = append(agentPanes, pane)
	}
	needed := template.GetAgentCount()
	if len(agentPanes) < needed {
		return nil, fmt.Errorf("workflow %s needs %d agent pane(s) but the session has %d (spawn more agents first, e.g. `ntm spawn`)",
			template.Name, needed, len(agentPanes))
	}
	var assigned []workflow.CoordinatorAgent
	next := 0
	for _, wa := range template.Agents {
		count := wa.Count
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			assigned = append(assigned, workflow.CoordinatorAgent{ID: agentPanes[next].ID, Role: wa.Role})
			next++
		}
	}
	return assigned, nil
}

// workflowGatedDispatch is the production dispatch port: a single-pane send
// through runSendWithTargets, i.e. the same liveness-gated, composer-verified
// path as `ntm send` — never raw send-keys.
func workflowGatedDispatch(workflowName string) func(ctx context.Context, session, paneID, prompt string) error {
	return func(ctx context.Context, session, paneID, prompt string) error {
		collected := &sendExecutionResult{}
		err := runSendWithTargets(SendOptions{
			Context:             ctx,
			Session:             session,
			Prompt:              prompt,
			PromptSource:        "workflow",
			TemplateName:        "workflow:" + workflowName,
			PaneSelector:        paneID,
			ForceNonInteractive: true,
			executionPolicy:     sendExecutionCollect,
			executionResult:     collected,
		})
		if err != nil {
			return err
		}
		if collected.recorded && !collected.result.Success {
			if collected.result.Error != "" {
				return errors.New(collected.result.Error)
			}
			return fmt.Errorf("delivery to pane %s failed", paneID)
		}
		return nil
	}
}

func newWorkflowsRunCmd() *cobra.Command {
	var (
		sessionFlag    string
		projectRoot    string
		varFlags       []string
		maxTransitions int
		interval       time.Duration
		timeout        time.Duration
		fireManual     bool
		triggerTimeout time.Duration
		resumeFlag     bool
	)

	cmd := &cobra.Command{
		Use:   "run <name-or-path>",
		Short: "Run a workflow template against a live session",
		Long: `Run a workflow template's coordination loop against a live session.

The workflow's roles are mapped onto the session's agent panes in pane order,
each stage's prompt is delivered through the same gated dispatch path as
'ntm send' (dead-pane gate + composer-verified submission), and the workflow's
triggers (file_created, command_success, agent_says, manual, ...) advance the
stages until the flow completes, the transition budget is spent, or --timeout
elapses.

The argument is a template name (builtin, then ~/.config/ntm/workflows/, then
.ntm/workflows/ — 'ntm workflows list' shows all) or a path to a workflow TOML
file (anything containing a path separator or ending in .toml). An unknown
name is an error listing the builtins — there is no fallback.

Stage-to-role resolution: a stage engages the role with the same name; else
the role the template's routing table names for the stage; else the role an
outgoing transition's trigger names; else the first declared role. Stages in
parallel workflows or with parallel_within_stage engage every pane of the
role.

Note: 'ntm spawn -t <workflow>' only uses a template's agent COUNTS to size a
session; this command is what actually runs the coordination.

Examples:
  ntm workflow run red-green --var feature="parser rewrite"
  ntm workflow run specialist-team --session myproj --fire-manual
  ntm workflow run ./my-flow.toml --session myproj --max-transitions 4
  ntm workflow run review-pipeline --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowRun(cmd.Context(), args[0], workflowRunCLIFlags{
				Session:        sessionFlag,
				ProjectRoot:    projectRoot,
				Vars:           varFlags,
				MaxTransitions: maxTransitions,
				Interval:       interval,
				Timeout:        timeout,
				FireManual:     fireManual,
				TriggerTimeout: triggerTimeout,
				Resume:         resumeFlag,
			})
		},
	}

	cmd.Flags().StringVarP(&sessionFlag, "session", "s", "", "target session (default: inferred like 'ntm send')")
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "project root for file/command triggers (default: session working dir, else cwd)")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "workflow setup variable key=value (repeatable); required prompts without defaults must be supplied")
	cmd.Flags().IntVar(&maxTransitions, "max-transitions", 8, "stop after this many stage transitions")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "trigger poll interval")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "overall run deadline")
	cmd.Flags().BoolVar(&fireManual, "fire-manual", false, "fire manual triggers automatically instead of waiting for an operator")
	cmd.Flags().DurationVar(&triggerTimeout, "trigger-timeout", 0, "per-check deadline for command triggers (0 = library default)")
	cmd.Flags().BoolVar(&resumeFlag, "resume", false, "clear a paused checkpoint for this session and run again from the initial stage")
	return cmd
}

type workflowRunCLIFlags struct {
	Session        string
	ProjectRoot    string
	Vars           []string
	MaxTransitions int
	Interval       time.Duration
	Timeout        time.Duration
	FireManual     bool
	TriggerTimeout time.Duration
	Resume         bool
}

func runWorkflowRun(ctx context.Context, ref string, flags workflowRunCLIFlags) error {
	emit := func(result WorkflowRunResult, cause error) error {
		if jsonOutput {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
				return encodeErr
			}
			return cause
		}
		if cause == nil {
			fmt.Printf("workflow %s %s: stages %s, %d transition(s)\n",
				result.Workflow, result.Reason, strings.Join(result.Stages, " → "), result.Transitions)
		}
		return cause
	}
	failEarly := func(err error) error {
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}, err)
		}
		return err
	}

	template, err := resolveWorkflowForRun(ref)
	if err != nil {
		return failEarly(err)
	}
	vars, err := resolveWorkflowVars(template, flags.Vars)
	if err != nil {
		return failEarly(err)
	}

	if flags.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, flags.Timeout, fmt.Errorf("workflow run --timeout %s elapsed", flags.Timeout))
		defer cancel()
	}

	session, _, err := resolveSendSessionForCommandContext(ctx, flags.Session)
	if err != nil {
		return failEarly(err)
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return failEarly(err)
	}
	agents, err := assignWorkflowPanes(template, panes)
	if err != nil {
		return failEarly(err)
	}

	projectRoot := strings.TrimSpace(flags.ProjectRoot)
	if projectRoot == "" {
		projectRoot = getSessionWorkingDir(ctx, session, false)
	}
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	opts := workflowRunOptions{
		Session:        session,
		ProjectRoot:    projectRoot,
		Vars:           vars,
		MaxTransitions: flags.MaxTransitions,
		Interval:       flags.Interval,
		FireManual:     flags.FireManual,
		TriggerTimeout: flags.TriggerTimeout,
		StateDir:       filepath.Join(projectRoot, ".ntm", "workflows", "state"),
	}

	// A paused checkpoint fails closed: --resume clears it (and reruns from
	// the initial stage; mid-flow resume is not supported yet).
	store := &workflow.StateStore{Dir: opts.StateDir}
	if prior, loadErr := store.Load(session); loadErr == nil && prior != nil && prior.Paused {
		if !flags.Resume {
			return failEarly(fmt.Errorf("workflow %s for session %s is paused (%s); re-run with --resume to clear the pause",
				prior.WorkflowName, session, prior.PauseReason))
		}
		if err := store.Resume(prior); err != nil {
			return failEarly(fmt.Errorf("clear paused workflow state: %w", err))
		}
	}

	notify := func(format string, args ...any) {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	runner, err := newWorkflowRunner(template, agents, opts, workflowRunPorts{
		dispatch: workflowGatedDispatch(template.Name),
		capture:  tmux.CapturePaneOutput,
		notify:   notify,
	})
	if err != nil {
		return failEarly(err)
	}

	result, runErr := runner.Run(ctx)
	return emit(result, runErr)
}
