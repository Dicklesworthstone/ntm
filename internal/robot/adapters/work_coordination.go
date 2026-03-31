package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

const (
	defaultWorkItemLimit             = 10
	defaultThreadStaleAfter          = 24 * time.Hour
	defaultReservationExpiringWithin = time.Hour
	defaultConflictWindow            = 30 * time.Minute
	defaultMailBacklogThreshold      = 5
	defaultSafePreviewLimit          = 120
)

// WorkSection normalizes beads and bv state into one stable projection shape.
type WorkSection struct {
	Ready      []WorkItem   `json:"ready"`
	Blocked    []WorkItem   `json:"blocked"`
	InProgress []WorkItem   `json:"in_progress"`
	Summary    *WorkSummary `json:"summary,omitempty"`
	Triage     *WorkTriage  `json:"triage,omitempty"`
	Graph      *WorkGraph   `json:"graph,omitempty"`
	Available  bool         `json:"available"`
	Reason     string       `json:"reason,omitempty"`
}

// WorkItem is a normalized bead record suitable for robot surfaces.
type WorkItem struct {
	ID              string              `json:"id"`
	Title           string              `json:"title"`
	TitleDisclosure *DisclosureMetadata `json:"title_disclosure,omitempty"`
	Priority        int                 `json:"priority,omitempty"`
	Type            string              `json:"type,omitempty"`
	Labels          []string            `json:"labels,omitempty"`
	Assignee        string              `json:"assignee,omitempty"`
	BlockedBy       []string            `json:"blocked_by,omitempty"`
	Unblocks        int                 `json:"unblocks,omitempty"`
	Score           *float64            `json:"score,omitempty"`
	UpdatedAt       string              `json:"updated_at,omitempty"`
}

// WorkSummary captures aggregate bead state without exposing raw tool output.
type WorkSummary struct {
	Total      int `json:"total"`
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Closed     int `json:"closed"`
	Ready      int `json:"ready"`
	Blocked    int `json:"blocked"`
}

// WorkTriage captures the high-value guidance from bv triage.
type WorkTriage struct {
	TopRecommendation *WorkRecommendation `json:"top_recommendation,omitempty"`
	ReadyCount        int                 `json:"ready_count"`
	QuickWinsCount    int                 `json:"quick_wins_count"`
	BlockersCount     int                 `json:"blockers_count"`
}

// WorkRecommendation is the normalized shape surfaces consume from bv triage.
type WorkRecommendation struct {
	ID              string              `json:"id"`
	Title           string              `json:"title"`
	TitleDisclosure *DisclosureMetadata `json:"title_disclosure,omitempty"`
	Priority        int                 `json:"priority"`
	Score           float64             `json:"score"`
	Reasons         []string            `json:"reasons,omitempty"`
	Unblocks        int                 `json:"unblocks"`
}

// WorkGraph summarizes graph-level dependency health.
type WorkGraph struct {
	TotalNodes int     `json:"total_nodes"`
	TotalEdges int     `json:"total_edges"`
	CycleCount int     `json:"cycle_count"`
	MaxDepth   int     `json:"max_depth"`
	Density    float64 `json:"density,omitempty"`
}

// CoordinationSection normalizes mail, reservation, conflict, and handoff state.
type CoordinationSection struct {
	Mail         *MailSummary          `json:"mail,omitempty"`
	Threads      *ThreadsSummary       `json:"threads,omitempty"`
	Reservations *ReservationsSummary  `json:"reservations,omitempty"`
	Handoff      *HandoffSummary       `json:"handoff,omitempty"`
	Problems     []CoordinationProblem `json:"problems,omitempty"`
	Available    bool                  `json:"available"`
	Reason       string                `json:"reason,omitempty"`
}

// MailSummary captures unread and pending coordination load.
type MailSummary struct {
	TotalUnread   int                       `json:"total_unread"`
	UrgentUnread  int                       `json:"urgent_unread"`
	PendingAck    int                       `json:"pending_ack"`
	ByAgent       map[string]AgentMailStats `json:"by_agent,omitempty"`
	LatestMessage string                    `json:"latest_message,omitempty"`
}

// AgentMailStats captures per-agent inbox load.
type AgentMailStats struct {
	Unread                  int                 `json:"unread"`
	Pending                 int                 `json:"pending"`
	Urgent                  int                 `json:"urgent"`
	Pane                    string              `json:"pane,omitempty"`
	LatestMessage           string              `json:"latest_message,omitempty"`
	LatestSubject           string              `json:"latest_subject,omitempty"`
	LatestSubjectDisclosure *DisclosureMetadata `json:"latest_subject_disclosure,omitempty"`
	LatestPreview           string              `json:"latest_preview,omitempty"`
	LatestPreviewDisclosure *DisclosureMetadata `json:"latest_preview_disclosure,omitempty"`
}

