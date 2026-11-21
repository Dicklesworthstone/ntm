// Package workflowrun wires the workflow RuntimeCoordinator (internal/workflow)
// to live tmux sessions and durable checkpoints. Both the CLI (`ntm workflow run`)
// and the REST/managed-job server (`POST /api/v1/workflows/{name}/run`,
// `POST /api/v1/jobs` with `type: "workflow_run"`) execute through this single
// production runner implementation.
package workflowrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// CaptureLines is how much pane scrollback feeds agent_says and idle
// observation on each poll.
const CaptureLines = 200

const (
	EvidenceVersion  = 1
	CaptureMaxBytes  = 512 << 10
	EvidenceMaxBytes = 64 << 10
)

// RunAgent reports one role→pane assignment in the run result.
type RunAgent struct {
	Role string `json:"role"`
	Pane string `json:"pane"`
}

// Result is the structured outcome of a workflow execution.
type Result struct {
	Success      bool       `json:"success"`
	Workflow     string     `json:"workflow"`
	Source       string     `json:"source"`
	Session      string     `json:"session"`
	Coordination string     `json:"coordination"`
	Agents       []RunAgent `json:"agents"`
	Stages       []string   `json:"stages"`
	Transitions  int        `json:"transitions"`
	Completed    bool       `json:"completed"`
	Resumed      bool       `json:"resumed,omitempty"`
	Faults       []RunFault `json:"faults,omitempty"`
	Restarts     int        `json:"restarts,omitempty"`
	Reason       string     `json:"reason"`
	Error        string     `json:"error,omitempty"`
}

// RunFault reports one agent fault raised into the template's
// error_handling policy.
type RunFault struct {
	Pane   string `json:"pane"`
	Role   string `json:"role"`
	Stage  string `json:"stage"`
	Type   string `json:"type"` // agent_crash | agent_error
	Reason string `json:"reason"`
	Action string `json:"action"` // the error action the policy applied
}

// AgentFault is one agent pane's current fault.
type AgentFault struct {
	Kind   workflow.ErrorType // ErrorAgentCrash or ErrorAgentError
	Reason string
	// Confirmed marks topology or process evidence (pane gone or dead, no
	// live agent process); it is raised on first sight. Text-classified
	// states must persist for AgentFaultPolls consecutive observations.
	Confirmed bool
}

// faultEpisode tracks one pane's current fault so each fault is raised once,
// not on every poll, until the pane recovers or is restarted.
type faultEpisode struct {
	kind   workflow.ErrorType
	polls  int
	raised bool
}

// Ports isolates the runner's side effects so tests can drive the coordinator
// loop with fake dispatch/capture implementations while production runs use
// real gated tmux transport.
type Ports struct {
	// Dispatch delivers one prompt to one pane and fails when delivery or
	// submission verification fails.
	Dispatch func(ctx context.Context, session, paneID, prompt string) error
	// Capture returns recent pane output for trigger observation.
	Capture func(ctx context.Context, paneID string, lines int) (string, error)
	// Validate binds production observations to the same physical pane/PID
	// throughout the run. Custom test transports own their pane identities.
	Validate func(context.Context) error
	// Endpoint returns the currently configured tmux host. It is checked
	// around observations as well as before dispatch, so matching pane IDs
	// cannot redirect a saved workflow when the transport changes.
	Endpoint func() string
	// Faults classifies the run's agent panes (pane → bound shell PID) with
	// ntm's shared health observation; panes absent from the result are
	// healthy. Nil disables agent crash/error supervision.
	Faults func(ctx context.Context, expected map[string]int) (map[string]AgentFault, error)
	// Restart relaunches one pane's agent through ntm's shared restart
	// engine and returns the replacement shell PID the run must rebind to.
	Restart func(ctx context.Context, session, paneID string) (int, error)
	// Notify surfaces progress/warnings on human or structured log paths.
	Notify func(format string, args ...any)
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
}

