package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type restartLaunchPlan struct {
	Commands map[string]string
	Affinity map[string]resilience.LaunchAffinity
}

func prepareRestartLaunchPlan(
	ctx context.Context,
	session string,
	panes []tmux.Pane,
	multiWindow bool,
	cfg *config.Config,
	override restartLaunchOverride,
	deps RestartPaneDependencies,
) (restartLaunchPlan, error) {
	plan := restartLaunchPlan{
		Commands: make(map[string]string),
		Affinity: make(map[string]resilience.LaunchAffinity),
	}
	manifest, err := deps.LoadManifest(session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return plan, fmt.Errorf("load launch affinity manifest: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		manifest = nil
	}
	caamBinary := ""
	if cfg != nil {
		caamBinary = cfg.Integrations.CAAM.BinaryPath
	}
	for _, pane := range panes {
		resolvedType := restartPaneAgentType(pane)
		if !restartTargetIsAgent(resolvedType) {
			continue
		}
		key := paneTargetKey(pane, multiWindow)
		command, err := restartAgentLaunchCommandWithOverride(cfg, resolvedType, pane.Variant, override)
		if err != nil {
			return plan, fmt.Errorf("compose relaunch command for pane %s: %w", key, err)
		}
		binding := restartLaunchBindingForPane(manifest, pane, resolvedType)
		prepared, affinity, err := deps.PrepareLaunchCommand(ctx, resolvedType, caamBinary, binding, command)
		if err != nil {
			return plan, fmt.Errorf("preflight pane %s launch affinity: %w", key, err)
		}
		plan.Commands[key] = prepared
		plan.Affinity[key] = affinity
	}
	return plan, nil
}

func restartLaunchBindingForPane(
	manifest *resilience.SpawnManifest,
	pane tmux.Pane,
	resolvedType string,
) *resilience.LaunchBinding {
	if manifest == nil {
		return nil
	}
	for i := range manifest.Agents {
		if manifest.Agents[i].PaneID == pane.ID {
			return resilience.CloneLaunchBinding(manifest.Agents[i].LaunchBinding)
		}
	}

	logicalIndex := pane.NTMIndex
	if logicalIndex <= 0 {
		logicalIndex = pane.Index
	}
	canonicalType := string(agentpkg.AgentType(resolvedType).Canonical())
	var candidate *resilience.LaunchBinding
	found := false
	for i := range manifest.Agents {
		entry := &manifest.Agents[i]
		if entry.PaneIndex != logicalIndex ||
			string(agentpkg.AgentType(entry.Type).Canonical()) != canonicalType {
			continue
		}
		if pane.Variant != "" && entry.Model != "" && !strings.EqualFold(pane.Variant, entry.Model) {
			continue
		}
		if found {
			return nil
		}
		found = true
		candidate = entry.LaunchBinding
	}
	return resilience.CloneLaunchBinding(candidate)
}
