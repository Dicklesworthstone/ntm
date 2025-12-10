package palette

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// AnimationTickMsg is used to trigger animation updates
type AnimationTickMsg time.Time

// Phase represents the current UI phase
type Phase int

const (
	PhaseCommand Phase = iota
	PhaseTarget
	PhaseConfirm
)

// Target represents the send target
type Target int

const (
	TargetAll Target = iota
	TargetClaude
	TargetCodex
	TargetGemini
)

// Model is the Bubble Tea model for the palette
type Model struct {
	session   string
	commands  []config.PaletteCmd
	filtered  []config.PaletteCmd
	cursor    int
	selected  *config.PaletteCmd
	phase     Phase
	target    Target
	filter    textinput.Model
	width     int
	height    int
	sent      bool
	sentCount int
	quitting  bool
	err       error

	// Animation state
	animTick    int
	showPreview bool

	// visualOrder maps visual display position (0-indexed) to index in filtered slice.
	// This is needed because items are grouped by category, so visual order differs from slice order.
	visualOrder []int

	// Theme and styles
	theme  theme.Theme
	styles theme.Styles
	icons  icons.IconSet

	// Computed gradient colors
	headerGradient []string
	listGradient   []string
}

// KeyMap defines the keybindings
type KeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Select  key.Binding
	Back    key.Binding
	Quit    key.Binding
	Target1 key.Binding
	Target2 key.Binding
	Target3 key.Binding
	Target4 key.Binding
	Num1    key.Binding
	Num2    key.Binding
	Num3    key.Binding
	Num4    key.Binding
	Num5    key.Binding
	Num6    key.Binding
	Num7    key.Binding
	Num8    key.Binding
	Num9    key.Binding
}

var keys = KeyMap{
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
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back/quit"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	Target1: key.NewBinding(key.WithKeys("1")),
	Target2: key.NewBinding(key.WithKeys("2")),
	Target3: key.NewBinding(key.WithKeys("3")),
	Target4: key.NewBinding(key.WithKeys("4")),
	Num1:    key.NewBinding(key.WithKeys("1")),
	Num2:    key.NewBinding(key.WithKeys("2")),
	Num3:    key.NewBinding(key.WithKeys("3")),
	Num4:    key.NewBinding(key.WithKeys("4")),
	Num5:    key.NewBinding(key.WithKeys("5")),
	Num6:    key.NewBinding(key.WithKeys("6")),
	Num7:    key.NewBinding(key.WithKeys("7")),
	Num8:    key.NewBinding(key.WithKeys("8")),
	Num9:    key.NewBinding(key.WithKeys("9")),
}

// New creates a new palette model
func New(session string, commands []config.PaletteCmd) Model {
	ti := textinput.New()
	ti.Placeholder = "Search commands..."
	ti.Focus()
	ti.CharLimit = 50
	ti.Width = 40

	t := theme.Current()

	// Style the input with theme colors
	ti.PromptStyle = lipgloss.NewStyle().Foreground(t.Mauve)
	ti.TextStyle = lipgloss.NewStyle().Foreground(t.Text)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.Overlay)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(t.Pink)

	s := theme.NewStyles(t)
	ic := icons.Current()

	m := Model{
		session:     session,
		commands:    commands,
		filtered:    commands,
		filter:      ti,
		phase:       PhaseCommand,
		width:       80,
		height:      24,
		showPreview: true,
		theme:       t,
		styles:      s,
		icons:       ic,
		headerGradient: []string{
			string(t.Blue),
			string(t.Lavender),
			string(t.Mauve),
		},
		listGradient: []string{
			string(t.Mauve),
			string(t.Pink),
		},
	}

	// Build initial visual order mapping
	m.buildVisualOrder()

	return m
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.tick(),
	)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Millisecond*50, func(t time.Time) tea.Msg {
		return AnimationTickMsg(t)
	})
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.filter.Width = m.width/2 - 10
		return m, nil

	case AnimationTickMsg:
		m.animTick++
		return m, m.tick()

	case tea.KeyMsg:
		switch m.phase {
		case PhaseCommand:
			return m.updateCommandPhase(msg)
		case PhaseTarget:
			return m.updateTargetPhase(msg)
		}
	}

	// Update filter input
	if m.phase == PhaseCommand {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.updateFiltered()
		return m, cmd
	}

	return m, nil
}

