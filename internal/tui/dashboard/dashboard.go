// Package dashboard provides a stunning visual session dashboard
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// DashboardTickMsg is sent for animation updates
type DashboardTickMsg time.Time

// RefreshMsg triggers a refresh of session data
type RefreshMsg struct{}

// StatusUpdateMsg is sent when status detection completes
type StatusUpdateMsg struct {
	Statuses []status.AgentStatus
	Time     time.Time
	Duration time.Duration
	Err      error
	Gen      uint64
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
	Err      error
	Gen      uint64
}

// AgentMailUpdateMsg is sent when Agent Mail data is fetched
type AgentMailUpdateMsg struct {
	Available    bool
	Connected    bool
	ArchiveFound bool // Fallback detection via archive directory
	Locks        int
	LockInfo     []AgentMailLockInfo
	Gen          uint64
}

// AgentMailInboxSummaryMsg is sent when per-agent inbox summaries are fetched.
type AgentMailInboxSummaryMsg struct {
	Inboxes  map[string][]agentmail.InboxMessage // paneID -> messages
	AgentMap map[string]string                   // paneID -> agent name
	Err      error
	Gen      uint64
}

// AgentMailInboxDetailMsg is sent when message bodies are fetched for a single agent.
type AgentMailInboxDetailMsg struct {
	PaneID   string
	Messages []agentmail.InboxMessage
	Err      error
	Gen      uint64
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
	Gen     uint64
}

// AlertsUpdateMsg is sent when alerts are refreshed
type AlertsUpdateMsg struct {
	Alerts []alerts.Alert
	Err    error
	Gen    uint64
}

// SpawnUpdateMsg is sent when spawn state is updated
type SpawnUpdateMsg struct {
	Data panels.SpawnData
	Gen  uint64
}

// MetricsUpdateMsg is sent when session metrics are updated
type MetricsUpdateMsg struct {
	Data panels.MetricsData
	Err  error
	Gen  uint64
}

// HistoryUpdateMsg is sent when command history is fetched
type HistoryUpdateMsg struct {
	Entries []history.HistoryEntry
	Err     error
	Gen     uint64
}

// FileChangeMsg is sent when file changes are detected
type FileChangeMsg struct {
	Changes []tracker.RecordedFileChange
	Err     error
	Gen     uint64
}

// CASSContextMsg is sent when relevant context is found
type CASSContextMsg struct {
	Hits []cass.SearchHit
	Err  error
	Gen  uint64
}

// HealthUpdateMsg is sent when agent health check completes
type HealthUpdateMsg struct {
	Health map[string]PaneHealthInfo // keyed by pane ID
	Err    error
}

// RoutingUpdateMsg is sent when routing scores are fetched
type RoutingUpdateMsg struct {
	Scores map[string]RoutingScore // keyed by pane ID
	Err    error
	Gen    uint64
}

// CheckpointUpdateMsg is sent when checkpoint status is fetched
type CheckpointUpdateMsg struct {
	Count     int // Total checkpoint count for session
	Latest    *checkpoint.Checkpoint
	LatestAge time.Duration // Age of latest checkpoint
	Status    string        // "recent", "stale", "old", "none"
	Err       error
	Gen       uint64
}

// HandoffUpdateMsg is sent when handoff status is fetched
type HandoffUpdateMsg struct {
	Goal   string
	Now    string
	Age    time.Duration
	Path   string
	Status string
	Err    error
	Gen    uint64
}

// CheckpointCreatedMsg is sent when a new checkpoint is created
type CheckpointCreatedMsg struct {
	Checkpoint *checkpoint.Checkpoint
	Err        error
}

// FileConflictMsg is sent when a file reservation conflict is detected
type FileConflictMsg struct {
	Conflict watcher.FileConflict
}

// RoutingScore holds routing info for a single agent
type RoutingScore struct {
	Score         float64 // 0-100 composite routing score
	IsRecommended bool    // True if this is the recommended agent
	State         string  // Agent state (waiting, generating, etc.)
}

// PaneHealthInfo holds health check results for a single pane
type PaneHealthInfo struct {
	Status       string   // "ok", "warning", "error", "unknown"
	Issues       []string // Issue messages
	RestartCount int      // Restarts in last hour
	Uptime       int      // Seconds of uptime
}

// PanelID identifies a dashboard panel
type PanelID int

const (
	PanelPaneList PanelID = iota
	PanelDetail
	PanelBeads
	PanelAlerts
	PanelConflicts // File reservation conflicts panel
	PanelMetrics
	PanelHistory
	PanelSidebar
	PanelCount // Total number of focusable panels
)

type refreshSource int

const (
	refreshSession refreshSource = iota
	refreshStatus
	refreshBeads
	refreshAlerts
	refreshMetrics
	refreshHistory
	refreshFiles
	refreshCass
	refreshScan
	refreshCheckpoint
	refreshHandoff
	refreshSpawn
	refreshAgentMail
	refreshAgentMailInbox
	refreshRouting
	refreshSourceCount
)

// Model is the session dashboard model
type Model struct {
	session      string
	projectDir   string
	panes        []tmux.Pane
	width        int
	height       int
	animTick     int
	cursor       int
	focusedPanel PanelID
	quitting     bool
	err          error

	// Diagnostics (opt-in)
	showDiagnostics     bool
	sessionFetchLatency time.Duration
	statusFetchLatency  time.Duration
	statusFetchErr      error

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

	// Refresh sequencing (prevents stale async updates)
	refreshSeq  [refreshSourceCount]uint64
	lastUpdated [refreshSourceCount]time.Time

	// Subsystem refresh timers
	lastPaneFetch        time.Time
	lastContextFetch     time.Time
	lastAlertsFetch      time.Time
	lastBeadsFetch       time.Time
	lastCassContextFetch time.Time
	lastScanFetch        time.Time
	lastHandoffFetch     time.Time

	// Fetch state tracking to prevent pile-up
	fetchingSession     bool
	fetchingContext     bool
	fetchingAlerts      bool
	fetchingBeads       bool
	fetchingCassContext bool
	fetchingMetrics     bool
	fetchingRouting     bool
	fetchingHistory     bool
	fetchingFileChanges bool
	fetchingScan        bool
	fetchingHandoff     bool
	scanDisabled        bool // User toggled UBS scanning off
	fetchingSpawn       bool
	spawnActive         bool // Whether a spawn is currently active (for adaptive polling)

	// Coalescing/cancellation for user-triggered refreshes
	sessionFetchPending bool
	sessionFetchCancel  context.CancelFunc
	contextFetchPending bool
	scanFetchPending    bool
	scanFetchCancel     context.CancelFunc

	// Auto-refresh configuration
	refreshInterval time.Duration

	// Refresh cadence (configurable; defaults match the constants below)
	paneRefreshInterval        time.Duration
	contextRefreshInterval     time.Duration
	alertsRefreshInterval      time.Duration
	beadsRefreshInterval       time.Duration
	cassContextRefreshInterval time.Duration
	scanRefreshInterval        time.Duration
	checkpointRefreshInterval  time.Duration
	handoffRefreshInterval     time.Duration
	spawnRefreshInterval       time.Duration // How often to poll spawn state (faster when active)

	// Pane output capture budgeting/caching
	paneOutputLines         int
	paneOutputCaptureBudget int
	paneOutputCaptureCursor int
	paneOutputCache         map[string]string
	paneOutputLastCaptured  map[string]time.Time
	renderedOutputCache     map[string]string // Cache for expensive markdown rendering

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
	agentMailAvailable       bool
	agentMailConnected       bool
	agentMailArchiveFound    bool                // Fallback: archive directory exists
	agentMailLocks           int                 // Active file reservations
	agentMailUnread          int                 // Unread message count (requires agent context)
	agentMailUrgent          int                 // Urgent unread count (subset of unread)
	agentMailLockInfo        []AgentMailLockInfo // Lock details for display
	agentMailInbox           map[string][]agentmail.InboxMessage
	agentMailInboxErrors     map[string]error
	agentMailAgents          map[string]string // paneID -> agent name
	fetchingMailInbox        bool
	lastMailInboxFetch       time.Time
	mailInboxRefreshInterval time.Duration
	showInboxDetails         bool

	// Config watcher
	configSub    chan *config.Config
	configCloser func()
	cfg          *config.Config

	// Markdown renderer
	renderer *glamour.TermRenderer

	// CASS Search
	showCassSearch bool
	cassSearch     components.CassSearchModel

	// Help overlay
	showHelp bool

	// Panels
	beadsPanel     *panels.BeadsPanel
	alertsPanel    *panels.AlertsPanel
	metricsPanel   *panels.MetricsPanel
	historyPanel   *panels.HistoryPanel
	cassPanel      *panels.CASSPanel
	filesPanel     *panels.FilesPanel
	tickerPanel    *panels.TickerPanel
	spawnPanel     *panels.SpawnPanel
	conflictsPanel *panels.ConflictsPanel

	// Data for new panels
	beadsSummary  bv.BeadsSummary
	beadsReady    []bv.BeadPreview
	activeAlerts  []alerts.Alert
	metricsTokens int
	metricsCost   float64
	metricsData   panels.MetricsData // Cached full metrics data for panel
	cmdHistory    []history.HistoryEntry
	fileChanges   []tracker.RecordedFileChange
	cassContext   []cass.SearchHit
	routingScores map[string]RoutingScore // keyed by pane ID

	// Checkpoint status
	checkpointCount     int                    // Number of checkpoints for this session
	latestCheckpoint    *checkpoint.Checkpoint // Most recent checkpoint
	checkpointStatus    string                 // "recent", "stale", "old", "none"
	lastCheckpointFetch time.Time
	fetchingCheckpoint  bool
	checkpointError     error
	lastSpawnFetch      time.Time

	// Handoff status
	handoffGoal   string
	handoffNow    string
	handoffAge    time.Duration
	handoffPath   string
	handoffStatus string
	handoffError  error

	// UBS bug scanner status
	bugsCritical int  // Critical bugs from last scan
	bugsWarning  int  // Warning bugs from last scan
	bugsInfo     int  // Info bugs from last scan
	bugsScanned  bool // Whether a scan has been run

	// Error tracking for data sources (displayed as badges)
	beadsError       error
	alertsError      error
	metricsError     error
	historyError     error
	fileChangesError error
	cassError        error
	routingError     error
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

	// Agent Mail inbox tracking
	MailUnread int
	MailUrgent int

	TokenVelocity float64 // Estimated tokens/sec

	// Health tracking
	HealthStatus  string   // "ok", "warning", "error", "unknown"
	HealthIssues  []string // List of issue messages (rate limit, crash, etc.)
	RestartCount  int      // Number of restarts in last hour
	UptimeSeconds int      // Seconds since agent started (negative = uptime from tracker)

	// Rotation tracking
	IsRotating bool       // True when agent rotation is in progress
	RotatedAt  *time.Time // When agent was last rotated (nil if never)
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
	NextPanel      key.Binding // Tab to cycle panels
	PrevPanel      key.Binding // Shift+Tab to cycle back
	Send           key.Binding
	Refresh        key.Binding
	Pause          key.Binding
	Quit           key.Binding
	ContextRefresh key.Binding // 'c' to refresh context data
	MailRefresh    key.Binding // 'm' to refresh Agent Mail data
	InboxToggle    key.Binding // 'i' to toggle inbox details
	CassSearch     key.Binding // 'ctrl+s' to open CASS search
	Help           key.Binding // '?' to toggle help overlay
	Diagnostics    key.Binding // 'd' to toggle diagnostics
	ScanToggle     key.Binding // 'u' to toggle UBS scanning
	Checkpoint     key.Binding // 'ctrl+k' to create checkpoint
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

// DefaultRefreshInterval is the default auto-refresh interval
const DefaultRefreshInterval = 2 * time.Second

// Per-subsystem refresh cadence (driven by DashboardTickMsg)
const (
	PaneRefreshInterval        = 1 * time.Second
	ContextRefreshInterval     = 10 * time.Second
	AlertsRefreshInterval      = 3 * time.Second
	BeadsRefreshInterval       = 5 * time.Second
	CassContextRefreshInterval = 15 * time.Minute
	ScanRefreshInterval        = 1 * time.Minute
	CheckpointRefreshInterval  = 30 * time.Second
	HandoffRefreshInterval     = 30 * time.Second
	SpawnActiveRefreshInterval = 500 * time.Millisecond // Poll frequently when spawn is active
	SpawnIdleRefreshInterval   = 2 * time.Second        // Poll slowly when no spawn is active
	MailInboxRefreshInterval   = 30 * time.Second
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
	NextPanel:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
	PrevPanel:      key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
	Send:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "send prompt")),
	Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:          key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause/resume auto-refresh")),
	Quit:           key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q/esc", "quit")),
	ContextRefresh: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "refresh context")),
	MailRefresh:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "refresh mail")),
	InboxToggle:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inbox details")),
	CassSearch:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "cass search")),
	Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
	Diagnostics:    key.NewBinding(key.WithKeys("d", "ctrl+d"), key.WithHelp("d/ctrl+d", "toggle diagnostics")),
	ScanToggle:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "toggle UBS scan")),
	Checkpoint:     key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "create checkpoint")),
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

