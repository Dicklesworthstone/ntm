package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// AttentionCLIItem represents a surfaced attention event annotated with its
// durable operator state for `ntm attention list`.
type AttentionCLIItem struct {
	Cursor            int64      `json:"cursor"`
	ItemKey           string     `json:"item_key"`
	DedupKey          string     `json:"dedup_key,omitempty"`
	Session           string     `json:"session,omitempty"`
	Pane              string     `json:"pane,omitempty"`
	Category          string     `json:"category"`
	Type              string     `json:"type"`
	Severity          string     `json:"severity"`
	Actionability     string     `json:"actionability"`
	Summary           string     `json:"summary"`
	EventTimestamp    time.Time  `json:"event_timestamp"`
	State             string     `json:"state"`
	Pinned            bool       `json:"pinned"`
	Muted             bool       `json:"muted"`
	SnoozedUntil      *time.Time `json:"snoozed_until,omitempty"`
	OverridePriority  string     `json:"override_priority,omitempty"`
	ResurfacingPolicy string     `json:"resurfacing_policy,omitempty"`
}

// AttentionListResponse is the structured output of `ntm attention list`.
type AttentionListResponse struct {
	Success   bool               `json:"success"`
	Timestamp string             `json:"timestamp"`
	Count     int                `json:"count"`
	Items     []AttentionCLIItem `json:"items"`
}

// AttentionMutationResponse is the structured output of `ntm attention <action>`.
type AttentionMutationResponse struct {
	Success        bool                     `json:"success"`
	Timestamp      string                   `json:"timestamp"`
	Action         string                   `json:"action"`
	Ref            string                   `json:"ref"`
	Cursor         int64                    `json:"cursor,omitempty"`
	ItemKey        string                   `json:"item_key"`
	PreviousState  string                   `json:"previous_state"`
	CurrentState   string                   `json:"current_state"`
	AttentionState state.AttentionItemState `json:"attention_state"`
}

// AttentionIncidentsListResponse is the structured output of `ntm attention incidents [list]`.
type AttentionIncidentsListResponse struct {
	Success   bool             `json:"success"`
	Timestamp string           `json:"timestamp"`
	Count     int              `json:"count"`
	Incidents []state.Incident `json:"incidents"`
}

// AttentionIncidentShowResponse is the structured output of `ntm attention incidents show`.
type AttentionIncidentShowResponse struct {
	Success    bool                         `json:"success"`
	Timestamp  string                       `json:"timestamp"`
	Incident   *state.Incident              `json:"incident"`
	EventCount int                          `json:"event_count"`
	Events     []state.StoredAttentionEvent `json:"events"`
}

// AttentionIncidentMutationResponse is the structured output of `ntm attention incidents <action>`.
type AttentionIncidentMutationResponse struct {
	Success        bool            `json:"success"`
	Timestamp      string          `json:"timestamp"`
	Action         string          `json:"action"`
	IncidentID     string          `json:"incident_id"`
	PreviousStatus string          `json:"previous_status"`
	Status         string          `json:"status"`
	Idempotent     bool            `json:"idempotent,omitempty"`
	Note           string          `json:"note,omitempty"`
	Incident       *state.Incident `json:"incident"`
}

