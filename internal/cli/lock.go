package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/spf13/cobra"
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
	Success   bool                            `json:"success"`
	Session   string                          `json:"session"`
	Agent     string                          `json:"agent"`
	Granted   []agentmail.FileReservation     `json:"granted,omitempty"`
	Conflicts []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	TTL       string                          `json:"ttl"`
	ExpiresAt *time.Time                      `json:"expires_at,omitempty"`
	Error     string                          `json:"error,omitempty"`
}

func runLock(session string, patterns []string, reason, ttlStr string, shared bool) error {
	ttlDuration, err := time.ParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid TTL format '%s': use format like 30m, 1h", ttlStr)
	}
	ttlSeconds := int(ttlDuration.Seconds())
	if ttlSeconds < 60 {
		return fmt.Errorf("TTL must be at least 1 minute")
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	sessionAgent, err := agentmail.LoadSessionAgent(session, wd)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}
	if sessionAgent == nil {
		if IsJSONOutput() {
			result := LockResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(wd))
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := LockResult{Success: false, Session: session, Agent: sessionAgent.AgentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("Agent Mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agentmail.FileReservationOptions{
		ProjectKey: wd,
		AgentName:  sessionAgent.AgentName,
		Paths:      patterns,
		TTLSeconds: ttlSeconds,
		Exclusive:  !shared,
		Reason:     reason,
	}

	reservation, err := client.ReservePaths(ctx, opts)

	result := LockResult{Session: session, Agent: sessionAgent.AgentName, TTL: ttlStr}

	if err != nil {
		if reservation != nil && len(reservation.Conflicts) > 0 {
			result.Success = false
			result.Granted = reservation.Granted
			result.Conflicts = reservation.Conflicts
		} else {
			result.Success = false
			result.Error = err.Error()
		}
	} else {
		result.Success = true
		result.Granted = reservation.Granted
		if len(reservation.Granted) > 0 {
			result.ExpiresAt = &reservation.Granted[0].ExpiresTS
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	return printLockResult(result, shared)
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

	return fmt.Errorf("%s", result.Error)
}