// New creates a new dashboard model
func New(session, projectDir string) Model {
	t := theme.Current()
	ic := icons.Current()

	m := Model{
		session:                    session,
		projectDir:                 projectDir,
		width:                      80,
		height:                     24,
		tier:                       layout.TierForWidth(80),
		theme:                      t,
		icons:                      ic,
		compaction:                 status.NewCompactionRecoveryIntegrationDefault(),
		paneStatus:                 make(map[int]PaneStatus),
		detector:                   status.NewDetector(),
		agentStatuses:              make(map[string]status.AgentStatus),
		refreshInterval:            DefaultRefreshInterval,
		paneRefreshInterval:        PaneRefreshInterval,
		contextRefreshInterval:     ContextRefreshInterval,
		alertsRefreshInterval:      AlertsRefreshInterval,
		beadsRefreshInterval:       BeadsRefreshInterval,
		cassContextRefreshInterval: CassContextRefreshInterval,
		scanRefreshInterval:        ScanRefreshInterval,
		checkpointRefreshInterval:  CheckpointRefreshInterval,
		handoffRefreshInterval:     HandoffRefreshInterval,
		spawnRefreshInterval:       SpawnIdleRefreshInterval,
		mailInboxRefreshInterval:   MailInboxRefreshInterval,
		paneOutputLines:            50,
		paneOutputCaptureBudget:    20,
		paneOutputCache:            make(map[string]string),
		paneOutputLastCaptured:     make(map[string]time.Time),
		renderedOutputCache:        make(map[string]string),
		healthStatus:               "unknown",
		healthMessage:              "",
		agentMailInbox:             make(map[string][]agentmail.InboxMessage),
		agentMailInboxErrors:       make(map[string]error),
		agentMailAgents:            make(map[string]string),
		cassSearch: components.NewCassSearch(func(hit cass.SearchHit) tea.Cmd {
			return func() tea.Msg {
				return CassSelectMsg{Hit: hit}
			}
		}),
		beadsPanel:     panels.NewBeadsPanel(),
		alertsPanel:    panels.NewAlertsPanel(),
		metricsPanel:   panels.NewMetricsPanel(),
		historyPanel:   panels.NewHistoryPanel(),
		cassPanel:      panels.NewCASSPanel(),
		filesPanel:     panels.NewFilesPanel(),
		tickerPanel:    panels.NewTickerPanel(),
		spawnPanel:     panels.NewSpawnPanel(),
		conflictsPanel: panels.NewConflictsPanel(),

		// Init() kicks off these fetches immediately; mark as fetching so the tick loop
		// doesn't pile on duplicates if the first round is still in flight.
		fetchingSession:     true,
		fetchingContext:     true,
		fetchingAlerts:      true,
		fetchingBeads:       true,
		fetchingCassContext: true,
		fetchingMetrics:     true,
		fetchingRouting:     true,
		fetchingHistory:     true,
		fetchingFileChanges: true,
		fetchingCheckpoint:  true,
		fetchingHandoff:     true,
		fetchingMailInbox:   true,
	}

	// Initialize last-fetch timestamps to start cadence after the initial fetches from Init.
	now := time.Now()
	m.lastPaneFetch = now
	m.lastContextFetch = now
	m.lastAlertsFetch = now
	m.lastBeadsFetch = now
	m.lastCassContextFetch = now
	m.lastScanFetch = now
	m.lastCheckpointFetch = now
	m.lastHandoffFetch = now
	m.lastSpawnFetch = now
	m.lastMailInboxFetch = now

	applyDashboardEnvOverrides(&m)

	// Set up conflict action handler for the conflicts panel
	m.conflictsPanel.SetActionHandler(m.handleConflictAction)

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
func NewWithInterval(session, projectDir string, interval time.Duration) Model {
	m := New(session, projectDir)
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
		m.fetchHealthCmd(), // Agent health check
		m.fetchAgentMailStatus(),
		m.fetchAgentMailInboxes(),
		m.fetchBeadsCmd(),
		m.fetchAlertsCmd(),
		m.fetchMetricsCmd(),
		m.fetchRoutingCmd(),
		m.fetchHistoryCmd(),
		m.fetchFileChangesCmd(),
		m.fetchCASSContextCmd(),
		m.fetchCheckpointStatus(),
		m.fetchHandoffCmd(),
		m.subscribeToConfig(),
	)
}

func (m *Model) nextGen(src refreshSource) uint64 {
	m.refreshSeq[src]++
	return m.refreshSeq[src]
}

func (m *Model) isStale(src refreshSource, gen uint64) bool {
	return gen > 0 && gen < m.refreshSeq[src]
}

func (m *Model) markUpdated(src refreshSource, t time.Time) {
	if t.IsZero() {
		t = time.Now()
	}
	m.lastUpdated[src] = t
}

func (m *Model) acceptUpdate(src refreshSource, gen uint64) bool {
	if m.isStale(src, gen) {
		return false
	}
	if gen > m.refreshSeq[src] {
		m.refreshSeq[src] = gen
	}
	return true
}

func (m *Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	prevWidth := m.width
	prevHeight := m.height
	prevTier := m.tier

	width := msg.Width
	height := msg.Height
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	m.width = width
	m.height = height
	m.tier = layout.TierForWidthWithHysteresis(width, prevTier)

	m.cycleFocus(0)

	_, detailWidth := layout.SplitProportions(width)
	contentWidth := detailWidth - 4
	if contentWidth < 20 {
		contentWidth = 20
	}
	m.initRenderer(contentWidth)

	if prevWidth != m.width || prevHeight != m.height {
		m.renderedOutputCache = make(map[string]string)
	}

	searchW := int(float64(width) * 0.6)
	searchH := int(float64(height) * 0.6)
	if searchW < 20 {
		searchW = 20
	}
	if searchH < 10 {
		searchH = 10
	}
	m.cassSearch.SetSize(searchW, searchH)

	m.resizePanelsForLayout()

	if dashboardDebugEnabled(m) {
		contentHeight := contentHeightFor(m.height)
		log.Printf("[dashboard] resize width=%d height=%d contentHeight=%d tier=%s",
			m.width, m.height, contentHeight, tierLabel(m.tier))
		log.Printf("[dashboard] panels %s %s %s %s %s %s %s",
			logPanelSize("beads", m.beadsPanel),
			logPanelSize("alerts", m.alertsPanel),
			logPanelSize("metrics", m.metricsPanel),
			logPanelSize("history", m.historyPanel),
			logPanelSize("files", m.filesPanel),
			logPanelSize("cass", m.cassPanel),
			logPanelSize("spawn", m.spawnPanel),
		)
	}

	if prevTier != m.tier {
		log.Printf("[dashboard] tier transition %s -> %s (width=%d height=%d)",
			tierLabel(prevTier), tierLabel(m.tier), m.width, m.height)
	}

	return m, nil
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

		result := bv.CheckDrift(m.projectDir)
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

func (m *Model) fetchScanStatusWithContext(ctx context.Context) tea.Cmd {
	gen := m.nextGen(refreshScan)
	return func() tea.Msg {
		if !scanner.IsAvailable() {
			return ScanStatusMsg{Status: "unavailable", Gen: gen}
		}

		if ctx == nil {
			ctx = context.Background()
		}

		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		opts := scanner.ScanOptions{
			DiffOnly: true,
			Timeout:  15 * time.Second,
		}

		start := time.Now()
		result, err := scanner.QuickScanWithOptions(ctx, ".", opts)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if errors.Is(ctxErr, context.Canceled) {
					return ScanStatusMsg{Err: ctxErr, Gen: gen}
				}
				return ScanStatusMsg{Status: "error", Err: ctxErr, Gen: gen}
			}
			if errors.Is(err, context.Canceled) {
				return ScanStatusMsg{Err: err, Gen: gen}
			}
			return ScanStatusMsg{Status: "error", Err: err, Gen: gen}
		}
		if result == nil {
			return ScanStatusMsg{Status: "unavailable", Gen: gen}
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
			Gen:      gen,
		}
	}
}

func (m *Model) newAgentMailClient(projectKey string) *agentmail.Client {
	if projectKey == "" {
		return nil
	}
	var opts []agentmail.Option
	opts = append(opts, agentmail.WithProjectKey(projectKey))
	if m.cfg != nil {
		if !m.cfg.AgentMail.Enabled {
			return nil
		}
		if m.cfg.AgentMail.URL != "" {
			opts = append(opts, agentmail.WithBaseURL(m.cfg.AgentMail.URL))
		}
		if m.cfg.AgentMail.Token != "" {
			opts = append(opts, agentmail.WithToken(m.cfg.AgentMail.Token))
		}
	}
	return agentmail.NewClient(opts...)
}

// fetchAgentMailStatus fetches Agent Mail data (locks, connection status)
func (m *Model) fetchAgentMailStatus() tea.Cmd {
	gen := m.nextGen(refreshAgentMail)
	projectKey := m.projectDir
	return func() tea.Msg {
		if projectKey == "" {
			return AgentMailUpdateMsg{Available: false, Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil {
			return AgentMailUpdateMsg{Available: false, Gen: gen}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Check availability via HTTP
		if !client.IsAvailable() {
			// Fallback: check if archive directory exists
			// This detects Agent Mail running via MCP stdio protocol (not HTTP)
			archiveFound := agentmail.HasArchiveForProject(projectKey)
			return AgentMailUpdateMsg{
				Available:    false,
				ArchiveFound: archiveFound,
				Gen:          gen,
			}
		}

		// Ensure project exists
		_, err := client.EnsureProject(ctx, projectKey)
		if err != nil {
			return AgentMailUpdateMsg{Available: true, Connected: false, Gen: gen}
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
			Gen:       gen,
		}
	}
}

// fetchAgentMailInboxes polls inbox summaries for all registered agents in this session.
func (m *Model) fetchAgentMailInboxes() tea.Cmd {
	gen := m.nextGen(refreshAgentMailInbox)
	projectKey := m.projectDir
	sessionName := m.session
	panes := append([]tmux.Pane(nil), m.panes...)

	return func() tea.Msg {
		if projectKey == "" || sessionName == "" {
			return AgentMailInboxSummaryMsg{Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil || !client.IsAvailable() {
			return AgentMailInboxSummaryMsg{Gen: gen}
		}

		registry, err := agentmail.LoadSessionAgentRegistry(sessionName, projectKey)
		if err != nil {
			return AgentMailInboxSummaryMsg{Err: err, Gen: gen}
		}
		if registry == nil {
			return AgentMailInboxSummaryMsg{AgentMap: map[string]string{}, Gen: gen}
		}

		agentMap := make(map[string]string)
		for _, pane := range panes {
			if pane.Type == tmux.AgentUser {
				continue
			}
			if name, ok := registry.GetAgent(pane.Title, pane.ID); ok {
				agentMap[pane.ID] = name
			}
		}

		if len(agentMap) == 0 {
			return AgentMailInboxSummaryMsg{AgentMap: agentMap, Gen: gen}
		}

		type job struct {
			paneID    string
			agentName string
		}
		type result struct {
			paneID string
			inbox  []agentmail.InboxMessage
			err    error
		}

		inboxes := make(map[string][]agentmail.InboxMessage, len(agentMap))
		var firstErr error
		var mu sync.Mutex
		jobs := make(chan job)
		results := make(chan result, len(agentMap))
		workerCount := 4
		if len(agentMap) < workerCount {
			workerCount = len(agentMap)
		}

		var wg sync.WaitGroup
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
						ProjectKey:    projectKey,
						AgentName:     j.agentName,
						UrgentOnly:    false,
						Limit:         5,
						IncludeBodies: false,
					})
					cancel()
					results <- result{paneID: j.paneID, inbox: msgs, err: err}
				}
			}()
		}

		go func() {
			for paneID, agentName := range agentMap {
				jobs <- job{paneID: paneID, agentName: agentName}
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()

		for res := range results {
			if res.err != nil {
				if firstErr == nil {
					firstErr = res.err
				}
				continue
			}
			mu.Lock()
			inboxes[res.paneID] = res.inbox
			mu.Unlock()
		}

		return AgentMailInboxSummaryMsg{
			Inboxes:  inboxes,
			AgentMap: agentMap,
			Err:      firstErr,
			Gen:      gen,
		}
	}
}

// fetchAgentMailInboxDetails fetches message bodies for a single pane's agent.
func (m *Model) fetchAgentMailInboxDetails(pane tmux.Pane) tea.Cmd {
	gen := m.nextGen(refreshAgentMailInbox)
	projectKey := m.projectDir
	sessionName := m.session
	paneID := pane.ID
	paneTitle := pane.Title

	return func() tea.Msg {
		if projectKey == "" || sessionName == "" {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil || !client.IsAvailable() {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		registry, err := agentmail.LoadSessionAgentRegistry(sessionName, projectKey)
		if err != nil || registry == nil {
			return AgentMailInboxDetailMsg{PaneID: paneID, Err: err, Gen: gen}
		}

		agentName, ok := registry.GetAgent(paneTitle, paneID)
		if !ok {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     agentName,
			UrgentOnly:    false,
			Limit:         5,
			IncludeBodies: true,
		})
		cancel()

		return AgentMailInboxDetailMsg{
			PaneID:   paneID,
			Messages: msgs,
			Err:      err,
			Gen:      gen,
		}
	}
}

// fetchCheckpointStatus fetches checkpoint status for the session
func (m *Model) fetchCheckpointStatus() tea.Cmd {
	gen := m.nextGen(refreshCheckpoint)
	session := m.session
	return func() tea.Msg {
		storage := checkpoint.NewStorage()
		checkpoints, err := storage.List(session)
		if err != nil {
			return CheckpointUpdateMsg{
				Status: "none",
				Err:    err,
				Gen:    gen,
			}
		}

		if len(checkpoints) == 0 {
			return CheckpointUpdateMsg{
				Count:  0,
				Status: "none",
				Gen:    gen,
			}
		}

		// Latest is first (sorted by creation time, newest first)
		latest := checkpoints[0]
		age := latest.Age()

		// Determine status based on age
		var status string
		switch {
		case age < 30*time.Minute:
			status = "recent"
		case age < 1*time.Hour:
			status = "stale"
		default:
			status = "old"
		}

		return CheckpointUpdateMsg{
			Count:     len(checkpoints),
			Latest:    latest,
			LatestAge: age,
			Status:    status,
			Gen:       gen,
		}
	}
}

// createCheckpointCmd creates a new checkpoint for the session
func (m Model) createCheckpointCmd() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		capturer := checkpoint.NewCapturer()
		cp, err := capturer.Create(session, "dashboard")
		if err != nil {
			return CheckpointCreatedMsg{Err: err}
		}
		return CheckpointCreatedMsg{Checkpoint: cp}
	}
}

// Helper struct to carry output data
type PaneOutputData struct {
	PaneID       string
	PaneIndex    int
	LastActivity time.Time
	Output       string
	AgentType    string
}

type SessionDataWithOutputMsg struct {
	Panes             []tmux.Pane
	Outputs           []PaneOutputData
	Duration          time.Duration
	NextCaptureCursor int
	Err               error
	Gen               uint64
}

func (m *Model) fetchSessionDataWithOutputs() tea.Cmd {
	return m.fetchSessionDataWithOutputsCtx(context.Background())
}

func (m *Model) requestSessionFetch(cancelInFlight bool) tea.Cmd {
	m.sessionFetchPending = true

	if m.fetchingSession {
		if cancelInFlight && m.sessionFetchCancel != nil {
			m.sessionFetchCancel()
		}
		return nil
	}

	return m.startSessionFetch()
}

func (m *Model) startSessionFetch() tea.Cmd {
	if m.fetchingSession || !m.sessionFetchPending {
		return nil
	}

	m.sessionFetchPending = false
	m.fetchingSession = true
	m.lastPaneFetch = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	m.sessionFetchCancel = cancel

	return m.fetchSessionDataWithOutputsCtx(ctx)
}

func (m *Model) finishSessionFetch() tea.Cmd {
	m.fetchingSession = false
	if m.sessionFetchCancel != nil {
		m.sessionFetchCancel()
		m.sessionFetchCancel = nil
	}

	return m.startSessionFetch()
}

func (m *Model) requestStatusesFetch() tea.Cmd {
	m.contextFetchPending = true

	if m.fetchingContext {
		return nil
	}

	return m.startStatusesFetch()
}

func (m *Model) startStatusesFetch() tea.Cmd {
	if m.fetchingContext || !m.contextFetchPending {
		return nil
	}

	m.contextFetchPending = false
	m.fetchingContext = true
	m.lastContextFetch = time.Now()

	return m.fetchStatuses()
}

func (m *Model) finishStatusesFetch() tea.Cmd {
	m.fetchingContext = false
	return m.startStatusesFetch()
}

func (m *Model) requestScanFetch(cancelInFlight bool) tea.Cmd {
	m.scanFetchPending = true

	if m.fetchingScan {
		if cancelInFlight && m.scanFetchCancel != nil {
			m.scanFetchCancel()
		}
		return nil
	}

	return m.startScanFetch()
}

func (m *Model) startScanFetch() tea.Cmd {
	if m.fetchingScan || !m.scanFetchPending {
		return nil
	}

	m.scanFetchPending = false
	m.fetchingScan = true

	ctx, cancel := context.WithCancel(context.Background())
	m.scanFetchCancel = cancel

	return m.fetchScanStatusWithContext(ctx)
}

func (m *Model) finishScanFetch() tea.Cmd {
	m.fetchingScan = false
	if m.scanFetchCancel != nil {
		m.scanFetchCancel()
		m.scanFetchCancel = nil
	}

	return m.startScanFetch()
}

func (m *Model) fullRefresh(cancelInFlight bool) []tea.Cmd {
	var cmds []tea.Cmd
	now := time.Now()

	if cmd := m.requestSessionFetch(cancelInFlight); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.requestStatusesFetch(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.requestScanFetch(cancelInFlight); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if !m.fetchingBeads {
		m.fetchingBeads = true
		m.lastBeadsFetch = now
		cmds = append(cmds, m.fetchBeadsCmd())
	}
	if !m.fetchingAlerts {
		m.fetchingAlerts = true
		m.lastAlertsFetch = now
		cmds = append(cmds, m.fetchAlertsCmd())
	}
	if !m.fetchingMetrics {
		m.fetchingMetrics = true
		cmds = append(cmds, m.fetchMetricsCmd())
	}
	if !m.fetchingRouting {
		m.fetchingRouting = true
		cmds = append(cmds, m.fetchRoutingCmd())
	}
	if !m.fetchingHistory {
		m.fetchingHistory = true
		cmds = append(cmds, m.fetchHistoryCmd())
	}
	if !m.fetchingFileChanges {
		m.fetchingFileChanges = true
		cmds = append(cmds, m.fetchFileChangesCmd())
	}
	if !m.fetchingCassContext {
		m.fetchingCassContext = true
		m.lastCassContextFetch = now
		cmds = append(cmds, m.fetchCASSContextCmd())
	}
	if !m.fetchingCheckpoint {
		m.fetchingCheckpoint = true
		m.lastCheckpointFetch = now
		cmds = append(cmds, m.fetchCheckpointStatus())
	}
	if !m.fetchingHandoff {
		m.fetchingHandoff = true
		m.lastHandoffFetch = now
		cmds = append(cmds, m.fetchHandoffCmd())
	}
	if !m.fetchingSpawn {
		m.fetchingSpawn = true
		m.lastSpawnFetch = now
		cmds = append(cmds, m.fetchSpawnStateCmd())
	}
	if !m.fetchingMailInbox {
		m.fetchingMailInbox = true
		m.lastMailInboxFetch = now
		cmds = append(cmds, m.fetchAgentMailInboxes())
	}

	// Agent mail status is light enough to refresh on demand.
	cmds = append(cmds, m.fetchAgentMailStatus())

	return cmds
}

func (m *Model) fetchSessionDataWithOutputsCtx(ctx context.Context) tea.Cmd {
	gen := m.nextGen(refreshSession)
	outputLines := m.paneOutputLines
	budget := m.paneOutputCaptureBudget
	startCursor := m.paneOutputCaptureCursor
	lastCaptured := copyTimeMap(m.paneOutputLastCaptured)

	selectedPaneID := ""
	if m.cursor >= 0 && m.cursor < len(m.panes) {
		selectedPaneID = m.panes[m.cursor].ID
	}

	session := m.session

	return func() tea.Msg {
		start := time.Now()
		if ctx == nil {
			ctx = context.Background()
		}

		panesWithActivity, err := tmux.GetPanesWithActivityContext(ctx, session)
		if err != nil {
			return SessionDataWithOutputMsg{Err: err, Duration: time.Since(start), Gen: gen}
		}

		panes := make([]tmux.Pane, 0, len(panesWithActivity))
		for _, pane := range panesWithActivity {
			panes = append(panes, pane.Pane)
		}

		plan := planPaneCaptures(panesWithActivity, selectedPaneID, lastCaptured, budget, startCursor)

		var outputs []PaneOutputData
		for _, pane := range plan.Targets {
			if err := ctx.Err(); err != nil {
				return SessionDataWithOutputMsg{Err: err, Duration: time.Since(start), Gen: gen}
			}

			out, err := tmux.CapturePaneOutputContext(ctx, pane.Pane.ID, outputLines)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return SessionDataWithOutputMsg{Err: ctxErr, Duration: time.Since(start), Gen: gen}
				}
				continue
			}

			outputs = append(outputs, PaneOutputData{
				PaneID:       pane.Pane.ID,
				PaneIndex:    pane.Pane.Index,
				LastActivity: pane.LastActivity,
				Output:       out,
				AgentType:    string(pane.Pane.Type), // Simplified mapping
			})
		}

		if err := ctx.Err(); err != nil {
			return SessionDataWithOutputMsg{Err: err, Duration: time.Since(start), Gen: gen}
		}

		return SessionDataWithOutputMsg{
			Panes:             panes,
			Outputs:           outputs,
			Duration:          time.Since(start),
			NextCaptureCursor: plan.NextCursor,
			Gen:               gen,
		}
	}
}

