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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// workflowRunCaptureLines is how much pane scrollback feeds agent_says and
// idle observation on each poll.
const workflowRunCaptureLines = 200

const (
	workflowEvidenceVersion  = 1
	workflowCaptureMaxBytes  = 512 << 10
	workflowEvidenceMaxBytes = 64 << 10
)

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
	Resumed      bool               `json:"resumed,omitempty"`
	Faults       []WorkflowRunFault `json:"faults,omitempty"`
	Restarts     int                `json:"restarts,omitempty"`
	Reason       string             `json:"reason"`
	Error        string             `json:"error,omitempty"`
}

// WorkflowRunFault reports one agent fault raised into the template's
// error_handling policy.
type WorkflowRunFault struct {
	Pane   string `json:"pane"`
	Role   string `json:"role"`
	Stage  string `json:"stage"`
	Type   string `json:"type"` // agent_crash | agent_error
	Reason string `json:"reason"`
	Action string `json:"action"` // the error action the policy applied
}

// workflowAgentFault is one agent pane's current fault.
type workflowAgentFault struct {
	Kind   workflow.ErrorType // ErrorAgentCrash or ErrorAgentError
	Reason string
	// Confirmed marks topology or process evidence (pane gone or dead, no
	// live agent process); it is raised on first sight. Text-classified
	// states must persist for AgentFaultPolls consecutive observations.
	Confirmed bool
}

// workflowFaultEpisode tracks one pane's current fault so each fault is
// raised once, not on every poll, until the pane recovers or is restarted.
type workflowFaultEpisode struct {
	kind   workflow.ErrorType
	polls  int
	raised bool
}

// workflowRunPorts isolates the runner's side effects so unit tests can drive
// the coordinator loop with fake dispatch/capture implementations.
type workflowRunPorts struct {
	// dispatch delivers one prompt to one pane and fails when delivery or
	// submission verification fails.
	dispatch func(ctx context.Context, session, paneID, prompt string) error
	// capture returns recent pane output for trigger observation.
	capture func(ctx context.Context, paneID string, lines int) (string, error)
	// validate binds production observations to the same physical pane/PID
	// throughout the run. Custom test transports own their pane identities.
	validate func(context.Context) error
	// faults classifies the run's agent panes (pane → bound shell PID) with
	// ntm's shared health observation; panes absent from the result are
	// healthy. Nil disables agent crash/error supervision.
	faults func(ctx context.Context, expected map[string]int) (map[string]workflowAgentFault, error)
	// restart relaunches one pane's agent through ntm's shared restart
	// engine and returns the replacement shell PID the run must rebind to.
	restart func(ctx context.Context, session, paneID string) (int, error)
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
	PanePIDs       map[string]int    // Live pane lifetimes, supplied by the tmux adapter.
	PaneTypes      map[string]string // Agent types captured alongside those lifetimes.
	MaxTransitions int
	Interval       time.Duration
	FireManual     bool
	TriggerTimeout time.Duration
	StateDir       string // defaults to <ProjectRoot>/.ntm/workflows/state
	Resume         bool
	Restart        bool

	// MaxAgentRestarts bounds restart_agent per pane per run ([resilience]
	// max_restarts); the next fault past the budget aborts the run.
	MaxAgentRestarts int
	// AgentFaultPolls is how many consecutive polls a text-classified agent
	// error must persist before it is raised ([resilience] crash_threshold).
	AgentFaultPolls int
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

	// dispatchMu serializes whole-stage prompt deliveries: the error
	// handler's RetryStage runs on the TimeoutMonitor goroutine, and without
	// this lock its keystrokes could interleave with a concurrent
	// dispatchStage from the main run loop.
	dispatchMu sync.Mutex

	mu         sync.Mutex
	manuals    []*workflow.ManualTrigger
	turn       int
	stopReason string
	stopErr    error
	cancel     context.CancelFunc

	// supervision holds each faulted pane's current episode; healthDown
	// suppresses repeated "health unavailable" notices. Both guarded by mu.
	supervision map[string]*workflowFaultEpisode
	healthDown  bool

	result WorkflowRunResult
}

// newWorkflowRunner validates the template against the pane assignment and
// builds the coordinator with a trigger registry that tracks manual triggers
// (for --fire-manual) and applies the configured command-trigger timeout.
func newWorkflowRunner(template *workflow.WorkflowTemplate, agents []workflow.CoordinatorAgent, opts workflowRunOptions, ports workflowRunPorts) (*workflowRunner, error) {
	ports.fillDefaults()
	if opts.Resume && opts.Restart {
		return nil, errors.New("--resume and --restart are mutually exclusive")
	}
	if (opts.Resume || opts.Restart) && strings.TrimSpace(opts.StateDir) == "" {
		return nil, errors.New("workflow resume/restart requires a durable state directory")
	}
	if ports.dispatch == nil || ports.capture == nil {
		return nil, errors.New("workflow dispatch and capture ports are required")
	}
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workflow project root: %w", err)
	}
	opts.ProjectRoot = filepath.Clean(root)
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.MaxTransitions <= 0 {
		opts.MaxTransitions = 8
	}
	if opts.MaxAgentRestarts < 0 {
		opts.MaxAgentRestarts = 0
	}
	if opts.AgentFaultPolls <= 0 {
		opts.AgentFaultPolls = 3 // [resilience] crash_threshold default
	}
	// The runner owns its pane binding: restart_agent rebinds a pane to its
	// replacement process (under dispatchMu), and every identity check reads
	// this map rather than the caller's.
	pids := make(map[string]int, len(opts.PanePIDs))
	for pane, pid := range opts.PanePIDs {
		pids[pane] = pid
	}
	opts.PanePIDs = pids
	r := &workflowRunner{
		template: template,
		agents:   append([]workflow.CoordinatorAgent(nil), agents...),
		opts:     opts,
		ports:    ports,
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
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	return r.dispatchStageLocked(ctx, stage, targets)
}