func newAttentionCmd() *cobra.Command {
	var robotOut bool
	var listSession string
	var listLimit int
	var listIncludeAll bool

	cmd := &cobra.Command{
		Use:   "attention",
		Short: "Inspect and manage operator attention state and durable incidents",
		Long: `Inspect and transition durable operator attention state and incidents.

Provides the human CLI companion to --robot-attention (docs/robot-attention-state.md):
  ntm attention list
  ntm attention ack <cursor-or-key>
  ntm attention snooze <cursor-or-key> --until +30m
  ntm attention dismiss <cursor-or-key>
  ntm attention pin <cursor-or-key>
  ntm attention unpin <cursor-or-key>
  ntm attention escalate <cursor-or-key> --priority critical
  ntm attention restore <cursor-or-key>
  ntm attention incidents list
  ntm attention incidents show <incident-id>
  ntm attention incidents investigate <incident-id>
  ntm attention incidents resolve <incident-id>
  ntm attention incidents mute <incident-id>`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionList(cmd, listSession, listLimit, listIncludeAll, jsonOutput || robotOut)
		},
	}

	cmd.PersistentFlags().BoolVar(&robotOut, "robot", false, "Emit machine-readable JSON output")
	cmd.Flags().StringVar(&listSession, "session", "", "Filter attention items by session name")
	cmd.Flags().IntVar(&listLimit, "limit", 50, "Maximum number of attention items to display")
	cmd.Flags().BoolVar(&listIncludeAll, "all", false, "Include dismissed and currently snoozed items")

	cmd.AddCommand(
		newAttentionListCmd(&robotOut),
		newAttentionActionCmd("ack", "Acknowledge an attention item", &robotOut),
		newAttentionSnoozeCmd(&robotOut),
		newAttentionActionCmd("dismiss", "Dismiss an attention item", &robotOut),
		newAttentionActionCmd("pin", "Pin an attention item to keep it elevated", &robotOut),
		newAttentionActionCmd("unpin", "Remove pin from an attention item", &robotOut),
		newAttentionEscalateCmd(&robotOut),
		newAttentionActionCmd("restore", "Restore a snoozed or dismissed attention item to new", &robotOut),
		newAttentionIncidentsCmd(&robotOut),
	)

	return cmd
}

func newAttentionListCmd(robotOut *bool) *cobra.Command {
	var session string
	var limit int
	var includeAll bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List surfaced attention items and their operator state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionList(cmd, session, limit, includeAll, jsonOutput || (robotOut != nil && *robotOut))
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Filter attention items by session name")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of attention items to display")
	cmd.Flags().BoolVar(&includeAll, "all", false, "Include dismissed and currently snoozed items")
	return cmd
}

func newAttentionActionCmd(action, short string, robotOut *bool) *cobra.Command {
	var reason string
	var policy string

	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <cursor-or-item-key>", action),
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionMutate(cmd, action, args[0], attentionMutateOpts{
				reason: reason,
				policy: policy,
			}, jsonOutput || (robotOut != nil && *robotOut))
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Optional operator reason recorded in the audit trail")
	cmd.Flags().StringVar(&policy, "policy", "", "Resurfacing policy override (on_change, never)")
	return cmd
}

func newAttentionSnoozeCmd(robotOut *bool) *cobra.Command {
	var until string
	var reason string
	var policy string

	cmd := &cobra.Command{
		Use:   "snooze <cursor-or-item-key>",
		Short: "Temporarily snooze an attention item until a future RFC3339 timestamp or duration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionMutate(cmd, "snooze", args[0], attentionMutateOpts{
				until:  until,
				reason: reason,
				policy: policy,
			}, jsonOutput || (robotOut != nil && *robotOut))
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "Future RFC3339 timestamp (e.g. 2026-11-12T23:00:00Z) or duration (e.g. 30m, +2h)")
	cmd.Flags().StringVar(&reason, "reason", "", "Optional reason for snoozing")
	cmd.Flags().StringVar(&policy, "policy", "", "Resurfacing policy override (on_change, never)")
	return cmd
}

func newAttentionEscalateCmd(robotOut *bool) *cobra.Command {
	var priority string
	var expires string
	var reason string

	cmd := &cobra.Command{
		Use:   "escalate <cursor-or-item-key>",
		Short: "Escalate an attention item with a priority override",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionMutate(cmd, "escalate", args[0], attentionMutateOpts{
				priority: priority,
				expires:  expires,
				reason:   reason,
			}, jsonOutput || (robotOut != nil && *robotOut))
		},
	}
	cmd.Flags().StringVar(&priority, "priority", "critical", "Priority override (critical, high)")
	cmd.Flags().StringVar(&expires, "expires", "", "Optional RFC3339 timestamp or duration when escalation expires")
	cmd.Flags().StringVar(&reason, "reason", "", "Optional reason for escalation")
	return cmd
}

