package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newApproveCmd() *cobra.Command {
	var (
		reason    string
		robotJSON bool
	)

	cmd := &cobra.Command{
		Use:   "approve [token]",
		Short: "Manage approval requests for dangerous operations",
		Long: `Manage approval requests for dangerous operations like force-release,
force-push, and other sensitive actions that require human sign-off.

When called with a token argument, approves that request.
Use subcommands for other operations.

Subcommands:
  approve list             List all pending approvals
  approve deny <token>     Deny a pending request
  approve history          Show approval history
  approve show <token>     Show details of an approval

Examples:
  ntm approve abc123                  # Approve request abc123
  ntm approve list                    # List pending approvals
  ntm approve deny abc123 --reason "Too risky"
  ntm approve show abc123             # Show approval details`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runApprove(args[0], robotJSON)
		},
	}
	cmd.Flags().BoolVar(&robotJSON, "json", false, "Output in JSON format")

	// list - list pending approvals
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all pending approvals",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveList(robotJSON)
		},
	}
	listCmd.Flags().BoolVar(&robotJSON, "json", false, "Output in JSON format")

	// deny <token> - deny a request
	denyCmd := &cobra.Command{
		Use:   "deny <token>",
		Short: "Deny a pending request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveDeny(args[0], reason, robotJSON)
		},
	}
	denyCmd.Flags().StringVar(&reason, "reason", "", "Reason for denial")
	denyCmd.Flags().BoolVar(&robotJSON, "json", false, "Output in JSON format")

	// show <token> - show details
	showCmd := &cobra.Command{
		Use:   "show <token>",
		Short: "Show details of an approval request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveShow(args[0], robotJSON)
		},
	}
	showCmd.Flags().BoolVar(&robotJSON, "json", false, "Output in JSON format")

	// history - show history
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Show approval history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApproveHistory(robotJSON)
		},
	}
	historyCmd.Flags().BoolVar(&robotJSON, "json", false, "Output in JSON format")

	cmd.AddCommand(listCmd, denyCmd, showCmd, historyCmd)
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

func getApprovalEngine() (*approval.Engine, *state.Store, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, nil, fmt.Errorf("open state store: %w", err)
	}

	// Ensure migrations are applied
	if err := store.Migrate(); err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("apply migrations: %w", err)
	}

	engine := approval.New(store, nil, nil, approval.DefaultConfig())
	return engine, store, nil
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

func runApproveList(jsonOutput bool) error {
	engine, store, err := getApprovalEngine()
	if err != nil {
		return outputError(err, jsonOutput)
	}
	defer store.Close()

	ctx := context.Background()
	pending, err := engine.ListPending(ctx)
	if err != nil {
		return outputError(err, jsonOutput)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success": true,
			"pending": pending,
			"count":   len(pending),
		})
	}

	if len(pending) == 0 {
		fmt.Println("No pending approvals")
		return nil
	}

	fmt.Printf("Pending Approvals (%d):\n\n", len(pending))
	for _, a := range pending {
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

	// For now, we can only list pending. Full history would need additional store methods.
	// This is a minimal implementation that shows we'd need to extend the store.
	ctx := context.Background()
	pending, _ := engine.ListPending(ctx)

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"success": true,
			"pending": pending,
			"note":    "Full history requires additional state store methods",
		})
	}

	fmt.Println("Approval History:")
	fmt.Println("  (Full history tracking requires state store extension)")
	fmt.Printf("  Currently pending: %d\n", len(pending))
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
		if encErr := json.NewEncoder(os.Stdout).Encode(result); encErr != nil {
			return fmt.Errorf("encoding JSON: %w (original error: %v)", encErr, err)
		}
		return nil // Don't return error for JSON output
	}
	return err
}
