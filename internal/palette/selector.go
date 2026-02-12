package palette

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// SessionSelector is a TUI for selecting a tmux session
type SessionSelector struct {
	sessions []tmux.Session
	cursor   int
	selected string
	quitting bool
	width    int
	height   int
	animTick int

	// Theme
	theme theme.Theme
	icons icons.IconSet
}

// SessionSelectorKeyMap defines keybindings for the selector
type SessionSelectorKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Quit   key.Binding
	Num1   key.Binding
	Num2   key.Binding
	Num3   key.Binding
	Num4   key.Binding
	Num5   key.Binding
	Num6   key.Binding
	Num7   key.Binding
	Num8   key.Binding
	Num9   key.Binding
}

var selectorKeys = SessionSelectorKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Select: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q/esc", "quit"),
	),
	Num1: key.NewBinding(key.WithKeys("1")),
	Num2: key.NewBinding(key.WithKeys("2")),
	Num3: key.NewBinding(key.WithKeys("3")),
	Num4: key.NewBinding(key.WithKeys("4")),
	Num5: key.NewBinding(key.WithKeys("5")),
	Num6: key.NewBinding(key.WithKeys("6")),
	Num7: key.NewBinding(key.WithKeys("7")),
	Num8: key.NewBinding(key.WithKeys("8")),
	Num9: key.NewBinding(key.WithKeys("9")),
}

// NewSessionSelector creates a new session selector
func NewSessionSelector(sessions []tmux.Session) SessionSelector {
	return SessionSelector{
		sessions: sessions,
		width:    60,
		height:   20,
		theme:    theme.Current(),
		icons:    icons.Current(),
	}
}

// Init implements tea.Model
func (s SessionSelector) Init() tea.Cmd {
	return s.tick()
}

func (s SessionSelector) tick() tea.Cmd {
	return tea.Tick(time.Millisecond*250, func(t time.Time) tea.Msg {
		return AnimationTickMsg(t)
	})
}

// Update implements tea.Model
func (s SessionSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
		return s, nil

	case AnimationTickMsg:
		s.animTick++
		return s, s.tick()

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, selectorKeys.Quit):
			s.quitting = true
			return s, tea.Quit

		case key.Matches(msg, selectorKeys.Up):
			if s.cursor > 0 {
				s.cursor--
			}

		case key.Matches(msg, selectorKeys.Down):
			if s.cursor < len(s.sessions)-1 {
				s.cursor++
			}

		case key.Matches(msg, selectorKeys.Select):
			if len(s.sessions) > 0 {
				s.selected = s.sessions[s.cursor].Name
				return s, tea.Quit
			}

		// Quick select with numbers 1-9
		case key.Matches(msg, selectorKeys.Num1):
			if s.selectByNumber(1) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num2):
			if s.selectByNumber(2) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num3):
			if s.selectByNumber(3) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num4):
			if s.selectByNumber(4) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num5):
			if s.selectByNumber(5) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num6):
			if s.selectByNumber(6) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num7):
			if s.selectByNumber(7) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num8):
			if s.selectByNumber(8) {
				return s, tea.Quit
			}
		case key.Matches(msg, selectorKeys.Num9):
			if s.selectByNumber(9) {
				return s, tea.Quit
			}
		}
	}

	return s, nil
}

func (s *SessionSelector) selectByNumber(n int) bool {
	idx := n - 1
	if idx >= 0 && idx < len(s.sessions) {
		s.cursor = idx
		s.selected = s.sessions[idx].Name
		return true
	}
	return false
}

