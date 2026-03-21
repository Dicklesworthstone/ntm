package dashboard

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	synthtui "github.com/Dicklesworthstone/ntm/internal/tui/synthesizer"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// RoutingScore holds routing info for a single agent.
type RoutingScore struct {
	Score         float64 // 0-100 composite routing score
	IsRecommended bool    // True if this is the recommended agent
	State         string  // Agent state (waiting, generating, etc.)
}

// PaneHealthInfo holds health check results for a single pane.
type PaneHealthInfo struct {
	Status       string   // "ok", "warning", "error", "unknown"
	Issues       []string // Issue messages
	RestartCount int      // Restarts in last hour
	Uptime       int      // Seconds of uptime
}

// PanelID identifies a dashboard panel.
type PanelID int

const (
	PanelPaneList PanelID = iota
	PanelDetail
	PanelBeads
	PanelAlerts
	PanelAttention // Attention feed panel
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
	refreshAttention
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
	refreshRCH
	refreshRanoNetwork
	refreshDCG
	refreshPendingRotations
	refreshPTHealth
	refreshSourceCount
)

type costSnapshot struct {
	At       time.Time
	TotalUSD float64
}

// AgentMailLockInfo represents a file lock for dashboard display.
type AgentMailLockInfo struct {
	PathPattern string
	AgentName   string
	Exclusive   bool
	ExpiresIn   string
}

