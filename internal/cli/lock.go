package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func newLockCmd() *cobra.Command {
	var (
		reason string
		ttl    string
		shared bool
	)

	cmd := &cobra.Command{
		Use:   "lock <session> <patterns...>",
		Short: "Reserve files for editing via Agent Mail",
		Long: `Reserve file paths to signal intent before editing, avoiding conflicts with other agents.

File reservations are advisory locks that help coordinate multi-agent work.
Patterns support glob syntax (e.g., "src/**/*.go", "*.json").

Examples:
  ntm lock myproject "src/api/**" --reason "Implementing user endpoints"
  ntm lock myproject "src/api/**" "tests/api/**" --ttl 2h
  ntm lock myproject "docs/**" --shared     # Non-exclusive (read) lock
  ntm lock myproject "config/*.json"        # Default 1 hour TTL`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			patterns := args[1:]
			return runLock(session, patterns, reason, ttl, shared)
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Reason for the lock")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "Time to live (e.g., 30m, 2h, 24h)")
	cmd.Flags().BoolVar(&shared, "shared", false, "Non-exclusive (read) lock")

	return cmd
}

// LockResult represents the result of a lock operation.
type LockResult struct {
	Success    bool                            `json:"success"`
	Session    string                          `json:"session"`
	Agent      string                          `json:"agent"`
	ProjectKey string                          `json:"project_key,omitempty"`
	Granted    []agentmail.FileReservation     `json:"granted,omitempty"`
	Conflicts  []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	TTL        string                          `json:"ttl"`
	ExpiresAt  *time.Time                      `json:"expires_at,omitempty"`
	Error      string                          `json:"error,omitempty"`
	ErrorCode  string                          `json:"error_code,omitempty"`
	ReasonCode string                          `json:"reason_code,omitempty"`
	NextAction string                          `json:"next_action,omitempty"`
	Detail     string                          `json:"detail,omitempty"`
}

func runLock(session string, patterns []string, reason, ttlStr string, shared bool) error {
	ttlDuration, err := util.ParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid TTL format '%s': use format like 30m, 1h, 1d", ttlStr)
	}
	ttlSeconds := int(ttlDuration.Seconds())
	if ttlSeconds < 60 {
		return fmt.Errorf("TTL must be at least 1 minute")
	}

	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}
	if sessionAgent == nil {
		if IsJSONOutput() {
			failure := locksSessionNotConfiguredFailure(
				session,
				projectKey,
				fmt.Sprintf("ntm lock %s <patterns...> --json", locksShellQuote(session)),
			)
			result := LockResult{
				Success:    false,
				Session:    session,
				ProjectKey: projectKey,
				Error:      failure.Message,
				ErrorCode:  failure.ErrorCode,
				ReasonCode: failure.ReasonCode,
				NextAction: failure.NextAction,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailReservationClient(projectKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  sessionAgent.AgentName,
		Paths:      patterns,
		TTLSeconds: ttlSeconds,
		Exclusive:  !shared,
		Reason:     reason,
	}

	reservation, err := client.ReservePaths(ctx, opts)
	if err != nil {
		if refreshed, recovered, recoverErr := refreshReservationSessionAgent(ctx, client, session, projectKey, err); recovered {
			if recoverErr != nil {
				err = fmt.Errorf("%w; recovery failed: %v", err, recoverErr)
			} else {
				sessionAgent = refreshed
				opts.AgentName = refreshed.AgentName
				reservation, err = client.ReservePaths(ctx, opts)
			}
		}
	}
	if err == nil {
		if verifyErr := verifyGrantedReservationsVisible(ctx, client, projectKey, opts.AgentName, reservation); verifyErr != nil {
			err = verifyErr
		}
	}

	result := LockResult{Session: session, Agent: sessionAgent.AgentName, ProjectKey: projectKey, TTL: ttlStr}

	if err != nil {
		if reservation != nil && len(reservation.Conflicts) > 0 {
			result.Success = false
			result.Granted = reservation.Granted
			result.Conflicts = reservation.Conflicts
			result.Error = "Agent Mail file reservation conflict"
			result.ErrorCode = "reservation_conflict"
			result.ReasonCode = "reservation_conflict"
			result.NextAction = "Coordinate with the reservation holder, wait for expiry, or rerun with --shared for read-only access."
		} else {
			failure := classifyLockFailure(session, projectKey, err)
			result.Success = false
			result.Error = failure.Message
			result.ErrorCode = failure.ErrorCode
			result.ReasonCode = failure.ReasonCode
			result.NextAction = failure.NextAction
			result.Detail = failure.Detail
		}
	} else {
		result.Success = true
		result.Granted = reservation.Granted
		if len(reservation.Granted) > 0 {
			t := reservation.Granted[0].ExpiresTS.Time
			result.ExpiresAt = &t
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		if !result.Success {
			return jsonFailureExit()
		}
		return nil
	}

	return printLockResult(result, shared)
}

func classifyLockFailure(session, projectKey string, err error) locksListFailure {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	lower := strings.ToLower(detail)
	failure := locksListFailure{
		Message:    "Agent Mail file reservation failed",
		ErrorCode:  "agent_mail_reservation_failed",
		ReasonCode: "agent_mail_reservation_failed",
		NextAction: fmt.Sprintf(
			"Run `am doctor check --json`, verify Agent Mail reservations, then rerun `ntm lock %s <patterns...> --json`.",
			locksShellQuote(session),
		),
		Detail: detail,
	}

	switch {
	case strings.Contains(lower, "fromisoformat") || strings.Contains(lower, "timestamp"):
		failure.Message = "Agent Mail reservation timestamp payload is malformed"
		failure.ErrorCode = "agent_mail_reservation_timestamp_malformed"
		failure.ReasonCode = "agent_mail_reservation_timestamp_malformed"
		failure.NextAction = "Capture the sanitized reservation payload, repair Agent Mail timestamp parsing or archive rows, then rerun the lock command."
	case strings.Contains(lower, "recovery failed") && (strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout")):
		failure.Message = "Agent Mail identity registration timed out"
		failure.ErrorCode = "agent_identity_registration_timeout"
		failure.ReasonCode = "agent_identity_registration_timeout"
		failure.NextAction = fmt.Sprintf(
			"Run `am doctor check --json`, verify Agent Mail register_agent latency, then rerun `ntm lock %s <patterns...> --json`.",
			locksShellQuote(session),
		)
	case strings.Contains(lower, "registration_token"):
		failure.Message = "Agent Mail reservation requires a registration token"
		failure.ErrorCode = "agent_mail_registration_token_required"
		failure.ReasonCode = "agent_mail_registration_token_required"
		failure.NextAction = fmt.Sprintf(
			"Run `%s`, then rerun `ntm lock %s <patterns...> --json`.",
			locksListBootstrapCommand(projectKey),
			locksShellQuote(session),
		)
	case strings.Contains(lower, "agent") && strings.Contains(lower, "not found"):
		failure.Message = "Agent Mail session identity is stale or unregistered"
		failure.ErrorCode = "agent_identity_not_registered"
		failure.ReasonCode = "agent_identity_not_registered"
		failure.NextAction = fmt.Sprintf(
			"Run `%s`, then rerun `ntm lock %s <patterns...> --json`.",
			locksListBootstrapCommand(projectKey),
			locksShellQuote(session),
		)
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "http 401"):
		failure.Message = "Agent Mail file reservation is unauthorized"
		failure.ErrorCode = "agent_mail_unauthorized"
		failure.ReasonCode = "agent_mail_unauthorized"
		failure.NextAction = "Check Agent Mail token configuration, then rerun the lock command."
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		failure.Message = "Agent Mail file reservation timed out"
		failure.ErrorCode = "agent_mail_reservation_timeout"
		failure.ReasonCode = "agent_mail_reservation_timeout"
		failure.NextAction = "Check Agent Mail server load and reservation resource health, then rerun the lock command."
	case strings.Contains(lower, "agent mail server unavailable") || strings.Contains(lower, "connection refused"):
		failure.Message = "Agent Mail reservation service is unavailable"
		failure.ErrorCode = "agent_mail_unavailable"
		failure.ReasonCode = "agent_mail_unavailable"
		failure.NextAction = "Start or repair Agent Mail, then rerun the lock command."
	}

	return failure
}

const reservationReadbackSettleDelay = 250 * time.Millisecond

func verifyGrantedReservationsVisible(ctx context.Context, client *agentmail.Client, projectKey, agentName string, result *agentmail.ReservationResult) error {
	if client == nil || result == nil || len(result.Granted) == 0 {
		return nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(reservationReadbackSettleDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("reservation grant readback failed: %w", ctx.Err())
			case <-timer.C:
			}
		}

		reservations, err := client.ListReservations(ctx, projectKey, "", true)
		if err != nil {
			return fmt.Errorf("reservation grant readback failed: %w", err)
		}
		for _, granted := range result.Granted {
			if !grantedReservationVisible(granted, reservations, agentName) {
				return fmt.Errorf("reservation grant not stably visible after create: id=%d path=%q", granted.ID, granted.PathPattern)
			}
		}
	}
	return nil
}

func grantedReservationVisible(granted agentmail.FileReservation, reservations []agentmail.FileReservation, agentName string) bool {
	for _, reservation := range reservations {
		if granted.PathPattern != "" && reservation.PathPattern == granted.PathPattern {
			if granted.ID > 0 && reservation.ID > 0 && reservation.ID != granted.ID {
				continue
			}
			return agentName == "" || reservation.AgentName == "" || reservation.AgentName == agentName
		}
		if granted.PathPattern == "" && granted.ID > 0 && reservation.ID == granted.ID {
			return true
		}
	}
	return false
}

func printLockResult(result LockResult, shared bool) error {
	lockType := "exclusive"
	if shared {
		lockType = "shared"
	}

	if result.Success {
		fmt.Printf("Reserved %d path(s) (%s)\n", len(result.Granted), lockType)
		fmt.Printf("  Agent: %s\n", result.Agent)
		if result.ExpiresAt != nil {
			fmt.Printf("  Expires: %s (%s)\n", result.ExpiresAt.Format(time.RFC3339), result.TTL)
		}
		for _, r := range result.Granted {
			fmt.Printf("  [X] %s\n", r.PathPattern)
			if r.Reason != "" {
				fmt.Printf("      %s\n", r.Reason)
			}
		}
		return nil
	}

	if len(result.Conflicts) > 0 {
		fmt.Printf("Conflict detected!\n\n")
		for _, c := range result.Conflicts {
			fmt.Printf("  Pattern: %s\n", c.Path)
			fmt.Printf("  Held by: %s\n", strings.Join(c.Holders, ", "))
		}
		fmt.Println("\nOptions:")
		fmt.Println("  1. Wait for existing locks to expire")
		fmt.Println("  2. Request release from holder")
		fmt.Println("  3. Use --shared for read-only access")
		return fmt.Errorf("reservation conflicts detected")
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("lock failed")
}
