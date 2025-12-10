// Package dashboard provides a stunning visual session dashboard
package dashboard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DashboardTickMsg is sent for animation updates
type DashboardTickMsg time.Time

// RefreshMsg triggers a refresh of session data
type RefreshMsg struct{}

// StatusUpdateMsg is sent when status detection completes
type StatusUpdateMsg struct {
	Statuses []status.AgentStatus
	Time     time.Time
}

// HealthCheckMsg is sent when health check (bv drift) completes
type HealthCheckMsg struct {
	Status  string // "ok", "warning", "critical", "no_baseline", "unavailable"
	Message string
}

// AgentMailUpdateMsg is sent when Agent Mail data is fetched
type AgentMailUpdateMsg struct {
	Available bool
	Connected bool
	Locks     int
	LockInfo  []AgentMailLockInfo
}

// Model is the session dashboard model
type Model struct {
	session  string
	panes    []tmux.Pane
	width    int
	height   int
	animTick int
	cursor   int
	quitting bool
	err      error

	// Stats
	claudeCount int
	codexCount  int
	geminiCount int
	userCount   int

	// Theme
	theme theme.Theme
	icons icons.IconSet

	// Compaction detection and recovery
	compaction *status.CompactionRecoveryIntegration

	// Per-pane status tracking
	paneStatus map[int]PaneStatus

	// Live status detection
	detector      *status.UnifiedDetector
	agentStatuses map[string]status.AgentStatus // keyed by pane ID
	lastRefresh   time.Time
	refreshPaused bool
	refreshCount  int

	// Auto-refresh configuration
	refreshInterval time.Duration

	// Health badge (bv drift status)
	healthStatus  string // "ok", "warning", "critical", "no_baseline", "unavailable"
	healthMessage string

	// Layout tier (narrow/split/wide/ultra)
	tier layout.Tier

	// Agent Mail integration
	agentMailAvailable bool
	agentMailConnected bool
	agentMailLocks     int                 // Active file reservations
	agentMailUnread    int                 // Unread message count (requires agent context)
	agentMailLockInfo  []AgentMailLockInfo // Lock details for display
}

// PaneStatus tracks the status of a pane including compaction state
type PaneStatus struct {
	LastCompaction *time.Time // When compaction was last detected
	RecoverySent   bool       // Whether recovery prompt was sent
	State          string     // "working", "idle", "error", "compacted"

	// Context usage tracking
	ContextTokens  int     // Estimated tokens used
	ContextLimit   int     // Context limit for the model
	ContextPercent float64 // Usage percentage (0-100+)
	ContextModel   string  // Model name for context limit lookup
}

// AgentMailLockInfo represents a file lock for dashboard display
type AgentMailLockInfo struct {
	PathPattern string
	AgentName   string
	Exclusive   bool
	ExpiresIn   string
}

// KeyMap defines dashboard keybindings
type KeyMap struct {
	Up             key.Binding
	Down           key.Binding
	Left           key.Binding
	Right          key.Binding
	Zoom           key.Binding
	Send           key.Binding
	Refresh        key.Binding
	Pause          key.Binding
	Quit           key.Binding
	ContextRefresh key.Binding // 'c' to refresh context data
	MailRefresh    key.Binding // 'm' to refresh Agent Mail data
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

// DefaultRefreshInterval is the default auto-refresh interval
const DefaultRefreshInterval = 2 * time.Second

var dashKeys = KeyMap{
	Up:             key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:           key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Left:           key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
	Right:          key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
	Zoom:           key.NewBinding(key.WithKeys("z", "enter"), key.WithHelp("z/enter", "zoom")),
	Send:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "send prompt")),
	Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:          key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause/resume auto-refresh")),
	Quit:           key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q/esc", "quit")),
	ContextRefresh: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "refresh context")),
	MailRefresh:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "refresh mail")),
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

// New creates a new dashboard model
func New(session string) Model {
	t := theme.Current()
	ic := icons.Current()

	return Model{
		session:         session,
		width:           80,
		height:          24,
		tier:            layout.TierForWidth(80),
		theme:           t,
		icons:           ic,
		compaction:      status.NewCompactionRecoveryIntegrationDefault(),
		paneStatus:      make(map[int]PaneStatus),
		detector:        status.NewDetector(),
		agentStatuses:   make(map[string]status.AgentStatus),
		refreshInterval: DefaultRefreshInterval,
		healthStatus:    "unknown",
		healthMessage:   "",
	}
}

