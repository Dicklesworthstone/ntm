package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SpawnStaggerRequest is the requested thundering-herd prompt pacing for one
// spawn batch (docs/ORCHESTRATION_FEATURES.md Feature 7). It is the single
// stagger contract of `ntm spawn` and `--robot-spawn`: both surfaces resolve
// it with ResolveSpawnStagger, so a mode means the same thing everywhere.
type SpawnStaggerRequest struct {
	// Mode is config.SpawnStagger{None,Fixed,Smart}; empty means none.
	Mode string
	// Delay is the fixed-mode interval between consecutive agents.
	Delay time.Duration
	// Legacy is the `ntm spawn --stagger[=DURATION]` interval. It applies only
	// when Mode is none (an explicit mode wins); zero disables it.
	Legacy time.Duration
}

// SpawnStaggerLegacy is the resolved mode of `ntm spawn --stagger[=DURATION]`
// without --stagger-mode. It is a resolution result, never a requested mode.
const SpawnStaggerLegacy = "legacy"

// SpawnStagger is a resolved pacing policy: the agent at delivery order i
// (0-based) receives its prompt i*Interval after the first agent.
type SpawnStagger struct {
	Mode     string        // none, fixed, smart, or legacy
	Interval time.Duration // gap between consecutive agents' deliveries
	Provider string        // smart only: rate-limit bucket Interval was learned from
}

// ResolveSpawnStagger resolves req for a batch of agent types (any spelling
// ratelimit.NormalizeProvider accepts, e.g. "cc", "claude", "cod", "omp").
// Smart mode reads the learned delay of the strictest provider in the batch
// from tracker; a nil tracker means no learned history, i.e. that provider's
// built-in default delay.
func ResolveSpawnStagger(req SpawnStaggerRequest, agentTypes []string, tracker *ratelimit.RateLimitTracker) SpawnStagger {
	switch req.Mode {
	case config.SpawnStaggerFixed:
		return SpawnStagger{Mode: config.SpawnStaggerFixed, Interval: req.Delay}
	case config.SpawnStaggerSmart:
		if tracker == nil {
			tracker = ratelimit.NewRateLimitTracker("")
		}
		provider := spawnStaggerProvider(agentTypes)
		return SpawnStagger{Mode: config.SpawnStaggerSmart, Interval: tracker.GetOptimalDelay(provider), Provider: provider}
	}
	if req.Legacy > 0 {
		return SpawnStagger{Mode: SpawnStaggerLegacy, Interval: req.Legacy}
	}
	return SpawnStagger{Mode: config.SpawnStaggerNone}
}

// spawnStaggerProvider picks the strictest rate-limit bucket present in the
// batch: anthropic, then openai, then google. omp routes panes through its own
// configured providers, so an omp-only batch uses the tracker's own omp bucket
// instead of inheriting Anthropic's learned delay. A batch with none of these
// types uses anthropic, the strictest default.
func spawnStaggerProvider(agentTypes []string) string {
	present := make(map[string]bool, len(agentTypes))
	for _, agentType := range agentTypes {
		present[ratelimit.NormalizeProvider(agentType)] = true
	}
	for _, provider := range []string{"anthropic", "openai", "google", ratelimit.NormalizeProvider("omp")} {
		if present[provider] {
			return provider
		}
	}
	return "anthropic"
}

// Active reports whether deliveries are paced at all.
func (s SpawnStagger) Active() bool {
	return s.Mode != "" && s.Mode != config.SpawnStaggerNone && s.Interval > 0
}

// Delay is the offset of delivery order (0-based) from the first delivery.
func (s SpawnStagger) Delay(order int) time.Duration {
	if !s.Active() || order <= 0 {
		return 0
	}
	return time.Duration(order) * s.Interval
}

// SpawnStaggerPlan reports how --robot-spawn paces prompt delivery (initial
// --spawn-prompt prompts and --spawn-assign-work prompts) between agents.
type SpawnStaggerPlan struct {
	Mode       string             `json:"mode"`               // none, fixed, or smart
	IntervalMs int64              `json:"interval_ms"`        // gap between consecutive agents' deliveries
	Provider   string             `json:"provider,omitempty"` // smart: rate-limit bucket the interval was learned from
	Warning    string             `json:"warning,omitempty"`  // smart: learned history unreadable, built-in delay used
	Schedule   []SpawnStaggerSlot `json:"schedule"`
}

