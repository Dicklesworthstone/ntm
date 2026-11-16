package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	timelineSchemaID          = "ntm:robot:timeline:v1"
	timelineSchemaVersion     = "1.0.0"
	defaultRobotTimelineLimit = 20
)

// TimelineOptions configures --robot-timeline inspection and statistics.
type TimelineOptions struct {
	Session       string // session ID from --robot-timeline=SESSION, or "all" / "list" / "stats" / ""
	Action        string // "list", "show", "stats" (auto-inferred when empty)
	IncludeEvents bool   // --timeline-events
	Stats         bool   // --timeline-stats
	Limit         int    // --timeline-limit (default 20)
	BaseDir       string // optional explicit timeline directory override; defaults to state.GetDefaultTimelinePersister()
}

// TimelineEventStats contains aggregated statistics for one session timeline.
type TimelineEventStats struct {
	TotalEvents    int                         `json:"total_events"`
	UniqueAgents   int                         `json:"unique_agents"`
	AgentBreakdown map[string]int              `json:"agent_breakdown"`
	StateBreakdown map[state.TimelineState]int `json:"state_breakdown"`
	Duration       time.Duration               `json:"duration"`
	FirstEvent     time.Time                   `json:"first_event"`
	LastEvent      time.Time                   `json:"last_event"`
}

// TimelineAggregateStats contains overall storage and activity statistics across all persisted timelines.
type TimelineAggregateStats struct {
	TotalTimelines  int    `json:"total_timelines"`
	TotalEvents     int    `json:"total_events"`
	TotalAgents     int    `json:"total_agents"`
	TotalSize       int64  `json:"total_size_bytes"`
	CompressedCount int    `json:"compressed_count"`
	OldestTimeline  string `json:"oldest_timeline,omitempty"`
	NewestTimeline  string `json:"newest_timeline,omitempty"`
}

// TimelineOutput represents the structured JSON envelope for --robot-timeline.
type TimelineOutput struct {
	RobotResponse
	SchemaID       string                  `json:"schema_id"`
	SchemaVersion  string                  `json:"schema_version"`
	Action         string                  `json:"action"`
	Session        string                  `json:"session"`
	Timelines      []state.TimelineInfo    `json:"timelines"`
	TotalCount     int                     `json:"total_count"`
	Showing        int                     `json:"showing"`
	Info           *state.TimelineInfo     `json:"info,omitempty"`
	Events         []state.AgentEvent      `json:"events"`
	SessionStats   *TimelineEventStats     `json:"session_stats,omitempty"`
	AggregateStats *TimelineAggregateStats `json:"aggregate_stats,omitempty"`
	AgentHints     *AgentHints             `json:"_agent_hints,omitempty"`
}