// Model is the session dashboard model.
type Model struct {
	session      string
	projectDir   string
	panes        []tmux.Pane
	width        int
	height       int
	animTick     int
	cursor       int
	paneList     list.Model
	paneDelegate paneDelegate
	focusedPanel PanelID
	focusRing    FocusRing
	quitting     bool
	err          error

	// Diagnostics (opt-in)
	showDiagnostics     bool
	sessionFetchLatency time.Duration
	statusFetchLatency  time.Duration
	statusFetchErr      error

	// Stats
	claudeCount   int
	codexCount    int
	geminiCount   int
	cursorCount   int
	windsurfCount int
	aiderCount    int
	userCount     int

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
	fetchingPTHealth    bool // Whether we're currently fetching process_triage health states
	startupWarmupDone   bool // Ensures post-startup background warmup only runs once

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
	attentionRefreshInterval   time.Duration
	beadsRefreshInterval       time.Duration
	cassContextRefreshInterval time.Duration
	scanRefreshInterval        time.Duration
	checkpointRefreshInterval  time.Duration
	handoffRefreshInterval     time.Duration
	spawnRefreshInterval       time.Duration // How often to poll spawn state (faster when active)

	// Pane output capture budgeting/caching
	paneOutputLines                 int
	paneOutputCaptureBudget         int
	paneOutputCaptureCursor         int
	initialPaneSnapshotDone         bool
	initialFocusedPaneHydrationDone bool
	paneOutputCache                 map[string]string
	paneOutputLastCaptured          map[string]time.Time
	renderedOutputCache             map[string]string // Cache for expensive markdown rendering

	// Local agent performance (best-effort; derived from output deltas + prompt history)
	localPerfByPaneID map[string]*localPerfTracker // keyed by pane ID

	// Ollama /api/ps cache (best-effort)
	ollamaModelMemory map[string]int64 // model name -> bytes
	lastOllamaPSFetch time.Time
	ollamaPSError     error

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
	seenMailAttentionIDs     map[int]struct{} // Track messages with attention signals emitted

	// Config watcher
	configSub    chan *config.Config
	configCloser func()
	cfg          *config.Config

	// Markdown renderer
	renderer *glamour.TermRenderer

	// CASS Search
	showCassSearch bool
	cassSearch     components.CassSearchModel

	// Ensemble modes full-screen view (SynthesizerTUI)
	showEnsembleModes bool
	ensembleModes     synthtui.ModeVisualization

	// Help overlay
	showHelp  bool
	helpModel help.Model // bubbles/help for rendering keybindings

	// Help verbosity (minimal/full), sourced from config (help_verbosity)
	helpVerbosity string

	// Toast notifications (ephemeral bottom-right overlays)
	toasts *components.ToastManager

	// Shared dashboard animation state for focus and other spring-driven UI.
	dashboardSprings *components.SpringManager

	// Panels
	beadsPanel           *panels.BeadsPanel
	alertsPanel          *panels.AlertsPanel
	attentionPanel       *panels.AttentionPanel
	costPanel            *panels.CostPanel
	ranoNetworkPanel     *panels.RanoNetworkPanel
	rchPanel             *panels.RCHPanel
	metricsPanel         *panels.MetricsPanel
	historyPanel         *panels.HistoryPanel
	cassPanel            *panels.CASSPanel
	filesPanel           *panels.FilesPanel
	timelinePanel        *panels.TimelinePanel
	tickerPanel          *panels.TickerPanel
	spawnPanel           *panels.SpawnPanel
	conflictsPanel       *panels.ConflictsPanel
	rotationConfirmPanel *panels.RotationConfirmPanel

	// Data for new panels
	beadsSummary             bv.BeadsSummary
	beadsReady               []bv.BeadPreview
	activeAlerts             []alerts.Alert
	attentionItems           []panels.AttentionItem
	attentionFeedOK          bool // Whether the attention feed is available
	lastAttentionFetch       time.Time
	fetchingAttention        bool
	requestedAttentionCursor int64
	attentionCursorApplied   bool
	costData                 panels.CostPanelData
	costError                error
	metricsData              panels.MetricsData // Cached full metrics data for panel
	cmdHistory               []history.HistoryEntry
	fileChanges              []tracker.RecordedFileChange
	cassContext              []cass.SearchHit
	routingScores            map[string]RoutingScore // keyed by pane ID

	// Cost tracking (estimated; derived from prompt history + pane output deltas)
	costInputTokens         map[string]int     // paneID -> estimated input tokens
	costOutputTokens        map[string]int     // paneID -> estimated output tokens
	costModels              map[string]string  // paneID -> model name (for pricing)
	costLastCosts           map[string]float64 // paneID -> last computed USD cost
	costLastPromptTimestamp time.Time          // last processed prompt timestamp
	costLastPromptRead      time.Time          // last time we attempted to read prompt history
	costSnapshots           []costSnapshot     // rolling window for last-hour computations
	costDailyBudgetUSD      float64            // 0 disables budget display

	// Process triage health states (from pt.HealthMonitor)
	healthStates map[string]*pt.AgentState // pane -> health state

	// Pending rotation confirmations
	pendingRotations    []*ctxmon.PendingRotation
	pendingRotationsErr error
	lastPendingFetch    time.Time
	fetchingPendingRot  bool

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

	// DCG (Destructive Command Guard) status
	dcgEnabled         bool   // Whether DCG is enabled in config
	dcgAvailable       bool   // Whether DCG binary is available
	dcgVersion         string // DCG version string
	dcgBlocked         int    // Commands blocked this session
	dcgLastBlocked     string // Last blocked command (for tooltip)
	dcgError           error  // Any error from DCG check
	fetchingDCG        bool   // Whether we're currently fetching DCG status
	lastDCGFetch       time.Time
	dcgRefreshInterval time.Duration // How often to refresh DCG status

	// RCH (Remote Compilation Helper) status
	rchActive          bool // Whether builds are actively running
	fetchingRCH        bool // Whether we're currently fetching RCH status
	lastRCHFetch       time.Time
	rchRefreshInterval time.Duration // How often to refresh RCH status

	// Rano network activity (best-effort; used by the dashboard network panel).
	fetchingRanoNetwork        bool
	lastRanoNetworkFetch       time.Time
	ranoNetworkRefreshInterval time.Duration

	// Error tracking for data sources (displayed as badges)
	beadsError       error
	alertsError      error
	metricsError     error
	historyError     error
	fileChangesError error
	cassError        error
	routingError     error

	// Activity state tracking for adaptive tick rate (fixes #32 - dashboard flicker)
	lastActivity  time.Time     // When user last interacted (key/mouse/pane output)
	activityState ActivityState // current activity state
	reduceMotion  bool          // NTM_REDUCE_MOTION=1 disables animations
	baseTick      time.Duration // Base tick interval when active (default 100ms)
	idleTick      time.Duration // Tick interval when idle (default 500ms)
	idleTimeout   time.Duration // Time before entering idle state (default 5s)

	// Post-quit action: tells the caller what to do after the TUI exits.
	postQuitAction *PostQuitAction

	// Popup/overlay mode: dashboard is running inside a tmux display-popup.
	// Escape closes the popup; zoom doesn't re-attach (we're already in-session).
	popupMode       bool
	overlayOpenedAt time.Time
}