// ThreadsSummary captures recent message thread activity.
type ThreadsSummary struct {
	Active     int      `json:"active"`
	Stale      int      `json:"stale"`
	TopThreads []string `json:"top_threads,omitempty"`
}

// ReservationsSummary captures live file reservation pressure.
type ReservationsSummary struct {
	Active    int            `json:"active"`
	Expiring  int            `json:"expiring"`
	Conflicts int            `json:"conflicts"`
	ByAgent   map[string]int `json:"by_agent,omitempty"`
}

// HandoffSummary captures the latest handoff context surfaces should understand.
type HandoffSummary struct {
	Session            string               `json:"session,omitempty"`
	Status             string               `json:"status,omitempty"`
	Goal               string               `json:"goal,omitempty"`
	GoalDisclosure     *DisclosureMetadata  `json:"goal_disclosure,omitempty"`
	Now                string               `json:"now,omitempty"`
	NowDisclosure      *DisclosureMetadata  `json:"now_disclosure,omitempty"`
	UpdatedAt          string               `json:"updated_at,omitempty"`
	ActiveBeads        []string             `json:"active_beads,omitempty"`
	AgentMailThreads   []string             `json:"agent_mail_threads,omitempty"`
	Blockers           []string             `json:"blockers,omitempty"`
	BlockerDisclosures []DisclosureMetadata `json:"blocker_disclosures,omitempty"`
	Files              []string             `json:"files,omitempty"`
}

// DisclosureMetadata reports how a free-text field was normalized for safe surfaces.
// disclosure_state uses the existing repo vocabulary: visible, redacted, preview_only, withheld.
type DisclosureMetadata struct {
	DisclosureState string `json:"disclosure_state,omitempty"`
	Preview         string `json:"preview,omitempty"`
	RedactionMode   string `json:"redaction_mode,omitempty"`
	Findings        int    `json:"findings,omitempty"`
}

// CoordinationProblem captures coordination issues that deserve operator attention.
type CoordinationProblem struct {
	Kind      string   `json:"kind"`
	Severity  string   `json:"severity"`
	Summary   string   `json:"summary"`
	Agents    []string `json:"agents,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	ThreadIDs []string `json:"thread_ids,omitempty"`
}

// ReservationConflict is the adapter-local shape for overlapping file reservations.
type ReservationConflict struct {
	ID         string              `json:"id"`
	FilePath   string              `json:"file_path,omitempty"`
	Pattern    string              `json:"pattern,omitempty"`
	Holders    []ReservationHolder `json:"holders,omitempty"`
	DetectedAt time.Time           `json:"detected_at"`
}

// ReservationHolder is the adapter-local shape for a conflicting reservation owner.
type ReservationHolder struct {
	AgentName  string    `json:"agent_name"`
	ReservedAt time.Time `json:"reserved_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Reason     string    `json:"reason,omitempty"`
}

// WorkInputs supplies raw beads/bv data to NormalizeWork.
type WorkInputs struct {
	Summary    *bv.BeadsSummary
	Ready      []bv.BeadPreview
	Blocked    []bv.BeadPreview
	InProgress []bv.BeadInProgress
	Triage     *bv.TriageResponse
}

// CoordinationInputs supplies raw coordination data to NormalizeCoordination.
type CoordinationInputs struct {
	InboxByAgent              map[string][]agentmail.InboxMessage
	Reservations              []agentmail.FileReservation
	ReservationConflicts      []ReservationConflict
	FileConflicts             []tracker.Conflict
	Handoff                   *handoff.Handoff
	ThreadStaleAfter          time.Duration
	ReservationExpiringWithin time.Duration
	MailBacklogThreshold      int
	Now                       time.Time
	Reason                    string
}

// WorkCoordinationAdapterConfig controls live work/coordination collection.
type WorkCoordinationAdapterConfig struct {
	ProjectDir                string
	SessionName               string
	WorkItemLimit             int
	ThreadStaleAfter          time.Duration
	ReservationExpiringWithin time.Duration
	ConflictWindow            time.Duration
	MailBacklogThreshold      int
	AgentMailClient           *agentmail.Client
}