// SpawnStaggerSlot is one agent's place in the delivery schedule.
type SpawnStaggerSlot struct {
	Pane        string `json:"pane"`
	AgentType   string `json:"agent_type"`
	Order       int    `json:"order"`                  // 1-based delivery position
	DelayMs     int64  `json:"delay_ms"`               // offset from the first delivery
	ScheduledAt string `json:"scheduled_at,omitempty"` // planned delivery time (RFC3339Nano); absent until delivery starts
}

// loadSpawnRateLimits loads the project's learned rate-limit history
// (.ntm/rate_limits.json), the same store `ntm spawn` smart mode reads.
func loadSpawnRateLimits(dir string) (*ratelimit.RateLimitTracker, error) {
	tracker := ratelimit.NewRateLimitTracker(dir)
	if err := tracker.LoadFromDir(dir); err != nil {
		return nil, err
	}
	return tracker, nil
}

// planSpawnStagger resolves the batch's stagger over agents (user panes are
// skipped) and builds its reportable schedule. An unreadable rate-limit
// history degrades smart mode to the built-in provider delay and says so.
func planSpawnStagger(opts SpawnOptions, dir string, agents []SpawnedAgent, deps SpawnLifecycleDependencies) (SpawnStagger, *SpawnStaggerPlan) {
	agentTypes := make([]string, 0, len(agents))
	for _, agent := range agents {
		if agent.Type != "user" {
			agentTypes = append(agentTypes, agent.Type)
		}
	}
	var tracker *ratelimit.RateLimitTracker
	warning := ""
	if opts.StaggerMode == config.SpawnStaggerSmart && deps.LoadRateLimits != nil {
		loaded, err := deps.LoadRateLimits(dir)
		if err != nil {
			warning = fmt.Sprintf("rate-limit history unavailable (%v); using the provider's built-in delay", err)
		} else {
			tracker = loaded
		}
	}
	stagger := ResolveSpawnStagger(SpawnStaggerRequest{Mode: opts.StaggerMode, Delay: opts.StaggerDelay}, agentTypes, tracker)
	plan := &SpawnStaggerPlan{
		Mode:       stagger.Mode,
		IntervalMs: stagger.Interval.Milliseconds(),
		Provider:   stagger.Provider,
		Warning:    warning,
		Schedule:   make([]SpawnStaggerSlot, 0, len(agentTypes)),
	}
	for _, agent := range agents {
		if agent.Type == "user" {
			continue
		}
		order := len(plan.Schedule)
		plan.Schedule = append(plan.Schedule, SpawnStaggerSlot{
			Pane: agent.Pane, AgentType: agent.Type, Order: order + 1,
			DelayMs: stagger.Delay(order).Milliseconds(),
		})
	}
	return stagger, plan
}

// spawnStaggerPacer holds each delivery slot until its planned offset from
// the first slot, which starts the schedule. Slots run in order; a slow
// delivery consumes the following gap instead of shifting later agents.
type spawnStaggerPacer struct {
	stagger SpawnStagger
	plan    *SpawnStaggerPlan
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
	base    time.Time
	started bool
}

func newSpawnStaggerPacer(stagger SpawnStagger, plan *SpawnStaggerPlan, deps SpawnLifecycleDependencies) *spawnStaggerPacer {
	return &spawnStaggerPacer{stagger: stagger, plan: plan, now: deps.Now, wait: deps.Wait}
}