type paneCapturePlan struct {
	Targets    []tmux.PaneActivity
	NextCursor int
}

func planPaneCaptures(panes []tmux.PaneActivity, selectedPaneID string, lastCaptured map[string]time.Time, budget int, startCursor int) paneCapturePlan {
	var candidates []tmux.PaneActivity
	for _, pane := range panes {
		if pane.Pane.Type == tmux.AgentUser {
			continue
		}
		candidates = append(candidates, pane)
	}

	if budget <= 0 || len(candidates) == 0 {
		next := 0
		if len(candidates) > 0 {
			next = startCursor % len(candidates)
			if next < 0 {
				next = 0
			}
		}
		return paneCapturePlan{Targets: nil, NextCursor: next}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Pane.Index < candidates[j].Pane.Index
	})

	if startCursor < 0 {
		startCursor = 0
	}
	startCursor = startCursor % len(candidates)

	selected := make(map[string]struct{}, budget)
	var targets []tmux.PaneActivity

	if selectedPaneID != "" {
		for _, pane := range candidates {
			if pane.Pane.ID == selectedPaneID {
				selected[pane.Pane.ID] = struct{}{}
				targets = append(targets, pane)
				budget--
				break
			}
		}
	}

	type captureCandidate struct {
		pane tmux.PaneActivity
	}

	var needs []captureCandidate
	if budget > 0 {
		for _, pane := range candidates {
			if _, ok := selected[pane.Pane.ID]; ok {
				continue
			}

			last, ok := lastCaptured[pane.Pane.ID]
			if !ok || pane.LastActivity.After(last) {
				needs = append(needs, captureCandidate{pane: pane})
			}
		}
	}

	sort.Slice(needs, func(i, j int) bool {
		if needs[i].pane.LastActivity.Equal(needs[j].pane.LastActivity) {
			return needs[i].pane.Pane.Index < needs[j].pane.Pane.Index
		}
		return needs[i].pane.LastActivity.After(needs[j].pane.LastActivity)
	})

	for _, c := range needs {
		if budget <= 0 {
			break
		}
		if _, ok := selected[c.pane.Pane.ID]; ok {
			continue
		}
		selected[c.pane.Pane.ID] = struct{}{}
		targets = append(targets, c.pane)
		budget--
	}

	rrSteps := 0
	for budget > 0 && rrSteps < len(candidates) {
		idx := (startCursor + rrSteps) % len(candidates)
		pane := candidates[idx]
		rrSteps++
		if _, ok := selected[pane.Pane.ID]; ok {
			continue
		}
		selected[pane.Pane.ID] = struct{}{}
		targets = append(targets, pane)
		budget--
	}

	nextCursor := startCursor
	if rrSteps > 0 {
		nextCursor = (startCursor + rrSteps) % len(candidates)
	}

	return paneCapturePlan{Targets: targets, NextCursor: nextCursor}
}

func copyTimeMap(src map[string]time.Time) map[string]time.Time {
	if len(src) == 0 {
		return nil
	}

	copied := make(map[string]time.Time, len(src))
	for k, v := range src {
		copied[k] = v
	}
	return copied
}

// fetchStatuses runs unified status detection across all panes
func (m *Model) fetchStatuses() tea.Cmd {
	gen := m.nextGen(refreshStatus)
	return func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()

		statuses, err := m.detector.DetectAllContext(ctx, m.session)
		duration := time.Since(start)
		if err != nil {
			// Keep UI responsive even if detection fails
			return StatusUpdateMsg{Statuses: nil, Time: time.Now(), Duration: duration, Err: err, Gen: gen}
		}
		return StatusUpdateMsg{Statuses: statuses, Time: time.Now(), Duration: duration, Gen: gen}
	}
}