func (r *workflowRunner) dispatchStageLocked(ctx context.Context, stage string, targets []workflow.CoordinatorAgent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The coordinator starts manual triggers while holding its own mutex;
	// those factories acquire r.mu. Never acquire the coordinator lock while
	// holding r.mu, including on the timeout goroutine's retry path.
	var nextByRole map[string]int
	if routing, ok := r.coordinator.(interface{ RoutingState() map[string]int }); ok {
		nextByRole = routing.RoutingState()
	}
	if err := r.validatePanes(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	needsBoundary := r.state != nil && r.state.Evidence == nil
	r.mu.Unlock()
	var boundary map[string]workflow.StagePaneEvidence
	if needsBoundary {
		var err error
		boundary, err = r.captureStageBoundary(ctx)
		if err != nil {
			return err
		}
	}
	r.mu.Lock()
	if r.state == nil || r.state.CurrentStage != stage {
		r.mu.Unlock()
		return errors.New("workflow stage changed before dispatch; no prompt sent")
	}
	if r.state.Dispatches == nil {
		r.state.Dispatches = make([]workflow.StageDispatch, 0, len(targets))
		for _, agent := range targets {
			r.turn++
			prompt := r.stagePrompt(stage, agent, r.turn)
			if err := r.validateVerdictPrompt(stage, agent.Role, prompt); err != nil {
				r.mu.Unlock()
				return err
			}
			if _, err := r.piDispatchEcho(stage, agent.ID, agent.Role, prompt); err != nil {
				r.mu.Unlock()
				return err
			}
			r.state.Dispatches = append(r.state.Dispatches, workflow.StageDispatch{
				Pane: agent.ID, Role: agent.Role, Turn: r.turn, Status: "pending",
			})
		}
		r.state.Turn = r.turn
		r.state.NextByRole = nextByRole
	}
	if r.state.Evidence == nil {
		r.state.Evidence = &workflow.StageEvidence{
			Version: workflowEvidenceVersion, Stage: stage, StartedAt: r.state.StageStartedAt,
			Round: r.state.Turn, Panes: boundary, Matches: make(map[int][]string),
		}
	}
	if err := r.saveCheckpointLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	plan := append([]workflow.StageDispatch(nil), r.state.Dispatches...)
	r.mu.Unlock()
	for i, delivery := range plan {
		if delivery.Status == "delivered" {
			continue
		}
		if delivery.Status != "pending" {
			return fmt.Errorf("workflow delivery outcome unknown for pane %s at stage %q; refusing to resend", delivery.Pane, stage)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.validatePanes(ctx); err != nil {
			return err
		}
		if err := r.refreshDeliveryBoundary(ctx, delivery.Pane); err != nil {
			return err
		}
		r.mu.Lock()
		r.state.Dispatches[i].Status = "sending"
		err := r.saveCheckpointLocked()
		r.mu.Unlock()
		if err != nil {
			return err // Nothing is sent without its durable intent.
		}
		agent := workflow.CoordinatorAgent{ID: delivery.Pane, Role: delivery.Role}
		prompt := r.stagePrompt(stage, agent, delivery.Turn)
		if err := r.pinPiDispatchPrompt(stage, agent.ID, prompt); err != nil {
			return err
		}
		r.ports.notify("workflow %s: stage %s → %s (%s), turn %d", r.template.Name, stageLabel(stage), agent.ID, agent.Role, delivery.Turn)
		if err := r.ports.dispatch(ctx, r.opts.Session, agent.ID, prompt); err != nil {
			// The port may have typed part or all of the prompt. Leave sending
			// on disk: an error is NOT proof that replay would be safe.
			return fmt.Errorf("dispatch stage %q to pane %s (%s): %w", stageLabel(stage), agent.ID, agent.Role, err)
		}
		r.mu.Lock()
		r.state.Dispatches[i].Status = "delivered"
		err = r.saveCheckpointLocked()
		r.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// pinPiDispatchPrompt runs before sending. The production preparation hook
// replaces the initial fingerprint with the exact final message after all
// enrichment and redaction, while the delivery is still durably "sending".
func (r *workflowRunner) pinPiDispatchPrompt(stage, pane, prompt string) error {
	if r.workflowTranscriptType(pane) != "pi" {
		return nil
	}
	echo := status.NormalizeAgentEcho(prompt)
	if len(echo) == 0 || len(echo) > workflowCaptureMaxBytes {
		return fmt.Errorf("workflow pi prompt for pane %s exceeds the observable echo bounds", pane)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil || r.state.CurrentStage != stage {
		return errors.New("workflow stage changed before its pi prompt could be recorded")
	}
	for i := range r.state.Dispatches {
		delivery := &r.state.Dispatches[i]
		if delivery.Pane != pane {
			continue
		}
		if delivery.Status != "sending" {
			return fmt.Errorf("workflow pi delivery for pane %s is not in progress", pane)
		}
		rendered, err := r.piDispatchEcho(stage, pane, delivery.Role, prompt)
		if err != nil {
			return err
		}
		delivery.PromptEchoHash = fmt.Sprintf("%x", sha256.Sum256([]byte(echo)))
		delivery.PromptEchoLength = len(echo)
		delivery.PiPromptEchoHash = rendered.Hash
		delivery.PiPromptEchoLength = rendered.Length
		return r.saveCheckpointLocked()
	}
	return fmt.Errorf("workflow pi pane %s has no current delivery intent", pane)
}

// Validate pi's rendered prompt before sending: formatting can make a verdict
// phrase visible even when the raw Markdown does not match its trigger.
func (r *workflowRunner) piDispatchEcho(stage, pane, role, prompt string) (status.PromptEchoFingerprint, error) {
	if r.workflowTranscriptType(pane) != "pi" {
		return status.PromptEchoFingerprint{}, nil
	}
	fingerprint, rendered, ok := status.PiPromptEcho(prompt)
	if !ok || fingerprint.Length == 0 || fingerprint.Length > workflowCaptureMaxBytes {
		return status.PromptEchoFingerprint{}, fmt.Errorf("workflow pi prompt for pane %s uses unsupported display formatting; use plain text or supported Markdown before dispatch", pane)
	}
	if err := r.validateVerdictPrompt(stage, role, rendered); err != nil {
		return status.PromptEchoFingerprint{}, fmt.Errorf("rendered pi prompt: %w", err)
	}
	return fingerprint, nil
}

// checkpointTargets reuses the saved plan rather than consuming another
// round-robin slot or delivering to a different pane on resume.
func (r *workflowRunner) checkpointTargets(stage string) ([]workflow.CoordinatorAgent, error) {
	r.mu.Lock()
	var targets []workflow.CoordinatorAgent
	if r.state != nil && r.state.Dispatches != nil {
		for _, delivery := range r.state.Dispatches {
			targets = append(targets, workflow.CoordinatorAgent{ID: delivery.Pane, Role: delivery.Role})
		}
		r.mu.Unlock()
		return targets, nil
	}
	r.mu.Unlock()
	return r.stagePanes(stage)
}

func (r *workflowRunner) saveCheckpointLocked() error {
	if r.store == nil {
		return nil
	}
	if err := r.store.Save(r.state); err != nil {
		return fmt.Errorf("persist workflow checkpoint before further execution: %w", err)
	}
	return nil
}

// loadCheckpoint validates identity and delivery evidence before touching the
// saved pause or starting watchers. Versions without a delivery journal fail
// closed, as do changed templates/variables/pane lifetimes and ambiguous sends.
func (r *workflowRunner) loadCheckpoint() error {
	if r.store == nil {
		return nil
	}
	prior, err := r.store.Load(r.opts.Session)
	if err != nil {
		return err
	}
	if !r.opts.Resume {
		if prior != nil && !prior.Completed && !r.opts.Restart {
			return fmt.Errorf("workflow %s has an unfinished checkpoint at stage %q; use --resume to continue, or --restart to deliberately repeat the workflow", prior.WorkflowName, prior.CurrentStage)
		}
		return nil
	}
	if prior == nil {
		return errors.New("no workflow checkpoint to resume")
	}
	hash, err := r.templateHash()
	if err != nil {
		return err
	}
	if prior.ResumeVersion != 1 || prior.TemplateHash != hash || prior.WorkflowName != r.template.Name || prior.ProjectRoot != r.opts.ProjectRoot {
		return errors.New("workflow checkpoint version, template, or project differs; refusing unsafe resume (use --restart for a deliberate fresh run)")
	}
	if prior.StageStartedAt.IsZero() || prior.StageStartedAt.After(r.ports.now()) || prior.Turn < 0 {
		return errors.New("workflow checkpoint has invalid timing or turn state")
	}
	if len(prior.Agents) != len(r.agents) {
		return errors.New("workflow checkpoint pane assignment changed; refusing unsafe resume")
	}
	for _, agent := range r.agents {
		if role, ok := prior.Agents[agent.ID]; !ok || role != agent.Role || prior.PanePIDs[agent.ID] != r.opts.PanePIDs[agent.ID] {
			return fmt.Errorf("workflow pane %s changed role or lifetime; refusing unsafe resume", agent.ID)
		}
	}
	for key, value := range r.opts.Vars {
		if saved, ok := prior.Variables[key]; !ok || saved != value {
			return fmt.Errorf("--var %s differs from the checkpoint; resume cannot change in-flight work", key)
		}
	}
	seen := make(map[string]bool)
	if prior.Dispatches != nil && len(prior.Dispatches) == 0 {
		return errors.New("workflow checkpoint has an empty stage delivery plan")
	}
	if prior.Completed && (!r.stageIsTerminal(prior.CurrentStage) || prior.Paused) {
		return errors.New("workflow checkpoint has inconsistent completion state")
	}
	for _, delivery := range prior.Dispatches {
		if prior.Completed && delivery.Status != "delivered" {
			return errors.New("completed workflow checkpoint contains unfinished deliveries")
		}
		if seen[delivery.Pane] || prior.Agents[delivery.Pane] != delivery.Role || delivery.Role == "" || delivery.Turn <= 0 || delivery.Turn > prior.Turn {
			return errors.New("workflow checkpoint has an invalid stage delivery plan")
		}
		seen[delivery.Pane] = true
		if delivery.PromptEchoHash != "" || delivery.PromptEchoLength != 0 {
			digest, err := hex.DecodeString(delivery.PromptEchoHash)
			if err != nil || len(digest) != sha256.Size || delivery.PromptEchoLength <= 0 || delivery.PromptEchoLength > workflowCaptureMaxBytes {
				return errors.New("workflow checkpoint has invalid delivered prompt evidence")
			}
		}
		if delivery.PiPromptEchoHash != "" || delivery.PiPromptEchoLength != 0 {
			digest, err := hex.DecodeString(delivery.PiPromptEchoHash)
			if err != nil || len(digest) != sha256.Size || delivery.PiPromptEchoLength <= 0 || delivery.PiPromptEchoLength > workflowCaptureMaxBytes || delivery.PromptEchoHash == "" {
				return errors.New("workflow checkpoint has invalid rendered pi prompt evidence")
			}
		}
		if !prior.Completed && r.workflowTranscriptType(delivery.Pane) == "pi" && delivery.Status == "delivered" && delivery.PiPromptEchoHash == "" {
			return errors.New("workflow checkpoint lacks rendered pi prompt evidence; inspect the stage before a deliberate --restart")
		}
		switch delivery.Status {
		case "pending", "delivered":
		case "sending":
			return fmt.Errorf("workflow delivery outcome unknown for pane %s at stage %q; inspect the pane before a deliberate --restart; --resume will not duplicate the prompt", delivery.Pane, prior.CurrentStage)
		default:
			return fmt.Errorf("workflow checkpoint has invalid delivery status %q", delivery.Status)
		}
	}
	if err := r.validateSavedEvidence(prior); err != nil {
		return err
	}
	r.opts.Vars = prior.Variables
	r.state = prior
	r.turn = prior.Turn
	return nil
}

func (r *workflowRunner) stageHasVerdict(stage string) bool {
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage && tr.Trigger.Type == workflow.TriggerAgentSays {
				return true
			}
		}
	}
	return false
}

func (r *workflowRunner) validateSavedEvidence(state *workflow.WorkflowState) error {
	if state.Evidence == nil {
		if !state.Completed && r.stageHasVerdict(state.CurrentStage) {
			for _, delivery := range state.Dispatches {
				if delivery.Status != "pending" {
					return errors.New("workflow checkpoint lacks the output boundary for an already-dispatched agent_says stage; inspect the stage before a deliberate --restart")
				}
			}
		}
		return nil
	}
	e := state.Evidence
	if e.Version != workflowEvidenceVersion || e.Stage != state.CurrentStage || !e.StartedAt.Equal(state.StageStartedAt) || e.Round <= 0 || e.Round != state.Turn || len(e.Panes) != len(r.agents) {
		return errors.New("workflow checkpoint has invalid stage output evidence")
	}
	for _, agent := range r.agents {
		pane, ok := e.Panes[agent.ID]
		if !ok || pane.PID != state.PanePIDs[agent.ID] || len(pane.Capture) > workflowCaptureMaxBytes || len(pane.Fresh) > workflowEvidenceMaxBytes || pane.LastActivity.IsZero() || pane.LastActivity.After(r.ports.now()) {
			return fmt.Errorf("workflow checkpoint has invalid output evidence for pane %s", agent.ID)
		}
		if pane.AgentType != r.workflowTranscriptType(agent.ID) {
			return fmt.Errorf("workflow pane %s transcript normalization changed; inspect the stage before a deliberate --restart", agent.ID)
		}
	}
	for index, panes := range e.Matches {
		if r.template.Flow == nil || index < 0 || index >= len(r.template.Flow.Transitions) {
			return errors.New("workflow checkpoint evidence names an unknown transition")
		}
		tr := r.template.Flow.Transitions[index]
		if tr.From != state.CurrentStage || tr.Trigger.Type != workflow.TriggerAgentSays {
			return errors.New("workflow checkpoint evidence belongs to a different stage or trigger")
		}
		seen := make(map[string]bool, len(panes))
		for _, pane := range panes {
			role, ok := state.Agents[pane]
			if !ok || seen[pane] || (tr.Trigger.Role != "" && role != tr.Trigger.Role) {
				return errors.New("workflow checkpoint verdict has an invalid pane or role binding")
			}
			seen[pane] = true
		}
	}
	return nil
}

func (r *workflowRunner) templateHash() (string, error) {
	copy := *r.template
	copy.Source = "" // Same definition may be addressed by name or file path.
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("fingerprint workflow template: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (r *workflowRunner) completeCheckpoint() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil || r.state.Completed {
		return nil
	}
	r.state.Completed = true
	if r.store != nil {
		return r.store.RecordStage(r.state, "completed", "terminal", r.ports.now())
	}
	return nil
}

func (r *workflowRunner) validatePanes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.ports.validate != nil {
		if err := r.ports.validate(ctx); err != nil {
			return fmt.Errorf("workflow pane identity changed; no further dispatch or transition: %w", err)
		}
	}
	return nil
}

// captureStageBoundary must finish for every pane before the first stage
// prompt. A partial roster is never interpreted as "all agents idle" or a
// smaller approval quorum.
func (r *workflowRunner) captureStageBoundary(ctx context.Context) (map[string]workflow.StagePaneEvidence, error) {
	panes := make(map[string]workflow.StagePaneEvidence, len(r.agents))
	for _, agent := range r.agents {
		pane, err := r.captureWorkflowPane(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		panes[agent.ID] = pane
	}
	return panes, nil
}

func (r *workflowRunner) workflowTranscriptType(pane string) string {
	if strings.EqualFold(strings.TrimSpace(r.opts.PaneTypes[pane]), "pi") {
		return "pi"
	}
	return ""
}

func (r *workflowRunner) captureWorkflowPane(ctx context.Context, pane string) (workflow.StagePaneEvidence, error) {
	if err := r.validatePanes(ctx); err != nil {
		return workflow.StagePaneEvidence{}, err
	}
	text, err := r.ports.capture(ctx, pane, workflowRunCaptureLines)
	if err != nil {
		return workflow.StagePaneEvidence{}, fmt.Errorf("capture workflow pane %s before trusting its output: %w", pane, err)
	}
	if len(text) > workflowCaptureMaxBytes {
		return workflow.StagePaneEvidence{}, fmt.Errorf("workflow pane %s capture exceeds %d bytes; output evidence is unavailable", pane, workflowCaptureMaxBytes)
	}
	if err := r.validatePanes(ctx); err != nil {
		return workflow.StagePaneEvidence{}, err
	}
	captured := workflow.StagePaneEvidence{
		PID: r.opts.PanePIDs[pane], Capture: strings.TrimRight(text, "\n"), LastActivity: r.ports.now(),
	}
	if r.workflowTranscriptType(pane) == "pi" {
		captured.AgentType = "pi"
		transcript, complete := status.AgentResponseTranscript(text, "pi")
		captured.Incomplete = !complete
		captured.Capture = strings.TrimRight(transcript, "\n")
		// Chrome is not verdict evidence, but its changes and working signal
		// still count as activity for all_agents_idle. Reuse plugin readiness.
		captured.ScreenHash = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		captured.Working = agentpkg.PluginActivelyWorking(status.StripANSI(text), agentpkg.AgentType("pi"), 0)
	}
	return captured, nil
}

// A pending recipient may finish unrelated work while earlier recipients are
// being prompted, or during a pause. Start its boundary at its own dispatch,
// keeping already-delivered recipients' boundaries and votes intact.
func (r *workflowRunner) refreshDeliveryBoundary(ctx context.Context, pane string) error {
	captured, err := r.captureWorkflowPane(ctx, pane)
	if err != nil {
		return err
	}
	if captured.Incomplete {
		return fmt.Errorf("workflow pi pane %s has an incomplete or unsupported composer; no prompt sent", pane)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil || r.state.Evidence == nil {
		return errors.New("workflow delivery has no stage output boundary")
	}
	found := false
	for _, delivery := range r.state.Dispatches {
		if delivery.Pane == pane && (delivery.Status == "pending" || delivery.Status == "sending") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("workflow pane %s is not awaiting this stage's delivery", pane)
	}
	restartPaneEvidence(r.state.Evidence, pane, captured)
	return r.saveCheckpointLocked()
}

// restartPaneEvidence moves one pane's output boundary to a fresh capture and
// discards the verdicts it gave this stage, leaving other panes' boundaries
// and votes intact.
func restartPaneEvidence(evidence *workflow.StageEvidence, pane string, captured workflow.StagePaneEvidence) {
	evidence.Panes[pane] = captured
	for index, receipts := range evidence.Matches {
		kept := make([]string, 0, len(receipts))
		for _, receipt := range receipts {
			if receipt != pane {
				kept = append(kept, receipt)
			}
		}
		evidence.Matches[index] = kept
	}
}

// workflowNewEvidence accepts append-only text or a retained complete-line
// overlap after scrolling. Unlike display diffs, a missing boundary is an
// error: a redraw or history reset must not turn old text into a new verdict.
func workflowNewEvidence(before, after string) (string, error) {
	if strings.HasPrefix(after, before) {
		return after[len(before):], nil
	}
	for i := 0; i < len(before); i++ {
		if before[i] != '\n' || i+1 >= len(before) {
			continue
		}
		suffix := before[i+1:]
		// Require an actual retained content line, never blank terminal rows.
		if strings.TrimSpace(suffix) != "" && strings.HasPrefix(after, suffix) {
			return after[len(suffix):], nil
		}
	}
	return "", errors.New("the saved output boundary is no longer visible (scrollback loss or screen redraw); inspect the stage before a deliberate --restart")
}

// recoverPiWorkflowEvidence is called under r.mu. A repaint can only adopt
// this stage's delivered prompt, never an arbitrary idle replacement screen.
// The fingerprint comes from the final prepared delivery, never a newly
// rendered template. Legacy receipts without a fingerprint remain strict.
func (r *workflowRunner) recoverPiWorkflowEvidence(agent workflow.CoordinatorAgent, prior workflow.StagePaneEvidence, after string) (string, bool) {
	for _, delivery := range r.state.Dispatches {
		if delivery.Pane != agent.ID || delivery.Role != agent.Role || delivery.Status != "delivered" {
			continue
		}
		if delivery.PiPromptEchoHash == "" || delivery.PiPromptEchoLength <= 0 {
			return "", false
		}
		rendered := status.PromptEchoFingerprint{Hash: delivery.PiPromptEchoHash, Length: delivery.PiPromptEchoLength}
		before := prior.Capture
		if strings.TrimSpace(prior.Fresh) != "" {
			// An already attributed answer cannot be replaced. An echo alone
			// may be redrawn, provided its exact suffix is still accounted for.
			if !status.MatchesAgentEcho(prior.Fresh, delivery.PromptEchoHash, delivery.PromptEchoLength, rendered) || !strings.HasSuffix(before, prior.Fresh) {
				return "", false
			}
			before = strings.TrimSuffix(before, prior.Fresh)
		}
		return status.RecoverPiRepaint(before, after, delivery.PromptEchoHash, delivery.PromptEchoLength, rendered)
	}
	return "", false
}

// validateVerdictPrompt refuses ambiguous templates before they can echo their
// own completion phrase into the observation stream. The production dispatch
// hook repeats this check after enrichment, redaction, and prompt stamping.
func (r *workflowRunner) validateVerdictPrompt(stage, role, prompt string) error {
	if r.template.Flow == nil {
		return nil
	}
	for _, tr := range r.template.Flow.Transitions {
		if tr.From != stage || tr.Trigger.Type != workflow.TriggerAgentSays || (tr.Trigger.Role != "" && tr.Trigger.Role != role) {
			continue
		}
		pattern, err := regexp.Compile(tr.Trigger.Pattern)
		if err != nil {
			return err
		}
		if pattern.MatchString(prompt) {
			return fmt.Errorf("stage %q prompt itself matches agent_says pattern %q; remove the verdict phrase from the prompt or use a more specific response pattern", stage, tr.Trigger.Pattern)
		}
	}
	return nil
}

// Match the complete evidence string so regex anchors retain their meaning.
// A pi verdict must follow the current prompt's unique visible echo, including
// when terminal wrapping makes text inside that echo match the trigger.
func piWorkflowVerdict(pattern, output string, delivery workflow.StageDispatch) (bool, error) {
	if delivery.PiPromptEchoHash == "" || delivery.PiPromptEchoLength <= 0 {
		return false, errors.New("workflow pi delivery lacks rendered prompt evidence")
	}
	_, end, found := status.UniqueAgentEchoSpan(output, delivery.PromptEchoHash, delivery.PromptEchoLength,
		status.PromptEchoFingerprint{Hash: delivery.PiPromptEchoHash, Length: delivery.PiPromptEchoLength})
	if !found || strings.TrimSpace(output[end:]) == "" {
		return false, nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	// Bound pathological regex matches like the prompt scan. Exhaustion
	// retains evidence and waits rather than dropping the original prefix.
	for _, match := range compiled.FindAllStringIndex(output, 4096) {
		if match[0] >= end {
			return true, nil
		}
	}
	return false, nil
}

type workflowObservation struct {
	context *workflow.TriggerContext
	stage   string
	round   int
}

// observe persists fresh stage evidence before the coordinator may act on it.
// Receipts remain useful after their text scrolls away or the runner pauses.
func (r *workflowRunner) observe(ctx context.Context) (*workflowObservation, error) {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	coordinatorStage := r.coordinator.CurrentStage()
	r.mu.Lock()
	if r.state != nil && r.state.CurrentStage != coordinatorStage && r.state.Evidence != nil {
		// A timeout policy deliberately skipped the stage. The serialized
		// advance helper will record that move without evaluating another one.
		observation := &workflowObservation{stage: r.state.CurrentStage, round: r.state.Evidence.Round}
		r.mu.Unlock()
		return observation, nil
	}
	r.mu.Unlock()
	current, err := r.captureStageBoundary(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.state == nil || r.state.Evidence == nil {
		r.mu.Unlock()
		return nil, errors.New("workflow stage is missing its durable output boundary")
	}
	evidence := *r.state.Evidence
	evidence.Panes = make(map[string]workflow.StagePaneEvidence, len(current))
	evidence.Matches = make(map[int][]string, len(r.state.Evidence.Matches))
	for index, panes := range r.state.Evidence.Matches {
		evidence.Matches[index] = append([]string(nil), panes...)
	}
	tctx := &workflow.TriggerContext{
		Context: ctx, ProjectRoot: r.opts.ProjectRoot, Session: r.opts.Session,
		Now: r.ports.now, TransitionEvidence: make(map[int]bool),
	}
	changed := false
	for _, agent := range r.agents {
		prior, ok := r.state.Evidence.Panes[agent.ID]
		frame := current[agent.ID]
		if !ok || prior.PID != frame.PID || prior.AgentType != frame.AgentType {
			r.mu.Unlock()
			return nil, fmt.Errorf("workflow pane %s lost its stage identity", agent.ID)
		}
		isPi := frame.AgentType == "pi"
		if isPi && (frame.Incomplete || (prior.Capture != "" && (strings.TrimSpace(frame.Capture) == "" || (len(frame.Capture) < len(prior.Capture) && strings.HasPrefix(prior.Capture, frame.Capture))))) {
			// Keep the last complete boundary through a blank or partially
			// restored screen. Restored history must not become fresh output.
			prior.LastActivity = r.ports.now()
			prior.ScreenHash = frame.ScreenHash
			evidence.Panes[agent.ID] = prior
			tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
			changed = true
			continue
		}
		delta := ""
		if r.stageHasVerdict(r.state.CurrentStage) {
			var err error
			delta, err = workflowNewEvidence(prior.Capture, frame.Capture)
			if isPi && (err != nil || prior.Capture == "") {
				if recovered, trusted := r.recoverPiWorkflowEvidence(agent, prior, frame.Capture); trusted {
					delta, err, prior.Fresh = recovered, nil, ""
				} else if prior.Capture == "" {
					// A capture taken mid-repaint has no transcript boundary.
					// Wait for a verifiable prompt rather than trusting a banner
					// or restored history as an answer to the current turn.
					prior.LastActivity = r.ports.now()
					prior.ScreenHash = frame.ScreenHash
					evidence.Panes[agent.ID] = prior
					tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
					changed = true
					continue
				}
			}
			if err != nil {
				r.mu.Unlock()
				return nil, fmt.Errorf("workflow pane %s: %w", agent.ID, err)
			}
		}
		if prior.Capture != frame.Capture || prior.ScreenHash != frame.ScreenHash || frame.Working {
			prior.LastActivity = r.ports.now()
			changed = true
		}
		prior.Capture, prior.ScreenHash = frame.Capture, frame.ScreenHash
		// Keep the exact response prefix until its applicable verdicts have
		// been recorded. Trimming changes regexp anchors and can manufacture
		// an approval at the beginning of a retained suffix.
		prior.Fresh += delta
		evidence.Panes[agent.ID] = prior
		tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
	}
	stage := r.state.CurrentStage
	dispatches := append([]workflow.StageDispatch(nil), r.state.Dispatches...)
	r.mu.Unlock()

	registry := workflow.NewTriggerRegistry()
	unmatched := make(map[string]bool, len(r.agents))
	if r.template.Flow != nil {
		for index, tr := range r.template.Flow.Transitions {
			if tr.From != stage || tr.Trigger.Type != workflow.TriggerAgentSays {
				continue
			}
			probe, err := registry.Create(tr.Trigger)
			if err != nil {
				return nil, err
			}
			for _, agent := range r.agents {
				if tr.Trigger.Role != "" && agent.Role != tr.Trigger.Role {
					continue
				}
				if containsWorkflowPane(evidence.Matches[index], agent.ID) {
					continue
				}
				text := evidence.Panes[agent.ID].Fresh
				fired := false
				if text != "" {
					if evidence.Panes[agent.ID].AgentType == "pi" {
						for _, delivery := range dispatches {
							if delivery.Pane == agent.ID && delivery.Role == agent.Role && delivery.Status == "delivered" {
								fired, err = piWorkflowVerdict(tr.Trigger.Pattern, text, delivery)
								break
							}
						}
					} else {
						fired, err = probe.Check(&workflow.TriggerContext{Outputs: []workflow.AgentOutput{{Role: agent.Role, Text: text}}})
					}
					if err != nil {
						return nil, err
					}
				}
				if fired {
					evidence.Matches[index] = append(evidence.Matches[index], agent.ID)
					changed = true
				} else {
					unmatched[agent.ID] = true
				}
			}
			met := len(evidence.Matches[index]) > 0
			if gate, ok := r.coordinator.(*workflow.ReviewGateCoordinator); ok && r.template.Flow.RequireApproval {
				met, err = gate.CheckApprovals(index, evidence.Matches[index])
				if err != nil {
					return nil, err
				}
			}
			tctx.TransitionEvidence[index] = met
		}
	}
	for pane, value := range evidence.Panes {
		if !unmatched[pane] {
			// All of this pane's applicable verdicts are durable receipts;
			// their text is no longer needed for matching or later resume.
			value.Fresh = ""
		} else if len(value.Fresh) > workflowEvidenceMaxBytes {
			return nil, fmt.Errorf("workflow pane %s response evidence exceeds %d bytes before all verdicts were observed; inspect the stage before a deliberate --restart", pane, workflowEvidenceMaxBytes)
		}
		evidence.Panes[pane] = value
	}
	if err := r.validatePanes(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.CurrentStage != stage || r.state.Evidence.Round != evidence.Round {
		return nil, errors.New("workflow stage changed while collecting output evidence")
	}
	if changed {
		r.state.Evidence = &evidence
		if err := r.saveCheckpointLocked(); err != nil {
			return nil, err
		}
	}
	return &workflowObservation{context: tctx, stage: stage, round: evidence.Round}, nil
}

// advanceObservation holds the same lease as retry/skip through evaluation
// and the durable transition. A response observed before a retry cannot approve
// the replacement round even if it was waiting for this lock during dispatch.
func (r *workflowRunner) advanceObservation(ctx context.Context, observed *workflowObservation) (bool, string, bool, error) {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	r.mu.Lock()
	current := observed != nil && r.state != nil && r.state.Evidence != nil &&
		r.state.CurrentStage == observed.stage && r.state.Evidence.Round == observed.round
	r.mu.Unlock()
	if !current {
		return false, "", true, nil
	}
	if err := r.validatePanes(ctx); err != nil {
		return false, "", false, err
	}
	newStage := r.coordinator.CurrentStage()
	fired := newStage != observed.stage
	if !fired {
		var err error
		fired, err = r.evaluate(observed.context)
		if err != nil {
			return false, "", false, err
		}
		newStage = r.coordinator.CurrentStage()
	}
	if fired || newStage != observed.stage {
		if err := r.recordTransitionLocked(newStage); err != nil {
			return false, newStage, false, err
		}
	}
	return fired, newStage, false, nil
}

func containsWorkflowPane(panes []string, pane string) bool {
	for _, candidate := range panes {
		if candidate == pane {
			return true
		}
	}
	return false
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
// runner. skip_stage from a stage with no outgoing transition degrades to a
// notification.
type workflowRunActions struct{ r *workflowRunner }

// RestartAgent relaunches the faulted pane's agent through ntm's shared
// restart engine (the one behind `ntm respawn`), rebinds the run to the
// replacement pane process, and re-sends the current stage prompt when the
// old process had already received it. The ErrorHandler bounds how often it
// runs per agent. Only agent faults restart (a stage timeout names no agent),
// so this always runs on the run goroutine. A failed restart stops the run:
// the pane may already be replaced, and its old binding is no longer valid.
func (a workflowRunActions) RestartAgent(ctx context.Context, pane string) error {
	r := a.r
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	failRestart := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err() // The run loop classifies cancellation/timeout.
		}
		r.stop("restart-failed", err)
		return err
	}
	role := ""
	for _, agent := range r.agents {
		if agent.ID == pane {
			role = agent.Role
			break
		}
	}
	if role == "" {
		return failRestart(fmt.Errorf("restart_agent: pane %s is not a workflow participant", pane))
	}
	if r.ports.restart == nil {
		return failRestart(fmt.Errorf("restart_agent: no agent restart transport for pane %s", pane))
	}
	r.ports.notify("workflow %s: restarting agent in pane %s (%s)", r.template.Name, pane, role)
	pid, err := r.ports.restart(ctx, r.opts.Session, pane)
	if err != nil {
		return failRestart(fmt.Errorf("restart agent in pane %s (%s): %w", pane, role, err))
	}
	stage, redeliver, err := r.rebindRestartedPaneLocked(ctx, pane, pid)
	if err != nil {
		return failRestart(fmt.Errorf("rebind restarted pane %s (%s): %w", pane, role, err))
	}
	r.result.Restarts++
	r.ports.notify("workflow %s: agent in pane %s (%s) restarted (pid %d)", r.template.Name, pane, role, pid)
	if !redeliver {
		return nil
	}
	r.ports.notify("workflow %s: re-sending stage %s prompt to restarted pane %s (%s)", r.template.Name, stageLabel(stage), pane, role)
	if err := r.dispatchStageLocked(ctx, stage, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.stop("dispatch-failed", err)
		return err
	}
	return nil
}

// rebindRestartedPaneLocked adopts a restarted pane's replacement process.
// The run's identity binding (live validation, checkpoint, stage evidence)
// moves to the new PID, the pane's output boundary restarts at its fresh
// screen, and verdicts the old process gave this stage are discarded. It
// reports the stage and whether the pane must receive that stage's prompt
// again. The caller holds dispatchMu.
func (r *workflowRunner) rebindRestartedPaneLocked(ctx context.Context, pane string, pid int) (string, bool, error) {
	if pid <= 0 {
		return "", false, errors.New("restart reported no replacement pane process")
	}
	r.opts.PanePIDs[pane] = pid
	captured, err := r.captureWorkflowPane(ctx, pane)
	if err != nil {
		return "", false, err
	}
	// Never acquire the coordinator lock while holding r.mu.
	coordinatorStage := r.coordinator.CurrentStage()
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.supervision, pane)
	if r.state == nil {
		return "", false, nil
	}
	if r.state.PanePIDs == nil {
		r.state.PanePIDs = make(map[string]int)
	}
	r.state.PanePIDs[pane] = pid
	if r.state.Evidence != nil {
		restartPaneEvidence(r.state.Evidence, pane, captured)
	}
	redeliver := false
	// A policy skip that the run loop has not recorded yet owns the next
	// dispatch; re-sending the old stage's prompt would be wrong.
	if !r.state.Completed && r.state.CurrentStage == coordinatorStage {
		for i := range r.state.Dispatches {
			if r.state.Dispatches[i].Pane == pane && r.state.Dispatches[i].Status == "delivered" {
				r.state.Dispatches[i].Status = "pending"
				redeliver = true
			}
		}
	}
	return r.state.CurrentStage, redeliver, r.saveCheckpointLocked()
}

func (a workflowRunActions) Pause(_ context.Context, reason string) error {
	// Hold r.mu across the store write: recordTransitionLocked mutates the same
	// WorkflowState from the main run loop under the same lock.
	a.r.mu.Lock()
	var saveErr error
	if a.r.store != nil && a.r.state != nil {
		saveErr = a.r.store.Pause(a.r.state, reason, a.r.ports.now())
	}
	a.r.mu.Unlock()
	if saveErr != nil {
		a.r.stop("checkpoint-failed", saveErr)
		return saveErr
	}
	a.r.stop("paused", fmt.Errorf("workflow paused: %s", reason))
	return nil
}

func (a workflowRunActions) SkipStage(_ context.Context) error {
	a.r.dispatchMu.Lock()
	defer a.r.dispatchMu.Unlock()
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
	a.r.dispatchMu.Lock()
	defer a.r.dispatchMu.Unlock()
	stage := a.r.coordinator.CurrentStage()
	a.r.mu.Lock()
	if a.r.state == nil || a.r.state.CurrentStage != stage {
		a.r.mu.Unlock()
		return errors.New("workflow stage changed before retry")
	}
	if len(a.r.state.Dispatches) == 0 || a.r.state.Evidence == nil {
		a.r.mu.Unlock()
		return errors.New("cannot retry a stage before its initial dispatch is established")
	}
	for _, delivery := range a.r.state.Dispatches {
		if delivery.Status != "delivered" {
			a.r.mu.Unlock()
			return errors.New("cannot retry a stage with unfinished or uncertain delivery")
		}
	}
	// This is an intentional retry requested by the template's policy, not
	// crash recovery. Save that new intent before assigning fresh turns.
	a.r.state.Dispatches = nil
	a.r.state.Evidence = nil
	err := a.r.saveCheckpointLocked()
	a.r.mu.Unlock()
	if err != nil {
		a.r.stop("checkpoint-failed", err)
		return err
	}
	targets, err := a.r.stagePanes(stage)
	if err != nil {
		return err
	}
	a.r.ports.notify("workflow %s: retrying stage %s", a.r.template.Name, stage)
	err = a.r.dispatchStageLocked(ctx, stage, targets)
	if err != nil {
		a.r.stop("dispatch-failed", err)
	}
	return err
}

func (a workflowRunActions) Notify(_ context.Context, werr *workflow.WorkflowError, subject string) error {
	a.r.ports.notify("workflow %s: %s: %v", a.r.template.Name, subject, werr)
	return nil
}

// agentFaultAction is the policy for an agent fault: the template's
// on_agent_crash / on_agent_error, or notify when unset, so a fault is never
// silently ignored.
func (r *workflowRunner) agentFaultAction(kind workflow.ErrorType) workflow.ErrorAction {
	var action workflow.ErrorAction
	if eh := r.template.ErrorHandling; eh != nil {
		switch kind {
		case workflow.ErrorAgentCrash:
			action = eh.OnAgentCrash
		case workflow.ErrorAgentError:
			action = eh.OnAgentError
		}
	}
	if action == "" {
		action = workflow.ErrorActionNotify
	}
	return action
}

// superviseAgents observes every workflow pane — not only the current
// stage's — and raises crash/error faults into the template's error_handling
// policy, so on_agent_crash and on_agent_error actions actually run. Each
// fault is raised once per episode: the pane must recover or be restarted
// before it is raised again. Raised faults are checkpointed. It returns an
// error only when the policy itself failed; stop reasons recorded by actions
// (paused, aborted, restart-failed, ...) are classified by the caller.
func (r *workflowRunner) superviseAgents(ctx context.Context) error {
	if r.ports.faults == nil || r.errorHandler == nil {
		return nil
	}
	r.dispatchMu.Lock()
	expected := make(map[string]int, len(r.agents))
	for _, agent := range r.agents {
		expected[agent.ID] = r.opts.PanePIDs[agent.ID]
	}
	r.dispatchMu.Unlock()
	faults, err := r.ports.faults(ctx, expected)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		r.mu.Lock()
		first := !r.healthDown
		r.healthDown = true
		r.mu.Unlock()
		if first {
			r.ports.notify("workflow %s: agent health unavailable; crash/error detection resumes when it recovers: %v", r.template.Name, err)
		}
		return nil
	}
	stage := r.coordinator.CurrentStage()
	now := r.ports.now()
	var raised []*workflow.WorkflowError
	r.mu.Lock()
	r.healthDown = false
	if r.supervision == nil {
		r.supervision = make(map[string]*workflowFaultEpisode)
	}
	for _, agent := range r.agents {
		fault, faulted := faults[agent.ID]
		if !faulted {
			delete(r.supervision, agent.ID) // Recovered: a later fault is a new episode.
			continue
		}
		episode := r.supervision[agent.ID]
		if episode == nil || episode.kind != fault.Kind {
			episode = &workflowFaultEpisode{kind: fault.Kind}
			r.supervision[agent.ID] = episode
		}
		episode.polls++
		threshold := r.opts.AgentFaultPolls
		if fault.Confirmed {
			threshold = 1
		}
		if episode.raised || episode.polls < threshold {
			continue
		}
		episode.raised = true
		werr := &workflow.WorkflowError{
			Type: fault.Kind, Stage: stage, AgentID: agent.ID, Timestamp: now,
			Message: fmt.Sprintf("pane %s (%s): %s", agent.ID, agent.Role, fault.Reason),
		}
		raised = append(raised, werr)
		r.result.Faults = append(r.result.Faults, WorkflowRunFault{
			Pane: agent.ID, Role: agent.Role, Stage: stageLabel(stage), Type: string(fault.Kind),
			Reason: fault.Reason, Action: string(r.agentFaultAction(fault.Kind)),
		})
		if r.state != nil {
			r.state.Errors = append(r.state.Errors, *werr)
		}
	}
	var saveErr error
	if len(raised) > 0 && r.state != nil {
		saveErr = r.saveCheckpointLocked()
	}
	r.mu.Unlock()
	if saveErr != nil {
		r.stop("checkpoint-failed", saveErr)
		return nil
	}
	for _, werr := range raised {
		if reason, _ := r.stopped(); reason != "" || ctx.Err() != nil {
			return nil
		}
		r.ports.notify("workflow %s: %s at stage %s: %s (on_%s = %s)", r.template.Name, werr.Type, stageLabel(werr.Stage), werr.Message, werr.Type, r.agentFaultAction(werr.Type))
		if err := r.errorHandler.Handle(ctx, werr); err != nil {
			return err
		}
	}
	return nil
}

// superviseOrStop runs agent supervision and reports the stop reason, if
// any, that the run must end with.
func (r *workflowRunner) superviseOrStop(ctx context.Context) (string, error) {
	err := r.superviseAgents(ctx)
	if reason, stopErr := r.stopped(); reason != "" {
		return reason, stopErr
	}
	if err != nil && ctx.Err() == nil {
		return "error-handler-failed", err
	}
	return "", nil
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

// recordTransitionLocked persists the stage change to the state store. The
// caller must hold r.dispatchMu (advanceObservation does). It holds r.mu for
// the duration: the TimeoutMonitor goroutine's Pause writes the same
// WorkflowState under the same lock.
func (r *workflowRunner) recordTransitionLocked(newStage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return nil
	}
	now := r.ports.now()
	r.state.StageHistory = append(r.state.StageHistory, workflow.StageRecord{
		Stage: r.state.CurrentStage, StartedAt: r.state.StageStartedAt,
		CompletedAt: now, DurationSec: int(now.Sub(r.state.StageStartedAt).Seconds()),
		Result: "advanced", Trigger: "trigger",
	})
	r.state.CurrentStage = newStage
	r.state.StageStartedAt = now
	r.state.Dispatches = nil
	r.state.Evidence = nil
	r.state.Completed = r.stageIsTerminal(newStage)
	// History and destination commit in ONE snapshot. A crash between two
	// separate saves must not resurrect a stage already marked advanced.
	return r.saveCheckpointLocked()
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
	if r.store != nil {
		lockCtx, lockCancel := context.WithTimeout(runCtx, time.Second)
		unlock, err := r.store.Acquire(lockCtx, r.opts.Session)
		lockCancel()
		if err != nil {
			return fail("checkpoint-locked", err)
		}
		defer unlock()
	}
	if err := r.loadCheckpoint(); err != nil {
		return fail("resume-rejected", err)
	}
	result.Resumed = r.opts.Resume

	// Error handling per template policy. Agent faults always have a policy
	// (unset actions notify); trigger failures use it only when the template
	// declares error_handling.
	eh := r.template.ErrorHandling
	if eh == nil {
		eh = &workflow.ErrorConfig{}
	}
	r.errorHandler = workflow.NewErrorHandler(workflow.ErrorHandlingConfig{
		OnAgentCrash:        r.agentFaultAction(workflow.ErrorAgentCrash),
		OnAgentError:        r.agentFaultAction(workflow.ErrorAgentError),
		OnTriggerFailed:     workflow.ErrorActionAbort,
		OnTimeout:           eh.OnTimeout,
		MaxRetriesPerStage:  eh.MaxRetriesPerStage,
		MaxRestartsPerAgent: r.opts.MaxAgentRestarts,
	}, workflowRunActions{r: r})
	if eh.StageTimeoutMinutes > 0 {
		r.timeoutMonitor = workflow.NewTimeoutMonitor(
			time.Duration(eh.StageTimeoutMinutes)*time.Minute, r.errorHandler, r.coordinator.CurrentStage)
	}

	tctx := &workflow.TriggerContext{
		Context:     runCtx,
		ProjectRoot: r.opts.ProjectRoot,
		Session:     r.opts.Session,
		Now:         r.ports.now,
	}
	if r.state != nil {
		restorer, ok := r.coordinator.(interface {
			StartAt(*workflow.TriggerContext, string, time.Time, map[string]int) error
		})
		if !ok {
			return fail("resume-rejected", errors.New("coordinator does not support checkpoint resume"))
		}
		if err := restorer.StartAt(tctx, r.state.CurrentStage, r.state.StageStartedAt, r.state.NextByRole); err != nil {
			return fail("resume-rejected", err)
		}
	} else if err := r.coordinator.Start(tctx); err != nil {
		return fail("start-failed", err)
	}
	defer func() {
		cancel()
		if r.timeoutMonitor != nil {
			r.timeoutMonitor.StopAndWait()
		}
		if err := r.coordinator.Stop(); err != nil {
			r.ports.notify("workflow %s: stop coordinator: %v", r.template.Name, err)
		}
	}()

	stage := r.coordinator.CurrentStage()
	result.Stages = append(result.Stages, stageLabel(stage))
	if r.state != nil && r.state.Completed {
		result.Success, result.Completed, result.Reason = true, true, "completed"
		return *result, nil
	}

	// Durable state checkpoint.
	if r.state == nil {
		paneRoles := make(map[string]string, len(r.agents))
		panePIDs := make(map[string]int, len(r.agents))
		for _, agent := range r.agents {
			paneRoles[agent.ID] = agent.Role
			panePIDs[agent.ID] = r.opts.PanePIDs[agent.ID]
		}
		hash, err := r.templateHash()
		if err != nil {
			return fail("checkpoint-failed", err)
		}
		state := &workflow.WorkflowState{
			WorkflowName:   r.template.Name,
			SessionName:    r.opts.Session,
			CurrentStage:   stage,
			StageStartedAt: r.ports.now(),
			Agents:         paneRoles,
			Variables:      r.opts.Vars,
			ResumeVersion:  1,
			TemplateHash:   hash,
			ProjectRoot:    r.opts.ProjectRoot,
			PanePIDs:       panePIDs,
		}
		r.mu.Lock()
		r.state = state
		err = r.saveCheckpointLocked()
		r.mu.Unlock()
		if err != nil {
			return fail("checkpoint-failed", err)
		}
	} else if r.state.Paused {
		if err := r.store.Resume(r.state); err != nil {
			return fail("checkpoint-failed", err)
		}
	}

	// Parallel workflows without a flow dispatch everyone and are done. There
	// is no poll loop to supervise, so agents are checked once, just before
	// the dispatch (a single check acts on confirmed crashes only).
	if parallel, ok := r.coordinator.(*workflow.ParallelCoordinator); ok && r.template.Flow == nil {
		if reason, err := r.superviseOrStop(runCtx); reason != "" {
			return fail(reason, err)
		}
		if err := r.dispatchStage(runCtx, stage, parallel.Agents()); err != nil {
			return fail("dispatch-failed", err)
		}
		if err := r.completeCheckpoint(); err != nil {
			return fail("checkpoint-failed", err)
		}
		result.Success = true
		result.Completed = true
		result.Reason = "completed"
		return *result, nil
	}

	targets, err := r.checkpointTargets(stage)
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
		if reason, stopErr := r.stopped(); reason != "" {
			return fail(reason, stopErr)
		}
		if runCtx.Err() != nil {
			if reason, stopErr := r.stopped(); reason != "" {
				return fail(reason, stopErr)
			}
			// Distinguish the --timeout deadline from an operator cancel
			// (Ctrl-C / parent context): only a deadline is a "timeout".
			reason := "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "timeout"
			}
			return fail(reason, fmt.Errorf("workflow run canceled: %w", context.Cause(ctx)))
		}
		if r.stageIsTerminal(stage) {
			if err := r.completeCheckpoint(); err != nil {
				return fail("checkpoint-failed", err)
			}
			result.Success = true
			result.Completed = true
			result.Reason = "completed"
			return *result, nil
		}

		r.ports.sleep(runCtx, r.opts.Interval)
		if runCtx.Err() != nil {
			continue // loop once more to classify via stopped()/timeout
		}

		if r.opts.FireManual {
			r.fireManualTriggers()
		}

		// Crashed or errored agents run the template's on_agent_crash /
		// on_agent_error policy before their output is trusted again.
		if reason, err := r.superviseOrStop(runCtx); reason != "" {
			return fail(reason, err)
		}
		if runCtx.Err() != nil {
			continue
		}

		observed, err := r.observe(runCtx)
		if err != nil {
			if runCtx.Err() != nil {
				continue // Classify cancellation/timeout at the loop boundary.
			}
			r.mu.Lock()
			if r.state != nil {
				now := r.ports.now()
				r.state.Paused, r.state.PausedAt, r.state.PauseReason = true, &now, "output evidence unavailable: "+err.Error()
				err = errors.Join(err, r.saveCheckpointLocked())
			}
			r.mu.Unlock()
			return fail("observation-failed", err)
		}
		fired, newStage, stale, err := r.advanceObservation(runCtx, observed)
		if stale {
			continue
		}
		if err != nil {
			if runCtx.Err() != nil {
				continue // Preserve the stop policy or cancellation/timeout reason.
			}
			if r.template.ErrorHandling != nil {
				if handleErr := r.errorHandler.Handle(runCtx, &workflow.WorkflowError{
					Type: workflow.ErrorTriggerFailed, Stage: stage, Message: err.Error(), Timestamp: r.ports.now(),
				}); handleErr != nil {
					return fail("error-handler-failed", handleErr)
				}
				continue
			}
			return fail("trigger-failed", err)
		}

		if !fired && newStage == stage {
			continue
		}
		result.Transitions++
		r.ports.notify("workflow %s: stage %s → %s (transition %d)", r.template.Name, stageLabel(stage), stageLabel(newStage), result.Transitions)
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
		// A quoted "~" reaches us unexpanded by the shell; expand it here so
		// `ntm workflow run "~/flows/x.toml"` works, and say so when it can't.
		path := ref
		if strings.HasPrefix(path, "~") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				if path == "~" {
					path = home
				} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
					path = filepath.Join(home, path[2:])
				}
			}
			if strings.HasPrefix(path, "~") {
				return nil, fmt.Errorf("workflow path %q starts with an unexpandable '~' (quoting prevents shell tilde expansion, and ~user paths are not supported); use an absolute path", ref)
			}
		}
		content, err := os.ReadFile(path)
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
			tmpl.Source = "file:" + path
			return &tmpl, nil
		}
		tmpl, err := workflow.ParseAndValidateWorkflow(string(content))
		if err != nil {
			return nil, fmt.Errorf("invalid workflow %s: %w", ref, err)
		}
		tmpl.Source = "file:" + path
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
func workflowGatedDispatch(workflowName string, beforeDispatch func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error) func(ctx context.Context, session, paneID, prompt string) error {
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
			beforeDispatch:      beforeDispatch,
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

// validateWorkflowPaneLifetimes observes the exact configured tmux server,
// including SSH mode. Remote PIDs are compared as identifiers only.
func validateWorkflowPaneLifetimes(ctx context.Context, session string, expected map[string]int) error {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return err
	}
	live := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		if _, duplicate := live[pane.ID]; duplicate {
			return fmt.Errorf("workflow pane %s has ambiguous session membership", pane.ID)
		}
		live[pane.ID] = pane
	}
	for id, pid := range expected {
		pane, ok := live[id]
		if pid <= 0 || !ok || pane.PID != pid || pane.Dead {
			return fmt.Errorf("workflow pane %s changed process lifetime or is unavailable", id)
		}
	}
	return ctx.Err()
}

