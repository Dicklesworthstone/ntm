package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	var ccCount, codCount, gmiCount int

	cmd := &cobra.Command{
		Use:   "add <session-name>",
		Short: "Add more agents to an existing session",
		Long: `Add additional AI agents to an existing tmux session.

Examples:
  ntm add myproject --cc=2           # Add 2 Claude agents
  ntm add myproject --cod=1 --gmi=1  # Add 1 Codex, 1 Gemini`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(args[0], ccCount, codCount, gmiCount)
		},
	}

	cmd.Flags().IntVar(&ccCount, "cc", 0, "number of Claude agents to add")
	cmd.Flags().IntVar(&codCount, "cod", 0, "number of Codex agents to add")
	cmd.Flags().IntVar(&gmiCount, "gmi", 0, "number of Gemini agents to add")

	return cmd
}

func runAdd(session string, ccCount, codCount, gmiCount int) error {
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

	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified (use --cc, --cod, or --gmi)"))
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

	maxCC, maxCod, maxGmi := 0, 0, 0

	// Helper to parse index from title (e.g., "myproject__cc_2" -> 2)
	parseIndex := func(title, suffix string) int {
		if idx := strings.LastIndex(title, suffix); idx != -1 {
			numPart := title[idx+len(suffix):]
			// Handle variants (e.g., "2_opus")
			if split := strings.SplitN(numPart, "_", 2); len(split) > 0 {
				if val, err := strconv.Atoi(split[0]); err == nil {
					return val
				}
			}
		}
		return 0
	}

	for _, p := range panes {
		if idx := parseIndex(p.Title, fmt.Sprintf("__%s_", tmux.AgentClaude)); idx > maxCC {
			maxCC = idx
		}
		if idx := parseIndex(p.Title, fmt.Sprintf("__%s_", tmux.AgentCodex)); idx > maxCod {
			maxCod = idx
		}
		if idx := parseIndex(p.Title, fmt.Sprintf("__%s_", tmux.AgentGemini)); idx > maxGmi {
			maxGmi = idx
		}
	}

	// Add Claude agents
	for i := 0; i < ccCount; i++ {
		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		num := maxCC + i + 1
		title := fmt.Sprintf("%s__cc_%d", session, num)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command using template (default model)
		agentCmd, err := config.GenerateAgentCommand(cfg.Agents.Claude, config.AgentTemplateVars{
			Model:       cfg.Models.DefaultClaude,
			SessionName: session,
			PaneIndex:   num,
			AgentType:   "cc",
			ProjectDir:  dir,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for Claude agent: %w", err))
		}

		cmd := fmt.Sprintf("cd %q && %s", dir, agentCmd)
		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}

		// Track for JSON output
		newPanes = append(newPanes, output.PaneResponse{
			Title:   title,
			Type:    "claude",
			Command: cmd,
		})
	}

	// Add Codex agents
	for i := 0; i < codCount; i++ {
		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		num := maxCod + i + 1
		title := fmt.Sprintf("%s__cod_%d", session, num)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command using template (default model)
		codexCmd, err := config.GenerateAgentCommand(cfg.Agents.Codex, config.AgentTemplateVars{
			Model:       cfg.Models.DefaultCodex,
			SessionName: session,
			PaneIndex:   num,
			AgentType:   "cod",
			ProjectDir:  dir,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for Codex agent: %w", err))
		}

		cmd := fmt.Sprintf("cd %q && %s", dir, codexCmd)
		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}

		// Track for JSON output
		newPanes = append(newPanes, output.PaneResponse{
			Title:   title,
			Type:    "codex",
			Command: cmd,
		})
	}

	// Add Gemini agents
	for i := 0; i < gmiCount; i++ {
		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		num := maxGmi + i + 1
		title := fmt.Sprintf("%s__gmi_%d", session, num)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command using template (default model)
		geminiCmd, err := config.GenerateAgentCommand(cfg.Agents.Gemini, config.AgentTemplateVars{
			Model:       cfg.Models.DefaultGemini,
			SessionName: session,
			PaneIndex:   num,
			AgentType:   "gmi",
			ProjectDir:  dir,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for Gemini agent: %w", err))
		}

		cmd := fmt.Sprintf("cd %q && %s", dir, geminiCmd)
		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}

		// Track for JSON output
		newPanes = append(newPanes, output.PaneResponse{
			Title:   title,
			Type:    "gemini",
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

	fmt.Printf("✓ Added %dx cc, %dx cod, %dx gmi\n", ccCount, codCount, gmiCount)
	return nil
}