// fetchHealthCmd fetches health status for all agents in the session
func (m Model) fetchHealthCmd() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		// Get health check from health package
		sessionHealth, err := health.CheckSession(session)
		if err != nil {
			return HealthUpdateMsg{Health: nil, Err: err}
		}

		// Get health tracker for uptime/restart data
		tracker := robot.GetHealthTracker(session)

		// Build health info map
		healthMap := make(map[string]PaneHealthInfo)
		for _, agent := range sessionHealth.Agents {
			info := PaneHealthInfo{
				Status: string(agent.Status),
			}

			// Collect issues
			for _, issue := range agent.Issues {
				info.Issues = append(info.Issues, issue.Message)
			}

			// Get uptime and restart count from tracker
			info.Uptime = int(tracker.GetUptime(agent.PaneID).Seconds())
			info.RestartCount = tracker.GetRestartsInWindow(agent.PaneID)

			healthMap[agent.PaneID] = info
		}

		return HealthUpdateMsg{Health: healthMap, Err: nil}
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

	case panels.ReplayMsg:
		// Handle replay request from history panel
		return m, m.executeReplay(msg.Entry)

	case BeadsUpdateMsg:
		if !m.acceptUpdate(refreshBeads, msg.Gen) {
			return m, nil
		}
		m.fetchingBeads = false
		m.beadsError = msg.Err
		if msg.Err == nil {
			m.beadsSummary = msg.Summary
			m.beadsReady = msg.Ready
			m.markUpdated(refreshBeads, time.Now())
		}
		m.beadsPanel.SetData(m.beadsSummary, m.beadsReady, m.beadsError)
		return m, nil

	case AlertsUpdateMsg:
		if !m.acceptUpdate(refreshAlerts, msg.Gen) {
			return m, nil
		}
		m.fetchingAlerts = false
		m.alertsError = msg.Err
		if msg.Err == nil {
			m.activeAlerts = msg.Alerts
			m.markUpdated(refreshAlerts, time.Now())
		}
		m.alertsPanel.SetData(m.activeAlerts, m.alertsError)
		return m, nil

	case FileConflictMsg:
		// Add the conflict to the conflicts panel
		m.conflictsPanel.AddConflict(msg.Conflict)
		return m, nil

	case panels.ConflictActionResultMsg:
		// Handle conflict action result
		if msg.Err != nil {
			// Log error but don't block
			log.Printf("[Dashboard] Conflict action error: %v", msg.Err)
		}
		// Remove the conflict from the panel if action was successful or dismissed
		if msg.Err == nil {
			m.conflictsPanel.RemoveConflict(msg.Conflict.Path, msg.Conflict.RequestorAgent)
		}
		return m, nil

	case SpawnUpdateMsg:
		if !m.acceptUpdate(refreshSpawn, msg.Gen) {
			return m, nil
		}
		m.fetchingSpawn = false
		m.spawnPanel.SetData(msg.Data)
		m.markUpdated(refreshSpawn, time.Now())

		// Adaptive polling: faster when spawn is active for smooth countdown display,
		// slower when idle to reduce CPU/render churn
		wasActive := m.spawnActive
		m.spawnActive = msg.Data.Active && !msg.Data.IsComplete()

		// Only adjust interval if not overridden by env var (check if it's one of the defaults)
		if m.spawnRefreshInterval == SpawnIdleRefreshInterval || m.spawnRefreshInterval == SpawnActiveRefreshInterval {
			if m.spawnActive {
				m.spawnRefreshInterval = SpawnActiveRefreshInterval
			} else if wasActive && !m.spawnActive {
				// Spawn just completed - switch back to idle rate
				m.spawnRefreshInterval = SpawnIdleRefreshInterval
			}
		}
		return m, nil

	case MetricsUpdateMsg:
		if !m.acceptUpdate(refreshMetrics, msg.Gen) {
			return m, nil
		}
		m.fetchingMetrics = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.metricsError = msg.Err
		if msg.Err == nil {
			m.metricsTokens = msg.Data.TotalTokens
			m.metricsCost = msg.Data.TotalCost
			m.metricsData = msg.Data
			m.markUpdated(refreshMetrics, time.Now())
		}
		m.metricsPanel.SetData(m.metricsData, m.metricsError)
		return m, nil

	case HistoryUpdateMsg:
		if !m.acceptUpdate(refreshHistory, msg.Gen) {
			return m, nil
		}
		m.fetchingHistory = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.historyError = msg.Err
		if msg.Err == nil {
			m.cmdHistory = msg.Entries
			m.markUpdated(refreshHistory, time.Now())
		}
		m.historyPanel.SetEntries(m.cmdHistory, m.historyError)
		return m, nil

	case FileChangeMsg:
		if !m.acceptUpdate(refreshFiles, msg.Gen) {
			return m, nil
		}
		m.fetchingFileChanges = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.fileChangesError = msg.Err
		if msg.Err == nil {
			m.fileChanges = msg.Changes
			m.markUpdated(refreshFiles, time.Now())
		}
		if m.filesPanel != nil {
			m.filesPanel.SetData(m.fileChanges, m.fileChangesError)
		}
		return m, nil

	case CASSContextMsg:
		if !m.acceptUpdate(refreshCass, msg.Gen) {
			return m, nil
		}
		m.fetchingCassContext = false
		m.cassError = msg.Err
		m.cassContext = msg.Hits
		if m.cassPanel != nil {
			m.cassPanel.SetData(m.cassContext, m.cassError)
		}
		if msg.Err == nil {
			m.markUpdated(refreshCass, time.Now())
		}
		return m, nil

	case RoutingUpdateMsg:
		if !m.acceptUpdate(refreshRouting, msg.Gen) {
			return m, nil
		}
		m.fetchingRouting = false
		m.routingError = msg.Err
		if msg.Err == nil && msg.Scores != nil {
			m.routingScores = msg.Scores
			// Merge routing data into metrics data
			m.metricsData = m.mergeRoutingIntoMetrics(m.metricsData, msg.Scores)
			m.metricsPanel.SetData(m.metricsData, m.metricsError)
			m.markUpdated(refreshRouting, time.Now())
		}
		return m, nil

	case tea.WindowSizeMsg:
		prevWidth := m.width
		prevHeight := m.height
		prevTier := m.tier

		m.width = msg.Width
		m.height = msg.Height
		m.tier = layout.TierForWidthWithHysteresis(msg.Width, prevTier)

		m.cycleFocus(0)

		_, detailWidth := layout.SplitProportions(msg.Width)
		contentWidth := detailWidth - 4
		if contentWidth < 20 {
			contentWidth = 20
		}
		m.initRenderer(contentWidth)

		if prevWidth != m.width || prevHeight != m.height {
			m.renderedOutputCache = make(map[string]string)
		}

		searchW := int(float64(msg.Width) * 0.6)
		searchH := int(float64(msg.Height) * 0.6)
		m.cassSearch.SetSize(searchW, searchH)

		m.resizePanelsForLayout()

		if dashboardDebugEnabled(&m) {
			contentHeight := contentHeightFor(m.height)
			log.Printf("[dashboard] resize width=%d height=%d contentHeight=%d tier=%s",
				m.width, m.height, contentHeight, tierLabel(m.tier))
			log.Printf("[dashboard] panels %s %s %s %s %s %s %s",
				logPanelSize("beads", m.beadsPanel),
				logPanelSize("alerts", m.alertsPanel),
				logPanelSize("metrics", m.metricsPanel),
				logPanelSize("history", m.historyPanel),
				logPanelSize("files", m.filesPanel),
				logPanelSize("cass", m.cassPanel),
				logPanelSize("spawn", m.spawnPanel),
			)
		}

		if prevTier != m.tier {
			log.Printf("[dashboard] tier transition %s -> %s (width=%d height=%d)",
				tierLabel(prevTier), tierLabel(m.tier), m.width, m.height)
		}

		return m, tea.Batch(cmds...)

	case DashboardTickMsg:
		m.animTick++

		// Update ticker panel with current data and animation tick
		m.updateTickerData()

		// Drive staggered refreshes on the animation ticker to avoid a single heavy burst.
		now := time.Now()
		if !m.refreshPaused {
			cmds = append(cmds, m.scheduleRefreshes(now)...)
		}

		cmds = append(cmds, m.tick())
		return m, tea.Batch(cmds...)

	case RefreshMsg:
		// Trigger a coordinated refresh across subsystems (coalesced to avoid pile-up).
		return m, tea.Batch(m.fullRefresh(false)...)

	case SessionDataWithOutputMsg:
		if !m.acceptUpdate(refreshSession, msg.Gen) {
			return m, nil
		}
		followUp := m.finishSessionFetch()
		m.sessionFetchLatency = msg.Duration

		if msg.Err != nil {
			// Ignore coalescing cancellations; we’ll immediately re-fetch if pending.
			if !errors.Is(msg.Err, context.Canceled) {
				m.err = msg.Err
			}
			return m, followUp
		}
		m.err = nil
		m.lastRefresh = time.Now()
		m.markUpdated(refreshSession, time.Now())

		{
			prevSelectedID := ""
			if m.cursor >= 0 && m.cursor < len(m.panes) {
				prevSelectedID = m.panes[m.cursor].ID
			}

			// Build old pane ID to index lookup BEFORE updating panes
			// This is used to migrate paneStatus entries to new indices
			oldPaneIDToIdx := make(map[string]int, len(m.panes))
			for _, p := range m.panes {
				oldPaneIDToIdx[p.ID] = p.Index
			}

			sort.Slice(msg.Panes, func(i, j int) bool {
				return msg.Panes[i].Index < msg.Panes[j].Index
			})

			m.panes = msg.Panes

			// Migrate paneStatus entries from old indices to new indices by pane ID
			// This prevents stale data when pane indices change (add/remove/reorder)
			newPaneIDToIdx := make(map[string]int, len(m.panes))
			for _, p := range m.panes {
				newPaneIDToIdx[p.ID] = p.Index
			}
			newPaneStatus := make(map[int]PaneStatus, len(m.panes))
			for paneID, newIdx := range newPaneIDToIdx {
				if oldIdx, exists := oldPaneIDToIdx[paneID]; exists {
					// Pane still exists - migrate its status to new index
					if ps, ok := m.paneStatus[oldIdx]; ok {
						newPaneStatus[newIdx] = ps
					}
				}
			}
			m.paneStatus = newPaneStatus

			m.updateStats()
			m.paneOutputCaptureCursor = msg.NextCaptureCursor

			if prevSelectedID != "" {
				for i := range m.panes {
					if m.panes[i].ID == prevSelectedID {
						m.cursor = i
						break
					}
				}
			}
			if len(m.panes) > 0 && m.cursor >= len(m.panes) {
				m.cursor = len(m.panes) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}

			if m.paneOutputCache == nil {
				m.paneOutputCache = make(map[string]string)
			}
			if m.paneOutputLastCaptured == nil {
				m.paneOutputLastCaptured = make(map[string]time.Time)
			}
			if m.renderedOutputCache == nil {
				m.renderedOutputCache = make(map[string]string)
			}

			// Cleanup caches for stale panes
			validPaneIDs := make(map[string]bool, len(m.panes))
			for _, p := range m.panes {
				validPaneIDs[p.ID] = true
			}
			for id := range m.paneOutputCache {
				if !validPaneIDs[id] {
					delete(m.paneOutputCache, id)
				}
			}
			for id := range m.paneOutputLastCaptured {
				if !validPaneIDs[id] {
					delete(m.paneOutputLastCaptured, id)
				}
			}
			for id := range m.renderedOutputCache {
				if !validPaneIDs[id] {
					delete(m.renderedOutputCache, id)
				}
			}

			// Process compaction checks, context tracking, AND live status updates
			for _, data := range msg.Outputs {
				if data.PaneID != "" {
					m.paneOutputCache[data.PaneID] = data.Output
					if !data.LastActivity.IsZero() {
						m.paneOutputLastCaptured[data.PaneID] = data.LastActivity
					}
				}

				// Find the pane to get the variant
				var currentPane tmux.Pane
				found := false
				for _, p := range m.panes {
					if p.ID == data.PaneID {
						currentPane = p
						found = true
						break
					}
				}

				// Map type string to model name for context limits
				statusAgentType := data.AgentType
				modelName := ""
				switch data.AgentType {
				case string(tmux.AgentClaude):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultClaude
					} else {
						modelName = "claude-sonnet-4-20250514"
					}
				case string(tmux.AgentCodex):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultCodex
					} else {
						modelName = "gpt-4"
					}
				case string(tmux.AgentGemini):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultGemini
					} else {
						modelName = "gemini-2.0-flash"
					}
				}

				// Use variant if available
				if found && currentPane.Variant != "" {
					modelName = currentPane.Variant
				}

				// Get or create pane status
				ps := m.paneStatus[data.PaneIndex]

				// Update LIVE STATUS using local analysis (avoid waiting for slow full fetch)
				st := m.detector.Analyze(data.PaneID, currentPane.Title, statusAgentType, data.Output, data.LastActivity)

				state := string(st.State)
				// Rate limit check
				if st.State == status.StateError && st.ErrorType == status.ErrorRateLimit {
					state = "rate_limited"
				} else if ps.LastCompaction != nil && state != string(status.StateError) {
					state = "compacted"
				}
				ps.State = state
				ps.TokenVelocity = tokenVelocityFromStatus(st)
				m.agentStatuses[st.PaneID] = st

				// Calculate context usage
				if data.Output != "" && modelName != "" {
					contextInfo := tokens.GetUsageInfo(data.Output, modelName)
					ps.ContextTokens = contextInfo.EstimatedTokens
					ps.ContextLimit = contextInfo.ContextLimit
					ps.ContextPercent = contextInfo.UsagePercent
					ps.ContextModel = modelName
				}

				// Compaction check
				event, recoverySent, _ := m.compaction.CheckAndRecover(data.Output, statusAgentType, m.session, data.PaneIndex)

				if event != nil {
					now := time.Now()
					ps.LastCompaction = &now
					ps.RecoverySent = recoverySent
					ps.State = "compacted"
				}

				m.paneStatus[data.PaneIndex] = ps
			}
		}
		return m, followUp

	case StatusUpdateMsg:
		if !m.acceptUpdate(refreshStatus, msg.Gen) {
			return m, nil
		}
		followUp := m.finishStatusesFetch()
		m.statusFetchLatency = msg.Duration
		m.statusFetchErr = msg.Err
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

			// Pre-calculate token velocity
			ps.TokenVelocity = tokenVelocityFromStatus(st)
			m.paneStatus[idx] = ps
			m.agentStatuses[st.PaneID] = st

			// Cache expensive markdown rendering
			if st.LastOutput != "" && m.renderer != nil {
				rendered, err := m.renderer.Render(st.LastOutput)
				if err == nil {
					if m.renderedOutputCache == nil {
						m.renderedOutputCache = make(map[string]string)
					}
					m.renderedOutputCache[st.PaneID] = rendered
				}
			}
		}
		if msg.Err == nil {
			m.markUpdated(refreshStatus, msg.Time)
		}
		m.lastRefresh = msg.Time
		// Also refresh health data after status update
		return m, tea.Batch(followUp, m.fetchHealthCmd())

	case HealthUpdateMsg:
		if msg.Err == nil && msg.Health != nil {
			// Build pane ID to index lookup
			paneIndexByID := make(map[string]int)
			for _, p := range m.panes {
				paneIndexByID[p.ID] = p.Index
			}

			// Update pane status with health info
			for paneID, healthInfo := range msg.Health {
				idx, ok := paneIndexByID[paneID]
				if !ok {
					continue
				}

				ps := m.paneStatus[idx]
				ps.HealthStatus = healthInfo.Status
				ps.HealthIssues = healthInfo.Issues
				ps.RestartCount = healthInfo.RestartCount
				ps.UptimeSeconds = healthInfo.Uptime
				m.paneStatus[idx] = ps
			}
		}
		return m, nil

	case ConfigReloadMsg:
		if msg.Config != nil {
			m.cfg = msg.Config
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
		if !m.acceptUpdate(refreshScan, msg.Gen) {
			return m, nil
		}
		followUp := m.finishScanFetch()
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, followUp
		}

		// Only update badge state if the scan actually produced a new result.
		if msg.Status != "" {
			m.scanStatus = msg.Status
			m.scanTotals = msg.Totals
			m.scanDuration = msg.Duration
			m.markUpdated(refreshScan, time.Now())
		}
		return m, followUp

	case AgentMailUpdateMsg:
		if !m.acceptUpdate(refreshAgentMail, msg.Gen) {
			return m, nil
		}
		m.agentMailAvailable = msg.Available
		m.agentMailConnected = msg.Connected
		m.agentMailArchiveFound = msg.ArchiveFound
		m.agentMailLocks = msg.Locks
		m.agentMailLockInfo = msg.LockInfo
		m.markUpdated(refreshAgentMail, time.Now())
		return m, nil

	case AgentMailInboxSummaryMsg:
		if !m.acceptUpdate(refreshAgentMailInbox, msg.Gen) {
			return m, nil
		}
		m.fetchingMailInbox = false
		m.lastMailInboxFetch = time.Now()

		if msg.Err == nil {
			m.agentMailInbox = msg.Inboxes
			m.agentMailAgents = msg.AgentMap
			// Calculate totals from summary
			unread := 0
			urgent := 0
			for _, msgs := range msg.Inboxes {
				unread += len(msgs)
				for _, mm := range msgs {
					if strings.EqualFold(mm.Importance, "urgent") {
						urgent++
					}
				}
			}
			m.agentMailUnread = unread
			m.agentMailUrgent = urgent
			m.markUpdated(refreshAgentMailInbox, time.Now())
		}
		return m, nil

	case AgentMailInboxDetailMsg:
		if !m.acceptUpdate(refreshAgentMailInbox, msg.Gen) {
			return m, nil
		}
		if msg.Err != nil {
			m.agentMailInboxErrors[msg.PaneID] = msg.Err
		} else {
			delete(m.agentMailInboxErrors, msg.PaneID)
			m.agentMailInbox[msg.PaneID] = msg.Messages
		}
		return m, nil

	case CheckpointUpdateMsg:
		if !m.acceptUpdate(refreshCheckpoint, msg.Gen) {
			return m, nil
		}
		m.fetchingCheckpoint = false
		m.lastCheckpointFetch = time.Now()
		if msg.Err != nil {
			m.checkpointError = msg.Err
			// Clear stale data on error
			m.checkpointCount = 0
			m.checkpointStatus = "none"
			m.latestCheckpoint = nil
		} else {
			m.checkpointCount = msg.Count
			m.latestCheckpoint = msg.Latest
			m.checkpointStatus = msg.Status
			m.checkpointError = nil
			m.markUpdated(refreshCheckpoint, time.Now())
		}
		return m, nil

	case HandoffUpdateMsg:
		if !m.acceptUpdate(refreshHandoff, msg.Gen) {
			return m, nil
		}
		m.fetchingHandoff = false
		m.lastHandoffFetch = time.Now()
		m.handoffError = msg.Err
		m.handoffGoal = msg.Goal
		m.handoffNow = msg.Now
		m.handoffAge = msg.Age
		m.handoffPath = msg.Path
		m.handoffStatus = msg.Status
		if msg.Err == nil {
			m.markUpdated(refreshHandoff, time.Now())
		}
		return m, nil

	case CheckpointCreatedMsg:
		if msg.Err != nil {
			m.checkpointError = msg.Err
		} else {
			// Refresh checkpoint status after creation
			m.latestCheckpoint = msg.Checkpoint
			m.checkpointCount++
			m.checkpointStatus = "recent"
			m.checkpointError = nil
		}
		return m, nil

	case tea.KeyMsg:
		// Handle help overlay: Esc or ? closes it
		if m.showHelp {
			if msg.String() == "esc" || msg.String() == "?" {
				m.showHelp = false
			}
			return m, nil
		}

		if m.showCassSearch {
			if msg.String() == "esc" {
				m.showCassSearch = false
			}
			return m, tea.Batch(cmds...)
		}

		switch {
		case key.Matches(msg, dashKeys.NextPanel):
			m.cycleFocus(1)
			return m, nil

		case key.Matches(msg, dashKeys.PrevPanel):
			m.cycleFocus(-1)
			return m, nil
		case key.Matches(msg, dashKeys.CassSearch):
			m.showCassSearch = true
			searchW := int(float64(m.width) * 0.6)
			searchH := int(float64(m.height) * 0.6)
			m.cassSearch.SetSize(searchW, searchH)
			cmds = append(cmds, m.cassSearch.Init())
			return m, tea.Batch(cmds...)

		case key.Matches(msg, dashKeys.Help):
			m.showHelp = !m.showHelp
			return m, nil
		case key.Matches(msg, dashKeys.Diagnostics):
			m.showDiagnostics = !m.showDiagnostics
			return m, nil
		case key.Matches(msg, dashKeys.ScanToggle):
			m.scanDisabled = !m.scanDisabled
			return m, nil

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
			// Manual refresh (coalesced; cancels in-flight where supported)
			return m, tea.Batch(m.fullRefresh(true)...)

		case key.Matches(msg, dashKeys.ContextRefresh):
			// Force context refresh (same as regular refresh but with user intent to see context)
			return m, tea.Batch(m.fullRefresh(true)...)

		case key.Matches(msg, dashKeys.MailRefresh):
			// Refresh Agent Mail data
			return m, m.fetchAgentMailStatus()

		case key.Matches(msg, dashKeys.Checkpoint):
			// Create a new checkpoint for the session
			return m, m.createCheckpointCmd()

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

	// Forward keyboard events to focused panels for panel-specific actions
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// Only forward if not handled by global shortcuts above
		switch m.focusedPanel {
		case PanelHistory:
			if m.historyPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.historyPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelSidebar:
			// For sidebar, forward to the focused sub-panel
			if m.historyPanel != nil && m.historyPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.historyPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.filesPanel != nil && m.filesPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.filesPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.cassPanel != nil && m.cassPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.cassPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.metricsPanel != nil && m.metricsPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.metricsPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelBeads:
			if m.beadsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.beadsPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelAlerts:
			if m.alertsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.alertsPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelConflicts:
			if m.conflictsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.conflictsPanel.Update(keyMsg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	}

	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m *Model) selectByNumber(n int) {
	idx := n - 1
	if idx >= 0 && idx < len(m.panes) {
		m.cursor = idx
	}
}

func (m *Model) cycleFocus(dir int) {
	var visiblePanes []PanelID
	switch {
	case m.tier >= layout.TierMega:
		// Use PanelConflicts instead of PanelAlerts when conflicts are present
		if m.conflictsPanel.HasConflicts() {
			visiblePanes = []PanelID{PanelPaneList, PanelDetail, PanelBeads, PanelConflicts, PanelSidebar}
		} else {
			visiblePanes = []PanelID{PanelPaneList, PanelDetail, PanelBeads, PanelAlerts, PanelSidebar}
		}
	case m.tier >= layout.TierUltra:
		visiblePanes = []PanelID{PanelPaneList, PanelDetail, PanelSidebar}
	case m.tier >= layout.TierSplit:
		visiblePanes = []PanelID{PanelPaneList, PanelDetail}
	default:
		visiblePanes = []PanelID{PanelPaneList}
	}

	// Find current index in visiblePanes
	currIdx := -1
	for i, p := range visiblePanes {
		if p == m.focusedPanel {
			currIdx = i
			break
		}
	}

	// If not found (e.g. resized from Mega to Split while focus was on Beads), default to 0
	if currIdx == -1 {
		currIdx = 0
	}

	// Cycle
	nextIdx := (currIdx + dir + len(visiblePanes)) % len(visiblePanes)
	m.focusedPanel = visiblePanes[nextIdx]
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

// updateTickerData updates the ticker panel with current dashboard data
func (m *Model) updateTickerData() {
	// Count active agents (those with any known status, not just "working")
	// "Active" means status has been determined - could be working, idle, error, or compacted
	// This gives a more accurate picture than only counting actively working agents
	activeAgents := 0
	for _, ps := range m.paneStatus {
		// Count as active if state is known (non-empty)
		// Empty or missing state means status detection hasn't run yet
		if ps.State != "" {
			activeAgents++
		}
	}
	// Fallback: if no panes have determined status yet but we have panes, count agent panes
	// (excludes user panes which are type "user" or empty)
	// Note: We check activeAgents==0 rather than len(paneStatus)==0 because paneStatus
	// may have entries with empty State when status detection is still pending
	if activeAgents == 0 && len(m.panes) > 0 {
		// Status detection hasn't populated yet; show total agents as placeholder
		// This prevents showing "0/17" when we simply haven't fetched status yet
		activeAgents = m.claudeCount + m.codexCount + m.geminiCount
	}

	// Count alerts by severity
	var critAlerts, warnAlerts, infoAlerts int
	for _, a := range m.activeAlerts {
		switch a.Severity {
		case alerts.SeverityCritical:
			critAlerts++
		case alerts.SeverityWarning:
			warnAlerts++
		default:
			infoAlerts++
		}
	}

	// Build ticker data from dashboard state
	data := panels.TickerData{
		TotalAgents:      len(m.panes),
		ActiveAgents:     activeAgents,
		ClaudeCount:      m.claudeCount,
		CodexCount:       m.codexCount,
		GeminiCount:      m.geminiCount,
		UserCount:        m.userCount,
		CriticalAlerts:   critAlerts,
		WarningAlerts:    warnAlerts,
		InfoAlerts:       infoAlerts,
		ReadyBeads:       m.beadsSummary.Ready,
		InProgressBeads:  m.beadsSummary.InProgress,
		BlockedBeads:     m.beadsSummary.Blocked,
		UnreadMessages:   m.agentMailUnread,
		ActiveLocks:      m.agentMailLocks,
		MailConnected:    m.agentMailConnected,
		MailAvailable:    m.agentMailAvailable,
		MailArchiveFound: m.agentMailArchiveFound,
		CheckpointCount:  m.checkpointCount,
		CheckpointStatus: m.checkpointStatus,
		BugsCritical:     m.bugsCritical,
		BugsWarning:      m.bugsWarning,
		BugsInfo:         m.bugsInfo,
		BugsScanned:      m.bugsScanned,
	}

	m.tickerPanel.SetData(data)
	m.tickerPanel.SetAnimTick(m.animTick)
}

// View implements tea.Model
func (m Model) View() string {
	if m.showHelp {
		helpOverlay := components.HelpOverlay(components.HelpOverlayOptions{
			Title:    "Dashboard Shortcuts",
			Sections: components.DashboardHelpSections(),
			MaxWidth: 60,
		})
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, helpOverlay)
	}

	if m.showCassSearch {
		searchView := m.cassSearch.View()
		modalStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Primary).
			Background(m.theme.Base).
			Padding(1, 2)
		modal := modalStyle.Render(searchView)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	header := m.renderHeaderSection()
	footer := m.renderFooterSection()
	content := m.renderMainContentSection()

	if m.height > 0 {
		available := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
		if available < 1 {
			available = 1
		}
		// Truncate content to fit within available height.
		// lipgloss Height/MaxHeight don't truncate - they're CSS-like properties.
		content = truncateToHeight(content, available)
		// Apply height style to ensure consistent spacing
		content = lipgloss.NewStyle().Height(available).MaxHeight(available).Render(content)
	}

	return header + content + footer
}

func (m Model) renderHeaderSection() string {
	t := m.theme

	var b strings.Builder
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════
	// HEADER with animated banner (centered)
	// ═══════════════════════════════════════════════════════════════
	bannerText := components.RenderBannerMedium(true, m.animTick)
	center := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)
	b.WriteString(center.Render(bannerText) + "\n")

	// Session title with gradient
	sessionTitle := m.icons.Session + "  " + m.session
	animatedSession := styles.Shimmer(sessionTitle, m.animTick,
		string(t.Blue), string(t.Lavender), string(t.Mauve))
	b.WriteString(center.Render(animatedSession) + "\n")
	if contextLine := m.renderHeaderContextLine(m.width); contextLine != "" {
		b.WriteString(center.Render(contextLine) + "\n")
	}
	if handoffLine := m.renderHeaderHandoffLine(m.width); handoffLine != "" {
		b.WriteString(center.Render(handoffLine) + "\n")
	}
	if contextWarnLine := m.renderHeaderContextWarningLine(m.width); contextWarnLine != "" {
		b.WriteString(center.Render(contextWarnLine) + "\n")
	}
	b.WriteString(styles.GradientDivider(m.width,
		string(t.Blue), string(t.Mauve)) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// STATS BAR with agent counts
	// ═══════════════════════════════════════════════════════════════
	statsBar := m.renderStatsBar()
	b.WriteString(center.Render(statsBar) + "\n\n")

	if m.showDiagnostics {
		diagWidth := m.width - 4
		if diagWidth < 20 {
			diagWidth = 20
		}
		b.WriteString(m.renderDiagnosticsBar(diagWidth) + "\n\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// RATE LIMIT ALERT (if any agent is rate limited)
	// ═══════════════════════════════════════════════════════════════
	if alert := m.renderRateLimitAlert(); alert != "" {
		b.WriteString(alert + "\n\n")
	}

	return b.String()
}

func (m Model) renderMainContentSection() string {
	var b strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// PANE GRID VISUALIZATION
	// ═══════════════════════════════════════════════════════════════
	stateWidth := m.width - 4
	if stateWidth < 20 {
		stateWidth = 20
	}

	if len(m.panes) == 0 {
		if m.err != nil {
			b.WriteString(components.ErrorState(m.err.Error(), hintForSessionFetchError(m.err), stateWidth) + "\n")
		} else if m.fetchingSession {
			message := "Fetching panes…"
			if !m.lastPaneFetch.IsZero() {
				elapsed := time.Since(m.lastPaneFetch).Round(100 * time.Millisecond)
				if elapsed > 0 {
					message = fmt.Sprintf("Fetching panes… (%s)", elapsed)
				}
			}
			b.WriteString(components.LoadingState(message, stateWidth) + "\n")
		} else {
			b.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
				Icon:        components.IconEmpty,
				Title:       "No panes found",
				Description: "Session has no active panes",
				Width:       stateWidth,
				Centered:    true,
			}) + "\n")
		}
	} else {
		if m.err != nil {
			b.WriteString(components.ErrorState(m.err.Error(), hintForSessionFetchError(m.err), stateWidth) + "\n\n")
		}
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

	return b.String()
}

func (m Model) renderFooterSection() string {
	t := m.theme

	var b strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// TICKER BAR (scrolling status summary)
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")
	m.tickerPanel.SetSize(m.width-4, 1)
	b.WriteString("  " + m.tickerPanel.View() + "\n")

	// ═══════════════════════════════════════════════════════════════
	// QUICK ACTIONS BAR (width-gated, only in wide+ modes)
	// ═══════════════════════════════════════════════════════════════
	if quickActions := m.renderQuickActions(); quickActions != "" {
		b.WriteString("  " + quickActions + "\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR
	// ═══════════════════════════════════════════════════════════════
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

	// Scan status badge
	scanBadge := m.renderScanBadge()
	if scanBadge != "" {
		parts = append(parts, scanBadge)
	}

	// Agent Mail status badge
	mailBadge := m.renderAgentMailBadge()
	if mailBadge != "" {
		parts = append(parts, mailBadge)
	}

	// Checkpoint status badge
	cpBadge := m.renderCheckpointBadge()
	if cpBadge != "" {
		parts = append(parts, cpBadge)
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

// renderCheckpointBadge renders the checkpoint status badge
func (m Model) renderCheckpointBadge() string {
	t := m.theme

	// Don't show badge if no checkpoints or status not set
	if m.checkpointCount == 0 || m.checkpointStatus == "" || m.checkpointStatus == "none" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string

	switch m.checkpointStatus {
	case "recent":
		bgColor = t.Green
		fgColor = t.Base
		icon = "💾"
		label = fmt.Sprintf("%d ckpt", m.checkpointCount)
	case "stale":
		bgColor = t.Yellow
		fgColor = t.Base
		icon = "💾"
		label = fmt.Sprintf("%d stale", m.checkpointCount)
	case "old":
		bgColor = t.Surface1
		fgColor = t.Overlay
		icon = "💾"
		label = fmt.Sprintf("%d old", m.checkpointCount)
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
// High context (>80%) uses shimmer effect on warning indicators
func (m Model) renderContextBar(percent float64, width int) string {
	t := m.theme

	// Calculate bar width (leave room for percentage text and warning icon)
	barWidth := width - 8 // "[████░░] XX%⚠"
	if barWidth < 5 {
		barWidth = 5
	}

	colors := []string{string(t.Green), string(t.Blue), string(t.Yellow), string(t.Red)}
	barContent := styles.ShimmerProgressBar(percent/100.0, barWidth, "█", "░", m.animTick, colors...)

	percentStyle := lipgloss.NewStyle().Foreground(t.Overlay)

	// Determine warning icon with shimmer effect for high context
	var warningIcon string
	switch {
	case percent >= 95:
		// Critical: shimmer the warning in red/maroon gradient
		warningIcon = " " + styles.Shimmer("!!!", m.animTick, string(t.Red), string(t.Maroon), string(t.Red))
	case percent >= 90:
		// High: shimmer in red/orange gradient
		warningIcon = " " + styles.Shimmer("!!", m.animTick, string(t.Red), string(t.Maroon), string(t.Red))
	case percent >= 80:
		// Warning: shimmer in yellow/orange gradient
		warningIcon = " " + styles.Shimmer("!", m.animTick, string(t.Yellow), string(t.Peach), string(t.Yellow))
	default:
		warningIcon = ""
	}

	bar := "[" + barContent + "]" +
		percentStyle.Render(fmt.Sprintf("%3.0f%%", percent)) +
		warningIcon

	return bar
}

// formatTokenDisplay formats token counts for display (e.g., "142.5K / 200K")
func formatTokenDisplay(used, limit int) string {
	formatTokens := func(n int) string {
		if n >= 1000000 {
			return fmt.Sprintf("%.1fM", float64(n)/1000000)
		}
		if n >= 1000 {
			return fmt.Sprintf("%.1fK", float64(n)/1000)
		}
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%s / %s", formatTokens(used), formatTokens(limit))
}

// formatDuration formats a duration for display (e.g., "2m", "45s")
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
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

	// In grid mode (used below Split threshold), show more detail when card width allows it.
	showExtendedInfo := cardWidth >= 24

	rows := BuildPaneTableRows(m.panes, m.agentStatuses, m.paneStatus, &m.beadsSummary, m.fileChanges, m.animTick, t)
	if summary := activitySummaryLine(rows, t); summary != "" {
		lines = append(lines, "  "+summary)
	}
	contextRanks := m.computeContextRanks()

	var cards []string

	for i, p := range m.panes {
		row := rows[i]
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

		// Pulse border for active/working panes (if not selected)
		if !isSelected && row.Status == "working" && m.animTick > 0 {
			borderColor = styles.Pulse(string(borderColor), m.animTick)
		}

		// Build card content
		var cardContent strings.Builder

		// Header line with icon and title
		statusIcon := "•"
		statusColor := t.Overlay
		switch row.Status {
		case "working":
			statusIcon = WorkingSpinnerFrame(m.animTick)
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
		case "rate_limited":
			statusIcon = "⏳"
			statusColor = t.Maroon
		}
		statusStyled := lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusIcon)

		iconStyled := lipgloss.NewStyle().Foreground(iconColor).Bold(true).Render(agentIcon)
		// Show profile name as primary identifier
		profileName := p.Type.ProfileName()
		profileStyled := lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(profileName)
		cardContent.WriteString(statusStyled + " " + iconStyled + " " + profileStyled + "\n")

		// Index badge + compact badges
		numBadge := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Render(fmt.Sprintf("#%d", p.Index))

		var line2Parts []string
		line2Parts = append(line2Parts, numBadge)
		if p.Variant != "" {
			label := layout.TruncateWidthDefault(p.Variant, 12)
			modelBadge := styles.TextBadge(label, iconColor, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			})
			line2Parts = append(line2Parts, modelBadge)
		}
		if showExtendedInfo {
			if rank, ok := contextRanks[p.Index]; ok && rank > 0 {
				rankBadge := styles.TextBadge(fmt.Sprintf("rank%d", rank), t.Mauve, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     false,
					ShowIcon: false,
				})
				line2Parts = append(line2Parts, rankBadge)
			}
		}
		cardContent.WriteString(strings.Join(line2Parts, " ") + "\n")

		// Bead + activity badges (best-effort)
		if row.CurrentBead != "" {
			beadID := row.CurrentBead
			if parts := strings.SplitN(row.CurrentBead, ": ", 2); len(parts) > 0 {
				beadID = parts[0]
			}
			beadBadge := styles.TextBadge(beadID, t.Mauve, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			})
			cardContent.WriteString(beadBadge + "\n")
		}

		var activityBadges []string
		if badge := activityBadge(row.Status, t); badge != "" {
			activityBadges = append(activityBadges, badge)
		}
		if row.FileChanges > 0 {
			activityBadges = append(activityBadges, styles.TextBadge(fmt.Sprintf("Δ%d", row.FileChanges), t.Blue, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			}))
		}
		if row.TokenVelocity > 0 && showExtendedInfo {
			activityBadges = append(activityBadges, styles.TokenVelocityBadge(row.TokenVelocity, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: true,
			}))
		}
		if len(activityBadges) > 0 {
			cardContent.WriteString(strings.Join(activityBadges, " ") + "\n")
		}

		// Health badges - show warning/error status and restart count
		if ps, ok := m.paneStatus[p.Index]; ok {
			var healthBadges []string

			// Health status badge
			if ps.HealthStatus == "warning" {
				healthBadges = append(healthBadges, styles.TextBadge("⚠ WARN", t.Yellow, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     true,
					ShowIcon: false,
				}))
			} else if ps.HealthStatus == "error" {
				healthBadges = append(healthBadges, styles.TextBadge("✗ ERR", t.Red, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     true,
					ShowIcon: false,
				}))
			}

			// Restart count badge
			if ps.RestartCount > 0 {
				healthBadges = append(healthBadges, styles.TextBadge(fmt.Sprintf("↻%d", ps.RestartCount), t.Peach, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     false,
					ShowIcon: false,
				}))
			}

			// Show first health issue as tooltip
			if len(ps.HealthIssues) > 0 && showExtendedInfo {
				issueStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
				issue := layout.TruncateWidthDefault(ps.HealthIssues[0], maxInt(cardWidth-4, 10))
				healthBadges = append(healthBadges, issueStyle.Render(issue))
			}

			if len(healthBadges) > 0 {
				cardContent.WriteString(strings.Join(healthBadges, " ") + "\n")
			}
		}

		// Size info - on wide displays show more detail
		sizeStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		if showExtendedInfo {
			cardContent.WriteString(sizeStyle.Render(fmt.Sprintf("%dx%d cols×rows", p.Width, p.Height)) + "\n")
		} else {
			cardContent.WriteString(sizeStyle.Render(fmt.Sprintf("%dx%d", p.Width, p.Height)) + "\n")
		}

		// Command running (if any) - only when there is room
		if p.Command != "" && showExtendedInfo {
			cmdStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
			cmd := layout.TruncateWidthDefault(p.Command, maxInt(cardWidth-4, 8))
			cardContent.WriteString(cmdStyle.Render(cmd))
		}

		// Context usage bar (best-effort; show in grid when available)
		if ps, ok := m.paneStatus[p.Index]; ok && ps.ContextLimit > 0 {
			cardContent.WriteString("\n")
			// Show token counts in extended view (e.g., "142.5K / 200K")
			if showExtendedInfo && ps.ContextTokens > 0 {
				tokenInfo := formatTokenDisplay(ps.ContextTokens, ps.ContextLimit)
				tokenStyle := lipgloss.NewStyle().Foreground(t.Subtext)
				cardContent.WriteString(tokenStyle.Render(tokenInfo) + "\n")
			}
			contextBar := m.renderContextBar(ps.ContextPercent, cardWidth-4)
			cardContent.WriteString(contextBar)
		}

		// Rotation in-progress indicator
		if ps, ok := m.paneStatus[p.Index]; ok && ps.IsRotating {
			cardContent.WriteString("\n")
			rotateIcon := styles.Shimmer("🔄", m.animTick, string(t.Blue), string(t.Sapphire), string(t.Blue))
			rotateStyle := lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
			cardContent.WriteString(rotateIcon + rotateStyle.Render(" Rotating..."))
		} else if ps, ok := m.paneStatus[p.Index]; ok && ps.RotatedAt != nil {
			// Show "rotated Xm ago" indicator for recently rotated agents
			elapsed := time.Since(*ps.RotatedAt)
			if elapsed < 5*time.Minute {
				cardContent.WriteString("\n")
				rotatedStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
				cardContent.WriteString(rotatedStyle.Render(fmt.Sprintf("↻ rotated %s ago", formatDuration(elapsed))))
			}
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

// renderQuickActions renders actionable buttons for common operations.
// Only shows in TierWide and above to avoid cluttering narrow terminals.
func (m Model) renderQuickActions() string {
	// Only show in wide modes (TierWide = 200+ cols)
	if m.tier < layout.TierWide {
		return ""
	}

	t := m.theme
	ic := m.icons

	// Action button style - more prominent than help bar keys
	buttonStyle := lipgloss.NewStyle().
		Background(t.Surface1).
		Foreground(t.Text).
		Bold(true).
		Padding(0, 2).
		MarginRight(1)

	// Disabled button style - dimmed text
	disabledButtonStyle := buttonStyle.
		Foreground(t.Surface2)

	// Subtle key hint
	keyHintStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Italic(true)

	type action struct {
		icon    string
		label   string
		key     string
		enabled bool
	}

	// Build actions based on current state
	hasSelection := m.cursor >= 0 && m.cursor < len(m.panes)

	actions := []action{
		{
			icon:    ic.Palette,
			label:   "Palette",
			key:     "F6",
			enabled: true,
		},
		{
			icon:    ic.Send,
			label:   "Send",
			key:     "s",
			enabled: hasSelection,
		},
		{
			icon:    ic.Copy,
			label:   "Copy",
			key:     "y",
			enabled: hasSelection,
		},
		{
			icon:    ic.Zoom,
			label:   "Zoom",
			key:     "z",
			enabled: hasSelection,
		},
	}

	var parts []string

	// Label for the section
	labelStyle := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Bold(true).
		MarginRight(2)
	parts = append(parts, labelStyle.Render("Actions"))

	for _, a := range actions {
		style := buttonStyle
		if !a.enabled {
			style = disabledButtonStyle
		}

		btn := style.Render(a.icon + " " + a.label)
		hint := keyHintStyle.Render(" " + a.key)
		parts = append(parts, btn+hint)
	}

	return strings.Join(parts, " ")
}

func (m Model) renderHelpBar() string {
	// Build hints: global navigation first
	hints := []components.KeyHint{
		{Key: "↑↓", Desc: "navigate"},
		{Key: "1-9", Desc: "select"},
		{Key: "z", Desc: "zoom"},
	}

	// Add panel-specific hints (max 3 to avoid overwhelming)
	panelHints := m.getFocusedPanelHints()
	for i, hint := range panelHints {
		if i >= 3 {
			break
		}
		hints = append(hints, hint)
	}

	// Always end with essential global hints
	hints = append(hints,
		components.KeyHint{Key: "r", Desc: "refresh"},
		components.KeyHint{Key: "?", Desc: "help"},
		components.KeyHint{Key: "q", Desc: "quit"},
	)

	// Use the reusable RenderHelpBar component with width-aware truncation.
	// Hints are progressively hidden from right-to-left when they don’t fit.
	return components.RenderHelpBar(components.HelpBarOptions{
		Hints: hints,
		Width: m.width - 4, // Account for margins
	})
}

// getFocusedPanelHints returns keybindings for the currently focused panel as KeyHints.
func (m Model) getFocusedPanelHints() []components.KeyHint {
	var keybindings []panels.Keybinding

	switch m.focusedPanel {
	case PanelBeads:
		if m.beadsPanel != nil {
			keybindings = m.beadsPanel.Keybindings()
		}
	case PanelAlerts:
		if m.alertsPanel != nil {
			keybindings = m.alertsPanel.Keybindings()
		}
	case PanelMetrics:
		if m.metricsPanel != nil {
			keybindings = m.metricsPanel.Keybindings()
		}
	case PanelHistory:
		if m.historyPanel != nil {
			keybindings = m.historyPanel.Keybindings()
		}
	case PanelSidebar:
		// Sidebar contains multiple sub-panels; could extend to show focused sub-panel
		if m.filesPanel != nil && m.filesPanel.IsFocused() {
			keybindings = m.filesPanel.Keybindings()
		} else if m.cassPanel != nil && m.cassPanel.IsFocused() {
			keybindings = m.cassPanel.Keybindings()
		} else if m.historyPanel != nil && m.historyPanel.IsFocused() {
			keybindings = m.historyPanel.Keybindings()
		} else if m.metricsPanel != nil && m.metricsPanel.IsFocused() {
			keybindings = m.metricsPanel.Keybindings()
		}
	}

	// Convert keybindings to KeyHints
	var hints []components.KeyHint
	for _, kb := range keybindings {
		// Skip navigation keys (j/k/up/down) - already shown globally
		action := kb.Action
		if action == "up" || action == "down" {
			continue
		}
		hints = append(hints, components.KeyHint{
			Key:  kb.Key.Help().Key,
			Desc: action,
		})
	}
	return hints
}

func (m Model) renderHeaderContextLine(width int) string {
	if width < 20 {
		return ""
	}

	t := m.theme

	var parts []string
	remote := strings.TrimSpace(tmux.DefaultClient.Remote)
	if remote == "" {
		parts = append(parts, "local")
	} else {
		parts = append(parts, "ssh "+remote)
	}

	if !m.lastRefresh.IsZero() {
		parts = append(parts, "refreshed "+formatRelativeTime(time.Since(m.lastRefresh)))
	} else if m.fetchingSession || m.fetchingContext {
		parts = append(parts, "refreshing…")
	}

	if m.refreshPaused {
		parts = append(parts, "paused")
	}
	if m.scanDisabled {
		parts = append(parts, "scan off")
	}

	line := strings.Join(parts, " · ")
	line = layout.TruncateWidthDefault(line, width-4)

	return lipgloss.NewStyle().
		Foreground(t.Subtext).
		Render(line)
}

func (m Model) renderHeaderHandoffLine(width int) string {
	if width < 20 {
		return ""
	}

	goal := strings.TrimSpace(m.handoffGoal)
	now := strings.TrimSpace(m.handoffNow)
	status := strings.TrimSpace(m.handoffStatus)

	if goal == "" && now == "" && status == "" {
		return ""
	}

	var parts []string
	if goal != "" {
		parts = append(parts, "goal: "+layout.TruncateWidthDefault(goal, 60))
	}
	if now != "" {
		parts = append(parts, "now: "+layout.TruncateWidthDefault(now, 40))
	}
	if m.handoffAge > 0 {
		parts = append(parts, formatRelativeTime(m.handoffAge))
	}
	if status != "" {
		parts = append(parts, status)
	}
	if len(parts) == 0 {
		return ""
	}

	line := "handoff · " + strings.Join(parts, " · ")
	line = layout.TruncateWidthDefault(line, width-4)

	return lipgloss.NewStyle().
		Foreground(m.theme.Subtext).
		Render(line)
}

func (m Model) renderHeaderContextWarningLine(width int) string {
	if width < 20 {
		return ""
	}

	type contextAlert struct {
		label   string
		percent float64
		model   string
	}

	paneByIndex := make(map[int]tmux.Pane, len(m.panes))
	for _, pane := range m.panes {
		paneByIndex[pane.Index] = pane
	}

	var alerts []contextAlert
	for idx, ps := range m.paneStatus {
		if ps.ContextLimit <= 0 || ps.ContextPercent < 70 {
			continue
		}
		pane, ok := paneByIndex[idx]
		if !ok {
			continue
		}
		model := ps.ContextModel
		if model == "" {
			model = pane.Variant
		}
		if model == "" {
			model = "unknown"
		}
		model = layout.TruncateWidthDefault(model, 24)
		label := formatPaneLabel(m.session, pane)
		label = layout.TruncateWidthDefault(label, 12)
		alerts = append(alerts, contextAlert{
			label:   label,
			percent: ps.ContextPercent,
			model:   model,
		})
	}

	if len(alerts) == 0 {
		return ""
	}

	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].percent > alerts[j].percent
	})

	t := m.theme
	prefix := "context"
	if m.icons.Warning != "" {
		prefix = m.icons.Warning + " " + prefix
	}

	warnStyle := lipgloss.NewStyle().Foreground(t.Warning)
	criticalStyle := lipgloss.NewStyle().Foreground(t.Error).Bold(true)

	sep := " · "
	sepWidth := lipgloss.Width(sep)
	maxWidth := width - 4

	prefixText := prefix + ":"
	rendered := []string{warnStyle.Render(prefixText)}
	currentWidth := lipgloss.Width(prefixText)

	for _, alert := range alerts {
		segmentText := fmt.Sprintf("%s %.0f%% of %s context", alert.label, alert.percent, alert.model)
		segmentWidth := lipgloss.Width(segmentText)
		if currentWidth+sepWidth+segmentWidth > maxWidth {
			break
		}

		style := warnStyle
		if alert.percent >= 85 {
			style = criticalStyle
		}
		rendered = append(rendered, style.Render(segmentText))
		currentWidth += sepWidth + segmentWidth
	}

	if len(rendered) == 1 {
		return ""
	}

	return strings.Join(rendered, sep)
}