// DefaultWorkCoordinationAdapterConfig returns conservative defaults.
func DefaultWorkCoordinationAdapterConfig(projectDir string) WorkCoordinationAdapterConfig {
	return WorkCoordinationAdapterConfig{
		ProjectDir:                projectDir,
		WorkItemLimit:             defaultWorkItemLimit,
		ThreadStaleAfter:          defaultThreadStaleAfter,
		ReservationExpiringWithin: defaultReservationExpiringWithin,
		ConflictWindow:            defaultConflictWindow,
		MailBacklogThreshold:      defaultMailBacklogThreshold,
	}
}

// WorkCoordinationAdapter collects work and coordination state from live sources.
type WorkCoordinationAdapter struct {
	config  WorkCoordinationAdapterConfig
	lastErr error
}

// NewWorkCoordinationAdapter constructs a live work/coordination adapter.
func NewWorkCoordinationAdapter(config WorkCoordinationAdapterConfig) *WorkCoordinationAdapter {
	if config.WorkItemLimit <= 0 {
		config.WorkItemLimit = defaultWorkItemLimit
	}
	if config.ThreadStaleAfter <= 0 {
		config.ThreadStaleAfter = defaultThreadStaleAfter
	}
	if config.ReservationExpiringWithin <= 0 {
		config.ReservationExpiringWithin = defaultReservationExpiringWithin
	}
	if config.ConflictWindow <= 0 {
		config.ConflictWindow = defaultConflictWindow
	}
	if config.MailBacklogThreshold <= 0 {
		config.MailBacklogThreshold = defaultMailBacklogThreshold
	}
	return &WorkCoordinationAdapter{config: config}
}

// Name returns the adapter identifier.
func (a *WorkCoordinationAdapter) Name() string {
	return "work_coordination"
}

// Available returns true when at least one work/coordination source should be readable.
func (a *WorkCoordinationAdapter) Available(ctx context.Context) bool {
	_ = ctx
	if a.config.ProjectDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(a.config.ProjectDir, ".beads")); err == nil {
		return true
	}
	if client := a.mailClient(); client != nil && client.IsAvailable() {
		return true
	}
	if handoff, _, err := handoffReaderForProject(a.config.ProjectDir).FindLatestAny(); err == nil && handoff != nil {
		return true
	}
	return false
}

// Collect gathers the live work and coordination sections.
func (a *WorkCoordinationAdapter) Collect(ctx context.Context) (*SignalBatch, error) {
	now := time.Now()
	work := a.collectWork()
	coordination := a.collectCoordination(ctx, now)

	batch := &SignalBatch{
		Source:       a.Name(),
		CollectedAt:  now,
		Work:         work,
		Coordination: coordination,
	}

	if (work == nil || !work.Available) && (coordination == nil || !coordination.Available) {
		a.lastErr = fmt.Errorf("work/coordination sources unavailable")
		return batch, a.lastErr
	}

	a.lastErr = nil
	return batch, nil
}

// LastError returns the most recent collection failure.
func (a *WorkCoordinationAdapter) LastError() error {
	return a.lastErr
}

func (a *WorkCoordinationAdapter) collectWork() *WorkSection {
	triage, _ := bv.GetTriage(a.config.ProjectDir)
	summary := bv.GetBeadsSummary(a.config.ProjectDir, a.config.WorkItemLimit)
	return NormalizeWork(WorkInputs{
		Summary:    summary,
		Ready:      readyItems(summary),
		Blocked:    bv.GetBlockedList(a.config.ProjectDir, a.config.WorkItemLimit),
		InProgress: inProgressItems(summary),
		Triage:     triage,
	})
}

func (a *WorkCoordinationAdapter) collectCoordination(ctx context.Context, now time.Time) *CoordinationSection {
	inputs := CoordinationInputs{
		FileConflicts:             tracker.DetectConflictsRecent(a.config.ConflictWindow),
		Handoff:                   latestHandoff(a.config.ProjectDir, a.config.SessionName),
		ThreadStaleAfter:          a.config.ThreadStaleAfter,
		ReservationExpiringWithin: a.config.ReservationExpiringWithin,
		MailBacklogThreshold:      a.config.MailBacklogThreshold,
		Now:                       now,
	}

	client := a.mailClient()
	if client == nil || !client.IsAvailable() {
		inputs.Reason = "agent mail unavailable"
		return NormalizeCoordination(inputs)
	}

	agents, err := client.ListProjectAgents(ctx, a.config.ProjectDir)
	if err != nil {
		inputs.Reason = fmt.Sprintf("list_agents failed: %v", err)
	} else {
		inputs.InboxByAgent = make(map[string][]agentmail.InboxMessage)
		for _, agent := range agents {
			if agent.Name == "HumanOverseer" {
				continue
			}
			inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey:    a.config.ProjectDir,
				AgentName:     agent.Name,
				Limit:         25,
				IncludeBodies: true,
			})
			if err != nil {
				continue
			}
			inputs.InboxByAgent[agent.Name] = inbox
		}
	}

	if reservations, err := client.ListReservations(ctx, a.config.ProjectDir, "", true); err == nil {
		inputs.Reservations = reservations
		inputs.ReservationConflicts = deriveReservationConflicts(reservations, now)
	}

	return NormalizeCoordination(inputs)
}

