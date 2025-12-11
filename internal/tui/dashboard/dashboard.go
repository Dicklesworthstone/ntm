// Package dashboard provides a stunning visual session dashboard
package dashboard

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	config "github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
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

// ConfigReloadMsg is sent when configuration changes
type ConfigReloadMsg struct {
	Config *config.Config
}

// HealthCheckMsg is sent when health check (bv drift) completes
type HealthCheckMsg struct {
	Status  string // "ok", "warning", "critical", "no_baseline", "unavailable"
	Message string
}

// ScanStatusMsg is sent when UBS scan completes
type ScanStatusMsg struct {
	Status   string
	Totals   scanner.ScanTotals
	Duration time.Duration
}

// AgentMailUpdateMsg is sent when Agent Mail data is fetched
type AgentMailUpdateMsg struct {
	Available bool
	Connected bool
	Locks     int
	LockInfo  []AgentMailLockInfo
}

// CassSelectMsg is sent when a CASS search result is selected
type CassSelectMsg struct {
	Hit cass.SearchHit
}

// BeadsUpdateMsg is sent when beads data is fetched
type BeadsUpdateMsg struct {
	Summary bv.BeadsSummary
	Ready   []bv.BeadPreview
	Err     error
}

// AlertsUpdateMsg is sent when alerts are refreshed
type AlertsUpdateMsg struct {
	Alerts []alerts.Alert
	Err    error
}

// MetricsUpdateMsg is sent when session metrics are updated
type MetricsUpdateMsg struct {
	TotalTokens int
	CostEst     float64
	Err         error
}

// HistoryUpdateMsg is sent when command history is fetched
type HistoryUpdateMsg struct {
	Entries []history.HistoryEntry
	Err     error
}

// FileChangeMsg is sent when file changes are detected
type FileChangeMsg struct {
	Changes []tracker.RecordedFileChange
	Err     error
}

// CASSContextMsg is sent when relevant context is found
type CASSContextMsg struct {
	Hits []cass.SearchHit
	Err  error
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

	// Subsystem refresh timers
	lastPaneFetch    time.Time
	lastContextFetch time.Time
	lastAlertsFetch  time.Time
	lastBeadsFetch   time.Time

	// Auto-refresh configuration
	refreshInterval time.Duration

	// Health badge (bv drift status)
	healthStatus  string // "ok", "warning", "critical", "no_baseline", "unavailable"
	healthMessage string

	// UBS scan status
	scanStatus   string             // "clean", "warning", "critical", "unavailable"
	scanTotals   scanner.ScanTotals // Scan result totals
	scanDuration time.Duration      // How long the scan took

	// Layout tier (narrow/split/wide/ultra)
	tier layout.Tier

	// Agent Mail integration
	agentMailAvailable bool
	agentMailConnected bool
	agentMailLocks     int                 // Active file reservations
	agentMailUnread    int                 // Unread message count (requires agent context)
	agentMailLockInfo  []AgentMailLockInfo // Lock details for display

	// Config watcher
	configSub    chan *config.Config
	configCloser func()

	// Markdown renderer
	renderer *glamour.TermRenderer

	// CASS Search
	showCassSearch bool
	cassSearch     components.CassSearchModel

	// Panels
	beadsPanel  *panels.BeadsPanel
	alertsPanel *panels.AlertsPanel

	// Data for new panels
	beadsSummary  bv.BeadsSummary
	beadsReady    []bv.BeadPreview
	activeAlerts  []alerts.Alert
	metricsTokens int
	metricsCost   float64
	cmdHistory    []history.HistoryEntry
	fileChanges   []tracker.RecordedFileChange
	cassContext   []cass.SearchHit
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
	CassSearch     key.Binding // 'ctrl+s' to open CASS search
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

// Per-subsystem refresh cadence (driven by DashboardTickMsg)
const (
	PaneRefreshInterval    = 1 * time.Second
	ContextRefreshInterval = 2 * time.Second
	AlertsRefreshInterval  = 3 * time.Second
	BeadsRefreshInterval   = 5 * time.Second
)

func (m *Model) initRenderer(width int) {
	r, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	m.renderer = r
}

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
	CassSearch:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "cass search")),
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

	m := Model{
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
		cassSearch: components.NewCassSearch(func(hit cass.SearchHit) tea.Cmd {
			return func() tea.Msg {
				return CassSelectMsg{Hit: hit}
			}
		}),
		beadsPanel:  panels.NewBeadsPanel(),
		alertsPanel: panels.NewAlertsPanel(),
	}

	// Initialize last-fetch timestamps to start cadence after the initial fetches from Init.
	now := time.Now()
	m.lastPaneFetch = now
	m.lastContextFetch = now
	m.lastAlertsFetch = now
	m.lastBeadsFetch = now

	// Setup config watcher
	m.configSub = make(chan *config.Config, 1)
	// We capture the channel in the closure. Since Model is copied, we must ensure
	// we use the channel we just created, which is what m.configSub holds.
	sub := m.configSub
	closer, err := config.Watch(func(cfg *config.Config) {
		select {
		case sub <- cfg:
		default:
			// If channel full, drop oldest
			select {
			case <-sub:
			default:
			}
			select {
			case sub <- cfg:
			default:
			}
		}
	})
	if err == nil {
		m.configCloser = closer
	}

	m.initRenderer(40)
	return m
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
		m.fetchBeadsCmd(),
		m.fetchAlertsCmd(),
		m.fetchMetricsCmd(),
		m.fetchHistoryCmd(),
		m.fetchFileChangesCmd(),
		m.subscribeToConfig(),
	)
}

