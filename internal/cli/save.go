package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newSaveCmd() *cobra.Command {
	var (
		outputDir string
		lines     int
		allFlag   bool
		ccFlag    bool
		codFlag   bool
		gmiFlag   bool
	)

	cmd := &cobra.Command{
		Use:     "save [session-name]",
		Aliases: []string{"dump", "export"},
		Short:   "Save pane outputs to files",
		Long: `Save the output from session panes to individual files.

Creates timestamped files for each pane in the output directory.
Files are named: {session}_{pane-title}_{timestamp}.txt

Examples:
  ntm save myproject                    # Save all panes to ./outputs/
  ntm save myproject -o ~/logs          # Custom output directory
  ntm save myproject --cc               # Save Claude panes only
  ntm save myproject -l 5000            # Save last 5000 lines`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			filter := AgentFilter{
				All:    allFlag,
				Claude: ccFlag,
				Codex:  codFlag,
				Gemini: gmiFlag,
			}

			// Default to all if no filter specified
			if filter.IsEmpty() {
				filter.All = true
			}

			return runSave(cmd.OutOrStdout(), session, outputDir, lines, filter)
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output", "o", "./outputs", "Output directory")
	cmd.Flags().IntVarP(&lines, "lines", "l", 2000, "Number of lines to capture")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Save from all panes")
	cmd.Flags().BoolVar(&ccFlag, "cc", false, "Save from Claude panes")
	cmd.Flags().BoolVar(&codFlag, "cod", false, "Save from Codex panes")
	cmd.Flags().BoolVar(&gmiFlag, "gmi", false, "Save from Gemini panes")

	return cmd
}

func runSave(w io.Writer, session, outputDir string, lines int, filter AgentFilter) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	t := theme.Current()

	// Determine target session
	if session == "" {
		if tmux.InTmux() {
			session = tmux.GetCurrentSession()
		} else {
			if !IsInteractive(w) {
				return fmt.Errorf("non-interactive environment: session name is required")
			}
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found")
			}

			selected, err := palette.RunSessionSelector(sessions)
			if err != nil {
				return err
			}
			if selected == "" {
				return nil
			}
			session = selected
		}
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	// Filter panes
	var targetPanes []tmux.Pane
	for _, p := range panes {
		if filter.Matches(p.Type) {
			targetPanes = append(targetPanes, p)
		}
	}

	if len(targetPanes) == 0 {
		return fmt.Errorf("no matching panes found")
	}

	timestamp := time.Now().Format("20060102_150405")
	savedFiles := 0

	for _, p := range targetPanes {
		output, err := tmux.CapturePaneOutput(p.ID, lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to capture pane %d: %v\n", p.Index, err)
			continue
		}

		// Sanitize title for filename
		title := sanitizeFilename(p.Title)
		if title == "" {
			title = fmt.Sprintf("pane_%d", p.Index)
		}

		filename := fmt.Sprintf("%s_%s_%s.txt", session, title, timestamp)
		outputPath := filepath.Join(outputDir, filename)

		// Add header to file
		header := fmt.Sprintf("═══════════════════════════════════════════════════════════════\n")
		header += fmt.Sprintf("Session: %s\n", session)
		header += fmt.Sprintf("Pane: %s (index %d)\n", p.Title, p.Index)
		header += fmt.Sprintf("Command: %s\n", p.Command)
		header += fmt.Sprintf("Captured: %s\n", time.Now().Format(time.RFC3339))
		header += fmt.Sprintf("Lines: %d\n", lines)
		header += fmt.Sprintf("═══════════════════════════════════════════════════════════════\n\n")

		content := header + output

		if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write %s: %v\n", outputPath, err)
			continue
		}

		savedFiles++
		fmt.Printf("  %s✓%s %s\n", colorize(t.Success), colorize(t.Text), outputPath)
	}

	if savedFiles == 0 {
		return fmt.Errorf("no files saved")
	}

	fmt.Printf("\n%s✓%s Saved %d file(s) to %s\n",
		colorize(t.Success), colorize(t.Text), savedFiles, outputDir)

	return nil
}

// sanitizeFilename removes/replaces characters that are invalid in filenames
func sanitizeFilename(name string) string {
	// Remove or replace invalid characters
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"__", "_", // Collapse double underscores
	)

	result := replacer.Replace(name)

	// Remove leading/trailing underscores
	result = strings.Trim(result, "_")

	// Limit length
	if len(result) > 50 {
		result = result[:50]
	}

	return result
}
