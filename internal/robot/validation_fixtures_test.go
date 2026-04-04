package robot

// validation_fixtures_test.go provides deterministic fixture builders for robot
// surface validation. These builders enable reproducible tests with realistic
// state representations for tmux, sessions, agents, coordination, quota, health,
// incidents, cursors, requests, outcomes, and operator attention state.
//
// Bead: bd-j9jo3.9.6

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// =============================================================================
// Scenario Identity
// =============================================================================

// ScenarioID uniquely identifies a test scenario for reproducibility and debugging.
type ScenarioID string

// NewScenarioID creates a deterministic scenario ID from a test name and seed.
func NewScenarioID(testName string, seed int64) ScenarioID {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", testName, seed)))
	return ScenarioID(hex.EncodeToString(h[:8]))
}

// =============================================================================
// Time Fixtures
// =============================================================================

// FixedClock provides deterministic time values for tests.
type FixedClock struct {
	baseTime time.Time
	offset   time.Duration
}

// NewFixedClock creates a clock anchored at a deterministic epoch.
// Seed 0 uses 2026-01-01T00:00:00Z as the base time.
func NewFixedClock(seed int64) *FixedClock {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &FixedClock{
		baseTime: base.Add(time.Duration(seed) * time.Hour),
		offset:   0,
	}
}

// Now returns the current fixed time.
func (c *FixedClock) Now() time.Time {
	return c.baseTime.Add(c.offset)
}

// Advance moves the clock forward by the given duration.
func (c *FixedClock) Advance(d time.Duration) {
	c.offset += d
}

// Reset returns the clock to its initial state.
func (c *FixedClock) Reset() {
	c.offset = 0
}

// RFC3339 returns the current time formatted as RFC3339.
func (c *FixedClock) RFC3339() string {
	return c.Now().Format(time.RFC3339)
}

// =============================================================================
// Cursor Fixtures
// =============================================================================

// CursorFixture provides deterministic cursor values for tests.
type CursorFixture struct {
	base    int64
	counter int64
}

// NewCursorFixture creates a cursor fixture starting from a seed value.
func NewCursorFixture(seed int64) *CursorFixture {
	return &CursorFixture{
		base:    seed * 1000,
		counter: 0,
	}
}

// Next returns the next cursor value.
func (f *CursorFixture) Next() int64 {
	f.counter++
	return f.base + f.counter
}

// Current returns the most recently allocated cursor.
func (f *CursorFixture) Current() int64 {
	return f.base + f.counter
}

// =============================================================================
// Session Fixtures
// =============================================================================

// SessionFixtureOptions configures session fixture generation.
type SessionFixtureOptions struct {
	Name         string
	Label        string
	ProjectPath  string
	AgentCount   int
	PaneCount    int
	Attached     bool
	HealthStatus state.HealthStatus
	Clock        *FixedClock
}

// DefaultSessionFixtureOptions returns sensible defaults for session fixtures.
func DefaultSessionFixtureOptions() SessionFixtureOptions {
	return SessionFixtureOptions{
		Name:         "test-session",
		Label:        "",
		ProjectPath:  "/tmp/test-project",
		AgentCount:   3,
		PaneCount:    4,
		Attached:     true,
		HealthStatus: state.HealthStatusHealthy,
		Clock:        NewFixedClock(0),
	}
}

// RuntimeSessionFixture creates a deterministic RuntimeSession for tests.
func RuntimeSessionFixture(opts SessionFixtureOptions) *state.RuntimeSession {
	now := opts.Clock.Now()
	staleAfter := now.Add(30 * time.Second)

	return &state.RuntimeSession{
		Name:           opts.Name,
		Label:          opts.Label,
		ProjectPath:    opts.ProjectPath,
		Attached:       opts.Attached,
		WindowCount:    1,
		PaneCount:      opts.PaneCount,
		AgentCount:     opts.AgentCount,
		ActiveAgents:   opts.AgentCount / 2,
		IdleAgents:     opts.AgentCount - opts.AgentCount/2,
		ErrorAgents:    0,
		HealthStatus:   opts.HealthStatus,
		HealthReason:   "",
		CreatedAt:      &now,
		LastAttachedAt: &now,
		LastActivityAt: &now,
		CollectedAt:    now,
		StaleAfter:     staleAfter,
	}
}