func (m Model) subscribeToConfig() tea.Cmd {
	return func() tea.Msg {
		if m.configSub == nil {
			return nil
		}
		cfg := <-m.configSub
		return ConfigReloadMsg{Config: cfg}
	}
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

// fetchScanStatus performs a quick UBS scan for badge display (diff-only)
func (m Model) fetchScanStatus() tea.Cmd {
	return func() tea.Msg {
		if !scanner.IsAvailable() {
			return ScanStatusMsg{Status: "unavailable"}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		opts := scanner.ScanOptions{
			DiffOnly: true,
			Timeout:  15 * time.Second,
		}

		start := time.Now()
		result, err := scanner.QuickScanWithOptions(ctx, ".", opts)
		if err != nil {
			return ScanStatusMsg{Status: "error"}
		}
		if result == nil {
			return ScanStatusMsg{Status: "unavailable"}
		}

		status := "clean"
		switch {
		case result.Totals.Critical > 0:
			status = "critical"
		case result.Totals.Warning > 0:
			status = "warning"
		}

		dur := result.Duration
		if dur == 0 {
			dur = time.Since(start)
		}

		return ScanStatusMsg{
			Status:   status,
			Totals:   result.Totals,
			Duration: dur,
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
	var cmds []tea.Cmd

	// Handle CASS search updates
	passToSearch := true
	if _, ok := msg.(tea.KeyMsg); ok && !m.showCassSearch {
		passToSearch = false
	}

	if passToSearch {
		var cmd tea.Cmd
		m.cassSearch, cmd = m.cassSearch.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	switch msg := msg.(type) {
	case CassSelectMsg:
		m.showCassSearch = false
		m.healthMessage = fmt.Sprintf("Selected: %s", msg.Hit.Title)
		return m, tea.Batch(cmds...)

	case BeadsUpdateMsg:
		if msg.Err == nil {
			m.beadsSummary = msg.Summary
			m.beadsReady = msg.Ready
			m.beadsPanel.SetData(msg.Summary, msg.Ready)
		}
		return m, nil

	case AlertsUpdateMsg:
		if msg.Err == nil {
			m.activeAlerts = msg.Alerts
			m.alertsPanel.SetData(msg.Alerts)
		}
		return m, nil

	case MetricsUpdateMsg:
		if msg.Err == nil {
			m.metricsTokens = msg.TotalTokens
			m.metricsCost = msg.CostEst
		}
		return m, nil

	case HistoryUpdateMsg:
		if msg.Err == nil {
			m.cmdHistory = msg.Entries
		}
		return m, nil

	case FileChangeMsg:
		if msg.Err == nil {
			m.fileChanges = msg.Changes
		}
		return m, nil

	case CASSContextMsg:
		if msg.Err == nil {
			m.cassContext = msg.Hits
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidth(msg.Width)

		_, detailWidth := layout.SplitProportions(msg.Width)
		contentWidth := detailWidth - 4
		if contentWidth < 20 {
			contentWidth = 20
		}
		m.initRenderer(contentWidth)

		searchW := int(float64(msg.Width) * 0.6)
		searchH := int(float64(msg.Height) * 0.6)
		m.cassSearch.SetSize(searchW, searchH)

		return m, tea.Batch(cmds...)

	case DashboardTickMsg:
		m.animTick++

		// Drive staggered refreshes on the animation ticker to avoid a single heavy burst.
		now := time.Now()
		if !m.refreshPaused {
			if now.Sub(m.lastPaneFetch) >= PaneRefreshInterval {
				cmds = append(cmds, m.fetchSessionDataWithOutputs(), m.fetchScanStatus())
				m.lastPaneFetch = now
			}
			if now.Sub(m.lastContextFetch) >= ContextRefreshInterval {
				cmds = append(cmds,
					m.fetchStatuses(),
					m.fetchMetricsCmd(),
					m.fetchHistoryCmd(),
					m.fetchFileChangesCmd(),
				)
				m.lastContextFetch = now
			}
			if now.Sub(m.lastAlertsFetch) >= AlertsRefreshInterval {
				cmds = append(cmds, m.fetchAlertsCmd())
				m.lastAlertsFetch = now
			}
			if now.Sub(m.lastBeadsFetch) >= BeadsRefreshInterval {
				cmds = append(cmds, m.fetchBeadsCmd())
				m.lastBeadsFetch = now
			}
		}

		cmds = append(cmds, m.tick())
		return m, tea.Batch(cmds...)

	case RefreshMsg:
		// Trigger async fetch for pane data and status detection
		return m, tea.Batch(
			m.fetchSessionDataWithOutputs(),
			m.fetchStatuses(),
			m.fetchBeadsCmd(),
			m.fetchAlertsCmd(),
			m.fetchMetricsCmd(),
			m.fetchHistoryCmd(),
			m.fetchFileChangesCmd(),
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
		return m, nil

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

			// Rate limit should be shown with special indicator
			if st.State == status.StateError && st.ErrorType == status.ErrorRateLimit {
				state = "rate_limited"
			} else if ps.LastCompaction != nil && state != string(status.StateError) {
				// Compaction warning should override idle/working but not errors
				state = "compacted"
			}
			ps.State = state
			m.paneStatus[idx] = ps
			m.agentStatuses[st.PaneID] = st
		}
		m.lastRefresh = msg.Time
		return m, nil

	case ConfigReloadMsg:
		if msg.Config != nil {
			// Update theme
			m.theme = theme.FromName(msg.Config.Theme)
			// Reload icons (if dependent on config in future, pass cfg)
			m.icons = icons.Current()

			// Re-initialize renderer with new theme colors
			_, detailWidth := layout.SplitProportions(m.width)
			contentWidth := detailWidth - 4
			if contentWidth < 20 {
				contentWidth = 20
			}
			m.initRenderer(contentWidth)
		}
		return m, m.subscribeToConfig()

	case HealthCheckMsg:
		m.healthStatus = msg.Status
		m.healthMessage = msg.Message
		return m, nil

	case ScanStatusMsg:
		m.scanStatus = msg.Status
		m.scanTotals = msg.Totals
		m.scanDuration = msg.Duration
		return m, nil

	case AgentMailUpdateMsg:
		m.agentMailAvailable = msg.Available
		m.agentMailConnected = msg.Connected
		m.agentMailLocks = msg.Locks
		m.agentMailLockInfo = msg.LockInfo
		return m, nil

	case tea.KeyMsg:
		if m.showCassSearch {
			if msg.String() == "esc" {
				m.showCassSearch = false
			}
			return m, tea.Batch(cmds...)
		}

		switch {
		case key.Matches(msg, dashKeys.CassSearch):
			m.showCassSearch = true
			searchW := int(float64(m.width) * 0.6)
			searchH := int(float64(m.height) * 0.6)
			m.cassSearch.SetSize(searchW, searchH)
			cmds = append(cmds, m.cassSearch.Init())
			return m, tea.Batch(cmds...)

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
				m.fetchScanStatus(),
			)

		case key.Matches(msg, dashKeys.ContextRefresh):
			// Force context refresh (same as regular refresh but with user intent to see context)
			return m, tea.Batch(
				m.fetchSessionDataWithOutputs(),
				m.fetchStatuses(),
				m.fetchScanStatus(),
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
	// RATE LIMIT ALERT (if any agent is rate limited)
	// ═══════════════════════════════════════════════════════════════
	if alert := m.renderRateLimitAlert(); alert != "" {
		b.WriteString(alert + "\n\n")
	}

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
		// Responsive layout selection
		switch {
		case m.tier >= layout.TierMega:
			b.WriteString(m.renderMegaLayout() + "\n")
		case m.tier >= layout.TierUltra:
			b.WriteString(m.renderUltraLayout() + "\n")
		case m.tier >= layout.TierSplit:
			b.WriteString(m.renderSplitView() + "\n")
		default:
			b.WriteString(m.renderPaneGrid() + "\n")
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")
	b.WriteString("  " + styles.GradientDivider(m.width-4,
		string(t.Surface2), string(t.Surface1)) + "\n")
	b.WriteString("  " + m.renderHelpBar() + "\n")

	content := b.String()

	if m.showCassSearch {
		searchView := m.cassSearch.View()
		modalStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Background(t.Base).
			Padding(1, 2)
		modal := modalStyle.Render(searchView)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	return content
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

// renderRateLimitAlert renders a prominent alert banner if any agent is rate limited
func (m Model) renderRateLimitAlert() string {
	t := m.theme

	// Check if any pane is rate limited
	var rateLimitedPanes []int
	for _, p := range m.panes {
		if ps, ok := m.paneStatus[p.Index]; ok && ps.State == "rate_limited" {
			rateLimitedPanes = append(rateLimitedPanes, p.Index)
		}
	}

	if len(rateLimitedPanes) == 0 {
		return ""
	}

	// Build alert message
	var msg string
	if len(rateLimitedPanes) == 1 {
		msg = fmt.Sprintf("⏳ Rate limit hit on pane %d! Run: ntm rotate %s --pane=%d",
			rateLimitedPanes[0], m.session, rateLimitedPanes[0])
	} else {
		paneList := fmt.Sprintf("%v", rateLimitedPanes)
		msg = fmt.Sprintf("⏳ Rate limit hit on panes %s! Press 'r' to rotate", paneList)
	}

	// Render as a prominent alert box
	alertStyle := lipgloss.NewStyle().
		Background(t.Maroon).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 2).
		Width(m.width - 6)

	return "  " + alertStyle.Render(msg)
}

// renderContextBar renders a progress bar showing context usage percentage
func (m Model) renderContextBar(percent float64, width int) string {
	t := m.theme

	// Determine warning icon and gradient stops
	var warningIcon string
	switch {
	case percent >= 95:
		warningIcon = " !!!"
	case percent >= 90:
		warningIcon = " !!"
	case percent >= 80:
		warningIcon = " !"
	default:
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

	// Gradient-filled portion
	var filledStr string
	if filled > 0 {
		grad := []string{string(t.Green), string(t.Yellow), string(t.Red)}
		filledStr = styles.GradientText(strings.Repeat("█", filled), grad...)
	}

	emptyStr := lipgloss.NewStyle().Foreground(t.Surface1).Render(strings.Repeat("░", empty))

	percentStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	warningStyle := lipgloss.NewStyle().Foreground(t.Red).Bold(true)

	bar := "[" + filledStr + emptyStr + "]" +
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

// renderUltraLayout renders a three-panel layout: Agents | Detail | Sidebar
func (m Model) renderUltraLayout() string {
	t := m.theme
	leftWidth, centerWidth, rightWidth := layout.UltraProportions(m.width)

	contentHeight := m.height - 14
	if contentHeight < 5 {
		contentHeight = 5
	}

	listContent := m.renderPaneList(leftWidth - 4)
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	detailContent := m.renderPaneDetail(centerWidth - 4)
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Pink).
		Width(centerWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(detailContent)

	sidebarContent := m.renderSidebar(rightWidth - 4)
	sidebarPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Lavender).
		Width(rightWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(sidebarContent)

	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, listPanel, detailPanel, sidebarPanel)
}

func (m Model) renderSidebar(width int) string {
	t := m.theme
	var lines []string

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width).
		Padding(0, 1)

	lines = append(lines, headerStyle.Render("Activity & Locks"))
	lines = append(lines, "")

	if len(m.agentMailLockInfo) > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Lavender).Bold(true).Render("Active Locks"))
		for _, lock := range m.agentMailLockInfo {
			lines = append(lines, fmt.Sprintf("🔒 %s", layout.TruncateRunes(lock.PathPattern, width-4, "...")))
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Subtext).Render(fmt.Sprintf("  by %s (%s)", lock.AgentName, lock.ExpiresIn)))
		}
		lines = append(lines, "")
	} else {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Render("No active locks"))
		lines = append(lines, "")
	}

	// Scan status
	if m.scanStatus != "" && m.scanStatus != "unavailable" {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Blue).Bold(true).Render("Scan Status"))
		lines = append(lines, m.renderScanBadge())
	}

	return strings.Join(lines, "\n")
}

// renderMegaLayout renders a five-panel layout: Agents | Detail | Beads | Alerts | Activity
func (m Model) renderMegaLayout() string {
	t := m.theme
	p1, p2, p3, p4, p5 := layout.MegaProportions(m.width)

	contentHeight := m.height - 14
	if contentHeight < 5 {
		contentHeight = 5
	}

	panel1 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Width(p1).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderPaneList(p1 - 2))

	panel2 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Pink).
		Width(p2).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderPaneDetail(p2 - 2))

	panel3 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Green).
		Width(p3).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderBeadsPanel(p3 - 2))

	panel4 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Red).
		Width(p4).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderAlertsPanel(p4 - 2))

	panel5 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Lavender).
		Width(p5).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderSidebar(p5 - 2))

	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, panel1, panel2, panel3, panel4, panel5)
}