// await blocks until slot order (0-based) is due. The first call anchors the
// schedule and stamps every slot's scheduled_at. A nil pacer never waits.
func (p *spawnStaggerPacer) await(ctx context.Context, order int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if p.now == nil || p.wait == nil {
		return errors.New("spawn stagger requires a clock and a wait port")
	}
	if !p.started {
		p.started = true
		p.base = p.now()
		if p.plan != nil {
			for i := range p.plan.Schedule {
				due := p.base.Add(p.stagger.Delay(p.plan.Schedule[i].Order - 1))
				p.plan.Schedule[i].ScheduledAt = due.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	if remaining := p.base.Add(p.stagger.Delay(order)).Sub(p.now()); remaining > 0 {
		if err := p.wait(ctx, remaining); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// stamp is the delivery time reported for a slot that just delivered.
func (p *spawnStaggerPacer) stamp() string {
	if p == nil || p.now == nil {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return p.now().UTC().Format(time.RFC3339Nano)
}

// SpawnAdmissionError retains a machine-readable admission decision for launch
// surfaces which return errors rather than a SpawnOutput (notably add/scale).
// Err remains unwrap-able so cancellation does not turn into a quota refusal.
type SpawnAdmissionError struct {
	ErrorCode string
	Admission *pressure.SpawnAdmission
	Err       error
}

func (e *SpawnAdmissionError) Error() string {
	if e == nil || e.Err == nil {
		return "agent addition was not admitted"
	}
	return e.Err.Error()
}

func (e *SpawnAdmissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// BeginAgentAddition admits an entire add batch with the same fleet inventory,
// pressure policy and cross-process ownership used by GetSpawn. Counts include
// every requested type, including personas' underlying types and plugins; aliases
// are combined before evaluation. It does not create panes or launch agents.
//
// On success, defer the returned idempotent release immediately and call it once
// the last launch completes. Retain ownership across splits, launch delays and
// partial failures; releasing per agent would reopen the check-then-launch race.
// On error ownership has already been released and release is nil. Disabled
// pacing returns a no-op release and nil admission without observing the fleet.
func BeginAgentAddition(ctx context.Context, session string, counts map[string]int, cfg *config.Config) (func(), *pressure.SpawnAdmission, error) {
	return beginAgentAddition(ctx, session, counts, cfg, spawnLifecycleDeps(nil))
}

func beginAgentAddition(ctx context.Context, session string, counts map[string]int, cfg *config.Config, deps SpawnLifecycleDependencies) (func(), *pressure.SpawnAdmission, error) {
	fail := func(code string, admission *pressure.SpawnAdmission, err error) (func(), *pressure.SpawnAdmission, error) {
		return nil, admission, &SpawnAdmissionError{ErrorCode: code, Admission: admission, Err: err}
	}
	if ctx == nil {
		return fail(ErrCodeInvalidFlag, nil, errors.New("agent addition admission requires a context"))
	}
	if err := ctx.Err(); err != nil {
		return fail(ErrCodeTimeout, nil, err)
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return fail(ErrCodeInvalidFlag, nil, err)
	}
	if cfg == nil {
		return fail(ErrCodeInvalidFlag, nil, errors.New("agent addition admission requires the selected configuration"))
	}
	keys := make([]string, 0, len(counts))
	for kind := range counts {
		keys = append(keys, kind)
	}
	sort.Strings(keys)
	requested := make(map[string]int, len(counts))
	total, maxCount := 0, int(^uint(0)>>1)
	for _, raw := range keys {
		count := counts[raw]
		kind := spawnAdmissionAgentType(tmux.AgentType(raw))
		if count < 0 || kind == "" || kind == "user" || kind == "unknown" {
			return fail(ErrCodeInvalidFlag, nil, fmt.Errorf("invalid agent addition count %q=%d", raw, count))
		}
		if count > maxCount-total {
			return fail(ErrCodeInvalidFlag, nil, errors.New("requested agent count exceeds supported pane capacity"))
		}
		total += count
		requested[kind] += count
	}
	if total == 0 {
		return fail(ErrCodeInvalidFlag, nil, errors.New("no agents specified"))
	}
	if !cfg.SpawnPacing.Enabled {
		return func() {}, nil, nil
	}

	// Reuse the existing fence and failure receipts rather than a separate
	// lock namespace or quota policy. Retain the underlying ownership error.
	opts := SpawnOptions{Session: session, NoUserPane: true}
	output := newSpawnOutput(time.Now(), opts)
	var ownershipErr error
	if acquire := deps.AcquireAdmission; acquire != nil {
		deps.AcquireAdmission = func(ctx context.Context) (func(), error) {
			release, err := acquire(ctx)
			ownershipErr = err
			return release, err
		}
	}
	release, ok := beginSpawnAdmission(ctx, opts, cfg, deps, output, total, total)
	if !ok {
		cause := errors.Join(ownershipErr, ctx.Err())
		if cause == nil {
			cause = errors.New(output.Error)
		}
		return fail(output.ErrorCode, output.Admission, cause)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	var inventoryErr error
	input := collectSpawnAdmissionInputWithPanes(ctx, opts, cfg, total, total,
		func(ctx context.Context) (map[string][]tmux.Pane, error) {
			if deps.GetAllPanes == nil {
				inventoryErr = errors.New("agent addition fleet inventory is unavailable")
				return nil, inventoryErr
			}
			panes, err := deps.GetAllPanes(ctx)
			inventoryErr = err
			return panes, err
		})
	// The collector shares all host/topology policy with robot spawning. Add
	// supports more types than SpawnOptions, so supply its complete count map.
	input.RequestedByType = requested
	if err := spawnCancellationError(ctx, inventoryErr); err != nil {
		return fail(ErrCodeTimeout, nil, err)
	}
	// EvaluateSpawnAdmission's pane request is a desired session total, not
	// an increment. Add never reuses the session's existing agent panes.
	if input.SessionPanes > maxCount-total {
		return fail(ErrCodeInvalidFlag, nil, errors.New("agent addition exceeds supported session pane capacity"))
	}
	input.RequestedPanes = input.SessionPanes + total
	admission := pressure.EvaluateSpawnAdmission(input)
	admission.Serialized = needsSpawnAdmissionFence(opts, cfg)
	if err := ctx.Err(); err != nil {
		return fail(ErrCodeTimeout, &admission, err)
	}
	if admission.Decision != pressure.SpawnAdmissionAdmit {
		return fail(ErrCodeResourceBusy, &admission, fmt.Errorf("agent addition %s: %s; %s", admission.Decision, admission.Reason, admission.Hint))
	}
	handedOff = true
	return release, &admission, nil
}

// WithSpawnLaunchInterval adds a minimum start-to-start interval to one spawn
// request. It wraps the existing launcher, so topology validation, admission,
// readiness, assignment, and partial-failure reporting still belong to GetSpawn.
// The first launch is immediate. A slow or failed launch consumes its interval;
// there is no catch-up burst and no wait after the final launch. Dry runs never
// call the launcher and therefore never wait.
//
// Call this once per request. The returned options own their pacing state and
// a copy of the lifecycle ports; the caller's options and ports are not mutated.
// A zero interval preserves the original options without installing a wrapper.
func WithSpawnLaunchInterval(opts SpawnOptions, interval time.Duration) (SpawnOptions, error) {
	if interval < 0 {
		return opts, errors.New("launch_interval must be a non-negative duration")
	}
	if interval == 0 {
		return opts, nil
	}

	var deps SpawnLifecycleDependencies
	if opts.LifecycleDeps != nil {
		deps = *opts.LifecycleDeps
	}
	launch := deps.LaunchAgent
	if launch == nil {
		launch = launchAgent
	}

	// GetSpawn launches sequentially. Keep the policy safe even when a caller
	// invokes its lifecycle port concurrently, without an uncancellable mutex
	// wait or reserving future slots for requests that have already cancelled.
	gate := make(chan struct{}, 1)
	var nextStart time.Time
	deps.LaunchAgent = func(ctx context.Context, pane tmux.Pane, session, agentType string, number int, dir, command string) (SpawnedAgent, error) {
		if ctx == nil {
			return SpawnedAgent{}, errors.New("paced spawn requires a context")
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return SpawnedAgent{}, ctx.Err()
		}
		defer func() { <-gate }()
		if err := ctx.Err(); err != nil {
			return SpawnedAgent{}, err
		}
		if delay := time.Until(nextStart); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return SpawnedAgent{}, ctx.Err()
			}
		}
		// Cancellation and the timer can become ready together. Do not turn
		// that race into an extra launch after the operator cancels the job.
		if err := ctx.Err(); err != nil {
			return SpawnedAgent{}, err
		}
		nextStart = time.Now().Add(interval)
		return launch(ctx, pane, session, agentType, number, dir, command)
	}
	opts.LifecycleDeps = &deps
	return opts, nil
}

// WithSpawnLaunchReadiness gates each launch on that agent's actual readiness
// before admitting another launch. It reuses GetSpawn's launcher and readiness
// detector, not a second startup protocol. Zero leaves the request unchanged.
// A failed launch or readiness check permanently stops this request's launcher;
// already created panes and launched processes are left intact for inspection.
//
// Install after WithSpawnProgress and WithSpawnLaunchInterval: progress records
// the launched process before waiting, and interval pacing still bounds starts.
// The readiness gate is outermost so a failed request cannot wait for another
// interval before observing its stop condition. Dry runs invoke no ports.
func WithSpawnLaunchReadiness(opts SpawnOptions, timeout time.Duration) (SpawnOptions, error) {
	if timeout < 0 {
		return opts, errors.New("launch_ready_timeout must be a non-negative duration")
	}
	if timeout == 0 {
		return opts, nil
	}
	base := spawnLifecycleDeps(opts.LifecycleDeps)
	deps := base
	gate := make(chan struct{}, 1)
	var halted error // Protected by gate, including concurrent custom port calls.
	deps.LaunchAgent = func(ctx context.Context, pane tmux.Pane, session, agentType string, number int, dir, command string) (SpawnedAgent, error) {
		if ctx == nil {
			return SpawnedAgent{}, errors.New("readiness-gated spawn requires a context")
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return SpawnedAgent{}, ctx.Err()
		}
		defer func() { <-gate }()
		if err := ctx.Err(); err != nil {
			return SpawnedAgent{}, err
		}
		if halted != nil {
			return SpawnedAgent{}, halted
		}
		agent, err := base.LaunchAgent(ctx, pane, session, agentType, number, dir, command)
		if agent.Pane == "" {
			agent.Pane = fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
		}
		if agent.PaneID == "" {
			agent.PaneID = pane.ID
		}
		if agent.Type == "" {
			agent.Type = agentType
		}
		// Never trust a launch receipt's Ready bit as a fresh observation.
		agent.Ready = false
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		if err == nil && agent.Error != "" {
			err = errors.New(agent.Error)
		}
		if err == nil && (agent.Pane != fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index) ||
			(pane.ID != "" && agent.PaneID != pane.ID) || agent.Type != agentType) {
			err = errors.New("launch receipt does not identify the requested agent pane")
		}
		if err == nil {
			observation := &SpawnOutput{Session: session, WorkingDir: dir, Agents: []SpawnedAgent{agent}}
			readyCtx, cancel := context.WithTimeout(ctx, timeout)
			err = base.WaitForReady(readyCtx, observation, timeout)
			// A backend returning nil after the deadline cannot grant readiness.
			if readyCtx.Err() != nil {
				err = errors.Join(err, readyCtx.Err())
			}
			cancel()
			if err == nil && (observation.Session != session || len(observation.Agents) != 1 ||
				observation.Agents[0].Pane != agent.Pane || observation.Agents[0].PaneID != agent.PaneID ||
				observation.Agents[0].Type != agent.Type ||
				!observation.Agents[0].Ready || observation.Agents[0].Error != "") {
				err = errors.New("readiness check returned no ready observation for the launched agent")
			}
		}
		if err != nil {
			halted = fmt.Errorf("startup stopped at %s agent %d (%s): %w", agentType, number, agent.Pane, err)
			agent.Error = halted.Error()
			return agent, halted
		}
		agent.Ready = true
		return agent, nil
	}
	opts.LifecycleDeps = &deps
	// Existing preflight must reject types whose readiness protocol is not
	// supported (including types expanded from presets). The final readiness
	// pass sees the same agents already marked ready by these individual checks.
	opts.WaitReady = true
	return opts, nil
}

// SpawnProgress describes a lifecycle boundary without retaining launch commands
// or prompts. A started phase is intent, not proof the side effect completed.
// PaneID is the durable tmux identity; Agent.Pane is its physical window.pane.
type SpawnProgress struct {
	Stage      string         `json:"stage"`
	Phase      string         `json:"phase"`
	Session    string         `json:"session"`
	WorkingDir string         `json:"working_dir,omitempty"`
	PaneID     string         `json:"pane_id,omitempty"`
	AgentType  string         `json:"agent_type,omitempty"`
	Number     int            `json:"number,omitempty"`
	Agent      *SpawnedAgent  `json:"agent,omitempty"`
	Agents     []SpawnedAgent `json:"agents,omitempty"`
	MonitorPID int            `json:"monitor_pid,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// WithSpawnProgress checkpoints intent before each mutating lifecycle port and
// its outcome afterwards. It does not implement spawning: every effect still
// runs through the existing lifecycle dependencies. Install it before launch
// pacing so a launch-start record is written after its pacing wait.
//
// Observer failure is sticky: subsequent lifecycle effects cannot proceed, even
// if the engine normally tolerates that stage's failure. The caller should also
// cancel the spawn context on observer failure to stop later work assignment.
// Finished observations run even after cancellation to retain partial effects.
// Dry runs do not invoke these ports. Nil observers preserve options unchanged.
func WithSpawnProgress(opts SpawnOptions, observe func(SpawnProgress) error) SpawnOptions {
	if observe == nil {
		return opts
	}
	base := spawnLifecycleDeps(opts.LifecycleDeps)
	deps := base
	gate := make(chan struct{}, 1)
	var halted error // Owned by gate, including concurrent custom port calls.
	run := func(ctx context.Context, event SpawnProgress, effect func() (SpawnProgress, error)) error {
		if ctx == nil {
			return errors.New("observed spawn requires a context")
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-gate }()
		if halted != nil {
			return halted
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		event.Phase = "started"
		if err := observe(event); err != nil {
			halted = fmt.Errorf("checkpoint spawn %s intent: %w", event.Stage, err)
			return halted
		}
		// A durable write can outlive a deadline or trigger caller cancellation.
		if err := ctx.Err(); err != nil {
			return err
		}
		finished, effectErr := effect()
		finished.Phase = "finished"
		if effectErr != nil {
			finished.Error = effectErr.Error()
		}
		if err := observe(finished); err != nil {
			halted = fmt.Errorf("checkpoint spawn %s outcome: %w", event.Stage, err)
		}
		return errors.Join(effectErr, halted)
	}
	deps.CreateSession = func(ctx context.Context, session, dir string, history int) error {
		event := SpawnProgress{Stage: "create_session", Session: session, WorkingDir: dir}
		return run(ctx, event, func() (SpawnProgress, error) {
			return event, base.CreateSession(ctx, session, dir, history)
		})
	}
	deps.SplitWindow = func(ctx context.Context, session, dir string) (string, error) {
		event := SpawnProgress{Stage: "split_window", Session: session, WorkingDir: dir}
		var paneID string
		err := run(ctx, event, func() (SpawnProgress, error) {
			var err error
			paneID, err = base.SplitWindow(ctx, session, dir)
			event.PaneID = paneID
			return event, err
		})
		return paneID, err
	}
	deps.ApplyTiledLayout = func(ctx context.Context, session string) error {
		event := SpawnProgress{Stage: "layout", Session: session}
		return run(ctx, event, func() (SpawnProgress, error) {
			return event, base.ApplyTiledLayout(ctx, session)
		})
	}
	deps.LaunchAgent = func(ctx context.Context, pane tmux.Pane, session, agentType string, number int, dir, command string) (SpawnedAgent, error) {
		event := SpawnProgress{Stage: "launch_agent", Session: session, WorkingDir: dir,
			PaneID: pane.ID, AgentType: agentType, Number: number}
		var agent SpawnedAgent
		err := run(ctx, event, func() (SpawnProgress, error) {
			var err error
			agent, err = base.LaunchAgent(ctx, pane, session, agentType, number, dir, command)
			// Enrich only the observation. Never mutate the original receipt or
			// expose its address to an observer that could change the return value.
			observed := agent
			if observed.Pane == "" {
				observed.Pane = fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
			}
			if observed.Type == "" {
				observed.Type = agentType
			}
			event.Agent = &observed
			return event, err
		})
		return agent, err
	}
	deps.WaitForReady = func(ctx context.Context, output *SpawnOutput, timeout time.Duration) error {
		if output == nil {
			return errors.New("observed spawn readiness requires an output")
		}
		event := SpawnProgress{Stage: "wait_ready", Session: output.Session, WorkingDir: output.WorkingDir}
		return run(ctx, event, func() (SpawnProgress, error) {
			err := base.WaitForReady(ctx, output, timeout)
			event.Agents = append([]SpawnedAgent(nil), output.Agents...)
			return event, err
		})
	}
	deps.StartSessionMonitor = func(ctx context.Context, req resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		event := SpawnProgress{Stage: "start_monitor", Session: req.Session, WorkingDir: req.ProjectDir}
		var result *resilience.SpawnMonitorResult
		err := run(ctx, event, func() (SpawnProgress, error) {
			var err error
			result, err = base.StartSessionMonitor(ctx, req)
			if result != nil {
				event.MonitorPID = result.MonitorPID
			}
			return event, err
		})
		return result, err
	}
	opts.LifecycleDeps = &deps
	return opts
}
