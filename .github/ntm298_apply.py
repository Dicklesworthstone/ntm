from pathlib import Path

ROOT = Path(".")

def read(path):
    return (ROOT / path).read_text()

def write(path, text):
    (ROOT / path).write_text(text)

def replace_once(path, old, new):
    text = read(path)
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one match, found {count}: {old[:120]!r}")
    write(path, text.replace(old, new, 1))

replace_once("internal/resilience/manifest.go", '''type AgentConfig struct {
	PaneID    string `json:"pane_id"`
	PaneIndex int    `json:"pane_index"`
	Type      string `json:"type"`
	Model     string `json:"model"`
	Command   string `json:"command"`
}
''', '''type AgentConfig struct {
	PaneID        string         `json:"pane_id"`
	PaneIndex     int            `json:"pane_index"`
	Type          string         `json:"type"`
	Model         string         `json:"model"`
	Command       string         `json:"command"`
	LaunchBinding *LaunchBinding `json:"launch_binding,omitempty"`
}
''')

replace_once("internal/resilience/monitor_launch.go", '''	for _, agent := range req.Agents {
		if restartUnsupportedAgentTypes[agent.Type] {
			// Restart remains unsupported for these types until lifecycle
			// fixtures prove the necessary semantics.
			continue
		}
		manifest.Agents = append(manifest.Agents, agent)
	}
''', '''	for _, agentConfig := range req.Agents {
		if restartUnsupportedAgentTypes[agentConfig.Type] {
			// Restart remains unsupported for these types until lifecycle
			// fixtures prove the necessary semantics.
			continue
		}
		agentConfig.LaunchBinding = CloneLaunchBinding(agentConfig.LaunchBinding)
		if agentConfig.LaunchBinding == nil {
			agentConfig.LaunchBinding = CaptureLaunchBinding(agentConfig.Type)
		}
		manifest.Agents = append(manifest.Agents, agentConfig)
	}
''')

replace_once("internal/cli/monitor.go",
'''		monitor.RegisterAgent(agent.PaneID, agent.PaneIndex, 0, agent.Type, agent.Model, agent.Command)
''',
'''		monitor.RegisterAgentWithBinding(agent.PaneID, agent.PaneIndex, 0, agent.Type, agent.Model, agent.Command, agent.LaunchBinding)
''')

replace_once("internal/cli/add.go",
'''	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
''',
'''	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
''')

replace_once("internal/cli/add.go",
'''		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Per-pane Claude credential isolation (GH#237). A pane added to an
''',
'''		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Persist only the environment-free launch command. Credential
		// isolation and plugin environment are applied below for the live
		// process, but are deliberately excluded from restart metadata.
		manifestAgentCmd, err := tmux.SanitizePaneCommand(finalCmd)
		if err != nil {
			return outputError(fmt.Errorf("invalid %s resilience command: %w", agent.Type, err))
		}
		launchBinding := resilience.CaptureLaunchBinding(agentTypeStr)

		// Per-pane Claude credential isolation (GH#237). A pane added to an
''')

replace_once("internal/cli/add.go",
'''		if agent.Type == AgentTypeGrok {
			if _, err := tmux.WaitForPaneProcessStartContext(ctx, session, paneID); err != nil {
				return outputError(fmt.Errorf(
					"launching %s agent in pane %s did not start a stable process: %w",
					agent.Type, paneID, err,
				))
			}
		}
		if rateLimitTracker != nil && agent.Type == AgentTypeCodex {
''',
'''		if agent.Type == AgentTypeGrok {
			if _, err := tmux.WaitForPaneProcessStartContext(ctx, session, paneID); err != nil {
				return outputError(fmt.Errorf(
					"launching %s agent in pane %s did not start a stable process: %w",
					agent.Type, paneID, err,
				))
			}
		}
		if err := resilience.UpsertAgentConfig(session, dir, resilience.AgentConfig{
			PaneID:        paneID,
			PaneIndex:     num,
			Type:          agentTypeStr,
			Model:         agent.Model,
			Command:       manifestAgentCmd,
			LaunchBinding: launchBinding,
		}); err != nil {
			return outputError(fmt.Errorf(
				"persisting restart metadata for pane %s: %w; the pane and launched agent still exist",
				paneID, err,
			))
		}
		if rateLimitTracker != nil && agent.Type == AgentTypeCodex {
''')

replace_once("internal/resilience/monitor.go",
'''	sendKeysFn       = tmux.SendKeys
	buildPaneCmdFn   = tmux.BuildPaneCommand
	sleepFn          = time.Sleep
''',
'''	sendKeysFn             = tmux.SendKeys
	buildPaneCmdFn         = tmux.BuildPaneCommand
	prepareLaunchCommandFn = PrepareLaunchCommand
	sleepFn                = time.Sleep
''')

replace_once("internal/resilience/monitor.go",
'''	Command             string // Original launch command
	RestartCount        int
''',
'''	Command             string // Original environment-free launch command
	LaunchBinding       *LaunchBinding
	RestartCount        int
''')

