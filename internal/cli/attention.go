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

func init() {
	rootCmd.AddCommand(newAttentionCmd())
}

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

func newAttentionCmd() *cobra.Command {
	var robotOut bool
	var listSession string
	var listLimit int
	var listIncludeAll bool

	cmd := &cobra.Command{
		Use:   "attention",
		Short: "Inspect and manage operator attention state (ack, snooze, dismiss, pin)",
		Long: `Inspect and transition durable operator attention state on surfaced events.

Provides the human CLI companion to --robot-attention (docs/robot-attention-state.md):
  ntm attention list
  ntm attention ack <cursor-or-key>
  ntm attention snooze <cursor-or-key> --until +30m
  ntm attention dismiss <cursor-or-key>
  ntm attention pin <cursor-or-key>
  ntm attention unpin <cursor-or-key>
  ntm attention escalate <cursor-or-key> --priority critical
  ntm attention restore <cursor-or-key>`,
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

	events, err := store.GetAttentionEventsSince(0, scanLimit)
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
	sessionFilter = strings.TrimSpace(sessionFilter)
	items := make([]AttentionCLIItem, 0, len(events))
	// Iterate newest-first for operator triage, bounded to limit.
	for i := len(events) - 1; i >= 0 && len(items) < limit; i-- {
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
		// refs against real events attach their latest cursor & fingerprint.
		if evs, evErr := store.GetAttentionEventsSince(0, 500); evErr == nil {
			for i := len(evs) - 1; i >= 0; i-- {
				if evs[i].DedupKey == ref {
					ev := evs[i]
					storedEvent = &ev
					cursor = ev.Cursor
					fingerprint = attentionFingerprintForCLI(ev)
					if resolvedKey, _ := store.ResolveAttentionItemKey(cursor); resolvedKey != "" {
						itemKey = resolvedKey
						prior, _ = store.GetAttentionItemState(itemKey)
					}
					break
				}
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
