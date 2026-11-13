package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newApproveCmd() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "approve [token]",
		Short: "Manage approval requests for dangerous operations",
		Long: `Manage approval requests for dangerous operations like force-release,
force-push, and other sensitive actions that require human sign-off.

When called with a token argument, approves that request.
Use subcommands for other operations.

Subcommands:
  approve list             List all pending approvals (--expired for elapsed pending requests)
  approve expire           Mark expired pending approvals as expired
  approve deny <token>     Deny a pending request
  approve history          Show approval history
  approve show <token>     Show details of an approval

Examples:
  ntm approve abc123                  # Approve request abc123
  ntm approve list                    # List pending approvals
  ntm approve list --expired          # List expired pending approvals
  ntm approve expire                  # Transition expired pending approvals to expired
  ntm approve deny abc123 --reason "Too risky"
  ntm approve show abc123             # Show approval details

Identity note: the approver identity recorded on decisions is taken from the
NTM_USER (or USER) environment variable. It is asserted, not authenticated —
any process or person with shell access to this machine can approve or deny
under any name. Treat the approval trail as attribution among cooperating
operators on a trusted host, not as an authentication boundary.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runApprove(args[0], IsJSONOutput())
		},
	}

	// list - list pending approvals
	var listLimit, listOffset int
	var listExpired bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all pending approvals",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveList(IsJSONOutput(), listLimit, listOffset, listExpired)
		},
	}
	listCmd.Flags().IntVar(&listLimit, "limit", 0, "maximum approvals per page (0 = all)")
	listCmd.Flags().IntVar(&listOffset, "offset", 0, "pagination offset (use _agent_hints.next_offset for the next page)")
	listCmd.Flags().BoolVar(&listExpired, "expired", false, "list pending approvals whose expiration window has elapsed")

	// expire - mark expired pending approvals as expired
	var expireDryRun bool
	expireCmd := &cobra.Command{
		Use:   "expire",
		Short: "Mark expired pending approvals as expired",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveExpire(expireDryRun, IsJSONOutput())
		},
	}
	expireCmd.Flags().BoolVar(&expireDryRun, "dry-run", false, "preview expired pending approvals without modifying state")

	// deny <token> - deny a request
	denyCmd := &cobra.Command{
		Use:   "deny <token>",
		Short: "Deny a pending request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveDeny(args[0], reason, IsJSONOutput())
		},
	}
	denyCmd.Flags().StringVar(&reason, "reason", "", "Reason for denial")

	// show <token> - show details
	showCmd := &cobra.Command{
		Use:   "show <token>",
		Short: "Show details of an approval request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveShow(args[0], IsJSONOutput())
		},
	}

	// history - show history
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Show approval history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveHistory(IsJSONOutput())
		},
	}

	cmd.AddCommand(listCmd, expireCmd, denyCmd, showCmd, historyCmd)
	return cmd
}

// ApprovalResult represents the result of an approval operation.
type ApprovalResult struct {
	Success  bool   `json:"success"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// ApprovalExpireOutput is the JSON envelope for `ntm approve expire`.
type ApprovalExpireOutput struct {
	Success      bool             `json:"success"`
	DryRun       bool             `json:"dry_run,omitempty"`
	Expired      []state.Approval `json:"expired"`
	ExpiredCount int              `json:"expired_count"`
}

var openApprovalStateStoreFunc = func() (*state.Store, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}

	// Ensure migrations are applied
	if err := store.Migrate(); err != nil {
		store.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return store, nil
}

func getApprovalEngine() (*approval.Engine, *state.Store, error) {
	store, err := openApprovalStateStoreFunc()
	if err != nil {
		return nil, nil, err
	}

	engine := approval.New(store, nil, nil, approval.DefaultConfig())
	return engine, store, nil
}