// NewWithInterval creates a dashboard with custom refresh interval
func NewWithInterval(session string, interval time.Duration) Model {
	m := New(session)
	m.refreshInterval = interval
	return m
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tick(),
		m.fetchSessionDataWithOutputs(),
		m.fetchHealthStatus(),
		m.fetchStatuses(),
		m.fetchAgentMailStatus(),
	)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
		return DashboardTickMsg(t)
	})
}

// fetchHealthStatus performs the health check via bv
func (m Model) fetchHealthStatus() tea.Cmd {
	return func() tea.Msg {
		if !bv.IsInstalled() {
			return HealthCheckMsg{
				Status:  "unavailable",
				Message: "bv not installed",
			}
		}

		result := bv.CheckDrift()
		var status string
		switch result.Status {
		case bv.DriftOK:
			status = "ok"
		case bv.DriftWarning:
			status = "warning"
		case bv.DriftCritical:
			status = "critical"
		case bv.DriftNoBaseline:
			status = "no_baseline"
		default:
			status = "unknown"
		}

		return HealthCheckMsg{
			Status:  status,
			Message: result.Message,
		}
	}
}

// fetchAgentMailStatus fetches Agent Mail data (locks, connection status)
func (m Model) fetchAgentMailStatus() tea.Cmd {
	return func() tea.Msg {
		// Get project key from current working directory
		projectKey, err := os.Getwd()
		if err != nil {
			return AgentMailUpdateMsg{Available: false}
		}

		client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Check availability
		if !client.IsAvailable() {
			return AgentMailUpdateMsg{Available: false}
		}

		// Ensure project exists
		_, err = client.EnsureProject(ctx, projectKey)
		if err != nil {
			return AgentMailUpdateMsg{Available: true, Connected: false}
		}

		// Fetch file reservations
		var lockInfo []AgentMailLockInfo
		reservations, err := client.ListReservations(ctx, projectKey, "", true)
		if err == nil {
			for _, r := range reservations {
				expiresIn := ""
				if !r.ExpiresTS.IsZero() {
					remaining := time.Until(r.ExpiresTS)
					if remaining > 0 {
						if remaining < time.Minute {
							expiresIn = fmt.Sprintf("%ds", int(remaining.Seconds()))
						} else if remaining < time.Hour {
							expiresIn = fmt.Sprintf("%dm", int(remaining.Minutes()))
						} else {
							expiresIn = fmt.Sprintf("%dh%dm", int(remaining.Hours()), int(remaining.Minutes())%60)
						}
					} else {
						expiresIn = "expired"
					}
				}
				lockInfo = append(lockInfo, AgentMailLockInfo{
					PathPattern: r.PathPattern,
					AgentName:   r.AgentName,
					Exclusive:   r.Exclusive,
					ExpiresIn:   expiresIn,
				})
			}
		}

		return AgentMailUpdateMsg{
			Available: true,
			Connected: true,
			Locks:     len(lockInfo),
			LockInfo:  lockInfo,
		}
	}
}

func (m Model) refresh() tea.Cmd {
	return tea.Tick(m.refreshInterval, func(t time.Time) tea.Msg { return RefreshMsg{} })
}

// Helper struct to carry output data
type PaneOutputData struct {
	PaneIndex int
	Output    string
	AgentType string
}

type SessionDataWithOutputMsg struct {
	Panes   []tmux.Pane
	Outputs []PaneOutputData
	Err     error
}

func (m Model) fetchSessionDataWithOutputs() tea.Cmd {
	return func() tea.Msg {
		panes, err := tmux.GetPanes(m.session)
		if err != nil {
			return SessionDataWithOutputMsg{Err: err}
		}

		var outputs []PaneOutputData
		for _, pane := range panes {
			if pane.Type == tmux.AgentUser {
				continue
			}
			out, err := tmux.CapturePaneOutput(pane.ID, 50)
			if err == nil {
				outputs = append(outputs, PaneOutputData{
					PaneIndex: pane.Index,
					Output:    out,
					AgentType: string(pane.Type), // Simplified mapping
				})
			}
		}

		return SessionDataWithOutputMsg{Panes: panes, Outputs: outputs}
	}
}