func (m Model) renderBeadsPanel(width int) string {
	m.beadsPanel.SetSize(width, 0) // Height handled by container
	return m.beadsPanel.View()
}

func (m Model) renderAlertsPanel(width int) string {
	m.alertsPanel.SetSize(width, 0) // Height handled by container
	return m.alertsPanel.View()
}

// renderPaneList renders a compact list of panes with status indicators
func (m Model) renderPaneList(width int) string {
	t := m.theme
	var lines []string

	ranks := m.computeContextRanks()

	showRank := m.tier >= layout.TierWide

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
		if showRank {
			lines = append(lines, headerStyle.Render("  #  T  S  R  TITLE                   CTX   MODEL      CMD"))
		} else {
			lines = append(lines, headerStyle.Render("  #  T  S  TITLE                      CTX   MODEL      CMD"))
		}
	} else if m.tier >= layout.TierWide {
		if showRank {
			lines = append(lines, headerStyle.Render("  #  T  S  R  TITLE                   CTX   MODEL"))
		} else {
			lines = append(lines, headerStyle.Render("  #  T  S  TITLE                      CTX   MODEL"))
		}
	} else {
		lines = append(lines, headerStyle.Render("  #  T  S  TITLE"))
	}

	// Pane rows
	for i, p := range m.panes {
		isSelected := i == m.cursor
		row := m.renderPaneRow(p, ranks[p.Index], isSelected, width)
		lines = append(lines, row)
	}

	return strings.Join(lines, "\n")
}