// workflowAgentFaults is the production fault port. It classifies the run's
// panes with the observation behind `ntm health` and the [resilience]
// monitor. A crash is confirmed evidence: the pane left the session, its
// process died (pane_dead), or — on a local tmux server, where pane PIDs are
// inspectable — its shell has no live child, i.e. the agent CLI exited back
// to the shell. A pane whose PID no longer matches the run's binding is left
// to the identity check, which stops the run.
func workflowAgentFaults(ctx context.Context, session string, expected map[string]int) (map[string]workflowAgentFault, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, err
	}
	live := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		live[pane.ID] = pane
	}
	faults := make(map[string]workflowAgentFault)
	for id := range expected {
		pane, ok := live[id]
		switch {
		case !ok:
			faults[id] = workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "pane no longer exists", Confirmed: true}
		case pane.Dead:
			faults[id] = workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "pane process exited (pane is dead)", Confirmed: true}
		}
	}
	if len(faults) == len(expected) {
		return faults, nil
	}
	report, err := health.CheckSession(ctx, session)
	if err != nil {
		return nil, err
	}
	local := tmux.DefaultClient.Remote == ""
	for _, row := range report.Agents {
		pid, ok := expected[row.PaneID]
		if !ok || pid <= 0 || row.ShellPID != pid {
			continue
		}
		if _, known := faults[row.PaneID]; known {
			continue
		}
		if fault, faulted := classifyWorkflowAgentHealth(row, local, tmux.PaneAgentAlive); faulted {
			faults[row.PaneID] = fault
		}
	}
	return faults, nil
}