// fetchStatuses runs unified status detection across all panes
func (m Model) fetchStatuses() tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.detector.DetectAll(m.session)
		if err != nil {
			// Keep UI responsive even if detection fails
			return StatusUpdateMsg{Statuses: nil, Time: time.Now()}
		}
		return StatusUpdateMsg{Statuses: statuses, Time: time.Now()}
	}
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidth(msg.Width)
		return m, nil

	case DashboardTickMsg:
		m.animTick++
		return m, m.tick()

	case RefreshMsg:
		// Trigger async fetch for pane data and status detection
		return m, tea.Batch(
			m.fetchSessionDataWithOutputs(),
			m.fetchStatuses(),
		)

	case SessionDataWithOutputMsg:
		if msg.Err != nil {
			m.err = msg.Err
		} else {
			m.panes = msg.Panes
			m.updateStats()

			// Process compaction checks and context tracking on the main thread using fetched outputs
			for _, data := range msg.Outputs {
				// Map type string to model name for context limits
				agentType := "unknown"
				modelName := ""
				switch data.AgentType {
				case string(tmux.AgentClaude):
					agentType = "claude"
					modelName = "opus" // Default to opus for Claude agents
				case string(tmux.AgentCodex):
					agentType = "codex"
					modelName = "gpt4" // Default to GPT-4 for Codex agents
				case string(tmux.AgentGemini):
					agentType = "gemini"
					modelName = "gemini" // Default Gemini
				}

				// Get or create pane status
				ps := m.paneStatus[data.PaneIndex]

				// Calculate context usage
				if data.Output != "" && modelName != "" {
					contextInfo := tokens.GetUsageInfo(data.Output, modelName)
					ps.ContextTokens = contextInfo.EstimatedTokens
					ps.ContextLimit = contextInfo.ContextLimit
					ps.ContextPercent = contextInfo.UsagePercent
					ps.ContextModel = modelName
				}

				// Compaction check
				event, recoverySent, _ := m.compaction.CheckAndRecover(data.Output, agentType, m.session, data.PaneIndex)

				if event != nil {
					now := time.Now()
					ps.LastCompaction = &now
					ps.RecoverySent = recoverySent
					ps.State = "compacted"
				}

				m.paneStatus[data.PaneIndex] = ps
			}
		}
		// Schedule next refresh
		return m, m.refresh()

	case StatusUpdateMsg:
		// Build index lookup for current panes
		paneIndexByID := make(map[string]int)
		for _, p := range m.panes {
			paneIndexByID[p.ID] = p.Index
		}

		for _, st := range msg.Statuses {
			idx, ok := paneIndexByID[st.PaneID]
			if !ok {
				continue
			}

			ps := m.paneStatus[idx]
			state := string(st.State)

			// Compaction warning should override idle/working but not errors
			if ps.LastCompaction != nil && state != string(status.StateError) {
				state = "compacted"
			}
			ps.State = state
			m.paneStatus[idx] = ps
			m.agentStatuses[st.PaneID] = st
		}
		m.lastRefresh = msg.Time
		return m, nil

	case HealthCheckMsg:
		m.healthStatus = msg.Status
		m.healthMessage = msg.Message
		return m, nil

	case AgentMailUpdateMsg:
		m.agentMailAvailable = msg.Available
		m.agentMailConnected = msg.Connected
		m.agentMailLocks = msg.Locks
		m.agentMailLockInfo = msg.LockInfo
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, dashKeys.Quit):
			m.quitting = true
			return m, tea.Quit

		case key.Matches(msg, dashKeys.Up):
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, dashKeys.Down):
			if m.cursor < len(m.panes)-1 {
				m.cursor++
			}

		case key.Matches(msg, dashKeys.Refresh):
			// Manual refresh
			return m, tea.Batch(
				m.fetchSessionDataWithOutputs(),
				m.fetchStatuses(),
			)

		case key.Matches(msg, dashKeys.ContextRefresh):
			// Force context refresh (same as regular refresh but with user intent to see context)
			return m, tea.Batch(
				m.fetchSessionDataWithOutputs(),
				m.fetchStatuses(),
			)

		case key.Matches(msg, dashKeys.MailRefresh):
			// Refresh Agent Mail data
			return m, m.fetchAgentMailStatus()

		case key.Matches(msg, dashKeys.Zoom):
			if len(m.panes) > 0 && m.cursor < len(m.panes) {
				// Zoom to selected pane
				p := m.panes[m.cursor]
				_ = tmux.ZoomPane(m.session, p.Index)
				return m, tea.Quit
			}

		// Number quick-select
		case key.Matches(msg, dashKeys.Num1):
			m.selectByNumber(1)
		case key.Matches(msg, dashKeys.Num2):
			m.selectByNumber(2)
		case key.Matches(msg, dashKeys.Num3):
			m.selectByNumber(3)
		case key.Matches(msg, dashKeys.Num4):
			m.selectByNumber(4)
		case key.Matches(msg, dashKeys.Num5):
			m.selectByNumber(5)
		case key.Matches(msg, dashKeys.Num6):
			m.selectByNumber(6)
		case key.Matches(msg, dashKeys.Num7):
			m.selectByNumber(7)
		case key.Matches(msg, dashKeys.Num8):
			m.selectByNumber(8)
		case key.Matches(msg, dashKeys.Num9):
			m.selectByNumber(9)
		}
	}

	return m, nil
}