// PostQuitAction describes what the caller should do after the dashboard TUI exits.
type PostQuitAction struct {
	AttachSession string // Non-empty: attach/switch to this tmux session.
}

// PaneStatus tracks the status of a pane including compaction state.
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

	// Local agent performance (Ollama) - best-effort estimates.
	LocalTokensPerSecond float64
	LocalTotalTokens     int
	LocalLastLatency     time.Duration // First-token latency for the most recent prompt (if observed)
	LocalAvgLatency      time.Duration // Moving average of observed first-token latencies
	LocalMemoryBytes     int64         // VRAM/CPU bytes (from Ollama /api/ps), 0 if unknown
	LocalTPSHistory      []float64     // Recent TPS samples for sparkline rendering

	// Health tracking
	HealthStatus  string   // "ok", "warning", "error", "unknown"
	HealthIssues  []string // List of issue messages (rate limit, crash, etc.)
	RestartCount  int      // Number of restarts in last hour
	UptimeSeconds int      // Seconds since agent started (negative = uptime from tracker)

	// Rotation tracking
	IsRotating bool       // True when agent rotation is in progress
	RotatedAt  *time.Time // When agent was last rotated (nil if never)
}

// DefaultRefreshInterval is the default auto-refresh interval.
const DefaultRefreshInterval = 2 * time.Second

// Per-subsystem refresh cadence (driven by DashboardTickMsg).
const (
	PaneRefreshInterval        = 1 * time.Second
	ContextRefreshInterval     = 10 * time.Second
	AlertsRefreshInterval      = 3 * time.Second
	AttentionRefreshInterval   = 5 * time.Second // Same cadence as alerts
	BeadsRefreshInterval       = 5 * time.Second
	CassContextRefreshInterval = 15 * time.Minute
	ScanRefreshInterval        = 1 * time.Minute
	RCHActiveRefreshInterval   = 5 * time.Second  // Faster polling when builds are active
	RCHIdleRefreshInterval     = 30 * time.Second // Slower polling when idle
	DCGRefreshInterval         = 5 * time.Minute  // DCG status changes infrequently
	CheckpointRefreshInterval  = 30 * time.Second
	HandoffRefreshInterval     = 30 * time.Second
	SpawnActiveRefreshInterval = 500 * time.Millisecond // Poll frequently when spawn is active
	SpawnIdleRefreshInterval   = 2 * time.Second        // Poll slowly when no spawn is active
	MailInboxRefreshInterval   = 30 * time.Second
	CostPromptRefreshInterval  = 5 * time.Second // Poll ~/.ntm/sessions/<session>/prompts.json
)

func (m *Model) initRenderer(width int) {
	m.renderer = newMarkdownRenderer(m.theme, width)
}

func newMarkdownRenderer(t theme.Theme, width int) *glamour.TermRenderer {
	styleName := glamourstyles.DarkStyle
	switch {
	case theme.IsPlain(t):
		styleName = glamourstyles.NoTTYStyle
	case !theme.IsDark(t):
		styleName = glamourstyles.LightStyle
	}

	r, _ := glamour.NewTermRenderer(
		glamour.WithColorProfile(termenv.EnvColorProfile()),
		glamour.WithStandardStyle(styleName),
		glamour.WithWordWrap(width),
	)
	return r
}