func (m *Model) updateCommandPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, keys.Back):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}

	case key.Matches(msg, keys.Down):
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}

	case key.Matches(msg, keys.Select):
		if len(m.filtered) > 0 {
			m.selected = &m.filtered[m.cursor]
			m.phase = PhaseTarget
		}

	// Quick select with numbers 1-9
	case key.Matches(msg, keys.Num1):
		if m.selectByNumber(1) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num2):
		if m.selectByNumber(2) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num3):
		if m.selectByNumber(3) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num4):
		if m.selectByNumber(4) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num5):
		if m.selectByNumber(5) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num6):
		if m.selectByNumber(6) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num7):
		if m.selectByNumber(7) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num8):
		if m.selectByNumber(8) {
			m.phase = PhaseTarget
		}
	case key.Matches(msg, keys.Num9):
		if m.selectByNumber(9) {
			m.phase = PhaseTarget
		}

	default:
		// Let the textinput handle it
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.updateFiltered()
		return m, cmd
	}

	return m, nil
}

func (m *Model) selectByNumber(n int) bool {
	visualPos := n - 1 // Convert 1-based to 0-based
	if visualPos >= 0 && visualPos < len(m.visualOrder) {
		// Map visual position to actual index in filtered slice
		idx := m.visualOrder[visualPos]
		m.cursor = idx
		m.selected = &m.filtered[idx]
		return true
	}
	return false
}

func (m *Model) updateTargetPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back):
		m.phase = PhaseCommand
		m.selected = nil

	case key.Matches(msg, keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, keys.Target1):
		m.target = TargetAll
		return m.send()

	case key.Matches(msg, keys.Target2):
		m.target = TargetClaude
		return m.send()

	case key.Matches(msg, keys.Target3):
		m.target = TargetCodex
		return m.send()

	case key.Matches(msg, keys.Target4):
		m.target = TargetGemini
		return m.send()
	}

	return m, nil
}

func (m *Model) updateFiltered() {
	query := strings.ToLower(m.filter.Value())
	if query == "" {
		m.filtered = m.commands
	} else {
		m.filtered = nil
		for _, cmd := range m.commands {
			if strings.Contains(strings.ToLower(cmd.Label), query) ||
				strings.Contains(strings.ToLower(cmd.Key), query) ||
				strings.Contains(strings.ToLower(cmd.Category), query) {
				m.filtered = append(m.filtered, cmd)
			}
		}
	}

	// Build visual order mapping (items grouped by category)
	m.buildVisualOrder()

	// Keep cursor in bounds
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// buildVisualOrder creates a mapping from visual position to filtered index.
// Items are grouped by category, so the visual order differs from the slice order.
func (m *Model) buildVisualOrder() {
	m.visualOrder = nil
	if len(m.filtered) == 0 {
		return
	}

	// Group by category (same logic as renderCommandList)
	categories := make(map[string][]int)
	categoryOrder := []string{}

	for i, cmd := range m.filtered {
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	// Build visual order following category grouping
	for _, cat := range categoryOrder {
		indices := categories[cat]
		m.visualOrder = append(m.visualOrder, indices...)
	}
}

func (m *Model) send() (tea.Model, tea.Cmd) {
	if m.selected == nil {
		return m, nil
	}

	panes, err := tmux.GetPanes(m.session)
	if err != nil {
		m.err = err
		return m, tea.Quit
	}

	prompt := m.selected.Prompt
	count := 0

	for _, p := range panes {
		var shouldSend bool

		switch m.target {
		case TargetAll:
			// Send to all agent panes
			shouldSend = p.Type != tmux.AgentUser
		case TargetClaude:
			shouldSend = p.Type == tmux.AgentClaude
		case TargetCodex:
			shouldSend = p.Type == tmux.AgentCodex
		case TargetGemini:
			shouldSend = p.Type == tmux.AgentGemini
		}

		if shouldSend {
			if err := tmux.SendKeys(p.ID, prompt, true); err != nil {
				m.err = err
				return m, tea.Quit
			}
			count++
		}
	}

	m.sent = true
	m.sentCount = count
	m.quitting = true
	return m, tea.Quit
}

// View implements tea.Model
func (m Model) View() string {
	if m.quitting {
		return m.viewQuitting()
	}

	switch m.phase {
	case PhaseCommand:
		return m.viewCommandPhase()
	case PhaseTarget:
		return m.viewTargetPhase()
	}

	return ""
}

func (m Model) viewQuitting() string {
	t := m.theme
	ic := m.icons

	if m.err != nil {
		errorStyle := lipgloss.NewStyle().Foreground(t.Error)
		return errorStyle.Render(fmt.Sprintf("\n  %s Error: %v\n\n", ic.Cross, m.err))
	}

	if m.sent {
		// Beautiful success message with gradient
		targetName := "all agents"
		targetColor := string(t.Green)
		targetIcon := ic.All

		switch m.target {
		case TargetClaude:
			targetName = "Claude"
			targetColor = string(t.Claude)
			targetIcon = ic.Claude
		case TargetCodex:
			targetName = "Codex"
			targetColor = string(t.Codex)
			targetIcon = ic.Codex
		case TargetGemini:
			targetName = "Gemini"
			targetColor = string(t.Gemini)
			targetIcon = ic.Gemini
		}

		checkStyle := lipgloss.NewStyle().Foreground(t.Success).Bold(true)
		labelStyle := lipgloss.NewStyle().Foreground(t.Text)
		highlightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(targetColor)).Bold(true)
		countStyle := lipgloss.NewStyle().Foreground(t.Subtext)

		return fmt.Sprintf("\n  %s %s %s %s\n\n",
			checkStyle.Render(ic.Check),
			labelStyle.Render("Sent to"),
			highlightStyle.Render(targetIcon+" "+targetName),
			countStyle.Render(fmt.Sprintf("(%d panes)", m.sentCount)),
		)
	}

	return ""
}

func (m Model) viewCommandPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	// Calculate layout dimensions
	listWidth := m.width/2 - 2
	previewWidth := m.width/2 - 2
	if listWidth < 35 {
		listWidth = 35
	}
	if previewWidth < 35 {
		previewWidth = 35
	}

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated gradient
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")

	// Animated title with shimmer effect
	titleText := ic.Palette + "  NTM Command Palette"
	animatedTitle := styles.Shimmer(titleText, m.animTick, m.headerGradient...)

	sessionBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(ic.Session + " " + m.session)

	headerLine := "  " + animatedTitle + "  " + sessionBadge
	b.WriteString(headerLine + "\n")

	// Gradient divider
	b.WriteString("  " + styles.GradientDivider(m.width-4, m.headerGradient...) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// FILTER INPUT with glow effect
	// ═══════════════════════════════════════════════════════════════
	filterBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Mauve).
		Padding(0, 1).
		Width(listWidth - 4)

	searchIcon := lipgloss.NewStyle().Foreground(t.Mauve).Render(ic.Search + " ")
	b.WriteString("  " + filterBox.Render(searchIcon+m.filter.View()) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// TWO-COLUMN LAYOUT: Commands | Preview
	// ═══════════════════════════════════════════════════════════════
	listContent := m.renderCommandList(listWidth - 4)
	previewContent := m.renderPreview(previewWidth - 4)

	// List box with subtle glow
	listBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface2).
		Width(listWidth - 2).
		Height(m.height - 14).
		Padding(1, 1)

	// Preview box with accent border
	previewBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Blue).
		Width(previewWidth - 2).
		Height(m.height - 14).
		Padding(1, 1)

	// Join columns horizontally
	columns := lipgloss.JoinHorizontal(
		lipgloss.Top,
		listBox.Render(listContent),
		"  ",
		previewBox.Render(previewContent),
	)

	b.WriteString(columns + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR with styled keys
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("  " + m.renderHelpBar() + "\n")

	return b.String()
}

