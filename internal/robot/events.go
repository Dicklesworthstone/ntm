package robot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// --robot-events Command Implementation (br-kpvhy)
// =============================================================================

// EventsOptions configures the --robot-events command.
type EventsOptions struct {
	// SinceCursor is the cursor to replay from. 0 = from beginning.
	SinceCursor int64

	// Limit is the maximum number of events to return.
	Limit int

	// Session filters events to a specific session.
	Session string

	// Profile is the filter preset to use (operator, debug, minimal, alerts).
	// Explicit filters override profile defaults.
	Profile string

	// CategoryFilter restricts to specific event categories.
	CategoryFilter []string

	// ActionabilityFilter restricts to specific actionability levels.
	ActionabilityFilter []string

	// SeverityFilter restricts to specific severity levels.
	SeverityFilter []string
}

// EventsOutput is the JSON response for --robot-events.
type EventsOutput struct {
	RobotResponse

	// Events is the list of events matching the query.
	Events []AttentionEvent `json:"events"`

	// NextCursor is the cursor to use for the next request.
	// Use this as --since-cursor for the next call to continue from here.
	NextCursor int64 `json:"next_cursor"`

	// HasMore indicates if more events exist beyond limit.
	HasMore bool `json:"has_more"`

	// ReplayWindow describes the available cursor range.
	ReplayWindow *SnapshotReplayWindowInfo `json:"replay_window,omitempty"`
}

// PrintEvents outputs attention events since a cursor with optional filtering.
// This is the raw replay/feed surface for robot clients.
func PrintEvents(opts EventsOptions) error {
	feed := GetAttentionFeed()
	if feed == nil {
		return outputJSON(EventsOutput{
			RobotResponse: NewErrorResponse(
				errors.New("attention feed not initialized"),
				"FEED_UNAVAILABLE",
				"The attention feed service is not running",
			),
			Events: []AttentionEvent{},
		})
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	// Replay events from the feed
	events, newestCursor, err := feed.Replay(opts.SinceCursor, limit+1)
	if err != nil {
		var cursorErr *CursorExpiredError
		if errors.As(err, &cursorErr) {
			details := cursorErr.ToDetails()
			return outputJSON(EventsOutput{
				RobotResponse: NewErrorResponse(
					cursorErr,
					ErrCodeCursorExpired,
					details.ResyncCommand,
				),
				Events: []AttentionEvent{},
				ReplayWindow: &SnapshotReplayWindowInfo{
					Supported:       true,
					OldestCursor:    details.EarliestCursor,
					RetentionPeriod: details.RetentionPeriod,
					ResyncCommand:   details.ResyncCommand,
				},
			})
		}
		return outputJSON(EventsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, ""),
			Events:        []AttentionEvent{},
		})
	}

	// Apply filters
	filtered := filterEventsForRobot(events, opts)

	// Determine if there are more events
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}

	// Compute next cursor
	nextCursor := int64(0)
	if len(filtered) > 0 {
		nextCursor = filtered[len(filtered)-1].Cursor
	} else if newestCursor > 0 {
		nextCursor = newestCursor
	}

	// Build replay window info
	stats := feed.Stats()
	replayWindow := &SnapshotReplayWindowInfo{
		Supported:       true,
		OldestCursor:    stats.OldestCursor,
		LatestCursor:    stats.NewestCursor,
		RetentionPeriod: stats.RetentionPeriod.String(),
		ResyncCommand:   "ntm --robot-snapshot",
	}

	return outputJSON(EventsOutput{
		RobotResponse: RobotResponse{
			Success:      true,
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
			Version:      AttentionContractVersion,
			OutputFormat: "json",
		},
		Events:       filtered,
		NextCursor:   nextCursor,
		HasMore:      hasMore,
		ReplayWindow: replayWindow,
	})
}

