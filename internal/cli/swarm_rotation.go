package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var swarmPreflightAccountRotation = coordinator.PreflightAccountRotation

type swarmRotationPlan struct {
	output     *SwarmAccountRotationOutput
	options    resilience.RotationMonitorOptions
	configPath string
	specs      map[string]tmux.AgentLaunchSpec
	eligible   map[string]bool
}

func swarmRotationPaneKey(session string, index int) string {
	return fmt.Sprintf("%s:%d", session, index)
}

func swarmRotationProvider(agentType string) string {
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "openai"
	default:
		return ""
	}
}

func canonicalSwarmRotationProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "cc", "anthropic":
		return "claude"
	case "openai", "codex", "cod":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// prepareSwarmAccountRotation resolves the policy and exact commands before any
// session creation. The explicit swarm flag enables its newly launched panes;
// a configured provider list can further restrict that scope.
func prepareSwarmAccountRotation(ctx context.Context, plan *swarm.SwarmPlan, opts swarmOptions) (*swarmRotationPlan, error) {
	r := &swarmRotationPlan{
		output:   &SwarmAccountRotationOutput{Providers: []string{}, Declined: []SwarmRotationDecline{}, Monitors: []SwarmRotationMonitorOutput{}},
		specs:    make(map[string]tmux.AgentLaunchSpec),
		eligible: make(map[string]bool),
	}
	if opts.Remote != "" || tmux.DefaultClient.Remote != "" {
		return r, errors.New("automatic account rotation requires local sessions; a local monitor cannot own remote credentials")
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	configPath, err := filepath.Abs(selectedConfigPath())
	if err != nil {
		return r, fmt.Errorf("resolve monitor config path: %w", err)
	}
	r.configPath = configPath
	allowed := make(map[string]bool)
	for _, provider := range cfg.Integrations.CAAM.FailoverProviders {
		allowed[canonicalSwarmRotationProvider(provider)] = true
	}
	providers := make(map[string]bool)
	validatedProjects := make(map[string]bool)
	var unsupportedLaunch []error
	for _, session := range plan.Sessions {
		for _, pane := range session.Panes {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			project, err := filepath.Abs(pane.Project)
			if err != nil {
				return r, fmt.Errorf("resolve project for %s pane %d: %w", session.Name, pane.Index, err)
			}
			if !validatedProjects[project] {
				if _, _, err := config.FindProjectConfig(project); err != nil {
					return r, err
				}
				validatedProjects[project] = true
			}
			key := swarmRotationPaneKey(session.Name, pane.Index)
			// A false entry keeps the normal launcher authoritative for panes
			// outside the monitor scope, including its environment handling.
			r.eligible[key] = false
			provider := swarmRotationProvider(pane.AgentType)
			reason := ""
			if provider == "" {
				reason = "provider has no supported native account recovery"
			} else if len(allowed) > 0 && !allowed[provider] {
				reason = "provider is excluded by integrations.caam.failover_providers"
			} else if provider == "openai" && !opts.ForceGlobalAuth {
				reason = "global Codex account recovery requires --force-global-auth-clobber"
			} else {
				reason = swarmRotationCredentialRestriction(provider)
				if reason != "" {
					unsupportedLaunch = append(unsupportedLaunch, fmt.Errorf("%s pane %d: %s", session.Name, pane.Index, reason))
				}
			}
			if reason == "" {
				template := cfg.Agents.Claude
				if provider == "openai" {
					template = cfg.Agents.Codex
				}
				model := cfg.Models.GetModelName(pane.AgentType, "")
				command, err := config.GenerateAgentCommand(template, config.AgentTemplateVars{Model: model, AgentType: pane.AgentType, SessionName: session.Name, PaneIndex: pane.Index, ProjectDir: project})
				if err != nil {
					reason = fmt.Sprintf("configured launch command cannot be rendered: %v", err)
				} else if err := agentsession.ValidateGlobalCredentialLaunchCommand(pane.AgentType, command, agentsession.ResumeLaunchOptions{}); err != nil {
					reason = fmt.Sprintf("configured launch cannot use global account recovery: %v", err)
				} else {
					r.specs[key] = tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType(pane.AgentType), Command: command, Model: model}
					r.eligible[key] = true
					r.output.EligiblePanes++
					providers[provider] = true
				}
				// A malformed configured command cannot silently fall back to
				// a shell alias or launch without the requested recovery policy.
				if reason != "" {
					unsupportedLaunch = append(unsupportedLaunch, fmt.Errorf("%s pane %d: %s", session.Name, pane.Index, reason))
				}
			}
			if reason != "" {
				r.output.Declined = append(r.output.Declined, SwarmRotationDecline{Session: session.Name, PaneIndex: pane.Index, AgentType: pane.AgentType, Reason: reason})
			}
		}
	}
	for provider := range providers {
		r.output.Providers = append(r.output.Providers, provider)
	}
	sort.Strings(r.output.Providers)
	if len(unsupportedLaunch) > 0 {
		return r, errors.Join(unsupportedLaunch...)
	}
	if r.output.EligiblePanes == 0 {
		return r, errors.New("no swarm panes support the requested account rotation; inspect account_rotation.declined")
	}
	caamConfig := cfg.Integrations.CAAM
	caamConfig.AutoFailover = true
	caamConfig.FailoverProviders = append([]string(nil), r.output.Providers...)
	caamBinary := caamConfig.BinaryPath
	if strings.TrimSpace(caamBinary) == "" {
		caamBinary = "caam"
	}
	caamBinary, err = exec.LookPath(caamBinary)
	if err != nil {
		return r, fmt.Errorf("locate CAAM: %w", err)
	}
	caamConfig.BinaryPath, err = filepath.Abs(caamBinary)
	if err != nil {
		return r, fmt.Errorf("resolve CAAM path: %w", err)
	}
	if err := swarmPreflightAccountRotation(ctx, caamConfig); err != nil {
		return r, err
	}
	r.options = resilience.RotationMonitorOptions{ForceGlobalAuthClobber: opts.ForceGlobalAuth, Providers: r.output.Providers, CAAMBinary: caamConfig.BinaryPath, ResetHorizonMinutes: caamConfig.ResetHorizonMinutes, PollSeconds: 5}
	for _, session := range plan.Sessions {
		for _, pane := range session.Panes {
			if !r.eligible[swarmRotationPaneKey(session.Name, pane.Index)] {
				continue
			}
			project, _ := filepath.Abs(pane.Project)
			if err := swarmPreflightSessionMonitor(resilience.SpawnMonitorRequest{Session: session.Name, ProjectDir: project, ConfigPath: r.configPath, AccountRotation: &r.options}); err != nil {
				return r, err
			}
			break
		}
	}
	return r, nil
}