// =============================================================================
// Agent Fixtures
// =============================================================================

// AgentFixtureOptions configures agent fixture generation.
type AgentFixtureOptions struct {
	SessionName  string
	Pane         int
	AgentType    string
	State        state.AgentState
	HealthStatus state.HealthStatus
	CurrentBead  string
	PendingMail  int
	Clock        *FixedClock
}

// DefaultAgentFixtureOptions returns sensible defaults for agent fixtures.
func DefaultAgentFixtureOptions() AgentFixtureOptions {
	return AgentFixtureOptions{
		SessionName:  "test-session",
		Pane:         1,
		AgentType:    "claude",
		State:        state.AgentStateActive,
		HealthStatus: state.HealthStatusHealthy,
		CurrentBead:  "",
		PendingMail:  0,
		Clock:        NewFixedClock(0),
	}
}

// RuntimeAgentFixture creates a deterministic RuntimeAgent for tests.
func RuntimeAgentFixture(opts AgentFixtureOptions) *state.RuntimeAgent {
	now := opts.Clock.Now()
	staleAfter := now.Add(15 * time.Second)

	return &state.RuntimeAgent{
		ID:               fmt.Sprintf("%s:%d", opts.SessionName, opts.Pane),
		SessionName:      opts.SessionName,
		Pane:             fmt.Sprintf("%d", opts.Pane),
		AgentType:        opts.AgentType,
		Variant:          "",
		TypeConfidence:   0.95,
		TypeMethod:       "process",
		State:            opts.State,
		StateReason:      "",
		StateChangedAt:   &now,
		LastOutputAt:     &now,
		LastOutputAgeSec: 0,
		OutputTailLines:  50,
		CurrentBead:      opts.CurrentBead,
		PendingMail:      opts.PendingMail,
		AgentMailName:    fmt.Sprintf("TestAgent_%d", opts.Pane),
		HealthStatus:     opts.HealthStatus,
		HealthReason:     "",
		CollectedAt:      now,
		StaleAfter:       staleAfter,
	}
}

// =============================================================================
// Attention Event Fixtures
// =============================================================================

// AttentionEventFixtureOptions configures attention event fixture generation.
type AttentionEventFixtureOptions struct {
	Category      EventCategory
	Type          EventType
	Session       string
	Pane          int
	Severity      Severity
	Actionability Actionability
	Summary       string
	ReasonCode    string
	Cursor        *CursorFixture
	Clock         *FixedClock
}

// DefaultAttentionEventFixtureOptions returns sensible defaults.
func DefaultAttentionEventFixtureOptions() AttentionEventFixtureOptions {
	return AttentionEventFixtureOptions{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Session:       "test-session",
		Pane:          1,
		Severity:      SeverityInfo,
		Actionability: ActionabilityBackground,
		Summary:       "Test attention event",
		ReasonCode:    "test.event",
		Cursor:        NewCursorFixture(0),
		Clock:         NewFixedClock(0),
	}
}

// AttentionEventFixture creates a deterministic AttentionEvent for tests.
func AttentionEventFixture(opts AttentionEventFixtureOptions) AttentionEvent {
	cursor := opts.Cursor.Next()
	now := opts.Clock.Now()

	return AttentionEvent{
		Cursor:        cursor,
		Ts:            now.Format(time.RFC3339),
		Category:      opts.Category,
		Type:          opts.Type,
		Session:       opts.Session,
		Pane:          opts.Pane,
		Severity:      opts.Severity,
		Actionability: opts.Actionability,
		Summary:       opts.Summary,
		ReasonCode:    opts.ReasonCode,
		Details:       map[string]any{"test": true},
	}
}

// =============================================================================
// Incident Fixtures
// =============================================================================

