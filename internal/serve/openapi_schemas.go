package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/metrics"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// DefaultOpenAPIServerURL is the canonical default base URL for the served REST API.
const DefaultOpenAPIServerURL = "http://127.0.0.1:7337"

var (
	reflectTimeType       = reflect.TypeOf(time.Time{})
	reflectDurationType   = reflect.TypeOf(time.Duration(0))
	reflectRawMessageType = reflect.TypeOf(json.RawMessage{})
)

// routeQueryParam documents a query parameter accepted by a served handler.
type routeQueryParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// routeSchemaSpec binds a walked route or handler to concrete Go request/response types.
type routeSchemaSpec struct {
	ComponentName string
	Response      any
	Envelope      bool
	Request       any
	RequestReq    bool
	QueryParams   []routeQueryParam
	SSE           bool
	WebSocket     bool
	BinaryArchive bool
	Accepted202   bool
}

// =============================================================================
// Go-Reflected Envelope & Domain Shapes for Served REST Handlers
// =============================================================================

type AccountQuotaSummary struct {
	Provider          string `json:"provider"`
	Current           string `json:"current"`
	UsagePercent      int    `json:"usage_percent"`
	LimitReset        string `json:"limit_reset,omitempty"`
	AvailableAccounts int    `json:"available_accounts"`
	RateLimited       bool   `json:"rate_limited"`
}

type AccountsListEnvelopeResponse struct {
	APIResponse
	Accounts []robot.AccountInfo `json:"accounts"`
}

type AccountsByProviderEnvelopeResponse struct {
	APIResponse
	Provider string              `json:"provider"`
	Accounts []robot.AccountInfo `json:"accounts"`
}

type AccountStatusEnvelopeResponse struct {
	APIResponse
	Accounts map[string]robot.ProviderStatus `json:"accounts"`
}

type ActiveAccountsEnvelopeResponse struct {
	APIResponse
	Active map[string]robot.AccountInfo `json:"active"`
}

type AccountQuotaEnvelopeResponse struct {
	APIResponse
	Quotas []AccountQuotaSummary `json:"quotas"`
}

type AutoRotateConfigEnvelopeResponse struct {
	APIResponse
	Config AccountsConfig `json:"config"`
}

type AutoRotateConfigPatchRequest struct {
	AutoRotateEnabled     *bool    `json:"auto_rotate_enabled,omitempty"`
	AutoRotateThreshold   *float64 `json:"auto_rotate_threshold,omitempty"`
	RateLimitCooldownSec  *int     `json:"rate_limit_cooldown_sec,omitempty"`
	PreferLeastUsed       *bool    `json:"prefer_least_used,omitempty"`
	NotifyOnRotate        *bool    `json:"notify_on_rotate,omitempty"`
	MaxRotationsPerWindow *int     `json:"max_rotations_per_window,omitempty"`
}

type AccountHistoryEnvelopeResponse struct {
	APIResponse
	History []AccountRotationEvent `json:"history"`
	Total   int                    `json:"total"`
}

type RotateAccountRequest struct {
	Provider  string `json:"provider"`
	AccountID string `json:"account_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type RotateProviderAccountRequest struct {
	AccountID string `json:"account_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type RotateAccountEnvelopeResponse struct {
	APIResponse
	Switch robot.SwitchAccountResult `json:"switch"`
}

type ApprovalDenyRequest struct {
	Reason string `json:"reason,omitempty"`
}

// SSEEventStream documents Server-Sent Events frames emitted by NTM streaming endpoints.
type SSEEventStream struct {
	Event string               `json:"event"`
	ID    string               `json:"id,omitempty"`
	Retry int                  `json:"retry,omitempty"`
	Data  robot.AttentionEvent `json:"data"`
}

// WSProtocolCatalog documents the multiplexed WebSocket frame types served on /api/v1/ws.
type WSProtocolCatalog struct {
	Message           WSMessage           `json:"message"`
	Event             WSEvent             `json:"event,omitempty"`
	SubscribeRequest  WSSubscribeRequest  `json:"subscribe_request,omitempty"`
	Error             WSError             `json:"error,omitempty"`
	StreamReset       WSStreamReset       `json:"stream_reset,omitempty"`
	PaneOutputDropped WSPaneOutputDropped `json:"pane_output_dropped,omitempty"`
}

type AttentionEventsEnvelopeResponse struct {
	Success        bool                          `json:"success"`
	Timestamp      string                        `json:"timestamp"`
	Version        string                        `json:"version"`
	OutputFormat   string                        `json:"output_format"`
	Events         []robot.AttentionEvent        `json:"events"`
	SinceCursor    int64                         `json:"since_cursor"`
	NextCursor     int64                         `json:"next_cursor"`
	NewestCursor   int64                         `json:"newest_cursor"`
	OldestCursor   int64                         `json:"oldest_cursor"`
	EventCount     int                           `json:"event_count"`
	Truncated      bool                          `json:"truncated"`
	ReplayTarget   *robot.HistoricalReplayTarget `json:"replay_target,omitempty"`
	Reconstruction *robot.ReconstructionMeta     `json:"reconstruction,omitempty"`
	Boundedness    *robot.HistoricalBoundedness  `json:"boundedness,omitempty"`
	Incident       *robot.SnapshotIncident       `json:"incident,omitempty"`
	Error          string                        `json:"error,omitempty"`
	ErrorCode      string                        `json:"error_code,omitempty"`
	Hint           string                        `json:"hint,omitempty"`
}

type AttentionItemTarget struct {
	Cursor   int64  `json:"cursor"`
	ItemKey  string `json:"item_key"`
	DedupKey string `json:"dedup_key"`
	Session  string `json:"session,omitempty"`
	Pane     string `json:"pane,omitempty"`
}

type AttentionItemActionResult struct {
	Action         string                   `json:"action"`
	PreviousState  string                   `json:"previous_state"`
	NewState       string                   `json:"new_state"`
	AttentionState state.AttentionItemState `json:"attention_state"`
}

type AttentionItemAuditReceipt struct {
	EventID    int64  `json:"event_id"`
	DecisionID int64  `json:"decision_id"`
	ActorType  string `json:"actor_type"`
	ActorID    string `json:"actor_id"`
}

type AttentionItemStateEnvelopeResponse struct {
	APIResponse
	Target AttentionItemTarget       `json:"target"`
	Result AttentionItemActionResult `json:"result"`
	Audit  AttentionItemAuditReceipt `json:"audit"`
}

type WSTicketEnvelopeResponse struct {
	APIResponse
	Ticket       string `json:"ticket"`
	ExpiresInSec int    `json:"expires_in_sec"`
}

type BeadsListEnvelopeResponse struct {
	APIResponse
	Beads []bv.BeadPreview `json:"beads"`
	Count int              `json:"count"`
}

type BeadRecordEnvelopeResponse struct {
	APIResponse
	Bead    map[string]any `json:"bead,omitempty"`
	Created bool           `json:"created,omitempty"`
	Updated bool           `json:"updated,omitempty"`
	Output  string         `json:"output,omitempty"`
}

type BeadClaimEnvelopeResponse struct {
	APIResponse
	Bead     map[string]any `json:"bead,omitempty"`
	Claimed  bool           `json:"claimed"`
	Assignee string         `json:"assignee"`
}

type BeadCloseEnvelopeResponse struct {
	APIResponse
	BeadID string         `json:"bead_id,omitempty"`
	Closed bool           `json:"closed,omitempty"`
	Bead   map[string]any `json:"bead,omitempty"`
	Job    *Job           `json:"job,omitempty"`
	Output string         `json:"output,omitempty"`
}