func formatPaneLabel(session string, pane tmux.Pane) string {
	label := strings.TrimSpace(pane.Title)
	prefix := session + "__"
	if strings.HasPrefix(label, prefix) {
		label = strings.TrimPrefix(label, prefix)
	}
	if label == "" {
		label = fmt.Sprintf("pane %d", pane.Index)
	}
	return label
}

func formatRelativeTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 5*time.Second {
		return "just now"
	}

	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}

	hours := int(d.Hours())
	if hours < 48 {
		return fmt.Sprintf("%dh ago", hours)
	}

	days := hours / 24
	return fmt.Sprintf("%dd ago", days)
}

func formatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func (m Model) renderDiagnosticsBar(width int) string {
	t := m.theme

	labelStyle := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text)
	warnStyle := lipgloss.NewStyle().Foreground(t.Warning)
	errStyle := lipgloss.NewStyle().Foreground(t.Error)

	sessionPart := valueStyle.Render("ok")
	if m.fetchingSession {
		elapsed := time.Since(m.lastPaneFetch).Round(100 * time.Millisecond)
		sessionPart = warnStyle.Render("fetching " + elapsed.String())
	} else if m.sessionFetchLatency > 0 {
		sessionPart = valueStyle.Render(m.sessionFetchLatency.Round(time.Millisecond).String())
	}
	if m.err != nil {
		sessionPart = errStyle.Render("error")
	}

	statusPart := valueStyle.Render("ok")
	if m.fetchingContext {
		elapsed := time.Since(m.lastContextFetch).Round(100 * time.Millisecond)
		statusPart = warnStyle.Render("fetching " + elapsed.String())
	} else if m.statusFetchLatency > 0 {
		statusPart = valueStyle.Render(m.statusFetchLatency.Round(time.Millisecond).String())
	}
	if m.statusFetchErr != nil {
		statusPart = errStyle.Render("error")
	}

	parts := []string{
		labelStyle.Render("diag"),
		labelStyle.Render("tmux") + ":" + sessionPart,
		labelStyle.Render("status") + ":" + statusPart,
	}
	if width >= 120 {
		age := func(src refreshSource) string {
			t := m.lastUpdated[src]
			if t.IsZero() {
				return "n/a"
			}
			return formatAgeShort(time.Since(t))
		}
		agePart := valueStyle.Render(fmt.Sprintf("panes %s, status %s, beads %s", age(refreshSession), age(refreshStatus), age(refreshBeads)))
		parts = append(parts, labelStyle.Render("age")+":"+agePart)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Padding(0, 1).
		Width(width)

	return box.Render(strings.Join(parts, "  "))
}