// filterEventsForRobot applies profile-based and explicit filters.
// Profile filters use minimum thresholds; explicit filters are exact matches.
func filterEventsForRobot(events []AttentionEvent, opts EventsOptions) []AttentionEvent {
	// Check if any filters are active
	hasFilters := opts.Profile != "" || opts.Session != "" ||
		len(opts.CategoryFilter) > 0 || len(opts.ActionabilityFilter) > 0 ||
		len(opts.SeverityFilter) > 0

	if !hasFilters {
		return events
	}

	// Resolve profile filters (only if profile specified)
	var resolved ResolvedFilters
	if opts.Profile != "" {
		resolved = ResolveEffectiveFilters(opts.Profile, ProfileFilters{})
	}

	// Build filter sets for explicit filters (exact match)
	categorySet := toStringSetForEvents(opts.CategoryFilter)
	actionabilitySet := toStringSetForEvents(opts.ActionabilityFilter)
	severitySet := toStringSetForEvents(opts.SeverityFilter)

	filtered := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		// Apply profile filters (minimum thresholds)
		if opts.Profile != "" && !resolved.MatchesFilters(&event) {
			continue
		}

		// Apply explicit filters (exact match)
		if len(categorySet) > 0 && !categorySet[string(event.Category)] {
			continue
		}
		if len(actionabilitySet) > 0 && !actionabilitySet[string(event.Actionability)] {
			continue
		}
		if len(severitySet) > 0 && !severitySet[string(event.Severity)] {
			continue
		}
		if opts.Session != "" && event.Session != opts.Session {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

// toStringSetForEvents converts a slice of strings to a set (map[string]bool).
func toStringSetForEvents(strs []string) map[string]bool {
	if len(strs) == 0 {
		return nil
	}
	set := make(map[string]bool, len(strs))
	for _, s := range strs {
		set[s] = true
	}
	return set
}

func applyProfileToDigestOptions(profile string, opts AttentionDigestOptions) AttentionDigestOptions {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return opts
	}

	resolved := ResolveEffectiveFilters(profile, ProfileFilters{})
	if len(resolved.Categories) > 0 {
		opts.Categories = append([]EventCategory(nil), resolved.Categories...)
	}
	if len(resolved.ExcludeTypes) > 0 {
		opts.ExcludeTypes = append([]EventType(nil), resolved.ExcludeTypes...)
	}
	if resolved.MinSeverity != "" {
		opts.MinSeverity = resolved.MinSeverity
	}
	if resolved.MinActionability != "" {
		opts.MinActionability = resolved.MinActionability
	}
	return opts
}

func filterAttentionEventsByProfile(events []AttentionEvent, profile string) []AttentionEvent {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return events
	}

	resolved := ResolveEffectiveFilters(profile, ProfileFilters{})
	filtered := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		if resolved.MatchesFilters(&event) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func buildAttentionNextCommand(opts AttentionOptions, cursor int64) string {
	parts := []string{fmt.Sprintf("ntm --robot-attention --since-cursor=%d", cursor)}
	if opts.Session != "" {
		parts = append(parts, fmt.Sprintf("--session=%s", opts.Session))
	}
	if opts.Profile != "" {
		parts = append(parts, fmt.Sprintf("--profile=%s", opts.Profile))
	}
	if opts.Condition != "" && opts.Condition != WaitConditionAttention {
		parts = append(parts, fmt.Sprintf("--attention-condition=%s", opts.Condition))
	}
	return strings.Join(parts, " ")
}

// =============================================================================
// --robot-overlay (br-a6cmp)
// =============================================================================

var (
	overlayExecCommand      = exec.Command
	overlayBinaryPath       = tmux.BinaryPath
	overlayIsInstalled      = tmux.IsInstalled
	overlayInTmux           = tmux.InTmux
	overlaySessionExists    = tmux.SessionExists
	overlayCurrentSession   = tmux.GetCurrentSession
	overlayOSExecutable     = os.Executable
	overlayLaunchProbeDelay = 150 * time.Millisecond
)

// OverlayOptions configures the --robot-overlay command.
type OverlayOptions struct {
	Session string
	Cursor  int64
	NoWait  bool
}

// OverlayOutput reports the result of a --robot-overlay request.
type OverlayOutput struct {
	RobotResponse
	Session   string `json:"session"`
	Cursor    int64  `json:"cursor,omitempty"`
	NoWait    bool   `json:"no_wait"`
	Launched  bool   `json:"launched"`
	Dismissed bool   `json:"dismissed"`
	PID       int    `json:"pid,omitempty"`
}

func overlayPopupInnerCommand(ntmBin, session string, attentionCursor int64) string {
	parts := []string{
		"NTM_POPUP=1",
		tmux.ShellQuote(ntmBin),
		"dashboard",
		"--popup",
	}
	if attentionCursor > 0 {
		parts = append(parts, "--attention-cursor", strconv.FormatInt(attentionCursor, 10))
	}
	parts = append(parts, tmux.ShellQuote(session))
	return strings.Join(parts, " ")
}

func overlayPopupArgs(ntmBin, session string, attentionCursor int64) []string {
	return []string{
		"display-popup",
		"-E",
		"-w", "95%",
		"-h", "95%",
		overlayPopupInnerCommand(ntmBin, session, attentionCursor),
	}
}

func resolveOverlaySession(session string) string {
	session = strings.TrimSpace(session)
	if session != "" {
		return session
	}
	return strings.TrimSpace(overlayCurrentSession())
}

func newOverlayErrorOutput(opts OverlayOptions, err error, code, hint string) OverlayOutput {
	return OverlayOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		Session:       strings.TrimSpace(opts.Session),
		Cursor:        opts.Cursor,
		NoWait:        opts.NoWait,
	}
}