type BeadDepsListEnvelopeResponse struct {
	APIResponse
	BeadID       string         `json:"bead_id"`
	Dependencies []any          `json:"dependencies,omitempty"`
	Output       string         `json:"output,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

type BeadDepLinkEnvelopeResponse struct {
	APIResponse
	BeadID    string `json:"bead_id"`
	BlockedBy string `json:"blocked_by"`
	Linked    bool   `json:"linked"`
	Output    string `json:"output,omitempty"`
}

type BeadDepUnlinkEnvelopeResponse struct {
	APIResponse
	BeadID   string `json:"bead_id"`
	DepID    string `json:"dep_id"`
	Unlinked bool   `json:"unlinked"`
	Output   string `json:"output,omitempty"`
}

type BeadsStatsEnvelopeResponse struct {
	APIResponse
	Stats bv.BeadsSummary `json:"stats"`
}

type BeadsTriageEnvelopeResponse struct {
	APIResponse
	Recommendations []bv.TriageRecommendation `json:"recommendations"`
	Count           int                       `json:"count"`
}

type BeadsInsightsEnvelopeResponse struct {
	APIResponse
	Insights bv.InsightsResponse `json:"insights"`
}

type BeadsPlanEnvelopeResponse struct {
	APIResponse
	Plan bv.PlanResponse `json:"plan"`
}

type BeadsPriorityEnvelopeResponse struct {
	APIResponse
	Priority bv.PriorityResponse `json:"priority"`
}

type BeadsRecipesEnvelopeResponse struct {
	APIResponse
	Recipes bv.RecipesResponse `json:"recipes"`
}

type BeadsSyncEnvelopeResponse struct {
	APIResponse
	Synced bool   `json:"synced"`
	Output string `json:"output"`
}

type ScanStartedEnvelopeResponse struct {
	APIResponse
	ScanID  string `json:"scan_id"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type ScannerHistoryEnvelopeResponse struct {
	APIResponse
	Scans  []ScanRecord `json:"scans"`
	Count  int          `json:"count"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
}

type ScannerFindingsEnvelopeResponse struct {
	APIResponse
	Findings []FindingRecord `json:"findings"`
	Count    int             `json:"count"`
	Offset   int             `json:"offset"`
	Limit    int             `json:"limit"`
}

type FindingBeadCreatedEnvelopeResponse struct {
	APIResponse
	BeadID    string `json:"bead_id"`
	FindingID string `json:"finding_id"`
	Title     string `json:"title"`
}

type BugsListEnvelopeResponse struct {
	APIResponse
	Bugs   []FindingRecord `json:"bugs"`
	Count  int             `json:"count"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

type BugNotifyEnvelopeResponse struct {
	APIResponse
	Notified      bool   `json:"notified"`
	Channel       string `json:"channel"`
	FindingsCount int    `json:"findings_count"`
	MinSeverity   string `json:"min_severity"`
}

type CASSInsightsEnvelopeResponse struct {
	APIResponse
	TotalDocuments int                      `json:"total_documents"`
	Agents         []cass.AggregationBucket `json:"agents"`
	Workspaces     []cass.AggregationBucket `json:"workspaces"`
	Warnings       []string                 `json:"warnings,omitempty"`
}

type CASSTimelineEnvelopeResponse struct {
	APIResponse
	Entries       []cass.TimelineSession `json:"entries"`
	Count         int                    `json:"count"`
	TotalSessions int                    `json:"total_sessions"`
	Since         string                 `json:"since,omitempty"`
}

type CASSPreviewEnvelopeResponse struct {
	APIResponse
	SourcePath string `json:"source_path"`
	Agent      string `json:"agent"`
	Workspace  string `json:"workspace"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Snippet    string `json:"snippet"`
	MatchType  string `json:"match_type"`
	LineNumber int    `json:"line_number,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

type MemoryDaemonStatusEnvelopeResponse struct {
	APIResponse
	Installed bool   `json:"installed"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	Port      int    `json:"port,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

type MemoryDaemonActionEnvelopeResponse struct {
	APIResponse
	State     string `json:"state"`
	Port      int    `json:"port,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

type MemoryContextEnvelopeResponse struct {
	APIResponse
	Task             string       `json:"task"`
	RelevantRules    []MemoryRule `json:"relevant_rules"`
	AntiPatterns     []MemoryRule `json:"anti_patterns"`
	HistorySnippets  []any        `json:"history_snippets"`
	SuggestedQueries []string     `json:"suggested_queries,omitempty"`
}

type MemoryOutcomeEnvelopeResponse struct {
	APIResponse
	Recorded bool   `json:"recorded"`
	Status   string `json:"status"`
}

type MemoryPrivacyUpdateEnvelopeResponse struct {
	APIResponse
	Enabled bool            `json:"enabled"`
	Agents  map[string]bool `json:"agents,omitempty"`
	Updated bool            `json:"updated"`
}

type MemoryRulesEnvelopeResponse struct {
	APIResponse
	Rules        []MemoryRule `json:"rules"`
	AntiPatterns []MemoryRule `json:"anti_patterns"`
}

type CheckpointsListEnvelopeResponse struct {
	APIResponse
	SessionName               string               `json:"session_name"`
	Count                     int                  `json:"count"`
	Checkpoints               []CheckpointResponse `json:"checkpoints"`
	InvalidCheckpointsPresent bool                 `json:"invalid_checkpoints_present,omitempty"`
	InvalidCheckpointIDs      []string             `json:"invalid_checkpoint_ids,omitempty"`
}

type CheckpointEnvelopeResponse struct {
	APIResponse
	Checkpoint CheckpointResponse `json:"checkpoint"`
	Imported   bool               `json:"imported,omitempty"`
}

type DeleteCheckpointEnvelopeResponse struct {
	APIResponse
	Deleted           bool   `json:"deleted"`
	CheckpointID      string `json:"checkpoint_id"`
	InvalidCheckpoint bool   `json:"invalid_checkpoint,omitempty"`
}

type MailHealthEnvelopeResponse struct {
	APIResponse
	Status    string `json:"status"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type MailProjectsEnvelopeResponse struct {
	APIResponse
	Projects []agentmail.Project `json:"projects"`
	Count    int                 `json:"count"`
}

type MailAgentsEnvelopeResponse struct {
	APIResponse
	Agents []agentmail.Agent `json:"agents"`
	Count  int               `json:"count"`
}

type MailAgentEnvelopeResponse struct {
	APIResponse
	Agent agentmail.Agent `json:"agent"`
}

type MailInboxEnvelopeResponse struct {
	APIResponse
	Messages []MailInboxMessageResponse `json:"messages"`
	Count    int                        `json:"count"`
}

type MailSendEnvelopeResponse struct {
	APIResponse
	Deliveries []agentmail.MessageDelivery `json:"deliveries"`
	Count      int                         `json:"count"`
}

type MailMessageEnvelopeResponse struct {
	APIResponse
	Message MailMessageResponse `json:"message"`
}

type MailReadEnvelopeResponse struct {
	APIResponse
	MessageID string `json:"message_id"`
	Read      bool   `json:"read"`
	ReadAt    string `json:"read_at,omitempty"`
}

type MailAckEnvelopeResponse struct {
	APIResponse
	MessageID      string `json:"message_id"`
	Acknowledged   bool   `json:"acknowledged"`
	AcknowledgedAt string `json:"acknowledged_at,omitempty"`
}

type MailSearchEnvelopeResponse struct {
	APIResponse
	Results []MailSearchResultResponse `json:"results"`
	Count   int                        `json:"count"`
	Query   string                     `json:"query"`
}

type MailContactsEnvelopeResponse struct {
	APIResponse
	Contacts []agentmail.ContactLink `json:"contacts"`
	Count    int                     `json:"count"`
}

type ReservationsListEnvelopeResponse struct {
	APIResponse
	Reservations []agentmail.FileReservation `json:"reservations"`
	Count        int                         `json:"count"`
}

type ReservationEnvelopeResponse struct {
	APIResponse
	Reservation agentmail.FileReservation `json:"reservation"`
}

type ReservationConflictsEnvelopeResponse struct {
	APIResponse
	Conflicts    []agentmail.ReservationConflict `json:"conflicts"`
	HasConflicts bool                            `json:"has_conflicts"`
}

type PipelinesListEnvelopeResponse struct {
	APIResponse
	Pipelines []pipeline.PipelineSummary `json:"pipelines"`
	Count     int                        `json:"count"`
}

type PipelineExecutionEnvelopeResponse struct {
	APIResponse
	RunID       string                           `json:"run_id"`
	WorkflowID  string                           `json:"workflow_id,omitempty"`
	Session     string                           `json:"session,omitempty"`
	Status      string                           `json:"status"`
	DryRun      bool                             `json:"dry_run,omitempty"`
	Resumed     bool                             `json:"resumed,omitempty"`
	Background  bool                             `json:"background,omitempty"`
	StartedAt   string                           `json:"started_at,omitempty"`
	FinishedAt  string                           `json:"finished_at,omitempty"`
	DurationMs  int64                            `json:"duration_ms,omitempty"`
	CurrentStep string                           `json:"current_step,omitempty"`
	Progress    pipeline.PipelineProgress        `json:"progress,omitempty"`
	Steps       map[string]pipeline.PipelineStep `json:"steps,omitempty"`
	Message     string                           `json:"message,omitempty"`
	Error       string                           `json:"error,omitempty"`
}

type PipelineRecoveryEnvelopeResponse struct {
	APIResponse
	RunID      string                                    `json:"run_id"`
	WorkflowID string                                    `json:"workflow_id"`
	Session    string                                    `json:"session,omitempty"`
	Deliveries []pipeline.AgentDeliveryRecoveryEntry     `json:"deliveries,omitempty"`
	Commands   []pipeline.CommandExecutionRecoveryEntry  `json:"commands,omitempty"`
	Selectors  []pipeline.CommandEvaluationRecoveryEntry `json:"selectors,omitempty"`
	Delivery   *pipeline.AgentDeliveryRecoveryEntry      `json:"delivery,omitempty"`
	Command    *pipeline.CommandExecutionRecoveryEntry   `json:"command,omitempty"`
	Selector   *pipeline.CommandEvaluationRecoveryEntry  `json:"selector,omitempty"`
	Replaced   bool                                      `json:"replaced,omitempty"`
}

type PipelineValidateEnvelopeResponse struct {
	APIResponse
	Valid      bool                      `json:"valid"`
	Validation pipeline.ValidationResult `json:"validation"`
}

type PipelineTemplatesEnvelopeResponse struct {
	APIResponse
	Templates []PipelineTemplate `json:"templates"`
	Count     int                `json:"count"`
}

type PipelineCleanupEnvelopeResponse struct {
	APIResponse
	Deleted        int                          `json:"deleted"`
	Eligible       int                          `json:"eligible"`
	Retained       int                          `json:"retained"`
	Entries        []pipeline.StateCleanupEntry `json:"entries"`
	OlderThanHours int                          `json:"older_than_hours"`
}

type ServerConfigEnvelopeResponse struct {
	APIResponse
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	AuthMode          string   `json:"auth_mode"`
	AllowedOrigins    []string `json:"allowed_origins"`
	PublicBaseURL     string   `json:"public_base_url,omitempty"`
	ProjectDir        string   `json:"project_dir,omitempty"`
	StartupProjectDir string   `json:"startup_project_dir,omitempty"`
}

type ServerConfigPatchRequest struct {
	AllowedOrigins     []string `json:"allowed_origins,omitempty"`
	CORSAllowedOrigins []string `json:"cors_allowed_origins,omitempty"`
	ProjectDir         *string  `json:"project_dir,omitempty"`
}

type ServerConfigPatchEnvelopeResponse struct {
	APIResponse
	Updated bool           `json:"updated"`
	Reset   bool           `json:"reset,omitempty"`
	Config  map[string]any `json:"config"`
}

type DoctorDependencyStatus struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

type DoctorDaemonStatus struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DoctorGuardHookStatus struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	DegradedRuns int    `json:"degraded_runs"`
	Since        string `json:"since,omitempty"`
	Message      string `json:"message,omitempty"`
}

type ServerDoctorEnvelopeResponse struct {
	APIResponse
	Overall      string                   `json:"overall"`
	Warnings     int                      `json:"warnings"`
	Errors       int                      `json:"errors"`
	Dependencies []DoctorDependencyStatus `json:"dependencies"`
	Daemons      []DoctorDaemonStatus     `json:"daemons"`
	GuardHook    DoctorGuardHookStatus    `json:"guard_hook"`
}

type KernelCommandsEnvelopeResponse struct {
	APIResponse
	Commands []kernel.Command `json:"commands"`
	Count    int              `json:"count"`
}

type SessionSummaryRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectPath string `json:"project_path,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Status      string `json:"status,omitempty"`
	Windows     int    `json:"windows,omitempty"`
	Attached    bool   `json:"attached,omitempty"`
	Source      string `json:"source,omitempty"`
}

type SessionsListEnvelopeResponse struct {
	APIResponse
	Sessions []SessionSummaryRecord `json:"sessions"`
	Count    int                    `json:"count"`
}

type SessionDetailEnvelopeResponse struct {
	APIResponse
	Session SessionSummaryRecord `json:"session"`
}

type SessionEventsEnvelopeResponse struct {
	APIResponse
	SessionID string           `json:"session_id"`
	Events    []map[string]any `json:"events"`
	Count     int              `json:"count"`
}

type PaneSummaryRecord struct {
	Index   int      `json:"index"`
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"`
	Variant string   `json:"variant,omitempty"`
	Active  bool     `json:"active"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	Command string   `json:"command,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type PanesListEnvelopeResponse struct {
	APIResponse
	SessionID string              `json:"session_id"`
	Panes     []PaneSummaryRecord `json:"panes"`
	Count     int                 `json:"count"`
}

type PaneDetailEnvelopeResponse struct {
	APIResponse
	Pane PaneSummaryRecord `json:"pane"`
}

type PaneInputEnvelopeResponse struct {
	APIResponse
	Sent bool   `json:"sent"`
	Pane string `json:"pane"`
}

type PaneInterruptEnvelopeResponse struct {
	APIResponse
	Interrupted bool   `json:"interrupted"`
	Pane        string `json:"pane"`
	Key         string `json:"key"`
}

type PaneOutputEnvelopeResponse struct {
	APIResponse
	Pane   string `json:"pane"`
	Output string `json:"output"`
	Lines  int    `json:"lines"`
}

type PaneTitleEnvelopeResponse struct {
	APIResponse
	Pane  string `json:"pane"`
	Title string `json:"title"`
}

type PaneStreamEnvelopeResponse struct {
	APIResponse
	Target  string `json:"target"`
	Topic   string `json:"topic,omitempty"`
	Message string `json:"message"`
}

type StreamingStatsEnvelopeResponse struct {
	APIResponse
	ActiveStreams int      `json:"active_streams,omitempty"`
	TotalLines    int64    `json:"total_lines,omitempty"`
	ActiveTargets []string `json:"active_targets"`
}

type MetricsExportEnvelopeResponse struct {
	APIResponse
	metrics.MetricsReport
	Format string `json:"format"`
}

type SessionAgentsEnvelopeResponse struct {
	APIResponse
	SessionID string           `json:"session_id"`
	Agents    []map[string]any `json:"agents"`
	Count     int              `json:"count"`
	Warnings  []string         `json:"warnings,omitempty"`
}

type JobsListEnvelopeResponse struct {
	APIResponse
	Jobs      []*Job             `json:"jobs"`
	Count     int                `json:"count"`
	Execution JobExecutionStatus `json:"execution"`
}

type JobEnvelopeResponse struct {
	APIResponse
	Job *Job `json:"job"`
}

// =============================================================================
// Route -> Reflected Schema Descriptor Table
// =============================================================================

var servedRouteSchemasByRoute = map[string]routeSchemaSpec{
	// Multi-method routes where GET vs POST/DELETE differ
	"GET /api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/export": {
		ComponentName: "ExportCheckpointResponse",
		Response:      ExportCheckpointResponse{},
		Envelope:      true,
		BinaryArchive: true,
		QueryParams: []routeQueryParam{
			{Name: "format", Type: "string", Description: "Archive format: tar.gz (default) or zip."},
			{Name: "download", Type: "boolean", Description: "Stream raw archive bytes instead of JSON metadata."},
			{Name: "redact_secrets", Type: "boolean", Description: "Redact secrets before export."},
			{Name: "rewrite_paths", Type: "boolean", Description: "Rewrite host-specific paths to portable placeholders."},
		},
	},
	"POST /api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/export": {
		ComponentName: "ExportCheckpointResponse",
		Response:      ExportCheckpointResponse{},
		Envelope:      true,
		Request:       ExportCheckpointRequest{},
	},
	"GET /api/v1/pipelines/{id}/recover": {
		ComponentName: "PipelineRecoveryEnvelopeResponse",
		Response:      PipelineRecoveryEnvelopeResponse{},
		Envelope:      true,
	},
	"POST /api/v1/pipelines/{id}/recover": {
		ComponentName: "PipelineRecoveryEnvelopeResponse",
		Response:      PipelineRecoveryEnvelopeResponse{},
		Envelope:      true,
	},
	"DELETE /api/v1/pipelines/{id}/": {
		ComponentName: "PipelineExecutionEnvelopeResponse",
		Response:      PipelineExecutionEnvelopeResponse{},
		Envelope:      true,
		Accepted202:   true,
	},
	"POST /api/v1/pipelines/{id}/cancel": {
		ComponentName: "PipelineExecutionEnvelopeResponse",
		Response:      PipelineExecutionEnvelopeResponse{},
		Envelope:      true,
		Accepted202:   true,
	},
}

var servedRouteSchemasByHandler = map[string]routeSchemaSpec{
	// Accounts
	"handleListAccountsV1":           {Response: AccountsListEnvelopeResponse{}, Envelope: true},
	"handleAccountStatusV1":          {Response: AccountStatusEnvelopeResponse{}, Envelope: true},
	"handleActiveAccountsV1":         {Response: ActiveAccountsEnvelopeResponse{}, Envelope: true},
	"handleAccountQuotaV1":           {Response: AccountQuotaEnvelopeResponse{}, Envelope: true},
	"handleGetAutoRotateConfigV1":    {Response: AutoRotateConfigEnvelopeResponse{}, Envelope: true},
	"handlePatchAutoRotateConfigV1":  {Response: AutoRotateConfigEnvelopeResponse{}, Envelope: true, Request: AutoRotateConfigPatchRequest{}},
	"handleAccountHistoryV1":         {Response: AccountHistoryEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "limit", Type: "integer", Description: "Maximum history records to return."}}},
	"handleRotateAccountV1":          {Response: RotateAccountEnvelopeResponse{}, Envelope: true, Request: RotateAccountRequest{}, RequestReq: true},
	"handleListAccountsByProviderV1": {Response: AccountsByProviderEnvelopeResponse{}, Envelope: true},
	"handleRotateProviderAccountV1":  {Response: RotateAccountEnvelopeResponse{}, Envelope: true, Request: RotateProviderAccountRequest{}},

	// Safety, Policy, Approvals
	"handleSafetyStatusV1":           {Response: SafetyStatusResponse{}, Envelope: true},
	"handleSafetyBlockedV1":          {Response: SafetyBlockedResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "hours", Type: "integer", Description: "Lookback window in hours."}, {Name: "limit", Type: "integer", Description: "Maximum blocked commands to return."}}},
	"handleSafetyCheckV1":            {Response: SafetyCheckResponse{}, Envelope: true, Request: SafetyCheckRequest{}, RequestReq: true},
	"handleSafetyInstallV1":          {Response: SafetyInstallResponse{}, Envelope: true, Request: SafetyInstallRequest{}},
	"handleSafetyUninstallV1":        {Response: SafetyUninstallResponse{}, Envelope: true},
	"handlePolicyGetV1":              {Response: PolicyGetResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "content", Type: "boolean", Description: "Include raw policy YAML content."}, {Name: "rules", Type: "boolean", Description: "Include expanded rule summaries."}}},
	"handlePolicyUpdateV1":           {Response: PolicyUpdateResponse{}, Envelope: true, Request: PolicyUpdateRequest{}, RequestReq: true},
	"handlePolicyValidateV1":         {Response: PolicyValidateResponse{}, Envelope: true, Request: PolicyValidateRequest{}},
	"handlePolicyResetV1":            {Response: PolicyResetResponse{}, Envelope: true},
	"handlePolicyAutomationGetV1":    {Response: AutomationGetResponse{}, Envelope: true},
	"handlePolicyAutomationUpdateV1": {Response: AutomationUpdateResponse{}, Envelope: true, Request: AutomationUpdateRequest{}, RequestReq: true},
	"handleApprovalsListV1":          {Response: ApprovalsListResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "status", Type: "string", Description: "Filter approvals by status."}}},
	"handleApprovalsHistoryV1":       {Response: ApprovalsListResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "limit", Type: "integer", Description: "Maximum approval records to return."}}},
	"handleApprovalGetV1":            {ComponentName: "ApprovalEnvelopeResponse", Response: Approval{}, Envelope: true},
	"handleApprovalApproveV1":        {Response: ApprovalDecisionResponse{}, Envelope: true},
	"handleApprovalDenyV1":           {Response: ApprovalDecisionResponse{}, Envelope: true, Request: ApprovalDenyRequest{}},
	"handleApprovalRequestV1":        {Response: ApprovalRequestResponse{}, Envelope: true, Request: ApprovalRequestRequest{}, RequestReq: true},

	// Attention & Event Streams
	"handleAttentionStreamV1": {
		Response: SSEEventStream{},
		SSE:      true,
		QueryParams: []routeQueryParam{
			{Name: "since_cursor", Type: "integer", Description: "Replay attention events strictly after this cursor."},
			{Name: "session", Type: "string", Description: "Filter events to one session."},
			{Name: "category", Type: "string", Description: "Comma-separated event category filter."},
			{Name: "actionability", Type: "string", Description: "Comma-separated actionability filter."},
			{Name: "heartbeat", Type: "string", Description: "Optional SSE heartbeat interval duration."},
		},
	},
	"handleEventStream": {
		Response: SSEEventStream{},
		SSE:      true,
	},
	"handleAttentionEventsV1": {
		Response: AttentionEventsEnvelopeResponse{},
		QueryParams: []routeQueryParam{
			{Name: "since_cursor", Type: "integer", Description: "Replay events strictly after cursor."},
			{Name: "limit", Type: "integer", Description: "Maximum events to return (default 100)."},
			{Name: "session", Type: "string", Description: "Filter events by session."},
			{Name: "category", Type: "string", Description: "Comma-separated event categories."},
			{Name: "actionability", Type: "string", Description: "Comma-separated actionability levels."},
			{Name: "incident_id", Type: "string", Description: "Bounded replay around a durable incident ID."},
			{Name: "as_of", Type: "string", Description: "Reconstruct context at or before RFC3339 timestamp."},
			{Name: "window_before_ms", Type: "integer", Description: "Replay window milliseconds before target."},
			{Name: "window_after_ms", Type: "integer", Description: "Replay window milliseconds after target."},
		},
	},
	"handleAttentionDigestV1": {
		Response: robot.AttentionDigest{},
		QueryParams: []routeQueryParam{
			{Name: "since_cursor", Type: "integer", Description: "Aggregate events after cursor."},
			{Name: "category", Type: "string", Description: "Comma-separated category filter."},
			{Name: "session", Type: "string", Description: "Filter digest by session."},
			{Name: "action_required_limit", Type: "integer", Description: "Max action_required items."},
			{Name: "interesting_limit", Type: "integer", Description: "Max interesting items."},
			{Name: "background_limit", Type: "integer", Description: "Max background items."},
			{Name: "trace", Type: "boolean", Description: "Include attention scoring trace."},
		},
	},
	"handleAttentionItemStateV1": {
		Response:   AttentionItemStateEnvelopeResponse{},
		Envelope:   true,
		Request:    AttentionItemStateRequest{},
		RequestReq: true,
	},

	// Auth & WebSocket & OpenAPI
	"handleIssueWSTicketV1": {Response: WSTicketEnvelopeResponse{}, Envelope: true},
	"handleWebSocket":       {Response: WSProtocolCatalog{}, WebSocket: true},
	"handleOpenAPISpec":     {Response: OpenAPISpec{}},

	// Beads & BV
	"handleListBeads":       {Response: BeadsListEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "status", Type: "string", Description: "Filter beads by status."}, {Name: "assignee", Type: "string", Description: "Filter beads by assignee."}, {Name: "label", Type: "string", Description: "Filter beads by label."}}},
	"handleCreateBead":      {Response: BeadRecordEnvelopeResponse{}, Envelope: true, Request: CreateBeadRequest{}, RequestReq: true},
	"handleGetBead":         {Response: BeadRecordEnvelopeResponse{}, Envelope: true},
	"handleUpdateBead":      {Response: BeadRecordEnvelopeResponse{}, Envelope: true, Request: UpdateBeadRequest{}},
	"handleClaimBead":       {Response: BeadClaimEnvelopeResponse{}, Envelope: true, Request: ClaimBeadRequest{}, RequestReq: true},
	"handleCloseBead":       {Response: BeadCloseEnvelopeResponse{}, Envelope: true, Accepted202: true, QueryParams: []routeQueryParam{{Name: "async", Type: "boolean", Description: "Queue asynchronous bead close job and return 202."}}},
	"handleBeadsStats":      {Response: BeadsStatsEnvelopeResponse{}, Envelope: true},
	"handleBeadsReady":      {Response: BeadsListEnvelopeResponse{}, Envelope: true},
	"handleBeadsBlocked":    {Response: BeadsListEnvelopeResponse{}, Envelope: true},
	"handleBeadsInProgress": {Response: BeadsListEnvelopeResponse{}, Envelope: true},
	"handleListBeadDeps":    {Response: BeadDepsListEnvelopeResponse{}, Envelope: true},
	"handleAddBeadDep":      {Response: BeadDepLinkEnvelopeResponse{}, Envelope: true, Request: AddDependencyRequest{}, RequestReq: true},
	"handleRemoveBeadDep":   {Response: BeadDepUnlinkEnvelopeResponse{}, Envelope: true},
	"handleBeadsTriage":     {Response: BeadsTriageEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "limit", Type: "integer", Description: "Maximum triage recommendations."}}},
	"handleBeadsInsights":   {Response: BeadsInsightsEnvelopeResponse{}, Envelope: true},
	"handleBeadsPlan":       {Response: BeadsPlanEnvelopeResponse{}, Envelope: true},
	"handleBeadsPriority":   {Response: BeadsPriorityEnvelopeResponse{}, Envelope: true},
	"handleBeadsRecipes":    {Response: BeadsRecipesEnvelopeResponse{}, Envelope: true},
	"handleBeadsSync":       {Response: BeadsSyncEnvelopeResponse{}, Envelope: true},

	// Scanner & Bugs
	"handleScannerStatus":         {Response: ScanStatusResponse{}, Envelope: true},
	"handleRunScan":               {Response: ScanStartedEnvelopeResponse{}, Envelope: true, Request: ScanOptionsRequest{}, Accepted202: true},
	"handleScannerHistory":        {Response: ScannerHistoryEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "limit", Type: "integer", Description: "Maximum scan records."}, {Name: "offset", Type: "integer", Description: "Pagination offset."}}},
	"handleListFindings":          {Response: ScannerFindingsEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "scan_id", Type: "string", Description: "Filter by scan ID."}, {Name: "severity", Type: "string", Description: "Filter by severity."}, {Name: "include_dismissed", Type: "boolean", Description: "Include dismissed findings."}, {Name: "limit", Type: "integer", Description: "Max findings."}, {Name: "offset", Type: "integer", Description: "Pagination offset."}}},
	"handleGetFinding":            {ComponentName: "FindingEnvelopeResponse", Response: FindingRecord{}, Envelope: true},
	"handleDismissFinding":        {ComponentName: "FindingEnvelopeResponse", Response: FindingRecord{}, Envelope: true, Request: DismissFindingRequest{}},
	"handleCreateBeadFromFinding": {Response: FindingBeadCreatedEnvelopeResponse{}, Envelope: true, Request: CreateBeadFromFindingRequest{}},
	"handleListBugs":              {Response: BugsListEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "severity", Type: "string", Description: "Filter by severity."}, {Name: "category", Type: "string", Description: "Filter by category."}, {Name: "file", Type: "string", Description: "Filter by file path."}, {Name: "limit", Type: "integer", Description: "Max bugs."}, {Name: "offset", Type: "integer", Description: "Pagination offset."}}},
	"handleBugsSummary":           {Response: BugSummaryResponse{}, Envelope: true},
	"handleBugsNotify":            {Response: BugNotifyEnvelopeResponse{}, Envelope: true, Request: BugNotifyRequest{}, RequestReq: true},

	// CASS & Memory
	"handleCASSStatus":          {Response: CASSStatusResponse{}, Envelope: true},
	"handleCASSCapabilities":    {ComponentName: "CASSCapabilitiesEnvelopeResponse", Response: cass.Capabilities{}, Envelope: true},
	"handleCASSSearch":          {ComponentName: "CASSSearchEnvelopeResponse", Response: cass.SearchResponse{}, Envelope: true, Request: CASSSearchRequest{}, RequestReq: true},
	"handleCASSInsights":        {Response: CASSInsightsEnvelopeResponse{}, Envelope: true},
	"handleCASSTimeline":        {Response: CASSTimelineEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "since", Type: "string", Description: "Filter timeline since duration or date."}, {Name: "agent", Type: "string", Description: "Filter by agent."}, {Name: "workspace", Type: "string", Description: "Filter by workspace."}, {Name: "limit", Type: "integer", Description: "Maximum timeline entries."}}},
	"handleCASSPreview":         {Response: CASSPreviewEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "path", Type: "string", Description: "Document source path to preview.", Required: true}}},
	"handleMemoryDaemonStatus":  {Response: MemoryDaemonStatusEnvelopeResponse{}, Envelope: true},
	"handleMemoryDaemonStart":   {Response: MemoryDaemonActionEnvelopeResponse{}, Envelope: true, Request: MemoryDaemonRequest{}, Accepted202: true},
	"handleMemoryDaemonStop":    {Response: MemoryDaemonActionEnvelopeResponse{}, Envelope: true},
	"handleMemoryContext":       {Response: MemoryContextEnvelopeResponse{}, Envelope: true, Request: MemoryContextRequest{}, RequestReq: true},
	"handleMemoryOutcome":       {Response: MemoryOutcomeEnvelopeResponse{}, Envelope: true, Request: MemoryOutcomeRequest{}, RequestReq: true},
	"handleMemoryPrivacyGet":    {ComponentName: "MemoryPrivacyEnvelopeResponse", Response: PrivacySettings{}, Envelope: true},
	"handleMemoryPrivacyUpdate": {Response: MemoryPrivacyUpdateEnvelopeResponse{}, Envelope: true, Request: PrivacyUpdateRequest{}, RequestReq: true},
	"handleMemoryRules":         {Response: MemoryRulesEnvelopeResponse{}, Envelope: true},

	// Checkpoints & Rollback
	"handleListCheckpoints":   {Response: CheckpointsListEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "details", Type: "boolean", Description: "Include full checkpoint manifest details."}}},
	"handleCreateCheckpoint":  {Response: CheckpointEnvelopeResponse{}, Envelope: true, Request: CreateCheckpointRequest{}},
	"handleGetCheckpoint":     {Response: CheckpointEnvelopeResponse{}, Envelope: true},
	"handleDeleteCheckpoint":  {Response: DeleteCheckpointEnvelopeResponse{}, Envelope: true},
	"handleRestoreCheckpoint": {Response: RestoreCheckpointResponse{}, Envelope: true, Request: RestoreCheckpointRequest{}},
	"handleVerifyCheckpoint":  {Response: VerifyCheckpointResponse{}, Envelope: true},
	"handleImportCheckpoint":  {Response: CheckpointEnvelopeResponse{}, Envelope: true, Request: ImportCheckpointRequest{}, RequestReq: true},
	"handleRollback":          {Response: RollbackResponse{}, Envelope: true, Request: RollbackRequest{}},

	// Mail & File Reservations
	"handleMailHealth":              {Response: MailHealthEnvelopeResponse{}, Envelope: true},
	"handleListMailProjects":        {Response: MailProjectsEnvelopeResponse{}, Envelope: true},
	"handleListMailAgents":          {Response: MailAgentsEnvelopeResponse{}, Envelope: true},
	"handleCreateMailAgent":         {Response: MailAgentEnvelopeResponse{}, Envelope: true, Request: CreateAgentRequest{}, RequestReq: true},
	"handleGetMailAgent":            {Response: MailAgentEnvelopeResponse{}, Envelope: true},
	"handleMailInbox":               {Response: MailInboxEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Agent name to fetch inbox for."}, {Name: "limit", Type: "integer", Description: "Max messages."}, {Name: "urgent_only", Type: "boolean", Description: "Only return urgent messages."}, {Name: "since_ts", Type: "string", Description: "Filter messages since RFC3339 timestamp."}, {Name: "include_bodies", Type: "boolean", Description: "Include full message bodies."}}},
	"handleSendMessage":             {Response: MailSendEnvelopeResponse{}, Envelope: true, Request: SendMessageRequest{}, RequestReq: true},
	"handleGetMessage":              {Response: MailMessageEnvelopeResponse{}, Envelope: true},
	"handleReplyMessage":            {Response: MailMessageEnvelopeResponse{}, Envelope: true, Request: ReplyMessageRequest{}, RequestReq: true},
	"handleMarkMessageRead":         {Response: MailReadEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Agent marking message read."}}},
	"handleAckMessage":              {Response: MailAckEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Agent acknowledging message."}}},
	"handleSearchMessages":          {Response: MailSearchEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "q", Type: "string", Description: "Search query.", Required: true}, {Name: "limit", Type: "integer", Description: "Max search results."}}},
	"handleThreadSummary":           {ComponentName: "MailThreadSummaryEnvelopeResponse", Response: agentmail.ThreadSummaryResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "include_examples", Type: "boolean", Description: "Include example messages."}, {Name: "llm_mode", Type: "boolean", Description: "Enable LLM summarization."}}},
	"handleListContacts":            {Response: MailContactsEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Agent whose contacts to list."}}},
	"handleRequestContact":          {ComponentName: "MailContactLinkEnvelopeResponse", Response: agentmail.ContactLink{}, Envelope: true, Request: ContactRequestBody{}, RequestReq: true},
	"handleRespondContact":          {ComponentName: "MailContactRespondEnvelopeResponse", Response: agentmail.ContactLink{}, Envelope: true, Request: ContactRespondRequest{}, RequestReq: true},
	"handleSetContactPolicy":        {ComponentName: "MailContactPolicyEnvelopeResponse", Response: agentmail.ContactPolicyResult{}, Envelope: true, Request: SetContactPolicyRequest{}, RequestReq: true},
	"handleListReservations":        {Response: ReservationsListEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Filter reservations by agent."}}},
	"handleReservePaths":            {ComponentName: "ReservePathsEnvelopeResponse", Response: agentmail.ReservationResult{}, Envelope: true, Request: ReservePathsRequest{}, RequestReq: true},
	"handleReleaseReservations":     {ComponentName: "ReleaseReservationsEnvelopeResponse", Response: agentmail.ReleaseReservationsResult{}, Envelope: true, Request: ReleaseReservationsRequest{}},
	"handleReservationConflicts":    {Response: ReservationConflictsEnvelopeResponse{}, Envelope: true},
	"handleGetReservation":          {Response: ReservationEnvelopeResponse{}, Envelope: true},
	"handleReleaseReservationByID":  {ComponentName: "ReleaseReservationsEnvelopeResponse", Response: agentmail.ReleaseReservationsResult{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "agent_name", Type: "string", Description: "Agent releasing reservation."}}},
	"handleRenewReservation":        {ComponentName: "RenewReservationsEnvelopeResponse", Response: agentmail.RenewReservationsResult{}, Envelope: true, Request: RenewReservationsRequest{}},
	"handleForceReleaseReservation": {ComponentName: "ForceReleaseEnvelopeResponse", Response: agentmail.ForceReleaseResult{}, Envelope: true, Request: ForceReleaseRequest{}},

	// Pipelines
	"handleListPipelines":         {Response: PipelinesListEnvelopeResponse{}, Envelope: true},
	"handleRunPipeline":           {Response: PipelineExecutionEnvelopeResponse{}, Envelope: true, Request: PipelineRunRequest{}, RequestReq: true},
	"handleExecPipeline":          {Response: PipelineExecutionEnvelopeResponse{}, Envelope: true, Request: PipelineExecRequest{}, RequestReq: true},
	"handleGetPipeline":           {Response: PipelineExecutionEnvelopeResponse{}, Envelope: true},
	"handleResumePipeline":        {Response: PipelineExecutionEnvelopeResponse{}, Envelope: true},
	"handleValidatePipeline":      {Response: PipelineValidateEnvelopeResponse{}, Envelope: true, Request: PipelineValidateRequest{}, RequestReq: true},
	"handleListPipelineTemplates": {Response: PipelineTemplatesEnvelopeResponse{}, Envelope: true},
	"handleCleanupPipelines":      {Response: PipelineCleanupEnvelopeResponse{}, Envelope: true, Request: PipelineCleanupRequest{}},

	// System, Config, Deps, Doctor, Kernel
	"handleDepsV1":         {ComponentName: "DepsEnvelopeResponse", Response: output.DepsResponse{}, Envelope: true},
	"handleDoctorV1":       {Response: ServerDoctorEnvelopeResponse{}, Envelope: true},
	"handleGetConfigV1":    {Response: ServerConfigEnvelopeResponse{}, Envelope: true},
	"handlePatchConfigV1":  {Response: ServerConfigPatchEnvelopeResponse{}, Envelope: true, Request: ServerConfigPatchRequest{}, RequestReq: true},
	"handleKernelCommands": {Response: KernelCommandsEnvelopeResponse{}, Envelope: true},

	// Sessions, Panes, Agents, Streaming, Metrics, Jobs
	"handleSessionsV1":           {Response: SessionsListEnvelopeResponse{}, Envelope: true},
	"handleSessionV1":            {Response: SessionDetailEnvelopeResponse{}, Envelope: true},
	"handleSessionEventsRouteV1": {Response: SessionEventsEnvelopeResponse{}, Envelope: true},
	"handleCreateSessionV1":      {ComponentName: "CreateSessionEnvelopeResponse", Response: output.CreateResponse{}, Envelope: true, Request: CreateSessionRequest{}, RequestReq: true},
	"handleSessionStatusV1":      {ComponentName: "SessionStatusEnvelopeResponse", Response: output.StatusResponse{}, Envelope: true},
	"handleSessionAttachV1":      {ComponentName: "SessionAttachEnvelopeResponse", Response: output.SessionResponse{}, Envelope: true},
	"handleSessionZoomV1":        {ComponentName: "SessionActionEnvelopeResponse", Response: output.SuccessResponse{}, Envelope: true, Request: SessionZoomRequest{}, RequestReq: true},
	"handleSessionViewV1":        {ComponentName: "SessionActionEnvelopeResponse", Response: output.SuccessResponse{}, Envelope: true},
	"handleListPanesV1":          {Response: PanesListEnvelopeResponse{}, Envelope: true},
	"handleGetPaneV1":            {Response: PaneDetailEnvelopeResponse{}, Envelope: true},
	"handlePaneInputV1":          {Response: PaneInputEnvelopeResponse{}, Envelope: true, Request: PaneInputRequest{}, RequestReq: true},
	"handlePaneInterruptV1":      {Response: PaneInterruptEnvelopeResponse{}, Envelope: true},
	"handlePaneOutputV1":         {Response: PaneOutputEnvelopeResponse{}, Envelope: true, QueryParams: []routeQueryParam{{Name: "lines", Type: "integer", Description: "Number of scrollback lines to capture (default 100)."}}},
	"handleGetPaneTitleV1":       {Response: PaneTitleEnvelopeResponse{}, Envelope: true},
	"handleSetPaneTitleV1":       {Response: PaneTitleEnvelopeResponse{}, Envelope: true, Request: PaneTitleRequest{}, RequestReq: true},
	"handleStartPaneStreamV1":    {Response: PaneStreamEnvelopeResponse{}, Envelope: true},
	"handleStopPaneStreamV1":     {Response: PaneStreamEnvelopeResponse{}, Envelope: true},
	"handleStreamingStatsV1":     {Response: StreamingStatsEnvelopeResponse{}, Envelope: true},
	"handleMetricsExportV1":      {Response: MetricsExportEnvelopeResponse{}, Envelope: true},
	"handleListAgentsV1":         {Response: SessionAgentsEnvelopeResponse{}, Envelope: true},
	"handleListJobs":             {Response: JobsListEnvelopeResponse{}, Envelope: true},
	"handleCreateJob":            {Response: JobEnvelopeResponse{}, Envelope: true, Request: CreateJobRequest{}, RequestReq: true},
	"handleGetJob":               {Response: JobEnvelopeResponse{}, Envelope: true},
	"handleCancelJob":            {Response: JobEnvelopeResponse{}, Envelope: true},

	// Request bodies for robot-joined mutation handlers
	"handleAgentSpawnV1":     {Request: AgentSpawnRequest{}, RequestReq: true},
	"handleAgentSendV1":      {Request: AgentSendRequest{}, RequestReq: true},
	"handleAgentInterruptV1": {Request: AgentInterruptRequest{}},
	"handleAgentWaitV1":      {Request: AgentWaitRequest{}},
	"handleWaitRequestV1":    {Request: waitRequestBodyV1{}, QueryParams: []routeQueryParam{{Name: "timeout", Type: "string", Description: "Maximum wait duration (e.g. 30s)."}, {Name: "poll", Type: "string", Description: "Polling interval duration (e.g. 100ms)."}}},

	// Ensemble REST API
	"handleEnsembleModesV1": {
		Response: EnsembleModesEnvelopeResponse{},
		Envelope: true,
		QueryParams: []routeQueryParam{
			{Name: "category", Type: "string", Description: "Filter reasoning modes by category code or name."},
			{Name: "tier", Type: "string", Description: "Filter reasoning modes by tier (core, advanced, experimental, all)."},
			{Name: "search", Type: "string", Description: "Case-insensitive search term across mode ID, name, description, and best_for."},
			{Name: "limit", Type: "integer", Description: "Maximum modes to return per page."},
			{Name: "offset", Type: "integer", Description: "Zero-based pagination offset."},
		},
	},
	"handleEnsemblePresetsV1": {
		Response: EnsemblePresetsEnvelopeResponse{},
		Envelope: true,
	},
	"handleEnsembleSuggestV1": {
		Response:   EnsembleSuggestEnvelopeResponse{},
		Envelope:   true,
		Request:    EnsembleSuggestRequest{},
		RequestReq: true,
	},
	"handleEnsembleSessionV1": {
		Response: EnsembleSessionEnvelopeResponse{},
		Envelope: true,
	},
	"handleEnsembleSpawnV1": {
		Response:   EnsembleSpawnEnvelopeResponse{},
		Envelope:   true,
		Request:    EnsembleSpawnRequest{},
		RequestReq: true,
	},
	"handleEnsembleSynthesizeV1": {
		Response: EnsembleSynthesizeEnvelopeResponse{},
		Envelope: true,
		Request:  EnsembleSynthesizeRequest{},
	},
	"handleEnsembleStopV1": {
		Response: EnsembleStopEnvelopeResponse{},
		Envelope: true,
		Request:  EnsembleStopRequest{},
	},

	// Worktrees REST API
	"handleListWorktreesV1": {
		Response: WorktreesListEnvelopeResponse{},
		Envelope: true,
		QueryParams: []routeQueryParam{
			{Name: "session", Type: "string", Description: "Optional session filter; omitted lists worktrees across all sessions."},
			{Name: "project_dir", Type: "string", Description: "Optional existing absolute project directory override (alias: ?project=)."},
		},
	},
	"handleCreateWorktreeV1": {
		Response:   WorktreeCreateEnvelopeResponse{},
		Envelope:   true,
		Request:    CreateWorktreeRequest{},
		RequestReq: true,
	},
	"handleMergeWorktreeV1": {
		Response: WorktreeMergeEnvelopeResponse{},
		Envelope: true,
		Request:  MergeWorktreeRequest{},
	},
	"handleDeleteWorktreeV1": {
		Response: WorktreeDeleteEnvelopeResponse{},
		Envelope: true,
		QueryParams: []routeQueryParam{
			{Name: "session", Type: "string", Description: "Optional session owning the agent worktree; auto-resolved when unique."},
			{Name: "force", Type: "boolean", Description: "Force removal even when uncommitted or unmerged changes exist."},
			{Name: "project_dir", Type: "string", Description: "Optional existing absolute project directory override (alias: ?project=)."},
		},
	},

	// Backpressure & Synthetic Swarm Lab REST API
	"handleHealthBackpressureV1": {
		Response: HealthBackpressureEnvelopeResponse{},
		Envelope: true,
		QueryParams: []routeQueryParam{
			{Name: "session", Type: "string", Description: "Optional tmux session filter for live subsystem backpressure inspection."},
		},
	},
	"handleSwarmLabScenariosV1": {
		Response: SwarmLabEnvelopeResponse{},
		Envelope: true,
		QueryParams: []routeQueryParam{
			{Name: "scenario", Type: "string", Description: "Optional synthetic experiment scenario ID filter."},
			{Name: "gate", Type: "string", Description: "Optional scale gate filter (short, benchmark, load)."},
		},
	},
	"handleSwarmLabRunV1": {
		Response: SwarmLabEnvelopeResponse{},
		Envelope: true,
		Request:  SwarmLabRunRequest{},
	},
}

