package robot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

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
