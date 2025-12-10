package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var ccCount, codCount, gmiCount int
	var noUserPane bool

	cmd := &cobra.Command{
		Use:   "spawn <session-name>",
		Short: "Create session and spawn AI agents in panes",
		Long: `Create a new tmux session and launch AI coding agents in separate panes.

By default, the first pane is reserved for the user. Agent panes are created
and titled with their type (e.g., myproject__cc_1, myproject__cod_1).

Examples:
  ntm spawn myproject --cc=2 --cod=2           # 2 Claude, 2 Codex + user pane
  ntm spawn myproject --cc=3 --cod=3 --gmi=1   # 3 Claude, 3 Codex, 1 Gemini
  ntm spawn myproject --cc=4 --no-user         # 4 Claude, no user pane`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpawn(args[0], ccCount, codCount, gmiCount, !noUserPane)
		},
	}

	cmd.Flags().IntVar(&ccCount, "cc", 0, "number of Claude agents")
	cmd.Flags().IntVar(&codCount, "cod", 0, "number of Codex agents")
	cmd.Flags().IntVar(&gmiCount, "gmi", 0, "number of Gemini agents")
	cmd.Flags().BoolVar(&noUserPane, "no-user", false, "don't reserve a pane for the user")

	return cmd
}

func runSpawn(session string, ccCount, codCount, gmiCount int, userPane bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if err := tmux.ValidateSessionName(session); err != nil {
		return err
	}

	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		return fmt.Errorf("no agents specified (use --cc, --cod, or --gmi)")
	}

	dir := cfg.GetProjectDir(session)

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Printf("Directory not found: %s\n", dir)
		if !confirm("Create it?") {
			fmt.Println("Aborted.")
			return nil
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
		fmt.Printf("Created %s\n", dir)
	}

	// Calculate total panes needed
	totalPanes := totalAgents
	if userPane {
		totalPanes++
	}

	// Create or use existing session
	if !tmux.SessionExists(session) {
		fmt.Printf("Creating session '%s' in %s...\n", session, dir)
		if err := tmux.CreateSession(session, dir); err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
	}

	// Get current pane count
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}
	existingPanes := len(panes)

	// Add more panes if needed
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		fmt.Printf("Creating %d pane(s) (%d -> %d)...\n", toAdd, existingPanes, totalPanes)
		for i := 0; i < toAdd; i++ {
			if _, err := tmux.SplitWindow(session, dir); err != nil {
				return fmt.Errorf("creating pane: %w", err)
			}
		}
	}

	// Get updated pane list
	panes, err = tmux.GetPanes(session)
	if err != nil {
		return err
	}

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if userPane {
		startIdx = 1
	}

	agentNum := startIdx
	fmt.Printf("Launching agents: %dx cc, %dx cod, %dx gmi...\n", ccCount, codCount, gmiCount)

	// Launch Claude agents
	for i := 0; i < ccCount && agentNum < len(panes); i++ {
		pane := panes[agentNum]
		title := fmt.Sprintf("%s__cc_%d", session, i+1)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			return fmt.Errorf("setting pane title: %w", err)
		}
		cmd := fmt.Sprintf("cd %q && %s", dir, cfg.Agents.Claude)
		if err := tmux.SendKeys(pane.ID, cmd, true); err != nil {
			return fmt.Errorf("launching claude agent: %w", err)
		}
		agentNum++
	}

	// Launch Codex agents
	for i := 0; i < codCount && agentNum < len(panes); i++ {
		pane := panes[agentNum]
		title := fmt.Sprintf("%s__cod_%d", session, i+1)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			return fmt.Errorf("setting pane title: %w", err)
		}
		cmd := fmt.Sprintf("cd %q && %s", dir, cfg.Agents.Codex)
		if err := tmux.SendKeys(pane.ID, cmd, true); err != nil {
			return fmt.Errorf("launching codex agent: %w", err)
		}
		agentNum++
	}

	// Launch Gemini agents
	for i := 0; i < gmiCount && agentNum < len(panes); i++ {
		pane := panes[agentNum]
		title := fmt.Sprintf("%s__gmi_%d", session, i+1)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			return fmt.Errorf("setting pane title: %w", err)
		}
		cmd := fmt.Sprintf("cd %q && %s", dir, cfg.Agents.Gemini)
		if err := tmux.SendKeys(pane.ID, cmd, true); err != nil {
			return fmt.Errorf("launching gemini agent: %w", err)
		}
		agentNum++
	}

	fmt.Printf("✓ Launched %d agent(s)\n", totalAgents)

	// Register session as Agent Mail agent (non-blocking)
	registerSessionAgent(session, dir)

	return tmux.AttachOrSwitch(session)
}

// registerSessionAgent registers the session with Agent Mail.
// This is non-blocking and logs but does not fail if unavailable.
func registerSessionAgent(sessionName, workingDir string) {
	client := agentmail.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.RegisterSessionAgent(ctx, sessionName, workingDir)
	if err != nil {
		// Log but don't fail
		fmt.Printf("⚠ Agent Mail registration failed: %v\n", err)
		return
	}
	if info != nil {
		fmt.Printf("✓ Registered with Agent Mail as %s\n", info.AgentName)
	}
}