// classifyWorkflowAgentHealth maps one `ntm health` row onto the workflow's
// fault kinds. Process liveness is authoritative and only inspectable for a
// local tmux server (remote PIDs are identifiers). An agent error is the
// health classification's error state: rate limited, an authentication
// failure, a blocking interactive gate, or the status detector's error state.
// Those are text classifications, so they require a fresh observation and
// are debounced by the caller.
func classifyWorkflowAgentHealth(row health.AgentHealth, local bool, agentAlive func(int) bool) (workflowAgentFault, bool) {
	if local && row.ShellPID > 0 && !agentAlive(row.ShellPID) {
		return workflowAgentFault{Kind: workflow.ErrorAgentCrash, Reason: "agent process exited; the pane is back at its shell", Confirmed: true}, true
	}
	if row.ObservationFreshness != status.FreshnessFresh {
		return workflowAgentFault{}, false
	}
	if row.RateLimited {
		return workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is rate limited"}, true
	}
	for _, issue := range row.Issues {
		switch issue.Type {
		case "auth_error", "trust_prompt", "interactive_gate":
			return workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: issue.Message}, true
		}
	}
	if row.ObservedState == status.StateError {
		reason := "agent is in an error state"
		if len(row.Issues) > 0 {
			reason += ": " + row.Issues[0].Message
		}
		return workflowAgentFault{Kind: workflow.ErrorAgentError, Reason: reason}, true
	}
	return workflowAgentFault{}, false
}

