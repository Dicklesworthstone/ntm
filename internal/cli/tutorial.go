package cli

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/tui/tuilog"
	"github.com/Dicklesworthstone/ntm/internal/tutorial"
)

func newTutorialCmd() *cobra.Command {
	var (
		skipAnimations bool
		startSlide     int
	)

	cmd := &cobra.Command{
		Use:   "tutorial",
		Short: "Start the interactive NTM tutorial",
		Long: `Launch a beautiful, interactive tutorial that teaches you how to use NTM.

The tutorial features:
  • Stunning animated slides with gradient effects
  • Step-by-step explanation of core concepts
  • Interactive command demonstrations
  • Pro tips for multi-agent workflows

Navigation:
  ← → / h l    Navigate between slides
  1-9          Jump to specific slide
  Space/Enter  Next slide
  s            Skip current animation
  r            Restart slide animation
  q            Quit tutorial`,
		Example: `  # Start the tutorial
  ntm tutorial

  # Skip animations for faster navigation
  ntm tutorial --skip-animations

  # Start from a specific slide (1-9)
  ntm tutorial --slide=5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Build options
			var opts []tutorial.Option

			if skipAnimations {
				opts = append(opts, tutorial.WithSkipAnimations())
			}

			if startSlide > 0 && startSlide <= tutorial.SlideCount {
				opts = append(opts, tutorial.WithStartSlide(tutorial.SlideID(startSlide-1)))
			}

			// Create and run the tutorial
			m := tutorial.New(opts...)

			progOpts := []tea.ProgramOption{tea.WithAltScreen()}
			// Enable mouse support unless NTM_MOUSE=0
			if v, ok := os.LookupEnv("NTM_MOUSE"); !ok || (v != "0" && v != "false") {
				progOpts = append(progOpts, tea.WithMouseCellMotion())
			}
			// Keep default-logger output off the terminal while the TUI owns it.
			restoreLogs := tuilog.Redirect("tutorial", false)
			p := tea.NewProgram(m, progOpts...)
			_, runErr := p.Run()
			restoreLogs()
			if runErr != nil {
				return fmt.Errorf("failed to run tutorial: %w", runErr)
			}

			// Print a nice exit message
			printTutorialExitMessage()

			return nil
		},
	}

	cmd.Flags().BoolVar(&skipAnimations, "skip-animations", false, "Skip animations for faster navigation")
	cmd.Flags().IntVar(&startSlide, "slide", 0, "Start from a specific slide (1-9)")

	return cmd
}

func printTutorialExitMessage() {
	// Nice colored exit message
	fmt.Fprintln(os.Stderr)

	// Using ANSI colors directly for a gradient effect
	messages := []struct {
		text  string
		color string
	}{
		{"✨ Thanks for completing the NTM tutorial!", "#a6e3a1"},
		{"", ""},
		{"Quick reference:", "#89b4fa"},
		{"  ntm quick myproject --template=go    Create project", "#cdd6f4"},
		{"  ntm spawn myproject --cc=2           Spawn agents", "#cdd6f4"},
		{"  ntm send myproject --all \"prompt\"    Send to all", "#cdd6f4"},
		{"  ntm palette myproject                Open palette", "#cdd6f4"},
		{"  ntm --help                           Full help", "#cdd6f4"},
		{"", ""},
		{"Happy coding! 🚀", "#f5c2e7"},
	}

	for _, m := range messages {
		if m.text == "" {
			fmt.Fprintln(os.Stderr)
			continue
		}

		// Parse hex color
		var r, g, b int
		if len(m.color) == 7 && m.color[0] == '#' {
			fmt.Sscanf(m.color, "#%02x%02x%02x", &r, &g, &b)
		} else {
			r, g, b = 205, 214, 244 // default text color
		}

		// Print with ANSI color
		fmt.Fprintf(os.Stderr, "\x1b[38;2;%d;%d;%dm%s\x1b[0m\n", r, g, b, m.text)
	}

	fmt.Fprintln(os.Stderr)
}
