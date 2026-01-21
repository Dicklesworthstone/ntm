package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func newWatchCmd() *cobra.Command {
	var (
		filterClaude bool
		filterCodex  bool
		filterGemini bool
		filterPane   string
		activityOnly bool
		tailLines    int
		noColor      bool
		noTimestamps bool
		pollInterval int
		watchPattern string
		watchCommand string
	)

	cmd := &cobra.Command{
		Use:     "watch [session-name]",
		Aliases: []string{"w"},
		Short:   "Stream agent output or watch files to trigger commands",
		Long: `Watch mode streams agent output or monitors files.

1. Stream output (default):
   Monitor agent activity without attaching to the tmux session.

2. File watcher:
   Monitor file changes and trigger commands in the session.

Examples:
  ntm watch myproject              # Stream all panes
  ntm watch myproject --cc         # Only Claude agents
  ntm watch myproject --pattern="*.go" --command="go test ./..." # Run tests on change`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			opts := watchOptions{
				filterClaude: filterClaude,
				filterCodex:  filterCodex,
				filterGemini: filterGemini,
				filterPane:   filterPane,
				activityOnly: activityOnly,
				tailLines:    tailLines,
				noColor:      noColor,
				noTimestamps: noTimestamps,
				pollInterval: time.Duration(pollInterval) * time.Millisecond,
				watchPattern: watchPattern,
				watchCommand: watchCommand,
			}

			return runWatch(session, opts)
		},
	}

	cmd.Flags().BoolVar(&filterClaude, "cc", false, "Only watch Claude agents")
	cmd.Flags().BoolVar(&filterCodex, "cod", false, "Only watch Codex agents")
	cmd.Flags().BoolVar(&filterGemini, "gmi", false, "Only watch Gemini agents")
	cmd.Flags().StringVar(&filterPane, "pane", "", "Watch specific pane (by name or index)")
	cmd.Flags().BoolVar(&activityOnly, "activity", false, "Only print when new output appears")
	cmd.Flags().IntVar(&tailLines, "tail", 20, "Start with last N lines of output")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colors")
	cmd.Flags().BoolVar(&noTimestamps, "no-timestamps", false, "Disable timestamps")
	cmd.Flags().IntVar(&pollInterval, "interval", 250, "Poll interval in milliseconds")
	cmd.Flags().StringVar(&watchPattern, "pattern", "", "File pattern to watch (e.g. '*.go')")
	cmd.Flags().StringVar(&watchCommand, "command", "", "Command to send to agent on change")

	return cmd
}

type watchOptions struct {
	filterClaude bool
	filterCodex  bool
	filterGemini bool
	filterPane   string
	activityOnly bool
	tailLines    int
	noColor      bool
	noTimestamps bool
	pollInterval time.Duration
	watchPattern string
	watchCommand string
}

func runWatch(session string, opts watchOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, os.Stdout)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\nWatch mode stopped.")
		cancel()
	}()

	// Get theme for colors
	t := theme.Current()

	// File watching mode
	if opts.watchPattern != "" {
		return runFileWatch(ctx, session, opts, t)
	}

	// Start watching
	return watchLoop(ctx, session, opts, t)
}

// paneState tracks the state of a pane for diffing
type paneState struct {
	lastOutput string
}

func watchLoop(ctx context.Context, session string, opts watchOptions, t theme.Theme) error {
	paneStates := make(map[string]*paneState)
	firstRun := true

	// Print header
	if !opts.noColor {
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Blue).
			Render(fmt.Sprintf("Watching session: %s", session))
		fmt.Printf("\n%s\n", header)
		fmt.Println(lipgloss.NewStyle().Foreground(t.Overlay).Render("Press Ctrl+C to stop\n"))
	} else {
		fmt.Printf("\nWatching session: %s\n", session)
		fmt.Println("Press Ctrl+C to stop")
	}

	ticker := time.NewTicker(opts.pollInterval)
	defer ticker.Stop()

	for {
		// Initial run proceeds immediately; subsequent runs wait for the poll interval.
		if !firstRun {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		}

		// Get panes
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return fmt.Errorf("failed to get panes: %w", err)
		}

		// Filter panes
		filteredPanes := filterPanes(panes, opts)

		// Process each pane
		for _, pane := range filteredPanes {
			paneKey := pane.ID

			// Initialize state if needed
			if paneStates[paneKey] == nil {
				paneStates[paneKey] = &paneState{}
			}
			state := paneStates[paneKey]

			// Capture output
			lines := opts.tailLines
			if !firstRun {
				lines = 100 // Capture more to find diff
			}
			output, err := tmux.CapturePaneOutput(pane.ID, lines)
			if err != nil {
				if opts.activityOnly {
					continue
				}
				return fmt.Errorf("failed to capture pane output: %w", err)
			}

			// Compute diff
			newOutput := computeDiff(state.lastOutput, output)
			if newOutput == "" && opts.activityOnly {
				continue
			}

			// On first run, show tail
			if firstRun && output != "" {
				printPaneOutput(pane, output, opts, t)
				state.lastOutput = output
				continue
			}

			// Print new output if any
			if newOutput != "" {
				printPaneOutput(pane, newOutput, opts, t)
				state.lastOutput = output
			}
		}

		firstRun = false
	}
}

