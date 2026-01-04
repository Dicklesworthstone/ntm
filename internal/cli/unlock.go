package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func newUnlockCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "unlock <session> [patterns...]",
		Short: "Release file reservations",
		Long: `Release file path reservations held by this session's agent.

Without patterns, you must use --all to release all reservations.

Examples:
  ntm unlock myproject "src/api/**"       # Release specific pattern
  ntm unlock myproject --all              # Release all reservations
  ntm unlock myproject "*.go" "*.json"    # Release multiple patterns`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			patterns := args[1:]
			return runUnlock(session, patterns, all)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Release all reservations for this session")

	return cmd
}

// UnlockResult represents the result of an unlock operation.
type UnlockResult struct {
	Success    bool       `json:"success"`
	Session    string     `json:"session"`
	Agent      string     `json:"agent"`
	Released   int        `json:"released"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func runUnlock(session string, patterns []string, all bool) error {
	if len(patterns) == 0 && !all {
		return fmt.Errorf("specify patterns to release or use --all")
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
			result := UnlockResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(wd)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := UnlockResult{Success: false, Session: session, Agent: sessionAgent.AgentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("Agent Mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var pathsToRelease []string
	if !all {
		pathsToRelease = patterns
	}

	err = client.ReleaseReservations(ctx, wd, sessionAgent.AgentName, pathsToRelease, nil)

	now := time.Now()
	result := UnlockResult{Session: session, Agent: sessionAgent.AgentName, ReleasedAt: &now}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
		if all {
			result.Released = -1
		} else {
			result.Released = len(patterns)
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if result.Success {
		if all {
			fmt.Printf("Released all reservations for %s\n", result.Agent)
		} else {
			fmt.Printf("Released %d reservation(s)\n", result.Released)
			for _, p := range patterns {
				fmt.Printf("  [_] %s\n", p)
			}
		}
		return nil
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("unlock failed")
}