func lookupServedRouteSchema(rt RouterRoute) (routeSchemaSpec, bool) {
	if spec, ok := servedRouteSchemasByRoute[rt.Method+" "+rt.Pattern]; ok {
		return spec, true
	}
	if rt.Handler != "" {
		if spec, ok := servedRouteSchemasByHandler[rt.Handler]; ok {
			return spec, true
		}
	}
	return routeSchemaSpec{}, false
}

// =============================================================================
// Go Struct Reflection -> OpenAPI 3.1 Component Schemas
// =============================================================================

type openapiReflector struct {
	components *OpenAPIComponents
	seenTypes  map[string]reflect.Type
	namesByTyp map[reflect.Type]string
}

func newOpenAPIReflector(components *OpenAPIComponents) *openapiReflector {
	if components.Schemas == nil {
		components.Schemas = make(map[string]*Schema)
	}
	return &openapiReflector{
		components: components,
		seenTypes:  make(map[string]reflect.Type),
		namesByTyp: make(map[reflect.Type]string),
	}
}

func (r *openapiReflector) registerRootType(sample any, explicitName string, withEnvelope bool) string {
	if sample == nil {
		return ""
	}
	t := reflect.TypeOf(sample)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := strings.TrimSpace(explicitName)
	if name == "" {
		name = exportedTypeName(t)
	}
	if name == "" {
		return ""
	}

	if existing, exists := r.components.Schemas[name]; exists {
		if prevType, ok := r.seenTypes[name]; ok && prevType == t {
			return name
		}
		if _, ok := r.seenTypes[name]; !ok && existing != nil && !withEnvelope {
			return name
		}
	}

	cycleGuard := &Schema{Type: "object"}
	r.components.Schemas[name] = cycleGuard
	r.seenTypes[name] = t
	if !withEnvelope {
		r.namesByTyp[t] = name
	}
	schema := r.structSchema(t, withEnvelope)
	*cycleGuard = *schema
	return name
}

