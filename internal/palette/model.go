package palette

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
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

// ReloadMsg is emitted when palette commands are reloaded from config changes.
type ReloadMsg struct {
	Commands []config.PaletteCmd
}

type paneCounts struct {
	totalAgents int
	claude      int
	codex       int
	gemini      int

	// Representative pane titles per target (best-effort, used for UI clarity).
	allSamples    []string
	claudeSamples []string
	codexSamples  []string
	geminiSamples []string
}

type paneCountsMsg struct {
	counts paneCounts
	err    error
}

type recentsMsg struct {
	keys []string
	err  error
}

type paletteStateSavedMsg struct {
	err error
}

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
	showHelp    bool

	// Recents/favorites/pins
	recents          []string
	paletteState     config.PaletteState
	paletteStatePath string
	paletteStateErr  error

	// Cached target counts for target summary preview (best-effort).
	paneCounts      paneCounts
	paneCountsKnown bool
	paneCountsErr   error

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

	// Layout tier (narrow/split/wide/ultra)
	tier layout.Tier

	// Viewport for scrollable command list
	listViewport viewport.Model
}

// KeyMap defines the keybindings
type KeyMap struct {
	Up             key.Binding
	Down           key.Binding
	PageUp         key.Binding
	PageDown       key.Binding
	HalfPageUp     key.Binding
	HalfPageDown   key.Binding
	Home           key.Binding
	End            key.Binding
	Select         key.Binding
	Back           key.Binding
	Quit           key.Binding
	Help           key.Binding
	TogglePin      key.Binding
	ToggleFavorite key.Binding
	Target1        key.Binding
	Target2        key.Binding
	Target3        key.Binding
	Target4        key.Binding
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

var keys = KeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	),
	HalfPageUp: key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("ctrl+u", "half page up"),
	),
	HalfPageDown: key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "half page down"),
	),
	Home: key.NewBinding(
		key.WithKeys("home", "g"),
		key.WithHelp("home/g", "top"),
	),
	End: key.NewBinding(
		key.WithKeys("end", "G"),
		key.WithHelp("end/G", "bottom"),
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
	Help: key.NewBinding(
		key.WithKeys("?", "f1"),
		key.WithHelp("?", "help"),
	),
	TogglePin: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl+p", "pin"),
	),
	ToggleFavorite: key.NewBinding(
		key.WithKeys("ctrl+f"),
		key.WithHelp("ctrl+f", "favorite"),
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

type Options struct {
	PaletteState     config.PaletteState
	PaletteStatePath string
}

// New creates a new palette model.
func New(session string, commands []config.PaletteCmd) Model {
	return NewWithOptions(session, commands, Options{})
}

// NewWithOptions creates a new palette model with optional persisted state wiring.
func NewWithOptions(session string, commands []config.PaletteCmd, opts Options) Model {
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

	// Initialize viewport for scrollable command list
	vp := viewport.New(80, 10) // Will be resized on WindowSizeMsg
	vp.Style = lipgloss.NewStyle()

	m := Model{
		session:      session,
		commands:     commands,
		filtered:     commands,
		filter:       ti,
		phase:        PhaseCommand,
		width:        80,
		height:       24,
		showPreview:  true,
		theme:        t,
		styles:       s,
		icons:        ic,
		tier:         layout.TierForWidth(80),
		listViewport: vp,
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

	m.paletteState = opts.PaletteState
	m.paletteStatePath = opts.PaletteStatePath

	// Build initial visual order mapping
	m.buildVisualOrder()

	return m
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.tick(),
		m.fetchPaneCounts(),
		m.fetchRecents(),
	)
}

func (m Model) tick() tea.Cmd {
	// Use 100ms tick interval to reduce flickering on some terminals.
	// The shimmer animation is still smooth at this rate.
	return tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
		return AnimationTickMsg(t)
	})
}

func (m Model) fetchRecents() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		if session == "" {
			return recentsMsg{}
		}

		entries, err := history.ReadRecent(200)
		if err != nil {
			return recentsMsg{err: err}
		}

		const maxRecents = 8
		keys := make([]string, 0, maxRecents)
		seen := make(map[string]bool, maxRecents)
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Source != history.SourcePalette || e.Session != session {
				continue
			}
			key := strings.TrimSpace(e.Template)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
			if len(keys) >= maxRecents {
				break
			}
		}

		return recentsMsg{keys: keys}
	}
}

