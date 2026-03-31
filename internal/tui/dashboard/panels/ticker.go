package panels

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// TickerData holds the data displayed in the ticker
type TickerData struct {
	// Fleet status
	TotalAgents   int
	ActiveAgents  int
	ClaudeCount   int
	CodexCount    int
	GeminiCount   int
	CursorCount   int
	WindsurfCount int
	AiderCount    int
	OllamaCount   int
	UserCount     int

	// Alerts
	CriticalAlerts int
	WarningAlerts  int
	InfoAlerts     int

	// Beads
	ReadyBeads      int
	InProgressBeads int
	BlockedBeads    int

	// Mail
	UnreadMessages   int
	ActiveLocks      int
	MailConnected    bool
	MailAvailable    bool // HTTP API reachable
	MailArchiveFound bool // Archive directory exists (fallback detection)

	// Checkpoints
	CheckpointCount  int
	CheckpointStatus string // "recent", "stale", "old", "none"

	// UBS Bug Scanner
	BugsCritical int
	BugsWarning  int
	BugsInfo     int
	BugsScanned  bool // Whether a scan has been run
}

// TickerPanel displays a scrolling status bar at the bottom of the dashboard
type TickerPanel struct {
	width    int
	height   int
	focused  bool
	data     TickerData
	theme    theme.Theme
	offset   int // Current scroll offset for animation
	animTick int // Animation tick counter
}

// NewTickerPanel creates a new ticker panel
func NewTickerPanel() *TickerPanel {
	return &TickerPanel{
		theme:  theme.Current(),
		height: 1, // Ticker is typically single-line
	}
}

// Init implements tea.Model
func (m *TickerPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *TickerPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

// SetSize sets the panel dimensions
func (m *TickerPanel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Focus marks the panel as focused
func (m *TickerPanel) Focus() {
	m.focused = true
}

// Blur marks the panel as unfocused
func (m *TickerPanel) Blur() {
	m.focused = false
}

// SetData updates the panel data
func (m *TickerPanel) SetData(data TickerData) {
	m.data = data
}

// SetAnimTick updates the animation tick for scrolling
func (m *TickerPanel) SetAnimTick(tick int) {
	m.animTick = tick
	// Update scroll offset every 2 ticks (~200ms at 100ms tick rate)
	m.offset = tick / 2
}

// View renders the panel
func (m *TickerPanel) View() string {
	t := m.theme

	if m.width <= 0 {
		return ""
	}

	// Build ticker segments as plain text first (no ANSI codes)
	// This allows proper scrolling without corrupting escape sequences
	plainSegments := m.buildPlainSegments()
	plainText := strings.Join(plainSegments, " | ")

	// Calculate visible portion based on scroll offset using plain text
	visiblePlain := m.scrollPlainText(plainText)

	// Now style the visible portion
	// We need to re-apply styling to the visible text
	styledText := m.styleVisibleText(visiblePlain)
	if badge := m.overflowBadge([]rune(plainText)); badge != "" {
		styledText = m.attachOverflowBadge(styledText, badge)
	}

	// Style the ticker bar container
	tickerStyle := lipgloss.NewStyle().
		Width(m.width).
		Background(t.Surface0).
		Foreground(t.Text)

	if m.focused {
		tickerStyle = tickerStyle.
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(t.Primary)
	}

	return tickerStyle.Render(styledText)
}

func (m *TickerPanel) overflowBadge(text []rune) string {
	if len(text) <= m.width || m.width < 10 {
		return ""
	}

	cycle := len(text) + 4
	progress := 0
	if cycle > 0 {
		progress = (m.offset % cycle) * 100 / cycle
	}

	t := m.theme
	return lipgloss.NewStyle().
		Foreground(t.Base).
		Background(t.Surface2).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("<>%2d%%", progress))
}

func (m *TickerPanel) attachOverflowBadge(text, badge string) string {
	badgeWidth := lipgloss.Width(badge)
	if badgeWidth <= 0 || badgeWidth >= m.width {
		return badge
	}

	available := m.width - badgeWidth - 1
	if available < 1 {
		return badge
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(available).Render(text),
		" ",
		badge,
	)
}

// buildPlainSegments creates plain text segments without ANSI styling
func (m *TickerPanel) buildPlainSegments() []string {
	var segments []string

	// Fleet segment (plain)
	fleetSegment := m.buildPlainFleetSegment()
	segments = append(segments, fleetSegment)

	// Alerts segment (plain)
	alertsSegment := m.buildPlainAlertsSegment()
	segments = append(segments, alertsSegment)

	// Beads segment (plain)
	beadsSegment := m.buildPlainBeadsSegment()
	segments = append(segments, beadsSegment)

	// Mail segment (plain)
	mailSegment := m.buildPlainMailSegment()
	segments = append(segments, mailSegment)

	// Checkpoint segment (plain)
	cpSegment := m.buildPlainCheckpointSegment()
	segments = append(segments, cpSegment)

	// Bugs segment (plain)
	bugsSegment := m.buildPlainBugsSegment()
	segments = append(segments, bugsSegment)

	return segments
}

// buildPlainFleetSegment creates plain text fleet segment
func (m *TickerPanel) buildPlainFleetSegment() string {
	var parts []string

	activeStatus := fmt.Sprintf("Fleet: %d active / %d total", m.data.ActiveAgents, m.data.TotalAgents)
	parts = append(parts, activeStatus)

	if m.data.TotalAgents > 0 {
		var agentParts []string
		if m.data.ClaudeCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("C:%d", m.data.ClaudeCount))
		}
		if m.data.CodexCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("X:%d", m.data.CodexCount))
		}
		if m.data.GeminiCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("G:%d", m.data.GeminiCount))
		}
		if m.data.CursorCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("Cur:%d", m.data.CursorCount))
		}
		if m.data.WindsurfCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("W:%d", m.data.WindsurfCount))
		}
		if m.data.AiderCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("A:%d", m.data.AiderCount))
		}
		if m.data.OllamaCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("Oll:%d", m.data.OllamaCount))
		}
		if m.data.UserCount > 0 {
			agentParts = append(agentParts, fmt.Sprintf("U:%d", m.data.UserCount))
		}
		if len(agentParts) > 0 {
			parts = append(parts, "("+strings.Join(agentParts, " ")+")")
		}
	}

	return strings.Join(parts, " ")
}