replace_once("internal/resilience/monitor.go",
'''func (m *Monitor) RegisterAgent(paneID string, paneIndex int, shellPID int, agentType, model, command string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.agents[paneID] = &AgentState{
		PaneID:    paneID,
		PaneIndex: paneIndex,
		ShellPID:  shellPID,
		AgentType: agentType,
		Model:     model,
		Command:   command,
		Healthy:   true,
	}
}
''',
'''func (m *Monitor) RegisterAgent(paneID string, paneIndex int, shellPID int, agentType, model, command string) {
	m.RegisterAgentWithBinding(paneID, paneIndex, shellPID, agentType, model, command, nil)
}

// RegisterAgentWithBinding registers an agent together with its persisted,
// provider-scoped launch affinity.
func (m *Monitor) RegisterAgentWithBinding(
	paneID string,
	paneIndex int,
	shellPID int,
	agentType, model, command string,
	binding *LaunchBinding,
) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.agents[paneID] = &AgentState{
		PaneID:        paneID,
		PaneIndex:     paneIndex,
		ShellPID:      shellPID,
		AgentType:     agentType,
		Model:         model,
		Command:       command,
		LaunchBinding: CloneLaunchBinding(binding),
		Healthy:       true,
	}
}
''')

replace_once("internal/resilience/monitor.go",
'''	buildFunc := buildPaneCmdFn
	sendFunc := sendKeysFn
	isChildAliveFunc := isChildAliveFn
''',
'''	buildFunc := buildPaneCmdFn
	prepareFunc := prepareLaunchCommandFn
	sendFunc := sendKeysFn
	isChildAliveFunc := isChildAliveFn
''')

replace_once("internal/resilience/monitor.go",
'''	agentCommand := currentAgent.Command
	shellPID := currentAgent.ShellPID
	m.mu.Unlock()
''',
'''	agentCommand := currentAgent.Command
	launchBinding := CloneLaunchBinding(currentAgent.LaunchBinding)
	trackedAgentType := currentAgent.AgentType
	shellPID := currentAgent.ShellPID
	m.mu.Unlock()
''')

replace_once("internal/resilience/monitor.go",
'''	// Re-run the agent command in the pane
	paneCmd, err := buildFunc(m.projectDir, agentCommand)
	if err != nil {
		log.Printf("[resilience] Refusing to restart agent %s: %v", agent.PaneID, err)
		return
	}

	// Final PID guard: last-second check before injecting keys.
''',
'''	// Final PID guard: last-second check before injecting keys.
''')

replace_once("internal/resilience/monitor.go",
'''	if present, presentErr := panePresentFunc(m.session, agent.PaneID); presentErr != nil || !present {
		if presentErr != nil {
			log.Printf("[resilience] Refusing to restart agent %s: cannot verify pane membership in session %s: %v", agent.PaneID, m.session, presentErr)
		} else {
			log.Printf("[resilience] Retiring stale pane binding %s: pane no longer exists in session %s", agent.PaneID, m.session)
			m.mu.Lock()
			delete(m.agents, agent.PaneID)
			m.mu.Unlock()
		}
		return
	}

	m.mu.Lock()
''',
'''	if present, presentErr := panePresentFunc(m.session, agent.PaneID); presentErr != nil || !present {
		if presentErr != nil {
			log.Printf("[resilience] Refusing to restart agent %s: cannot verify pane membership in session %s: %v", agent.PaneID, m.session, presentErr)
		} else {
			log.Printf("[resilience] Retiring stale pane binding %s: pane no longer exists in session %s", agent.PaneID, m.session)
			m.mu.Lock()
			delete(m.agents, agent.PaneID)
			m.mu.Unlock()
		}
		return
	}

	caamBinary := ""
	if m.cfg != nil {
		caamBinary = m.cfg.Integrations.CAAM.BinaryPath
	}
	preparedAgentCommand, affinity, err := prepareFunc(ctx, trackedAgentType, caamBinary, launchBinding, agentCommand)
	if err != nil {
		log.Printf("[resilience] Refusing to restart agent %s: launch affinity preflight failed: %v", agent.PaneID, err)
		return
	}
	if affinity == LaunchAffinityUnknown {
		log.Printf("[resilience] Agent %s has legacy unknown launch affinity; restarting with the current controller environment", agent.PaneID)
	}
	paneCmd, err := buildFunc(m.projectDir, preparedAgentCommand)
	if err != nil {
		log.Printf("[resilience] Refusing to restart agent %s: %v", agent.PaneID, err)
		return
	}

	m.mu.Lock()
''')

replace_once("internal/resilience/monitor_test.go",
'''	origBuild := buildPaneCmdFn
	origSleep := sleepFn
''',
'''	origBuild := buildPaneCmdFn
	origPrepare := prepareLaunchCommandFn
	origSleep := sleepFn
''')

