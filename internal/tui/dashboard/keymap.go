package dashboard

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// KeyMap defines dashboard keybindings.
type KeyMap struct {
	Up             key.Binding
	Down           key.Binding
	Zoom           key.Binding
	NextPanel      key.Binding // Tab to cycle panels
	PrevPanel      key.Binding // Shift+Tab to cycle back
	Refresh        key.Binding
	Pause          key.Binding
	Quit           key.Binding
	ContextRefresh key.Binding // 'c' to refresh context data
	MailRefresh    key.Binding // 'm' to refresh Agent Mail data
	InboxToggle    key.Binding // 'i' to toggle inbox details
	CassSearch     key.Binding // 'ctrl+s' to open CASS search
	EnsembleModes  key.Binding // 'e' to open ensemble modes view
	Help           key.Binding // '?' to toggle help overlay
	Diagnostics    key.Binding // 'd' to toggle diagnostics
	ScanToggle     key.Binding // 'u' to toggle UBS scanning
	Checkpoint     key.Binding // 'ctrl+k' to create checkpoint
	ToastDismiss   key.Binding // 'ctrl+x' to dismiss newest toast
	ToastHistory   key.Binding // 'n' to toggle toast history
	SpawnWizard    key.Binding // 'w' to open spawn wizard [tui-upgrade: bd-uz09d]
	ViewToggle     key.Binding // 'v' to toggle table/list view [tui-upgrade: bd-ijnu3]
	Tab            key.Binding
	ShiftTab       key.Binding
	Num1           key.Binding
	Num2           key.Binding
	Num3           key.Binding
	Num4           key.Binding
	Num5           key.Binding
	Num6           key.Binding
	Num7           key.Binding
	Num8           key.Binding
	Num9           key.Binding
}

// ShortHelp returns the short help bindings for the footer bar.
// Implements help.KeyMap interface.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit, k.Tab, k.Zoom, k.Refresh}
}

// FullHelp returns the full help bindings for the help overlay.
// Implements help.KeyMap interface.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Zoom},                                                                  // Navigation
		{k.NextPanel, k.PrevPanel, k.ViewToggle},                                                // Panels & Actions
		{k.Refresh, k.ContextRefresh, k.MailRefresh, k.CassSearch},                              // Data
		{k.Help, k.Quit, k.Pause, k.Diagnostics, k.ToastDismiss, k.ToastHistory, k.SpawnWizard}, // Control
	}
}

// Compile-time check that KeyMap implements help.KeyMap.
var _ help.KeyMap = KeyMap{}

// newHelpModel creates a styled help.Model using the current theme.
func newHelpModel(t theme.Theme) help.Model {
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(t.Subtext)
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(t.Overlay)
	h.Styles.FullKey = lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	h.Styles.FullDesc = lipgloss.NewStyle().Foreground(t.Subtext)
	h.Styles.FullSeparator = lipgloss.NewStyle().Foreground(t.Overlay)
	return h
}

var dashKeys = KeyMap{
	Up:             key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:           key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Zoom:           key.NewBinding(key.WithKeys("z", "enter"), key.WithHelp("z/enter", "zoom")),
	NextPanel:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
	PrevPanel:      key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
	Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:          key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause/resume auto-refresh")),
	Quit:           key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	ContextRefresh: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "refresh context")),
	MailRefresh:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "refresh mail")),
	InboxToggle:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inbox details")),
	CassSearch:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "cass search")),
	EnsembleModes:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "ensemble modes")),
	Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
	Diagnostics:    key.NewBinding(key.WithKeys("d", "ctrl+d"), key.WithHelp("d/ctrl+d", "toggle diagnostics")),
	ScanToggle:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "toggle UBS scan")),
	Checkpoint:     key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "create checkpoint")),
	ToastDismiss:   key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "dismiss toast")),
	ToastHistory:   key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "toast history")),
	SpawnWizard:    key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "spawn wizard")),
	ViewToggle:     key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle table view")),
	Tab:            key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
	ShiftTab:       key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
	Num1:           key.NewBinding(key.WithKeys("1")),
	Num2:           key.NewBinding(key.WithKeys("2")),
	Num3:           key.NewBinding(key.WithKeys("3")),
	Num4:           key.NewBinding(key.WithKeys("4")),
	Num5:           key.NewBinding(key.WithKeys("5")),
	Num6:           key.NewBinding(key.WithKeys("6")),
	Num7:           key.NewBinding(key.WithKeys("7")),
	Num8:           key.NewBinding(key.WithKeys("8")),
	Num9:           key.NewBinding(key.WithKeys("9")),
}