type attentionMutateOpts struct {
	until    string
	priority string
	expires  string
	reason   string
	policy   string
}

func openAttentionStore() (*state.Store, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("migrate state store: %w", err)
	}
	return store, nil
}

func runAttentionList(cmd *cobra.Command, sessionFilter string, limit int, includeAll, emitJSON bool) error {
	store, err := openAttentionStore()
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	defer store.Close()

	if limit <= 0 {
		limit = 50
	}
	scanLimit := limit * 4
	if scanLimit < 100 {
		scanLimit = 100
	}

	sessionFilter = strings.TrimSpace(sessionFilter)
	events, err := store.GetRecentAttentionEvents(scanLimit, sessionFilter)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("load attention events: %w", err), "INTERNAL_ERROR", emitJSON)
	}

	cursors := make([]int64, 0, len(events))
	for _, ev := range events {
		cursors = append(cursors, ev.Cursor)
	}
	statesByCursor, err := store.GetAttentionItemStatesForCursors(cursors)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("load attention states: %w", err), "INTERNAL_ERROR", emitJSON)
	}

	now := time.Now().UTC()
	items := make([]AttentionCLIItem, 0, len(events))
	// GetRecentAttentionEvents already returns newest-first (ORDER BY cursor DESC),
	// bounded to limit after filtering dismissed/snoozed items.
	for i := 0; i < len(events) && len(items) < limit; i++ {
		ev := events[i]
		if sessionFilter != "" && ev.SessionName != sessionFilter {
			continue
		}
		st, hasState := statesByCursor[ev.Cursor]
		stateLabel := string(state.AttentionStateNew)
		if hasState && st.State != "" {
			stateLabel = string(st.State)
		}
		if stateLabel == string(state.AttentionStateSnoozed) && st.SnoozedUntil != nil && !st.SnoozedUntil.After(now) {
			stateLabel = string(state.AttentionStateNew)
		}
		if !includeAll && !st.Pinned {
			if stateLabel == string(state.AttentionStateDismissed) || stateLabel == string(state.AttentionStateSnoozed) {
				continue
			}
		}
		itemKey := st.ItemKey
		if itemKey == "" {
			if resolved, _ := store.ResolveAttentionItemKey(ev.Cursor); resolved != "" {
				itemKey = resolved
			} else {
				itemKey = fmt.Sprintf("cursor:%d", ev.Cursor)
			}
		}
		items = append(items, AttentionCLIItem{
			Cursor:            ev.Cursor,
			ItemKey:           itemKey,
			DedupKey:          ev.DedupKey,
			Session:           ev.SessionName,
			Pane:              ev.Pane,
			Category:          ev.Category,
			Type:              ev.EventType,
			Severity:          string(ev.Severity),
			Actionability:     string(ev.Actionability),
			Summary:           ev.Summary,
			EventTimestamp:    ev.Ts.UTC(),
			State:             stateLabel,
			Pinned:            st.Pinned,
			Muted:             st.Muted,
			SnoozedUntil:      st.SnoozedUntil,
			OverridePriority:  st.OverridePriority,
			ResurfacingPolicy: st.ResurfacingPolicy,
		})
	}

	resp := AttentionListResponse{
		Success:   true,
		Timestamp: now.Format(time.RFC3339),
		Count:     len(items),
		Items:     items,
	}
	if emitJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	out := cmd.OutOrStdout()
	if len(items) == 0 {
		fmt.Fprintln(out, "No active attention items.")
		return nil
	}
	fmt.Fprintf(out, "%-8s %-14s %-8s %-12s %-14s %s\n", "CURSOR", "STATE", "PINNED", "SEVERITY", "SESSION", "SUMMARY")
	for _, it := range items {
		sess := it.Session
		if sess == "" {
			sess = "-"
		}
		pin := "-"
		if it.Pinned {
			pin = "pinned"
		}
		fmt.Fprintf(out, "%-8d %-14s %-8s %-12s %-14s %s\n",
			it.Cursor, it.State, pin, it.Severity, sess, it.Summary)
	}
	return nil
}