replace_once("internal/resilience/monitor_test.go",
'''		buildPaneCmdFn = origBuild
		sleepFn = origSleep
''',
'''		buildPaneCmdFn = origBuild
		prepareLaunchCommandFn = origPrepare
		sleepFn = origSleep
''')

replace_once("internal/robot/restart_pane.go",
'''	"github.com/Dicklesworthstone/ntm/internal/process"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
''',
'''	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
''')

replace_once("internal/robot/restart_pane.go",
'''	AgentRelaunched     map[string]bool                       `json:"agent_relaunched,omitempty"`
	AgentRelaunchStatus map[string]RestartAgentRelaunchStatus `json:"agent_relaunch_status,omitempty"`

	// PaneShellPIDs
''',
'''	AgentRelaunched     map[string]bool                       `json:"agent_relaunched,omitempty"`
	AgentRelaunchStatus map[string]RestartAgentRelaunchStatus `json:"agent_relaunch_status,omitempty"`
	LaunchAffinity      map[string]resilience.LaunchAffinity  `json:"launch_affinity,omitempty"`

	// PaneShellPIDs
''')

replace_once("internal/robot/restart_pane.go",
'''	DispatchDeliverer      dispatchsvc.Deliverer
	DispatchPacer          dispatchsvc.Pacer
}
''',
'''	DispatchDeliverer      dispatchsvc.Deliverer
	DispatchPacer          dispatchsvc.Pacer
	LoadManifest           func(string) (*resilience.SpawnManifest, error)
	PrepareLaunchCommand   func(context.Context, string, string, *resilience.LaunchBinding, string) (string, resilience.LaunchAffinity, error)
}
''')

replace_once("internal/robot/restart_pane.go",
'''		ObserveSession:         observer.Observe,
		DispatchDeliverer:      dispatchsvc.TMUXDeliverer{},
	}
''',
'''		ObserveSession:         observer.Observe,
		DispatchDeliverer:      dispatchsvc.TMUXDeliverer{},
		LoadManifest:           resilience.LoadManifest,
		PrepareLaunchCommand:   resilience.PrepareLaunchCommand,
	}
''')

replace_once("internal/robot/restart_pane.go",
'''	if custom.DispatchPacer != nil {
		deps.DispatchPacer = custom.DispatchPacer
	}
	return deps
}
''',
'''	if custom.DispatchPacer != nil {
		deps.DispatchPacer = custom.DispatchPacer
	}
	if custom.LoadManifest != nil {
		deps.LoadManifest = custom.LoadManifest
	}
	if custom.PrepareLaunchCommand != nil {
		deps.PrepareLaunchCommand = custom.PrepareLaunchCommand
	}
	return deps
}
''')

replace_once("internal/robot/restart_pane.go",
'''	if err := ctx.Err(); err != nil {
		setRestartPaneCancellation(output, err, "restart canceled after assignment preflight")
		return output, nil
	}

	// Dry-run mode
''',
'''	if err := ctx.Err(); err != nil {
		setRestartPaneCancellation(output, err, "restart canceled after assignment preflight")
		return output, nil
	}

	restartCfg := opts.Config
	if beadPreflight != nil && beadPreflight.Policy != nil {
		restartCfg = beadPreflight.Policy
	}
	if restartCfg == nil {
		var cfgErr error
		restartCfg, cfgErr = config.LoadMerged(mustGetwd(), config.DefaultPath())
		if cfgErr != nil {
			restartCfg = config.Default()
		}
	}
	launchPlan, err := prepareRestartLaunchPlan(ctx, opts.Session, targetPanes, multiWindow, restartCfg, launchOverride, deps)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Restore the named launcher profile or remove the stale binding before retrying; no pane was respawned",
		)
		return output, nil
	}
	output.LaunchAffinity = launchPlan.Affinity

	// Dry-run mode
''')

replace_once("internal/robot/restart_pane.go",
'''		cfg := opts.Config
		if beadPreflight != nil && beadPreflight.Policy != nil {
			cfg = beadPreflight.Policy
		}
		if cfg == nil {
			var cfgErr error
			cfg, cfgErr = config.LoadMerged(mustGetwd(), config.DefaultPath())
			if cfgErr != nil {
				cfg = config.Default()
			}
		}

		// Let fresh shells initialize before typing into them.
''',
'''		// Let fresh shells initialize before typing into them.
''')

replace_once("internal/robot/restart_pane.go",
'''			launchCmd, launchCmdErr := restartAgentLaunchCommandWithOverride(cfg, info.ResolvedType, info.Variant, launchOverride)
			if launchCmdErr != nil {
				appendRestartFailureOnce(output, paneKey, fmt.Sprintf("compose relaunch command: %v", launchCmdErr))
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
				continue
			}
''',
'''			launchCmd, ok := launchPlan.Commands[paneKey]
			if !ok {
				appendRestartFailureOnce(output, paneKey, "missing preflighted relaunch command")
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
				continue
			}
''')