func filterPanes(panes []tmux.Pane, opts watchOptions) []tmux.Pane {
	// If no filters, return all panes
	if !opts.filterClaude && !opts.filterCodex && !opts.filterGemini && opts.filterPane == "" {
		return panes
	}

	var filtered []tmux.Pane
	for _, p := range panes {
		// Filter by specific pane
		if opts.filterPane != "" {
			if p.Title == opts.filterPane || fmt.Sprintf("%d", p.Index) == opts.filterPane {
				filtered = append(filtered, p)
			}
			continue
		}

		// Filter by agent type
		if opts.filterClaude && p.Type == tmux.AgentClaude {
			filtered = append(filtered, p)
		}
		if opts.filterCodex && p.Type == tmux.AgentCodex {
			filtered = append(filtered, p)
		}
		if opts.filterGemini && p.Type == tmux.AgentGemini {
			filtered = append(filtered, p)
		}
	}

	return filtered
}

func computeDiff(old, new string) string {
	if old == "" {
		return new
	}

	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	// Find where old output ends in new output
	// Look for the last line of old output in new output
	if len(oldLines) == 0 {
		return new
	}

	lastOld := strings.TrimSpace(oldLines[len(oldLines)-1])
	if lastOld == "" && len(oldLines) > 1 {
		lastOld = strings.TrimSpace(oldLines[len(oldLines)-2])
	}

	// Find this line in new output
	startIdx := -1
	for i := len(newLines) - 1; i >= 0; i-- {
		if strings.TrimSpace(newLines[i]) == lastOld {
			startIdx = i + 1
			break
		}
	}

	if startIdx < 0 || startIdx >= len(newLines) {
		// Couldn't find overlap, just check if output changed
		if old == new {
			return ""
		}
		// Return all new content
		return new
	}

	// Return only new lines
	newContent := strings.Join(newLines[startIdx:], "\n")
	return strings.TrimRight(newContent, "\n")
}

func printPaneOutput(pane tmux.Pane, output string, opts watchOptions, t theme.Theme) {
	if output == "" {
		return
	}

	// Create prefix style based on agent type
	var prefixColor lipgloss.Color
	switch pane.Type {
	case tmux.AgentClaude:
		prefixColor = t.Claude
	case tmux.AgentCodex:
		prefixColor = t.Codex
	case tmux.AgentGemini:
		prefixColor = t.Gemini
	default:
		prefixColor = t.Green
	}

	// Build prefix
	prefix := pane.Title
	if prefix == "" {
		prefix = fmt.Sprintf("pane-%d", pane.Index)
	}

	timestamp := ""
	if !opts.noTimestamps {
		timestamp = time.Now().Format("15:04:05")
	}

	// Print each line with prefix
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		if opts.noColor {
			if timestamp != "" {
				fmt.Printf("[%s %s] %s\n", prefix, timestamp, line)
			} else {
				fmt.Printf("[%s] %s\n", prefix, line)
			}
		} else {
			prefixStyle := lipgloss.NewStyle().
				Foreground(prefixColor).
				Bold(true)

			timeStyle := lipgloss.NewStyle().
				Foreground(t.Overlay)

			if timestamp != "" {
				fmt.Printf("%s %s %s\n",
					prefixStyle.Render(fmt.Sprintf("[%s", prefix)),
					timeStyle.Render(timestamp+"]"),
					line)
			} else {
				fmt.Printf("%s %s\n",
					prefixStyle.Render(fmt.Sprintf("[%s]", prefix)),
					line)
			}
		}
	}
}

func runFileWatch(ctx context.Context, session string, opts watchOptions, t theme.Theme) error {
	if opts.watchCommand == "" {
		return fmt.Errorf("--command is required with --pattern")
	}

	fmt.Printf("\nWatching files matching '%s' in current directory...\n", opts.watchPattern)
	fmt.Printf("Will run command: %s\n", opts.watchCommand)
	fmt.Println("Press Ctrl+C to stop")

	handler := func(events []watcher.Event) {
		matched := false
		for _, e := range events {
			// Check if pattern contains path separators
			if strings.Contains(opts.watchPattern, string(os.PathSeparator)) || strings.Contains(opts.watchPattern, "/") {
				// Match against relative path
				rel, err := filepath.Rel(".", e.Path)
				if err == nil {
					if m, _ := filepath.Match(opts.watchPattern, rel); m {
						matched = true
					}
				}
			} else {
				// Match against base name
				name := filepath.Base(e.Path)
				if m, _ := filepath.Match(opts.watchPattern, name); m {
					matched = true
				}
			}

			if matched {
				if !opts.noColor {
					fmt.Printf("File changed: %s\n", e.Path)
				}
				break
			}
		}

		if matched {
			// Determine targets
			panes, err := tmux.GetPanes(session)
			if err != nil {
				fmt.Printf("Error getting panes: %v\n", err)
				return
			}

			targets := filterPanes(panes, opts)
			if len(targets) == 0 {
				fmt.Println("No target agents found")
				return
			}

			fmt.Printf("Triggering command on %d pane(s)...\n", len(targets))
			for _, p := range targets {
				if err := sendPromptToPane(p, opts.watchCommand); err != nil {
					fmt.Printf("Failed to send to pane %s: %v\n", p.ID, err)
				}
			}
		}
	}

	w, err := watcher.New(handler,
		watcher.WithRecursive(true),
		watcher.WithPollInterval(opts.pollInterval),
		watcher.WithIgnorePaths([]string{
			".git",
			"node_modules",
			"dist",
			"build",
			"vendor",
			"coverage",
			"__pycache__",
			".venv",
			".idea",
			".vscode",
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer w.Close()

	if err := w.Add("."); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	<-ctx.Done()
	return nil
}