// IncidentFixtureOptions configures incident fixture generation.
type IncidentFixtureOptions struct {
	ID           string
	Title        string
	Fingerprint  string
	Family       string
	Category     string
	Status       state.IncidentStatus
	Severity     state.Severity
	SessionNames []string
	AgentIDs     []string
	AlertCount   int
	EventCount   int
	Clock        *FixedClock
}

// DefaultIncidentFixtureOptions returns sensible defaults.
func DefaultIncidentFixtureOptions() IncidentFixtureOptions {
	return IncidentFixtureOptions{
		ID:           "inc-test-001",
		Title:        "Test incident: agent_stuck",
		Fingerprint:  "fp-test-001",
		Family:       "agent_health",
		Category:     "stuck",
		Status:       state.IncidentStatusOpen,
		Severity:     state.SeverityWarning,
		SessionNames: []string{"test-session"},
		AgentIDs:     []string{"test-session:1"},
		AlertCount:   1,
		EventCount:   3,
		Clock:        NewFixedClock(0),
	}
}

// IncidentFixture creates a deterministic Incident for tests.
func IncidentFixture(opts IncidentFixtureOptions) *state.Incident {
	now := opts.Clock.Now()

	// Serialize session names and agent IDs as JSON arrays
	sessionNamesJSON := "[]"
	if len(opts.SessionNames) > 0 {
		if data, err := json.Marshal(opts.SessionNames); err == nil {
			sessionNamesJSON = string(data)
		}
	}

	agentIDsJSON := "[]"
	if len(opts.AgentIDs) > 0 {
		if data, err := json.Marshal(opts.AgentIDs); err == nil {
			agentIDsJSON = string(data)
		}
	}

	return &state.Incident{
		ID:           opts.ID,
		Title:        opts.Title,
		Fingerprint:  opts.Fingerprint,
		Family:       opts.Family,
		Category:     opts.Category,
		Status:       opts.Status,
		Severity:     opts.Severity,
		SessionNames: sessionNamesJSON,
		AgentIDs:     agentIDsJSON,
		AlertCount:   opts.AlertCount,
		EventCount:   opts.EventCount,
		StartedAt:    now,
		LastEventAt:  now,
	}
}

// =============================================================================
// Request/Response Fixtures
// =============================================================================

// RequestFixtureOptions configures request fixture generation.
type RequestFixtureOptions struct {
	Command        string
	Session        string
	IdempotencyKey string
	CorrelationID  string
	Clock          *FixedClock
}

// DefaultRequestFixtureOptions returns sensible defaults.
func DefaultRequestFixtureOptions() RequestFixtureOptions {
	return RequestFixtureOptions{
		Command:        "robot-status",
		Session:        "",
		IdempotencyKey: "",
		CorrelationID:  "",
		Clock:          NewFixedClock(0),
	}
}

// RequestFixture represents a deterministic test request.
type RequestFixture struct {
	Command        string
	Session        string
	IdempotencyKey string
	CorrelationID  string
	RequestedAt    time.Time
}

// NewRequestFixture creates a deterministic request fixture.
func NewRequestFixture(opts RequestFixtureOptions) *RequestFixture {
	return &RequestFixture{
		Command:        opts.Command,
		Session:        opts.Session,
		IdempotencyKey: opts.IdempotencyKey,
		CorrelationID:  opts.CorrelationID,
		RequestedAt:    opts.Clock.Now(),
	}
}

// =============================================================================
// Operator Attention State Fixtures
// =============================================================================

// OperatorStateFixtureOptions configures operator attention state fixtures.
type OperatorStateFixtureOptions struct {
	Acknowledged   bool
	Snoozed        bool
	Pinned         bool
	SnoozeUntil    time.Duration
	AcknowledgedBy string
	Clock          *FixedClock
}

// DefaultOperatorStateFixtureOptions returns sensible defaults.
func DefaultOperatorStateFixtureOptions() OperatorStateFixtureOptions {
	return OperatorStateFixtureOptions{
		Acknowledged:   false,
		Snoozed:        false,
		Pinned:         false,
		SnoozeUntil:    0,
		AcknowledgedBy: "",
		Clock:          NewFixedClock(0),
	}
}