// buildPlainAlertsSegment creates plain text alerts segment
func (m *TickerPanel) buildPlainAlertsSegment() string {
	totalAlerts := m.data.CriticalAlerts + m.data.WarningAlerts + m.data.InfoAlerts

	if totalAlerts == 0 {
		return "Alerts: OK"
	}

	var alertParts []string
	if m.data.CriticalAlerts > 0 {
		alertParts = append(alertParts, fmt.Sprintf("%d!", m.data.CriticalAlerts))
	}
	if m.data.WarningAlerts > 0 {
		alertParts = append(alertParts, fmt.Sprintf("%dw", m.data.WarningAlerts))
	}
	if m.data.InfoAlerts > 0 {
		alertParts = append(alertParts, fmt.Sprintf("%di", m.data.InfoAlerts))
	}

	return "Alerts: " + strings.Join(alertParts, "/")
}

// buildPlainBeadsSegment creates plain text beads segment
func (m *TickerPanel) buildPlainBeadsSegment() string {
	var beadParts []string

	beadParts = append(beadParts, fmt.Sprintf("R:%d", m.data.ReadyBeads))

	if m.data.InProgressBeads > 0 {
		beadParts = append(beadParts, fmt.Sprintf("I:%d", m.data.InProgressBeads))
	}
	if m.data.BlockedBeads > 0 {
		beadParts = append(beadParts, fmt.Sprintf("B:%d", m.data.BlockedBeads))
	}

	return "Beads: " + strings.Join(beadParts, " ")
}

// buildPlainMailSegment creates plain text mail segment
func (m *TickerPanel) buildPlainMailSegment() string {
	// Connected via HTTP - full functionality available
	if m.data.MailConnected {
		var mailParts []string
		if m.data.UnreadMessages > 0 {
			mailParts = append(mailParts, fmt.Sprintf("%d unread", m.data.UnreadMessages))
		} else {
			mailParts = append(mailParts, "0 unread")
		}
		if m.data.ActiveLocks > 0 {
			mailParts = append(mailParts, fmt.Sprintf("%d locks", m.data.ActiveLocks))
		}
		return "Mail: " + strings.Join(mailParts, " ")
	}

	// Fallback: archive detected (Agent Mail running via MCP stdio, not HTTP)
	if m.data.MailArchiveFound {
		return "Mail: detected"
	}

	// Neither HTTP nor archive found
	return "Mail: offline"
}

// buildPlainCheckpointSegment creates plain text checkpoint segment
func (m *TickerPanel) buildPlainCheckpointSegment() string {
	if m.data.CheckpointCount == 0 {
		return "Ckpt: none"
	}

	switch m.data.CheckpointStatus {
	case "recent":
		return fmt.Sprintf("Ckpt: %d recent", m.data.CheckpointCount)
	case "stale":
		return fmt.Sprintf("Ckpt: %d stale", m.data.CheckpointCount)
	case "old":
		return fmt.Sprintf("Ckpt: %d old", m.data.CheckpointCount)
	default:
		return fmt.Sprintf("Ckpt: %d", m.data.CheckpointCount)
	}
}