func recordApprovalStateAudit(store *state.Store, actorID, eventType, prevState, newState, summary, reason string, appr *state.Approval) {
	if store == nil || appr == nil {
		return
	}
	actorType := "user"
	if actorID == "" {
		actorID = "unknown"
	}
	_ = store.UpsertAuditActor(&state.AuditActor{
		ActorType:    actorType,
		ActorID:      actorID,
		DisplayName:  actorID,
		KnownOrigins: `["cli"]`,
	})
	_, _ = store.RecordAuditEvent(&state.AuditEvent{
		ActorType:     actorType,
		ActorID:       actorID,
		ActorOrigin:   "cli",
		CorrelationID: appr.CorrelationID,
		Category:      "actuation",
		EventType:     eventType,
		Severity:      state.SeverityInfo,
		EntityType:    "approval",
		EntityID:      appr.ID,
		PreviousState: prevState,
		NewState:      newState,
		ChangeSummary: summary,
		Reason:        reason,
		Evidence: state.EncodeJSON(map[string]interface{}{
			"action":       appr.Action,
			"resource":     appr.Resource,
			"requested_by": appr.RequestedBy,
			"requires_slb": appr.RequiresSLB,
		}),
	})
}

func runApprove(token string, jsonOutput bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	ctx := context.Background()
	currentUser := getCurrentApprover()

	if err := engine.Approve(ctx, token, currentUser); err != nil {
		return outputError(err, jsonOutput)
	}

	appr, err := engine.Check(ctx, token)
	if err != nil {
		return outputError(err, jsonOutput)
	}

	// Durable state.db record is the primary trail; mirror the decision into
	// both the state audit tables and the file-backed audit log so approvals
	// appear alongside other audited operations (bd-2y2on).
	recordApprovalStateAudit(
		store,
		currentUser,
		"approval.approved",
		string(state.ApprovalPending),
		string(appr.Status),
		fmt.Sprintf("approved %s on %s", appr.Action, appr.Resource),
		appr.Reason,
		appr,
	)
	_ = audit.LogEvent("", audit.EventTypeStateChange, audit.ActorUser, "approval.approve", map[string]interface{}{
		"approval_id":  token,
		"action":       appr.Action,
		"resource":     appr.Resource,
		"requested_by": appr.RequestedBy,
		"approved_by":  currentUser,
		"requires_slb": appr.RequiresSLB,
	}, nil)

	result := ApprovalResult{
		Success:  true,
		ID:       token,
		Action:   appr.Action,
		Resource: appr.Resource,
		Status:   string(appr.Status),
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("✓ Approved: %s\n", token)
	fmt.Printf("  Action:   %s\n", appr.Action)
	fmt.Printf("  Resource: %s\n", appr.Resource)
	fmt.Printf("  Approved by: %s at %s\n", appr.ApprovedBy, appr.ApprovedAt.Format(time.RFC3339))
	return nil
}

// ApprovalsListOutput is the paginated JSON envelope for `ntm approve list`
// (D1, bd-ws3-contract-breadth-psvyu.1).
type ApprovalsListOutput struct {
	Success      bool                        `json:"success"`
	ExpiredOnly  bool                        `json:"expired_only,omitempty"`
	ExpiredCount int                         `json:"expired_count,omitempty"`
	Pending      []state.Approval            `json:"pending"`
	Count        int                         `json:"count"`
	TotalMatches int                         `json:"total_matches"`
	HasMore      bool                        `json:"has_more"`
	Pagination   *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints   *robot.PaginationAgentHints `json:"_agent_hints,omitempty"`
}

// buildApprovalsListOutput pages the pending-approvals inventory.
func buildApprovalsListOutput(pending []state.Approval, limit, offset int) *ApprovalsListOutput {
	out := &ApprovalsListOutput{
		Success:      true,
		TotalMatches: len(pending),
	}
	page, info := robot.ApplyPagination(pending, robot.PaginationOptions{Limit: limit, Offset: offset})
	out.Pending = page
	if out.Pending == nil {
		out.Pending = []state.Approval{}
	}
	out.Count = len(page)
	if info != nil {
		out.Pagination = info
		out.HasMore = info.HasMore
		out.AgentHints = robot.PaginationHints(info)
	}
	return out
}

func runApproveList(jsonOutput bool, limit, offset int, expiredOnly bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	expired, err := store.ListExpiredPendingApprovals()
	if err != nil {
		return outputError(err, jsonOutput)
	}

	var items []state.Approval
	if expiredOnly {
		items = expired
	} else {
		ctx := context.Background()
		items, err = engine.ListPending(ctx)
		if err != nil {
			return outputError(err, jsonOutput)
		}
	}
	if items == nil {
		items = []state.Approval{}
	}

	if jsonOutput {
		out := buildApprovalsListOutput(items, limit, offset)
		out.ExpiredOnly = expiredOnly
		out.ExpiredCount = len(expired)
		return json.NewEncoder(os.Stdout).Encode(out)
	}

	if len(items) == 0 {
		if expiredOnly {
			fmt.Println("No expired pending approvals")
			return nil
		}
		fmt.Println("No pending approvals")
		if len(expired) > 0 {
			fmt.Printf("Note: %d pending approval(s) have expired; run 'ntm approve expire' to mark them expired.\n", len(expired))
		}
		return nil
	}

	if expiredOnly {
		fmt.Printf("Expired Pending Approvals (%d):\n\n", len(items))
	} else {
		fmt.Printf("Pending Approvals (%d):\n\n", len(items))
	}
	for _, a := range items {
		slb := ""
		if a.RequiresSLB {
			slb = " [SLB]"
		}
		fmt.Printf("  ID:       %s%s\n", a.ID, slb)
		fmt.Printf("  Action:   %s\n", a.Action)
		fmt.Printf("  Resource: %s\n", a.Resource)
		fmt.Printf("  Reason:   %s\n", a.Reason)
		fmt.Printf("  By:       %s\n", a.RequestedBy)
		fmt.Printf("  Expires:  %s\n", a.ExpiresAt.Format(time.RFC3339))
		fmt.Println()
	}
	if !expiredOnly && len(expired) > 0 {
		fmt.Printf("Note: %d pending approval(s) have expired; run 'ntm approve expire' to mark them expired.\n", len(expired))
	}
	return nil
}

func runApproveExpire(dryRun, jsonOutput bool) error {
	_, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	expiredPending, err := store.ListExpiredPendingApprovals()
	if err != nil {
		return outputError(err, jsonOutput)
	}

	transitioned := make([]state.Approval, 0, len(expiredPending))
	currentUser := getCurrentApprover()

	if dryRun {
		transitioned = append(transitioned, expiredPending...)
	} else {
		for _, appr := range expiredPending {
			updated := appr
			updated.Status = state.ApprovalExpired
			ok, err := store.UpdateApprovalFrom(&updated, state.ApprovalPending)
			if err != nil {
				return outputError(err, jsonOutput)
			}
			if ok {
				transitioned = append(transitioned, updated)
				recordApprovalStateAudit(
					store,
					currentUser,
					"approval.expired",
					string(state.ApprovalPending),
					string(state.ApprovalExpired),
					fmt.Sprintf("expired stale approval %s on %s", updated.Action, updated.Resource),
					"approval TTL elapsed before decision",
					&updated,
				)
			}
		}
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(ApprovalExpireOutput{
			Success:      true,
			DryRun:       dryRun,
			Expired:      transitioned,
			ExpiredCount: len(transitioned),
		})
	}

	if len(transitioned) == 0 {
		fmt.Println("No expired pending approvals")
		return nil
	}
	if dryRun {
		fmt.Printf("Would mark %d pending approval(s) as expired:\n", len(transitioned))
	} else {
		fmt.Printf("✓ Marked %d pending approval(s) as expired:\n", len(transitioned))
	}
	for _, appr := range transitioned {
		fmt.Printf("  - %s (%s on %s)\n", appr.ID, appr.Action, appr.Resource)
	}
	return nil
}

func runApproveDeny(token, reason string, jsonOutput bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	ctx := context.Background()
	currentUser := getCurrentApprover()

	if err := engine.Deny(ctx, token, currentUser, reason); err != nil {
		return outputError(err, jsonOutput)
	}

	appr, err := engine.Check(ctx, token)
	if err != nil {
		return outputError(err, jsonOutput)
	}

	recordApprovalStateAudit(
		store,
		currentUser,
		"approval.denied",
		string(state.ApprovalPending),
		string(appr.Status),
		fmt.Sprintf("denied %s on %s", appr.Action, appr.Resource),
		reason,
		appr,
	)
	_ = audit.LogEvent("", audit.EventTypeStateChange, audit.ActorUser, "approval.deny", map[string]interface{}{
		"approval_id":  token,
		"action":       appr.Action,
		"resource":     appr.Resource,
		"requested_by": appr.RequestedBy,
		"denied_by":    currentUser,
		"reason":       reason,
		"requires_slb": appr.RequiresSLB,
	}, nil)

	result := ApprovalResult{
		Success:  true,
		ID:       token,
		Action:   appr.Action,
		Resource: appr.Resource,
		Status:   string(appr.Status),
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("✓ Denied: %s\n", token)
	fmt.Printf("  Action:   %s\n", appr.Action)
	fmt.Printf("  Resource: %s\n", appr.Resource)
	if reason != "" {
		fmt.Printf("  Reason:   %s\n", reason)
	}
	return nil
}

func runApproveShow(token string, jsonOutput bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	ctx := context.Background()
	appr, err := engine.Check(ctx, token)
	if err != nil {
		return outputError(err, jsonOutput)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success":  true,
			"approval": appr,
		})
	}

	slb := ""
	if appr.RequiresSLB {
		slb = " [SLB Required]"
	}

	fmt.Printf("Approval Request: %s%s\n\n", appr.ID, slb)
	fmt.Printf("  Action:       %s\n", appr.Action)
	fmt.Printf("  Resource:     %s\n", appr.Resource)
	fmt.Printf("  Reason:       %s\n", appr.Reason)
	fmt.Printf("  Requested By: %s\n", appr.RequestedBy)
	fmt.Printf("  Status:       %s\n", appr.Status)
	fmt.Printf("  Created At:   %s\n", appr.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Expires At:   %s\n", appr.ExpiresAt.Format(time.RFC3339))

	if appr.ApprovedBy != "" {
		fmt.Printf("  Decided By:   %s\n", appr.ApprovedBy)
	}
	if appr.ApprovedAt != nil {
		fmt.Printf("  Decided At:   %s\n", appr.ApprovedAt.Format(time.RFC3339))
	}
	if appr.DeniedReason != "" {
		fmt.Printf("  Deny Reason:  %s\n", appr.DeniedReason)
	}
	if appr.CorrelationID != "" {
		fmt.Printf("  Correlation:  %s\n", appr.CorrelationID)
	}

	return nil
}

func runApproveHistory(jsonOutput bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	// Full history: pending AND resolved records (approved, denied,
	// consumed, expired), each with status/decider/timestamps
	// (bd-ws7-docs-ux-truth-tqh3l.9).
	ctx := context.Background()
	history, err := engine.History(ctx)
	if err != nil {
		return outputError(err, jsonOutput)
	}

	if jsonOutput {
		if history == nil {
			history = []state.Approval{}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success":   true,
			"approvals": history,
			"count":     len(history),
		})
	}

	fmt.Println("Approval History:")
	if len(history) == 0 {
		fmt.Println("  (no approval requests recorded)")
		return nil
	}
	for _, appr := range history {
		fmt.Printf("\n  %s [%s]\n", appr.ID, appr.Status)
		fmt.Printf("    Action:    %s\n", appr.Action)
		fmt.Printf("    Resource:  %s\n", appr.Resource)
		fmt.Printf("    Requested: %s by %s\n", appr.CreatedAt.Format(time.RFC3339), appr.RequestedBy)
		if appr.ApprovedBy != "" {
			fmt.Printf("    Decided:   by %s\n", appr.ApprovedBy)
		}
		if appr.ApprovedAt != nil {
			fmt.Printf("    Decided At: %s\n", appr.ApprovedAt.Format(time.RFC3339))
		}
		if appr.DeniedReason != "" {
			fmt.Printf("    Deny Reason: %s\n", appr.DeniedReason)
		}
	}
	return nil
}

func getCurrentApprover() string {
	// Try to get from environment or config
	if user := os.Getenv("NTM_USER"); user != "" {
		return user
	}
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "unknown"
}

func outputError(err error, jsonOutput bool) error {
	if jsonOutput {
		result := map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
		return emitJSONFailureEnvelopeWithCause(result, err)
	}
	return err
}