func (m Model) savePaletteState() tea.Cmd {
	path := strings.TrimSpace(m.paletteStatePath)
	state := m.paletteState
	if path == "" {
		return nil
	}

	return func() tea.Msg {
		return paletteStateSavedMsg{err: config.UpsertPaletteState(path, state)}
	}
}

func (m Model) fetchPaneCounts() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		if session == "" {
			return paneCountsMsg{}
		}

		panes, err := tmux.GetPanes(session)
		if err != nil {
			return paneCountsMsg{err: err}
		}

		var counts paneCounts
		const (
			maxAllSamples  = 3
			maxTypeSamples = 2
		)
		addSample := func(dst *[]string, value string, max int) {
			if value == "" || len(*dst) >= max {
				return
			}
			*dst = append(*dst, value)
		}

		for _, p := range panes {
			if p.Type == tmux.AgentUser {
				continue
			}

			title := strings.TrimSpace(p.Title)
			if title == "" {
				title = fmt.Sprintf("pane %d", p.Index)
			}

			counts.totalAgents++
			addSample(&counts.allSamples, title, maxAllSamples)
			switch p.Type {
			case tmux.AgentClaude:
				counts.claude++
				addSample(&counts.claudeSamples, title, maxTypeSamples)
			case tmux.AgentCodex:
				counts.codex++
				addSample(&counts.codexSamples, title, maxTypeSamples)
			case tmux.AgentGemini:
				counts.gemini++
				addSample(&counts.geminiSamples, title, maxTypeSamples)
			}
		}

		return paneCountsMsg{counts: counts}
	}
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidth(msg.Width)
		m.filter.Width = m.width/2 - 10
		if m.filter.Width < 20 {
			m.filter.Width = 20
		}
		// Update viewport dimensions (list height minus chrome)
		// Account for header (~4 lines), filter (~3 lines), help bar (~2 lines), borders (~4 lines)
		listHeight := m.height - 14
		if listHeight < 5 {
			listHeight = 5
		}
		m.listViewport.Width = m.width - 8
		m.listViewport.Height = listHeight
		return m, nil

	case AnimationTickMsg:
		m.animTick++
		return m, m.tick()

	case paneCountsMsg:
		if msg.err != nil {
			m.paneCountsErr = msg.err
			m.paneCountsKnown = false
			return m, nil
		}
		m.paneCounts = msg.counts
		m.paneCountsKnown = true
		m.paneCountsErr = nil
		return m, nil

	case recentsMsg:
		if msg.err == nil {
			m.recents = msg.keys
			m.buildVisualOrder()
		}
		return m, nil

	case paletteStateSavedMsg:
		m.paletteStateErr = msg.err
		return m, nil

	case ReloadMsg:
		if len(msg.Commands) > 0 {
			m.commands = msg.Commands
			m.updateFiltered()
		}
		return m, nil

	case tea.MouseMsg:
		// Handle mouse wheel scrolling in command phase
		if m.phase == PhaseCommand && !m.showHelp {
			var cmd tea.Cmd
			m.listViewport, cmd = m.listViewport.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		// Help overlay: Esc or ?/F1 closes it; otherwise ignore input.
		if m.showHelp {
			if msg.String() == "esc" || key.Matches(msg, keys.Help) {
				m.showHelp = false
			}
			return m, nil
		}

		if key.Matches(msg, keys.Help) {
			m.showHelp = true
			return m, nil
		}

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
		return *m, tea.Quit

	case key.Matches(msg, keys.Back):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, keys.Up):
		if len(m.visualOrder) > 0 {
			if pos := m.cursorVisualPos(); pos > 0 {
				m.cursor = m.visualOrder[pos-1]
				m.ensureCursorVisible()
			}
		}

	case key.Matches(msg, keys.Down):
		if len(m.visualOrder) > 0 {
			if pos := m.cursorVisualPos(); pos < len(m.visualOrder)-1 {
				m.cursor = m.visualOrder[pos+1]
				m.ensureCursorVisible()
			}
		}

	case key.Matches(msg, keys.PageUp):
		// Move cursor up by approximately one page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			// Estimate items per page (viewport height minus some header overhead)
			itemsPerPage := m.listViewport.Height - 4
			if itemsPerPage < 3 {
				itemsPerPage = 3
			}
			newPos := pos - itemsPerPage
			if newPos < 0 {
				newPos = 0
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, keys.PageDown):
		// Move cursor down by approximately one page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerPage := m.listViewport.Height - 4
			if itemsPerPage < 3 {
				itemsPerPage = 3
			}
			newPos := pos + itemsPerPage
			if newPos >= len(m.visualOrder) {
				newPos = len(m.visualOrder) - 1
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, keys.HalfPageUp):
		// Move cursor up by half a page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerHalfPage := (m.listViewport.Height - 4) / 2
			if itemsPerHalfPage < 2 {
				itemsPerHalfPage = 2
			}
			newPos := pos - itemsPerHalfPage
			if newPos < 0 {
				newPos = 0
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, keys.HalfPageDown):
		// Move cursor down by half a page worth of items
		if len(m.visualOrder) > 0 {
			pos := m.cursorVisualPos()
			itemsPerHalfPage := (m.listViewport.Height - 4) / 2
			if itemsPerHalfPage < 2 {
				itemsPerHalfPage = 2
			}
			newPos := pos + itemsPerHalfPage
			if newPos >= len(m.visualOrder) {
				newPos = len(m.visualOrder) - 1
			}
			m.cursor = m.visualOrder[newPos]
			m.ensureCursorVisible()
		}

	case key.Matches(msg, keys.Home):
		// Jump to first item
		if len(m.visualOrder) > 0 {
			m.cursor = m.visualOrder[0]
			m.listViewport.GotoTop()
		}

	case key.Matches(msg, keys.End):
		// Jump to last item
		if len(m.visualOrder) > 0 {
			m.cursor = m.visualOrder[len(m.visualOrder)-1]
			m.listViewport.GotoBottom()
		}

	case key.Matches(msg, keys.TogglePin):
		if len(m.filtered) > 0 {
			selectedKey := strings.TrimSpace(m.filtered[m.cursor].Key)
			if selectedKey != "" {
				var added bool
				m.paletteState.Pinned, added = toggleListKey(m.paletteState.Pinned, selectedKey, true)
				if added {
					m.paletteState.Favorites = ensureListKey(m.paletteState.Favorites, selectedKey, true)
				}
				m.buildVisualOrder()
				return *m, m.savePaletteState()
			}
		}

	case key.Matches(msg, keys.ToggleFavorite):
		if len(m.filtered) > 0 {
			selectedKey := strings.TrimSpace(m.filtered[m.cursor].Key)
			if selectedKey != "" {
				var added bool
				m.paletteState.Favorites, added = toggleListKey(m.paletteState.Favorites, selectedKey, true)
				if !added {
					// Pinned is a subset of favorites.
					m.paletteState.Pinned = removeListKey(m.paletteState.Pinned, selectedKey)
				}
				m.buildVisualOrder()
				return *m, m.savePaletteState()
			}
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
		return *m, cmd
	}

	return *m, nil
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
		return *m, tea.Quit

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

	return *m, nil
}

