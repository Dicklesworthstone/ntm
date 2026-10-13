// Package dashboard provides a stunning visual session dashboard.
// run.go contains the dashboard TUI entry points.
package dashboard

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/tui/tuilog"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// mouseEnabled returns true if NTM_MOUSE is not explicitly disabled.
// Mouse support is enabled by default; set NTM_MOUSE=0 to disable.
func mouseEnabled() bool {
	if v, ok := os.LookupEnv("NTM_MOUSE"); ok && (v == "0" || v == "false") {
		return false
	}
	return true
}

// RunOptions configures dashboard startup behavior.
type RunOptions struct {
	PopupMode       bool
	AttentionCursor int64
	InitialPanes    []tmux.PaneActivity
	// Config is the merged configuration in effect when the dashboard
	// starts. When nil the dashboard runs with built-in defaults until the
	// config watcher delivers a change.
	Config *config.Config
}

// RunWithOptions starts the dashboard with configurable options.
func RunWithOptions(session, projectDir string, opts RunOptions) (*PostQuitAction, error) {
	// The program owns the terminal from here until it exits; anything the
	// default loggers would print to stderr in the meantime corrupts the
	// rendered frame (#317).
	restoreLogs := tuilog.Redirect("dashboard", dashboardDebugEnabled(nil))
	defer restoreLogs()

	if opts.Config != nil {
		// Panels resolve their theme while New builds them, so the merged
		// config's theme (which may come from the session's project overlay
		// rather than the launch directory) must be recorded first.
		theme.SetConfigured(opts.Config.Theme)
	}
	model := New(session, projectDir)
	if opts.Config != nil {
		model.applyConfig(opts.Config)
	}
	if len(opts.InitialPanes) > 0 {
		model.seedInitialPanes(opts.InitialPanes)
	}
	if opts.AttentionCursor > 0 {
		model.requestAttentionCursor(opts.AttentionCursor)
	}
	if opts.PopupMode {
		model.activatePopupMode(dashboardNow())
	}
	programCtx, cancelProgram := context.WithCancel(context.Background())
	defer cancelProgram()
	programOpts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(programCtx)}
	if mouseEnabled() {
		programOpts = append(programOpts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(model, programOpts...)
	if opts.Config != nil && opts.Config.AgentMail.Enabled &&
		opts.Config.FileReservation.Enabled && opts.Config.FileReservation.NotifyOnConflict {
		stopConflicts := subscribeDashboardConflicts(programCtx, events.DefaultBus,
			session, agentmail.CanonicalProjectKey(projectDir), func(_ context.Context, conflict watcher.FileConflict) {
				p.Send(FileConflictMsg{Conflict: conflict})
			})
		defer func() {
			// Unblock any pending Send before joining subscription callbacks,
			// including when program initialization fails before its event loop.
			cancelProgram()
			stopConflicts()
		}()
	}
	finalModel, err := p.Run()
	if err != nil {
		return nil, err
	}
	if m, ok := finalModel.(Model); ok && m.postQuitAction != nil {
		return m.postQuitAction, nil
	}
	return nil, nil
}