func (r *openapiReflector) componentNameForStruct(t reflect.Type) string {
	if name, ok := r.namesByTyp[t]; ok {
		return name
	}
	base := t.Name()
	if base == "" {
		return ""
	}
	candidate := base
	if existingSchema, exists := r.components.Schemas[candidate]; exists {
		if prevType, ok := r.seenTypes[candidate]; ok && prevType == t {
			r.namesByTyp[t] = candidate
			return candidate
		}
		if existingSchema != nil {
			pkg := t.PkgPath()
			if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
				pkg = pkg[idx+1:]
			}
			pkg = strings.ReplaceAll(pkg, "_", " ")
			pkg = strings.ReplaceAll(pkg, "-", " ")
			var prefix strings.Builder
			for _, part := range strings.Fields(pkg) {
				prefix.WriteString(strings.ToUpper(part[:1]) + part[1:])
			}
			if prefix.Len() > 0 && !strings.HasPrefix(base, prefix.String()) {
				candidate = prefix.String() + base
			} else {
				candidate = base + "Model"
			}
		}
	}
	r.namesByTyp[t] = candidate
	r.seenTypes[candidate] = t
	return candidate
}

func exportedTypeName(t reflect.Type) string {
	name := t.Name()
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (r *openapiReflector) structSchema(t reflect.Type, withEnvelope bool) *Schema {
	props := make(map[string]*Schema)
	reqSet := make(map[string]bool)
	r.collectStructFields(t, props, reqSet)

	if withEnvelope {
		if _, ok := props["success"]; !ok {
			props["success"] = &Schema{Type: "boolean", Description: "Whether the operation succeeded."}
		}
		reqSet["success"] = true
		if _, ok := props["timestamp"]; !ok {
			props["timestamp"] = &Schema{Type: "string", Format: "date-time", Description: "RFC3339 response timestamp."}
		}
		reqSet["timestamp"] = true
		if _, ok := props["request_id"]; !ok {
			props["request_id"] = &Schema{Type: "string", Description: "Unique request correlation identifier."}
		}
	}

	var required []string
	if len(reqSet) > 0 {
		required = make([]string, 0, len(reqSet))
		for k := range reqSet {
			required = append(required, k)
		}
		sort.Strings(required)
	}

	return &Schema{
		Type:       "object",
		Properties: props,
		Required:   required,
	}
}

func (r *openapiReflector) collectStructFields(t reflect.Type, props map[string]*Schema, reqSet map[string]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		fieldName, omitempty := splitJSONTag(tag)
		if f.Anonymous && fieldName == "" {
			r.collectStructFields(f.Type, props, reqSet)
			continue
		}
		if fieldName == "" {
			fieldName = f.Name
		}
		props[fieldName] = r.typeSchema(f.Type)
		if !omitempty && f.Type.Kind() != reflect.Ptr {
			reqSet[fieldName] = true
		}
	}
}