func (m *Model) updateFiltered() {
	prevKey := ""
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		prevKey = m.filtered[m.cursor].Key
	}

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

	// Preserve selection when filtering, if possible.
	if prevKey != "" {
		for i, cmd := range m.filtered {
			if cmd.Key == prevKey {
				m.cursor = i
				break
			}
		}
	}

	// Keep cursor in bounds
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) cursorVisualPos() int {
	for pos, idx := range m.visualOrder {
		if idx == m.cursor {
			return pos
		}
	}
	return 0
}

// visualPosToLineNum converts a visual position (index in visualOrder) to an
// approximate line number in the rendered list, accounting for section headers
// and blank lines between sections.
func (m *Model) visualPosToLineNum(pos int) int {
	if pos < 0 || len(m.visualOrder) == 0 {
		return 0
	}
	if pos >= len(m.visualOrder) {
		pos = len(m.visualOrder) - 1
	}

	// Count how many items are in pinned and recents sections
	pinnedCount := 0
	recentsCount := 0

	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool)
	for _, k := range m.paletteState.Pinned {
		if idx, ok := idxByKey[k]; ok && !used[idx] {
			used[idx] = true
			pinnedCount++
		}
	}
	for _, k := range m.recents {
		if idx, ok := idxByKey[k]; ok && !used[idx] {
			used[idx] = true
			recentsCount++
		}
	}

	// Calculate which section the position falls into and the line offset
	lineNum := 0
	itemsBeforePos := 0

	// Pinned section: header + items + blank
	if pinnedCount > 0 {
		lineNum++ // header
		if pos < pinnedCount {
			return lineNum + pos
		}
		lineNum += pinnedCount + 1 // items + blank
		itemsBeforePos = pinnedCount
	}

	// Recents section: header + items + blank
	if recentsCount > 0 {
		lineNum++ // header
		if pos < itemsBeforePos+recentsCount {
			return lineNum + (pos - itemsBeforePos)
		}
		lineNum += recentsCount + 1 // items + blank
		itemsBeforePos += recentsCount
	}

	// Category sections: each has header + items + blank
	// Estimate categories from remaining items
	remainingItems := len(m.visualOrder) - itemsBeforePos
	if remainingItems > 0 && pos >= itemsBeforePos {
		posInCategories := pos - itemsBeforePos
		// Estimate ~4 items per category on average, with 2 extra lines per category
		estimatedCategories := (posInCategories / 4) + 1
		lineNum += posInCategories + (estimatedCategories * 2)
	}

	return lineNum
}

