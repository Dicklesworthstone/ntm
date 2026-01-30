package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func newLocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "locks",
		Short: "Manage file reservations",
		Long: `Manage file path reservations for multi-agent coordination.

File reservations are advisory locks that help prevent conflicts when multiple
agents work on the same codebase.

Subcommands:
  list          Show current file reservations
  force-release Forcibly release a stale reservation
  renew         Extend the TTL of active reservations

Examples:
  ntm locks list myproject               # Show session's reservations
  ntm locks list myproject --all-agents  # Show all project reservations
  ntm locks force-release myproject 42   # Force release reservation #42
  ntm locks renew myproject              # Extend all reservations by 30m`,
	}

	cmd.AddCommand(
		newLocksListCmd(),
		newLocksForceReleaseCmd(),
		newLocksRenewCmd(),
	)

	return cmd
}

func newLocksListCmd() *cobra.Command {
	var allAgents bool

	cmd := &cobra.Command{
		Use:   "list <session>",
		Short: "Show current file reservations",
		Long: `Display file path reservations for this session or all agents in the project.

Examples:
  ntm locks list myproject               # Show session's reservations
  ntm locks list myproject --all-agents  # Show all project reservations
  ntm locks list myproject --json        # JSON output for scripts`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			return runLocks(session, allAgents)
		},
	}

	cmd.Flags().BoolVar(&allAgents, "all-agents", false, "Show reservations for all agents")

	return cmd
}

// LocksResult contains the list of active file reservations.
type LocksResult struct {
	Success      bool                        `json:"success"`
	Session      string                      `json:"session"`
	Agent        string                      `json:"agent,omitempty"`
	ProjectKey   string                      `json:"project_key"`
	Reservations []agentmail.FileReservation `json:"reservations"`
	Count        int                         `json:"count"`
	Error        string                      `json:"error,omitempty"`
}

func runLocks(session string, allAgents bool) error {
	wd := GetProjectRoot()
	if wd == "" {
		return fmt.Errorf("getting project root failed")
	}

	sessionAgent, err := agentmail.LoadSessionAgent(session, wd)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	}

	client := newAgentMailClient(wd)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := LocksResult{Success: false, Session: session, Agent: agentName, ProjectKey: wd, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reservations, err := fetchActiveReservations(ctx, client, wd, agentName, allAgents)

	result := LocksResult{
		Session:      session,
		Agent:        agentName,
		ProjectKey:   wd,
		Reservations: reservations,
		Count:        len(reservations),
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	return printLocksResult(result, allAgents)
}

func fetchActiveReservations(ctx context.Context, client *agentmail.Client, projectKey, agentName string, allAgents bool) ([]agentmail.FileReservation, error) {
	reservations, err := client.ListReservations(ctx, projectKey, agentName, allAgents)
	if err != nil {
		return nil, fmt.Errorf("listing reservations: %w", err)
	}
	return reservations, nil
}

func printLocksResult(result LocksResult, allAgents bool) error {
	if !result.Success {
		if result.Error != "" {
			return fmt.Errorf("%s", result.Error)
		}
		return fmt.Errorf("failed to list reservations")
	}

	scope := "session"
	if allAgents {
		scope = "project"
	}

	if result.Count == 0 {
		fmt.Printf("No active file reservations (%s scope)\n", scope)
		if result.Agent != "" {
			fmt.Printf("   Agent: %s\n", result.Agent)
		}
		fmt.Printf("   Project: %s\n", result.ProjectKey)
		fmt.Println("\nTip: Use 'ntm lock <session> <pattern>' to reserve files")
		return nil
	}

	fmt.Printf("File Reservations: %d active (%s scope)\n", result.Count, scope)
	fmt.Println(strings.Repeat("-", 60))

	for _, r := range result.Reservations {
		lockType := "Exclusive"
		if !r.Exclusive {
			lockType = "Shared"
		}

		remaining := time.Until(r.ExpiresTS.Time)
		expiresStr := formatLockDuration(remaining)

		fmt.Printf("[#%d] %s\n", r.ID, r.PathPattern)
		fmt.Printf("   Agent: %s | %s | Expires in %s\n", r.AgentName, lockType, expiresStr)
		if r.Reason != "" {
			fmt.Printf("   Reason: %s\n", r.Reason)
		}
		fmt.Println(strings.Repeat("-", 60))
	}

	return nil
}

func formatLockDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}

	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func newLocksForceReleaseCmd() *cobra.Command {
	var (
		note        string
		noNotify    bool
		skipConfirm bool
	)

	cmd := &cobra.Command{
		Use:   "force-release <session> <reservation-id>",
		Short: "Forcibly release a stale reservation",
		Long: `Forcibly release a file reservation held by another agent.

This command is intended for situations where an agent has become inactive
or unresponsive while holding a reservation that blocks other work.

The server validates inactivity heuristics before allowing the release.
By default, the previous holder is notified about the forced release.

Examples:
  ntm locks force-release myproject 42              # Force release reservation #42
  ntm locks force-release myproject 42 --note "Agent crashed"
  ntm locks force-release myproject 42 --no-notify  # Don't notify previous holder`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			reservationIDStr := args[1]

			reservationID, err := strconv.Atoi(reservationIDStr)
			if err != nil {
				return fmt.Errorf("invalid reservation ID '%s': must be a number", reservationIDStr)
			}
			if reservationID < 1 {
				return fmt.Errorf("invalid reservation ID '%s': must be a positive number", reservationIDStr)
			}

			return runForceRelease(session, reservationID, note, !noNotify, skipConfirm)
		},
	}

	cmd.Flags().StringVar(&note, "note", "", "Explanation for the force-release")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "Don't notify the previous holder")
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