func splitJSONTag(tag string) (string, bool) {
	if tag == "" {
		return "", false
	}
	parts := strings.Split(tag, ",")
	omitempty := false
	for _, p := range parts[1:] {
		if p == "omitempty" || p == "omitzero" {
			omitempty = true
		}
	}
	return parts[0], omitempty
}

func isTimeWrapperStruct(t reflect.Type) bool {
	if t == reflectTimeType {
		return true
	}
	if t.Kind() == reflect.Struct && t.NumField() == 1 && t.Field(0).Anonymous && t.Field(0).Type == reflectTimeType {
		return true
	}
	return false
}

func (r *openapiReflector) typeSchema(t reflect.Type) *Schema {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == reflectRawMessageType {
		return &Schema{Type: "object", AdditionalProperties: true}
	}
	if t == reflectDurationType {
		return &Schema{Type: "integer", Format: "int64"}
	}
	if isTimeWrapperStruct(t) {
		return &Schema{Type: "string", Format: "date-time"}
	}

	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return &Schema{Type: "integer"}
	case reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string", Format: "byte"}
		}
		return &Schema{
			Type:  "array",
			Items: r.typeSchema(t.Elem()),
		}
	case reflect.Map:
		valElem := t.Elem()
		for valElem.Kind() == reflect.Ptr {
			valElem = valElem.Elem()
		}
		if valElem.Kind() == reflect.Interface {
			return &Schema{Type: "object", AdditionalProperties: true}
		}
		return &Schema{
			Type:                 "object",
			AdditionalProperties: r.typeSchema(t.Elem()),
		}
	case reflect.Struct:
		compName := r.componentNameForStruct(t)
		if compName == "" {
			return r.structSchema(t, false)
		}
		if _, exists := r.components.Schemas[compName]; !exists {
			// Insert cycle guard first so self-referential structs terminate cleanly.
			cycleGuard := &Schema{Type: "object"}
			r.components.Schemas[compName] = cycleGuard
			built := r.structSchema(t, false)
			*cycleGuard = *built
		}
		return &Schema{Ref: "#/components/schemas/" + compName}
	case reflect.Interface:
		return &Schema{Type: "object", AdditionalProperties: true}
	default:
		return &Schema{Type: "string"}
	}
}