// computeContextRanks returns a 1-based rank per pane index based on context usage (desc).
// Ties share the same rank.
func (m Model) computeContextRanks() map[int]int {
	type pair struct {
		idx int
		pct float64
	}

	var pairs []pair
	for _, p := range m.panes {
		if ps, ok := m.paneStatus[p.Index]; ok {
			pairs = append(pairs, pair{idx: p.Index, pct: ps.ContextPercent})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].pct > pairs[j].pct
	})

	ranks := make(map[int]int, len(pairs))
	prevPct := -1.0
	currentRank := 0
	for i, pr := range pairs {
		if prevPct < 0 || pr.pct < prevPct {
			currentRank = i + 1
			prevPct = pr.pct
		}
		ranks[pr.idx] = currentRank
	}
	return ranks
}

// renderPaneRow renders a single pane as a table row
func (m Model) renderPaneRow(p tmux.Pane, rank int, selected bool, width int) string {
	t := m.theme
	ic := m.icons
	var parts []string
	showRank := m.tier >= layout.TierWide

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
		statusIcon = spinnerDot(m.animTick)
		statusStyle = statusStyle.Foreground(t.Green)
	case "idle":
		statusIcon = "○"
		statusStyle = statusStyle.Foreground(t.Yellow)
	case "error":
		statusIcon = "✗"
		statusStyle = statusStyle.Foreground(t.Red)
	case "rate_limited":
		statusIcon = "⏳"
		statusStyle = statusStyle.Foreground(t.Maroon).Bold(true)
	case "compacted":
		statusIcon = "⚠"
		statusStyle = statusStyle.Foreground(t.Peach).Bold(true)
	default:
		statusIcon = "•"
		statusStyle = statusStyle.Foreground(t.Overlay)
	}
	parts = append(parts, statusStyle.Render(statusIcon))

	// Rank badge (context usage rank)
	if showRank {
		if rank > 0 {
			badge := styles.RankBadge(rank, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     true,
				ShowIcon: rank <= 3,
			})
			parts = append(parts, badge)
		} else {
			parts = append(parts, "     ")
		}
	}

	// Title (flexible width based on available columns)
	baseSpaces := 10 // separators + selection/index/icon/status
	if showRank {
		baseSpaces += 6 // rank badge + spacer
	}
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
		bar := RenderMiniBar(ctxPct, 6, t)
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