// OperatorStateFixture represents operator attention state for tests.
type OperatorStateFixture struct {
	Acknowledged   bool       `json:"acknowledged"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	Snoozed        bool       `json:"snoozed"`
	SnoozedUntil   *time.Time `json:"snoozed_until,omitempty"`
	Pinned         bool       `json:"pinned"`
	PinnedAt       *time.Time `json:"pinned_at,omitempty"`
}

// NewOperatorStateFixture creates a deterministic operator state fixture.
func NewOperatorStateFixture(opts OperatorStateFixtureOptions) *OperatorStateFixture {
	now := opts.Clock.Now()
	fixture := &OperatorStateFixture{
		Acknowledged:   opts.Acknowledged,
		AcknowledgedBy: opts.AcknowledgedBy,
		Snoozed:        opts.Snoozed,
		Pinned:         opts.Pinned,
	}

	if opts.Acknowledged {
		fixture.AcknowledgedAt = &now
	}
	if opts.Snoozed && opts.SnoozeUntil > 0 {
		snoozeUntil := now.Add(opts.SnoozeUntil)
		fixture.SnoozedUntil = &snoozeUntil
	}
	if opts.Pinned {
		fixture.PinnedAt = &now
	}

	return fixture
}

// =============================================================================
// Source Health Fixtures
// =============================================================================

// SourceHealthFixtureOptions configures source health fixtures.
type SourceHealthFixtureOptions struct {
	TmuxStatus      state.SourceStatus
	BeadsStatus     state.SourceStatus
	MailStatus      state.SourceStatus
	PtStatus        state.SourceStatus
	DegradedSources []string
	Clock           *FixedClock
}

// DefaultSourceHealthFixtureOptions returns all-healthy defaults.
func DefaultSourceHealthFixtureOptions() SourceHealthFixtureOptions {
	return SourceHealthFixtureOptions{
		TmuxStatus:      state.SourceStatusFresh,
		BeadsStatus:     state.SourceStatusFresh,
		MailStatus:      state.SourceStatusFresh,
		PtStatus:        state.SourceStatusFresh,
		DegradedSources: nil,
		Clock:           NewFixedClock(0),
	}
}

// SourceHealthFixture represents source health state for tests.
type SourceHealthFixture struct {
	TmuxStatus      state.SourceStatus `json:"tmux_status"`
	TmuxLastCheck   time.Time          `json:"tmux_last_check"`
	BeadsStatus     state.SourceStatus `json:"beads_status"`
	BeadsLastCheck  time.Time          `json:"beads_last_check"`
	MailStatus      state.SourceStatus `json:"mail_status"`
	MailLastCheck   time.Time          `json:"mail_last_check"`
	PtStatus        state.SourceStatus `json:"pt_status"`
	PtLastCheck     time.Time          `json:"pt_last_check"`
	DegradedSources []string           `json:"degraded_sources,omitempty"`
}

// NewSourceHealthFixture creates a deterministic source health fixture.
func NewSourceHealthFixture(opts SourceHealthFixtureOptions) *SourceHealthFixture {
	now := opts.Clock.Now()

	degraded := make([]string, 0)
	if opts.TmuxStatus != state.SourceStatusFresh {
		degraded = append(degraded, "tmux")
	}
	if opts.BeadsStatus != state.SourceStatusFresh {
		degraded = append(degraded, "beads")
	}
	if opts.MailStatus != state.SourceStatusFresh {
		degraded = append(degraded, "mail")
	}
	if opts.PtStatus != state.SourceStatusFresh {
		degraded = append(degraded, "pt")
	}
	if len(opts.DegradedSources) > 0 {
		degraded = opts.DegradedSources
	}

	return &SourceHealthFixture{
		TmuxStatus:      opts.TmuxStatus,
		TmuxLastCheck:   now,
		BeadsStatus:     opts.BeadsStatus,
		BeadsLastCheck:  now,
		MailStatus:      opts.MailStatus,
		MailLastCheck:   now,
		PtStatus:        opts.PtStatus,
		PtLastCheck:     now,
		DegradedSources: degraded,
	}
}

// =============================================================================
// Quota Fixtures
// =============================================================================

// QuotaFixtureOptions configures quota state fixtures.
type QuotaFixtureOptions struct {
	RateLimitRemaining int
	RateLimitLimit     int
	DailyUsed          int
	DailyLimit         int
	QuotaResetIn       time.Duration
	Exhausted          bool
	Clock              *FixedClock
}

// DefaultQuotaFixtureOptions returns healthy quota defaults.
func DefaultQuotaFixtureOptions() QuotaFixtureOptions {
	return QuotaFixtureOptions{
		RateLimitRemaining: 900,
		RateLimitLimit:     1000,
		DailyUsed:          5000,
		DailyLimit:         100000,
		QuotaResetIn:       4 * time.Hour,
		Exhausted:          false,
		Clock:              NewFixedClock(0),
	}
}

// QuotaFixture represents quota state for tests.
type QuotaFixture struct {
	RateLimitRemaining int       `json:"rate_limit_remaining"`
	RateLimitLimit     int       `json:"rate_limit_limit"`
	DailyUsed          int       `json:"daily_used"`
	DailyLimit         int       `json:"daily_limit"`
	QuotaResetAt       time.Time `json:"quota_reset_at"`
	Exhausted          bool      `json:"exhausted"`
	CheckedAt          time.Time `json:"checked_at"`
}

// NewQuotaFixture creates a deterministic quota fixture.
func NewQuotaFixture(opts QuotaFixtureOptions) *QuotaFixture {
	now := opts.Clock.Now()

	return &QuotaFixture{
		RateLimitRemaining: opts.RateLimitRemaining,
		RateLimitLimit:     opts.RateLimitLimit,
		DailyUsed:          opts.DailyUsed,
		DailyLimit:         opts.DailyLimit,
		QuotaResetAt:       now.Add(opts.QuotaResetIn),
		Exhausted:          opts.Exhausted,
		CheckedAt:          now,
	}
}

// =============================================================================
// Coordination Fixtures
// =============================================================================

// CoordinationFixtureOptions configures coordination state fixtures.
type CoordinationFixtureOptions struct {
	ActiveAgents     int
	PendingMessages  int
	FileReservations int
	Conflicts        int
	Clock            *FixedClock
}

// DefaultCoordinationFixtureOptions returns healthy coordination defaults.
func DefaultCoordinationFixtureOptions() CoordinationFixtureOptions {
	return CoordinationFixtureOptions{
		ActiveAgents:     3,
		PendingMessages:  0,
		FileReservations: 2,
		Conflicts:        0,
		Clock:            NewFixedClock(0),
	}
}

// CoordinationFixture represents coordination state for tests.
type CoordinationFixture struct {
	ActiveAgents         int       `json:"active_agents"`
	PendingMessages      int       `json:"pending_messages"`
	FileReservations     int       `json:"file_reservations"`
	ReservationConflicts int       `json:"reservation_conflicts"`
	LastSyncAt           time.Time `json:"last_sync_at"`
}

// NewCoordinationFixture creates a deterministic coordination fixture.
func NewCoordinationFixture(opts CoordinationFixtureOptions) *CoordinationFixture {
	now := opts.Clock.Now()

	return &CoordinationFixture{
		ActiveAgents:         opts.ActiveAgents,
		PendingMessages:      opts.PendingMessages,
		FileReservations:     opts.FileReservations,
		ReservationConflicts: opts.Conflicts,
		LastSyncAt:           now,
	}
}

// =============================================================================
// Outcome Fixtures
// =============================================================================

// OutcomeFixtureOptions configures actuation outcome fixtures.
type OutcomeFixtureOptions struct {
	Success          bool
	Command          string
	AffectedPanes    []string
	ErrorCode        string
	ErrorMessage     string
	RemediationHint  string
	DiagnosticHandle string
	DurationMs       int64
	Clock            *FixedClock
}

// DefaultOutcomeFixtureOptions returns successful outcome defaults.
func DefaultOutcomeFixtureOptions() OutcomeFixtureOptions {
	return OutcomeFixtureOptions{
		Success:          true,
		Command:          "send",
		AffectedPanes:    []string{"1", "2"},
		ErrorCode:        "",
		ErrorMessage:     "",
		RemediationHint:  "",
		DiagnosticHandle: "",
		DurationMs:       150,
		Clock:            NewFixedClock(0),
	}
}

// OutcomeFixture represents an actuation outcome for tests.
type OutcomeFixture struct {
	Success          bool      `json:"success"`
	Command          string    `json:"command"`
	AffectedPanes    []string  `json:"affected_panes"`
	ErrorCode        string    `json:"error_code,omitempty"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	RemediationHint  string    `json:"remediation_hint,omitempty"`
	DiagnosticHandle string    `json:"diagnostic_handle,omitempty"`
	DurationMs       int64     `json:"duration_ms"`
	CompletedAt      time.Time `json:"completed_at"`
}