func (a *WorkCoordinationAdapter) mailClient() *agentmail.Client {
	if a.config.AgentMailClient != nil {
		return a.config.AgentMailClient
	}
	if strings.TrimSpace(a.config.ProjectDir) == "" {
		return nil
	}
	return agentmail.NewClient(agentmail.WithProjectKey(a.config.ProjectDir))
}

// NewWorkSection returns an initialized work section.
func NewWorkSection() *WorkSection {
	return &WorkSection{
		Ready:      []WorkItem{},
		Blocked:    []WorkItem{},
		InProgress: []WorkItem{},
		Available:  false,
	}
}

// NewCoordinationSection returns an initialized coordination section.
func NewCoordinationSection() *CoordinationSection {
	return &CoordinationSection{
		Problems:  []CoordinationProblem{},
		Available: false,
	}
}

// NormalizeWork converts beads/bv data into a stable projection section.
func NormalizeWork(inputs WorkInputs) *WorkSection {
	section := NewWorkSection()

	recByID := make(map[string]bv.TriageRecommendation)
	if inputs.Triage != nil {
		for _, rec := range inputs.Triage.Triage.Recommendations {
			recByID[rec.ID] = rec
		}
		for _, rec := range inputs.Triage.Triage.QuickWins {
			if _, exists := recByID[rec.ID]; !exists {
				recByID[rec.ID] = rec
			}
		}
	}

	for _, preview := range inputs.Ready {
		section.Ready = append(section.Ready, workItemFromPreview(preview, recByID[preview.ID]))
	}
	for _, preview := range inputs.Blocked {
		section.Blocked = append(section.Blocked, workItemFromPreview(preview, recByID[preview.ID]))
	}
	for _, item := range inputs.InProgress {
		section.InProgress = append(section.InProgress, workItemFromInProgress(item, recByID[item.ID]))
	}

	if inputs.Summary != nil {
		section.Summary = &WorkSummary{
			Total:      inputs.Summary.Total,
			Open:       inputs.Summary.Open,
			InProgress: inputs.Summary.InProgress,
			Closed:     inputs.Summary.Closed,
			Ready:      inputs.Summary.Ready,
			Blocked:    inputs.Summary.Blocked,
		}
		if inputs.Summary.Available {
			section.Available = true
		} else if inputs.Summary.Reason != "" {
			section.Reason = inputs.Summary.Reason
		}
	}

	if inputs.Triage != nil {
		section.Available = true
		section.Triage = &WorkTriage{
			ReadyCount:     inputs.Triage.Triage.QuickRef.ActionableCount,
			QuickWinsCount: len(inputs.Triage.Triage.QuickWins),
			BlockersCount:  len(inputs.Triage.Triage.BlockersToClear),
		}
		if len(inputs.Triage.Triage.Recommendations) > 0 {
			section.Triage.TopRecommendation = workRecommendationFromTriage(inputs.Triage.Triage.Recommendations[0])
		}
		if metrics := graphMetricsForWork(inputs.Triage); metrics != nil {
			section.Graph = metrics
		}
	}

	if !section.Available && (len(section.Ready) > 0 || len(section.Blocked) > 0 || len(section.InProgress) > 0) {
		section.Available = true
	}
	if !section.Available && section.Reason == "" {
		section.Reason = "work data unavailable"
	}

	return section
}