// ensureCursorVisible scrolls the viewport if necessary to keep the cursor visible.
func (m *Model) ensureCursorVisible() {
	pos := m.cursorVisualPos()
	linePos := m.visualPosToLineNum(pos)

	// If cursor is above the visible area, scroll up
	if linePos < m.listViewport.YOffset {
		m.listViewport.SetYOffset(linePos)
	}

	// If cursor is below the visible area, scroll down
	// Leave a small margin at the bottom
	visibleBottom := m.listViewport.YOffset + m.listViewport.Height - 2
	if linePos > visibleBottom {
		newOffset := linePos - m.listViewport.Height + 3
		if newOffset < 0 {
			newOffset = 0
		}
		m.listViewport.SetYOffset(newOffset)
	}
}

// buildVisualOrder creates a mapping from visual position to filtered index.
// Items are grouped by category, so the visual order differs from the slice order.
func (m *Model) buildVisualOrder() {
	m.visualOrder = nil
	if len(m.filtered) == 0 {
		return
	}

	// Key → index for filtered commands (keys are unique after config merge/dedupe).
	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool, len(m.filtered))
	appendKeyOrder := func(keys []string) {
		for _, k := range keys {
			idx, ok := idxByKey[k]
			if !ok || used[idx] {
				continue
			}
			used[idx] = true
			m.visualOrder = append(m.visualOrder, idx)
		}
	}

	// Pinned first, then recents, then the remaining categories.
	appendKeyOrder(m.paletteState.Pinned)
	appendKeyOrder(m.recents)

	// Group remaining by category (same logic as renderCommandList)
	categories := make(map[string][]int)
	categoryOrder := []string{}
	for i, cmd := range m.filtered {
		if used[i] {
			continue
		}
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	for _, cat := range categoryOrder {
		m.visualOrder = append(m.visualOrder, categories[cat]...)
	}
}

func (m *Model) send() (tea.Model, tea.Cmd) {
	start := time.Now()
	if m.selected == nil {
		return *m, nil
	}

	panes, err := tmux.GetPanes(m.session)
	if err != nil {
		m.err = err
		m.recordHistory(nil, start, err)
		return *m, tea.Quit
	}

	prompt := m.selected.Prompt
	count := 0
	var targetPanes []int

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
			// Use agent-aware send method which handles Codex/Gemini multi-line quirks
			// by using buffer-based paste instead of send-keys when content has newlines
			if err := tmux.SendKeysForAgent(p.ID, prompt, true, p.Type); err != nil {
				m.err = err
				m.recordHistory(targetPanes, start, err)
				return *m, tea.Quit
			}
			count++
			targetPanes = append(targetPanes, p.Index)
		}
	}

	m.recordHistory(targetPanes, start, nil)
	m.sent = true
	m.sentCount = count
	m.quitting = true
	return *m, tea.Quit
}

func (m *Model) recordHistory(targetPanes []int, start time.Time, err error) {
	entry := history.NewEntry(m.session, intsToStrings(targetPanes), m.selected.Prompt, history.SourcePalette)
	entry.Template = m.selected.Key
	entry.DurationMs = int(time.Since(start) / time.Millisecond)
	if err == nil {
		entry.SetSuccess()
	} else {
		entry.SetError(err)
	}
	_ = history.Append(entry)
}

func intsToStrings(ints []int) []string {
	out := make([]string, 0, len(ints))
	for _, v := range ints {
		out = append(out, fmt.Sprintf("%d", v))
	}
	return out
}

