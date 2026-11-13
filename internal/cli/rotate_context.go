package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newRotateContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Context window rotation management",
		Long: `Commands for viewing and managing context window rotation history.

Context rotation automatically replaces agents when their context window
approaches capacity, preserving work in progress.

Examples:
  ntm rotate context history                     # View recent rotations
  ntm rotate context history --last=20           # View last 20 rotations
  ntm rotate context history myproject           # Rotations for a session
  ntm rotate context history --failed            # Only failed rotations
  ntm rotate context stats                       # Rotation statistics
  ntm rotate context pending                     # View pending rotation confirmations
  ntm rotate context confirm <agent>             # Confirm a pending rotation
  ntm rotate context cancel <agent>              # Cancel a queued pending rotation
  ntm rotate context manual <session> <agent>    # Run manual compaction/rotation check
  ntm rotate context recover <agent>             # Resolve an interrupted rotation`,
	}

	cmd.AddCommand(newRotateContextHistoryCmd())
	cmd.AddCommand(newRotateContextStatsCmd())
	cmd.AddCommand(newRotateContextClearCmd())
	cmd.AddCommand(newRotateContextPendingCmd())
	cmd.AddCommand(newRotateContextConfirmCmd())
	cmd.AddCommand(newRotateContextCancelCmd())
	cmd.AddCommand(newRotateContextManualCmd())
	cmd.AddCommand(newRotateContextRecoverCmd())

	return cmd
}

func newRotateContextHistoryCmd() *cobra.Command {
	var (
		limit  int
		failed bool
	)

	cmd := &cobra.Command{
		Use:   "history [session]",
		Short: "View context rotation history",
		Long: `View the history of context window rotations.

Examples:
  ntm rotate context history              # All recent rotations
  ntm rotate context history --last=20    # Last 20 rotations
  ntm rotate context history myproject    # Rotations for a session
  ntm rotate context history --failed     # Only failed rotations`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationHistory(args, limit, failed)
		},
	}

	cmd.Flags().IntVar(&limit, "last", 20, "Number of recent rotations to show")
	cmd.Flags().BoolVar(&failed, "failed", false, "Show only failed rotations")

	return cmd
}

func newRotateContextStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show context rotation statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationStats()
		},
	}
}

func newRotateContextClearCmd() *cobra.Command {
	var force bool
	var pendingOnly bool

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear context rotation history or resolved pending entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationClear(cmd, force, pendingOnly)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().BoolVar(&pendingOnly, "pending", false, "Clear resolved/expired pending rotation entries instead of history")

	return cmd
}

// RotationHistoryResult contains the rotation history output
type RotationHistoryResult struct {
	Records    []ctxmon.RotationRecord `json:"records"`
	TotalCount int                     `json:"total_count"`
	Showing    int                     `json:"showing"`
}

func (r *RotationHistoryResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Records) == 0 {
		fmt.Fprintf(w, "%sNo context rotations found%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	// Print header
	fmt.Fprintf(w, " %sTIME%s          %sSESSION%s       %sAGENT%s      %sCONTEXT%s  %sRESULT%s\n",
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text))

	// Print entries in reverse order (newest first)
	for i := len(r.Records) - 1; i >= 0; i-- {
		rec := r.Records[i]

		// Format time
		timeStr := rec.Timestamp.Local().Format("Jan 02 15:04")

		// Format session (truncate if needed)
		sessionName := truncateStr(rec.SessionName, 12)

		// Format agent (truncate if needed)
		agentID := truncateStr(rec.AgentID, 10)

		// Context usage
		contextStr := fmt.Sprintf("%.0f%%", rec.ContextBefore)

		// Result indicator
		resultStr := colorize(t.Success) + "✓" + colorize(t.Text)
		if !rec.Success {
			resultStr = colorize(t.Red) + "✗" + colorize(t.Text)
			if rec.FailureReason != "" {
				resultStr += " " + truncateStr(rec.FailureReason, 20)
			}
		}

		fmt.Fprintf(w, " %-14s %-13s %-10s %7s  %s\n",
			timeStr,
			sessionName,
			agentID,
			contextStr,
			resultStr)
	}

	if r.TotalCount > r.Showing {
		fmt.Fprintf(w, "\n%sShowing %d of %d rotations. Use --last for more.%s\n",
			colorize(t.Surface1), r.Showing, r.TotalCount, colorize(t.Text))
	}

	return nil
}

func (r *RotationHistoryResult) JSON() interface{} {
	return r
}