// GetTimeline lists saved session timelines, inspects a session's timeline, or computes aggregate timeline statistics.
func GetTimeline(ctx context.Context, opts TimelineOptions) (*TimelineOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	rawSession := strings.TrimSpace(opts.Session)
	action := inferTimelineAction(opts, rawSession)
	sessionLabel := normalizeTimelineSessionLabel(rawSession, action)

	if err := ctx.Err(); err != nil {
		return newTimelineErrorOutput(action, sessionLabel, err, ErrCodeTimeout, "Retry --robot-timeline with an active context"), nil
	}

	switch action {
	case "list", "show", "stats":
	default:
		return newTimelineErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("unsupported --timeline-action %q (want list, show, or stats)", action),
			ErrCodeInvalidFlag,
			"Use --timeline-action=list, show, or stats",
		), nil
	}

	if opts.Limit < 0 {
		return newTimelineErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("--timeline-limit must be zero or greater (got %d)", opts.Limit),
			ErrCodeInvalidFlag,
			"Pass a non-negative --timeline-limit such as --timeline-limit=20",
		), nil
	}

	limit := opts.Limit
	if limit == 0 {
		limit = defaultRobotTimelineLimit
	}

	persister, err := resolveRobotTimelinePersister(opts.BaseDir)
	if err != nil {
		return newTimelineErrorOutput(
			action,
			sessionLabel,
			fmt.Errorf("failed to get timeline persister: %w", err),
			ErrCodeInternalError,
			"Check timeline storage directory permissions",
		), nil
	}

	switch action {
	case "list":
		timelines, err := persister.ListTimelines()
		if err != nil {
			return newTimelineErrorOutput(
				action,
				sessionLabel,
				fmt.Errorf("failed to list timelines: %w", err),
				ErrCodeInternalError,
				"Check timeline storage directory readability",
			), nil
		}
		if timelines == nil {
			timelines = []state.TimelineInfo{}
		}
		totalCount := len(timelines)
		showing := totalCount
		if showing > limit {
			timelines = timelines[:limit]
			showing = limit
		}
		out := &TimelineOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      timelineSchemaID,
			SchemaVersion: timelineSchemaVersion,
			Action:        action,
			Session:       sessionLabel,
			Timelines:     timelines,
			TotalCount:    totalCount,
			Showing:       showing,
			Events:        []state.AgentEvent{},
		}
		out.AgentHints = buildTimelineAgentHints(out, opts.IncludeEvents)
		return out, nil

	case "show":
		if isWildcardOrReservedTimelineSession(rawSession) {
			return newTimelineErrorOutput(
				action,
				sessionLabel,
				errors.New("--robot-timeline action 'show' requires a concrete session ID"),
				ErrCodeInvalidFlag,
				"Pass --robot-timeline=SESSION, or use --robot-timeline to list saved timelines",
			), nil
		}
		if !isSafeTimelineSessionID(rawSession) {
			return newTimelineErrorOutput(
				action,
				rawSession,
				fmt.Errorf("invalid session ID %q", rawSession),
				ErrCodeInvalidFlag,
				"Session ID cannot be '.' or '..' or contain path separators",
			), nil
		}

		info, err := persister.GetTimelineInfo(rawSession)
		if err != nil {
			return newTimelineErrorOutput(
				action,
				rawSession,
				fmt.Errorf("failed to get timeline info: %w", err),
				ErrCodeInternalError,
				"Check timeline storage directory readability",
			), nil
		}
		if info == nil {
			return newTimelineErrorOutput(
				action,
				rawSession,
				fmt.Errorf("timeline not found: %s", rawSession),
				ErrCodeSessionNotFound,
				"Run ntm --robot-timeline to see available saved session timelines",
			), nil
		}

		events := []state.AgentEvent{}
		var sessionStats *TimelineEventStats
		loadedEvents, loadErr := persister.LoadTimeline(rawSession)
		if loadErr != nil {
			return newTimelineErrorOutput(
				action,
				rawSession,
				fmt.Errorf("failed to load timeline: %w", loadErr),
				ErrCodeInternalError,
				"Inspect persisted timeline JSONL/gzip file integrity",
			), nil
		}
		if len(loadedEvents) > 0 {
			sessionStats = ComputeTimelineEventStats(loadedEvents)
			if opts.IncludeEvents {
				events = loadedEvents
			}
		}

		out := &TimelineOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      timelineSchemaID,
			SchemaVersion: timelineSchemaVersion,
			Action:        action,
			Session:       info.SessionID,
			Timelines:     []state.TimelineInfo{},
			TotalCount:    1,
			Showing:       1,
			Info:          info,
			Events:        events,
			SessionStats:  sessionStats,
		}
		out.AgentHints = buildTimelineAgentHints(out, opts.IncludeEvents)
		return out, nil

	case "stats":
		timelines, err := persister.ListTimelines()
		if err != nil {
			return newTimelineErrorOutput(
				action,
				sessionLabel,
				fmt.Errorf("failed to list timelines: %w", err),
				ErrCodeInternalError,
				"Check timeline storage directory readability",
			), nil
		}
		agg := computeAggregateTimelineStats(persister, timelines)
		out := &TimelineOutput{
			RobotResponse:  NewRobotResponse(true),
			SchemaID:       timelineSchemaID,
			SchemaVersion:  timelineSchemaVersion,
			Action:         action,
			Session:        sessionLabel,
			Timelines:      []state.TimelineInfo{},
			TotalCount:     agg.TotalTimelines,
			Showing:        0,
			Events:         []state.AgentEvent{},
			AggregateStats: agg,
		}
		out.AgentHints = buildTimelineAgentHints(out, opts.IncludeEvents)
		return out, nil
	}

	return newTimelineErrorOutput(action, sessionLabel, fmt.Errorf("unhandled action %q", action), ErrCodeInternalError, ""), nil
}

// PrintTimeline executes GetTimeline and writes the terminal JSON/TOON response.
func PrintTimeline(ctx context.Context, opts TimelineOptions) error {
	out, err := GetTimeline(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot timeline failed")
}

// ComputeTimelineEventStats computes per-agent and per-state transition counts and time span.
func ComputeTimelineEventStats(events []state.AgentEvent) *TimelineEventStats {
	if len(events) == 0 {
		return nil
	}

	first := events[0].Timestamp
	last := events[0].Timestamp
	agentBreakdown := make(map[string]int)
	stateBreakdown := make(map[state.TimelineState]int)
	agents := make(map[string]struct{})

	for _, ev := range events {
		if ev.Timestamp.Before(first) {
			first = ev.Timestamp
		}
		if ev.Timestamp.After(last) {
			last = ev.Timestamp
		}
		agents[ev.AgentID] = struct{}{}
		agentBreakdown[ev.AgentID]++
		stateBreakdown[ev.State]++
	}

	return &TimelineEventStats{
		TotalEvents:    len(events),
		UniqueAgents:   len(agents),
		AgentBreakdown: agentBreakdown,
		StateBreakdown: stateBreakdown,
		Duration:       last.Sub(first),
		FirstEvent:     first,
		LastEvent:      last,
	}
}

func computeAggregateTimelineStats(persister *state.TimelinePersister, timelines []state.TimelineInfo) *TimelineAggregateStats {
	agg := &TimelineAggregateStats{
		TotalTimelines: len(timelines),
	}
	if len(timelines) == 0 {
		return agg
	}

	sorted := make([]state.TimelineInfo, len(timelines))
	copy(sorted, timelines)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].ModifiedAt.Equal(sorted[j].ModifiedAt) {
			return sorted[i].ModifiedAt.Before(sorted[j].ModifiedAt)
		}
		return sorted[i].SessionID < sorted[j].SessionID
	})

	allAgents := make(map[string]struct{})
	for _, ti := range sorted {
		agg.TotalEvents += ti.EventCount
		agg.TotalSize += ti.Size
		if ti.Compressed {
			agg.CompressedCount++
		}
		events, err := persister.LoadTimeline(ti.SessionID)
		if err == nil {
			for _, ev := range events {
				allAgents[ev.AgentID] = struct{}{}
			}
		}
	}

	agg.TotalAgents = len(allAgents)
	agg.OldestTimeline = sorted[0].SessionID
	agg.NewestTimeline = sorted[len(sorted)-1].SessionID
	return agg
}