func hintForSessionFetchError(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "tmux is responding slowly. Press r to retry, p to pause auto-refresh, or try running ntm outside of tmux"
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "tmux is not installed"):
		return "Install tmux, then run: ntm deps -v"
	case strings.Contains(msg, "executable file not found"):
		return "Install tmux, then run: ntm deps -v"
	case strings.Contains(msg, "no server running"):
		return "Start tmux or create a session with: ntm spawn <name>"
	case strings.Contains(msg, "failed to connect to server"):
		return "Start tmux or create a session with: ntm spawn <name>"
	case strings.Contains(msg, "can't find session"), strings.Contains(msg, "session not found"):
		return "Session may have ended. Create a new one with: ntm spawn <name>"
	}

	return "Press r to retry"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func contentHeightFor(total int) int {
	contentHeight := total - 14
	if contentHeight < 5 {
		contentHeight = 5
	}
	return contentHeight
}

// truncateToHeight truncates content to fit within maxLines.
// If the content has more lines than maxLines, it truncates and optionally
// shows a "more" indicator. This is needed because lipgloss's Height/MaxHeight
// don't actually truncate content - they're CSS-like properties for layout.
func truncateToHeight(content string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	// Truncate to maxLines
	return strings.Join(lines[:maxLines], "\n")
}