func (m *Model) selectByNumber(n int) {
	idx := n - 1
	if idx >= 0 && idx < len(m.panes) {
		m.cursor = idx
	}
}

func (m *Model) updateStats() {
	m.claudeCount = 0
	m.codexCount = 0
	m.geminiCount = 0
	m.userCount = 0

	for _, p := range m.panes {
		switch p.Type {
		case tmux.AgentClaude:
			m.claudeCount++
		case tmux.AgentCodex:
			m.codexCount++
		case tmux.AgentGemini:
			m.geminiCount++
		default:
			m.userCount++
		}
	}
}

// View implements tea.Model
func (m Model) View() string {
	t := m.theme
	ic := m.icons

	var b strings.Builder

	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated banner
	// ═══════════════════════════════════════════════════════════════
	bannerText := components.RenderBannerMedium(true, m.animTick)
	b.WriteString(bannerText + "\n")

	// Session title with gradient
	sessionTitle := ic.Session + "  " + m.session
	animatedSession := styles.Shimmer(sessionTitle, m.animTick,
		string(t.Blue), string(t.Lavender), string(t.Mauve))
	b.WriteString("  " + animatedSession + "\n")
	b.WriteString("  " + styles.GradientDivider(m.width-4,
		string(t.Blue), string(t.Mauve)) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// STATS BAR with agent counts
	// ═══════════════════════════════════════════════════════════════
	statsBar := m.renderStatsBar()
	b.WriteString("  " + statsBar + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// PANE GRID VISUALIZATION
	// ═══════════════════════════════════════════════════════════════
	if m.err != nil {
		errorStyle := lipgloss.NewStyle().Foreground(t.Error)
		b.WriteString("  " + errorStyle.Render(ic.Cross+" Error: "+m.err.Error()) + "\n")
	} else if len(m.panes) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		b.WriteString("  " + emptyStyle.Render("No panes found in session") + "\n")
	} else {
		// On wide terminals (≥110 cols), use split view: list + detail panel
		// On narrow terminals, use the traditional card grid
		if m.tier >= layout.TierSplit {
			splitView := m.renderSplitView()
			b.WriteString(splitView + "\n")
		} else {
			// Render pane cards in a grid
			paneGrid := m.renderPaneGrid()
			b.WriteString(paneGrid + "\n")
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")
	b.WriteString("  " + styles.GradientDivider(m.width-4,
		string(t.Surface2), string(t.Surface1)) + "\n")
	b.WriteString("  " + m.renderHelpBar() + "\n")

	return b.String()
}

func (m Model) renderStatsBar() string {
	t := m.theme
	ic := m.icons

	var parts []string

	// Health badge (bv drift status)
	healthBadge := m.renderHealthBadge()
	if healthBadge != "" {
		parts = append(parts, healthBadge)
	}

	// Total panes
	totalBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %d panes", ic.Pane, len(m.panes)))
	parts = append(parts, totalBadge)

	// Claude count
	if m.claudeCount > 0 {
		claudeBadge := lipgloss.NewStyle().
			Background(t.Claude).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Claude, m.claudeCount))
		parts = append(parts, claudeBadge)
	}

	// Codex count
	if m.codexCount > 0 {
		codexBadge := lipgloss.NewStyle().
			Background(t.Codex).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Codex, m.codexCount))
		parts = append(parts, codexBadge)
	}

	// Gemini count
	if m.geminiCount > 0 {
		geminiBadge := lipgloss.NewStyle().
			Background(t.Gemini).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Gemini, m.geminiCount))
		parts = append(parts, geminiBadge)
	}

	// User count
	if m.userCount > 0 {
		userBadge := lipgloss.NewStyle().
			Background(t.Green).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.User, m.userCount))
		parts = append(parts, userBadge)
	}

	// Agent Mail status badge
	mailBadge := m.renderAgentMailBadge()
	if mailBadge != "" {
		parts = append(parts, mailBadge)
	}

	return strings.Join(parts, "  ")
}

