// Package dashboard provides a stunning visual session dashboard.
// run.go contains the dashboard TUI entry points.
package dashboard

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Run starts the dashboard.
func Run(session, projectDir string) (*PostQuitAction, error) {
	return RunWithOptions(session, projectDir, RunOptions{})
}

// RunPopup starts the dashboard in popup/overlay mode.
// Escape closes the popup; zoom doesn't re-attach.
func RunPopup(session, projectDir string) (*PostQuitAction, error) {
	return RunWithOptions(session, projectDir, RunOptions{PopupMode: true})
}

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
}

// RunWithOptions starts the dashboard with configurable options.
func RunWithOptions(session, projectDir string, opts RunOptions) (*PostQuitAction, error) {
	model := New(session, projectDir)
	if len(opts.InitialPanes) > 0 {
		model.seedInitialPanes(opts.InitialPanes)
	}
	if opts.AttentionCursor > 0 {
		model.requestAttentionCursor(opts.AttentionCursor)
	}
	if opts.PopupMode {
		model.activatePopupMode(dashboardNow())
	}
	programOpts := []tea.ProgramOption{tea.WithAltScreen()}
	if mouseEnabled() {
		programOpts = append(programOpts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(model, programOpts...)
	finalModel, err := p.Run()
	if err != nil {
		return nil, err
	}
	if m, ok := finalModel.(Model); ok && m.postQuitAction != nil {
		return m.postQuitAction, nil
	}
	return nil, nil
}