// New creates a new dashboard model.
func New(session, projectDir string) Model {
	t := theme.Current()
	theme.ApplyLipGlossDefaults(t)
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
		costInputTokens:            make(map[string]int),
		costOutputTokens:           make(map[string]int),
		costModels:                 make(map[string]string),
		costLastCosts:              make(map[string]float64),
		refreshInterval:            DefaultRefreshInterval,
		paneRefreshInterval:        PaneRefreshInterval,
		contextRefreshInterval:     ContextRefreshInterval,
		alertsRefreshInterval:      AlertsRefreshInterval,
		attentionRefreshInterval:   AttentionRefreshInterval,
		beadsRefreshInterval:       BeadsRefreshInterval,
		cassContextRefreshInterval: CassContextRefreshInterval,
		scanRefreshInterval:        ScanRefreshInterval,
		ranoNetworkRefreshInterval: 1 * time.Second,
		rchRefreshInterval:         RCHIdleRefreshInterval,
		dcgRefreshInterval:         DCGRefreshInterval,
		checkpointRefreshInterval:  CheckpointRefreshInterval,
		handoffRefreshInterval:     HandoffRefreshInterval,
		spawnRefreshInterval:       SpawnIdleRefreshInterval,
		mailInboxRefreshInterval:   MailInboxRefreshInterval,
		paneOutputLines:            50,
		paneOutputCaptureBudget:    20,
		paneOutputCaptureCursor:    0,
		paneOutputCache:            make(map[string]string),
		paneOutputLastCaptured:     make(map[string]time.Time),
		renderedOutputCache:        make(map[string]string),
		healthStatus:               "unknown",
		healthMessage:              "",
		agentMailInbox:             make(map[string][]agentmail.InboxMessage),
		agentMailInboxErrors:       make(map[string]error),
		agentMailAgents:            make(map[string]string),
		seenMailAttentionIDs:       make(map[int]struct{}),
		helpVerbosity:              "full",
		helpModel:                  newHelpModel(t),
		cassSearch: components.NewCassSearch(func(hit cass.SearchHit) tea.Cmd {
			return func() tea.Msg {
				return CassSelectMsg{Hit: hit}
			}
		}),
		ensembleModes:        synthtui.NewModeVisualization(),
		toasts:               components.NewToastManager(),
		dashboardSprings:     components.NewSpringManager(),
		beadsPanel:           panels.NewBeadsPanel(),
		alertsPanel:          panels.NewAlertsPanel(),
		attentionPanel:       panels.NewAttentionPanel(),
		costPanel:            panels.NewCostPanel(),
		ranoNetworkPanel:     panels.NewRanoNetworkPanel(),
		rchPanel:             panels.NewRCHPanel(),
		metricsPanel:         panels.NewMetricsPanel(),
		historyPanel:         panels.NewHistoryPanel(),
		cassPanel:            panels.NewCASSPanel(),
		filesPanel:           panels.NewFilesPanel(),
		timelinePanel:        panels.NewTimelinePanel(),
		tickerPanel:          panels.NewTickerPanel(),
		spawnPanel:           panels.NewSpawnPanel(),
		conflictsPanel:       panels.NewConflictsPanel(),
		rotationConfirmPanel: panels.NewRotationConfirmPanel(),

		// Init() only kicks off the critical first-paint session fetch. Everything
		// else warms in after the UI is already visible.
		fetchingSession: true,
	}

	m.paneDelegate = newPaneDelegate(t, CalculateLayout(40, 1))
	m.paneList = list.New(nil, m.paneDelegate, 40, 8)
	m.paneList.DisableQuitKeybindings()
	m.paneList.SetShowFilter(true)
	m.paneList.SetShowTitle(false)
	m.paneList.SetShowHelp(false)
	m.paneList.SetShowPagination(false)
	m.paneList.SetShowStatusBar(true)
	m.paneList.SetStatusBarItemName("pane", "panes")
	m.paneList.SetFilteringEnabled(true)

	// Initialize last-fetch timestamps to start cadence after the initial fetches from Init.
	now := time.Now()
	m.lastPaneFetch = now
	m.lastContextFetch = now
	m.lastAlertsFetch = now
	m.lastAttentionFetch = now
	m.lastBeadsFetch = now
	m.lastCassContextFetch = now
	m.lastScanFetch = now
	m.lastRanoNetworkFetch = now
	m.lastRCHFetch = now
	m.lastDCGFetch = now
	m.lastCheckpointFetch = now
	m.lastHandoffFetch = now
	m.lastSpawnFetch = now
	m.lastMailInboxFetch = now

	// Initialize activity tracking for adaptive tick rate (fixes #32)
	m.lastActivity = now
	m.activityState = StateActive
	m.reduceMotion = styles.ReducedMotionEnabled()
	m.baseTick = 100 * time.Millisecond
	m.idleTick = 500 * time.Millisecond
	if m.reduceMotion {
		m.baseTick = 250 * time.Millisecond
		m.idleTick = 1 * time.Second
	}
	m.idleTimeout = 5 * time.Second

	applyDashboardEnvOverrides(&m)
	m.syncFocusRing()
	m.syncFocusAnimations()

	// Set up conflict action handler for the conflicts panel
	m.conflictsPanel.SetActionHandler(m.handleConflictAction)

	// Set up the reload channel immediately, but start the file watcher only
	// after startup warmup so fsnotify/path setup never blocks first paint.
	m.configSub = make(chan *config.Config, 1)

	return m
}