// applyReflectedRouteSchemas enriches an OpenAPI Operation with Go-reflected
// request, response, query-parameter, SSE, WebSocket, and binary archive schemas.
func applyReflectedRouteSchemas(rt RouterRoute, op *Operation, reflector *openapiReflector, alreadyRobotJoined bool) {
	spec, ok := lookupServedRouteSchema(rt)
	if !ok {
		return
	}

	// Add documented query parameters without duplicating parameters already added.
	existingParams := make(map[string]bool, len(op.Parameters))
	for _, p := range op.Parameters {
		existingParams[p.In+":"+p.Name] = true
	}
	for _, qp := range spec.QueryParams {
		key := "query:" + qp.Name
		if existingParams[key] {
			continue
		}
		paramType := qp.Type
		if paramType == "" {
			paramType = "string"
		}
		op.Parameters = append(op.Parameters, Parameter{
			Name:        qp.Name,
			In:          "query",
			Description: qp.Description,
			Required:    qp.Required,
			Schema:      &Schema{Type: paramType},
		})
		existingParams[key] = true
	}

	// Reflect concrete response type if not already joined via verifiedRobotHandlers.
	if !alreadyRobotJoined && spec.Response != nil {
		respName := reflector.registerRootType(spec.Response, spec.ComponentName, spec.Envelope)
		if respName != "" {
			respRef := "#/components/schemas/" + respName
			const genericDescSuffix = " Response payload is not registered in the robot schema registry; the generic success envelope documents only the envelope fields."
			typedSuffix := fmt.Sprintf(" Response shape reflected from Go type `%s`.", respName)
			if strings.Contains(op.Description, genericDescSuffix) {
				op.Description = strings.Replace(op.Description, genericDescSuffix, typedSuffix, 1)
			} else {
				op.Description += typedSuffix
			}

			switch {
			case spec.SSE:
				op.Responses["200"] = Response{
					Description: "Server-Sent Events (SSE) stream (`text/event-stream`).",
					Content: map[string]MediaType{
						"text/event-stream": {Schema: &Schema{Ref: respRef}},
					},
				}
			case spec.WebSocket:
				// Ensure all 6 WebSocket protocol frame structs are reflected in components.schemas.
				for _, wsModel := range []any{
					WSMessage{},
					WSEvent{},
					WSSubscribeRequest{},
					WSError{},
					WSStreamReset{},
					WSPaneOutputDropped{},
				} {
					reflector.registerRootType(wsModel, "", false)
				}
				op.Responses["101"] = Response{
					Description: "Switching Protocols — WebSocket upgrade frame catalog.",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: respRef}},
					},
				}
				op.Responses["200"] = Response{
					Description: "WebSocket protocol frame catalog (`WSMessage`, `WSEvent`, `WSSubscribeRequest`, `WSError`, `WSStreamReset`, `WSPaneOutputDropped`).",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: respRef}},
					},
				}
			case spec.BinaryArchive:
				op.Responses["200"] = Response{
					Description: "Checkpoint export metadata or raw binary archive (`?download=true`).",
					Content: map[string]MediaType{
						"application/json": {Schema: &Schema{Ref: respRef}},
						"application/gzip": {Schema: &Schema{Type: "string", Format: "binary"}},
						"application/zip":  {Schema: &Schema{Type: "string", Format: "binary"}},
					},
				}
			default:
				if okResp, has200 := op.Responses["200"]; has200 {
					if okResp.Content == nil {
						okResp.Content = make(map[string]MediaType)
					}
					okResp.Content["application/json"] = MediaType{Schema: &Schema{Ref: respRef}}
					op.Responses["200"] = okResp
				}
				if accResp, has202 := op.Responses["202"]; has202 {
					if accResp.Content == nil {
						accResp.Content = make(map[string]MediaType)
					}
					accResp.Content["application/json"] = MediaType{Schema: &Schema{Ref: respRef}}
					op.Responses["202"] = accResp
				} else if spec.Accepted202 {
					op.Responses["202"] = Response{
						Description: "Accepted for asynchronous execution.",
						Content: map[string]MediaType{
							"application/json": {Schema: &Schema{Ref: respRef}},
						},
					}
				}
			}
		}
	}

	// Reflect concrete request body type when specified (preserving hand-authored
	// schemas for handleResumePipeline and handleRecoverPipeline).
	if spec.Request != nil && rt.Handler != "handleResumePipeline" && rt.Handler != "handleRecoverPipeline" {
		if rt.Method == http.MethodPost || rt.Method == http.MethodPut || rt.Method == http.MethodPatch || rt.Method == http.MethodDelete {
			reqName := reflector.registerRootType(spec.Request, "", false)
			if reqName != "" {
				op.RequestBody = &RequestBody{
					Required: spec.RequestReq,
					Content: map[string]MediaType{
						"application/json": {
							Schema: &Schema{Ref: "#/components/schemas/" + reqName},
						},
					},
				}
			}
		}
	}
}