func swarmRotationCredentialRestriction(provider string) string {
	if strings.TrimSpace(os.Getenv("SHALLOW_PROFILE")) != "" {
		return "CAAM profile-bound launches require cross-profile transcript recovery, which is not supported"
	}
	keys := []string{"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL"}
	if provider == "claude" {
		if cfg.Agents.ClaudeIsolateCredentials || strings.TrimSpace(cfg.Agents.ClaudeTokenFile) != "" {
			return "Claude isolated credentials and setup-token launches cannot use global account rotation"
		}
		keys = []string{"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	}
	for _, key := range keys {
		if os.Getenv(key) != "" {
			return fmt.Sprintf("%s overrides global credentials; account rotation cannot verify effective account replacement", key)
		}
	}
	return ""
}

func (r *swarmRotationPlan) launchSpec(session string, pane swarm.PaneSpec) (*tmux.AgentLaunchSpec, error) {
	key := swarmRotationPaneKey(session, pane.Index)
	eligible, ok := r.eligible[key]
	if !ok {
		return nil, errors.New("pane was not in the preflighted account rotation plan")
	}
	if !eligible {
		return nil, nil
	}
	spec, ok := r.specs[key]
	if !ok {
		return nil, errors.New("eligible pane has no preflighted account rotation command")
	}
	return &spec, nil
}

func (r *swarmRotationPlan) startMonitors(ctx context.Context, launches *swarm.BatchLaunchResult) error {
	bySession := make(map[string][]resilience.AgentConfig)
	for _, pane := range launches.Results {
		if !pane.Success || !r.eligible[swarmRotationPaneKey(pane.SessionName, pane.PaneIndex)] {
			continue
		}
		project, err := filepath.Abs(pane.Project)
		if err != nil {
			return fmt.Errorf("resolve launched pane project: %w", err)
		}
		spec := r.specs[swarmRotationPaneKey(pane.SessionName, pane.PaneIndex)]
		bySession[pane.SessionName] = append(bySession[pane.SessionName], resilience.AgentConfig{PaneID: pane.PaneTarget, PaneIndex: pane.PaneIndex, Type: pane.AgentType, Model: spec.Model, Command: spec.Command, ProjectDir: project})
	}
	sessions := make([]string, 0, len(bySession))
	for session := range bySession {
		sessions = append(sessions, session)
	}
	sort.Strings(sessions)
	var errs []error
	for _, session := range sessions {
		agents := bySession[session]
		receipt := SwarmRotationMonitorOutput{Session: session, PaneIDs: make([]string, 0, len(agents))}
		for _, agent := range agents {
			receipt.PaneIDs = append(receipt.PaneIDs, agent.PaneID)
		}
		identity, err := accountRotationSessionIdentity(ctx, session)
		var result *resilience.SpawnMonitorResult
		if err == nil {
			result, err = swarmStartSessionMonitor(ctx, resilience.SpawnMonitorRequest{Session: session, ProjectDir: agents[0].ProjectDir, Agents: agents, ConfigPath: r.configPath, AccountRotation: &r.options, SessionIdentity: identity})
		}
		if result != nil {
			receipt.Started, receipt.PID, receipt.Generation = result.MonitorStarted, result.MonitorPID, result.Generation
		}
		if err == nil && (!receipt.Started || receipt.PID <= 0 || receipt.Generation == "") {
			err = errors.New("resident monitor did not acknowledge startup")
		}
		if err != nil {
			receipt.Started = false
			receipt.Error = err.Error()
			errs = append(errs, fmt.Errorf("account rotation monitor for %s: %w", session, err))
		}
		r.output.Monitors = append(r.output.Monitors, receipt)
	}
	return errors.Join(errs...)
}