func (p *Ports) fillDefaults() {
	if p.Notify == nil {
		p.Notify = func(string, ...any) {}
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Sleep == nil {
		p.Sleep = func(ctx context.Context, d time.Duration) {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
	}
}

// Options configures one workflow run.
type Options struct {
	Session        string
	ProjectRoot    string
	TmuxEndpoint   string // "local" or "ssh:<target>"; durable host identity.
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

// Observation carries one durably-bound trigger observation frame.
type Observation struct {
	Context *workflow.TriggerContext
	Stage   string
	Round   int
}

// Runner drives one coordinator instance against a live session.
type Runner struct {
	template    *workflow.WorkflowTemplate
	agents      []workflow.CoordinatorAgent
	Coordinator workflow.Coordinator
	Opts        Options
	Ports       Ports

	Store *workflow.StateStore
	State *workflow.WorkflowState
	// Checkpoints are control data, never evidence that an agent finished a
	// file task. The actual store can be inside or outside the project tree.
	fileTriggerExcludes []string

	errorHandler *workflow.ErrorHandler

	// DispatchMu serializes state decisions and whole-stage deliveries.
	// Timeout policies run synchronously on the runner's owner loop.
	DispatchMu sync.Mutex

	Mu         sync.Mutex
	manuals    []*workflow.ManualTrigger
	turn       int
	stopReason string
	stopErr    error
	cancel     context.CancelFunc
	// policyAdvanced tells the owner loop to dispatch and report a skip that
	// has already committed its destination, including self-transitions.
	policyAdvanced bool

	// supervision holds each faulted pane's current episode; healthDown
	// suppresses repeated "health unavailable" notices. Both guarded by Mu.
	supervision map[string]*faultEpisode
	healthDown  bool

	result Result
}

// NewRunner validates the template against the pane assignment and builds the
// coordinator with a trigger registry that tracks manual triggers (for
// --fire-manual) and applies the configured command-trigger timeout.
func NewRunner(template *workflow.WorkflowTemplate, agents []workflow.CoordinatorAgent, opts Options, ports Ports) (*Runner, error) {
	ports.fillDefaults()
	if opts.Resume && opts.Restart {
		return nil, errors.New("--resume and --restart are mutually exclusive")
	}
	if (opts.Resume || opts.Restart) && strings.TrimSpace(opts.StateDir) == "" {
		return nil, errors.New("workflow resume/restart requires a durable state directory")
	}
	if ports.Dispatch == nil || ports.Capture == nil {
		return nil, errors.New("workflow dispatch and capture ports are required")
	}
	if opts.TmuxEndpoint == "" {
		opts.TmuxEndpoint = "local"
	}
	if err := ValidateEndpoint(template, opts.TmuxEndpoint); err != nil {
		return nil, err
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
	// replacement process (under DispatchMu), and every identity check reads
	// this map rather than the caller's.
	pids := make(map[string]int, len(opts.PanePIDs))
	for pane, pid := range opts.PanePIDs {
		pids[pane] = pid
	}
	opts.PanePIDs = pids
	r := &Runner{
		template: template,
		agents:   append([]workflow.CoordinatorAgent(nil), agents...),
		Opts:     opts,
		Ports:    ports,
	}

	defaults := workflow.NewTriggerRegistry()
	registry := workflow.NewTriggerRegistry()
	registry.Register(workflow.TriggerManual, func(cfg workflow.Trigger) (workflow.RuntimeTrigger, error) {
		trig, err := defaults.Create(cfg)
		if err != nil {
			return nil, err
		}
		if manual, ok := trig.(*workflow.ManualTrigger); ok && manual.Type() == workflow.TriggerManual {
			r.Mu.Lock()
			r.manuals = append(r.manuals, manual)
			r.Mu.Unlock()
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
				if limiter, ok := trig.(interface{ SetTimeout(time.Duration) }); ok && trig.Type() == kind {
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
	r.Coordinator = coord

	if dir := strings.TrimSpace(opts.StateDir); dir != "" {
		r.Store = &workflow.StateStore{Dir: dir}
	}
	return r, nil
}

// StageRole resolves which workflow role acts in a stage. Resolution order:
// exact role match, template routing keyed by the stage name, the role named
// by an outgoing transition trigger, then the first declared agent role.
func (r *Runner) StageRole(stage string) string {
	if len(r.template.GetAgentsByRole(stage)) > 0 {
		return stage
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
func (r *Runner) stageIsTerminal(stage string) bool {
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
func (r *Runner) stagePanes(stage string) ([]workflow.CoordinatorAgent, error) {
	role := r.StageRole(stage)
	parallelStage := r.template.Coordination == workflow.CoordParallel ||
		(r.template.Flow != nil && r.template.Flow.ParallelWithinStage)
	if role == stage && !parallelStage {
		agent, err := r.Coordinator.GetAgentForTask(workflow.Task{ID: stage, Path: stage})
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

// TriggerHint describes how a transition advances, WITHOUT quoting agent_says
// patterns: quoting the pattern into a pane transcript would satisfy the
// trigger with our own dispatch echo.
func TriggerHint(tr workflow.Transition) string {
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
func (r *Runner) stagePrompt(stage string, agent workflow.CoordinatorAgent, turn int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[ntm workflow %s] stage %s turn %d\n", r.template.Name, StageLabel(stage), turn)
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
	if len(r.Opts.Vars) > 0 {
		keys := make([]string, 0, len(r.Opts.Vars))
		for key := range r.Opts.Vars {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("context:\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", key, r.Opts.Vars[key])
		}
	}
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage {
				fmt.Fprintf(&b, "advance: %s\n", TriggerHint(tr))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// StageLabel formats a stage name for human-readable output, using "(parallel)"
// for flowless parallel workflows.
func StageLabel(stage string) string {
	if stage == "" {
		return "(parallel)"
	}
	return stage
}

// dispatchStage delivers the stage prompt through the gated dispatch port.
func (r *Runner) dispatchStage(ctx context.Context, stage string, targets []workflow.CoordinatorAgent) error {
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	return r.dispatchStageLocked(ctx, stage, targets)
}

func (r *Runner) dispatchStageLocked(ctx context.Context, stage string, targets []workflow.CoordinatorAgent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	checkCtx, cancelCheck := r.stageOperationContext(ctx)
	defer cancelCheck()
	ctx = checkCtx
	var nextByRole map[string]int
	if routing, ok := r.Coordinator.(interface{ RoutingState() map[string]int }); ok {
		nextByRole = routing.RoutingState()
	}
	if err := r.validatePanes(ctx); err != nil {
		return err
	}
	r.Mu.Lock()
	needsBoundary := r.State != nil && r.State.Evidence == nil
	r.Mu.Unlock()
	var boundary map[string]workflow.StagePaneEvidence
	if needsBoundary {
		var err error
		boundary, err = r.captureStageBoundary(ctx)
		if err != nil {
			return err
		}
	}
	files, err := r.snapshotFileTriggers(stage)
	if err != nil {
		return err
	}
	r.Mu.Lock()
	if r.State == nil || r.State.CurrentStage != stage {
		r.Mu.Unlock()
		return errors.New("workflow stage changed before dispatch; no prompt sent")
	}
	if r.State.Dispatches == nil {
		r.State.Dispatches = make([]workflow.StageDispatch, 0, len(targets))
		for _, agent := range targets {
			r.turn++
			prompt := r.stagePrompt(stage, agent, r.turn)
			if err := r.validateVerdictPrompt(stage, agent.Role, prompt); err != nil {
				r.Mu.Unlock()
				return err
			}
			if _, err := r.piDispatchEcho(stage, agent.ID, agent.Role, prompt); err != nil {
				r.Mu.Unlock()
				return err
			}
			r.State.Dispatches = append(r.State.Dispatches, workflow.StageDispatch{
				Pane: agent.ID, Role: agent.Role, Turn: r.turn, Status: "pending",
			})
		}
		r.State.Turn = r.turn
		r.State.NextByRole = nextByRole
	}
	if r.State.Evidence == nil {
		r.State.Evidence = &workflow.StageEvidence{
			Version: EvidenceVersion, Stage: stage, StartedAt: r.State.StageStartedAt,
			Round: r.State.Turn, Panes: boundary, Matches: make(map[int][]string),
		}
	}
	if files != nil {
		r.State.Files = &workflow.StageFileEvidence{
			Version: EvidenceVersion, Stage: stage, StartedAt: r.State.StageStartedAt,
			Round: r.State.Turn, Triggers: files,
		}
	}
	if r.State.Timeout == nil && r.stageTimeout() > 0 && !r.stageIsTerminal(stage) {
		r.State.Timeout = &workflow.StageTimeout{
			Version: 1, Stage: stage, StartedAt: r.State.StageStartedAt,
			Round: r.State.Turn, Deadline: r.Ports.Now().Add(r.stageTimeout()),
			Action: r.timeoutAction(),
		}
	}
	if err := r.saveCheckpointLocked(); err != nil {
		r.Mu.Unlock()
		return err
	}
	plan := append([]workflow.StageDispatch(nil), r.State.Dispatches...)
	r.Mu.Unlock()
	ctx, cancel := r.stageOperationContext(ctx)
	defer cancel()
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
		r.Mu.Lock()
		r.State.Dispatches[i].Status = "sending"
		err := r.saveCheckpointLocked()
		r.Mu.Unlock()
		if err != nil {
			return err // Nothing is sent without its durable intent.
		}
		agent := workflow.CoordinatorAgent{ID: delivery.Pane, Role: delivery.Role}
		prompt := r.stagePrompt(stage, agent, delivery.Turn)
		if err := r.PinPiDispatchPrompt(stage, agent.ID, prompt); err != nil {
			return err
		}
		r.Ports.Notify("workflow %s: stage %s → %s (%s), turn %d", r.template.Name, StageLabel(stage), agent.ID, agent.Role, delivery.Turn)
		if err := r.Ports.Dispatch(ctx, r.Opts.Session, agent.ID, prompt); err != nil {
			// The port may have typed part or all of the prompt. Leave sending
			// on disk: an error is NOT proof that replay would be safe.
			return fmt.Errorf("dispatch stage %q to pane %s (%s): %w", StageLabel(stage), agent.ID, agent.Role, err)
		}
		r.Mu.Lock()
		r.State.Dispatches[i].Status = "delivered"
		err = r.saveCheckpointLocked()
		r.Mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// PinPiDispatchPrompt runs before sending. The production preparation hook
// replaces the initial fingerprint with the exact final message after all
// enrichment and redaction, while the delivery is still durably "sending".
func (r *Runner) PinPiDispatchPrompt(stage, pane, prompt string) error {
	if r.workflowTranscriptType(pane) != "pi" {
		return nil
	}
	echo := status.NormalizeAgentEcho(prompt)
	if len(echo) == 0 || len(echo) > CaptureMaxBytes {
		return fmt.Errorf("workflow pi prompt for pane %s exceeds the observable echo bounds", pane)
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State == nil || r.State.CurrentStage != stage {
		return errors.New("workflow stage changed before its pi prompt could be recorded")
	}
	for i := range r.State.Dispatches {
		delivery := &r.State.Dispatches[i]
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

func (r *Runner) piDispatchEcho(stage, pane, role, prompt string) (status.PromptEchoFingerprint, error) {
	if r.workflowTranscriptType(pane) != "pi" {
		return status.PromptEchoFingerprint{}, nil
	}
	fingerprint, rendered, ok := status.PiPromptEcho(prompt)
	if !ok || fingerprint.Length == 0 || fingerprint.Length > CaptureMaxBytes {
		return status.PromptEchoFingerprint{}, fmt.Errorf("workflow pi prompt for pane %s uses unsupported display formatting; use plain text or supported Markdown before dispatch", pane)
	}
	if err := r.validateVerdictPrompt(stage, role, rendered); err != nil {
		return status.PromptEchoFingerprint{}, fmt.Errorf("rendered pi prompt: %w", err)
	}
	return fingerprint, nil
}

func (r *Runner) checkpointTargets(stage string) ([]workflow.CoordinatorAgent, error) {
	r.Mu.Lock()
	var targets []workflow.CoordinatorAgent
	if r.State != nil && r.State.Dispatches != nil {
		for _, delivery := range r.State.Dispatches {
			targets = append(targets, workflow.CoordinatorAgent{ID: delivery.Pane, Role: delivery.Role})
		}
		r.Mu.Unlock()
		return targets, nil
	}
	r.Mu.Unlock()
	return r.stagePanes(stage)
}

func (r *Runner) saveCheckpointLocked() error {
	if r.Store == nil {
		return nil
	}
	if err := r.State.RefreshManualGate(r.template); err != nil {
		return fmt.Errorf("prepare workflow manual gate: %w", err)
	}
	if err := r.Store.Save(r.State); err != nil {
		return fmt.Errorf("persist workflow checkpoint before further execution: %w", err)
	}
	return nil
}

func (r *Runner) loadCheckpoint() error {
	if r.Store == nil {
		return nil
	}
	prior, err := r.Store.Load(r.Opts.Session)
	if err != nil {
		return err
	}
	if !r.Opts.Resume {
		if prior != nil && !prior.Completed && !r.Opts.Restart {
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
	if prior.ResumeVersion != 1 || prior.TemplateHash != hash || prior.WorkflowName != r.template.Name || prior.ProjectRoot != r.Opts.ProjectRoot {
		return errors.New("workflow checkpoint version, template, or project differs; refusing unsafe resume (use --restart for a deliberate fresh run)")
	}
	if prior.TmuxEndpoint == "" {
		return errors.New("workflow checkpoint lacks its tmux endpoint; inspect the original agents before a deliberate --restart")
	}
	if prior.TmuxEndpoint != r.Opts.TmuxEndpoint {
		return fmt.Errorf("workflow checkpoint belongs to tmux endpoint %q, not %q; resume with the original endpoint", prior.TmuxEndpoint, r.Opts.TmuxEndpoint)
	}
	if prior.StageStartedAt.IsZero() || prior.StageStartedAt.After(r.Ports.Now()) || prior.Turn < 0 {
		return errors.New("workflow checkpoint has invalid timing or turn state")
	}
	if len(prior.Agents) != len(r.agents) {
		return errors.New("workflow checkpoint pane assignment changed; refusing unsafe resume")
	}
	for _, agent := range r.agents {
		if role, ok := prior.Agents[agent.ID]; !ok || role != agent.Role || prior.PanePIDs[agent.ID] != r.Opts.PanePIDs[agent.ID] {
			return fmt.Errorf("workflow pane %s changed role or lifetime; refusing unsafe resume", agent.ID)
		}
	}
	for key, value := range r.Opts.Vars {
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
			if err != nil || len(digest) != sha256.Size || delivery.PromptEchoLength <= 0 || delivery.PromptEchoLength > CaptureMaxBytes {
				return errors.New("workflow checkpoint has invalid delivered prompt evidence")
			}
		}
		if delivery.PiPromptEchoHash != "" || delivery.PiPromptEchoLength != 0 {
			digest, err := hex.DecodeString(delivery.PiPromptEchoHash)
			if err != nil || len(digest) != sha256.Size || delivery.PiPromptEchoLength <= 0 || delivery.PiPromptEchoLength > CaptureMaxBytes || delivery.PromptEchoHash == "" {
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
	if err := r.validateSavedFileEvidence(prior); err != nil {
		return err
	}
	if err := r.validateRecoveryBudgets(prior); err != nil {
		return err
	}
	if err := r.validateStageTimeout(prior); err != nil {
		return err
	}
	r.Opts.Vars = prior.Variables
	r.State = prior
	r.turn = prior.Turn
	return nil
}

func (r *Runner) validateRecoveryBudgets(state *workflow.WorkflowState) error {
	if state.Recovery == nil {
		if !state.Completed && r.template.ErrorHandling != nil {
			eh := r.template.ErrorHandling
			for _, action := range []workflow.ErrorAction{eh.OnAgentCrash, eh.OnAgentError, eh.OnTimeout} {
				if action == workflow.ErrorActionRetry || action == workflow.ErrorActionRestartAgent {
					return errors.New("workflow checkpoint lacks recovery budget evidence; inspect the run before a deliberate --restart")
				}
			}
		}
		return nil
	}
	if state.Recovery.Retries == nil || state.Recovery.Restarts == nil {
		return errors.New("workflow checkpoint has incomplete recovery budgets")
	}
	stages := make(map[string]bool)
	if flow := r.template.Flow; flow != nil {
		stages[flow.Initial] = true
		for _, stage := range flow.Stages {
			stages[stage] = true
		}
		for _, transition := range flow.Transitions {
			stages[transition.From], stages[transition.To] = true, true
		}
	}
	for stage, count := range state.Recovery.Retries {
		if strings.TrimSpace(stage) == "" || !stages[stage] || count < 0 {
			return fmt.Errorf("workflow checkpoint has invalid retry budget for stage %q", stage)
		}
	}
	for pane, count := range state.Recovery.Restarts {
		if _, known := state.Agents[pane]; !known || strings.TrimSpace(pane) == "" || count < 0 {
			return fmt.Errorf("workflow checkpoint has invalid restart budget for pane %q", pane)
		}
	}
	return nil
}

func (r *Runner) configureRecoveryBudgets() error {
	if r.State.Recovery == nil {
		r.State.Recovery = &workflow.RecoveryBudgets{
			Retries: make(map[string]int), Restarts: make(map[string]int),
		}
	}
	return r.errorHandler.ConfigureRecovery(*r.State.Recovery, func(budgets workflow.RecoveryBudgets) error {
		r.Mu.Lock()
		r.State.Recovery = &budgets
		err := r.saveCheckpointLocked()
		r.Mu.Unlock()
		if err != nil {
			r.stop("checkpoint-failed", err)
		}
		return err
	})
}

func (r *Runner) stageTimeout() time.Duration {
	if r.template.ErrorHandling == nil {
		return 0
	}
	return time.Duration(r.template.ErrorHandling.StageTimeoutMinutes) * time.Minute
}

func (r *Runner) timeoutAction() workflow.ErrorAction {
	if r.template.ErrorHandling != nil && r.template.ErrorHandling.OnTimeout != "" {
		return r.template.ErrorHandling.OnTimeout
	}
	return workflow.ErrorActionNotify
}

func (r *Runner) validateStageTimeout(state *workflow.WorkflowState) error {
	receipt := state.Timeout
	if receipt == nil {
		if r.stageTimeout() > 0 && !state.Completed && !r.stageIsTerminal(state.CurrentStage) && state.Dispatches != nil {
			return errors.New("workflow checkpoint lacks the original stage timeout deadline; inspect the run before a deliberate --restart")
		}
		return nil
	}
	if r.stageTimeout() <= 0 || state.Completed || r.stageIsTerminal(state.CurrentStage) ||
		receipt.Version != 1 || receipt.Stage != state.CurrentStage || !receipt.StartedAt.Equal(state.StageStartedAt) ||
		receipt.Round != state.Turn || receipt.Round <= 0 || len(state.Dispatches) == 0 ||
		receipt.Deadline.IsZero() || !receipt.Deadline.After(receipt.StartedAt) ||
		receipt.Action != r.timeoutAction() || (receipt.Handling && receipt.Handled) ||
		(!receipt.Handled && receipt.StopReason != "") {
		return errors.New("workflow checkpoint has invalid stage timeout evidence")
	}
	armedAt := receipt.Deadline.Add(-r.stageTimeout())
	if armedAt.Before(state.StageStartedAt) || armedAt.After(r.Ports.Now()) {
		return errors.New("workflow checkpoint has invalid stage timeout timing")
	}
	if receipt.Handled && !((receipt.Action == workflow.ErrorActionNotify && receipt.StopReason == "") ||
		(receipt.Action == workflow.ErrorActionPause && receipt.StopReason == "paused") || receipt.StopReason == "aborted") {
		return errors.New("workflow checkpoint has invalid timeout action receipt")
	}
	if receipt.Handling {
		return errors.New("workflow timeout action outcome is unknown; inspect the stage before a deliberate --restart; --resume will not repeat the action")
	}
	if receipt.StopReason == "aborted" {
		return errors.New("workflow timeout policy already aborted this run; use --restart only for a deliberate fresh run")
	}
	return nil
}

func (r *Runner) handleStageTimeout(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, nil
	}
	stage := r.Coordinator.CurrentStage()
	r.Mu.Lock()
	if r.State == nil || r.State.Completed || r.State.Timeout == nil {
		r.Mu.Unlock()
		return false, nil
	}
	receipt := r.State.Timeout
	if receipt.Handled || receipt.Handling || receipt.Stage != stage || receipt.Stage != r.State.CurrentStage ||
		!receipt.StartedAt.Equal(r.State.StageStartedAt) || receipt.Round != r.State.Turn || r.Ports.Now().Before(receipt.Deadline) {
		r.Mu.Unlock()
		return false, nil
	}
	receipt.Handling = true
	werr := &workflow.WorkflowError{Type: workflow.ErrorTimeout, Stage: stage, Message: "stage timeout", Timestamp: r.Ports.Now()}
	r.State.Errors = append(r.State.Errors, *werr)
	err := r.saveCheckpointLocked()
	r.Mu.Unlock()
	if err != nil {
		return true, err
	}
	if err := r.errorHandler.Handle(ctx, werr); err != nil {
		if reason, _ := r.Stopped(); reason == "" && ctx.Err() == nil {
			r.stop("error-handler-failed", err)
		}
		return true, nil
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State.Timeout != receipt {
		return true, nil
	}
	receipt.Handling, receipt.Handled, receipt.StopReason = false, true, r.stopReason
	if err := r.saveCheckpointLocked(); err != nil {
		return true, err
	}
	return true, nil
}

func (r *Runner) stageOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	r.Mu.Lock()
	var deadline time.Time
	if r.State != nil && r.State.Timeout != nil && !r.State.Timeout.Handled && !r.State.Timeout.Handling {
		deadline = r.State.Timeout.Deadline
	}
	r.Mu.Unlock()
	if deadline.IsZero() {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, deadline.Sub(r.Ports.Now()))
}

func (r *Runner) pollInterval() time.Duration {
	interval := r.Opts.Interval
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State != nil && r.State.Timeout != nil && !r.State.Timeout.Handled && !r.State.Timeout.Handling {
		if remaining := r.State.Timeout.Deadline.Sub(r.Ports.Now()); remaining < interval {
			if remaining < 0 {
				return 0
			}
			return remaining
		}
	}
	return interval
}

func (r *Runner) dispatchPendingStage(ctx context.Context, stage string) (string, error) {
	if r.stageIsTerminal(stage) {
		return "", nil
	}
	r.Mu.Lock()
	pending := !r.State.Completed && r.State.Dispatches == nil
	for _, delivery := range r.State.Dispatches {
		pending = pending || delivery.Status != "delivered"
	}
	r.Mu.Unlock()
	if !pending {
		return "", nil
	}
	targets, err := r.checkpointTargets(stage)
	if err != nil {
		return "stage-role-unresolved", err
	}
	if err := r.dispatchStage(ctx, stage, targets); err != nil {
		return "dispatch-failed", err
	}
	return "", nil
}

func (r *Runner) stageHasVerdict(stage string) bool {
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage && tr.Trigger.Type == workflow.TriggerAgentSays {
				return true
			}
		}
	}
	return false
}

func (r *Runner) stageHasFileTrigger(stage string) bool {
	if r.template.Flow != nil {
		for _, tr := range r.template.Flow.Transitions {
			if tr.From == stage && (tr.Trigger.Type == workflow.TriggerFileCreated || tr.Trigger.Type == workflow.TriggerFileModified) {
				return true
			}
		}
	}
	return false
}

func (r *Runner) resolveFileTriggerExclusions() error {
	if r.Store == nil || r.template.Flow == nil {
		return nil
	}
	usesFiles := false
	for _, transition := range r.template.Flow.Transitions {
		usesFiles = usesFiles || transition.Trigger.Type == workflow.TriggerFileCreated || transition.Trigger.Type == workflow.TriggerFileModified
	}
	if !usesFiles {
		return nil
	}
	root, err := filepath.EvalSymlinks(r.Opts.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve workflow file-trigger root: %w", err)
	}
	stateRoot, err := filepath.Abs(r.Store.Dir)
	if err != nil {
		return fmt.Errorf("resolve workflow state directory: %w", err)
	}
	stateRoot, err = filepath.EvalSymlinks(stateRoot)
	if err != nil {
		return fmt.Errorf("resolve workflow state directory: %w", err)
	}
	relative, err := filepath.Rel(root, stateRoot)
	if err != nil {
		return fmt.Errorf("locate workflow state directory: %w", err)
	}
	if relative == "." {
		return errors.New("file-triggered workflows require a state directory separate from the project root")
	}
	if filepath.IsLocal(relative) {
		r.fileTriggerExcludes = []string{filepath.ToSlash(relative)}
	}
	return nil
}

func (r *Runner) validateSavedFileEvidence(state *workflow.WorkflowState) error {
	if state.Files == nil {
		if !state.Completed && r.stageHasFileTrigger(state.CurrentStage) {
			if state.Dispatches == nil && state.Evidence != nil {
				return errors.New("workflow checkpoint has filesystem work evidence without an unattempted delivery plan")
			}
			for _, delivery := range state.Dispatches {
				if delivery.Status != "pending" {
					return errors.New("workflow checkpoint lacks the filesystem boundary for an already-dispatched file-triggered stage; inspect the stage before a deliberate --restart")
				}
			}
		}
		return nil
	}
	evidence := state.Files
	if evidence.Version != EvidenceVersion || evidence.Stage != state.CurrentStage ||
		!evidence.StartedAt.Equal(state.StageStartedAt) || evidence.Round <= 0 || evidence.Round != state.Turn ||
		!r.stageHasFileTrigger(state.CurrentStage) || evidence.Triggers == nil {
		return errors.New("workflow checkpoint has invalid stage filesystem evidence")
	}
	return workflow.ValidateFileTriggerStates(r.template, r.Opts.ProjectRoot, state.CurrentStage, evidence.Triggers, r.fileTriggerExcludes)
}

func (r *Runner) snapshotFileTriggers(stage string) (map[int]workflow.FileTriggerState, error) {
	if !r.stageHasFileTrigger(stage) {
		return nil, nil
	}
	observer, ok := r.Coordinator.(interface {
		FileTriggerStates() (map[int]workflow.FileTriggerState, error)
	})
	if !ok {
		return nil, errors.New("workflow coordinator cannot checkpoint filesystem triggers")
	}
	return observer.FileTriggerStates()
}

func workflowFileEvidenceChanged(prior *workflow.StageFileEvidence, current map[int]workflow.FileTriggerState) bool {
	if prior == nil || len(prior.Triggers) != len(current) {
		return true
	}
	for index, state := range current {
		old, ok := prior.Triggers[index]
		if !ok || old.Fired != state.Fired {
			return true
		}
	}
	return false
}

func (r *Runner) stopAndCheckpointFileTriggers() error {
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	stage := r.Coordinator.CurrentStage()
	observer, ok := r.Coordinator.(interface {
		StopWithFileTriggerStates() (map[int]workflow.FileTriggerState, error)
	})
	if !ok {
		return r.Coordinator.Stop()
	}
	files, err := observer.StopWithFileTriggerStates()
	if err != nil || files == nil {
		return err
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State == nil || r.State.Completed || r.State.CurrentStage != stage || r.State.Evidence == nil {
		return nil
	}
	if workflowFileEvidenceChanged(r.State.Files, files) {
		r.State.Files = &workflow.StageFileEvidence{
			Version: EvidenceVersion, Stage: stage, StartedAt: r.State.StageStartedAt,
			Round: r.State.Turn, Triggers: files,
		}
		return r.saveCheckpointLocked()
	}
	return nil
}

func (r *Runner) validateSavedEvidence(state *workflow.WorkflowState) error {
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
	if e.Version != EvidenceVersion || e.Stage != state.CurrentStage || !e.StartedAt.Equal(state.StageStartedAt) || e.Round <= 0 || e.Round != state.Turn || len(e.Panes) != len(r.agents) {
		return errors.New("workflow checkpoint has invalid stage output evidence")
	}
	for _, agent := range r.agents {
		pane, ok := e.Panes[agent.ID]
		if !ok || pane.PID != state.PanePIDs[agent.ID] || len(pane.Capture) > CaptureMaxBytes || len(pane.Fresh) > EvidenceMaxBytes || pane.LastActivity.IsZero() || pane.LastActivity.After(r.Ports.Now()) {
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

func (r *Runner) templateHash() (string, error) {
	return r.template.Fingerprint()
}

func (r *Runner) completeCheckpoint() error {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State == nil || r.State.Completed {
		return nil
	}
	r.State.Completed = true
	r.State.ManualGate = nil
	if r.Store != nil {
		return r.Store.RecordStage(r.State, "completed", "terminal", r.Ports.Now())
	}
	return nil
}

func (r *Runner) validatePanes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.validateEndpoint(); err != nil {
		return err
	}
	if r.Ports.Validate != nil {
		if err := r.Ports.Validate(ctx); err != nil {
			return fmt.Errorf("workflow pane identity changed; no further dispatch or transition: %w", err)
		}
	}
	return r.validateEndpoint()
}

func (r *Runner) validateEndpoint() error {
	if r.Ports.Endpoint != nil && r.Ports.Endpoint() != r.Opts.TmuxEndpoint {
		return fmt.Errorf("workflow tmux endpoint changed from %q; no further observation, dispatch, or transition", r.Opts.TmuxEndpoint)
	}
	return nil
}

func (r *Runner) captureStageBoundary(ctx context.Context) (map[string]workflow.StagePaneEvidence, error) {
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

func (r *Runner) workflowTranscriptType(pane string) string {
	if strings.EqualFold(strings.TrimSpace(r.Opts.PaneTypes[pane]), "pi") {
		return "pi"
	}
	return ""
}

func (r *Runner) captureWorkflowPane(ctx context.Context, pane string) (workflow.StagePaneEvidence, error) {
	if err := r.validatePanes(ctx); err != nil {
		return workflow.StagePaneEvidence{}, err
	}
	text, err := r.Ports.Capture(ctx, pane, CaptureLines)
	if err != nil {
		return workflow.StagePaneEvidence{}, fmt.Errorf("capture workflow pane %s before trusting its output: %w", pane, err)
	}
	if len(text) > CaptureMaxBytes {
		return workflow.StagePaneEvidence{}, fmt.Errorf("workflow pane %s capture exceeds %d bytes; output evidence is unavailable", pane, CaptureMaxBytes)
	}
	if err := r.validatePanes(ctx); err != nil {
		return workflow.StagePaneEvidence{}, err
	}
	captured := workflow.StagePaneEvidence{
		PID: r.Opts.PanePIDs[pane], Capture: strings.TrimRight(text, "\n"), LastActivity: r.Ports.Now(),
	}
	if r.workflowTranscriptType(pane) == "pi" {
		captured.AgentType = "pi"
		transcript, complete := status.AgentResponseTranscript(text, "pi")
		captured.Incomplete = !complete
		captured.Capture = strings.TrimRight(transcript, "\n")
		captured.ScreenHash = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		captured.Working = agentpkg.PluginActivelyWorking(status.StripANSI(text), agentpkg.AgentType("pi"), 0)
	}
	return captured, nil
}

func (r *Runner) refreshDeliveryBoundary(ctx context.Context, pane string) error {
	captured, err := r.captureWorkflowPane(ctx, pane)
	if err != nil {
		return err
	}
	if captured.Incomplete {
		return fmt.Errorf("workflow pi pane %s has an incomplete or unsupported composer; no prompt sent", pane)
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State == nil || r.State.Evidence == nil {
		return errors.New("workflow delivery has no stage output boundary")
	}
	found := false
	for _, delivery := range r.State.Dispatches {
		if delivery.Pane == pane && (delivery.Status == "pending" || delivery.Status == "sending") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("workflow pane %s is not awaiting this stage's delivery", pane)
	}
	restartPaneEvidence(r.State.Evidence, pane, captured)
	return r.saveCheckpointLocked()
}

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
// overlap after scrolling.
func workflowNewEvidence(before, after string) (string, error) {
	if strings.HasPrefix(after, before) {
		return after[len(before):], nil
	}
	for i := 0; i < len(before); i++ {
		if before[i] != '\n' || i+1 >= len(before) {
			continue
		}
		suffix := before[i+1:]
		if strings.TrimSpace(suffix) != "" && strings.HasPrefix(after, suffix) {
			return after[len(suffix):], nil
		}
	}
	return "", errors.New("the saved output boundary is no longer visible (scrollback loss or screen redraw); inspect the stage before a deliberate --restart")
}

func (r *Runner) recoverPiWorkflowEvidence(agent workflow.CoordinatorAgent, prior workflow.StagePaneEvidence, after string) (string, bool) {
	for _, delivery := range r.State.Dispatches {
		if delivery.Pane != agent.ID || delivery.Role != agent.Role || delivery.Status != "delivered" {
			continue
		}
		if delivery.PiPromptEchoHash == "" || delivery.PiPromptEchoLength <= 0 {
			return "", false
		}
		rendered := status.PromptEchoFingerprint{Hash: delivery.PiPromptEchoHash, Length: delivery.PiPromptEchoLength}
		before := prior.Capture
		if strings.TrimSpace(prior.Fresh) != "" {
			if !status.MatchesAgentEcho(prior.Fresh, delivery.PromptEchoHash, delivery.PromptEchoLength, rendered) || !strings.HasSuffix(before, prior.Fresh) {
				return "", false
			}
			before = strings.TrimSuffix(before, prior.Fresh)
		}
		return status.RecoverPiRepaint(before, after, delivery.PromptEchoHash, delivery.PromptEchoLength, rendered)
	}
	return "", false
}

func (r *Runner) validateVerdictPrompt(stage, role, prompt string) error {
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

// PiVerdict matches a Pi agent's output against pattern strictly after the
// current prompt's unique visible echo.
func PiVerdict(pattern, output string, delivery workflow.StageDispatch) (bool, error) {
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
	for _, match := range compiled.FindAllStringIndex(output, 4096) {
		if match[0] >= end {
			return true, nil
		}
	}
	return false, nil
}

// Observe persists fresh stage evidence before the coordinator may act on it.
func (r *Runner) Observe(ctx context.Context) (*Observation, error) {
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	coordinatorStage := r.Coordinator.CurrentStage()
	files, err := r.snapshotFileTriggers(coordinatorStage)
	if err != nil {
		return nil, err
	}
	current, err := r.captureStageBoundary(ctx)
	if err != nil {
		return nil, err
	}
	r.Mu.Lock()
	if r.State == nil || r.State.Evidence == nil {
		r.Mu.Unlock()
		return nil, errors.New("workflow stage is missing its durable output boundary")
	}
	evidence := *r.State.Evidence
	evidence.Panes = make(map[string]workflow.StagePaneEvidence, len(current))
	evidence.Matches = make(map[int][]string, len(r.State.Evidence.Matches))
	for index, panes := range r.State.Evidence.Matches {
		evidence.Matches[index] = append([]string(nil), panes...)
	}
	tctx := &workflow.TriggerContext{
		Context: ctx, ProjectRoot: r.Opts.ProjectRoot, Session: r.Opts.Session,
		Now: r.Ports.Now, TransitionEvidence: make(map[int]bool), FileTriggerStates: files,
		FileTriggerExcludes: r.fileTriggerExcludes,
	}
	changed := false
	for _, agent := range r.agents {
		prior, ok := r.State.Evidence.Panes[agent.ID]
		frame := current[agent.ID]
		if !ok || prior.PID != frame.PID || prior.AgentType != frame.AgentType {
			r.Mu.Unlock()
			return nil, fmt.Errorf("workflow pane %s lost its stage identity", agent.ID)
		}
		isPi := frame.AgentType == "pi"
		if isPi && (frame.Incomplete || (prior.Capture != "" && (strings.TrimSpace(frame.Capture) == "" || (len(frame.Capture) < len(prior.Capture) && strings.HasPrefix(prior.Capture, frame.Capture))))) {
			prior.LastActivity = r.Ports.Now()
			prior.ScreenHash = frame.ScreenHash
			evidence.Panes[agent.ID] = prior
			tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
			changed = true
			continue
		}
		delta := ""
		if r.stageHasVerdict(r.State.CurrentStage) {
			var err error
			delta, err = workflowNewEvidence(prior.Capture, frame.Capture)
			if isPi && (err != nil || prior.Capture == "") {
				if recovered, trusted := r.recoverPiWorkflowEvidence(agent, prior, frame.Capture); trusted {
					delta, err, prior.Fresh = recovered, nil, ""
				} else if prior.Capture == "" {
					prior.LastActivity = r.Ports.Now()
					prior.ScreenHash = frame.ScreenHash
					evidence.Panes[agent.ID] = prior
					tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
					changed = true
					continue
				}
			}
			if err != nil {
				r.Mu.Unlock()
				return nil, fmt.Errorf("workflow pane %s: %w", agent.ID, err)
			}
		}
		if prior.Capture != frame.Capture || prior.ScreenHash != frame.ScreenHash || frame.Working {
			prior.LastActivity = r.Ports.Now()
			changed = true
		}
		prior.Capture, prior.ScreenHash = frame.Capture, frame.ScreenHash
		prior.Fresh += delta
		evidence.Panes[agent.ID] = prior
		tctx.Activities = append(tctx.Activities, workflow.AgentActivity{Role: agent.Role, LastActivity: prior.LastActivity})
	}
	stage := r.State.CurrentStage
	dispatches := append([]workflow.StageDispatch(nil), r.State.Dispatches...)
	r.Mu.Unlock()

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
								fired, err = PiVerdict(tr.Trigger.Pattern, text, delivery)
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
			if gate, ok := r.Coordinator.(*workflow.ReviewGateCoordinator); ok && r.template.Flow.RequireApproval {
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
			value.Fresh = ""
		} else if len(value.Fresh) > EvidenceMaxBytes {
			return nil, fmt.Errorf("workflow pane %s response evidence exceeds %d bytes before all verdicts were observed; inspect the stage before a deliberate --restart", pane, EvidenceMaxBytes)
		}
		evidence.Panes[pane] = value
	}
	if err := r.validatePanes(ctx); err != nil {
		return nil, err
	}
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State.CurrentStage != stage || r.State.Evidence.Round != evidence.Round {
		return nil, errors.New("workflow stage changed while collecting output evidence")
	}
	if files != nil && workflowFileEvidenceChanged(r.State.Files, files) {
		r.State.Files = &workflow.StageFileEvidence{
			Version: EvidenceVersion, Stage: stage, StartedAt: r.State.StageStartedAt,
			Round: r.State.Turn, Triggers: files,
		}
		changed = true
	}
	if changed {
		r.State.Evidence = &evidence
		if err := r.saveCheckpointLocked(); err != nil {
			return nil, err
		}
	}
	return &Observation{Context: tctx, Stage: stage, Round: evidence.Round}, nil
}

// AdvanceObservation evaluates a previously captured observation under the
// runner's dispatch lock so a retry cannot adopt stale evidence.
func (r *Runner) AdvanceObservation(ctx context.Context, observed *Observation) (bool, string, bool, error) {
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	r.Mu.Lock()
	current := observed != nil && r.State != nil && r.State.Evidence != nil &&
		r.State.CurrentStage == observed.Stage && r.State.Evidence.Round == observed.Round
	r.Mu.Unlock()
	if !current {
		return false, "", true, nil
	}
	if err := r.validatePanes(ctx); err != nil {
		return false, "", false, err
	}
	newStage := r.Coordinator.CurrentStage()
	fired := newStage != observed.Stage
	if !fired {
		var err error
		fired, err = r.evaluate(observed.Context)
		if err != nil {
			return false, "", false, err
		}
		newStage = r.Coordinator.CurrentStage()
	}
	if fired || newStage != observed.Stage {
		if err := r.RecordTransitionLocked(newStage, nil); err != nil {
			return false, newStage, false, err
		}
	}
	return fired, newStage, false, nil
}

// AdvanceManualDecision consumes one recorded operator choice for the exact
// current manual gate.
func (r *Runner) AdvanceManualDecision(ctx context.Context) (bool, string, error) {
	if r.Store == nil {
		return false, "", nil
	}
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	stage := r.Coordinator.CurrentStage()
	r.Mu.Lock()
	if r.State == nil || r.State.CurrentStage != stage || r.State.ManualGate == nil {
		r.Mu.Unlock()
		return false, stage, nil
	}
	decision, err := r.Store.ReadManualDecision(r.State)
	r.Mu.Unlock()
	if err != nil || decision == nil {
		return false, stage, err
	}
	checkCtx, cancel := r.stageOperationContext(ctx)
	defer cancel()
	if err := r.validatePanes(checkCtx); err != nil {
		return false, stage, err
	}
	if err := checkCtx.Err(); err != nil {
		return false, stage, err
	}
	manual, ok := r.Coordinator.(interface {
		AdvanceManual(*workflow.TriggerContext, int) error
	})
	if !ok {
		return false, stage, errors.New("workflow coordinator does not support indexed manual transitions")
	}
	tctx := &workflow.TriggerContext{
		Context: ctx, ProjectRoot: r.Opts.ProjectRoot, Session: r.Opts.Session,
		CheckContext: checkCtx,
		CurrentRole:  r.StageRole(stage), Now: r.Ports.Now,
		FileTriggerExcludes: r.fileTriggerExcludes,
	}
	if err := manual.AdvanceManual(tctx, decision.TransitionIndex); err != nil {
		return false, stage, err
	}
	next := r.Coordinator.CurrentStage()
	if err := r.RecordTransitionLocked(next, decision); err != nil {
		return false, next, err
	}
	return true, next, nil
}

func containsWorkflowPane(panes []string, pane string) bool {
	for _, candidate := range panes {
		if candidate == pane {
			return true
		}
	}
	return false
}

func (r *Runner) fireManualTriggers() {
	r.Mu.Lock()
	manuals := append([]*workflow.ManualTrigger(nil), r.manuals...)
	r.Mu.Unlock()
	for _, manual := range manuals {
		if manual.Fire() {
			label := manual.Label()
			if label == "" {
				label = "manual"
			}
			r.Ports.Notify("workflow %s: fired manual trigger %q (--fire-manual)", r.template.Name, label)
		}
	}
}

// RunActions adapts the template's error_handling policy to a Runner.
type RunActions struct{ R *Runner }

// RestartAgent relaunches one faulted pane's agent through the configured
// restart transport and rebinds the runner to the new shell PID.
func (a RunActions) RestartAgent(ctx context.Context, pane string) error {
	r := a.R
	r.DispatchMu.Lock()
	defer r.DispatchMu.Unlock()
	failRestart := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
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
	if r.Ports.Restart == nil {
		return failRestart(fmt.Errorf("restart_agent: no agent restart transport for pane %s", pane))
	}
	r.Ports.Notify("workflow %s: restarting agent in pane %s (%s)", r.template.Name, pane, role)
	if err := r.validateEndpoint(); err != nil {
		return failRestart(err)
	}
	pid, err := r.Ports.Restart(ctx, r.Opts.Session, pane)
	if err != nil {
		return failRestart(fmt.Errorf("restart agent in pane %s (%s): %w", pane, role, err))
	}
	if err := r.validateEndpoint(); err != nil {
		return failRestart(err)
	}
	stage, redeliver, err := r.rebindRestartedPaneLocked(ctx, pane, pid)
	if err != nil {
		return failRestart(fmt.Errorf("rebind restarted pane %s (%s): %w", pane, role, err))
	}
	r.result.Restarts++
	r.Ports.Notify("workflow %s: agent in pane %s (%s) restarted (pid %d)", r.template.Name, pane, role, pid)
	if !redeliver {
		return nil
	}
	r.Ports.Notify("workflow %s: re-sending stage %s prompt to restarted pane %s (%s)", r.template.Name, StageLabel(stage), pane, role)
	if err := r.dispatchStageLocked(ctx, stage, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.stop("dispatch-failed", err)
		return err
	}
	return nil
}

func (r *Runner) rebindRestartedPaneLocked(ctx context.Context, pane string, pid int) (string, bool, error) {
	if pid <= 0 {
		return "", false, errors.New("restart reported no replacement pane process")
	}
	r.Opts.PanePIDs[pane] = pid
	captured, err := r.captureWorkflowPane(ctx, pane)
	if err != nil {
		return "", false, err
	}
	coordinatorStage := r.Coordinator.CurrentStage()
	r.Mu.Lock()
	defer r.Mu.Unlock()
	delete(r.supervision, pane)
	if r.State == nil {
		return "", false, nil
	}
	if r.State.PanePIDs == nil {
		r.State.PanePIDs = make(map[string]int)
	}
	r.State.PanePIDs[pane] = pid
	if r.State.Evidence != nil {
		restartPaneEvidence(r.State.Evidence, pane, captured)
	}
	redeliver := false
	if !r.State.Completed && r.State.CurrentStage == coordinatorStage {
		for i := range r.State.Dispatches {
			if r.State.Dispatches[i].Pane == pane && r.State.Dispatches[i].Status == "delivered" {
				r.State.Dispatches[i].Status = "pending"
				redeliver = true
			}
		}
	}
	return r.State.CurrentStage, redeliver, r.saveCheckpointLocked()
}

// Pause durably pauses the runner and stops its execution loop.
func (a RunActions) Pause(ctx context.Context, reason string) error {
	a.R.DispatchMu.Lock()
	defer a.R.DispatchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.R.validateEndpoint(); err != nil {
		return err
	}
	a.R.Mu.Lock()
	if a.R.State != nil && a.R.State.Completed {
		a.R.Mu.Unlock()
		return nil
	}
	var saveErr error
	if a.R.Store != nil && a.R.State != nil {
		saveErr = a.R.Store.Pause(a.R.State, reason, a.R.Ports.Now())
	}
	a.R.Mu.Unlock()
	if saveErr != nil {
		a.R.stop("checkpoint-failed", saveErr)
		return saveErr
	}
	a.R.stop("paused", fmt.Errorf("workflow paused: %s", reason))
	return nil
}

// SkipStage advances across the first outgoing edge from the current stage.
func (a RunActions) SkipStage(ctx context.Context) error {
	a.R.DispatchMu.Lock()
	defer a.R.DispatchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.R.validateEndpoint(); err != nil {
		return err
	}
	stage := a.R.Coordinator.CurrentStage()
	if a.R.template.Flow != nil {
		for _, tr := range a.R.template.Flow.Transitions {
			if tr.From == stage {
				trigger := string(tr.Trigger.Type)
				if tr.Trigger.Type == workflow.TriggerManual && tr.Trigger.Label != "" {
					trigger = tr.Trigger.Label
				}
				if err := a.R.Coordinator.Transition(trigger); err != nil {
					return err
				}
				if err := a.R.RecordTransitionLocked(a.R.Coordinator.CurrentStage(), nil); err != nil {
					a.R.stop("checkpoint-failed", err)
					return err
				}
				a.R.Mu.Lock()
				a.R.policyAdvanced = true
				a.R.Mu.Unlock()
				a.R.Ports.Notify("workflow %s: skipped stage %s via %s", a.R.template.Name, stage, trigger)
				return nil
			}
		}
	}
	a.R.Ports.Notify("workflow %s: skip_stage found no outgoing transition from %s", a.R.template.Name, stage)
	return nil
}

// Abort terminates the workflow with an abort reason.
func (a RunActions) Abort(_ context.Context, err error) error {
	a.R.stop("aborted", err)
	return nil
}

// RetryStage establishes a fresh prompt round for the current stage.
func (a RunActions) RetryStage(ctx context.Context) error {
	a.R.DispatchMu.Lock()
	defer a.R.DispatchMu.Unlock()
	if err := a.R.validateEndpoint(); err != nil {
		return err
	}
	stage := a.R.Coordinator.CurrentStage()
	a.R.Mu.Lock()
	if a.R.State == nil || a.R.State.CurrentStage != stage {
		a.R.Mu.Unlock()
		return errors.New("workflow stage changed before retry")
	}
	if len(a.R.State.Dispatches) == 0 || a.R.State.Evidence == nil {
		a.R.Mu.Unlock()
		return errors.New("cannot retry a stage before its initial dispatch is established")
	}
	for _, delivery := range a.R.State.Dispatches {
		if delivery.Status != "delivered" {
			a.R.Mu.Unlock()
			return errors.New("cannot retry a stage with unfinished or uncertain delivery")
		}
	}
	a.R.State.Dispatches = nil
	a.R.State.Evidence = nil
	a.R.State.Files = nil
	a.R.State.ManualGate = nil
	a.R.State.Timeout = nil
	startedAt := a.R.State.StageStartedAt
	err := a.R.saveCheckpointLocked()
	a.R.Mu.Unlock()
	if err != nil {
		a.R.stop("checkpoint-failed", err)
		return err
	}
	if restarter, ok := a.R.Coordinator.(interface {
		RestartStage(*workflow.TriggerContext, time.Time) error
	}); ok {
		if err := restarter.RestartStage(&workflow.TriggerContext{
			Context: ctx, ProjectRoot: a.R.Opts.ProjectRoot, Session: a.R.Opts.Session, Now: a.R.Ports.Now,
			FileTriggerExcludes: a.R.fileTriggerExcludes,
		}, startedAt); err != nil {
			a.R.stop("trigger-failed", err)
			return err
		}
	} else {
		return errors.New("workflow coordinator cannot reset triggers for a retry")
	}
	targets, err := a.R.stagePanes(stage)
	if err != nil {
		return err
	}
	a.R.Ports.Notify("workflow %s: retrying stage %s", a.R.template.Name, stage)
	err = a.R.dispatchStageLocked(ctx, stage, targets)
	if err != nil {
		a.R.stop("dispatch-failed", err)
	}
	return err
}

// Notify surfaces a policy error notification.
func (a RunActions) Notify(_ context.Context, werr *workflow.WorkflowError, subject string) error {
	a.R.Ports.Notify("workflow %s: %s: %v", a.R.template.Name, subject, werr)
	return nil
}

func (r *Runner) agentFaultAction(kind workflow.ErrorType) workflow.ErrorAction {
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

func (r *Runner) superviseAgents(ctx context.Context) error {
	if r.Ports.Faults == nil || r.errorHandler == nil {
		return nil
	}
	if err := r.validateEndpoint(); err != nil {
		return err
	}
	r.DispatchMu.Lock()
	expected := make(map[string]int, len(r.agents))
	for _, agent := range r.agents {
		expected[agent.ID] = r.Opts.PanePIDs[agent.ID]
	}
	r.DispatchMu.Unlock()
	checkCtx, cancel := r.stageOperationContext(ctx)
	faults, err := r.Ports.Faults(checkCtx, expected)
	checkErr := checkCtx.Err()
	cancel()
	if checkErr != nil {
		return nil
	}
	if endpointErr := r.validateEndpoint(); endpointErr != nil {
		return endpointErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		r.Mu.Lock()
		first := !r.healthDown
		r.healthDown = true
		r.Mu.Unlock()
		if first {
			r.Ports.Notify("workflow %s: agent health unavailable; crash/error detection resumes when it recovers: %v", r.template.Name, err)
		}
		return nil
	}
	stage := r.Coordinator.CurrentStage()
	now := r.Ports.Now()
	var raised []*workflow.WorkflowError
	r.Mu.Lock()
	round := 0
	if r.State != nil {
		round = r.State.Turn
	}
	r.healthDown = false
	if r.supervision == nil {
		r.supervision = make(map[string]*faultEpisode)
	}
	for _, agent := range r.agents {
		fault, faulted := faults[agent.ID]
		if !faulted {
			delete(r.supervision, agent.ID)
			continue
		}
		episode := r.supervision[agent.ID]
		if episode == nil || episode.kind != fault.Kind {
			episode = &faultEpisode{kind: fault.Kind}
			r.supervision[agent.ID] = episode
		}
		episode.polls++
		threshold := r.Opts.AgentFaultPolls
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
		r.result.Faults = append(r.result.Faults, RunFault{
			Pane: agent.ID, Role: agent.Role, Stage: StageLabel(stage), Type: string(fault.Kind),
			Reason: fault.Reason, Action: string(r.agentFaultAction(fault.Kind)),
		})
		if r.State != nil {
			r.State.Errors = append(r.State.Errors, *werr)
		}
	}
	var saveErr error
	if len(raised) > 0 && r.State != nil {
		saveErr = r.saveCheckpointLocked()
	}
	r.Mu.Unlock()
	if saveErr != nil {
		r.stop("checkpoint-failed", saveErr)
		return nil
	}
	for i, werr := range raised {
		if reason, _ := r.Stopped(); reason != "" || ctx.Err() != nil {
			return nil
		}
		r.Ports.Notify("workflow %s: %s at stage %s: %s (on_%s = %s)", r.template.Name, werr.Type, StageLabel(werr.Stage), werr.Message, werr.Type, r.agentFaultAction(werr.Type))
		if err := r.errorHandler.Handle(ctx, werr); err != nil {
			return err
		}
		r.Mu.Lock()
		advanced := r.policyAdvanced || (r.State != nil && (r.State.CurrentStage != stage || r.State.Turn != round))
		if advanced {
			for _, pending := range raised[i+1:] {
				if episode := r.supervision[pending.AgentID]; episode != nil && episode.kind == pending.Type {
					episode.raised = false
				}
			}
		}
		r.Mu.Unlock()
		if advanced {
			return nil
		}
	}
	return nil
}

func (r *Runner) superviseOrStop(ctx context.Context) (string, error) {
	err := r.superviseAgents(ctx)
	if reason, stopErr := r.Stopped(); reason != "" {
		return reason, stopErr
	}
	if err != nil && ctx.Err() == nil {
		return "error-handler-failed", err
	}
	return "", nil
}

func (r *Runner) stop(reason string, err error) {
	r.Mu.Lock()
	if r.stopReason == "" {
		r.stopReason = reason
		r.stopErr = err
	}
	cancel := r.cancel
	r.Mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stopped returns the recorded stop reason and error, if any.
func (r *Runner) Stopped() (string, error) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	return r.stopReason, r.stopErr
}

// RecordTransitionLocked persists the stage change to the state store.
// The caller must hold r.DispatchMu.
func (r *Runner) RecordTransitionLocked(newStage string, decision *workflow.ManualDecision) error {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.State == nil {
		return nil
	}
	now := r.Ports.Now()
	trigger := "trigger"
	if decision != nil {
		trigger = "manual"
	}
	r.State.StageHistory = append(r.State.StageHistory, workflow.StageRecord{
		Stage: r.State.CurrentStage, StartedAt: r.State.StageStartedAt,
		CompletedAt: now, DurationSec: int(now.Sub(r.State.StageStartedAt).Seconds()),
		Result: "advanced", Trigger: trigger, ManualDecision: decision,
	})
	r.State.CurrentStage = newStage
	r.State.StageStartedAt = now
	r.State.Dispatches = nil
	r.State.Evidence = nil
	r.State.Files = nil
	r.State.ManualGate = nil
	r.State.Timeout = nil
	r.State.Completed = r.stageIsTerminal(newStage)
	return r.saveCheckpointLocked()
}

// Run drives the workflow until completion, the transition budget, a
// configured pause/abort, or context expiry.
func (r *Runner) Run(ctx context.Context) (out Result, runErr error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.Mu.Lock()
	r.cancel = cancel
	r.Mu.Unlock()

	result := &r.result
	result.Workflow = r.template.Name
	result.Source = r.template.Source
	result.Session = r.Opts.Session
	result.Coordination = string(r.template.Coordination)
	for _, agent := range r.agents {
		result.Agents = append(result.Agents, RunAgent{Role: agent.Role, Pane: agent.ID})
	}

	fail := func(reason string, err error) (Result, error) {
		result.Success = false
		result.Reason = reason
		if err != nil {
			result.Error = err.Error()
		}
		return *result, err
	}
	if err := r.validateEndpoint(); err != nil {
		return fail("endpoint-changed", err)
	}
	if r.Store != nil {
		lockCtx, lockCancel := context.WithTimeout(runCtx, time.Second)
		unlock, err := r.Store.Acquire(lockCtx, r.Opts.Session)
		lockCancel()
		if err != nil {
			return fail("checkpoint-locked", err)
		}
		defer unlock()
	}
	if err := r.resolveFileTriggerExclusions(); err != nil {
		return fail("checkpoint-failed", err)
	}
	if err := r.loadCheckpoint(); err != nil {
		return fail("resume-rejected", err)
	}
	result.Resumed = r.Opts.Resume

	eh := r.template.ErrorHandling
	if eh == nil {
		eh = &workflow.ErrorConfig{}
	}
	r.errorHandler = workflow.NewErrorHandler(workflow.ErrorHandlingConfig{
		OnAgentCrash:        r.agentFaultAction(workflow.ErrorAgentCrash),
		OnAgentError:        r.agentFaultAction(workflow.ErrorAgentError),
		OnTriggerFailed:     workflow.ErrorActionAbort,
		OnTimeout:           r.timeoutAction(),
		MaxRetriesPerStage:  eh.MaxRetriesPerStage,
		MaxRestartsPerAgent: r.Opts.MaxAgentRestarts,
	}, RunActions{R: r})

	tctx := &workflow.TriggerContext{
		Context:             runCtx,
		ProjectRoot:         r.Opts.ProjectRoot,
		Session:             r.Opts.Session,
		Now:                 r.Ports.Now,
		FileTriggerExcludes: r.fileTriggerExcludes,
	}
	if r.State != nil {
		if r.State.Files != nil {
			tctx.FileTriggerStates = r.State.Files.Triggers
		}
		restorer, ok := r.Coordinator.(interface {
			StartAt(*workflow.TriggerContext, string, time.Time, map[string]int) error
		})
		if !ok {
			return fail("resume-rejected", errors.New("coordinator does not support checkpoint resume"))
		}
		if err := restorer.StartAt(tctx, r.State.CurrentStage, r.State.StageStartedAt, r.State.NextByRole); err != nil {
			return fail("resume-rejected", err)
		}
	} else if err := r.Coordinator.Start(tctx); err != nil {
		return fail("start-failed", err)
	}
	defer func() {
		cancel()
		if err := r.stopAndCheckpointFileTriggers(); err != nil {
			if runErr == nil {
				out.Reason = "checkpoint-failed"
			}
			runErr = errors.Join(runErr, fmt.Errorf("retain workflow filesystem evidence: %w", err))
			out.Success = false
			out.Error = runErr.Error()
		}
	}()

	stage := r.Coordinator.CurrentStage()
	result.Stages = append(result.Stages, StageLabel(stage))
	if r.State != nil && r.State.Completed {
		result.Success, result.Completed, result.Reason = true, true, "completed"
		return *result, nil
	}

	if r.State == nil {
		paneRoles := make(map[string]string, len(r.agents))
		panePIDs := make(map[string]int, len(r.agents))
		for _, agent := range r.agents {
			paneRoles[agent.ID] = agent.Role
			panePIDs[agent.ID] = r.Opts.PanePIDs[agent.ID]
		}
		hash, err := r.templateHash()
		if err != nil {
			return fail("checkpoint-failed", err)
		}
		state := &workflow.WorkflowState{
			WorkflowName:   r.template.Name,
			SessionName:    r.Opts.Session,
			CurrentStage:   stage,
			StageStartedAt: r.Ports.Now(),
			Agents:         paneRoles,
			Variables:      r.Opts.Vars,
			ResumeVersion:  1,
			TemplateHash:   hash,
			ProjectRoot:    r.Opts.ProjectRoot,
			TmuxEndpoint:   r.Opts.TmuxEndpoint,
			PanePIDs:       panePIDs,
			Recovery: &workflow.RecoveryBudgets{
				Retries: make(map[string]int), Restarts: make(map[string]int),
			},
		}
		r.Mu.Lock()
		r.State = state
		err = r.saveCheckpointLocked()
		r.Mu.Unlock()
		if err != nil {
			return fail("checkpoint-failed", err)
		}
	} else if r.State.Paused {
		if err := r.Store.Resume(r.State); err != nil {
			return fail("checkpoint-failed", err)
		}
	}
	if err := r.configureRecoveryBudgets(); err != nil {
		return fail("resume-rejected", fmt.Errorf("restore workflow recovery budgets: %w", err))
	}

	if parallel, ok := r.Coordinator.(*workflow.ParallelCoordinator); ok && r.template.Flow == nil {
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

	for {
		if reason, stopErr := r.Stopped(); reason != "" {
			return fail(reason, stopErr)
		}
		if runCtx.Err() != nil {
			if reason, stopErr := r.Stopped(); reason != "" {
				return fail(reason, stopErr)
			}
			reason := "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "timeout"
			}
			return fail(reason, fmt.Errorf("workflow run canceled: %w", context.Cause(ctx)))
		}
		r.Mu.Lock()
		policyAdvanced := r.policyAdvanced
		r.policyAdvanced = false
		r.Mu.Unlock()
		if policyAdvanced {
			newStage := r.Coordinator.CurrentStage()
			result.Transitions++
			r.Ports.Notify("workflow %s: stage %s → %s (transition %d)", r.template.Name, StageLabel(stage), StageLabel(newStage), result.Transitions)
			stage = newStage
			result.Stages = append(result.Stages, StageLabel(stage))
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
		if handled, err := r.handleStageTimeout(runCtx); err != nil {
			return fail("checkpoint-failed", err)
		} else if handled {
			continue
		}
		if reason, err := r.dispatchPendingStage(runCtx, stage); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && runCtx.Err() == nil {
				r.Mu.Lock()
				knownDelivery := r.State != nil
				if r.State != nil {
					for _, delivery := range r.State.Dispatches {
						knownDelivery = knownDelivery && (delivery.Status == "pending" || delivery.Status == "delivered")
					}
				}
				r.Mu.Unlock()
				if knownDelivery {
					if handled, timeoutErr := r.handleStageTimeout(runCtx); timeoutErr != nil {
						return fail("checkpoint-failed", timeoutErr)
					} else if handled {
						continue
					}
				}
			}
			return fail(reason, err)
		}
		if result.Transitions >= r.Opts.MaxTransitions {
			result.Success = true
			result.Reason = "max-transitions"
			return *result, nil
		}

		r.Ports.Sleep(runCtx, r.pollInterval())
		if runCtx.Err() != nil {
			continue
		}
		if handled, err := r.handleStageTimeout(runCtx); err != nil {
			return fail("checkpoint-failed", err)
		} else if handled {
			continue
		}

		if r.Opts.FireManual {
			r.fireManualTriggers()
		}

		if reason, err := r.superviseOrStop(runCtx); reason != "" {
			return fail(reason, err)
		}
		if runCtx.Err() != nil {
			continue
		}
		r.Mu.Lock()
		policyAdvanced = r.policyAdvanced
		r.Mu.Unlock()
		if policyAdvanced {
			continue
		}
		if handled, err := r.handleStageTimeout(runCtx); err != nil {
			return fail("checkpoint-failed", err)
		} else if handled {
			continue
		}

		fired, newStage, err := r.AdvanceManualDecision(runCtx)
		if err == nil && !fired {
			checkCtx, cancelCheck := r.stageOperationContext(runCtx)
			var observed *Observation
			observed, err = r.Observe(checkCtx)
			if err != nil {
				cancelCheck()
				if runCtx.Err() != nil {
					continue
				}
				if handled, timeoutErr := r.handleStageTimeout(runCtx); timeoutErr != nil {
					return fail("checkpoint-failed", timeoutErr)
				} else if handled {
					continue
				}
				r.Mu.Lock()
				if r.State != nil {
					now := r.Ports.Now()
					r.State.Paused, r.State.PausedAt, r.State.PauseReason = true, &now, "output evidence unavailable: "+err.Error()
					err = errors.Join(err, r.saveCheckpointLocked())
				}
				r.Mu.Unlock()
				return fail("observation-failed", err)
			}
			if handled, timeoutErr := r.handleStageTimeout(runCtx); timeoutErr != nil {
				cancelCheck()
				return fail("checkpoint-failed", timeoutErr)
			} else if handled {
				cancelCheck()
				continue
			}
			observed.Context.Context = runCtx
			observed.Context.CheckContext = checkCtx
			var stale bool
			fired, newStage, stale, err = r.AdvanceObservation(checkCtx, observed)
			cancelCheck()
			if stale {
				continue
			}
		}
		if err != nil {
			if runCtx.Err() != nil {
				continue
			}
			if handled, timeoutErr := r.handleStageTimeout(runCtx); timeoutErr != nil {
				return fail("checkpoint-failed", timeoutErr)
			} else if handled {
				continue
			}
			if r.template.ErrorHandling != nil {
				if handleErr := r.errorHandler.Handle(runCtx, &workflow.WorkflowError{
					Type: workflow.ErrorTriggerFailed, Stage: stage, Message: err.Error(), Timestamp: r.Ports.Now(),
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
		r.Ports.Notify("workflow %s: stage %s → %s (transition %d)", r.template.Name, StageLabel(stage), StageLabel(newStage), result.Transitions)
		stage = newStage
		result.Stages = append(result.Stages, StageLabel(stage))
	}
}

func (r *Runner) evaluate(tctx *workflow.TriggerContext) (bool, error) {
	evaluator, ok := r.Coordinator.(interface {
		Evaluate(*workflow.TriggerContext) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("coordinator for %q does not support trigger evaluation", r.template.Name)
	}
	return evaluator.Evaluate(tctx)
}

// =============================================================================
// Template resolution, variable validation, pane assignment, and production ports
// =============================================================================

func workflowNotFoundError(ref string) error {
	return fmt.Errorf("workflow %q not found: not a builtin (%s), no user (~/.config/ntm/workflows/) or project (.ntm/workflows/) template has that name, and it is not a path to a workflow TOML file",
		ref, strings.Join(workflow.BuiltinNames(), ", "))
}

func looksLikeWorkflowPath(ref string) bool {
	return strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".toml") || ref == "." || strings.HasPrefix(ref, "~")
}

// ResolveTemplateByName loads a named template from builtin, user, or the
// given project directory (never accepting arbitrary filesystem paths).
func ResolveTemplateByName(projectDir, name string) (*workflow.WorkflowTemplate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("workflow name is required")
	}
	loader := workflow.NewLoader()
	if strings.TrimSpace(projectDir) != "" {
		loader.ProjectDir = projectDir
	}
	tmpl, err := loader.Get(name)
	if err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			return nil, fmt.Errorf("%w: %s", workflow.ErrWorkflowNotFound, workflowNotFoundError(name).Error())
		}
		return nil, fmt.Errorf("loading workflow templates: %w", err)
	}
	return tmpl, nil
}

// ResolveTemplate loads a template by explicit TOML path or by name
// (builtin < user < project precedence via the loader).
func ResolveTemplate(projectDir, ref string) (*workflow.WorkflowTemplate, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("workflow name or path is required")
	}
	if looksLikeWorkflowPath(ref) {
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
	return ResolveTemplateByName(projectDir, ref)
}

// ResolveVars validates CLI --var assignments against the template's setup prompts.
func ResolveVars(template *workflow.WorkflowTemplate, assignments []string) (map[string]string, error) {
	vars := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		key, value, ok := strings.Cut(assignment, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --var %q: want key=value", assignment)
		}
		vars[key] = value
	}
	return ResolveVarsMap(template, vars, false)
}

// ResolveVarsMap validates a variable map against the template's setup prompts.
// When resume is true, prompt defaults and required-key checks are deferred to
// the runner's durable checkpoint validator.
func ResolveVarsMap(template *workflow.WorkflowTemplate, provided map[string]string, resume bool) (map[string]string, error) {
	vars := make(map[string]string, len(provided))
	for key, value := range provided {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return nil, errors.New("workflow variable key must not be empty")
		}
		vars[trimmed] = value
	}
	if resume || template == nil {
		return vars, nil
	}
	var missing []string
	for _, prompt := range template.Prompts {
		if val, ok := vars[prompt.Key]; ok && (!prompt.Required || strings.TrimSpace(val) != "") {
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

// AssignPanes maps template roles onto the session's agent panes in topology order.
func AssignPanes(template *workflow.WorkflowTemplate, panes []tmux.Pane) ([]workflow.CoordinatorAgent, error) {
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

// BindSessionPanes resolves the exact session's live panes, maps workflow
// roles onto them, and verifies every bound pane lifetime.
func BindSessionPanes(ctx context.Context, template *workflow.WorkflowTemplate, session string) ([]workflow.CoordinatorAgent, map[string]int, map[string]string, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, nil, nil, err
	}
	agents, err := AssignPanes(template, panes)
	if err != nil {
		return nil, nil, nil, err
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
	if err := ValidatePaneLifetimes(ctx, session, panePIDs); err != nil {
		return nil, nil, nil, err
	}
	return agents, panePIDs, paneTypes, nil
}

// ValidatePaneLifetimes observes the exact configured tmux server, including
// SSH mode. Remote PIDs are compared as identifiers only.
func ValidatePaneLifetimes(ctx context.Context, session string, expected map[string]int) error {
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

// AgentFaults is the production fault port classifying the run's panes with
// the observation behind `ntm health` and the [resilience] monitor.
func AgentFaults(ctx context.Context, session string, expected map[string]int) (map[string]AgentFault, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return nil, err
	}
	live := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		live[pane.ID] = pane
	}
	faults := make(map[string]AgentFault)
	for id := range expected {
		pane, ok := live[id]
		switch {
		case !ok:
			faults[id] = AgentFault{Kind: workflow.ErrorAgentCrash, Reason: "pane no longer exists", Confirmed: true}
		case pane.Dead:
			faults[id] = AgentFault{Kind: workflow.ErrorAgentCrash, Reason: "pane process exited (pane is dead)", Confirmed: true}
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
		if fault, faulted := ClassifyAgentHealth(row, local, tmux.PaneAgentAlive); faulted {
			faults[row.PaneID] = fault
		}
	}
	return faults, nil
}

// ClassifyAgentHealth maps one `ntm health` row onto workflow fault kinds.
func ClassifyAgentHealth(row health.AgentHealth, local bool, agentAlive func(int) bool) (AgentFault, bool) {
	if local && row.ShellPID > 0 && !agentAlive(row.ShellPID) {
		return AgentFault{Kind: workflow.ErrorAgentCrash, Reason: "agent process exited; the pane is back at its shell", Confirmed: true}, true
	}
	if row.ObservationFreshness != status.FreshnessFresh {
		return AgentFault{}, false
	}
	if row.RateLimited {
		return AgentFault{Kind: workflow.ErrorAgentError, Reason: "agent is rate limited"}, true
	}
	for _, issue := range row.Issues {
		switch issue.Type {
		case "auth_error", "trust_prompt", "interactive_gate":
			return AgentFault{Kind: workflow.ErrorAgentError, Reason: issue.Message}, true
		}
	}
	if row.ObservedState == status.StateError {
		reason := "agent is in an error state"
		if len(row.Issues) > 0 {
			reason += ": " + row.Issues[0].Message
		}
		return AgentFault{Kind: workflow.ErrorAgentError, Reason: reason}, true
	}
	return AgentFault{}, false
}

// RestartAgent is the production restart port using the shared restart engine.
func RestartAgent(ctx context.Context, cfg *config.Config, session, paneID string) (int, error) {
	out, err := robot.GetRestartPaneContext(ctx, robot.RestartPaneOptions{
		Session: session,
		Panes:   []string{paneID},
		Config:  cfg,
	})
	if err != nil {
		return 0, err
	}
	return RestartOutcome(out)
}

// RestartOutcome accepts only a verified relaunch with a new shell PID and
// ready agent CLI.
func RestartOutcome(out *robot.RestartPaneOutput) (int, error) {
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

// TmuxEndpoint returns the canonical configured tmux endpoint identifier.
func TmuxEndpoint() string {
	if tmux.DefaultClient.Remote == "" {
		return "local"
	}
	return "ssh:" + tmux.DefaultClient.Remote
}

// ValidateEndpoint rejects local-only file/command triggers on remote SSH hosts.
func ValidateEndpoint(template *workflow.WorkflowTemplate, endpoint string) error {
	if template == nil {
		return errors.New("workflow template is required")
	}
	if endpoint == "local" {
		return nil
	}
	if !strings.HasPrefix(endpoint, "ssh:") || strings.TrimSpace(strings.TrimPrefix(endpoint, "ssh:")) == "" {
		return fmt.Errorf("invalid workflow tmux endpoint %q", endpoint)
	}
	if template.Flow != nil {
		for _, transition := range template.Flow.Transitions {
			switch transition.Trigger.Type {
			case workflow.TriggerFileCreated, workflow.TriggerFileModified, workflow.TriggerCommandSuccess, workflow.TriggerCommandFailure:
				return fmt.Errorf("workflow %q uses %s at stage %q, which observes the local host and cannot drive SSH agents; run ntm workflow on the remote host instead", template.Name, transition.Trigger.Type, transition.From)
			}
		}
	}
	return nil
}

func redactorForConfig(cfg *config.Config) dispatchsvc.FinalMessageRedactor {
	redactCfg := redaction.Config{Mode: redaction.ModeOff}
	if cfg != nil {
		redactCfg = cfg.Redaction.ToRedactionLibConfig()
	}
	return dispatchsvc.FinalMessageRedactorFunc(func(_ context.Context, _ dispatchsvc.Target, message string) (dispatchsvc.RedactionResult, error) {
		result := redaction.ScanAndRedact(message, redactCfg)
		categories := make(map[string]int, len(result.Findings))
		for _, finding := range result.Findings {
			categories[string(finding.Category)]++
		}
		if len(categories) == 0 {
			categories = nil
		}
		return dispatchsvc.RedactionResult{
			Message:    result.Output,
			Mode:       string(result.Mode),
			Findings:   len(result.Findings),
			Categories: categories,
			Blocked:    result.Blocked,
		}, nil
	})
}

// DefaultGatedDispatch delivers a single workflow prompt through the canonical
// dispatch service (dead-pane gate, rotation quarantine, trust-dialog check,
// composer readiness, redaction, BeforeDispatch verification, and double-enter
// submission verification).
func DefaultGatedDispatch(
	cfg *config.Config,
	_ string,
	beforeDispatch func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error,
) func(ctx context.Context, session, paneID, prompt string) error {
	return func(ctx context.Context, session, paneID, prompt string) error {
		panes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			return err
		}
		service, err := dispatchsvc.NewService(dispatchsvc.Ports{
			Redactor:  redactorForConfig(cfg),
			Protocols: dispatchsvc.DefaultProtocolPlanner{},
			Deliverer: dispatchsvc.TMUXDeliverer{},
			Lifecycle: dispatchsvc.LifecycleHooks{
				BeforeDispatch: beforeDispatch,
			},
		})
		if err != nil {
			return err
		}
		res, err := service.Execute(ctx, dispatchsvc.Request{
			Session:               session,
			Panes:                 panes,
			Selectors:             []string{paneID},
			RequireSingleSelector: true,
			IncludeUser:           true,
			Message:               prompt,
			Submit:                true,
			StopOnFailure:         true,
		})
		if err != nil {
			return err
		}
		if res.Delivered != 1 || len(res.Receipts) != 1 || res.Receipts[0].Status != dispatchsvc.ReceiptDelivered {
			for _, receipt := range res.Receipts {
				if receipt.Error != "" {
					return errors.New(receipt.Error)
				}
			}
			return fmt.Errorf("delivery to pane %s failed", paneID)
		}
		return nil
	}
}

// ProductionConfig customizes the production workflow runner wiring for CLI or
// REST callers while sharing identical identity, boundary, and verdict guards.
type ProductionConfig struct {
	Config          *config.Config
	DispatchFactory func(workflowName string, beforeDispatch func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error) func(ctx context.Context, session, paneID, prompt string) error
	Notify          func(format string, args ...any)
}

// NewProductionRunner constructs a Runner bound to live tmux observation,
// health supervision, agent restart, and gated prompt dispatch.
func NewProductionRunner(
	template *workflow.WorkflowTemplate,
	agents []workflow.CoordinatorAgent,
	opts Options,
	prod ProductionConfig,
) (*Runner, error) {
	if template == nil {
		return nil, errors.New("workflow template is required")
	}
	session := opts.Session
	var runner *Runner
	validatePanes := func(ctx context.Context) error {
		if err := runner.validateEndpoint(); err != nil {
			return err
		}
		if err := ValidatePaneLifetimes(ctx, session, runner.Opts.PanePIDs); err != nil {
			return err
		}
		return runner.validateEndpoint()
	}
	beforeDispatch := func(ctx context.Context, request dispatchsvc.Request, deliveries []dispatchsvc.Delivery) error {
		if request.Session != session || len(deliveries) != 1 {
			return errors.New("workflow dispatch changed its session or single-pane target")
		}
		if err := validatePanes(ctx); err != nil {
			return err
		}
		stage := runner.Coordinator.CurrentStage()
		runner.Mu.Lock()
		matchesStage := runner.State != nil && runner.State.CurrentStage == stage && runner.State.Evidence != nil
		runnerAgents := append([]workflow.CoordinatorAgent(nil), runner.agents...)
		runner.Mu.Unlock()
		if !matchesStage {
			return errors.New("workflow stage changed before the prepared prompt could be delivered")
		}
		for _, delivery := range deliveries {
			pane := delivery.Target.Pane
			if pid, ok := runner.Opts.PanePIDs[pane.ID]; !ok || pid <= 0 || pane.PID != pid {
				return fmt.Errorf("prepared workflow delivery changed pane %s lifetime", pane.ID)
			}
			role := ""
			for _, agent := range runnerAgents {
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
			if err := runner.PinPiDispatchPrompt(stage, pane.ID, delivery.Message); err != nil {
				return err
			}
		}
		return nil
	}

	dispatchPort := DefaultGatedDispatch(prod.Config, template.Name, beforeDispatch)
	if prod.DispatchFactory != nil {
		dispatchPort = prod.DispatchFactory(template.Name, beforeDispatch)
	}

	var err error
	runner, err = NewRunner(template, agents, opts, Ports{
		Dispatch: dispatchPort,
		Capture:  tmux.CapturePaneOutputContext,
		Validate: validatePanes,
		Endpoint: TmuxEndpoint,
		Faults: func(ctx context.Context, expected map[string]int) (map[string]AgentFault, error) {
			return AgentFaults(ctx, session, expected)
		},
		Restart: func(ctx context.Context, sess, paneID string) (int, error) {
			return RestartAgent(ctx, prod.Config, sess, paneID)
		},
		Notify: prod.Notify,
	})
	if err != nil {
		return nil, err
	}
	return runner, nil
}

// CheckpointResult exposes durable execution evidence without printing setup
// variables or prompt contents.
type CheckpointResult struct {
	Success         bool                       `json:"success"`
	Workflow        string                     `json:"workflow"`
	Session         string                     `json:"session"`
	TmuxEndpoint    string                     `json:"tmux_endpoint,omitempty"`
	CurrentStage    string                     `json:"current_stage"`
	Paused          bool                       `json:"paused"`
	Completed       bool                       `json:"completed"`
	Dispatches      []workflow.StageDispatch   `json:"dispatches"`
	CheckpointToken string                     `json:"checkpoint_token"`
	ManualGate      *workflow.ManualGateStatus `json:"manual_gate,omitempty"`
}

// GetCheckpoint reads or reconciles one durable workflow checkpoint.
func GetCheckpoint(ctx context.Context, projectRoot, session, pane, token, outcome string, recoverDelivery bool) (CheckpointResult, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return CheckpointResult{}, fmt.Errorf("workflow project root is required")
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return CheckpointResult{}, err
	}
	store := &workflow.StateStore{Dir: filepath.Join(root, ".ntm", "workflows", "state")}
	var state *workflow.WorkflowState
	if recoverDelivery {
		state, err = store.ReconcileDelivery(ctx, session, pane, token, outcome)
	} else {
		state, err = store.Load(session)
	}
	if err != nil {
		return CheckpointResult{}, err
	}
	if state == nil {
		return CheckpointResult{}, fmt.Errorf("no workflow checkpoint for session %q in %s; check --project-root", session, root)
	}
	currentToken, err := state.CheckpointToken()
	if err != nil {
		return CheckpointResult{}, err
	}
	manualGate, err := store.ManualStatus(state)
	if err != nil {
		return CheckpointResult{}, err
	}
	return CheckpointResult{
		Success: true, Workflow: state.WorkflowName, Session: state.SessionName,
		TmuxEndpoint: state.TmuxEndpoint,
		CurrentStage: state.CurrentStage, Paused: state.Paused, Completed: state.Completed,
		Dispatches:      append([]workflow.StageDispatch{}, state.Dispatches...),
		CheckpointToken: currentToken,
		ManualGate:      manualGate,
	}, nil
}