// NormalizeCoordination converts coordination inputs into a shared projection section.
func NormalizeCoordination(inputs CoordinationInputs) *CoordinationSection {
	section := NewCoordinationSection()
	now := inputs.Now
	if now.IsZero() {
		now = time.Now()
	}

	threadStaleAfter := inputs.ThreadStaleAfter
	if threadStaleAfter <= 0 {
		threadStaleAfter = defaultThreadStaleAfter
	}
	reservationExpiringWithin := inputs.ReservationExpiringWithin
	if reservationExpiringWithin <= 0 {
		reservationExpiringWithin = defaultReservationExpiringWithin
	}
	mailBacklogThreshold := inputs.MailBacklogThreshold
	if mailBacklogThreshold <= 0 {
		mailBacklogThreshold = defaultMailBacklogThreshold
	}

	if len(inputs.InboxByAgent) > 0 {
		section.Mail, section.Threads = summarizeMail(inputs.InboxByAgent, now, threadStaleAfter)
		section.Available = true
	}

	reservationConflicts := inputs.ReservationConflicts
	if len(reservationConflicts) == 0 && len(inputs.Reservations) > 0 {
		reservationConflicts = deriveReservationConflicts(inputs.Reservations, now)
	}
	if len(inputs.Reservations) > 0 || len(reservationConflicts) > 0 {
		section.Reservations = summarizeReservations(inputs.Reservations, reservationConflicts, now, reservationExpiringWithin)
		section.Available = true
	}

	if inputs.Handoff != nil {
		section.Handoff = summarizeHandoff(inputs.Handoff)
		section.Available = true
	}

	section.Problems = collectCoordinationProblems(section.Mail, section.Threads, section.Reservations, reservationConflicts, inputs.FileConflicts, section.Handoff, mailBacklogThreshold)
	if len(section.Problems) > 0 {
		section.Available = true
	}

	if !section.Available {
		section.Reason = firstNonEmpty(inputs.Reason, "coordination data unavailable")
	} else if inputs.Reason != "" {
		section.Reason = inputs.Reason
	}

	return section
}

func summarizeMail(inboxByAgent map[string][]agentmail.InboxMessage, now time.Time, threadStaleAfter time.Duration) (*MailSummary, *ThreadsSummary) {
	mail := &MailSummary{
		ByAgent: make(map[string]AgentMailStats),
	}
	type threadActivity struct {
		key     string
		count   int
		lastAt  time.Time
		subject string
	}
	threadsByKey := make(map[string]*threadActivity)

	for agentName, inbox := range inboxByAgent {
		stats := AgentMailStats{}
		var latestAt time.Time
		for _, msg := range inbox {
			if msg.AckRequired {
				stats.Pending++
				mail.PendingAck++
			}
			if msg.ReadAt == nil {
				stats.Unread++
				mail.TotalUnread++
				if strings.EqualFold(msg.Importance, "urgent") {
					stats.Urgent++
					mail.UrgentUnread++
				}
			}
			if msg.CreatedTS.After(parseTimestamp(mail.LatestMessage)) {
				mail.LatestMessage = FormatTimestamp(msg.CreatedTS.Time)
			}
			if msg.CreatedTS.After(latestAt) {
				latestAt = msg.CreatedTS.Time
				stats.LatestMessage = FormatTimestamp(latestAt)
				stats.LatestSubject, stats.LatestSubjectDisclosure = NormalizeDisclosureText(msg.Subject)
				previewSource := strings.TrimSpace(msg.BodyMD)
				if previewSource == "" {
					previewSource = msg.Subject
				}
				stats.LatestPreview, stats.LatestPreviewDisclosure = NormalizeDisclosureText(previewSource)
			}

			threadKey := fmt.Sprintf("msg:%d", msg.ID)
			if msg.ThreadID != nil && *msg.ThreadID != "" {
				threadKey = *msg.ThreadID
			}
			activity := threadsByKey[threadKey]
			if activity == nil {
				subject, _ := NormalizeDisclosureText(msg.Subject)
				activity = &threadActivity{key: threadKey, subject: subject}
				threadsByKey[threadKey] = activity
			}
			activity.count++
			if msg.CreatedTS.After(activity.lastAt) {
				activity.lastAt = msg.CreatedTS.Time
				if strings.TrimSpace(msg.Subject) != "" {
					subject, _ := NormalizeDisclosureText(msg.Subject)
					activity.subject = subject
				}
			}
		}
		mail.ByAgent[agentName] = stats
	}

	threads := &ThreadsSummary{}
	if len(threadsByKey) == 0 {
		return mail, threads
	}

	list := make([]threadActivity, 0, len(threadsByKey))
	for _, activity := range threadsByKey {
		if activity.lastAt.IsZero() {
			activity.lastAt = now
		}
		list = append(list, *activity)
		threads.Active++
		if now.Sub(activity.lastAt) > threadStaleAfter {
			threads.Stale++
		}
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].lastAt.After(list[j].lastAt)
	})
	for _, item := range list {
		threads.TopThreads = append(threads.TopThreads, item.key)
		if len(threads.TopThreads) >= 5 {
			break
		}
	}

	return mail, threads
}