// ForceReleaseResult is the JSON output for force-release.
type ForceReleaseResult struct {
	Success        bool       `json:"success"`
	Session        string     `json:"session"`
	Agent          string     `json:"agent"`
	ReservationID  int        `json:"reservation_id"`
	PreviousHolder string     `json:"previous_holder,omitempty"`
	PathPattern    string     `json:"path_pattern,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	Notified       bool       `json:"notified,omitempty"`
	Error          string     `json:"error,omitempty"`
}

func runForceRelease(session string, reservationID int, note string, notify, skipConfirm bool) error {
	wd := GetProjectRoot()
	if wd == "" {
		return fmt.Errorf("getting project root failed")
	}

	sessionAgent, err := agentmail.LoadSessionAgent(session, wd)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	} else {
		if IsJSONOutput() {
			result := ForceReleaseResult{Success: false, Session: session, ReservationID: reservationID, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(wd)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := ForceReleaseResult{Success: false, Session: session, Agent: agentName, ReservationID: reservationID, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	// Confirmation prompt (unless skipped or JSON mode)
	if !skipConfirm && !IsJSONOutput() {
		fmt.Printf("Force release reservation #%d?\n", reservationID)
		fmt.Printf("  This will notify the previous holder: %v\n", notify)
		if note != "" {
			fmt.Printf("  Note: %s\n", note)
		}
		fmt.Print("\nContinue? [y/N] ")

		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agentmail.ForceReleaseOptions{
		ProjectKey:     wd,
		AgentName:      agentName,
		ReservationID:  reservationID,
		Note:           note,
		NotifyPrevious: notify,
	}

	releaseResult, err := client.ForceReleaseReservation(ctx, opts)

	result := ForceReleaseResult{
		Session:       session,
		Agent:         agentName,
		ReservationID: reservationID,
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else if releaseResult != nil {
		result.Success = releaseResult.Success
		result.PreviousHolder = releaseResult.PreviousHolder
		result.PathPattern = releaseResult.PathPattern
		if releaseResult.ReleasedAt != nil {
			t := releaseResult.ReleasedAt.Time
			result.ReleasedAt = &t
		}
		result.Notified = releaseResult.Notified
		// Server may return success=false if reservation is not stale enough
		if !releaseResult.Success {
			result.Error = "force-release denied: reservation may not be stale or agent may still be active"
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if result.Success {
		fmt.Printf("Force released reservation #%d\n", reservationID)
		if result.PathPattern != "" {
			fmt.Printf("  Pattern: %s\n", result.PathPattern)
		}
		if result.PreviousHolder != "" {
			fmt.Printf("  Previous holder: %s\n", result.PreviousHolder)
		}
		if result.Notified {
			fmt.Println("  Previous holder has been notified")
		}
		return nil
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("force-release failed")
}

func newLocksRenewCmd() *cobra.Command {
	var extendMinutes int

	cmd := &cobra.Command{
		Use:   "renew <session>",
		Short: "Extend TTL of active reservations",
		Long: `Extend the time-to-live of all active file reservations for this session.

This is useful when work is taking longer than expected and you need more time
before the reservations expire.

Examples:
  ntm locks renew myproject              # Extend by 30 minutes (default)
  ntm locks renew myproject --extend 60  # Extend by 60 minutes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			return runRenewLocks(session, extendMinutes)
		},
	}

	cmd.Flags().IntVar(&extendMinutes, "extend", 30, "Minutes to extend reservations")

	return cmd
}

// RenewResult is the JSON output for renew.
type RenewResult struct {
	Success       bool   `json:"success"`
	Session       string `json:"session"`
	Agent         string `json:"agent"`
	ExtendMinutes int    `json:"extend_minutes"`
	Error         string `json:"error,omitempty"`
}

func runRenewLocks(session string, extendMinutes int) error {
	if extendMinutes < 1 {
		return fmt.Errorf("extend time must be at least 1 minute")
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	sessionAgent, err := agentmail.LoadSessionAgent(session, wd)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}

	agentName := ""
	if sessionAgent != nil {
		agentName = sessionAgent.AgentName
	} else {
		if IsJSONOutput() {
			result := RenewResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(wd)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := RenewResult{Success: false, Session: session, Agent: agentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	extendSeconds := extendMinutes * 60
	_, err = client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
		ProjectKey:    wd,
		AgentName:     agentName,
		ExtendSeconds: extendSeconds,
	})

	result := RenewResult{
		Session:       session,
		Agent:         agentName,
		ExtendMinutes: extendMinutes,
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if result.Success {
		fmt.Printf("Extended reservations by %d minutes\n", extendMinutes)
		fmt.Printf("  Agent: %s\n", agentName)
		return nil
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("renewal failed")
}