// NewOutcomeFixture creates a deterministic outcome fixture.
func NewOutcomeFixture(opts OutcomeFixtureOptions) *OutcomeFixture {
	now := opts.Clock.Now()

	return &OutcomeFixture{
		Success:          opts.Success,
		Command:          opts.Command,
		AffectedPanes:    opts.AffectedPanes,
		ErrorCode:        opts.ErrorCode,
		ErrorMessage:     opts.ErrorMessage,
		RemediationHint:  opts.RemediationHint,
		DiagnosticHandle: opts.DiagnosticHandle,
		DurationMs:       opts.DurationMs,
		CompletedAt:      now,
	}
}

// =============================================================================
// Composite Scenario Fixtures
// =============================================================================

// ScenarioFixture bundles related fixtures for a complete test scenario.
type ScenarioFixture struct {
	ID           ScenarioID
	Clock        *FixedClock
	Cursor       *CursorFixture
	Session      *state.RuntimeSession
	Agents       []*state.RuntimeAgent
	Incidents    []*state.Incident
	SourceHealth *SourceHealthFixture
	Quota        *QuotaFixture
	Coordination *CoordinationFixture
}

// NewScenarioFixture creates a complete scenario with all fixtures.
func NewScenarioFixture(testName string, seed int64) *ScenarioFixture {
	id := NewScenarioID(testName, seed)
	clock := NewFixedClock(seed)
	cursor := NewCursorFixture(seed)

	sessionOpts := DefaultSessionFixtureOptions()
	sessionOpts.Clock = clock

	agents := make([]*state.RuntimeAgent, 3)
	for i := 0; i < 3; i++ {
		agentOpts := DefaultAgentFixtureOptions()
		agentOpts.Pane = i + 1
		agentOpts.Clock = clock
		agents[i] = RuntimeAgentFixture(agentOpts)
	}

	incidentOpts := DefaultIncidentFixtureOptions()
	incidentOpts.Clock = clock

	sourceHealthOpts := DefaultSourceHealthFixtureOptions()
	sourceHealthOpts.Clock = clock

	quotaOpts := DefaultQuotaFixtureOptions()
	quotaOpts.Clock = clock

	coordOpts := DefaultCoordinationFixtureOptions()
	coordOpts.Clock = clock

	return &ScenarioFixture{
		ID:           id,
		Clock:        clock,
		Cursor:       cursor,
		Session:      RuntimeSessionFixture(sessionOpts),
		Agents:       agents,
		Incidents:    []*state.Incident{IncidentFixture(incidentOpts)},
		SourceHealth: NewSourceHealthFixture(sourceHealthOpts),
		Quota:        NewQuotaFixture(quotaOpts),
		Coordination: NewCoordinationFixture(coordOpts),
	}
}