func summarizeReservations(reservations []agentmail.FileReservation, conflicts []ReservationConflict, now time.Time, expiringWithin time.Duration) *ReservationsSummary {
	summary := &ReservationsSummary{
		ByAgent: make(map[string]int),
	}
	for _, reservation := range reservations {
		if reservation.ReleasedTS != nil || now.After(reservation.ExpiresTS.Time) {
			continue
		}
		summary.Active++
		summary.ByAgent[reservation.AgentName]++
		if reservation.ExpiresTS.Sub(now) <= expiringWithin {
			summary.Expiring++
		}
	}
	summary.Conflicts = len(conflicts)
	return summary
}

func summarizeHandoff(h *handoff.Handoff) *HandoffSummary {
	if h == nil {
		return nil
	}
	files := append([]string{}, h.Files.Created...)
	files = append(files, h.Files.Modified...)
	files = append(files, h.Files.Deleted...)
	sort.Strings(files)
	goal, goalDisclosure := normalizeFreeText(h.Goal)
	nowText, nowDisclosure := normalizeFreeText(h.Now)
	blockers, blockerDisclosures := normalizeFreeTextList(h.Blockers)
	return &HandoffSummary{
		Session:            h.Session,
		Status:             h.Status,
		Goal:               goal,
		GoalDisclosure:     goalDisclosure,
		Now:                nowText,
		NowDisclosure:      nowDisclosure,
		UpdatedAt:          FormatTimestamp(h.UpdatedAt),
		ActiveBeads:        append([]string{}, h.ActiveBeads...),
		AgentMailThreads:   append([]string{}, h.AgentMailThreads...),
		Blockers:           blockers,
		BlockerDisclosures: blockerDisclosures,
		Files:              files,
	}
}

func collectCoordinationProblems(mail *MailSummary, threads *ThreadsSummary, reservations *ReservationsSummary, reservationConflicts []ReservationConflict, fileConflicts []tracker.Conflict, handoff *HandoffSummary, mailBacklogThreshold int) []CoordinationProblem {
	var problems []CoordinationProblem

	if mail != nil {
		if mail.UrgentUnread > 0 {
			problems = append(problems, CoordinationProblem{
				Kind:     "urgent_mail",
				Severity: string(SeverityError),
				Summary:  fmt.Sprintf("%d urgent unread message(s)", mail.UrgentUnread),
				Agents:   busyMailAgents(mail.ByAgent, func(stats AgentMailStats) bool { return stats.Urgent > 0 }),
			})
		}
		if mail.PendingAck > 0 {
			problems = append(problems, CoordinationProblem{
				Kind:     "pending_ack",
				Severity: string(SeverityWarning),
				Summary:  fmt.Sprintf("%d message(s) awaiting acknowledgement", mail.PendingAck),
				Agents:   busyMailAgents(mail.ByAgent, func(stats AgentMailStats) bool { return stats.Pending > 0 }),
			})
		}
		if mail.TotalUnread >= mailBacklogThreshold {
			problems = append(problems, CoordinationProblem{
				Kind:     "mail_backlog",
				Severity: string(SeverityWarning),
				Summary:  fmt.Sprintf("%d unread message(s) across the swarm", mail.TotalUnread),
				Agents:   busyMailAgents(mail.ByAgent, func(stats AgentMailStats) bool { return stats.Unread > 0 }),
			})
		}
	}

	for _, conflict := range reservationConflicts {
		problems = append(problems, CoordinationProblem{
			Kind:     "reservation_conflict",
			Severity: string(SeverityWarning),
			Summary:  firstNonEmpty(conflict.Pattern, conflict.FilePath),
			Agents:   holderNames(conflict.Holders),
			Paths:    nonEmptyStrings(conflict.Pattern, conflict.FilePath),
		})
	}

	for _, conflict := range fileConflicts {
		severity := string(SeverityWarning)
		if strings.EqualFold(conflict.Severity, "critical") {
			severity = string(SeverityError)
		}
		problems = append(problems, CoordinationProblem{
			Kind:     "file_conflict",
			Severity: severity,
			Summary:  fmt.Sprintf("file conflict on %s", conflict.Path),
			Agents:   append([]string{}, conflict.Agents...),
			Paths:    nonEmptyStrings(conflict.Path),
		})
	}

	if handoff != nil && (strings.EqualFold(handoff.Status, "blocked") || len(handoff.Blockers) > 0) {
		problems = append(problems, CoordinationProblem{
			Kind:     "handoff_blocked",
			Severity: string(SeverityWarning),
			Summary:  fmt.Sprintf("handoff for %s is blocked", firstNonEmpty(handoff.Session, "latest session")),
			Paths:    append([]string{}, handoff.Files...),
		})
	}

	sort.Slice(problems, func(i, j int) bool {
		left := coordinationProblemRank(problems[i].Severity)
		right := coordinationProblemRank(problems[j].Severity)
		if left != right {
			return left > right
		}
		return problems[i].Summary < problems[j].Summary
	})

	return problems
}

