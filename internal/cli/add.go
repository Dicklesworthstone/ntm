package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs

	cmd := &cobra.Command{
		Use:   "add <session-name>",
		Short: "Add more agents to an existing session",
		Long: `Add additional AI agents to an existing tmux session.

		You can specify agent counts and optional model variants:
	  ntm add myproject --cc=2           # Add 2 Claude agents (default model)
	  ntm add myproject --cc=1:opus      # Add 1 Claude Opus agent
	  ntm add myproject --cod=1 --gmi=1  # Add 1 Codex, 1 Gemini

		Persona mode:
	  Use --persona to add agents with predefined roles and system prompts.
	  Built-in personas: architect, implementer, reviewer, tester, documenter
	  ntm add myproject --persona=reviewer  # Add 1 reviewer agent

		Agent count syntax: N or N:model where N is count and model is optional.
		Multiple flags of the same type accumulate.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]
			dir := cfg.GetProjectDir(sessionName)

			// Load plugins (re-load here to ensure latest state and to pass map)
			// Ideally we should share this logic or load once.
			configDir := filepath.Dir(config.DefaultPath())
			pluginsDir := filepath.Join(configDir, "agents")
			loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
			pluginMap := make(map[string]plugins.AgentPlugin)
			for _, p := range loadedPlugins {
				pluginMap[p.Name] = p
				if p.Alias != "" {
					pluginMap[p.Alias] = p
				}
			}

			// Handle personas (they contribute to agentSpecs)
			if len(personaSpecs) > 0 {
				resolved, err := ResolvePersonas(personaSpecs, dir)
				if err != nil {
					return err
				}
				personaAgents := FlattenPersonas(resolved)

				// Add persona agents to agentSpecs with persona name as variant
				for _, pa := range personaAgents {
					agentSpecs = append(agentSpecs, AgentSpec{
						Type:  pa.AgentType,
						Count: 1,
						Model: pa.PersonaName, // Use persona name as variant
					})
				}

				if !IsJSONOutput() {
					fmt.Printf("Resolved %d persona agent(s)\n", len(personaAgents))
				}
			}

			return runAdd(sessionName, agentSpecs, pluginMap)
		},
	}

	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeClaude, &agentSpecs), "cc", "Claude agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCodex, &agentSpecs), "cod", "Codex agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGemini, &agentSpecs), "gmi", "Gemini agents (N or N:model)")
	cmd.Flags().Var(&personaSpecs, "persona", "Persona-defined agents (name or name:count)")

	// Register plugin flags
	configDir := filepath.Dir(config.DefaultPath())
	pluginsDir := filepath.Join(configDir, "agents")
	loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
	for _, p := range loadedPlugins {
		agentType := AgentType(p.Name)
		cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Name, p.Description)
		if p.Alias != "" {
			cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Alias, p.Description+" (alias)")
		}
	}

	return cmd
}

func runAdd(session string, specs AgentSpecs, pluginMap map[string]plugins.AgentPlugin) error {
	totalAgents := specs.TotalCount()
	// Helper for JSON error output
	outputError := func(err error) error {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	if !tmux.SessionExists(session) {
		return outputError(fmt.Errorf("session '%s' does not exist (use 'ntm spawn' to create)", session))
	}

	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified"))
	}

	dir := cfg.GetProjectDir(session)
	if !IsJSONOutput() {
		fmt.Printf("Adding %d agent(s) to session '%s'...\n", totalAgents, session)
	}

	// Auto-checkpoint before adding many agents
	if cfg.Checkpoints.Enabled && cfg.Checkpoints.BeforeAddAgents > 0 && totalAgents >= cfg.Checkpoints.BeforeAddAgents {
		if !IsJSONOutput() {
			fmt.Println("Creating auto-checkpoint before adding agents...")
		}
		autoCP := checkpoint.NewAutoCheckpointer()
		cp, err := autoCP.Create(checkpoint.AutoCheckpointOptions{
			SessionName:     session,
			Reason:          checkpoint.ReasonAddAgents,
			Description:     fmt.Sprintf("before adding %d agents", totalAgents),
			ScrollbackLines: cfg.Checkpoints.ScrollbackLines,
			IncludeGit:      cfg.Checkpoints.IncludeGit,
			MaxCheckpoints:  cfg.Checkpoints.MaxAutoCheckpoints,
		})
		if err != nil {
			// Log warning but continue - auto-checkpoint is best-effort
			if !IsJSONOutput() {
				fmt.Printf("⚠ Auto-checkpoint failed: %v\n", err)
			}
		} else if !IsJSONOutput() {
			fmt.Printf("✓ Auto-checkpoint created: %s\n", cp.ID)
		}
	}

	// Track newly added panes for JSON output
	var newPanes []output.PaneResponse

	// Get existing panes to determine next indices
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}

	maxIndices := make(map[string]int)

	// Helper to parse index from title
	parseIndex := func(title string) {
		parts := strings.Split(title, "__")
		if len(parts) >= 2 {
			// part[1] is like "cc_2" or "cc_2_opus"
			// Split by first underscore to get type
			// But type can contain underscore? No, usually short.
			// Format is {type}_{index} or {type}_{index}_{variant}
			// But type itself might be "claude_code"?
			// Standard types are "cc", "cod", "gmi".
			// Plugin types are user defined.
			// Let's iterate types we know? No, dynamic.
			// We scan for last "_" before digits?
			// Example: "cursor_1" -> type "cursor", index 1.
			// "local_llm_2" -> type "local_llm", index 2?
			// Tmux format: FormatPaneName uses `{type}_{index}` prefix.
			// So `cc_1`. `cursor_1`.
			// We can try to split by `_` and find where the number is.
			sub := parts[1]
			subParts := strings.Split(sub, "_")
			// Iterate to find the index part
			for i, p := range subParts {
				if num, err := strconv.Atoi(p); err == nil && num > 0 {
					// Found the index. Everything before is type.
					// Everything after is variant.
					typeStr := strings.Join(subParts[:i], "_")
					if num > maxIndices[typeStr] {
						maxIndices[typeStr] = num
					}
					break
				}
			}
		}
	}

	for _, p := range panes {
		parseIndex(p.Title)
	}

	// Add agents
	flatAgents := specs.Flatten()
	ccCount, codCount, gmiCount := 0, 0, 0

	for _, agent := range flatAgents {
		agentTypeStr := string(agent.Type)
		
		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		// Increment index for this type
		maxIndices[agentTypeStr]++
		num := maxIndices[agentTypeStr]
		
		title := tmux.FormatPaneName(session, agentTypeStr, num, agent.Model)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command
		var agentCmd string
		var envVars map[string]string
		
		switch agent.Type {
		case AgentTypeClaude:
			agentCmd = cfg.Agents.Claude
			ccCount++
		case AgentTypeCodex:
			agentCmd = cfg.Agents.Codex
			codCount++
		case AgentTypeGemini:
			agentCmd = cfg.Agents.Gemini
			gmiCount++
		default:
			if p, ok := pluginMap[agentTypeStr]; ok {
				agentCmd = p.Command
				envVars = p.Env
			} else {
				return outputError(fmt.Errorf("unknown agent type: %s", agent.Type))
			}
		}

		// Resolve model alias to full model name
		model := ResolveModel(agent.Type, agent.Model)
		
		finalCmd, err := config.GenerateAgentCommand(agentCmd, config.AgentTemplateVars{
			Model:       model,
			SessionName: session,
			PaneIndex:   num,
			AgentType:   agentTypeStr,
			ProjectDir:  dir,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}
		
		// Apply plugin env vars
		if len(envVars) > 0 {
			var envPrefix string
			for k, v := range envVars {
				envPrefix += fmt.Sprintf("%s=%q ", k, v)
			}
			finalCmd = envPrefix + finalCmd
		}

		safeCmd, err := tmux.SanitizePaneCommand(finalCmd)
		if err != nil {
			return outputError(fmt.Errorf("invalid agent command: %w", err))
		}

		cmd, err := tmux.BuildPaneCommand(dir, safeCmd)
		if err != nil {
			return outputError(fmt.Errorf("building agent command: %w", err))
		}

		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}

		// Track for JSON output
		newPanes = append(newPanes, output.PaneResponse{
			Title:   title,
			Type:    agentTypeStr,
			Variant: agent.Model,
			Command: cmd,
		})
	}

	// JSON output mode
	if IsJSONOutput() {
		return output.PrintJSON(output.AddResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			AddedClaude:         ccCount,
			AddedCodex:          codCount,
			AddedGemini:         gmiCount,
			TotalAdded:          totalAgents,
			NewPanes:            newPanes,
		})
	}

	fmt.Printf("✓ Added %d agent(s) (total %d panes now)\n", totalAgents, len(panes)+totalAgents)
	return nil
}