// renderHealthBadge renders the health badge based on bv drift status
func (m Model) renderHealthBadge() string {
	t := m.theme

	if m.healthStatus == "" || m.healthStatus == "unknown" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string

	switch m.healthStatus {
	case "ok":
		bgColor = t.Green
		fgColor = t.Base
		icon = "✓"
		label = "healthy"
	case "warning":
		bgColor = t.Yellow
		fgColor = t.Base
		icon = "⚠"
		label = "drift"
	case "critical":
		bgColor = t.Red
		fgColor = t.Base
		icon = "✗"
		label = "critical"
	case "no_baseline":
		bgColor = t.Surface1
		fgColor = t.Overlay
		icon = "?"
		label = "no baseline"
	case "unavailable":
		return "" // Don't show badge if bv not installed
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

// renderScanBadge renders the UBS scan status badge
func (m Model) renderScanBadge() string {
	t := m.theme

	if m.scanStatus == "" || m.scanStatus == "unavailable" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string

	switch m.scanStatus {
	case "clean":
		bgColor = t.Green
		fgColor = t.Base
		icon = "✓"
		label = "scan clean"
	case "warning":
		bgColor = t.Yellow
		fgColor = t.Base
		icon = "⚠"
		label = fmt.Sprintf("scan %d warn", m.scanTotals.Warning)
	case "critical":
		bgColor = t.Red
		fgColor = t.Base
		icon = "✗"
		label = fmt.Sprintf("scan %d crit", m.scanTotals.Critical)
	case "error":
		bgColor = t.Surface1
		fgColor = t.Overlay
		icon = "?"
		label = "scan error"
	default:
		return ""
	}

	if m.scanStatus == "clean" && (m.scanTotals.Critical+m.scanTotals.Warning+m.scanTotals.Info) > 0 {
		label = fmt.Sprintf("scan %d/%d/%d", m.scanTotals.Critical, m.scanTotals.Warning, m.scanTotals.Info)
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

// renderAgentMailBadge renders the Agent Mail status badge
func (m Model) renderAgentMailBadge() string {
	t := m.theme

	if !m.agentMailAvailable {
		return "" // Don't show badge if Agent Mail not available
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string

	if m.agentMailConnected {
		if m.agentMailLocks > 0 {
			bgColor = t.Lavender
			fgColor = t.Base
			icon = "🔒"
			label = fmt.Sprintf("%d locks", m.agentMailLocks)
		} else {
			bgColor = t.Surface1
			fgColor = t.Text
			icon = "📬"
			label = "mail"
		}
	} else {
		bgColor = t.Yellow
		fgColor = t.Base
		icon = "📭"
		label = "offline"
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

// renderContextBar renders a progress bar showing context usage percentage
func (m Model) renderContextBar(percent float64, width int) string {
	t := m.theme

	// Determine color based on usage
	var barColor lipgloss.Color
	var warningIcon string
	if percent >= 80 {
		barColor = t.Red
		warningIcon = " ⚠"
	} else if percent >= 60 {
		barColor = t.Yellow
		warningIcon = ""
	} else {
		barColor = t.Green
		warningIcon = ""
	}

	// Calculate bar width (leave room for percentage text and warning icon)
	barWidth := width - 8 // "[████░░] XX%⚠"
	if barWidth < 5 {
		barWidth = 5
	}

	// Cap percent at 100 for display, but show actual value
	displayPercent := percent
	if displayPercent > 100 {
		displayPercent = 100
	}

	filled := int(displayPercent * float64(barWidth) / 100)
	empty := barWidth - filled

	// Build the bar
	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(t.Surface1)
	percentStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	warningStyle := lipgloss.NewStyle().Foreground(t.Red).Bold(true)

	bar := "[" +
		filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty)) +
		"]" +
		percentStyle.Render(fmt.Sprintf("%3.0f%%", percent)) +
		warningStyle.Render(warningIcon)

	return bar
}

func (m Model) renderPaneGrid() string {
	t := m.theme
	ic := m.icons

	var lines []string

	// Calculate adaptive card dimensions based on terminal width
	// Uses beads_viewer-inspired algorithm with min/max constraints
	const (
		minCardWidth = 22 // Minimum usable card width
		maxCardWidth = 45 // Maximum card width for readability
		cardGap      = 2  // Gap between cards
	)

	availableWidth := m.width - 4 // Account for margins
	cardWidth, cardsPerRow := styles.AdaptiveCardDimensions(availableWidth, minCardWidth, maxCardWidth, cardGap)

	// On wide/ultra displays, show more detail per card
	showExtendedInfo := m.tier >= layout.TierWide

	var cards []string

	for i, p := range m.panes {
		isSelected := i == m.cursor

		// Determine card colors based on agent type
		var borderColor, iconColor lipgloss.Color
		var agentIcon string

		switch p.Type {
		case tmux.AgentClaude:
			borderColor = t.Claude
			iconColor = t.Claude
			agentIcon = ic.Claude
		case tmux.AgentCodex:
			borderColor = t.Codex
			iconColor = t.Codex
			agentIcon = ic.Codex
		case tmux.AgentGemini:
			borderColor = t.Gemini
			iconColor = t.Gemini
			agentIcon = ic.Gemini
		default:
			borderColor = t.Green
			iconColor = t.Green
			agentIcon = ic.User
		}

		// Selection highlight
		if isSelected {
			borderColor = t.Pink
		}

		// Build card content
		var cardContent strings.Builder

		// Header line with icon and title
		iconStyled := lipgloss.NewStyle().Foreground(iconColor).Bold(true).Render(agentIcon)
		title := layout.TruncateRunes(p.Title, maxInt(cardWidth-6, 10), "…")

		titleStyled := lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(title)
		cardContent.WriteString(iconStyled + " " + titleStyled + "\n")

		// Index badge with variant info on wide displays
		numBadge := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Render(fmt.Sprintf("#%d", p.Index))
		variantInfo := ""
		if showExtendedInfo && p.Variant != "" {
			variantStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
			variantInfo = " " + variantStyle.Render("("+p.Variant+")")
		}
		cardContent.WriteString(numBadge + variantInfo + "\n")

		// Size info - on wide displays show more detail
		sizeStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		if showExtendedInfo {
			cardContent.WriteString(sizeStyle.Render(fmt.Sprintf("%dx%d cols×rows", p.Width, p.Height)) + "\n")
		} else {
			cardContent.WriteString(sizeStyle.Render(fmt.Sprintf("%dx%d", p.Width, p.Height)) + "\n")
		}

		// Command running (if any) - only when there is room
		if p.Command != "" && m.tier >= layout.TierSplit {
			cmdStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
			cmd := layout.TruncateRunes(p.Command, maxInt(cardWidth-4, 8), "…")
			cardContent.WriteString(cmdStyle.Render(cmd))
		}

		// Context usage bar
		if ps, ok := m.paneStatus[p.Index]; ok && ps.ContextLimit > 0 && m.tier >= layout.TierWide {
			cardContent.WriteString("\n")
			contextBar := m.renderContextBar(ps.ContextPercent, cardWidth-4)
			cardContent.WriteString(contextBar)
		}

		// Compaction indicator
		if ps, ok := m.paneStatus[p.Index]; ok && ps.LastCompaction != nil {
			cardContent.WriteString("\n")
			compactStyle := lipgloss.NewStyle().Foreground(t.Warning).Bold(true)
			indicator := "⚠ compacted"
			if ps.RecoverySent {
				indicator = "↻ recovering"
			}
			cardContent.WriteString(compactStyle.Render(indicator))
		}

		// Create card box
		cardStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Width(cardWidth).
			Padding(0, 1)

		if isSelected {
			// Add glow effect for selected card
			cardStyle = cardStyle.
				Background(t.Surface0)
		}

		cards = append(cards, cardStyle.Render(cardContent.String()))
	}

	// Arrange cards in rows
	for i := 0; i < len(cards); i += cardsPerRow {
		end := i + cardsPerRow
		if end > len(cards) {
			end = len(cards)
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cards[i:end]...)
		lines = append(lines, "  "+row)
	}

	return strings.Join(lines, "\n")
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
		{"↑↓", "navigate"},
		{"1-9", "select"},
		{"z", "zoom"},
		{"c", "context"},
		{"m", "mail"},
		{"r", "refresh"},
		{"q", "quit"},
	}

	var parts []string
	for _, item := range items {
		parts = append(parts, keyStyle.Render(item.key)+" "+descStyle.Render(item.desc))
	}

	return strings.Join(parts, "  ")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ═══════════════════════════════════════════════════════════════════════════
// SPLIT VIEW RENDERING (for wide terminals ≥110 cols)
// Inspired by beads_viewer's responsive layout patterns
// ═══════════════════════════════════════════════════════════════════════════

// renderSplitView renders a two-panel layout: pane list (left) + detail (right)
func (m Model) renderSplitView() string {
	t := m.theme
	leftWidth, rightWidth := layout.SplitProportions(m.width)

	// Calculate content height (leave room for header/footer)
	contentHeight := m.height - 14
	if contentHeight < 5 {
		contentHeight = 5
	}

	// Build left panel (pane list)
	listContent := m.renderPaneList(leftWidth - 4) // -4 for borders/padding
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	// Build right panel (detail view)
	detailContent := m.renderPaneDetail(rightWidth - 4)
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Pink). // Accent color for detail
		Width(rightWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(detailContent)

	// Join panels horizontally
	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, listPanel, detailPanel)
}

// renderPaneList renders a compact list of panes with status indicators
func (m Model) renderPaneList(width int) string {
	t := m.theme
	var lines []string

	// Header row
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Subtext).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width)

	// Column headers vary by tier
	if m.tier >= layout.TierUltra {
		lines = append(lines, headerStyle.Render("  #  T  S  TITLE                    CTX   MODEL      CMD"))
	} else if m.tier >= layout.TierWide {
		lines = append(lines, headerStyle.Render("  #  T  S  TITLE                    CTX   MODEL"))
	} else {
		lines = append(lines, headerStyle.Render("  #  T  S  TITLE"))
	}

	// Pane rows
	for i, p := range m.panes {
		isSelected := i == m.cursor
		row := m.renderPaneRow(p, i, isSelected, width)
		lines = append(lines, row)
	}

	return strings.Join(lines, "\n")
}

// renderPaneRow renders a single pane as a table row
func (m Model) renderPaneRow(p tmux.Pane, _ int, selected bool, width int) string {
	t := m.theme
	ic := m.icons
	var parts []string

	// Selection indicator
	if selected {
		parts = append(parts, lipgloss.NewStyle().Foreground(t.Pink).Bold(true).Render("▸"))
	} else {
		parts = append(parts, " ")
	}

	// Index
	idxStyle := lipgloss.NewStyle().Foreground(t.Overlay).Width(2)
	parts = append(parts, idxStyle.Render(fmt.Sprintf("%2d", p.Index)))

	// Type icon with color
	var typeColor lipgloss.Color
	var typeIcon string
	switch p.Type {
	case tmux.AgentClaude:
		typeColor = t.Claude
		typeIcon = ic.Claude
	case tmux.AgentCodex:
		typeColor = t.Codex
		typeIcon = ic.Codex
	case tmux.AgentGemini:
		typeColor = t.Gemini
		typeIcon = ic.Gemini
	default:
		typeColor = t.Green
		typeIcon = ic.User
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(typeColor).Bold(true).Render(typeIcon))

	// Status indicator
	ps := m.paneStatus[p.Index]
	statusStyle := lipgloss.NewStyle()
	var statusIcon string
	switch ps.State {
	case "working":
		statusIcon = "●"
		statusStyle = statusStyle.Foreground(t.Green)
	case "idle":
		statusIcon = "○"
		statusStyle = statusStyle.Foreground(t.Yellow)
	case "error":
		statusIcon = "✗"
		statusStyle = statusStyle.Foreground(t.Red)
	case "compacted":
		statusIcon = "⚠"
		statusStyle = statusStyle.Foreground(t.Peach).Bold(true)
	default:
		statusIcon = "•"
		statusStyle = statusStyle.Foreground(t.Overlay)
	}
	parts = append(parts, statusStyle.Render(statusIcon))

	// Title (flexible width based on available columns)
	baseSpaces := 6 // approx separators + selection/index/icon/status
	fixedAfterTitle := 0
	if m.tier >= layout.TierWide {
		fixedAfterTitle += 6 /*ctx*/ + 1
		fixedAfterTitle += 8 /*model*/ + 1
	}
	if m.tier >= layout.TierUltra {
		fixedAfterTitle += 12 /*cmd*/ + 1
	}
	titleWidth := width - baseSpaces - fixedAfterTitle
	if titleWidth < 12 {
		titleWidth = 12
	}
	if titleWidth > 40 {
		titleWidth = 40
	}
	title := layout.TruncateRunes(p.Title, titleWidth, "…")
	titleStyle := lipgloss.NewStyle().Foreground(t.Text)
	if selected {
		titleStyle = titleStyle.Bold(true)
	}
	parts = append(parts, titleStyle.Render(title))

	// Context bar (TierWide+)
	if m.tier >= layout.TierWide && ps.ContextLimit > 0 {
		ctxPct := ps.ContextPercent / 100
		if ctxPct > 1 {
			ctxPct = 1
		}
		bar := renderMiniSparkline(ctxPct, 6, t)
		parts = append(parts, bar)
	} else if m.tier >= layout.TierWide {
		parts = append(parts, "      ") // placeholder
	}

	// Model/variant (TierWide+)
	if m.tier >= layout.TierWide && p.Variant != "" {
		varStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true).Width(8)
		parts = append(parts, varStyle.Render(layout.TruncateRunes(p.Variant, 8, "…")))
	} else if m.tier >= layout.TierWide {
		parts = append(parts, "        ") // placeholder
	}

	// Command snippet (TierUltra)
	if m.tier >= layout.TierUltra {
		cmd := layout.TruncateRunes(strings.TrimSpace(p.Command), 12, "…")
		cmdStyle := lipgloss.NewStyle().Foreground(t.Overlay).Width(12)
		parts = append(parts, cmdStyle.Render(cmd))
	}

	// Join and pad to requested width to avoid wrap jitter
	rowContent := strings.Join(parts, " ")
	if width > 0 {
		padding := width - lipgloss.Width(rowContent)
		if padding > 0 {
			rowContent += strings.Repeat(" ", padding)
		}
	}

	if selected {
		return lipgloss.NewStyle().Background(t.Surface0).Render(rowContent)
	}
	return rowContent
}

// renderPaneDetail renders detailed info for the selected pane
func (m Model) renderPaneDetail(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.panes) == 0 || m.cursor >= len(m.panes) {
		emptyStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		return emptyStyle.Render("No pane selected")
	}

	p := m.panes[m.cursor]
	ps := m.paneStatus[p.Index]
	var lines []string

	// Header with pane title
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width-2).
		Padding(0, 1)
	lines = append(lines, headerStyle.Render(p.Title))
	lines = append(lines, "")

	// Info grid
	labelStyle := lipgloss.NewStyle().Foreground(t.Subtext).Width(12)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text)

	// Type badge
	var typeColor lipgloss.Color
	var typeIcon string
	switch p.Type {
	case tmux.AgentClaude:
		typeColor = t.Claude
		typeIcon = ic.Claude
	case tmux.AgentCodex:
		typeColor = t.Codex
		typeIcon = ic.Codex
	case tmux.AgentGemini:
		typeColor = t.Gemini
		typeIcon = ic.Gemini
	default:
		typeColor = t.Green
		typeIcon = ic.User
	}
	typeBadge := lipgloss.NewStyle().
		Background(typeColor).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(typeIcon + " " + string(p.Type))
	lines = append(lines, labelStyle.Render("Type:")+typeBadge)

	// Index
	lines = append(lines, labelStyle.Render("Index:")+valueStyle.Render(fmt.Sprintf("%d", p.Index)))

	// Dimensions
	lines = append(lines, labelStyle.Render("Size:")+valueStyle.Render(fmt.Sprintf("%d × %d", p.Width, p.Height)))

	// Variant/Model
	if p.Variant != "" {
		variantBadge := lipgloss.NewStyle().
			Background(t.Surface1).
			Foreground(t.Text).
			Padding(0, 1).
			Render(p.Variant)
		lines = append(lines, labelStyle.Render("Model:")+variantBadge)
	}

	lines = append(lines, "")

	// Context usage section
	if ps.ContextLimit > 0 {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Context Usage"))
		lines = append(lines, "")

		// Large context bar
		barWidth := width - 10
		if barWidth < 10 {
			barWidth = 10
		} else if barWidth > 50 {
			barWidth = 50
		}
		contextBar := m.renderContextBar(ps.ContextPercent, barWidth)
		lines = append(lines, "  "+contextBar)

		// Stats
		statsStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		lines = append(lines, statsStyle.Render(fmt.Sprintf(
			"  %d / %d tokens (%.1f%%)",
			ps.ContextTokens, ps.ContextLimit, ps.ContextPercent,
		)))
		lines = append(lines, "")
	}

	// Status section
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Status"))
	lines = append(lines, "")

	statusText := ps.State
	if statusText == "" {
		statusText = "unknown"
	}
	var statusColor lipgloss.Color
	var statusIcon string
	switch ps.State {
	case "working":
		statusIcon = "●"
		statusColor = t.Green
	case "idle":
		statusIcon = "○"
		statusColor = t.Yellow
	case "error":
		statusIcon = "✗"
		statusColor = t.Red
	case "compacted":
		statusIcon = "⚠"
		statusColor = t.Peach
	default:
		statusIcon = "•"
		statusColor = t.Overlay
	}
	lines = append(lines, "  "+lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon+" "+statusText))

	// Compaction warning
	if ps.LastCompaction != nil {
		lines = append(lines, "")
		warnStyle := lipgloss.NewStyle().Foreground(t.Peach).Bold(true)
		lines = append(lines, warnStyle.Render("  ⚠ Context compaction detected"))
		if ps.RecoverySent {
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Green).Render("    ↻ Recovery prompt sent"))
		}
	}

	// Command (if running)
	if p.Command != "" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Command"))
		lines = append(lines, "")
		cmdStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Width(width - 6)
		lines = append(lines, "  "+cmdStyle.Render(p.Command))
	}

	return strings.Join(lines, "\n")
}

// renderMiniSparkline renders a compact sparkline bar for table rows
func renderMiniSparkline(value float64, width int, t theme.Theme) string {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}

	filled := int(value * float64(width))
	empty := width - filled

	// Color based on value
	var barColor lipgloss.Color
	if value >= 0.80 {
		barColor = t.Red
	} else if value >= 0.60 {
		barColor = t.Yellow
	} else {
		barColor = t.Green
	}

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(t.Surface1)

	return filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty))
}

// Run starts the dashboard
func Run(session string) error {
	model := New(session)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