func runAttentionMutate(cmd *cobra.Command, action, ref string, opts attentionMutateOpts, emitJSON bool) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return writeAttentionError(cmd, fmt.Errorf("attention reference is required"), "INVALID_FLAG", emitJSON)
	}

	store, err := openAttentionStore()
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	defer store.Close()

	var (
		cursor      int64
		itemKey     string
		dedupKey    string
		fingerprint string
		storedEvent *state.StoredAttentionEvent
	)

	if parsedCursor, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil && parsedCursor > 0 {
		cursor = parsedCursor
		evs, evErr := store.GetAttentionEventsSince(cursor-1, 1)
		if evErr != nil {
			return writeAttentionError(cmd, fmt.Errorf("load attention event %d: %w", cursor, evErr), "INTERNAL_ERROR", emitJSON)
		}
		if len(evs) == 0 || evs[0].Cursor != cursor {
			return writeAttentionError(cmd, fmt.Errorf("attention cursor %d not found", cursor), "NOT_FOUND", emitJSON)
		}
		ev := evs[0]
		storedEvent = &ev
		dedupKey = ev.DedupKey
		fingerprint = attentionFingerprintForCLI(ev)
		resolvedKey, keyErr := store.ResolveAttentionItemKey(cursor)
		if keyErr != nil {
			return writeAttentionError(cmd, keyErr, "INTERNAL_ERROR", emitJSON)
		}
		itemKey = resolvedKey
	} else {
		itemKey = ref
		dedupKey = ref
	}

	prior, err := store.GetAttentionItemState(itemKey)
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	if storedEvent == nil && prior == nil {
		// Check whether an attention event carries this dedup_key so cursor-less
		// refs against real events attach their latest cursor & fingerprint
		// regardless of how many earlier events exist in the store.
		if ev, evErr := store.GetLatestAttentionEventByDedupKey(ref); evErr == nil && ev != nil {
			storedEvent = ev
			cursor = ev.Cursor
			fingerprint = attentionFingerprintForCLI(*ev)
			if resolvedKey, _ := store.ResolveAttentionItemKey(cursor); resolvedKey != "" {
				itemKey = resolvedKey
				prior, _ = store.GetAttentionItemState(itemKey)
			}
		}
	}

	now := time.Now().UTC()
	next := state.AttentionItemState{
		ItemKey:           itemKey,
		DedupKey:          dedupKey,
		State:             state.AttentionStateNew,
		Fingerprint:       fingerprint,
		ResurfacingPolicy: "on_change",
	}
	prevLabel := string(state.AttentionStateNew)
	if prior != nil {
		next = *prior
		if next.State != "" {
			prevLabel = string(next.State)
		}
		if fingerprint != "" {
			next.Fingerprint = fingerprint
		}
		if next.ResurfacingPolicy == "" {
			next.ResurfacingPolicy = "on_change"
		}
	}

	actor := strings.TrimSpace(os.Getenv("AGENT_NAME"))
	if actor == "" {
		actor = strings.TrimSpace(os.Getenv("USER"))
	}
	if actor == "" {
		actor = "cli"
	}

	if err := applyCLIAttentionTransition(&next, action, opts, actor, now); err != nil {
		return writeAttentionError(cmd, err, "INVALID_FLAG", emitJSON)
	}

	prevBytes, _ := json.Marshal(prior)
	nextBytes, _ := json.Marshal(next)

	if err := store.Transaction(func(tx *state.Tx) error {
		if err := tx.UpsertAttentionItemState(&next); err != nil {
			return err
		}
		sev := state.SeverityInfo
		if storedEvent != nil && storedEvent.Severity != "" {
			sev = storedEvent.Severity
		}
		evID, err := tx.RecordAuditEvent(&state.AuditEvent{
			Ts:              now,
			ActorType:       "user",
			ActorID:         actor,
			ActorOrigin:     "cli",
			Category:        "attention",
			EventType:       action,
			Severity:        sev,
			EntityType:      "attention_item",
			EntityID:        itemKey,
			PreviousState:   string(prevBytes),
			NewState:        string(nextBytes),
			ChangeSummary:   fmt.Sprintf("%s attention item %s", action, itemKey),
			Reason:          opts.reason,
			DisclosureState: "visible",
			RetentionClass:  state.RetentionClassStandard,
		})
		if err != nil {
			return err
		}
		_, err = tx.RecordAuditDecision(&state.AuditDecision{
			DecisionType: action,
			DecisionAt:   now,
			ActorType:    "user",
			ActorID:      actor,
			EntityType:   "attention_item",
			EntityID:     itemKey,
			Reason:       opts.reason,
			ExpiresAt:    next.OverrideExpiresAt,
			AuditEventID: &evID,
		})
		return err
	}); err != nil {
		return writeAttentionError(cmd, fmt.Errorf("persist attention state: %w", err), "INTERNAL_ERROR", emitJSON)
	}

	resp := AttentionMutationResponse{
		Success:        true,
		Timestamp:      now.Format(time.RFC3339),
		Action:         action,
		Ref:            ref,
		Cursor:         cursor,
		ItemKey:        itemKey,
		PreviousState:  prevLabel,
		CurrentState:   string(next.State),
		AttentionState: next,
	}
	if emitJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Attention item %s: %s -> %s (pinned=%v)\n", itemKey, prevLabel, next.State, next.Pinned)
	return nil
}

