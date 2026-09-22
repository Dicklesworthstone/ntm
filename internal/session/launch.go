package session

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type savedPaneLaunch struct {
	pane      PaneState
	directory string
	spec      *tmux.AgentLaunchSpec
	plan      *resilience.AgentLaunchPlan
	outcome   ResumePane
}

func sortedSavedPanes(state *SessionState) []PaneState {
	panes := append([]PaneState(nil), state.Panes...)
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		return panes[i].Index < panes[j].Index
	})
	return panes
}

// preflightSavedLaunches pins complete commands and configuration for every
// pane before topology replacement. The caller's saved snapshot stays immutable.
func preflightSavedLaunches(ctx context.Context, state *SessionState, cmds AgentCommands, cfg *config.Config, resume *ResumeOptions) ([]savedPaneLaunch, error) {
	if ctx == nil {
		return nil, errors.New("session launch preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("session state is nil")
	}
	if err := ValidateAutomatedRelaunch(state); err != nil {
		return nil, err
	}
	directory, err := restoreWorkingDirectory(state.WorkDir)
	if err != nil {
		return nil, err
	}
	panes := sortedSavedPanes(state)
	launches := make([]savedPaneLaunch, len(panes))
	for i, pane := range panes {
		launch := &launches[i]
		launch.pane = pane
		launch.outcome = ResumePane{Index: pane.Index, WindowIndex: pane.WindowIndex, Title: pane.Title, AgentType: pane.AgentType, Action: "skipped"}
		launch.directory, err = restorePaneDirectory(pane, directory)
		if err != nil {
			return nil, fmt.Errorf("saved pane %d.%d: %w", pane.WindowIndex, pane.Index, err)
		}
		agentType := tmux.ParsePaneAgentTypeOption(pane.AgentType)
		if pane.LaunchSpec != nil {
			if err := pane.LaunchSpec.ValidateReplay(agentType); err != nil {
				return nil, fmt.Errorf("saved pane %d.%d launch settings: %w", pane.WindowIndex, pane.Index, err)
			}
			copy := *pane.LaunchSpec
			copy.OmittedEnv = append([]string(nil), pane.LaunchSpec.OmittedEnv...)
			launch.spec = &copy
		}
		if agentType == tmux.AgentUser || agentType == tmux.AgentUnknown {
			continue
		}
		command := pane.Command
		if launch.spec != nil {
			command = launch.spec.Command
		} else if command == "" {
			command = getAgentCommand(pane.AgentType, cmds)
		}
		launch.outcome.Action = "launched"
		if resume != nil && pane.HasFreshSessionBinding() {
			provider := agentsession.ResumeProvider(pane.AgentType)
			if pane.SessionProvider != "" && pane.SessionProvider != provider {
				return nil, fmt.Errorf("saved pane %d.%d resume provider %q does not match agent %q", pane.WindowIndex, pane.Index, pane.SessionProvider, pane.AgentType)
			}
			if provider != "" {
				if pane.LaunchSpec == nil && pane.Command == "" {
					command = agentsession.ResumeCommand(provider, pane.SessionID, resume.PreferCASR)
				} else {
					resumeOpts := agentsession.ResumeLaunchOptions{}
					if launch.spec != nil {
						resumeOpts.SystemPromptFile = launch.spec.SystemPromptFile
					}
					command, err = agentsession.ResumeLaunchCommand(provider, pane.SessionID, command, resumeOpts)
					if err != nil {
						return nil, fmt.Errorf("saved pane %d.%d native resume: %w", pane.WindowIndex, pane.Index, err)
					}
				}
				launch.outcome.Action = "resumed"
				launch.outcome.SessionID, launch.outcome.Provider = pane.SessionID, provider
			}
		}
		if command == "" {
			launch.outcome.Action = "skipped"
			continue
		}
		if launch.spec == nil {
			launch.spec = &tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: agentType, Model: pane.Model}
			// Old saves carry no envelope. Preserve their existing isolation
			// policy, then record it so the next save no longer depends on defaults.
			if agentType == tmux.AgentClaude && cfg != nil && cfg.Agents.ClaudeIsolateCredentials {
				launch.spec.ClaudeIsolateCredentials = true
				launch.spec.ClaudeTokenFile, err = swarm.ResolveClaudeSetupTokenFile(cfg.Agents.ClaudeTokenFile)
				if err != nil {
					return nil, fmt.Errorf("saved pane %d.%d Claude token file: %w", pane.WindowIndex, pane.Index, err)
				}
			}
		}
		launch.spec.Command = command
		if _, err := buildRestoreCommand(launch.directory, command); err != nil {
			return nil, fmt.Errorf("saved pane %d.%d launch command: %w", pane.WindowIndex, pane.Index, err)
		}
		launch.plan, err = resilience.PreflightAgentLaunchSpec(ctx, cfg, *launch.spec, launch.directory)
		if err != nil {
			return nil, fmt.Errorf("saved pane %d.%d launch settings: %w", pane.WindowIndex, pane.Index, err)
		}
		launch.outcome.Command = command
	}
	return launches, ctx.Err()
}