// View implements tea.Model
func (s SessionSelector) View() string {
	t := s.theme
	ic := s.icons

	var b strings.Builder

	// Responsive box width based on layout mode
	layoutMode := styles.GetLayoutMode(s.width)
	var boxWidth int
	switch layoutMode {
	case styles.LayoutUltraWide:
		boxWidth = 80
	case styles.LayoutSpacious:
		boxWidth = 70
	case styles.LayoutDefault:
		boxWidth = 60
	default:
		boxWidth = s.width - 6
		if boxWidth < 45 {
			boxWidth = 45
		}
	}

	b.WriteString("\n")

	// Header with animated gradient
	titleText := ic.Session + "  Select Session"
	animatedTitle := styles.Shimmer(titleText, s.animTick, string(t.Blue), string(t.Lavender), string(t.Mauve))
	b.WriteString("  " + animatedTitle + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Blue), string(t.Mauve)) + "\n\n")

	if len(s.sessions) == 0 {
		// Empty state with styled message
		b.WriteString(components.RenderState(components.StateOptions{
			Kind:    components.StateEmpty,
			Icon:    ic.Warning,
			Message: "No tmux sessions found",
			Width:   boxWidth,
		}) + "\n\n")

		hintStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		cmdStyle := lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
		b.WriteString("  " + hintStyle.Render("Create one with: ") + cmdStyle.Render("ntm spawn <name>") + "\n\n")
	} else {
		// Session list
		for i, session := range s.sessions {
			isSelected := i == s.cursor

			var line strings.Builder

			// Selection indicator
			if isSelected {
				pointer := styles.Shimmer(ic.Pointer, s.animTick, string(t.Pink), string(t.Mauve))
				line.WriteString(pointer + " ")
			} else {
				line.WriteString("  ")
			}

			// Number badge (1-9)
			if i < 9 {
				numStyle := lipgloss.NewStyle().
					Foreground(t.Overlay).
					Background(t.Surface0).
					Padding(0, 0)
				line.WriteString(numStyle.Render(fmt.Sprintf("%d", i+1)) + " ")
			} else {
				line.WriteString("  ")
			}

			// Session name with selection styling
			name := session.Name
			if isSelected {
				line.WriteString(styles.GradientText(name, string(t.Pink), string(t.Rosewater)))
			} else {
				nameStyle := lipgloss.NewStyle().Foreground(t.Text)
				line.WriteString(nameStyle.Render(name))
			}

			// Window count badge
			windowBadge := lipgloss.NewStyle().
				Foreground(t.Subtext).
				Render(fmt.Sprintf(" %d win", session.Windows))
			line.WriteString(windowBadge)

			// Attached indicator with pulsing dot
			if session.Attached {
				dotColor := string(t.Green)
				if s.animTick%20 < 10 {
					dotColor = string(t.Teal)
				}
				dot := lipgloss.NewStyle().Foreground(lipgloss.Color(dotColor)).Render(" " + ic.Dot)
				attachedLabel := lipgloss.NewStyle().Foreground(t.Green).Render(" attached")
				line.WriteString(dot + attachedLabel)
			}

			b.WriteString(line.String() + "\n")
		}
		b.WriteString("\n")
	}

	// Divider
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Help bar
	b.WriteString("  " + s.renderHelpBar() + "\n")

	return b.String()
}

func (s SessionSelector) renderHelpBar() string {
	t := s.theme

	keyStyle := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Bold(true).
		Padding(0, 1)

	descStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)

	items := []struct {
		key  string
		desc string
	}{
		{"↑/↓", "navigate"},
		{"1-9", "quick select"},
		{"Enter", "select"},
		{"Esc", "quit"},
	}

	var parts []string
	for _, item := range items {
		parts = append(parts, keyStyle.Render(item.key)+" "+descStyle.Render(item.desc))
	}

	return strings.Join(parts, "  ")
}

// Selected returns the selected session name (empty if cancelled)
func (s SessionSelector) Selected() string {
	return s.selected
}

// RunSessionSelector runs the session selector and returns the selected session
func RunSessionSelector(sessions []tmux.Session) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions available")
	}

	// If only one session, return it directly
	if len(sessions) == 1 {
		return sessions[0].Name, nil
	}

	model := NewSessionSelector(sessions)
	p := tea.NewProgram(model)

	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	result := finalModel.(SessionSelector)
	return result.Selected(), nil
}
