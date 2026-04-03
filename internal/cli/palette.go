package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
		sess := strings.TrimSpace(session)
		sessionExplicit := sess != ""
		if sessionExplicit {
			resolved, err := resolvePaletteConfigSession(sess, true)
			if err != nil {
				return err
			}
			sess = resolved
		}
		paletteCfg, err := loadPaletteRuntimeConfig(sess, sessionExplicit)
		if err != nil {
			return err
		}
		return robot.PrintPalette(paletteCfg, robot.PaletteOptions{
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
	sessionExplicit := strings.TrimSpace(session) != "" && !res.Inferred

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	contextDir, err := paletteConfigContextDir(session, sessionExplicit)
	if err != nil {
		return err
	}
	paletteCfg, err := loadPaletteRuntimeConfig(session, sessionExplicit)
	if err != nil {
		return err
	}
	paletteCommands, err := loadPaletteCommands(paletteCfg)
	if err != nil {
		return err
	}
	if len(paletteCommands) == 0 {
		return fmt.Errorf("no palette commands configured - run 'ntm config init' first")
	}

	// Create and run the TUI palette
	statePath := paletteStatePath(contextDir)

	model := palette.NewWithOptions(session, paletteCommands, palette.Options{
		PaletteState:     paletteCfg.PaletteState,
		PaletteStatePath: statePath,
	})
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	// Enable mouse support unless NTM_MOUSE=0
	if v, ok := os.LookupEnv("NTM_MOUSE"); !ok || (v != "0" && v != "false") {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(model, opts...)

	// Watch config/palette for live reloads while the palette is open
	stopWatchers, err := watchPaletteConfig(p, contextDir)
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

func resolvePaletteConfigSession(session string, sessionExplicit bool) (string, error) {
	session = strings.TrimSpace(session)
	if !sessionExplicit || session == "" {
		return session, nil
	}

	return normalizeProjectScopedSessionName(session, true)
}

func paletteConfigContextDir(session string, sessionExplicit bool) (string, error) {
	resolvedSession, err := resolvePaletteConfigSession(session, sessionExplicit)
	if err != nil {
		return "", err
	}
	if sessionExplicit && resolvedSession != "" {
		return resolveExplicitProjectDirForSession(resolvedSession)
	}
	if dir := resolveProjectDirForSession(resolvedSession, false); strings.TrimSpace(dir) != "" {
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd, nil
}

func loadPaletteRuntimeConfig(session string, sessionExplicit bool) (*config.Config, error) {
	contextDir, err := paletteConfigContextDir(session, sessionExplicit)
	if err != nil {
		return nil, err
	}
	return loadPaletteWatchConfig(contextDir)
}

func paletteProjectConfigPath(cwd string) string {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	if projectDir, _, err := config.FindProjectConfig(cwd); err == nil && projectDir != "" {
		projectCfg := filepath.Join(projectDir, ".ntm", "config.toml")
		if _, err := os.Stat(projectCfg); err == nil {
			return projectCfg
		}
	}
	return ""
}

func paletteProjectMarkdownPath(cwd string) string {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	projectDir, projectCfg, err := config.FindProjectConfig(cwd)
	if err != nil || projectDir == "" || projectCfg == nil {
		return ""
	}
	palettePath, err := config.ResolveProjectPalettePath(projectDir, projectCfg)
	if err != nil || strings.TrimSpace(palettePath) == "" {
		return ""
	}
	if _, err := os.Stat(palettePath); err == nil {
		return palettePath
	}
	return ""
}

func paletteStatePath(cwd string) string {
	if projectCfg := paletteProjectConfigPath(cwd); projectCfg != "" {
		return projectCfg
	}
	cfgPath := selectedConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath
	}
	return ""
}

func appendPaletteWatchPath(paths []string, candidate string) []string {
	if candidate == "" {
		return paths
	}
	if _, err := os.Stat(candidate); err != nil {
		return paths
	}
	cleanCandidate := filepath.Clean(candidate)
	for _, existing := range paths {
		if filepath.Clean(existing) == cleanCandidate {
			return paths
		}
	}
	return append(paths, candidate)
}

func paletteWatchPaths(cwd string, cfg *config.Config) []string {
	paths := make([]string, 0, 4)
	paths = appendPaletteWatchPath(paths, selectedConfigPath())
	paths = appendPaletteWatchPath(paths, paletteProjectConfigPath(cwd))
	paths = appendPaletteWatchPath(paths, paletteProjectMarkdownPath(cwd))
	if palPath := config.DetectPalettePathForConfigPathAndCWD(cfg, selectedConfigPath(), cwd); palPath != "" {
		paths = appendPaletteWatchPath(paths, palPath)
	}
	return paths
}

func loadPaletteWatchConfig(cwd string) (*config.Config, error) {
	return config.LoadMerged(cwd, selectedConfigPath())
}

func loadPaletteWatchPaths(cwd string) ([]string, error) {
	watchCfg, err := loadPaletteWatchConfig(cwd)
	if err != nil {
		return nil, err
	}
	paths := paletteWatchPaths(cwd, watchCfg)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no config or palette file found to watch")
	}
	return paths, nil
}

// watchPaletteConfig watches the active config (and palette markdown if present)
// and sends reload messages to the palette program on changes.
func watchPaletteConfig(p *tea.Program, cwd string) (func(), error) {
	// Build list of files to watch
	paths, err := loadPaletteWatchPaths(cwd)
	if err != nil {
		return nil, err
	}

	w, err := watcher.New(func(events []watcher.Event) {
		// Reload config on any relevant change
		newCfg, loadErr := loadPaletteWatchConfig(cwd)
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