// dispatchSavedLaunches uses the physical targets obtained during topology
// creation. A failed metadata write prevents dispatch, and successful panes are
// retained when a later pane fails. Counts mean command dispatch, not agent-ready.
func dispatchSavedLaunches(ctx context.Context, name string, launches []savedPaneLaunch, panes []tmux.Pane) (*ResumeResult, error) {
	result := &ResumeResult{Session: name, Panes: make([]ResumePane, len(launches))}
	var failures []error
	for i, launch := range launches {
		result.Panes[i] = launch.outcome
		outcome := &result.Panes[i]
		if launch.plan == nil {
			result.Skipped++
			continue
		}
		var err error
		if i >= len(panes) {
			err = fmt.Errorf("restored session has %d pane(s); saved pane has no target", len(panes))
		} else {
			outcome.PaneID = panes[i].ID
			var command string
			// Position is unique across windows; tmux pane indices are not.
			command, err = launch.plan.Prepare(ctx, launch.directory, name, i)
			if err == nil {
				command, err = buildRestoreCommand(launch.directory, command)
			}
			if err == nil {
				err = validateSavedLaunchTarget(ctx, name, panes[i])
			}
			if err == nil {
				err = tmux.SetPaneLaunchSpecContext(ctx, panes[i].ID, *launch.spec)
			}
			if err == nil {
				err = tmux.SendKeysForAgentContext(ctx, panes[i].ID, command, true, launch.spec.AgentType)
			}
		}
		if err != nil {
			outcome.Action, outcome.Error = "failed", err.Error()
			result.Failed++
			failures = append(failures, fmt.Errorf("pane %d.%d agent dispatch: %w", launch.pane.WindowIndex, launch.pane.Index, err))
		} else if outcome.Action == "resumed" {
			result.Resumed++
		} else {
			result.Launched++
		}
		_ = audit.LogEvent(name, audit.EventTypeSpawn, audit.ActorSystem, "session.restore.agent", map[string]interface{}{
			"pane_id": outcome.PaneID, "pane_index": outcome.Index, "window_index": outcome.WindowIndex,
			"agent_type": outcome.AgentType, "action": outcome.Action, "error": outcome.Error,
		}, nil)
	}
	if len(failures) != 0 {
		return result, fmt.Errorf("launched %d of %d agent(s) in session %q: %w", result.Launched+result.Resumed, result.Launched+result.Resumed+result.Failed, name, errors.Join(failures...))
	}
	return result, nil
}

func validateSavedLaunchTarget(ctx context.Context, session string, expected tmux.Pane) error {
	if expected.PID <= 0 {
		return fmt.Errorf("pane %s has no recorded process identity before launch", expected.ID)
	}
	if err := tmux.ValidatePaneLaunchBaselineLiveContext(ctx, session, expected.ID); err != nil {
		return err
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fmt.Errorf("checking restored pane identity: %w", err)
	}
	for _, pane := range panes {
		if pane.ID != expected.ID {
			continue
		}
		if pane.PID != expected.PID || pane.Type != expected.Type {
			return fmt.Errorf("pane %s changed process or agent identity before launch", expected.ID)
		}
		if !pane.IdleShell() {
			return fmt.Errorf("pane %s is not an idle shell before launch", expected.ID)
		}
		return nil
	}
	return fmt.Errorf("pane %s disappeared before launch", expected.ID)
}

// RestoreWithAgents preflights every saved launch before --force can destroy an
// existing session, then restores topology and dispatches to its captured panes.
// A non-nil result with an error reports a partial launch after topology restore.
func RestoreWithAgents(ctx context.Context, state *SessionState, cmds AgentCommands, cfg *config.Config, opts RestoreOptions) (*ResumeResult, error) {
	launches, err := preflightSavedLaunches(ctx, state, cmds, cfg, nil)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = state.Name
	}
	var result *ResumeResult
	err = restoreSession(ctx, state, opts, func(panes []tmux.Pane) error {
		var launchErr error
		result, launchErr = dispatchSavedLaunches(ctx, name, launches, panes)
		return launchErr
	})
	return result, err
}