func toggleListKey(list []string, key string, prepend bool) ([]string, bool) {
	if key == "" {
		return list, false
	}
	for i, v := range list {
		if v == key {
			// Remove
			out := make([]string, 0, len(list)-1)
			out = append(out, list[:i]...)
			out = append(out, list[i+1:]...)
			return out, false
		}
	}

	// Add
	if prepend {
		return append([]string{key}, list...), true
	}
	return append(list, key), true
}

func removeListKey(list []string, key string) []string {
	if key == "" || len(list) == 0 {
		return list
	}
	out := list[:0]
	for _, v := range list {
		if v == key {
			continue
		}
		out = append(out, v)
	}
	// out aliases list's backing array; copy to avoid surprising retention if callers append.
	return append([]string(nil), out...)
}

func ensureListKey(list []string, key string, prepend bool) []string {
	if key == "" {
		return list
	}
	for _, v := range list {
		if v == key {
			return list
		}
	}
	if prepend {
		return append([]string{key}, list...)
	}
	return append(list, key)
}

// View implements tea.Model
func (m Model) View() string {
	if m.showHelp {
		maxWidth := 70
		if m.width > 0 && m.width-4 < maxWidth {
			maxWidth = m.width - 4
		}
		if maxWidth < 20 {
			maxWidth = 20
		}

		helpOverlay := components.HelpOverlay(components.HelpOverlayOptions{
			Title:    "Palette Shortcuts",
			Sections: components.PaletteHelpSections(),
			MaxWidth: maxWidth,
		})
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, helpOverlay)
	}

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

	// Calculate layout dimensions using shared tiers and split proportions
	const (
		minColumnWidth  = 35  // Minimum column width
		maxListWidth    = 70  // Maximum list width
		maxPreviewWidth = 100 // Maximum preview width
	)

	showSplitView := m.tier >= layout.TierSplit
	var listWidth, previewWidth int

	if !showSplitView {
		listWidth = m.width - 4
		previewWidth = 0
	} else {
		left, right := layout.SplitProportions(m.width)
		listWidth = left - 2 // borders/padding allowance
		previewWidth = right - 2

		if listWidth > maxListWidth {
			listWidth = maxListWidth
		}
		if previewWidth > maxPreviewWidth {
			previewWidth = maxPreviewWidth
		}
	}

	// Ensure minimums
	if listWidth < minColumnWidth {
		listWidth = minColumnWidth
	}
	if showSplitView && previewWidth < minColumnWidth {
		previewWidth = minColumnWidth
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
	// RESPONSIVE LAYOUT: Adapts to terminal width
	// ═══════════════════════════════════════════════════════════════
	listContent := m.renderCommandList(listWidth - 4)

	// Calculate list box height
	listBoxHeight := m.height - 14
	if listBoxHeight < 5 {
		listBoxHeight = 5
	}

	// Update viewport content and dimensions for scrolling
	m.listViewport.Width = listWidth - 6 // Account for borders and padding
	m.listViewport.Height = listBoxHeight - 2
	m.listViewport.SetContent(listContent)

	// Render scroll indicator if content overflows
	scrollIndicator := ""
	if m.listViewport.TotalLineCount() > m.listViewport.Height {
		pct := m.listViewport.ScrollPercent() * 100
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		if pct < 1 {
			scrollIndicator = scrollStyle.Render(" ↓ scroll")
		} else if pct > 99 {
			scrollIndicator = scrollStyle.Render(" ↑ scroll")
		} else {
			scrollIndicator = scrollStyle.Render(fmt.Sprintf(" %.0f%%", pct))
		}
	}

	// List box with subtle glow - use viewport content
	listBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface2).
		Width(listWidth - 2).
		Padding(1, 1)

	var columns string
	if showSplitView {
		// Show preview alongside list on wider displays
		previewContent := m.renderPreview(previewWidth - 4)

		// Preview box with accent border
		previewBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Blue).
			Width(previewWidth-2).
			Height(listBoxHeight).
			Padding(1, 1)

		// Join columns horizontally
		columns = lipgloss.JoinHorizontal(
			lipgloss.Top,
			listBox.Render(m.listViewport.View()+scrollIndicator),
			"  ",
			previewBox.Render(previewContent),
		)
	} else {
		// Narrow display: list only (preview shown on selection)
		columns = listBox.Render(m.listViewport.View() + scrollIndicator)
	}

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
		return components.EmptyState("No commands match your filter", width)
	}

	var lines []string
	query := strings.TrimSpace(m.filter.Value())

	// Index commands by key (keys are unique after config merge/dedupe).
	idxByKey := make(map[string]int, len(m.filtered))
	for i, cmd := range m.filtered {
		if cmd.Key != "" {
			idxByKey[cmd.Key] = i
		}
	}

	used := make(map[int]bool, len(m.filtered))
	resolveKeyOrder := func(keys []string) []int {
		out := make([]int, 0, len(keys))
		for _, k := range keys {
			idx, ok := idxByKey[k]
			if !ok || used[idx] {
				continue
			}
			used[idx] = true
			out = append(out, idx)
		}
		return out
	}

	pinned := resolveKeyOrder(m.paletteState.Pinned)
	recents := resolveKeyOrder(m.recents)

	// Remaining grouped by category.
	categories := make(map[string][]int)
	categoryOrder := []string{}
	for i, cmd := range m.filtered {
		if used[i] {
			continue
		}
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		if _, exists := categories[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categories[cat] = append(categories[cat], i)
	}

	renderHeader := func(icon, title string) {
		header := strings.TrimSpace(icon + " " + title)
		lines = append(lines, styles.GradientText(header, string(t.Lavender), string(t.Mauve)))
	}

	isFavorite := func(key string) bool {
		for _, v := range m.paletteState.Favorites {
			if v == key {
				return true
			}
		}
		return false
	}
	isPinned := func(key string) bool {
		for _, v := range m.paletteState.Pinned {
			if v == key {
				return true
			}
		}
		return false
	}

	pinStyle := lipgloss.NewStyle().Foreground(t.Mauve).Bold(true)
	favStyle := lipgloss.NewStyle().Foreground(t.Yellow).Bold(true)

	renderItem := func(idx int, itemNum int) {
		cmd := m.filtered[idx]
		isSelected := idx == m.cursor

		var line strings.Builder

		// Selection indicator with animation
		if isSelected {
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

		// Marker column (pinned or favorite)
		marker := " "
		if isPinned(cmd.Key) {
			marker = pinStyle.Render(ic.Target)
		} else if isFavorite(cmd.Key) {
			marker = favStyle.Render(ic.Star)
		}
		line.WriteString(marker + " ")

		// Item label with selection highlight
		labelBudget := width - 10
		if labelBudget < 10 {
			labelBudget = 10
		}
		label := layout.TruncateWidthDefault(cmd.Label, labelBudget)

		if isSelected {
			line.WriteString(styles.GradientText(label, string(t.Pink), string(t.Rosewater)))
		} else {
			labelStyle := lipgloss.NewStyle().Foreground(t.Text)
			matchStyle := lipgloss.NewStyle().Foreground(t.Mauve).Bold(true)
			line.WriteString(renderMatchHighlighted(label, query, labelStyle, matchStyle))
		}

		lines = append(lines, line.String())
	}

	itemNum := 0

	if len(pinned) > 0 {
		renderHeader(ic.Target, "Pinned")
		for _, idx := range pinned {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	if len(recents) > 0 {
		renderHeader(ic.Circle, "Recent")
		for _, idx := range recents {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	for _, cat := range categoryOrder {
		indices := categories[cat]
		catIcon := ic.CategoryIcon(cat)
		renderHeader(catIcon, cat)
		for _, idx := range indices {
			itemNum++
			renderItem(idx, itemNum)
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

func renderMatchHighlighted(text, query string, baseStyle, matchStyle lipgloss.Style) string {
	query = strings.TrimSpace(query)
	if query == "" || text == "" {
		return baseStyle.Render(text)
	}

	runes := []rune(text)
	needle := []rune(query)
	if len(needle) > len(runes) {
		return baseStyle.Render(text)
	}

	for i := 0; i <= len(runes)-len(needle); i++ {
		if strings.EqualFold(string(runes[i:i+len(needle)]), query) {
			return baseStyle.Render(string(runes[:i])) +
				matchStyle.Render(string(runes[i:i+len(needle)])) +
				baseStyle.Render(string(runes[i+len(needle):]))
		}
	}

	return baseStyle.Render(text)
}

func (m Model) renderPreview(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.filtered) == 0 || m.cursor >= len(m.filtered) {
		return components.RenderState(components.StateOptions{
			Kind:    components.StateEmpty,
			Message: "Select a command to preview",
			Width:   width,
			Align:   lipgloss.Center,
		})
	}

	cmd := m.filtered[m.cursor]

	var b strings.Builder

	// Title with gradient
	titleText := ic.Send + " " + cmd.Label
	b.WriteString(styles.GradientText(titleText, string(t.Blue), string(t.Sapphire)) + "\n")
	b.WriteString(styles.GradientDivider(width, string(t.Surface2), string(t.Surface1)) + "\n\n")

	// Key + Category badges
	var badges []string
	if cmd.Key != "" {
		badges = append(badges, styles.TextBadge("key: "+cmd.Key, t.Surface0, t.Text))
	}
	if cmd.Category != "" {
		// Only use agent badge for known agent types, otherwise create a generic category badge.
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
		badges = append(badges, badge)
	}
	if len(badges) > 0 {
		b.WriteString(styles.BadgeGroup(badges...) + "\n")
	}

	// Target summary + prompt metadata (reduce misfires)
	if targets := m.renderTargetSummaryBadges(); targets != "" {
		labelStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		b.WriteString(labelStyle.Render("Targets: ") + targets + "\n")
	}

	lineCount := 0
	if strings.TrimSpace(cmd.Prompt) != "" {
		lineCount = strings.Count(cmd.Prompt, "\n") + 1
	}
	charCount := utf8.RuneCountInString(cmd.Prompt)
	meta := styles.BadgeGroup(
		styles.TextBadge(fmt.Sprintf("%d lines", lineCount), t.Surface0, t.Text),
		styles.TextBadge(fmt.Sprintf("%d chars", charCount), t.Surface0, t.Text),
	)
	b.WriteString(meta + "\n")

	if warn := m.renderSafetyNudges(cmd.Prompt, lineCount, charCount); warn != "" {
		b.WriteString(warn + "\n")
	}

	b.WriteString("\n")

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

func (m Model) renderTargetSummaryBadges() string {
	t := m.theme
	ic := m.icons

	labelWithIcon := func(icon, fallback, label string, count *int) string {
		prefix := strings.TrimSpace(icon)
		if prefix == "" {
			prefix = fallback
		}
		if prefix != "" {
			prefix = prefix + " "
		}
		if count == nil {
			return prefix + label
		}
		return fmt.Sprintf("%s%s %d", prefix, label, *count)
	}

	var (
		all    *int
		claude *int
		codex  *int
		gemini *int
	)
	if m.paneCountsKnown {
		all = &m.paneCounts.totalAgents
		claude = &m.paneCounts.claude
		codex = &m.paneCounts.codex
		gemini = &m.paneCounts.gemini
	}

	badges := []string{
		styles.TextBadge(labelWithIcon(ic.All, "", "all", all), t.Green, t.Base),
		styles.TextBadge(labelWithIcon(ic.Claude, "", "cc", claude), t.Claude, t.Base),
		styles.TextBadge(labelWithIcon(ic.Codex, "", "cod", codex), t.Codex, t.Base),
		styles.TextBadge(labelWithIcon(ic.Gemini, "", "gmi", gemini), t.Gemini, t.Base),
	}

	return styles.BadgeGroup(badges...)
}

func (m Model) samplePaneTitlesForTargetKey(key string, max int) []string {
	if !m.paneCountsKnown || max <= 0 {
		return nil
	}

	var src []string
	switch key {
	case "1":
		src = m.paneCounts.allSamples
	case "2":
		src = m.paneCounts.claudeSamples
	case "3":
		src = m.paneCounts.codexSamples
	case "4":
		src = m.paneCounts.geminiSamples
	}

	if len(src) > max {
		return src[:max]
	}
	return src
}

func (m Model) renderSafetyNudges(prompt string, lineCount, charCount int) string {
	t := m.theme
	ic := m.icons
	lower := strings.ToLower(prompt)

	warnIcon := strings.TrimSpace(ic.Warning)
	if warnIcon == "" {
		warnIcon = "!"
	}

	type warn struct {
		label string
		bg    lipgloss.Color
		fg    lipgloss.Color
	}

	var warns []warn
	add := func(label string, bg, fg lipgloss.Color) {
		warns = append(warns, warn{label: label, bg: bg, fg: fg})
	}

	// Highest-signal warnings first.
	if strings.Contains(lower, "rm -rf") ||
		strings.Contains(lower, "git reset --hard") ||
		strings.Contains(lower, "git clean -fd") ||
		strings.Contains(lower, "delete all") ||
		strings.Contains(lower, "drop table") ||
		strings.Contains(lower, "truncate") {
		add(warnIcon+" destructive", t.Error, t.Base)
	}

	if strings.Contains(lower, "sudo ") || strings.HasPrefix(lower, "sudo") {
		add(warnIcon+" sudo", t.Warning, t.Base)
	}

	if strings.Contains(lower, "curl ") ||
		strings.Contains(lower, "wget ") ||
		strings.Contains(lower, "go get ") ||
		strings.Contains(lower, "npm install") ||
		strings.Contains(lower, "pip install") ||
		strings.Contains(lower, "brew install") {
		add(warnIcon+" network", t.Warning, t.Base)
	}

	if strings.Contains(lower, "git push") ||
		strings.Contains(lower, "git commit") ||
		strings.Contains(lower, "commit and push") ||
		strings.Contains(lower, "push.") ||
		strings.Contains(lower, "push ") {
		add(warnIcon+" git", t.Warning, t.Base)
	}

	if lineCount >= 40 || charCount >= 4000 {
		add(warnIcon+" long prompt", t.Surface1, t.Text)
	}

	if len(warns) == 0 {
		return ""
	}
	if len(warns) > 3 {
		warns = warns[:3]
	}

	var badges []string
	for _, w := range warns {
		badges = append(badges, styles.TextBadge(w.label, w.bg, w.fg))
	}
	return styles.BadgeGroup(badges...)
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
		{"Esc", "back"},
	}

	// Show scroll hint if there are more items than visible
	if m.tier >= layout.TierSplit && m.listViewport.TotalLineCount() > m.listViewport.Height {
		items = append(items, struct {
			key  string
			desc string
		}{"pgup/dn", "scroll"})
	}

	if m.tier >= layout.TierWide {
		items = append(items,
			struct {
				key  string
				desc string
			}{"ctrl+p", "pin"},
			struct {
				key  string
				desc string
			}{"ctrl+f", "favorite"},
			struct {
				key  string
				desc string
			}{"q/ctrl+c", "quit"},
			struct {
				key  string
				desc string
			}{"?", "help"},
		)
	}

	if m.tier >= layout.TierUltra {
		items = append(items,
			struct {
				key  string
				desc string
			}{"Enter→", "targets 1-4"},
			struct {
				key  string
				desc string
			}{"type", "filter commands"},
		)
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

	// Responsive box dimensions based on layout mode
	layoutMode := styles.GetLayoutMode(m.width)
	var boxWidth int
	switch layoutMode {
	case styles.LayoutUltraWide:
		boxWidth = 80
	case styles.LayoutSpacious:
		boxWidth = 70
	case styles.LayoutDefault:
		boxWidth = 60
	default:
		boxWidth = m.width - 10
		if boxWidth < 40 {
			boxWidth = 40
		}
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
		// Count suffixes (best-effort)
		countSuffix := ""
		if m.paneCountsKnown {
			switch target.key {
			case "1":
				countSuffix = fmt.Sprintf(" (%d)", m.paneCounts.totalAgents)
			case "2":
				countSuffix = fmt.Sprintf(" (%d)", m.paneCounts.claude)
			case "3":
				countSuffix = fmt.Sprintf(" (%d)", m.paneCounts.codex)
			case "4":
				countSuffix = fmt.Sprintf(" (%d)", m.paneCounts.gemini)
			}
		}

		labelText := target.label
		if target.key == "1" {
			star := strings.TrimSpace(ic.Star)
			if star == "" {
				star = "*"
			}
			labelText = star + " " + labelText
		}

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
			Width(18)

		// Description
		descStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)

		line := fmt.Sprintf("  %s  %s  %s %s",
			keyBadge,
			iconStyled,
			labelStyle.Render(labelText+countSuffix),
			descStyle.Render(target.desc))

		b.WriteString(line + "\n")

		// Representative pane samples (width-tier aware, avoid wrapping).
		maxSamples := 0
		switch {
		case m.tier >= layout.TierWide:
			if target.key == "1" {
				maxSamples = 3
			} else {
				maxSamples = 2
			}
		case m.tier >= layout.TierSplit:
			maxSamples = 1
		}

		samples := m.samplePaneTitlesForTargetKey(target.key, maxSamples)
		if len(samples) > 0 {
			sampleText := "e.g. " + strings.Join(samples, ", ")
			sampleIndent := "      "
			maxWidth := boxWidth - 2 - len(sampleIndent)
			if maxWidth < 10 {
				maxWidth = 10
			}
			sampleText = layout.TruncateWidthDefault(sampleText, maxWidth)
			sampleStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
			b.WriteString("  " + sampleIndent + sampleStyle.Render(sampleText) + "\n")
		}

		b.WriteString("\n")
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
		{"?", "help"},
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