func applyCLIAttentionTransition(item *state.AttentionItemState, action string, opts attentionMutateOpts, actor string, now time.Time) error {
	if policy := strings.TrimSpace(opts.policy); policy != "" {
		item.ResurfacingPolicy = policy
	}
	switch action {
	case "ack", "acknowledge":
		item.State = state.AttentionStateAcknowledged
		item.AcknowledgedAt = &now
		item.AcknowledgedBy = actor
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "snooze":
		untilTime, err := parseAttentionFutureTime(opts.until, now)
		if err != nil {
			return fmt.Errorf("--until is required for snooze: %w", err)
		}
		item.State = state.AttentionStateSnoozed
		item.SnoozedUntil = &untilTime
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "dismiss":
		item.State = state.AttentionStateDismissed
		item.DismissedAt = &now
		item.DismissedBy = actor
		item.SnoozedUntil = nil
	case "pin":
		item.Pinned = true
		item.PinnedAt = &now
		item.PinnedBy = actor
	case "unpin":
		item.Pinned = false
		item.PinnedAt = nil
		item.PinnedBy = ""
	case "escalate":
		prio := strings.TrimSpace(opts.priority)
		if prio == "" {
			prio = "critical"
		}
		item.OverridePriority = prio
		item.OverrideReason = strings.TrimSpace(opts.reason)
		if strings.TrimSpace(opts.expires) != "" {
			expTime, err := parseAttentionFutureTime(opts.expires, now)
			if err != nil {
				return fmt.Errorf("invalid --expires: %w", err)
			}
			item.OverrideExpiresAt = &expTime
		} else {
			item.OverrideExpiresAt = nil
		}
	case "restore":
		item.State = state.AttentionStateNew
		item.AcknowledgedAt = nil
		item.AcknowledgedBy = ""
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	default:
		return fmt.Errorf("unsupported attention action %q", action)
	}
	return nil
}

func parseAttentionFutureTime(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("future RFC3339 timestamp or duration (e.g. +30m) is required")
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := parsed.UTC()
		if !utc.After(now) {
			return time.Time{}, fmt.Errorf("timestamp %s must be in the future", utc.Format(time.RFC3339))
		}
		return utc, nil
	}
	durRaw := strings.TrimPrefix(raw, "+")
	if d, err := time.ParseDuration(durRaw); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("duration %s must be positive", raw)
		}
		return now.Add(d).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q: pass RFC3339 (2026-11-12T23:00:00Z) or duration (+30m)", raw)
}