func (m Model) startConfigWatcher() tea.Cmd {
	if m.configSub == nil || m.configCloser != nil {
		return nil
	}

	sub := m.configSub
	return func() tea.Msg {
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
		return ConfigWatcherReadyMsg{Closer: closer, Err: err}
	}
}

func (m Model) startRendererInit() tea.Cmd {
	if m.renderer != nil {
		return nil
	}

	_, detailWidth := layout.SplitProportions(m.width)
	contentWidth := detailWidth - 4
	if contentWidth < 20 {
		contentWidth = 20
	}
	t := m.theme

	return func() tea.Msg {
		return RendererReadyMsg{Renderer: newMarkdownRenderer(t, contentWidth)}
	}
}

// cleanup releases resources held by the dashboard model.
// Must be called before tea.Quit to prevent goroutine leaks.
func (m *Model) cleanup() {
	if m.configCloser != nil {
		m.configCloser()
		m.configCloser = nil
	}
}

func (m *Model) selectedPaneID() string {
	if selected := m.paneList.SelectedItem(); selected != nil {
		if item, ok := selected.(paneItem); ok {
			return item.pane.ID
		}
	}
	if m.cursor >= 0 && m.cursor < len(m.panes) {
		return m.panes[m.cursor].ID
	}
	return ""
}

func (m *Model) syncCursorFromPaneList() {
	selectedID := m.selectedPaneID()
	if selectedID != "" {
		for i := range m.panes {
			if m.panes[i].ID == selectedID {
				m.cursor = i
				return
			}
		}
	}
	if len(m.panes) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.panes) {
		m.cursor = len(m.panes) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) setPaneListSelectionByPaneID(paneID string) {
	if paneID != "" {
		if idx := findPaneIndexByID(m.paneList.Items(), paneID); idx >= 0 {
			m.paneList.Select(idx)
			m.syncCursorFromPaneList()
			return
		}
	}
	if len(m.panes) == 0 {
		m.paneList.ResetSelected()
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.panes) {
		m.cursor = len(m.panes) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.paneList.Select(m.cursor)
}

func (m *Model) rebuildPaneList() tea.Cmd {
	listWidth := maxInt(m.width/4-4, 24)
	listHeight := contentHeightFor(m.height)
	if listHeight < 6 {
		listHeight = 6
	}
	if rowCount := len(m.panes) + 4; rowCount < listHeight {
		listHeight = rowCount
	}

	m.paneDelegate.SetDims(CalculateLayout(listWidth, 1))
	m.paneDelegate.SetTick(m.animTick)
	m.paneList.SetDelegate(m.paneDelegate)
	m.paneList.SetSize(listWidth, listHeight)

	prevSelectedID := m.selectedPaneID()
	cmd := m.paneList.SetItems(toPaneItems(m.panes, m.paneStatus, m.beadsReady, m.theme))
	m.setPaneListSelectionByPaneID(prevSelectedID)
	return cmd
}

// NewWithInterval creates a dashboard with custom refresh interval.
func NewWithInterval(session, projectDir string, interval time.Duration) Model {
	m := New(session, projectDir)
	m.refreshInterval = interval
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.tick(),
		m.fetchSessionDataWithOutputs(),
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