func (m Model) renderCommandList(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.filtered) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)
		return emptyStyle.Render("No commands match your filter")
	}

	var lines []string

	// Group by category
	categories := make(map[string][]int)
	categoryOrder := []string{}

	for i, cmd := range m.filtered {
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	itemNum := 0
	for _, cat := range categoryOrder {
		indices := categories[cat]

		// Category header with icon and gradient
		catIcon := ic.CategoryIcon(cat)
		catText := catIcon + " " + cat
		catStyled := styles.GradientText(catText, string(t.Lavender), string(t.Mauve))
		lines = append(lines, catStyled)

		for _, idx := range indices {
			cmd := m.filtered[idx]
			isSelected := idx == m.cursor
			itemNum++

			var line strings.Builder

			// Selection indicator with animation
			if isSelected {
				// Animated pointer
				pointer := styles.Shimmer(ic.Pointer, m.animTick, string(t.Pink), string(t.Mauve))
				line.WriteString(pointer + " ")
			} else {
				line.WriteString("  ")
			}

			// Number (1-9) with subtle styling
			if itemNum <= 9 {
				numStyle := lipgloss.NewStyle().
					Foreground(t.Surface2).
					Background(t.Surface0).
					Padding(0, 0)
				line.WriteString(numStyle.Render(fmt.Sprintf("%d", itemNum)) + " ")
			} else {
				line.WriteString("  ")
			}

			// Item label with selection highlight
			label := cmd.Label
			if len(label) > width-8 {
				label = label[:width-11] + "..."
			}

			if isSelected {
				// Gradient highlight for selected item
				line.WriteString(styles.GradientText(label, string(t.Pink), string(t.Rosewater)))
			} else {
				labelStyle := lipgloss.NewStyle().Foreground(t.Text)
				line.WriteString(labelStyle.Render(label))
			}

			lines = append(lines, line.String())
		}

		lines = append(lines, "") // Spacing between categories
	}

	return strings.Join(lines, "\n")
}