func attentionFingerprintForCLI(ev state.StoredAttentionEvent) string {
	payload := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s",
		ev.SessionName, ev.Pane, ev.Category, ev.EventType,
		ev.Source, ev.Actionability, ev.Severity, ev.Summary)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("fp-%x", sum[:8])
}

func writeAttentionError(cmd *cobra.Command, err error, code string, emitJSON bool) error {
	if emitJSON {
		payload := map[string]any{
			"success":    false,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"error":      err.Error(),
			"error_code": code,
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	return err
}

func newAttentionIncidentsCmd(robotOut *bool) *cobra.Command {
	var includeAll bool
	var limit int
	var statusFilter string

	cmd := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident"},
		Short:   "Inspect and transition durable operator incidents",
		Long: `List, inspect, investigate, resolve, and mute durable operator incidents.

Subcommands:
  ntm attention incidents list [--all] [--limit 50] [--status open]
  ntm attention incidents show <incident-id> [--events-limit 50]
  ntm attention incidents investigate <incident-id> [--reason ...] [--by operator]
  ntm attention incidents resolve <incident-id> [--reason ...] [--by operator]
  ntm attention incidents mute <incident-id> [--reason ...] [--by operator]`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionIncidentsList(cmd, includeAll, limit, statusFilter, jsonOutput || (robotOut != nil && *robotOut))
		},
	}

	cmd.Flags().BoolVar(&includeAll, "all", false, "Include resolved and muted incidents")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of incidents to display")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Filter incidents by status (open, investigating, resolved, muted)")

	cmd.AddCommand(
		newAttentionIncidentsListCmd(robotOut),
		newAttentionIncidentsShowCmd(robotOut),
		newAttentionIncidentsTransitionCmd("investigate", "Mark an incident as under investigation", state.IncidentStatusInvestigating, robotOut),
		newAttentionIncidentsTransitionCmd("resolve", "Resolve an incident (idempotent if already resolved)", state.IncidentStatusResolved, robotOut),
		newAttentionIncidentsTransitionCmd("mute", "Mute an incident", state.IncidentStatusMuted, robotOut),
	)

	return cmd
}

func newAttentionIncidentsListCmd(robotOut *bool) *cobra.Command {
	var includeAll bool
	var limit int
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List durable incidents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionIncidentsList(cmd, includeAll, limit, statusFilter, jsonOutput || (robotOut != nil && *robotOut))
		},
	}

	cmd.Flags().BoolVar(&includeAll, "all", false, "Include resolved and muted incidents")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of incidents to display")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Filter incidents by status (open, investigating, resolved, muted)")

	return cmd
}

func newAttentionIncidentsShowCmd(robotOut *bool) *cobra.Command {
	var eventsLimit int

	cmd := &cobra.Command{
		Use:   "show <incident-id>",
		Short: "Show metadata and linked events for an incident",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionIncidentShow(cmd, args[0], eventsLimit, jsonOutput || (robotOut != nil && *robotOut))
		},
	}

	cmd.Flags().IntVar(&eventsLimit, "events-limit", 50, "Maximum number of linked events to display")

	return cmd
}

func newAttentionIncidentsTransitionCmd(action, short string, targetStatus state.IncidentStatus, robotOut *bool) *cobra.Command {
	var reason string
	var by string

	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <incident-id>", action),
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttentionIncidentTransition(cmd, action, args[0], targetStatus, by, reason, jsonOutput || (robotOut != nil && *robotOut))
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Optional operator reason or resolution note")
	cmd.Flags().StringVar(&by, "by", "operator", "Actor recording the incident transition")

	return cmd
}