func workItemFromPreview(preview bv.BeadPreview, rec bv.TriageRecommendation) WorkItem {
	title, disclosure := normalizeFreeText(preview.Title)
	item := WorkItem{
		ID:              preview.ID,
		Title:           title,
		TitleDisclosure: disclosure,
		Priority:        parsePriorityLabel(preview.Priority),
	}
	if rec.ID == "" {
		return item
	}
	item.Type = rec.Type
	item.Labels = append([]string{}, rec.Labels...)
	item.BlockedBy = append([]string{}, rec.BlockedBy...)
	item.Unblocks = len(rec.UnblocksIDs)
	item.Score = floatPointer(rec.Score)
	if item.Title == "" {
		item.Title, item.TitleDisclosure = normalizeFreeText(rec.Title)
	}
	if item.Priority == 0 {
		item.Priority = rec.Priority
	}
	return item
}

func workItemFromInProgress(item bv.BeadInProgress, rec bv.TriageRecommendation) WorkItem {
	title, disclosure := normalizeFreeText(item.Title)
	workItem := WorkItem{
		ID:              item.ID,
		Title:           title,
		TitleDisclosure: disclosure,
		Assignee:        item.Assignee,
	}
	if !item.UpdatedAt.IsZero() {
		workItem.UpdatedAt = FormatTimestamp(item.UpdatedAt)
	}
	if rec.ID == "" {
		return workItem
	}
	workItem.Type = rec.Type
	workItem.Labels = append([]string{}, rec.Labels...)
	workItem.BlockedBy = append([]string{}, rec.BlockedBy...)
	workItem.Unblocks = len(rec.UnblocksIDs)
	workItem.Score = floatPointer(rec.Score)
	if workItem.Title == "" {
		workItem.Title, workItem.TitleDisclosure = normalizeFreeText(rec.Title)
	}
	if workItem.Priority == 0 {
		workItem.Priority = rec.Priority
	}
	return workItem
}

func normalizeFreeText(raw string) (string, *DisclosureMetadata) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	cfg := redaction.DefaultConfig()
	cfg.Mode = redaction.ModeRedact
	result := redaction.ScanAndRedact(trimmed, cfg)

	preview, truncated := safePreview(result.Output)
	disclosure := &DisclosureMetadata{
		DisclosureState: "visible",
		Preview:         preview,
		RedactionMode:   string(cfg.Mode),
	}
	normalized := strings.TrimSpace(result.Output)
	if len(result.Findings) > 0 {
		disclosure.DisclosureState = "redacted"
		disclosure.Findings = len(result.Findings)
	} else if truncated {
		disclosure.DisclosureState = "preview_only"
		normalized = preview
	}

	return normalized, disclosure
}

// NormalizeDisclosureText applies the shared disclosure-control semantics used by
// normalized robot-visible free-text fields.
func NormalizeDisclosureText(raw string) (string, *DisclosureMetadata) {
	return normalizeFreeText(raw)
}

func normalizeFreeTextList(values []string) ([]string, []DisclosureMetadata) {
	if len(values) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(values))
	disclosures := make([]DisclosureMetadata, 0, len(values))
	for _, value := range values {
		safe, disclosure := normalizeFreeText(value)
		if safe == "" {
			continue
		}
		normalized = append(normalized, safe)
		if disclosure != nil {
			disclosures = append(disclosures, *disclosure)
		}
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, disclosures
}

func safePreview(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	runes := []rune(trimmed)
	if len(runes) <= defaultSafePreviewLimit {
		return trimmed, false
	}
	return string(runes[:defaultSafePreviewLimit-3]) + "...", true
}

func workRecommendationFromTriage(rec bv.TriageRecommendation) *WorkRecommendation {
	title, disclosure := normalizeFreeText(rec.Title)
	return &WorkRecommendation{
		ID:              rec.ID,
		Title:           title,
		TitleDisclosure: disclosure,
		Priority:        rec.Priority,
		Score:           rec.Score,
		Reasons:         append([]string{}, rec.Reasons...),
		Unblocks:        len(rec.UnblocksIDs),
	}
}

func graphMetricsForWork(triage *bv.TriageResponse) *WorkGraph {
	if triage == nil || triage.Triage.ProjectHealth == nil || triage.Triage.ProjectHealth.GraphMetrics == nil {
		return nil
	}
	metrics := triage.Triage.ProjectHealth.GraphMetrics
	return &WorkGraph{
		TotalNodes: metrics.TotalNodes,
		TotalEdges: metrics.TotalEdges,
		CycleCount: metrics.CycleCount,
		MaxDepth:   metrics.MaxDepth,
		Density:    metrics.Density,
	}
}