func (m Model) renderPreview(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.filtered) == 0 || m.cursor >= len(m.filtered) {
		emptyStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)
		return styles.CenterText(emptyStyle.Render("Select a command to preview"), width)
	}

	cmd := m.filtered[m.cursor]

	var b strings.Builder

	// Title with gradient
	titleText := ic.Send + " " + cmd.Label
	b.WriteString(styles.GradientText(titleText, string(t.Blue), string(t.Sapphire)) + "\n")
	b.WriteString(styles.GradientDivider(width, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Category badge
	if cmd.Category != "" {
		// Only use agent badge for known agent types, otherwise create a generic category badge
		catLower := strings.ToLower(cmd.Category)
		var badge string
		if catLower == "claude" || catLower == "cc" || catLower == "codex" || catLower == "cod" || catLower == "gemini" || catLower == "gmi" {
			badge = components.RenderAgentBadge(catLower)
		} else {
			badge = lipgloss.NewStyle().
				Background(t.Mauve).
				Foreground(t.Base).
				Bold(true).
				Padding(0, 1).
				Render(cmd.Category)
		}
		b.WriteString(badge + "\n\n")
	}

	// Prompt content with wrapping
	promptStyle := lipgloss.NewStyle().Foreground(t.Text)
	wrapped := wordwrap.String(cmd.Prompt, width)

	// Add subtle line highlighting on the left
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i < len(lines)-1 || line != "" {
			b.WriteString(promptStyle.Render(line) + "\n")
		}
	}

	return b.String()
}

func (m Model) renderHelpBar() string {
	t := m.theme

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

func (m Model) viewTargetPhase() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	// Box dimensions
	boxWidth := 60
	if m.width < 70 {
		boxWidth = m.width - 10
	}

	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated gradient
	// ═══════════════════════════════════════════════════════════════
	titleText := ic.Target + "  Select Target"
	animatedTitle := styles.Shimmer(titleText, m.animTick, string(t.Blue), string(t.Mauve), string(t.Pink))
	b.WriteString("  " + animatedTitle + "\n")
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Blue), string(t.Mauve)) + "\n\n")

	// Selected command info
	dimStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	cmdBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(m.selected.Label)

	b.WriteString("  " + dimStyle.Render("Sending:") + " " + cmdBadge + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// TARGET OPTIONS with visual styling
	// ═══════════════════════════════════════════════════════════════
	targets := []struct {
		key     string
		icon    string
		label   string
		desc    string
		color   lipgloss.Color
		bgColor lipgloss.Color
	}{
		{"1", ic.All, "All Agents", "broadcast to all", t.Green, t.Surface0},
		{"2", ic.Claude, "Claude (cc)", "Anthropic agents", t.Claude, t.Surface0},
		{"3", ic.Codex, "Codex (cod)", "OpenAI agents", t.Codex, t.Surface0},
		{"4", ic.Gemini, "Gemini (gmi)", "Google agents", t.Gemini, t.Surface0},
	}

	for _, target := range targets {
		// Key badge
		keyBadge := lipgloss.NewStyle().
			Background(target.color).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(target.key)

		// Icon with color
		iconStyled := lipgloss.NewStyle().
			Foreground(target.color).
			Bold(true).
			Render(target.icon)

		// Label
		labelStyle := lipgloss.NewStyle().
			Foreground(t.Text).
			Bold(true).
			Width(15)

		// Description
		descStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)

		line := fmt.Sprintf("  %s  %s  %s %s",
			keyBadge,
			iconStyled,
			labelStyle.Render(target.label),
			descStyle.Render(target.desc))

		b.WriteString(line + "\n\n")
	}

	// Divider
	b.WriteString("  " + styles.GradientDivider(boxWidth, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Help bar
	b.WriteString("  " + m.renderTargetHelpBar() + "\n")

	return b.String()
}

func (m Model) renderTargetHelpBar() string {
	t := m.theme

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
		{"1-4", "select target"},
		{"Esc", "back"},
		{"q", "quit"},
	}

	var parts []string
	for _, item := range items {
		parts = append(parts, keyStyle.Render(item.key)+" "+descStyle.Render(item.desc))
	}

	return strings.Join(parts, "  ")
}

// Result returns the send result after the program exits
func (m Model) Result() (sent bool, err error) {
	return m.sent, m.err
}
