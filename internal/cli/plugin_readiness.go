package cli

import (
	"log/slog"

	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

// registerAgentPluginTypes makes every agent plugin in dir known to the
// classification layer: the plugin's name (and alias) becomes a recognised
// agent type for status, --robot-tail, send targeting, and deps, and its
// declared readiness patterns drive idle/working/error detection (ntm#260).
// It runs for every command from the root pre-run, so robot flags and
// subcommands alike see plugin panes as their own type rather than
// "unknown". A missing directory is not an error. Returns the loaded plugins
// so callers that need the list (deps, send) can reuse them.
func registerAgentPluginTypes(dir string) []plugins.AgentPlugin {
	loaded, err := plugins.LoadAgentPlugins(dir)
	if err != nil {
		slog.Debug("agent plugins unavailable", "dir", dir, "error", err)
		return nil
	}
	for _, p := range loaded {
		for _, name := range []string{p.Name, p.Alias} {
			if name == "" {
				continue
			}
			if err := agentpkg.RegisterPlugin(name, p.Readiness.IdlePatterns, p.Readiness.WorkingPatterns, p.Readiness.ErrorPatterns); err != nil {
				slog.Warn("agent plugin readiness patterns rejected; pane classification falls back to generic heuristics",
					"plugin", p.Name, "error", err)
				// The type itself must still be recognised (status, robot,
				// send, deps) even when its patterns are unusable.
				_ = agentpkg.RegisterPlugin(name, nil, nil, nil)
			}
		}
	}
	return loaded
}

// registerPluginSendFlags exposes --<plugin> (and --<alias>) on `ntm send` as
// type selectors, mirroring registerPluginAgentFlags on spawn/add. Both flags
// target the plugin's canonical type (its name), which is what pane titles
// carry. Built-in flags always win a name collision.
func registerPluginSendFlags(cmd *cobra.Command, p plugins.AgentPlugin, targets *SendTargets) {
	agentType := AgentType(p.Name)
	for _, flag := range []string{p.Name, p.Alias} {
		if flag == "" {
			continue
		}
		if cmd.Flags().Lookup(flag) != nil {
			slog.Warn("skipping agent plugin send flag that collides with an existing flag; keeping built-in",
				"plugin", p.Name, "flag", "--"+flag)
			continue
		}
		cmd.Flags().Var(newSendTargetValue(agentType, targets), flag, "send to "+p.Name+" agents (plugin; optional :variant filter)")
		cmd.Flags().Lookup(flag).NoOptDefVal = "true"
	}
}

// pluginDepChecks turns loaded agent plugins into `ntm deps` probes so a
// plugin agent shows up in the AI Agents section and counts as available
// when its executable is on PATH (ntm#260).
func pluginDepChecks(loaded []plugins.AgentPlugin) []depCheck {
	var checks []depCheck
	for _, p := range loaded {
		exe := p.ProbeCommand()
		if exe == "" {
			continue
		}
		checks = append(checks, depCheck{
			Name:        p.Name + " (plugin)",
			Command:     exe,
			VersionArgs: []string{"--version"},
			Required:    false,
			Category:    "AI Agents",
			InstallHint: "install `" + exe + "` (declared by agent plugin " + p.Name + ")",
		})
	}
	return checks
}