// workflowRestartAgent is the production restart port: the shared restart
// engine behind `ntm respawn` and --robot-restart-pane (respawn-pane -k,
// saved launch-spec replay, agent ready gate), applied to one pane.
func workflowRestartAgent(ctx context.Context, session, paneID string) (int, error) {
	out, err := robot.GetRestartPaneContext(ctx, robot.RestartPaneOptions{
		Session: session,
		Panes:   []string{paneID},
		Config:  cfg,
	})
	if err != nil {
		return 0, err
	}
	return workflowRestartOutcome(out)
}

// workflowRestartOutcome accepts only a verified relaunch — exactly one pane
// respawned onto a new shell PID with its agent CLI ready — and returns that
// PID for the run to rebind to.
func workflowRestartOutcome(out *robot.RestartPaneOutput) (int, error) {
	if out == nil {
		return 0, errors.New("restart engine returned no result")
	}
	if out.Success && len(out.Restarted) == 1 {
		key := out.Restarted[0]
		pids := out.PaneShellPIDs[key]
		if out.AgentRelaunchStatus[key] == robot.RestartAgentRelaunchReady && pids.After > 0 && pids.After != pids.Before {
			return pids.After, nil
		}
	}
	reasons := make([]string, 0, len(out.Failed)+1)
	for _, failure := range out.Failed {
		reasons = append(reasons, failure.Pane+": "+failure.Reason)
	}
	if len(reasons) == 0 && out.Error != "" {
		reasons = append(reasons, out.Error)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no pane was respawned with a ready agent")
	}
	return 0, errors.New(strings.Join(reasons, "; "))
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
		restartFlag    bool
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

--resume continues the saved stage with the same template, variables, and
pane lifetimes. Confirmed prompts are not sent again; pending prompts resume
their saved plan. An interrupted send with an unknown outcome is refused.
Unfinished checkpoints require --resume, or --restart to deliberately repeat
the entire workflow. --resume never silently means restart.

Every poll checks all of the workflow's agent panes. A crash (the pane left
the session or died, or the agent CLI exited back to its shell) runs the
template's error_handling on_agent_crash action; an agent error (rate limited,
authentication failure, a blocking dialog, or the status detector's error
state, lasting [resilience] crash_threshold polls) runs on_agent_error. Unset
actions only report the fault. restart_agent restarts the pane with the same
engine as 'ntm respawn' and sends the current stage prompt again, at most
[resilience] max_restarts times per pane per run; the next fault aborts.

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
				Restart:        restartFlag,
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
	cmd.Flags().BoolVar(&resumeFlag, "resume", false, "continue the saved stage without repeating confirmed prompt deliveries")
	cmd.Flags().BoolVar(&restartFlag, "restart", false, "explicitly replace an unfinished checkpoint and repeat the workflow from the beginning")
	cmd.MarkFlagsMutuallyExclusive("resume", "restart")
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
	Restart        bool
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
	varsTemplate := *template
	if flags.Resume {
		// Only explicit overrides are parsed here. The runner validates them
		// against and restores required/default variables from the checkpoint.
		varsTemplate.Prompts = nil
	}
	vars, err := resolveWorkflowVars(&varsTemplate, flags.Vars)
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
	panePIDs := make(map[string]int, len(agents))
	paneTypes := make(map[string]string, len(agents))
	for _, agent := range agents {
		for _, pane := range panes {
			if pane.ID == agent.ID {
				panePIDs[pane.ID] = pane.PID
				paneTypes[pane.ID] = string(pane.Type)
				break
			}
		}
	}
	if err := validateWorkflowPaneLifetimes(ctx, session, panePIDs); err != nil {
		return failEarly(err)
	}

	projectRoot := strings.TrimSpace(flags.ProjectRoot)
	if projectRoot == "" {
		projectRoot = getSessionWorkingDir(ctx, session, false)
	}
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	// restart_agent's budget and the agent-error debounce reuse the
	// [resilience] auto-restart settings.
	resilienceCfg := config.Default().Resilience
	if cfg != nil {
		resilienceCfg = cfg.Resilience
	}
	opts := workflowRunOptions{
		Session:          session,
		ProjectRoot:      projectRoot,
		Vars:             vars,
		PanePIDs:         panePIDs,
		PaneTypes:        paneTypes,
		MaxAgentRestarts: resilienceCfg.MaxRestarts,
		AgentFaultPolls:  resilienceCfg.CrashThreshold,
		MaxTransitions:   flags.MaxTransitions,
		Interval:         flags.Interval,
		FireManual:       flags.FireManual,
		TriggerTimeout:   flags.TriggerTimeout,
		StateDir:         filepath.Join(projectRoot, ".ntm", "workflows", "state"),
		Resume:           flags.Resume,
		Restart:          flags.Restart,
	}

	notify := func(format string, args ...any) {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	var runner *workflowRunner
	// Identity checks read the runner's pane binding, which restart_agent
	// moves to a replacement process. Every caller holds runner.dispatchMu.
	validatePanes := func(ctx context.Context) error {
		return validateWorkflowPaneLifetimes(ctx, session, runner.opts.PanePIDs)
	}
	beforeDispatch := func(ctx context.Context, request dispatchsvc.Request, deliveries []dispatchsvc.Delivery) error {
		if request.Session != session || len(deliveries) != 1 {
			return errors.New("workflow dispatch changed its session or single-pane target")
		}
		if err := validatePanes(ctx); err != nil {
			return err
		}
		stage := runner.coordinator.CurrentStage()
		runner.mu.Lock()
		matchesStage := runner.state != nil && runner.state.CurrentStage == stage && runner.state.Evidence != nil
		runner.mu.Unlock()
		if !matchesStage {
			return errors.New("workflow stage changed before the prepared prompt could be delivered")
		}
		for _, delivery := range deliveries {
			pane := delivery.Target.Pane
			if pid, ok := runner.opts.PanePIDs[pane.ID]; !ok || pid <= 0 || pane.PID != pid {
				return fmt.Errorf("prepared workflow delivery changed pane %s lifetime", pane.ID)
			}
			role := ""
			for _, agent := range agents {
				if agent.ID == pane.ID {
					role = agent.Role
					break
				}
			}
			if err := runner.validateVerdictPrompt(stage, role, delivery.Message); err != nil {
				return err
			}
			if err := runner.refreshDeliveryBoundary(ctx, pane.ID); err != nil {
				return err
			}
			if err := runner.pinPiDispatchPrompt(stage, pane.ID, delivery.Message); err != nil {
				return err
			}
		}
		return nil
	}
	runner, err = newWorkflowRunner(template, agents, opts, workflowRunPorts{
		dispatch: workflowGatedDispatch(template.Name, beforeDispatch),
		capture:  tmux.CapturePaneOutputContext,
		validate: validatePanes,
		faults: func(ctx context.Context, expected map[string]int) (map[string]workflowAgentFault, error) {
			return workflowAgentFaults(ctx, session, expected)
		},
		restart: workflowRestartAgent,
		notify:  notify,
	})
	if err != nil {
		return failEarly(err)
	}

	result, runErr := runner.Run(ctx)
	return emit(result, runErr)
}
