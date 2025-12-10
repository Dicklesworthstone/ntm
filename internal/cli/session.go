package cli

import (
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "attach <session-name>",
		Aliases: []string{"a"},
		Short:   "Attach to a tmux session",
		Long: `Attach to an existing tmux session. If already inside tmux,
switches to the target session instead.

If the session doesn't exist, shows available sessions.

Examples:
  ntm attach myproject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// No session specified, list sessions
				return runList()
			}
			return runAttach(args[0])
		},
	}
}

func runAttach(session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if tmux.SessionExists(session) {
		return tmux.AttachOrSwitch(session)
	}

	fmt.Printf("Session '%s' does not exist.\n\n", session)
	fmt.Println("Available sessions:")
	if err := runList(); err != nil {
		return err
	}
	fmt.Println()

	if confirm(fmt.Sprintf("Create '%s' with default settings?", session)) {
		return runCreate(session, 0)
	}

	return nil
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "l"},
		Short:   "List all tmux sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList()
		},
	}
}

func runList() error {
	if err := tmux.EnsureInstalled(); err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	sessions, err := tmux.ListSessions()
	if err != nil {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	// Build response for JSON output
	if IsJSONOutput() {
		items := make([]output.SessionListItem, len(sessions))
		for i, s := range sessions {
			item := output.SessionListItem{
				Name:             s.Name,
				Windows:          s.Windows,
				Attached:         s.Attached,
				WorkingDirectory: s.Directory,
			}

			// Get panes to count agents
			panes, err := tmux.GetPanes(s.Name)
			if err == nil {
				item.PaneCount = len(panes)

				// Count agent types
				var claudeCount, codexCount, geminiCount, userCount int
				for _, p := range panes {
					switch p.Type {
					case tmux.AgentClaude:
						claudeCount++
					case tmux.AgentCodex:
						codexCount++
					case tmux.AgentGemini:
						geminiCount++
					default:
						userCount++
					}
				}
				item.AgentCounts = &output.AgentCountsResponse{
					Claude: claudeCount,
					Codex:  codexCount,
					Gemini: geminiCount,
					User:   userCount,
					Total:  len(panes),
				}
			}
			items[i] = item
		}
		resp := output.ListResponse{
			TimestampedResponse: output.NewTimestamped(),
			Sessions:            items,
			Count:               len(sessions),
		}
		return output.PrintJSON(resp)
	}

	// Text output
	if len(sessions) == 0 {
		fmt.Println("No tmux sessions running")
		return nil
	}

	for _, s := range sessions {
		attached := ""
		if s.Attached {
			attached = " (attached)"
		}
		fmt.Printf("  %s: %d windows%s\n", s.Name, s.Windows, attached)
	}

	return nil
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <session-name>",
		Short: "Show detailed status of a session",
		Long: `Show detailed information about a session including:
- All panes with their titles and current commands
- Agent type counts (Claude, Codex, Gemini)
- Session directory

Examples:
  ntm status myproject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(args[0])
		},
	}
}

func runStatus(session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	dir := cfg.GetProjectDir(session)

	// Use theme colors
	t := theme.Current()
	ic := icons.Current()

	// Convert theme colors to ANSI
	primary := colorize(t.Primary)
	claude := colorize(t.Claude)
	codex := colorize(t.Codex)
	gemini := colorize(t.Gemini)
	success := colorize(t.Success)
	text := colorize(t.Text)
	subtext := colorize(t.Subtext)
	overlay := colorize(t.Overlay)
	surface := colorize(t.Surface2)

	const reset = "\033[0m"
	const bold = "\033[1m"

	fmt.Println()

	// Header with icon
	fmt.Printf("  %s%s%s %s%s%s%s\n", primary, ic.Session, reset, bold, session, reset, text)
	fmt.Printf("  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)
	fmt.Println()

	// Directory info
	fmt.Printf("  %s%s Directory:%s %s%s%s\n", subtext, ic.Folder, reset, text, dir, reset)
	fmt.Printf("  %s%s Panes:%s    %s%d%s\n", subtext, ic.Pane, reset, text, len(panes), reset)
	fmt.Println()

	// Panes section
	fmt.Printf("  %sPanes%s\n", bold, reset)
	fmt.Printf("  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)

	ccCount, codCount, gmiCount, otherCount := 0, 0, 0, 0

	for i, p := range panes {
		var typeColor, typeIcon string
		switch p.Type {
		case tmux.AgentClaude:
			typeColor = claude
			typeIcon = ic.Claude
			ccCount++
		case tmux.AgentCodex:
			typeColor = codex
			typeIcon = ic.Codex
			codCount++
		case tmux.AgentGemini:
			typeColor = gemini
			typeIcon = ic.Gemini
			gmiCount++
		default:
			typeColor = success
			typeIcon = ic.User
			otherCount++
		}

		// Number for quick selection (1-9)
		num := ""
		if i < 9 {
			num = fmt.Sprintf("%s%d%s ", overlay, i+1, reset)
		} else {
			num = "  "
		}

		// Active indicator
		active := "  "
		if p.Active {
			active = fmt.Sprintf("%s%s%s ", primary, ic.Dot, reset)
		}

		// Pane info
		fmt.Printf("  %s%s%s%s %-20s%s %s│%s %s%-12s%s %s│%s %s%dx%d%s\n",
			num,
			active,
			typeColor, typeIcon, p.Title, reset,
			surface, reset,
			subtext, p.Command, reset,
			surface, reset,
			overlay, p.Width, p.Height, reset)
	}

	fmt.Printf("  %s%s%s\n", surface, "─────────────────────────────────────────────────────────", reset)
	fmt.Println()

	// Agent summary with icons
	fmt.Printf("  %sAgents%s\n", bold, reset)

	if ccCount > 0 {
		fmt.Printf("    %s%s Claude%s  %s%d instance(s)%s\n", claude, ic.Claude, reset, text, ccCount, reset)
	}
	if codCount > 0 {
		fmt.Printf("    %s%s Codex%s   %s%d instance(s)%s\n", codex, ic.Codex, reset, text, codCount, reset)
	}
	if gmiCount > 0 {
		fmt.Printf("    %s%s Gemini%s  %s%d instance(s)%s\n", gemini, ic.Gemini, reset, text, gmiCount, reset)
	}
	if otherCount > 0 {
		fmt.Printf("    %s%s User%s    %s%d pane(s)%s\n", success, ic.User, reset, text, otherCount, reset)
	}

	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		fmt.Printf("    %sNo agents running%s\n", overlay, reset)
	}

	fmt.Println()

	// Quick actions hint
	fmt.Printf("  %sQuick actions:%s\n", overlay, reset)
	fmt.Printf("    %sntm send %s --all \"prompt\"%s  %s# Broadcast to all agents%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Printf("    %sntm view %s%s                 %s# Tile all panes%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Printf("    %sntm zoom %s <n>%s             %s# Zoom pane n%s\n",
		subtext, session, reset, overlay, reset)
	fmt.Println()

	return nil
}