func dashboardDebugEnabled(m *Model) bool {
	if m != nil && m.showDiagnostics {
		return true
	}
	// Check NTM_TUI_DEBUG (preferred) or NTM_DASH_DEBUG (legacy alias)
	for _, envVar := range []string{"NTM_TUI_DEBUG", "NTM_DASH_DEBUG"} {
		value := strings.TrimSpace(os.Getenv(envVar))
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

type sizedPanel interface {
	Width() int
	Height() int
}

func logPanelSize(name string, panel sizedPanel) string {
	if panel == nil {
		return fmt.Sprintf("%s=0x0", name)
	}
	return fmt.Sprintf("%s=%dx%d", name, panel.Width(), panel.Height())
}

func tierLabel(tier layout.Tier) string {
	switch tier {
	case layout.TierNarrow:
		return "narrow"
	case layout.TierSplit:
		return "split"
	case layout.TierWide:
		return "wide"
	case layout.TierUltra:
		return "ultra"
	case layout.TierMega:
		return "mega"
	default:
		return fmt.Sprintf("tier-%d", int(tier))
	}
}

func (m *Model) resizeSidebarPanels(width, height int) {
	if m.spawnPanel != nil {
		m.spawnPanel.SetSize(width, height)
	}
	if m.metricsPanel != nil {
		m.metricsPanel.SetSize(width, height)
	}
	if m.historyPanel != nil {
		m.historyPanel.SetSize(width, height)
	}
	if m.filesPanel != nil {
		m.filesPanel.SetSize(width, height)
	}
	if m.cassPanel != nil {
		m.cassPanel.SetSize(width, height)
	}
}

func (m *Model) resizePanelsForLayout() {
	contentHeight := contentHeightFor(m.height)
	panelHeight := maxInt(contentHeight-2, 0)

	switch {
	case m.tier >= layout.TierMega:
		_, _, p3, p4, p5 := layout.MegaProportions(m.width)
		p3Inner := maxInt(p3-4, 0)
		p4Inner := maxInt(p4-4, 0)
		p5Inner := maxInt(p5-4, 0)

		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(p3Inner, panelHeight)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(p4Inner, panelHeight)
		}
		m.resizeSidebarPanels(p5Inner, panelHeight)

	case m.tier >= layout.TierUltra:
		_, _, rightWidth := layout.UltraProportions(m.width)
		rightWidth = maxInt(rightWidth-2, 0)
		sidebarWidth := maxInt(rightWidth-4, 0)
		m.resizeSidebarPanels(sidebarWidth, panelHeight)
		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(0, 0)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(0, 0)
		}

	default:
		m.resizeSidebarPanels(0, 0)
		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(0, 0)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(0, 0)
		}
	}

	if m.tickerPanel != nil {
		m.tickerPanel.SetSize(maxInt(m.width-4, 0), 1)
	}
}

func refreshDue(last time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return time.Since(last) >= interval
}

func (m *Model) scheduleRefreshes(now time.Time) []tea.Cmd {
	var cmds []tea.Cmd

	paneDue := refreshDue(m.lastPaneFetch, m.paneRefreshInterval)
	contextDue := refreshDue(m.lastContextFetch, m.contextRefreshInterval)
	coreDue := paneDue || contextDue

	if coreDue {
		if cmd := m.requestSessionFetch(false); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.requestStatusesFetch(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if contextDue && !m.fetchingContext {
		if !m.fetchingMetrics {
			m.fetchingMetrics = true
			cmds = append(cmds, m.fetchMetricsCmd())
		}
		if !m.fetchingRouting {
			m.fetchingRouting = true
			cmds = append(cmds, m.fetchRoutingCmd())
		}
		if !m.fetchingHistory {
			m.fetchingHistory = true
			cmds = append(cmds, m.fetchHistoryCmd())
		}
		if !m.fetchingFileChanges {
			m.fetchingFileChanges = true
			cmds = append(cmds, m.fetchFileChangesCmd())
		}
		// Refresh Agent Mail status along with context updates.
		cmds = append(cmds, m.fetchAgentMailStatus())
	}

	if !m.scanDisabled && refreshDue(m.lastScanFetch, m.scanRefreshInterval) && !m.fetchingScan {
		m.lastScanFetch = now
		if cmd := m.requestScanFetch(false); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if refreshDue(m.lastMailInboxFetch, m.mailInboxRefreshInterval) && !m.fetchingMailInbox {
		m.fetchingMailInbox = true
		m.lastMailInboxFetch = now
		cmds = append(cmds, m.fetchAgentMailInboxes())
	}

	if refreshDue(m.lastAlertsFetch, m.alertsRefreshInterval) && !m.fetchingAlerts {
		m.fetchingAlerts = true
		m.lastAlertsFetch = now
		cmds = append(cmds, m.fetchAlertsCmd())
	}

	if refreshDue(m.lastBeadsFetch, m.beadsRefreshInterval) && !m.fetchingBeads {
		m.fetchingBeads = true
		m.lastBeadsFetch = now
		cmds = append(cmds, m.fetchBeadsCmd())
	}

	if refreshDue(m.lastCassContextFetch, m.cassContextRefreshInterval) && !m.fetchingCassContext {
		m.fetchingCassContext = true
		m.lastCassContextFetch = now
		cmds = append(cmds, m.fetchCASSContextCmd())
	}

	if refreshDue(m.lastCheckpointFetch, m.checkpointRefreshInterval) && !m.fetchingCheckpoint {
		m.fetchingCheckpoint = true
		m.lastCheckpointFetch = now
		cmds = append(cmds, m.fetchCheckpointStatus())
	}

	if refreshDue(m.lastHandoffFetch, m.handoffRefreshInterval) && !m.fetchingHandoff {
		m.fetchingHandoff = true
		m.lastHandoffFetch = now
		cmds = append(cmds, m.fetchHandoffCmd())
	}

	return cmds
}

func (m *Model) scheduleSpawnRefresh(now time.Time) tea.Cmd {
	if refreshDue(m.lastSpawnFetch, m.spawnRefreshInterval) && !m.fetchingSpawn {
		m.fetchingSpawn = true
		m.lastSpawnFetch = now
		return m.fetchSpawnStateCmd()
	}
	return nil
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
	contentHeight := contentHeightFor(m.height)

	listBorder := t.Surface1
	if m.focusedPanel == PanelPaneList {
		listBorder = t.Primary
	}

	detailBorder := t.Pink
	if m.focusedPanel == PanelDetail {
		detailBorder = t.Primary
	}

	// Build left panel (pane list)
	listContent := m.renderPaneList(leftWidth - 4) // -4 for borders/padding
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	// Build right panel (detail view)
	detailContent := m.renderPaneDetail(rightWidth - 4)
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder). // Accent color for detail
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
	// The dashboard UI uses a left margin; trim the rightmost panel so the total
	// rendered width stays within the terminal width at exact thresholds.
	rightWidth = maxInt(rightWidth-2, 0)

	contentHeight := contentHeightFor(m.height)

	listBorder := t.Surface1
	if m.focusedPanel == PanelPaneList {
		listBorder = t.Primary
	}

	detailBorder := t.Pink
	if m.focusedPanel == PanelDetail {
		detailBorder = t.Primary
	}

	sidebarBorder := t.Lavender
	if m.focusedPanel == PanelSidebar {
		sidebarBorder = t.Primary
	}

	listContent := m.renderPaneList(leftWidth - 4)
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	detailContent := m.renderPaneDetail(centerWidth - 4)
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder).
		Width(centerWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(detailContent)

	sidebarContent := m.renderSidebar(rightWidth-4, contentHeight-2)
	sidebarPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(sidebarBorder).
		Width(rightWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(sidebarContent)

	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, listPanel, detailPanel, sidebarPanel)
}

func (m Model) renderSidebar(width, height int) string {
	t := m.theme
	var lines []string

	if width <= 0 {
		return ""
	}

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

	// Spawn progress (only show if active)
	if m.spawnPanel != nil && m.spawnPanel.IsActive() {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		panelHeight := 8 // Fixed height for spawn panel
		if height-used > panelHeight {
			m.spawnPanel.SetSize(width, panelHeight)
			lines = append(lines, m.spawnPanel.View())
			lines = append(lines, "")
		}
	}

	if len(m.agentMailLockInfo) > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Lavender).Bold(true).Render("Active Locks"))
		for _, lock := range m.agentMailLockInfo {
			lines = append(lines, fmt.Sprintf("🔒 %s", layout.TruncateWidthDefault(lock.PathPattern, width-4)))
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

	// Metrics (best-effort, height-gated)
	if m.metricsPanel != nil && height > 0 && (m.metricsError != nil || m.metricsData.TotalTokens > 0 || len(m.metricsData.Agents) > 0) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.metricsPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.metricsPanel.Focus()
			} else {
				m.metricsPanel.Blur()
			}
			m.metricsPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.metricsPanel.View())
		}
	}

	// Command history (best-effort, height-gated)
	if m.historyPanel != nil && height > 0 && (len(m.cmdHistory) > 0 || m.historyError != nil) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.historyPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.historyPanel.Focus()
			} else {
				m.historyPanel.Blur()
			}
			m.historyPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.historyPanel.View())
		}
	}

	// File activity (best-effort, height-gated)
	if m.filesPanel != nil && height > 0 && (len(m.fileChanges) > 0 || m.fileChangesError != nil) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.filesPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.filesPanel.Focus()
			} else {
				m.filesPanel.Blur()
			}
			m.filesPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.filesPanel.View())
		}
	}

	// CASS context (best-effort, height-gated)
	if m.cassPanel != nil && height > 0 {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.cassPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.cassPanel.Focus()
			} else {
				m.cassPanel.Blur()
			}
			m.cassPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.cassPanel.View())
		}
	}

	// Ensure stable height by padding the sidebar to fill allocated space
	content := strings.Join(lines, "\n")
	return panels.FitToHeight(content, height)
}