func runAttentionIncidentsList(cmd *cobra.Command, includeAll bool, limit int, statusFilter string, emitJSON bool) error {
	statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))
	switch statusFilter {
	case "", string(state.IncidentStatusOpen), string(state.IncidentStatusInvestigating), string(state.IncidentStatusResolved), string(state.IncidentStatusMuted):
	default:
		return writeAttentionError(cmd, fmt.Errorf("invalid --status %q (expected open, investigating, resolved, or muted)", statusFilter), "INVALID_FLAG", emitJSON)
	}

	store, err := openAttentionStore()
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	defer store.Close()

	if limit <= 0 {
		limit = 50
	}

	includeResolved := includeAll ||
		statusFilter == string(state.IncidentStatusResolved) ||
		statusFilter == string(state.IncidentStatusMuted)

	queryLimit := limit
	if statusFilter != "" {
		queryLimit = 0
	}

	raw, err := store.ListIncidents(includeResolved, queryLimit)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("list incidents: %w", err), "INTERNAL_ERROR", emitJSON)
	}

	incidents := make([]state.Incident, 0, len(raw))
	for _, inc := range raw {
		if statusFilter != "" && string(inc.Status) != statusFilter {
			continue
		}
		incidents = append(incidents, inc)
		if len(incidents) >= limit {
			break
		}
	}

	now := time.Now().UTC()
	resp := AttentionIncidentsListResponse{
		Success:   true,
		Timestamp: now.Format(time.RFC3339),
		Count:     len(incidents),
		Incidents: incidents,
	}
	if emitJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	out := cmd.OutOrStdout()
	if len(incidents) == 0 {
		fmt.Fprintln(out, "No matching incidents.")
		return nil
	}

	fmt.Fprintf(out, "%-22s %-14s %-10s %-7s %-7s %-20s %s\n",
		"ID", "STATUS", "SEVERITY", "ALERTS", "EVENTS", "STARTED", "TITLE")
	for _, inc := range incidents {
		fmt.Fprintf(out, "%-22s %-14s %-10s %-7d %-7d %-20s %s\n",
			inc.ID,
			inc.Status,
			inc.Severity,
			inc.AlertCount,
			inc.EventCount,
			inc.StartedAt.UTC().Format(time.RFC3339),
			inc.Title,
		)
	}
	return nil
}

func runAttentionIncidentShow(cmd *cobra.Command, id string, eventsLimit int, emitJSON bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return writeAttentionError(cmd, fmt.Errorf("incident ID is required"), "INVALID_FLAG", emitJSON)
	}

	store, err := openAttentionStore()
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	defer store.Close()

	incident, err := store.GetIncident(id)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("load incident %s: %w", id, err), "INTERNAL_ERROR", emitJSON)
	}
	if incident == nil {
		return writeAttentionError(cmd, fmt.Errorf("incident %q not found", id), "NOT_FOUND", emitJSON)
	}

	if eventsLimit <= 0 {
		eventsLimit = 50
	}
	events, err := store.GetEventsForIncident(id, eventsLimit)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("load events for incident %s: %w", id, err), "INTERNAL_ERROR", emitJSON)
	}
	if events == nil {
		events = []state.StoredAttentionEvent{}
	}

	now := time.Now().UTC()
	resp := AttentionIncidentShowResponse{
		Success:    true,
		Timestamp:  now.Format(time.RFC3339),
		Incident:   incident,
		EventCount: len(events),
		Events:     events,
	}
	if emitJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Incident: %s\n", incident.ID)
	fmt.Fprintf(out, "  Title:       %s\n", incident.Title)
	fmt.Fprintf(out, "  Status:      %s\n", incident.Status)
	fmt.Fprintf(out, "  Severity:    %s\n", incident.Severity)
	if incident.Family != "" {
		fmt.Fprintf(out, "  Family:      %s\n", incident.Family)
	}
	if incident.Category != "" {
		fmt.Fprintf(out, "  Category:    %s\n", incident.Category)
	}
	if incident.Fingerprint != "" {
		fmt.Fprintf(out, "  Fingerprint: %s\n", incident.Fingerprint)
	}
	fmt.Fprintf(out, "  Alerts:      %d\n", incident.AlertCount)
	fmt.Fprintf(out, "  Events:      %d\n", incident.EventCount)
	fmt.Fprintf(out, "  Started:     %s\n", incident.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(out, "  Last Event:  %s\n", incident.LastEventAt.UTC().Format(time.RFC3339))
	if incident.AcknowledgedBy != "" {
		fmt.Fprintf(out, "  Acked By:    %s\n", incident.AcknowledgedBy)
	}
	if incident.ResolvedBy != "" {
		fmt.Fprintf(out, "  Resolved By: %s\n", incident.ResolvedBy)
	}
	if incident.Resolution != "" {
		fmt.Fprintf(out, "  Resolution:  %s\n", incident.Resolution)
	}
	if incident.MutedBy != "" {
		fmt.Fprintf(out, "  Muted By:    %s\n", incident.MutedBy)
	}
	if incident.MutedReason != "" {
		fmt.Fprintf(out, "  Mute Reason: %s\n", incident.MutedReason)
	}
	if incident.Notes != "" {
		fmt.Fprintf(out, "  Notes:       %s\n", incident.Notes)
	}

	fmt.Fprintln(out)
	if len(events) == 0 {
		fmt.Fprintln(out, "Linked Events: none")
		return nil
	}
	fmt.Fprintf(out, "Linked Events (%d):\n", len(events))
	fmt.Fprintf(out, "  %-8s %-20s %-10s %-14s %s\n", "CURSOR", "TIMESTAMP", "SEVERITY", "TYPE", "SUMMARY")
	for _, ev := range events {
		fmt.Fprintf(out, "  %-8d %-20s %-10s %-14s %s\n",
			ev.Cursor,
			ev.Ts.UTC().Format(time.RFC3339),
			ev.Severity,
			ev.EventType,
			ev.Summary,
		)
	}
	return nil
}

