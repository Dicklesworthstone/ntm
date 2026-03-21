// Package dashboard provides a stunning visual session dashboard.
// messages.go contains Bubble Tea message types and dashboard activity state.
package dashboard

import (
	"time"

	"github.com/charmbracelet/glamour"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// DashboardTickMsg is sent for animation updates.
type DashboardTickMsg time.Time

// ActivityState tracks dashboard activity for adaptive tick rate.
type ActivityState int

const (
	// StateActive means user is actively interacting or output is flowing.
	StateActive ActivityState = iota
	// StateIdle means no activity for a period (reduced tick rate).
	StateIdle
)

// RefreshMsg triggers a refresh of session data.
type RefreshMsg struct{}

// StatusUpdateMsg is sent when status detection completes.
type StatusUpdateMsg struct {
	Statuses []status.AgentStatus
	Time     time.Time
	Duration time.Duration
	Err      error
	Gen      uint64
}

// ConfigReloadMsg is sent when configuration changes.
type ConfigReloadMsg struct {
	Config *config.Config
}

// ConfigWatcherReadyMsg reports the outcome of starting the config watcher.
type ConfigWatcherReadyMsg struct {
	Closer func()
	Err    error
}

// RendererReadyMsg reports that the deferred markdown renderer is available.
type RendererReadyMsg struct {
	Renderer *glamour.TermRenderer
}

// HealthCheckMsg is sent when health check (bv drift) completes.
type HealthCheckMsg struct {
	Status  string
	Message string
}

// ScanStatusMsg is sent when UBS scan completes.
type ScanStatusMsg struct {
	Status   string
	Totals   scanner.ScanTotals
	Duration time.Duration
	Err      error
	Gen      uint64
}

// AgentMailUpdateMsg is sent when Agent Mail data is fetched.
type AgentMailUpdateMsg struct {
	Available    bool
	Connected    bool
	ArchiveFound bool
	Locks        int
	LockInfo     []AgentMailLockInfo
	Gen          uint64
}

// AgentMailInboxSummaryMsg is sent when per-agent inbox summaries are fetched.
type AgentMailInboxSummaryMsg struct {
	Inboxes  map[string][]agentmail.InboxMessage
	AgentMap map[string]string
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

// CassSelectMsg is sent when a CASS search result is selected.
type CassSelectMsg struct {
	Hit cass.SearchHit
}

// EnsembleModesDataMsg provides ensemble session + mode catalog data for the SynthesizerTUI modes view.
type EnsembleModesDataMsg struct {
	SessionName string
	Session     *ensemble.EnsembleSession
	Catalog     *ensemble.ModeCatalog
	Panes       []tmux.Pane
	Err         error
}

// BeadsUpdateMsg is sent when beads data is fetched.
type BeadsUpdateMsg struct {
	Summary bv.BeadsSummary
	Ready   []bv.BeadPreview
	Err     error
	Gen     uint64
}

// AlertsUpdateMsg is sent when alerts are refreshed.
type AlertsUpdateMsg struct {
	Alerts []alerts.Alert
	Err    error
	Gen    uint64
}

// AttentionUpdateMsg is sent when attention items are refreshed.
type AttentionUpdateMsg struct {
	Items         []panels.AttentionItem
	FeedAvailable bool
	Gen           uint64
}

// SpawnUpdateMsg is sent when spawn state is updated.
type SpawnUpdateMsg struct {
	Data panels.SpawnData
	Gen  uint64
}

// MetricsUpdateMsg is sent when session metrics are updated.
type MetricsUpdateMsg struct {
	Data panels.MetricsData
	Err  error
	Gen  uint64
}

// HistoryUpdateMsg is sent when command history is fetched.
type HistoryUpdateMsg struct {
	Entries []history.HistoryEntry
	Err     error
	Gen     uint64
}

// FileChangeMsg is sent when file changes are detected.
type FileChangeMsg struct {
	Changes []tracker.RecordedFileChange
	Err     error
	Gen     uint64
}

// CASSContextMsg is sent when relevant context is found.
type CASSContextMsg struct {
	Hits []cass.SearchHit
	Err  error
	Gen  uint64
}

// TimelineLoadMsg is sent when persisted timeline data is loaded.
type TimelineLoadMsg struct {
	Events []state.AgentEvent
	Err    error
}

// HealthUpdateMsg is sent when agent health check completes.
type HealthUpdateMsg struct {
	Health map[string]PaneHealthInfo
	Err    error
}

// PTHealthStatesMsg is sent when process_triage health states are fetched.
type PTHealthStatesMsg struct {
	States map[string]*pt.AgentState
	Gen    uint64
}

// RoutingUpdateMsg is sent when routing scores are fetched.
type RoutingUpdateMsg struct {
	Scores map[string]RoutingScore
	Err    error
	Gen    uint64
}

// CheckpointUpdateMsg is sent when checkpoint status is fetched.
type CheckpointUpdateMsg struct {
	Count     int
	Latest    *checkpoint.Checkpoint
	LatestAge time.Duration
	Status    string
	Err       error
	Gen       uint64
}

// HandoffUpdateMsg is sent when handoff status is fetched.
type HandoffUpdateMsg struct {
	Goal   string
	Now    string
	Age    time.Duration
	Path   string
	Status string
	Err    error
	Gen    uint64
}

// CheckpointCreatedMsg is sent when a new checkpoint is created.
type CheckpointCreatedMsg struct {
	Checkpoint *checkpoint.Checkpoint
	Err        error
}

// FileConflictMsg is sent when a file reservation conflict is detected.
type FileConflictMsg struct {
	Conflict watcher.FileConflict
}

// DCGStatusUpdateMsg is sent when DCG status is fetched.
type DCGStatusUpdateMsg struct {
	Enabled     bool
	Available   bool
	Version     string
	Blocked     int
	LastBlocked string
	Err         error
	Gen         uint64
}

// RCHStatusUpdateMsg is sent when RCH status is fetched.
type RCHStatusUpdateMsg struct {
	Data panels.RCHPanelData
	Gen  uint64
}

// RanoNetworkUpdateMsg is sent when rano network activity is fetched.
type RanoNetworkUpdateMsg struct {
	Data panels.RanoNetworkPanelData
	Gen  uint64
}

// PendingRotationsUpdateMsg is sent when pending rotations data is fetched.
type PendingRotationsUpdateMsg struct {
	Pending []*ctxmon.PendingRotation
	Err     error
	Gen     uint64
}