// renderMegaLayout renders a five-panel layout: Agents | Detail | Beads | Alerts | Activity
func (m Model) renderMegaLayout() string {
	t := m.theme
	p1, p2, p3, p4, p5 := layout.MegaProportions(m.width)
	// The dashboard UI uses a left margin; trim the rightmost panel so the total
	// rendered width stays within the terminal width at exact thresholds.
	p5 = maxInt(p5-2, 0)
	p1Inner := maxInt(p1-4, 0)
	p2Inner := maxInt(p2-4, 0)
	p3Inner := maxInt(p3-4, 0)
	p4Inner := maxInt(p4-4, 0)
	p5Inner := maxInt(p5-4, 0)

	contentHeight := contentHeightFor(m.height)

	listBorder := t.Surface1
	if m.focusedPanel == PanelPaneList {
		listBorder = t.Primary
	}

	detailBorder := t.Pink
	if m.focusedPanel == PanelDetail {
		detailBorder = t.Primary
	}

	beadsBorder := t.Green
	if m.focusedPanel == PanelBeads {
		beadsBorder = t.Primary
	}

	alertsBorder := t.Red
	if m.focusedPanel == PanelAlerts {
		alertsBorder = t.Primary
	}

	conflictsBorder := t.Red
	if m.focusedPanel == PanelConflicts {
		conflictsBorder = t.Primary
	}

	sidebarBorder := t.Lavender
	if m.focusedPanel == PanelSidebar {
		sidebarBorder = t.Primary
	}

	panel1 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(p1).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderPaneList(p1Inner))

	panel2 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder).
		Width(p2).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderPaneDetail(p2Inner))

	panel3 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(beadsBorder).
		Width(p3).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderBeadsPanel(p3Inner, contentHeight-2))

	// Show conflicts panel instead of alerts when there are active conflicts
	var panel4 string
	if m.conflictsPanel.HasConflicts() {
		panel4 = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(conflictsBorder).
			Width(p4).Height(contentHeight).MaxHeight(contentHeight).
			Padding(0, 1).
			Render(m.renderConflictsPanel(p4Inner, contentHeight-2))
	} else {
		panel4 = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(alertsBorder).
			Width(p4).Height(contentHeight).MaxHeight(contentHeight).
			Padding(0, 1).
			Render(m.renderAlertsPanel(p4Inner, contentHeight-2))
	}

	panel5 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(sidebarBorder).
		Width(p5).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderSidebar(p5Inner, contentHeight-2))

	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, panel1, panel2, panel3, panel4, panel5)
}

func (m Model) renderBeadsPanel(width, height int) string {
	m.beadsPanel.SetSize(width, height)
	return m.beadsPanel.View()
}

func (m Model) renderAlertsPanel(width, height int) string {
	m.alertsPanel.SetSize(width, height)
	if m.focusedPanel == PanelAlerts {
		m.alertsPanel.Focus()
	} else {
		m.alertsPanel.Blur()
	}
	return m.alertsPanel.View()
}

func (m Model) renderConflictsPanel(width, height int) string {
	m.conflictsPanel.SetSize(width, height)
	if m.focusedPanel == PanelConflicts {
		m.conflictsPanel.Focus()
	} else {
		m.conflictsPanel.Blur()
	}
	return m.conflictsPanel.View()
}

func (m Model) renderSpawnPanel(width, height int) string {
	m.spawnPanel.SetSize(width, height)
	return m.spawnPanel.View()
}

func (m Model) renderMetricsPanel(width, height int) string {
	m.metricsPanel.SetSize(width, height)
	if m.focusedPanel == PanelMetrics {
		m.metricsPanel.Focus()
	} else {
		m.metricsPanel.Blur()
	}
	return m.metricsPanel.View()
}

func (m Model) renderHistoryPanel(width, height int) string {
	m.historyPanel.SetSize(width, height)
	if m.focusedPanel == PanelHistory {
		m.historyPanel.Focus()
	} else {
		m.historyPanel.Blur()
	}
	return m.historyPanel.View()
}

// renderPaneList renders a compact list of panes with status indicators
func (m Model) renderPaneList(width int) string {
	t := m.theme
	var lines []string

	// Calculate layout dimensions
	dims := CalculateLayout(width, 1)

	// Header row
	lines = append(lines, RenderTableHeader(dims, t))

	// Pane rows (hydrated with status, beads, file changes, with per-agent border colors)
	rows := BuildPaneTableRows(m.panes, m.agentStatuses, m.paneStatus, &m.beadsSummary, m.fileChanges, m.animTick, t)
	if summary := activitySummaryLine(rows, t); summary != "" {
		lines = append(lines, " "+summary)
	}
	for i := range rows {
		rows[i].IsSelected = i == m.cursor
		lines = append(lines, RenderPaneRow(rows[i], dims, t))
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

	// Header with profile name as primary identifier
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width-2).
		Padding(0, 1)
	lines = append(lines, headerStyle.Render(p.Type.ProfileName()))
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
		Render(typeIcon + " " + p.Type.ProfileName())
	lines = append(lines, labelStyle.Render("Profile:")+typeBadge)

	// Index
	lines = append(lines, labelStyle.Render("Index:")+valueStyle.Render(fmt.Sprintf("%d", p.Index)))

	// Pane ID (secondary identifier)
	lines = append(lines, labelStyle.Render("Pane ID:")+valueStyle.Render(p.Title))

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

	statusState := ps.State
	if statusState == "" || statusState == "unknown" {
		if st, ok := m.agentStatuses[p.ID]; ok && st.State != status.StateUnknown {
			statusState = st.State.String()
		}
	}
	statusText := statusState
	if statusText == "" {
		statusText = "unknown"
		statusState = statusText
	}
	var statusColor lipgloss.Color
	var statusIcon string
	switch statusState {
	case "working":
		// Animated spinner for working state
		statusIcon = WorkingSpinnerFrame(m.animTick)
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
			lines = append(lines, fmt.Sprintf("  🔒 %s (%s)", layout.TruncateWidthDefault(lock.PathPattern, 20), lock.AgentName))
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
	if st, ok := m.agentStatuses[p.ID]; ok && st.LastOutput != "" && m.renderer != nil {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Recent Output"))
		lines = append(lines, "")

		// Use cached rendering if available (cache is populated in Update, not here)
		if cached, ok := m.renderedOutputCache[p.ID]; ok {
			lines = append(lines, cached)
		} else {
			// Fallback: render on demand but don't cache here (View must be pure)
			// The Update handler will populate the cache on the next status update
			rendered, err := m.renderer.Render(st.LastOutput)
			if err == nil {
				lines = append(lines, rendered)
			} else {
				lines = append(lines, layout.TruncateWidthDefault(st.LastOutput, 500))
			}
		}
	}

	return strings.Join(lines, "\n")
}

func activitySummaryLine(rows []PaneTableRow, t theme.Theme) string {
	if len(rows) == 0 {
		return ""
	}

	counts := make(map[string]int)
	for _, row := range rows {
		state := row.Status
		if state == "" {
			state = "unknown"
		}
		counts[state]++
	}

	var badges []string
	badges = append(badges, activityCountBadge("working", counts["working"], t))
	badges = append(badges, activityCountBadge("idle", counts["idle"], t))
	badges = append(badges, activityCountBadge("error", counts["error"], t))
	badges = append(badges, activityCountBadge("compacted", counts["compacted"], t))
	badges = append(badges, activityCountBadge("rate_limited", counts["rate_limited"], t))
	badges = append(badges, activityCountBadge("unknown", counts["unknown"], t))

	var compactBadges []string
	for _, badge := range badges {
		if badge != "" {
			compactBadges = append(compactBadges, badge)
		}
	}
	if len(compactBadges) == 0 {
		return ""
	}

	label := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
	return label.Render("Activity:") + " " + strings.Join(compactBadges, " ")
}

// mergeRoutingIntoMetrics updates metrics data with routing scores.
// It matches agents by pane ID (mapped from pane title/name).
func (m Model) mergeRoutingIntoMetrics(data panels.MetricsData, scores map[string]RoutingScore) panels.MetricsData {
	// Build a lookup from pane name to pane ID
	paneNameToID := make(map[string]string)
	for _, p := range m.panes {
		paneNameToID[p.Title] = p.ID
	}

	// Update each agent with routing info
	for i := range data.Agents {
		agent := &data.Agents[i]
		// Look up pane ID by agent name
		paneID, ok := paneNameToID[agent.Name]
		if !ok {
			continue
		}

		// Look up routing score by pane ID
		if score, found := scores[paneID]; found {
			agent.RoutingScore = score.Score
			agent.IsRecommended = score.IsRecommended
			agent.State = score.State
		}
	}

	return data
}

// handleConflictAction handles user actions on file reservation conflicts.
// It integrates with Agent Mail to send messages or force-release reservations.
func (m *Model) handleConflictAction(conflict watcher.FileConflict, action watcher.ConflictAction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get project key from project directory or current working directory
	projectKey := m.projectDir
	if projectKey == "" {
		var err error
		projectKey, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get project directory: %w", err)
		}
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))

	switch action {
	case watcher.ConflictActionWait:
		// Wait action: nothing to do, user will wait for reservation to expire
		log.Printf("[ConflictAction] Waiting for reservation to expire: %s (held by %v)", conflict.Path, conflict.Holders)
		return nil

	case watcher.ConflictActionRequest:
		// Request action: send a message to the holder requesting handoff
		if len(conflict.Holders) == 0 {
			return fmt.Errorf("no holders to request handoff from")
		}

		// Register ourselves if not already registered
		agentName := m.session + "_dashboard"
		_, err := client.RegisterAgent(ctx, agentmail.RegisterAgentOptions{
			ProjectKey:      projectKey,
			Program:         "ntm-dashboard",
			Model:           "local",
			Name:            agentName,
			TaskDescription: "Dashboard conflict resolution",
		})
		if err != nil {
			log.Printf("[ConflictAction] Warning: could not register agent for messaging: %v", err)
			// Continue anyway - the agent might already be registered
		}

		// Send handoff request to each holder
		for _, holder := range conflict.Holders {
			subject := fmt.Sprintf("Handoff Request: %s", conflict.Path)
			body := fmt.Sprintf("**File Handoff Request**\n\n"+
				"Agent `%s` needs to edit the file:\n"+
				"```\n%s\n```\n\n"+
				"You currently hold the reservation for this file.\n"+
				"Please release the reservation when you're done editing, or confirm you're still actively working on it.\n\n"+
				"*Sent via NTM Dashboard conflict resolution*",
				conflict.RequestorAgent, conflict.Path)

			_, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
				ProjectKey:  projectKey,
				SenderName:  agentName,
				To:          []string{holder},
				Subject:     subject,
				BodyMD:      body,
				Importance:  "high",
				AckRequired: true,
			})
			if err != nil {
				log.Printf("[ConflictAction] Failed to send handoff request to %s: %v", holder, err)
				return fmt.Errorf("failed to send handoff request to %s: %w", holder, err)
			}
			log.Printf("[ConflictAction] Sent handoff request to %s for %s", holder, conflict.Path)
		}
		return nil

	case watcher.ConflictActionForce:
		// Force action: force-release the reservation via Agent Mail
		if len(conflict.HolderReservationIDs) == 0 {
			return fmt.Errorf("no reservation IDs available for force-release")
		}

		// Force-release each reservation
		for _, reservationID := range conflict.HolderReservationIDs {
			agentName := m.session + "_dashboard"
			result, err := client.ForceReleaseReservation(ctx, agentmail.ForceReleaseOptions{
				ProjectKey:     projectKey,
				AgentName:      agentName,
				ReservationID:  reservationID,
				Note:           fmt.Sprintf("Force-released by %s via NTM dashboard", conflict.RequestorAgent),
				NotifyPrevious: true,
			})
			if err != nil {
				log.Printf("[ConflictAction] Failed to force-release reservation %d: %v", reservationID, err)
				return fmt.Errorf("failed to force-release reservation %d: %w", reservationID, err)
			}
			log.Printf("[ConflictAction] Force-released reservation %d: success=%v", reservationID, result.Success)
		}
		return nil

	case watcher.ConflictActionDismiss:
		// Dismiss action: just remove the notification (handled by the panel)
		log.Printf("[ConflictAction] Dismissed conflict notification: %s", conflict.Path)
		return nil

	default:
		return fmt.Errorf("unknown conflict action: %v", action)
	}
}

// executeReplay executes a replay of a history entry
func (m Model) executeReplay(entry history.HistoryEntry) tea.Cmd {
	return func() tea.Msg {
		// Create a new history entry for the replay
		replayEntry := history.NewEntry(m.session, entry.Targets, entry.Prompt, history.SourceReplay)

		// Set template if the original entry used one
		if entry.Template != "" {
			replayEntry.Template = entry.Template
		}

		// Append to history
		if err := history.Append(replayEntry); err != nil {
			log.Printf("[Replay] Failed to append replay entry to history: %v", err)
			replayEntry.SetError(err)
		} else {
			// Execute the replay using tmux client
			client := tmux.DefaultClient

			// Parse targets - for now, replay to the same targets as the original entry
			// In the future, we could show a dialog to let user choose new targets
			for _, targetStr := range entry.Targets {
				target := fmt.Sprintf("%s:%s", m.session, targetStr)
				if err := client.SendKeys(target, entry.Prompt, true); err != nil {
					log.Printf("[Replay] Failed to send to target %s: %v", targetStr, err)
					replayEntry.SetError(fmt.Errorf("failed to send to target %s: %w", targetStr, err))
				}
			}

			// If no errors occurred, mark as successful
			if replayEntry.Error == "" {
				replayEntry.SetSuccess()
			}
		}

		// Return a message to update the status
		return struct {
			Entry   history.HistoryEntry
			Success bool
		}{
			Entry:   *replayEntry,
			Success: replayEntry.Success,
		}
	}
}

// Run starts the dashboard
func Run(session, projectDir string) error {
	model := New(session, projectDir)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