// buildPlainBugsSegment creates plain text UBS bugs segment
func (m *TickerPanel) buildPlainBugsSegment() string {
	if !m.data.BugsScanned {
		return "Bugs: --"
	}

	total := m.data.BugsCritical + m.data.BugsWarning
	if total == 0 {
		return "Bugs: OK"
	}

	var parts []string
	if m.data.BugsCritical > 0 {
		parts = append(parts, fmt.Sprintf("%d!", m.data.BugsCritical))
	}
	if m.data.BugsWarning > 0 {
		parts = append(parts, fmt.Sprintf("%dw", m.data.BugsWarning))
	}

	return "Bugs: " + strings.Join(parts, "/")
}

// scrollPlainText handles the horizontal scrolling animation on plain text
func (m *TickerPanel) scrollPlainText(text string) string {
	textRunes := []rune(text)
	textLen := len(textRunes)

	// If text fits in width, center it
	if textLen <= m.width {
		padding := (m.width - textLen) / 2
		return strings.Repeat(" ", padding) + text + strings.Repeat(" ", m.width-textLen-padding)
	}

	// For scrolling, duplicate text for seamless loop
	paddedText := text + "    " + text
	paddedRunes := []rune(paddedText)
	paddedLen := len(paddedRunes)

	// Calculate scroll position (wrap around)
	scrollPos := m.offset % (textLen + 4)

	// Extract visible portion
	endPos := scrollPos + m.width
	if endPos > paddedLen {
		endPos = paddedLen
	}

	visible := string(paddedRunes[scrollPos:endPos])

	// Pad if needed
	visibleLen := len([]rune(visible))
	if visibleLen < m.width {
		visible += strings.Repeat(" ", m.width-visibleLen)
	}

	return visible
}

// styleVisibleText applies styling to visible plain text
// This is a simplified styling that applies colors to known keywords
func (m *TickerPanel) styleVisibleText(text string) string {
	t := m.theme

	// Apply styling to known patterns
	// Note: This is a simplified approach - it styles keywords in-place
	result := text

	// Style "Fleet:" label
	fleetLabel := lipgloss.NewStyle().Foreground(t.Blue).Bold(true).Render("Fleet:")
	result = strings.ReplaceAll(result, "Fleet:", fleetLabel)

	// Style "Alerts:" label
	alertsLabel := lipgloss.NewStyle().Foreground(t.Pink).Bold(true).Render("Alerts:")
	result = strings.ReplaceAll(result, "Alerts:", alertsLabel)

	// Style "Beads:" label
	beadsLabel := lipgloss.NewStyle().Foreground(t.Green).Bold(true).Render("Beads:")
	result = strings.ReplaceAll(result, "Beads:", beadsLabel)

	// Style "Mail:" label
	mailLabel := lipgloss.NewStyle().Foreground(t.Lavender).Bold(true).Render("Mail:")
	result = strings.ReplaceAll(result, "Mail:", mailLabel)

	// Style "Ckpt:" label
	ckptLabel := lipgloss.NewStyle().Foreground(t.Teal).Bold(true).Render("Ckpt:")
	result = strings.ReplaceAll(result, "Ckpt:", ckptLabel)

	// Style "Bugs:" label
	bugsLabel := lipgloss.NewStyle().Foreground(t.Peach).Bold(true).Render("Bugs:")
	result = strings.ReplaceAll(result, "Bugs:", bugsLabel)

	// Style separators
	sepStyled := lipgloss.NewStyle().Foreground(t.Surface2).Render(" | ")
	result = strings.ReplaceAll(result, " | ", sepStyled)

	// Style "OK" in green
	okStyled := lipgloss.NewStyle().Foreground(t.Green).Render("OK")
	result = strings.ReplaceAll(result, " OK", " "+okStyled)

	// Style "offline" in dim
	offlineStyled := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Render("offline")
	result = strings.ReplaceAll(result, "offline", offlineStyled)

	// Style "detected" in yellow (MCP-only mode, partial functionality)
	detectedStyled := lipgloss.NewStyle().Foreground(t.Yellow).Render("detected")
	result = strings.ReplaceAll(result, "detected", detectedStyled)

	return result
}

// GetHeight returns the preferred height for the ticker (single line)
func (m *TickerPanel) GetHeight() int {
	return 1
}