func runAttentionIncidentTransition(cmd *cobra.Command, action, id string, targetStatus state.IncidentStatus, by, reason string, emitJSON bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return writeAttentionError(cmd, fmt.Errorf("incident ID is required"), "INVALID_FLAG", emitJSON)
	}
	actor := strings.TrimSpace(by)
	if actor == "" {
		actor = "operator"
	}
	reason = strings.TrimSpace(reason)

	store, err := openAttentionStore()
	if err != nil {
		return writeAttentionError(cmd, err, "INTERNAL_ERROR", emitJSON)
	}
	defer store.Close()

	prior, err := store.GetIncident(id)
	if err != nil {
		return writeAttentionError(cmd, fmt.Errorf("load incident %s: %w", id, err), "INTERNAL_ERROR", emitJSON)
	}
	if prior == nil {
		return writeAttentionError(cmd, fmt.Errorf("incident %q not found", id), "NOT_FOUND", emitJSON)
	}

	prevStatus := string(prior.Status)
	updated := prior
	idempotent := false
	note := reason

	if targetStatus == state.IncidentStatusResolved && prior.Status == state.IncidentStatusResolved {
		idempotent = true
		if note == "" {
			note = "already resolved (idempotent no-op)"
		}
	} else {
		if err := store.UpdateIncidentStatus(id, targetStatus, actor, reason); err != nil {
			return writeAttentionError(cmd, fmt.Errorf("update incident %s: %w", id, err), "INTERNAL_ERROR", emitJSON)
		}
		reloaded, reloadErr := store.GetIncident(id)
		if reloadErr != nil {
			return writeAttentionError(cmd, fmt.Errorf("reload incident %s: %w", id, reloadErr), "INTERNAL_ERROR", emitJSON)
		}
		if reloaded != nil {
			updated = reloaded
		}
	}

	now := time.Now().UTC()
	resp := AttentionIncidentMutationResponse{
		Success:        true,
		Timestamp:      now.Format(time.RFC3339),
		Action:         action,
		IncidentID:     id,
		PreviousStatus: prevStatus,
		Status:         string(updated.Status),
		Idempotent:     idempotent,
		Note:           note,
		Incident:       updated,
	}
	if emitJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	if idempotent {
		fmt.Fprintf(cmd.OutOrStdout(), "Incident %s: already %s (no-op)\n", id, updated.Status)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Incident %s: %s -> %s (by %s)\n", id, prevStatus, updated.Status, actor)
	}
	return nil
}