// PrintOverlay launches the dashboard overlay and returns JSON status.
func PrintOverlay(opts OverlayOptions) error {
	if opts.Cursor < 0 {
		return outputJSON(newOverlayErrorOutput(
			opts,
			fmt.Errorf("overlay cursor must be >= 0"),
			ErrCodeInvalidFlag,
			"Use --overlay-cursor with a non-negative event cursor",
		))
	}
	if !overlayIsInstalled() {
		return outputJSON(newOverlayErrorOutput(
			opts,
			fmt.Errorf("tmux not installed"),
			ErrCodeDependencyMissing,
			"Install tmux to use overlay popups",
		))
	}
	session := resolveOverlaySession(opts.Session)
	if session == "" {
		return outputJSON(newOverlayErrorOutput(
			opts,
			fmt.Errorf("session is required"),
			ErrCodeInvalidFlag,
			"Pass --overlay-session=<session> or run --robot-overlay inside the target tmux session",
		))
	}
	if !overlayInTmux() {
		return outputJSON(newOverlayErrorOutput(
			OverlayOptions{Session: session, Cursor: opts.Cursor, NoWait: opts.NoWait},
			fmt.Errorf("overlay requires an attached tmux client"),
			ErrCodeInternalError,
			"Run --robot-overlay from inside tmux so tmux can draw the popup",
		))
	}
	if !overlaySessionExists(session) {
		return outputJSON(newOverlayErrorOutput(
			OverlayOptions{Session: session, Cursor: opts.Cursor, NoWait: opts.NoWait},
			fmt.Errorf("session %q not found", session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		))
	}

	ntmBin, err := overlayOSExecutable()
	if err != nil || strings.TrimSpace(ntmBin) == "" {
		ntmBin = "ntm"
	}

	cmd := overlayExecCommand(overlayBinaryPath(), overlayPopupArgs(ntmBin, session, opts.Cursor)...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	output := OverlayOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		Cursor:        opts.Cursor,
		NoWait:        opts.NoWait,
	}

	if opts.NoWait {
		if err := cmd.Start(); err != nil {
			return outputJSON(newOverlayErrorOutput(
				OverlayOptions{Session: session, Cursor: opts.Cursor, NoWait: opts.NoWait},
				err,
				ErrCodeInternalError,
				"Check that tmux popup support is available in this client",
			))
		}

		waitCh := make(chan error, 1)
		go func() {
			waitCh <- cmd.Wait()
		}()

		select {
		case err := <-waitCh:
			if err != nil {
				return outputJSON(newOverlayErrorOutput(
					OverlayOptions{Session: session, Cursor: opts.Cursor, NoWait: opts.NoWait},
					err,
					ErrCodeInternalError,
					"Check that tmux popup support is available in this client",
				))
			}
		case <-time.After(overlayLaunchProbeDelay):
		}

		output.Launched = true
		if cmd.Process != nil {
			output.PID = cmd.Process.Pid
		}
		return outputJSON(output)
	}

	if err := cmd.Run(); err != nil {
		return outputJSON(newOverlayErrorOutput(
			OverlayOptions{Session: session, Cursor: opts.Cursor, NoWait: opts.NoWait},
			err,
			ErrCodeInternalError,
			"Check that tmux popup support is available in this client",
		))
	}

	output.Launched = true
	output.Dismissed = true
	return outputJSON(output)
}

// =============================================================================
// --robot-digest Command Implementation (br-6tzh9)
// =============================================================================

// DigestOptions configures the --robot-digest command.
type DigestOptions struct {
	// SinceCursor is the cursor to digest from. 0 = from beginning.
	SinceCursor int64

	// Session filters events to a specific session.
	Session string

	// Profile is the filter preset to use (operator, debug, minimal, alerts).
	// Explicit filters override profile defaults.
	Profile string

	// ActionRequiredLimit is the max action_required items to surface.
	ActionRequiredLimit int

	// InterestingLimit is the max interesting items to surface.
	InterestingLimit int

	// BackgroundLimit is the max background items to surface.
	BackgroundLimit int

	// IncludeTrace enables detailed decision tracing for debugging.
	IncludeTrace bool
}

// DigestResponse is the JSON response for --robot-digest.
// It wraps AttentionDigest with robot envelope and replay window metadata.
type DigestResponse struct {
	RobotResponse

	// CursorStart is the earliest cursor in this digest window.
	CursorStart int64 `json:"cursor_start"`

	// CursorEnd is the latest cursor included in this digest.
	// Use this as --since-cursor for the next call.
	CursorEnd int64 `json:"cursor_end"`

	// PeriodStart is the RFC3339 timestamp of the earliest event.
	PeriodStart string `json:"period_start,omitempty"`

	// PeriodEnd is the RFC3339 timestamp of the latest event.
	PeriodEnd string `json:"period_end,omitempty"`

	// EventCount is the total raw events that were processed.
	EventCount int `json:"event_count"`

	// ByCategory breaks down event counts by category.
	ByCategory map[EventCategory]int `json:"by_category"`

	// ByActionability breaks down event counts by actionability level.
	ByActionability map[Actionability]int `json:"by_actionability"`

	// Buckets contains the surfaced digest items by urgency.
	Buckets AttentionDigestBuckets `json:"buckets"`

	// Suppressed summarizes how much feed noise was filtered out.
	Suppressed AttentionDigestSuppression `json:"suppressed"`

	// Summary is a human-readable summary of the digest.
	Summary string `json:"summary"`

	// Trace contains detailed decision logs when IncludeTrace is enabled.
	Trace []AttentionDigestDecision `json:"trace,omitempty"`

	// ReplayWindow describes the available cursor range for follow-up commands.
	ReplayWindow *SnapshotReplayWindowInfo `json:"replay_window,omitempty"`
}

// PrintDigest outputs a token-efficient digest of what changed since a cursor.
// This is the default delta surface for operators that don't need raw event replay.
func PrintDigest(opts DigestOptions) error {
	feed := GetAttentionFeed()
	if feed == nil {
		return outputJSON(DigestResponse{
			RobotResponse: NewErrorResponse(
				errors.New("attention feed not initialized"),
				"FEED_UNAVAILABLE",
				"The attention feed service is not running",
			),
			ByCategory:      map[EventCategory]int{},
			ByActionability: map[Actionability]int{},
			Buckets: AttentionDigestBuckets{
				ActionRequired: []AttentionDigestItem{},
				Interesting:    []AttentionDigestItem{},
				Background:     []AttentionDigestItem{},
			},
			Suppressed: AttentionDigestSuppression{ByReason: map[string]int{}},
		})
	}

	// Build digest options from CLI options
	digestOpts := AttentionDigestOptions{
		Session:             opts.Session,
		ActionRequiredLimit: opts.ActionRequiredLimit,
		InterestingLimit:    opts.InterestingLimit,
		BackgroundLimit:     opts.BackgroundLimit,
		IncludeTrace:        opts.IncludeTrace,
	}
	digestOpts = applyProfileToDigestOptions(opts.Profile, digestOpts)

	// Apply defaults if not specified
	if digestOpts.ActionRequiredLimit <= 0 {
		digestOpts.ActionRequiredLimit = 5
	}
	if digestOpts.InterestingLimit <= 0 {
		digestOpts.InterestingLimit = 4
	}
	if digestOpts.BackgroundLimit <= 0 {
		digestOpts.BackgroundLimit = 3
	}

	// Build the digest
	digest, err := feed.Digest(opts.SinceCursor, digestOpts)
	if err != nil {
		var cursorErr *CursorExpiredError
		if errors.As(err, &cursorErr) {
			details := cursorErr.ToDetails()
			return outputJSON(DigestResponse{
				RobotResponse: NewErrorResponse(
					cursorErr,
					ErrCodeCursorExpired,
					details.ResyncCommand,
				),
				ByCategory:      map[EventCategory]int{},
				ByActionability: map[Actionability]int{},
				Buckets: AttentionDigestBuckets{
					ActionRequired: []AttentionDigestItem{},
					Interesting:    []AttentionDigestItem{},
					Background:     []AttentionDigestItem{},
				},
				Suppressed: AttentionDigestSuppression{ByReason: map[string]int{}},
				ReplayWindow: &SnapshotReplayWindowInfo{
					Supported:       true,
					OldestCursor:    details.EarliestCursor,
					RetentionPeriod: details.RetentionPeriod,
					ResyncCommand:   details.ResyncCommand,
				},
			})
		}
		return outputJSON(DigestResponse{
			RobotResponse:   NewErrorResponse(err, ErrCodeInternalError, ""),
			ByCategory:      map[EventCategory]int{},
			ByActionability: map[Actionability]int{},
			Buckets: AttentionDigestBuckets{
				ActionRequired: []AttentionDigestItem{},
				Interesting:    []AttentionDigestItem{},
				Background:     []AttentionDigestItem{},
			},
			Suppressed: AttentionDigestSuppression{ByReason: map[string]int{}},
		})
	}

	// Build replay window info
	stats := feed.Stats()
	replayWindow := &SnapshotReplayWindowInfo{
		Supported:       true,
		OldestCursor:    stats.OldestCursor,
		LatestCursor:    stats.NewestCursor,
		RetentionPeriod: stats.RetentionPeriod.String(),
		ResyncCommand:   "ntm --robot-snapshot",
	}

	return outputJSON(DigestResponse{
		RobotResponse: RobotResponse{
			Success:      true,
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
			Version:      AttentionContractVersion,
			OutputFormat: "json",
		},
		CursorStart:     digest.CursorStart,
		CursorEnd:       digest.CursorEnd,
		PeriodStart:     digest.PeriodStart,
		PeriodEnd:       digest.PeriodEnd,
		EventCount:      digest.EventCount,
		ByCategory:      digest.ByCategory,
		ByActionability: digest.ByActionability,
		Buckets:         digest.Buckets,
		Suppressed:      digest.Suppressed,
		Summary:         digest.Summary,
		Trace:           digest.Trace,
		ReplayWindow:    replayWindow,
	})
}

// =============================================================================
// --robot-attention Command Implementation (br-t540i)
// =============================================================================

// AttentionOptions configures the --robot-attention command.
// This is the one obvious tending primitive: wait for attention, then digest.
type AttentionOptions struct {
	// SinceCursor is the cursor to wait/digest from. Required for tending loops.
	SinceCursor int64

	// Session filters attention to a specific session.
	Session string

	// Timeout is how long to wait for attention before returning (default: 5m).
	Timeout time.Duration

	// PollInterval is how often to check for attention (default: 1s).
	PollInterval time.Duration

	// Condition specifies which attention conditions to wait for.
	// Default: "attention" (any action_required or interesting event).
	// Options: attention, action_required, mail_pending, mail_ack_required,
	//          context_hot, reservation_conflict, file_conflict, session_changed, pane_changed
	Condition string

	// Profile is the filter preset to use (operator, debug, minimal, alerts).
	// Explicit filters override profile defaults.
	Profile string

	// ActionRequiredLimit is the max action_required items in the digest.
	ActionRequiredLimit int

	// InterestingLimit is the max interesting items in the digest.
	InterestingLimit int

	// BackgroundLimit is the max background items in the digest.
	BackgroundLimit int

	// IncludeTrace enables detailed decision tracing for debugging.
	IncludeTrace bool
}

// AttentionResponse is the JSON response for --robot-attention.
// It combines wake information with a digest for the tending loop pattern.
type AttentionResponse struct {
	RobotResponse

	// WakeReason describes why attention returned (condition met or timeout).
	WakeReason string `json:"wake_reason"`

	// MatchedCondition is the specific condition that triggered the wake.
	// Empty if woke due to timeout.
	MatchedCondition string `json:"matched_condition,omitempty"`

	// TriggerEvent is the event that caused the wake (if applicable).
	TriggerEvent *AttentionEvent `json:"trigger_event,omitempty"`

	// WaitedSeconds is how long the command waited before returning.
	WaitedSeconds float64 `json:"waited_seconds"`

	// Digest contains the token-efficient summary of what changed.
	Digest *AttentionDigest `json:"digest"`

	// CursorInfo provides cursor handoff for the next tending iteration.
	CursorInfo AttentionCursorInfo `json:"cursor_info"`

	// ReplayWindow describes the available cursor range for follow-up commands.
	ReplayWindow *SnapshotReplayWindowInfo `json:"replay_window,omitempty"`
}

// AttentionCursorInfo provides cursor handoff for tending loops.
type AttentionCursorInfo struct {
	// StartCursor is the cursor this attention call started from.
	StartCursor int64 `json:"start_cursor"`

	// EndCursor is the cursor at the time of wake/timeout.
	// Use this as --since-cursor for the next --robot-attention call.
	EndCursor int64 `json:"end_cursor"`

	// OldestCursor is the oldest cursor still available in the feed.
	OldestCursor int64 `json:"oldest_cursor"`

	// NextCommand is a copy-paste ready command for the next iteration.
	NextCommand string `json:"next_command"`
}

// PrintAttention implements the --robot-attention command.
// This is the one obvious tending primitive: sleep until attention is needed,
// then wake with a compact digest. Returns exit code 0 on attention, 1 on timeout.
func PrintAttention(opts AttentionOptions) int {
	// Apply defaults
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 1 * time.Second
	}
	if opts.Condition == "" {
		opts.Condition = WaitConditionAttention
	}
	if opts.ActionRequiredLimit <= 0 {
		opts.ActionRequiredLimit = 5
	}
	if opts.InterestingLimit <= 0 {
		opts.InterestingLimit = 3
	}
	if opts.BackgroundLimit <= 0 {
		opts.BackgroundLimit = 0
	}

	feed := GetAttentionFeed()
	if feed == nil {
		outputJSON(AttentionResponse{
			RobotResponse: NewErrorResponse(
				errors.New("attention feed not initialized"),
				"FEED_UNAVAILABLE",
				"The attention feed service is not running",
			),
			WakeReason: "error",
			Digest:     &AttentionDigest{},
			CursorInfo: AttentionCursorInfo{StartCursor: opts.SinceCursor},
		})
		return 2
	}

	startTime := time.Now()
	deadline := startTime.Add(opts.Timeout)

	var wakeReason string
	var matchedCondition string
	var triggerEvent *AttentionEvent
	var finalCursor = opts.SinceCursor

	// Polling loop for attention conditions
	for {
		if time.Now().After(deadline) {
			wakeReason = "timeout"
			break
		}

		// Check for attention events since our cursor
		result := checkAttentionConditions(
			[]string{opts.Condition},
			opts.SinceCursor,
			opts.Session,
			opts.Profile,
		)
		if result != nil && result.CursorExpired != nil {
			cursorErr := result.CursorExpired
			details := cursorErr.ToDetails()
			outputJSON(AttentionResponse{
				RobotResponse: NewErrorResponse(
					cursorErr,
					ErrCodeCursorExpired,
					details.ResyncCommand,
				),
				WakeReason: "cursor_expired",
				Digest:     &AttentionDigest{},
				CursorInfo: AttentionCursorInfo{
					StartCursor:  opts.SinceCursor,
					OldestCursor: details.EarliestCursor,
					NextCommand:  details.ResyncCommand,
				},
			})
			return 2
		}

		if result != nil && result.Met {
			wakeReason = "attention"
			matchedCondition = result.Condition
			if result.TriggerEvent != nil {
				triggerEvent = result.TriggerEvent
			}
			finalCursor = result.NextCursor
			break
		}

		time.Sleep(opts.PollInterval)
	}

	waitedSeconds := time.Since(startTime).Seconds()

	// Build digest from the cursor window
	digestOpts := AttentionDigestOptions{
		Session:             opts.Session,
		ActionRequiredLimit: opts.ActionRequiredLimit,
		InterestingLimit:    opts.InterestingLimit,
		BackgroundLimit:     opts.BackgroundLimit,
		IncludeTrace:        opts.IncludeTrace,
	}
	digestOpts = applyProfileToDigestOptions(opts.Profile, digestOpts)

	digest, err := feed.Digest(opts.SinceCursor, digestOpts)
	if err != nil {
		var cursorErr *CursorExpiredError
		if errors.As(err, &cursorErr) {
			details := cursorErr.ToDetails()
			outputJSON(AttentionResponse{
				RobotResponse: NewErrorResponse(
					cursorErr,
					ErrCodeCursorExpired,
					details.ResyncCommand,
				),
				WakeReason: "cursor_expired",
				Digest:     &AttentionDigest{},
				CursorInfo: AttentionCursorInfo{
					StartCursor:  opts.SinceCursor,
					OldestCursor: details.EarliestCursor,
					NextCommand:  details.ResyncCommand,
				},
			})
			return 2
		}
		outputJSON(AttentionResponse{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, ""),
			WakeReason:    "error",
			Digest:        &AttentionDigest{},
			CursorInfo:    AttentionCursorInfo{StartCursor: opts.SinceCursor},
		})
		return 2
	}

	// Build replay window info
	stats := feed.Stats()
	resyncCmd := "ntm --robot-snapshot"
	if opts.Session != "" {
		resyncCmd = fmt.Sprintf("ntm --robot-snapshot --session=%s", opts.Session)
	}
	replayWindow := &SnapshotReplayWindowInfo{
		Supported:       true,
		OldestCursor:    stats.OldestCursor,
		LatestCursor:    stats.NewestCursor,
		EventCount:      stats.Count,
		RetentionPeriod: stats.RetentionPeriod.String(),
		ResyncCommand:   resyncCmd,
	}

	// Use digest end cursor if available, otherwise use feed's newest
	endCursor := digest.CursorEnd
	if endCursor == 0 && finalCursor > 0 {
		endCursor = finalCursor
	}
	if endCursor == 0 {
		endCursor = stats.NewestCursor
	}

	cursorInfo := AttentionCursorInfo{
		StartCursor:  opts.SinceCursor,
		EndCursor:    endCursor,
		OldestCursor: stats.OldestCursor,
		NextCommand:  buildAttentionNextCommand(opts, endCursor),
	}

	exitCode := 0
	if wakeReason == "timeout" {
		exitCode = 1
	}

	outputJSON(AttentionResponse{
		RobotResponse: RobotResponse{
			Success:      true,
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
			Version:      AttentionContractVersion,
			OutputFormat: "json",
		},
		WakeReason:       wakeReason,
		MatchedCondition: matchedCondition,
		TriggerEvent:     triggerEvent,
		WaitedSeconds:    waitedSeconds,
		Digest:           digest,
		CursorInfo:       cursorInfo,
		ReplayWindow:     replayWindow,
	})

	return exitCode
}