func resolveRobotTimelinePersister(baseDir string) (*state.TimelinePersister, error) {
	if trimmed := strings.TrimSpace(baseDir); trimmed != "" {
		return state.NewTimelinePersister(&state.TimelinePersistConfig{
			BaseDir: util.ExpandPath(trimmed),
		})
	}
	return state.GetDefaultTimelinePersister()
}

func inferTimelineAction(opts TimelineOptions, rawSession string) string {
	if explicit := strings.ToLower(strings.TrimSpace(opts.Action)); explicit != "" {
		return explicit
	}
	switch {
	case opts.Stats || strings.EqualFold(rawSession, "stats"):
		return "stats"
	case rawSession != "" && !isWildcardOrReservedTimelineSession(rawSession):
		return "show"
	default:
		return "list"
	}
}

func isWildcardOrReservedTimelineSession(session string) bool {
	trimmed := strings.TrimSpace(session)
	return trimmed == "" ||
		strings.EqualFold(trimmed, "all") ||
		strings.EqualFold(trimmed, "list") ||
		strings.EqualFold(trimmed, "stats") ||
		trimmed == "*"
}

func isSafeTimelineSessionID(session string) bool {
	trimmed := strings.TrimSpace(session)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return false
	}
	return !strings.ContainsAny(trimmed, `/\`)
}

func normalizeTimelineSessionLabel(rawSession, action string) string {
	trimmed := strings.TrimSpace(rawSession)
	if action == "list" || action == "stats" || isWildcardOrReservedTimelineSession(trimmed) {
		return "all"
	}
	return trimmed
}

func newTimelineErrorOutput(action, session string, err error, code, hint string) *TimelineOutput {
	if action == "" {
		action = "list"
	}
	if session == "" {
		session = "all"
	}
	return &TimelineOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		SchemaID:      timelineSchemaID,
		SchemaVersion: timelineSchemaVersion,
		Action:        action,
		Session:       session,
		Timelines:     []state.TimelineInfo{},
		Events:        []state.AgentEvent{},
	}
}

func buildTimelineAgentHints(out *TimelineOutput, includeEvents bool) *AgentHints {
	switch out.Action {
	case "list":
		if out.TotalCount == 0 {
			return &AgentHints{
				Summary: "No persisted session timelines found",
			}
		}
		hints := &AgentHints{
			Summary: fmt.Sprintf("Showing %d of %d saved session timeline(s)", out.Showing, out.TotalCount),
		}
		if len(out.Timelines) > 0 {
			hints.SuggestedActions = []RobotAction{
				{
					Action:   "inspect_timeline",
					Target:   out.Timelines[0].SessionID,
					Reason:   fmt.Sprintf("Inspect latest timeline with ntm --robot-timeline=%s --timeline-events", out.Timelines[0].SessionID),
					Priority: 1,
				},
			}
		}
		return hints

	case "show":
		evCount := 0
		agentCount := 0
		if out.Info != nil {
			evCount = out.Info.EventCount
			agentCount = out.Info.AgentCount
		}
		hints := &AgentHints{
			Summary: fmt.Sprintf("Timeline %q: %d event(s) across %d agent(s)", out.Session, evCount, agentCount),
		}
		if !includeEvents && evCount > 0 {
			hints.SuggestedActions = []RobotAction{
				{
					Action:   "include_timeline_events",
					Target:   out.Session,
					Reason:   fmt.Sprintf("Re-run with ntm --robot-timeline=%s --timeline-events to include full event rows", out.Session),
					Priority: 2,
				},
			}
		}
		return hints

	case "stats":
		if out.AggregateStats == nil || out.AggregateStats.TotalTimelines == 0 {
			return &AgentHints{
				Summary: "No persisted timelines recorded yet",
			}
		}
		return &AgentHints{
			Summary: fmt.Sprintf(
				"%d timeline(s), %d total event(s), %d unique agent(s), %d compressed",
				out.AggregateStats.TotalTimelines,
				out.AggregateStats.TotalEvents,
				out.AggregateStats.TotalAgents,
				out.AggregateStats.CompressedCount,
			),
		}
	}

	return nil
}
