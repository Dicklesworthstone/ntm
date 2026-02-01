package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func newPaletteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "palette [session]",
		Short: "Open the interactive command palette",
		Long: `Open an interactive TUI to select and send pre-configured prompts to agents.

The palette shows all commands defined in your config file, organized by category.
Filter by typing, select with Enter, then choose the target agents.

If no session is specified and you're inside tmux, uses the current session.

Examples:
  ntm palette myproject  # Open palette for specific session
  ntm palette            # Use current tmux session`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runPalette(cmd.OutOrStdout(), cmd.ErrOrStderr(), session)
		},
	}

	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

func runPalette(w io.Writer, errW io.Writer, session string) error {
	// When --json is passed, output palette data as JSON instead of launching the TUI
	if jsonOutput {
		var sess string
		if session != "" {
			sess = session
		}
		return robot.PrintPalette(cfg, robot.PaletteOptions{
			Session: sess,
		})
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(errW)
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	paletteCommands, err := loadPaletteCommands(cfg)
	if err != nil {
		return err
	}
	if len(paletteCommands) == 0 {
		return fmt.Errorf("no palette commands configured - run 'ntm config init' first")
	}

	// Create and run the TUI palette
	statePath := ""
	// Prefer persisting palette state in project config when available (shareable via git),
	// otherwise fall back to the active global config file.
	if cwd, err := os.Getwd(); err == nil {
		if projectDir, _, err := config.FindProjectConfig(cwd); err == nil && projectDir != "" {
			projectCfg := filepath.Join(projectDir, ".ntm", "config.toml")
			if _, err := os.Stat(projectCfg); err == nil {
				statePath = projectCfg
			}
		}
	}
	if statePath == "" {
		cfgPath := cfgFile
		if cfgPath == "" {
			cfgPath = config.DefaultPath()
		}
		if _, err := os.Stat(cfgPath); err == nil {
			statePath = cfgPath
		}
	}

	model := palette.NewWithOptions(session, paletteCommands, palette.Options{
		PaletteState:     cfg.PaletteState,
		PaletteStatePath: statePath,
	})
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Watch config/palette for live reloads while the palette is open
	stopWatchers, err := watchPaletteConfig(p)
	if err != nil {
		// Non-fatal: continue without live reload
		fmt.Fprintf(os.Stderr, "warning: live reload disabled: %v\n", err)
	} else {
		defer stopWatchers()
	}

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("running palette: %w", err)
	}

	// Check result
	m := finalModel.(palette.Model)
	sent, err := m.Result()
	if err != nil {
		return err
	}

	if !sent {
		// User cancelled
		return nil
	}

	return nil
}

// watchPaletteConfig watches the active config (and palette markdown if present)
// and sends reload messages to the palette program on changes.
func watchPaletteConfig(p *tea.Program) (func(), error) {
	// Determine config path in use
	cfgPath := cfgFile
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}

	// Build list of files to watch
	var paths []string
	if _, err := os.Stat(cfgPath); err == nil {
		paths = append(paths, cfgPath)
	}

	if palPath := config.DetectPalettePath(cfg); palPath != "" {
		if _, err := os.Stat(palPath); err == nil {
			paths = append(paths, palPath)
		}
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no config or palette file found to watch")
	}

	w, err := watcher.New(func(events []watcher.Event) {
		// Reload config on any relevant change
		newCfg, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			// ignore errors; keep previous config
			return
		}
		// Send reload to palette model
		commands, err := loadPaletteCommands(newCfg)
		if err != nil {
			return
		}
		p.Send(palette.ReloadMsg{Commands: commands})
	}, watcher.WithEventFilter(watcher.Write|watcher.Chmod|watcher.Create|watcher.Remove))
	if err != nil {
		return nil, err
	}

	for _, path := range paths {
		if err := w.Add(path); err != nil {
			_ = w.Close()
			return nil, err
		}
	}

	return func() { _ = w.Close() }, nil
}

func loadPaletteCommands(cfg *config.Config) ([]config.PaletteCmd, error) {
	output, err := robot.GetPalette(cfg, robot.PaletteOptions{})
	if err != nil {
		return nil, err
	}

	commands := make([]config.PaletteCmd, 0, len(output.Commands))
	for _, cmd := range output.Commands {
		commands = append(commands, config.PaletteCmd{
			Key:      cmd.Key,
			Label:    cmd.Label,
			Category: cmd.Category,
			Prompt:   cmd.Prompt,
			Tags:     cmd.Tags,
		})
	}
	return commands, nil
}