func runContextRotationHistory(args []string, limit int, failedOnly bool) error {
	store := ctxmon.DefaultRotationHistoryStore

	var records []ctxmon.RotationRecord
	var err error

	// Determine how to fetch records based on filters
	if len(args) > 0 {
		// Filter by session first
		records, err = store.ReadForSession(args[0])
		if err != nil {
			return err
		}
		// Additionally filter by failed if specified
		if failedOnly {
			var filtered []ctxmon.RotationRecord
			for _, r := range records {
				if !r.Success {
					filtered = append(filtered, r)
				}
			}
			records = filtered
		}
	} else if failedOnly {
		records, err = store.ReadFailed()
	} else {
		records, err = store.ReadRecent(limit)
	}

	if err != nil {
		return err
	}

	totalCount := len(records)

	// Apply limit
	showing := len(records)
	if len(records) > limit {
		records = records[len(records)-limit:]
		showing = limit
	}

	result := &RotationHistoryResult{
		Records:    records,
		TotalCount: totalCount,
		Showing:    showing,
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

// RotationStatsResult contains the rotation statistics output
type RotationStatsResult struct {
	*ctxmon.RotationStats
}

func (r *RotationStatsResult) Text(w io.Writer) error {
	t := theme.Current()
	s := r.RotationStats

	fmt.Fprintf(w, "%sContext Rotation Statistics%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Fprintf(w, "─────────────────────────────────\n")
	fmt.Fprintf(w, "Total rotations:      %d\n", s.TotalRotations)
	fmt.Fprintf(w, "Successful:           %s%d%s\n", colorize(t.Success), s.SuccessCount, colorize(t.Text))
	fmt.Fprintf(w, "Failed:               %s%d%s\n", colorize(t.Red), s.FailureCount, colorize(t.Text))
	fmt.Fprintf(w, "Unique sessions:      %d\n", s.UniqueSessions)

	if s.TotalRotations > 0 {
		fmt.Fprintf(w, "\n%sContext at rotation:%s  %.1f%% avg\n", colorize(t.Blue), colorize(t.Text), s.AvgContextBefore)
		fmt.Fprintf(w, "%sRotation duration:%s    %dms avg\n", colorize(t.Blue), colorize(t.Text), s.AvgDurationMs)
	}

	if s.ThresholdRotations > 0 || s.ManualRotations > 0 {
		fmt.Fprintf(w, "\n%sBy trigger:%s\n", colorize(t.Blue), colorize(t.Text))
		fmt.Fprintf(w, "  Threshold:          %d\n", s.ThresholdRotations)
		fmt.Fprintf(w, "  Manual:             %d\n", s.ManualRotations)
	}

	if s.CompactionAttempts > 0 {
		fmt.Fprintf(w, "\n%sCompaction:%s\n", colorize(t.Blue), colorize(t.Text))
		fmt.Fprintf(w, "  Attempts:           %d\n", s.CompactionAttempts)
		fmt.Fprintf(w, "  Successes:          %d\n", s.CompactionSuccesses)
	}

	if len(s.RotationsByAgentType) > 0 {
		fmt.Fprintf(w, "\n%sBy agent type:%s\n", colorize(t.Blue), colorize(t.Text))
		for agentType, count := range s.RotationsByAgentType {
			fmt.Fprintf(w, "  %-18s %d\n", agentType+":", count)
		}
	}

	fmt.Fprintf(w, "\nFile size:            %s\n", formatBytes(s.FileSizeBytes))
	fmt.Fprintf(w, "File location:        %s\n", ctxmon.RotationHistoryStoragePath())

	return nil
}

func (r *RotationStatsResult) JSON() interface{} {
	return r.RotationStats
}

func runContextRotationStats() error {
	stats, err := ctxmon.GetRotationStats()
	if err != nil {
		return err
	}

	result := &RotationStatsResult{RotationStats: stats}
	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func runContextRotationClear(cmd *cobra.Command, force, pendingOnly bool) error {
	t := theme.Current()
	out := cmd.OutOrStdout()

	if pendingOnly {
		visible, err := ctxmon.GetAllPendingRotations()
		if err != nil {
			return err
		}
		if len(visible) > 0 && !force && !IsJSONOutput() {
			fmt.Fprintf(out, "This will clear %d pending rotation entries. Continue? [y/N]: ", len(visible))
			var response string
			_, _ = fmt.Fscanln(cmd.InOrStdin(), &response)
			if response != "y" && response != "Y" {
				fmt.Fprintln(out, "Aborted.")
				return nil
			}
		}
		if err := ctxmon.DefaultPendingRotationStore.Clear(); err != nil {
			if IsJSONOutput() {
				_ = json.NewEncoder(out).Encode(map[string]any{
					"success": false,
					"target":  "pending",
					"error":   err.Error(),
				})
				return jsonFailureExit()
			}
			return err
		}
		if IsJSONOutput() {
			return json.NewEncoder(out).Encode(map[string]any{
				"success":         true,
				"target":          "pending",
				"cleared_visible": len(visible),
			})
		}
		fmt.Fprintf(out, "%s✓%s Cleared pending rotation store (%d queued entries)\n", colorize(t.Success), colorize(t.Text), len(visible))
		return nil
	}

	store := ctxmon.DefaultRotationHistoryStore
	count, err := store.Count()
	if err != nil {
		return err
	}

	if count == 0 {
		fmt.Fprintf(out, "%sNo rotation history to clear%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	if !force {
		fmt.Fprintf(out, "This will remove all %d rotation history entries. Continue? [y/N]: ", count)
		var response string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &response)
		if response != "y" && response != "Y" {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	if err := store.Clear(); err != nil {
		return err
	}

	fmt.Fprintf(out, "%s✓%s Cleared %d rotation history entries\n", colorize(t.Success), colorize(t.Text), count)
	return nil
}

// Note: truncateStr is defined in checkpoint.go

func newRotateContextPendingCmd() *cobra.Command {
	var sessionFilter string

	cmd := &cobra.Command{
		Use:   "pending [session]",
		Short: "View pending rotation confirmations",
		Long: `View pending context rotation confirmations awaiting user action.

Rotations triggered by the coordinator ([rotation] usage_percent_threshold) are
queued as pending until confirmed via this CLI or dashboard, unless [rotation]
auto_confirm is set.

Examples:
  ntm rotate context pending              # All pending rotations
  ntm rotate context pending myproject    # Pending for a specific session`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				sessionFilter = args[0]
			}
			return runContextRotationPending(cmd.Context(), sessionFilter)
		},
	}

	return cmd
}

// PendingRotationsResult contains the pending rotations output
type PendingRotationsResult struct {
	Pending []PendingRotationInfo `json:"pending"`
	Count   int                   `json:"count"`
}

// PendingRotationInfo contains info about a pending rotation for display
type PendingRotationInfo struct {
	AgentID            string                            `json:"agent_id"`
	SessionName        string                            `json:"session_name"`
	ContextPercent     float64                           `json:"context_percent"`
	TimeoutSeconds     int                               `json:"timeout_seconds"`
	CreatedAt          string                            `json:"created_at"`
	SelectedAction     string                            `json:"selected_action,omitempty"`
	ExecutionState     string                            `json:"execution_state,omitempty"`
	LastError          string                            `json:"last_error,omitempty"`
	ExecutionID        string                            `json:"execution_id,omitempty"`
	ContinuationPhase  string                            `json:"continuation_phase,omitempty"`
	SourcePaneID       string                            `json:"source_pane_id,omitempty"`
	SourcePanePID      int                               `json:"source_pane_pid,omitempty"`
	ReplacementPaneID  string                            `json:"replacement_pane_id,omitempty"`
	ReplacementPanePID int                               `json:"replacement_pane_pid,omitempty"`
	RetryAllowed       bool                              `json:"retry_allowed"`
	RecoveryActions    []ctxmon.RotationResolutionAction `json:"recovery_actions,omitempty"`
	Resolution         *ctxmon.RotationResolution        `json:"resolution,omitempty"`
}

func (r *PendingRotationsResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Pending) == 0 {
		fmt.Fprintf(w, "%sNo pending rotation confirmations%s\n", colorize(t.Surface1), colorize(t.Text))
		return nil
	}

	fmt.Fprintf(w, "%sPending Rotation Confirmations%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Fprintf(w, "────────────────────────────────────────\n")

	for _, p := range r.Pending {
		// Color based on timeout
		timeoutColor := t.Success
		if p.TimeoutSeconds < 30 {
			timeoutColor = t.Red
		} else if p.TimeoutSeconds < 60 {
			timeoutColor = t.Warning
		}

		fmt.Fprintf(w, "\n%sAgent:%s %s\n", colorize(t.Surface1), colorize(t.Text), p.AgentID)
		fmt.Fprintf(w, "  Session: %s\n", p.SessionName)
		fmt.Fprintf(w, "  Context: %.1f%%\n", p.ContextPercent)
		fmt.Fprintf(w, "  Timeout: %s%ds%s\n", colorize(timeoutColor), p.TimeoutSeconds, colorize(t.Text))
		fmt.Fprintf(w, "  Created: %s\n", p.CreatedAt)
		if p.ExecutionState != "" {
			fmt.Fprintf(w, "  Confirmation: %s (%s)\n", p.SelectedAction, p.ExecutionState)
			fmt.Fprintf(w, "  Execution: %s\n", p.ExecutionID)
			if p.ContinuationPhase != "" {
				fmt.Fprintf(w, "  Continuation: %s\n", p.ContinuationPhase)
				fmt.Fprintf(w, "  Original pane: %s (PID %d)\n", p.SourcePaneID, p.SourcePanePID)
				if p.ReplacementPaneID != "" {
					fmt.Fprintf(w, "  Replacement pane: %s (PID %d)\n", p.ReplacementPaneID, p.ReplacementPanePID)
				}
			}
			if p.Resolution != nil {
				fmt.Fprintf(w, "  Operator decision: %s (%q)\n", p.Resolution.Action, p.Resolution.Reason)
			}
			if p.LastError != "" {
				fmt.Fprintf(w, "  Last error: %s\n", p.LastError)
			}
			if len(p.RecoveryActions) > 0 {
				actions := make([]string, len(p.RecoveryActions))
				for i, action := range p.RecoveryActions {
					actions[i] = string(action)
				}
				fmt.Fprintf(w, "  Inspect both panes, then use recover %s --execution-id=%s --action=<%s> --reason=<operator-note>.\n", p.AgentID, p.ExecutionID, strings.Join(actions, "|"))
			} else if p.RetryAllowed {
				fmt.Fprintf(w, "  Inspect the pane before explicitly retrying with --action=%s --retry.\n", p.SelectedAction)
			}
		}
	}

	fmt.Fprintf(w, "\n%sUse 'ntm rotate context confirm <agent> --action=<action>' to confirm%s\n",
		colorize(t.Surface1), colorize(t.Text))
	fmt.Fprintf(w, "Actions: rotate, compact, ignore, postpone\n")

	return nil
}

func (r *PendingRotationsResult) JSON() interface{} {
	return r
}

func runContextRotationPending(ctx context.Context, sessionFilter string) error {
	var pending []*ctxmon.PendingRotation
	var err error

	if sessionFilter != "" {
		sessionFilter, err = normalizeProjectScopedSessionName(ctx, sessionFilter, true)
		if err != nil {
			return err
		}
		pending, err = ctxmon.GetPendingRotationsForSession(sessionFilter)
	} else {
		pending, err = ctxmon.GetAllPendingRotations()
	}

	if err != nil {
		return err
	}

	infos := make([]PendingRotationInfo, 0, len(pending))
	for _, p := range pending {
		lastError := ""
		if p.Result != nil {
			lastError = p.Result.Error
		}
		info := PendingRotationInfo{
			AgentID:         p.AgentID,
			SessionName:     p.SessionName,
			ContextPercent:  p.ContextPercent,
			TimeoutSeconds:  p.RemainingSeconds(),
			CreatedAt:       p.CreatedAt.Local().Format("15:04:05"),
			SelectedAction:  string(p.SelectedAction),
			ExecutionState:  string(p.ExecutionState),
			LastError:       lastError,
			ExecutionID:     p.ExecutionID,
			RetryAllowed:    contextRotationRetryAllowed(p),
			RecoveryActions: ctxmon.RotationRecoveryActions(p),
		}
		if p.Continuation != nil {
			info.ContinuationPhase = p.Continuation.Phase
			info.SourcePaneID, info.SourcePanePID = p.Continuation.Source.ID, p.Continuation.Source.PID
			info.ReplacementPaneID, info.ReplacementPanePID = p.Continuation.Replacement.ID, p.Continuation.Replacement.PID
			info.Resolution = p.Continuation.Resolution
		}
		infos = append(infos, info)
	}

	result := &PendingRotationsResult{
		Pending: infos,
		Count:   len(infos),
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func contextRotationRetryAllowed(pending *ctxmon.PendingRotation) bool {
	if pending == nil || pending.ExecutionState != ctxmon.RotationStateFailed && pending.ExecutionState != ctxmon.RotationStateInProgress {
		return false
	}
	if pending.Continuation == nil {
		return true
	}
	switch pending.Continuation.Phase {
	case "handoff_accepted", "ownership_committed", "retirement_attempted":
		return true
	default:
		return false
	}
}

func newRotateContextConfirmCmd() *cobra.Command {
	var action string
	var postponeMinutes int
	var retry bool

	cmd := &cobra.Command{
		Use:   "confirm <agent-id>",
		Short: "Confirm a pending rotation",
		Long: `Confirm a pending context rotation with the specified action.

Actions:
  rotate    - Proceed with the rotation (default)
  compact   - Try to compact the context instead of rotating
  ignore    - Cancel the rotation and continue as-is
  postpone  - Delay the rotation (use --minutes to specify duration)

Examples:
  ntm rotate context confirm myproject__cc_1 --action=rotate
  ntm rotate context confirm myproject__cc_1 --action=compact
  ntm rotate context confirm myproject__cc_1 --action=ignore
  ntm rotate context confirm myproject__cc_1 --action=postpone --minutes=30`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationConfirm(cmd, args[0], action, postponeMinutes, retry)
		},
	}

	cmd.Flags().StringVarP(&action, "action", "a", "rotate", "Action to take: rotate, compact, ignore, postpone")
	cmd.Flags().IntVarP(&postponeMinutes, "minutes", "m", 30, "Minutes to postpone (only with --action=postpone)")
	cmd.Flags().BoolVar(&retry, "retry", false, "Explicitly retry an interrupted or failed confirmation of the same action")

	return cmd
}

// ConfirmRotationResult contains the confirmation result
type ConfirmRotationResult struct {
	AgentID           string                            `json:"agent_id"`
	Action            string                            `json:"action"`
	Success           bool                              `json:"success"`
	Message           string                            `json:"message"`
	Rotation          *ctxmon.RotationResult            `json:"rotation,omitempty"`
	RetryRequired     bool                              `json:"retry_required,omitempty"`
	ExecutionID       string                            `json:"execution_id,omitempty"`
	ContinuationPhase string                            `json:"continuation_phase,omitempty"`
	RecoveryActions   []ctxmon.RotationResolutionAction `json:"recovery_actions,omitempty"`
}

func (r *ConfirmRotationResult) Text(w io.Writer) error {
	t := theme.Current()

	if r.Success {
		fmt.Fprintf(w, "%s✓%s %s\n", colorize(t.Success), colorize(t.Text), r.Message)
	} else {
		fmt.Fprintf(w, "%s✗%s %s\n", colorize(t.Red), colorize(t.Text), r.Message)
	}

	return nil
}

func (r *ConfirmRotationResult) JSON() interface{} {
	return r
}

func runContextRotationConfirm(cmd *cobra.Command, agentID, action string, postponeMinutes int, retry bool) error {
	// Validate action
	var confirmAction ctxmon.ConfirmAction
	switch action {
	case "rotate":
		confirmAction = ctxmon.ConfirmRotate
	case "compact":
		confirmAction = ctxmon.ConfirmCompact
	case "ignore":
		confirmAction = ctxmon.ConfirmIgnore
	case "postpone":
		confirmAction = ctxmon.ConfirmPostpone
	default:
		return fmt.Errorf("invalid action: %s (use: rotate, compact, ignore, postpone)", action)
	}

	if confirmAction == ctxmon.ConfirmPostpone && postponeMinutes <= 0 {
		return fmt.Errorf("postpone minutes must be positive")
	}
	rotation := ctxmon.ConfirmPendingRotationContext(cmd.Context(), agentID, confirmAction, postponeMinutes, retry, cfg)
	message := rotation.Error
	if rotation.Success {
		switch confirmAction {
		case ctxmon.ConfirmRotate:
			if rotation.ResolutionAction == ctxmon.RotationResolutionAbort {
				message = fmt.Sprintf("Rotation aborted for %s; ownership remains with %s", agentID, rotation.OldPaneID)
			} else if rotation.State == ctxmon.RotationStateAborted && rotation.NewPaneID == "" {
				// A coordinator may have already satisfied this request through
				// its automatic compaction policy. Report that stored receipt.
				message = fmt.Sprintf("Context compaction completed for %s; replacement was not needed", agentID)
			} else {
				message = fmt.Sprintf("Rotation completed for %s (%s → %s)", agentID, rotation.OldPaneID, rotation.NewPaneID)
			}
		case ctxmon.ConfirmCompact:
			message = fmt.Sprintf("Context compaction completed for %s", agentID)
		case ctxmon.ConfirmIgnore:
			message = fmt.Sprintf("Pending rotation for %s cancelled", agentID)
		case ctxmon.ConfirmPostpone:
			message = fmt.Sprintf("Pending rotation for %s postponed by %d minutes", agentID, postponeMinutes)
		}
	}
	result := &ConfirmRotationResult{
		AgentID:  agentID,
		Action:   action,
		Success:  rotation.Success,
		Message:  message,
		Rotation: &rotation,
	}
	if pending, err := ctxmon.GetPendingRotationByID(agentID); err == nil && pending != nil {
		result.RetryRequired = contextRotationRetryAllowed(pending)
		result.ExecutionID = pending.ExecutionID
		result.RecoveryActions = ctxmon.RotationRecoveryActions(pending)
		if pending.Continuation != nil {
			result.ContinuationPhase = pending.Continuation.Phase
		}
	}
	if IsJSONOutput() {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
	} else if err := result.Text(cmd.OutOrStdout()); err != nil {
		return err
	}
	if !result.Success {
		return jsonFailureExit()
	}
	return nil
}

func newRotateContextRecoverCmd() *cobra.Command {
	var action, executionID, reason string
	cmd := &cobra.Command{
		Use:   "recover <agent-id>",
		Short: "Resolve an interrupted context rotation after inspecting its panes",
		Long: `Resolve the exact interrupted execution shown by 'rotate context pending'.

Inspect the original and replacement panes before choosing an action:
  accept - Attest that the replacement received the handoff, then finish ownership
           transfer without launching another agent or resending the handoff.
  abort  - Stop the recorded replacement and retain ownership with the original
           agent. Work already performed by the replacement is not undone.

The execution ID prevents resolving a newer rotation by mistake. The action and
operator reason are saved before recovery effects. Repeating the same decision
resumes interrupted recovery or returns its saved result. Conflicting decisions
are refused, and abort cannot reverse an accepted ownership transfer.

Examples:
  ntm rotate context recover payments__cc_1 --execution-id=<id> --action=accept --reason="Replacement is working on the handoff"
  ntm rotate context recover payments__cc_1 --execution-id=<id> --action=abort --reason="Stop replacement and continue in the original pane"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationRecover(cmd, args[0], executionID, action, reason)
		},
	}
	cmd.Flags().StringVar(&action, "action", "", "Operator decision: accept or abort")
	cmd.Flags().StringVar(&executionID, "execution-id", "", "Exact execution ID from rotate context pending")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for the operator decision, retained in the recovery record")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("execution-id")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

type RecoverRotationResult struct {
	AgentID     string                 `json:"agent_id"`
	ExecutionID string                 `json:"execution_id"`
	Action      string                 `json:"action"`
	Success     bool                   `json:"success"`
	Message     string                 `json:"message"`
	Rotation    *ctxmon.RotationResult `json:"rotation,omitempty"`
}

func runContextRotationRecover(cmd *cobra.Command, agentID, executionID, action, reason string) error {
	rotation := ctxmon.ResolvePendingRotationContext(cmd.Context(), agentID, executionID, ctxmon.RotationResolutionAction(action), reason, cfg)
	message := rotation.Error
	if rotation.Success {
		if action == string(ctxmon.RotationResolutionAbort) {
			message = fmt.Sprintf("Rotation aborted for %s; ownership remains with %s", agentID, rotation.OldPaneID)
		} else {
			message = fmt.Sprintf("Rotation recovered for %s (%s → %s)", agentID, rotation.OldPaneID, rotation.NewPaneID)
		}
		if rotation.Error != "" {
			message += "; " + rotation.Error
		}
	}
	result := RecoverRotationResult{AgentID: agentID, ExecutionID: executionID, Action: action, Success: rotation.Success, Message: message, Rotation: &rotation}
	if IsJSONOutput() {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
	} else {
		display := ConfirmRotationResult{Success: result.Success, Message: result.Message}
		if err := display.Text(cmd.OutOrStdout()); err != nil {
			return err
		}
	}
	if !result.Success {
		return jsonFailureExit()
	}
	return nil
}

// CancelRotationResult reports the outcome of `ntm rotate context cancel`.
type CancelRotationResult struct {
	Success          bool   `json:"success"`
	AgentID          string `json:"agent_id"`
	Cancelled        bool   `json:"cancelled"`
	RemainingPending int    `json:"remaining_pending"`
	Message          string `json:"message"`
}

func newRotateContextCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <agent-id>",
		Short: "Cancel a queued pending context rotation",
		Long: `Cancel a queued pending context rotation before confirmation starts.

If a rotation confirmation has already started or was interrupted in-flight,
inspect the panes and resolve it via 'ntm rotate context recover' instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextRotationCancel(cmd, strings.TrimSpace(args[0]))
		},
	}
}

func runContextRotationCancel(cmd *cobra.Command, agentID string) error {
	out := cmd.OutOrStdout()
	t := theme.Current()

	r := ctxmon.NewRotator(ctxmon.RotatorConfig{})
	if err := r.LoadPendingFromStore(); err != nil {
		return err
	}

	existing := r.GetPendingRotation(agentID)
	if existing == nil {
		res := CancelRotationResult{
			Success:          false,
			AgentID:          agentID,
			Cancelled:        false,
			RemainingPending: len(r.GetPendingRotations()),
			Message:          fmt.Sprintf("No pending rotation found for %s", agentID),
		}
		if IsJSONOutput() {
			if err := json.NewEncoder(out).Encode(res); err != nil {
				return err
			}
			return jsonFailureExit()
		}
		fmt.Fprintf(out, "%s✗%s %s\n", colorize(t.Red), colorize(t.Text), res.Message)
		return fmt.Errorf("no pending rotation found for %s", agentID)
	}

	if existing.ExecutionState != "" && existing.ExecutionState != ctxmon.RotationStatePending && existing.ExecutionState != ctxmon.RotationStateCompleted {
		res := CancelRotationResult{
			Success:          false,
			AgentID:          agentID,
			Cancelled:        false,
			RemainingPending: len(r.GetPendingRotations()),
			Message:          fmt.Sprintf("Pending rotation for %s has unresolved confirmation (%s); inspect panes and use 'ntm rotate context recover'", agentID, existing.ExecutionState),
		}
		if IsJSONOutput() {
			if err := json.NewEncoder(out).Encode(res); err != nil {
				return err
			}
			return jsonFailureExit()
		}
		fmt.Fprintf(out, "%s✗%s %s\n", colorize(t.Red), colorize(t.Text), res.Message)
		return fmt.Errorf("pending rotation for %s has unresolved confirmation (%s)", agentID, existing.ExecutionState)
	}

	cancelled := r.CancelPendingRotation(agentID)
	remaining := len(r.GetPendingRotations())

	if stillStored, err := ctxmon.GetPendingRotationByID(agentID); err != nil || stillStored != nil {
		errMsg := fmt.Sprintf("Failed to remove pending rotation for %s from store", agentID)
		if err != nil {
			errMsg = fmt.Sprintf("%s: %v", errMsg, err)
		}
		res := CancelRotationResult{
			Success:          false,
			AgentID:          agentID,
			Cancelled:        false,
			RemainingPending: remaining,
			Message:          errMsg,
		}
		if IsJSONOutput() {
			if encErr := json.NewEncoder(out).Encode(res); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		fmt.Fprintf(out, "%s✗%s %s\n", colorize(t.Red), colorize(t.Text), res.Message)
		return fmt.Errorf("%s", errMsg)
	}

	res := CancelRotationResult{
		Success:          cancelled,
		AgentID:          agentID,
		Cancelled:        cancelled,
		RemainingPending: remaining,
		Message:          fmt.Sprintf("Cancelled pending rotation for %s (%d remaining)", agentID, remaining),
	}
	if IsJSONOutput() {
		return json.NewEncoder(out).Encode(res)
	}
	fmt.Fprintf(out, "%s✓%s %s\n", colorize(t.Success), colorize(t.Text), res.Message)
	return nil
}

type rotateContextManualOptions struct {
	Session         string
	AgentID         string
	WarnThreshold   float64
	RotateThreshold float64
	CompactFirst    bool
	IfNeeded        bool
}

type rotateContextManualDeps struct {
	resolve             func(ctx context.Context, name string) (string, error)
	projectDir          func(ctx context.Context, session string) (string, error)
	spawner             ctxmon.PaneSpawner
	seedMonitor         func(ctx context.Context, monitor *ctxmon.ContextMonitor, session, agentID string) (paneID string, agentType string, err error)
	refreshAfterCompact func(ctx context.Context, monitor *ctxmon.ContextMonitor, session, agentID, paneID string)
}

// ManualRotationResult reports the outcome of `ntm rotate context manual`.
type ManualRotationResult struct {
	Success        bool                     `json:"success"`
	Session        string                   `json:"session"`
	AgentID        string                   `json:"agent_id"`
	Skipped        bool                     `json:"skipped,omitempty"`
	ShouldCompact  bool                     `json:"should_compact"`
	CompactAdvice  string                   `json:"compact_advice"`
	ShouldRotate   bool                     `json:"should_rotate"`
	PreCheckReason string                   `json:"pre_check_reason"`
	Compaction     *ctxmon.CompactionResult `json:"compaction,omitempty"`
	Rotation       *ctxmon.RotationResult   `json:"rotation,omitempty"`
	Events         []ctxmon.RotationEvent   `json:"events"`
}

func newRotateContextManualCmd() *cobra.Command {
	return newRotateContextManualCmdWithDeps(nil)
}

func newRotateContextManualCmdWithDeps(deps *rotateContextManualDeps) *cobra.Command {
	opts := rotateContextManualOptions{
		WarnThreshold:   0.70,
		RotateThreshold: 0.80,
	}

	cmd := &cobra.Command{
		Use:   "manual <session> <agent-id>",
		Short: "Manually evaluate compaction and trigger context rotation for an agent",
		Long: `Manually trigger context window rotation for an agent pane, with optional
pre-rotation compaction evaluation and threshold gating.

Examples:
  ntm rotate context manual myproject myproject__cc_1
  ntm rotate context manual myproject myproject__cc_1 --compact-first --if-needed`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Session = strings.TrimSpace(args[0])
			opts.AgentID = strings.TrimSpace(args[1])
			if opts.WarnThreshold <= 0 || opts.WarnThreshold > 1.0 {
				return fmt.Errorf("--warn-threshold must be in (0.0, 1.0], got %.2f", opts.WarnThreshold)
			}
			if opts.RotateThreshold <= 0 || opts.RotateThreshold > 1.0 {
				return fmt.Errorf("--rotate-threshold must be in (0.0, 1.0], got %.2f", opts.RotateThreshold)
			}

			active := deps
			if active == nil {
				defaults := defaultRotateContextManualDeps(cmd.OutOrStdout())
				active = &defaults
			}

			result, err := runRotateContextManual(cmd.Context(), opts, *active)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if IsJSONOutput() {
				if encErr := json.NewEncoder(out).Encode(result); encErr != nil {
					return encErr
				}
				if !result.Success {
					return jsonFailureExit()
				}
				return nil
			}

			renderManualRotationText(out, result)
			if !result.Success {
				if result.Rotation != nil && result.Rotation.Error != "" {
					return fmt.Errorf("manual rotation failed for %s: %s", result.AgentID, result.Rotation.Error)
				}
				return fmt.Errorf("manual rotation failed for %s", result.AgentID)
			}
			return nil
		},
	}

	cmd.Flags().Float64Var(&opts.WarnThreshold, "warn-threshold", 0.70, "Compaction warning threshold as decimal (0.0-1.0)")
	cmd.Flags().Float64Var(&opts.RotateThreshold, "rotate-threshold", 0.80, "Rotation threshold as decimal (0.0-1.0)")
	cmd.Flags().BoolVar(&opts.CompactFirst, "compact-first", false, "Attempt builtin context compaction before replacing the agent")
	cmd.Flags().BoolVar(&opts.IfNeeded, "if-needed", false, "Skip replacement rotation when usage is below --rotate-threshold")

	return cmd
}

func defaultRotateContextManualDeps(w io.Writer) rotateContextManualDeps {
	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}
	return rotateContextManualDeps{
		resolve: func(ctx context.Context, name string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			res, err := ResolveSessionWithOptions(name, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
			if err != nil {
				return "", err
			}
			return res.Session, nil
		},
		projectDir: func(ctx context.Context, session string) (string, error) {
			dir, err := resolveExplicitProjectDirForSessionContext(ctx, session)
			if err == nil && strings.TrimSpace(dir) != "" {
				return dir, nil
			}
			if root := GetProjectRoot(); root != "" {
				return root, nil
			}
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("getting project root for session %s failed", session)
		},
		spawner: ctxmon.NewDefaultPaneSpawner(activeCfg),
		seedMonitor: func(ctx context.Context, monitor *ctxmon.ContextMonitor, session, agentID string) (string, string, error) {
			panes, err := tmux.GetPanes(session)
			if err != nil {
				return "", "", err
			}
			var matched *tmux.Pane
			for i := range panes {
				p := &panes[i]
				if strings.TrimSpace(p.Title) == agentID || p.ID == agentID {
					matched = p
					break
				}
			}
			if matched == nil {
				monitor.RegisterAgent(agentID, "", "")
				return "", "", nil
			}

			agentType := string(agent.AgentType(matched.Type).Canonical())
			model := strings.TrimSpace(matched.Variant)
			var transcriptPath string
			var usage *ctxmon.TranscriptUsage
			if tmux.DefaultClient.Remote == "" && matched.PID > 0 {
				if u, ok := ctxmon.ProcessTranscriptUsage(ctx, agentType, matched.PID); ok && u != nil {
					usage = u
					transcriptPath = u.Path
					if model == "" && u.Model != "" {
						model = u.Model
					}
				}
			}

			monitor.RegisterAgentWithTranscript(agentID, matched.ID, model, agentType, session, transcriptPath)
			probeAgentMonitorUsage(ctx, monitor, agentID, matched.ID, usage, transcriptPath)
			return matched.ID, agentType, nil
		},
		refreshAfterCompact: func(ctx context.Context, monitor *ctxmon.ContextMonitor, session, agentID, paneID string) {
			panes, err := tmux.GetPanes(session)
			if err != nil {
				return
			}
			for _, p := range panes {
				if p.ID != paneID {
					continue
				}
				agentType := string(agent.AgentType(p.Type).Canonical())
				var usage *ctxmon.TranscriptUsage
				var transcriptPath string
				if tmux.DefaultClient.Remote == "" && p.PID > 0 {
					if u, ok := ctxmon.ProcessTranscriptUsage(ctx, agentType, p.PID); ok && u != nil {
						usage = u
						transcriptPath = u.Path
					}
				}
				probeAgentMonitorUsage(ctx, monitor, agentID, p.ID, usage, transcriptPath)
				return
			}
		},
	}
}

func probeAgentMonitorUsage(ctx context.Context, monitor *ctxmon.ContextMonitor, agentID, paneID string, usage *ctxmon.TranscriptUsage, transcriptPath string) {
	if paneID != "" {
		if captured, err := tmux.CapturePaneOutputContext(ctx, paneID, 60); err == nil && captured != "" && ctxmon.ParseRobotModeContext(captured) != nil {
			monitor.UpdateFromRobotMode(agentID, captured)
			return
		}
	}
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.Tokens > 0) {
		inTokens := int64(usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens)
		outTokens := int64(usage.OutputTokens)
		if inTokens == 0 && outTokens == 0 && usage.Tokens > 0 {
			inTokens = int64(usage.Tokens)
		}
		monitor.RecordMessage(agentID, inTokens, outTokens)
		return
	}
	if transcriptPath != "" {
		_, _ = monitor.UpdateFromTranscript(agentID)
	}
}

func runRotateContextManual(ctx context.Context, opts rotateContextManualOptions, deps rotateContextManualDeps) (ManualRotationResult, error) {
	if err := ctx.Err(); err != nil {
		return ManualRotationResult{}, err
	}
	session, err := deps.resolve(ctx, opts.Session)
	if err != nil {
		return ManualRotationResult{}, err
	}
	workDir, err := deps.projectDir(ctx, session)
	if err != nil {
		return ManualRotationResult{}, err
	}

	monitor := ctxmon.NewContextMonitor(ctxmon.MonitorConfig{
		WarningThreshold: opts.WarnThreshold * 100,
		RotateThreshold:  opts.RotateThreshold * 100,
	})

	paneID, agentType, err := deps.seedMonitor(ctx, monitor, session, opts.AgentID)
	if err != nil {
		return ManualRotationResult{}, err
	}

	compactor := ctxmon.NewCompactor(monitor, ctxmon.DefaultCompactorConfig())
	shouldCompact, compactAdvice := compactor.ShouldTryCompaction(opts.AgentID, opts.WarnThreshold)

	var compactResult *ctxmon.CompactionResult
	if opts.CompactFirst && shouldCompact && deps.spawner != nil && paneID != "" {
		cmds := compactor.GetCompactionCommands(agentType)
		if len(cmds) > 0 {
			cState, stateErr := compactor.NewCompactionState(opts.AgentID)
			if stateErr == nil {
				cmdSpec := cmds[0]
				var sendErr error
				if cmdSpec.IsPrompt {
					sendErr = deps.spawner.SendBuffer(paneID, cmdSpec.Command, true)
				} else {
					sendErr = deps.spawner.SendKeys(paneID, cmdSpec.Command, true)
				}
				if sendErr == nil {
					cState.UpdateState(cmdSpec, cmdSpec.Method)
					if deps.refreshAfterCompact != nil {
						deps.refreshAfterCompact(ctx, monitor, session, opts.AgentID, paneID)
					} else if cmdSpec.WaitTime > 0 {
						timer := time.NewTimer(cmdSpec.WaitTime)
						select {
						case <-ctx.Done():
							timer.Stop()
						case <-timer.C:
						}
					}
					compactResult, _ = compactor.FinishCompaction(cState)
				}
			}
		}
	}

	shouldRotate, preCheckReason := compactor.PreRotationCheck(opts.AgentID, opts.RotateThreshold, compactResult)
	if !shouldRotate && (opts.IfNeeded || (opts.CompactFirst && compactResult != nil && compactResult.Success)) {
		return ManualRotationResult{
			Success:        true,
			Session:        session,
			AgentID:        opts.AgentID,
			Skipped:        true,
			ShouldCompact:  shouldCompact,
			CompactAdvice:  compactAdvice,
			ShouldRotate:   shouldRotate,
			PreCheckReason: preCheckReason,
			Compaction:     compactResult,
			Events:         []ctxmon.RotationEvent{},
		}, nil
	}

	rotCfg := config.DefaultContextRotationConfig()
	if cfg != nil {
		rotCfg = cfg.ContextRotation
	}
	rotCfg.RotateThreshold = opts.RotateThreshold
	rotCfg.TryCompactFirst = false

	rotator := ctxmon.NewRotator(ctxmon.RotatorConfig{
		Monitor:   monitor,
		Compactor: compactor,
		Spawner:   deps.spawner,
		Config:    rotCfg,
	})

	rotResult := rotator.ManualRotate(session, opts.AgentID, workDir)
	events := rotator.GetHistory()
	if events == nil {
		events = []ctxmon.RotationEvent{}
	}

	return ManualRotationResult{
		Success:        rotResult.Success,
		Session:        session,
		AgentID:        opts.AgentID,
		ShouldCompact:  shouldCompact,
		CompactAdvice:  compactAdvice,
		ShouldRotate:   shouldRotate,
		PreCheckReason: preCheckReason,
		Compaction:     compactResult,
		Rotation:       &rotResult,
		Events:         events,
	}, nil
}

func renderManualRotationText(w io.Writer, result ManualRotationResult) {
	fmt.Fprintf(w, "Context Check: compact=%t (%s)\n", result.ShouldCompact, result.CompactAdvice)
	if result.Compaction != nil {
		fmt.Fprint(w, result.Compaction.FormatForDisplay())
	}
	fmt.Fprintf(w, "Pre-Rotation Check: rotate=%t (%s)\n", result.ShouldRotate, result.PreCheckReason)
	if result.Skipped {
		fmt.Fprintf(w, "✓ Replacement rotation skipped for %s\n", result.AgentID)
		return
	}
	if result.Rotation != nil {
		fmt.Fprint(w, result.Rotation.FormatForDisplay())
	}
	if len(result.Events) > 0 {
		last := result.Events[len(result.Events)-1]
		fmt.Fprintf(w, "  Recorded history event: %s → %s (%.1f%% → %.1f%%)\n",
			last.OldAgentID, last.NewAgentID, last.ContextBefore, last.ContextAfter)
	}
}