func deriveReservationConflicts(reservations []agentmail.FileReservation, now time.Time) []ReservationConflict {
	type aggregate struct {
		pattern string
		holders map[string]ReservationHolder
	}

	groups := make(map[string]*aggregate)
	for i := 0; i < len(reservations); i++ {
		for j := i + 1; j < len(reservations); j++ {
			left := reservations[i]
			right := reservations[j]
			if left.ReleasedTS != nil || right.ReleasedTS != nil {
				continue
			}
			if now.After(left.ExpiresTS.Time) || now.After(right.ExpiresTS.Time) {
				continue
			}
			if left.AgentName == right.AgentName {
				continue
			}
			if !left.Exclusive && !right.Exclusive {
				continue
			}
			if !reservationPatternsConflict(left.PathPattern, right.PathPattern) {
				continue
			}

			pattern := normalizeConflictPattern(left.PathPattern, right.PathPattern)
			group := groups[pattern]
			if group == nil {
				group = &aggregate{
					pattern: pattern,
					holders: make(map[string]ReservationHolder),
				}
				groups[pattern] = group
			}
			group.holders[left.AgentName] = ReservationHolder{
				AgentName:  left.AgentName,
				ReservedAt: left.CreatedTS.Time,
				ExpiresAt:  left.ExpiresTS.Time,
				Reason:     left.Reason,
			}
			group.holders[right.AgentName] = ReservationHolder{
				AgentName:  right.AgentName,
				ReservedAt: right.CreatedTS.Time,
				ExpiresAt:  right.ExpiresTS.Time,
				Reason:     right.Reason,
			}
		}
	}

	conflicts := make([]ReservationConflict, 0, len(groups))
	for _, group := range groups {
		conflict := ReservationConflict{
			ID:         group.pattern,
			Pattern:    group.pattern,
			DetectedAt: now,
		}
		for _, holder := range group.holders {
			conflict.Holders = append(conflict.Holders, holder)
		}
		sort.Slice(conflict.Holders, func(i, j int) bool {
			return conflict.Holders[i].AgentName < conflict.Holders[j].AgentName
		})
		conflicts = append(conflicts, conflict)
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Pattern < conflicts[j].Pattern
	})
	return conflicts
}

func reservationPatternsConflict(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return matchesPattern(a, b) || matchesPattern(b, a)
}

func normalizeConflictPattern(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return a
	}
	if a > b {
		a, b = b, a
	}
	return a + " <-> " + b
}

func readyItems(summary *bv.BeadsSummary) []bv.BeadPreview {
	if summary == nil {
		return nil
	}
	return summary.ReadyPreview
}

func inProgressItems(summary *bv.BeadsSummary) []bv.BeadInProgress {
	if summary == nil {
		return nil
	}
	return summary.InProgressList
}

func latestHandoff(projectDir, sessionName string) *handoff.Handoff {
	reader := handoffReaderForProject(projectDir)
	if strings.TrimSpace(sessionName) != "" {
		h, _, err := reader.FindLatest(sessionName)
		if err == nil {
			return h
		}
	}
	h, _, err := reader.FindLatestAny()
	if err != nil {
		return nil
	}
	return h
}

func handoffReaderForProject(projectDir string) *handoff.Reader {
	return handoff.NewReader(projectDir)
}

func parsePriorityLabel(label string) int {
	label = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(label), "P"))
	if label == "" {
		return 0
	}
	value, err := strconv.Atoi(label)
	if err != nil {
		return 0
	}
	return value
}

func floatPointer(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func coordinationProblemRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case string(SeverityCritical):
		return 4
	case string(SeverityError):
		return 3
	case string(SeverityWarning):
		return 2
	default:
		return 1
	}
}

func holderNames(holders []ReservationHolder) []string {
	names := make([]string, 0, len(holders))
	for _, holder := range holders {
		if strings.TrimSpace(holder.AgentName) == "" {
			continue
		}
		names = append(names, holder.AgentName)
	}
	sort.Strings(names)
	return names
}

func busyMailAgents(byAgent map[string]AgentMailStats, keep func(AgentMailStats) bool) []string {
	agents := make([]string, 0, len(byAgent))
	for agent, stats := range byAgent {
		if keep(stats) {
			agents = append(agents, agent)
		}
	}
	sort.Strings(agents)
	return agents
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseTimestamp(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