// spinnerDot returns a one-cell dot spinner frame based on the animation tick.
func spinnerDot(tick int) string {
	frames := []string{".", "·", "•", "·"}
	return frames[tick%len(frames)]
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

		// Legend for thresholds (kept compact and ASCII-safe)
		legend := lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Foreground(t.Green).Render("green<40%"),
			lipgloss.NewStyle().Foreground(t.Blue).Render("  blue<60%"),
			lipgloss.NewStyle().Foreground(t.Yellow).Render("  yellow<80%"),
			lipgloss.NewStyle().Foreground(t.Red).Render("  red≥80%"),
		)
		lines = append(lines, "  "+legend)
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

	// Project Health (if warning/critical)
	if m.healthStatus == "warning" || m.healthStatus == "critical" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Yellow).Render("Project Health"))
		lines = append(lines, "")
		msg := wordwrap.String(m.healthMessage, width-4)
		lines = append(lines, "  "+lipgloss.NewStyle().Foreground(t.Warning).Render(msg))
	}

	// Global Locks (TierWide+)
	if m.tier >= layout.TierWide && len(m.agentMailLockInfo) > 0 {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Active Locks"))
		lines = append(lines, "")
		for i, lock := range m.agentMailLockInfo {
			if i >= 5 {
				lines = append(lines, fmt.Sprintf("  ...and %d more", len(m.agentMailLockInfo)-5))
				break
			}
			lines = append(lines, fmt.Sprintf("  🔒 %s (%s)", layout.TruncateRunes(lock.PathPattern, 20, "..."), lock.AgentName))
		}
	}

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

	// Recent Output (rendered with glamour)
	if status, ok := m.agentStatuses[p.ID]; ok && status.LastOutput != "" && m.renderer != nil {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Recent Output"))
		lines = append(lines, "")

		rendered, err := m.renderer.Render(status.LastOutput)
		if err == nil {
			lines = append(lines, rendered)
		} else {
			lines = append(lines, layout.TruncateRunes(status.LastOutput, 500, "..."))
		}
	}

	return strings.Join(lines, "\n")
}

// Run starts the dashboard
func Run(session string) error {
	model := New(session)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
