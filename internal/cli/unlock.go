package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
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
			result := UnlockResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := UnlockResult{Success: false, Session: session, Agent: sessionAgent.AgentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var pathsToRelease []string
	if !all {
		pathsToRelease = patterns
	}

	releaseResult, err := client.ReleaseReservations(ctx, projectKey, sessionAgent.AgentName, pathsToRelease, nil)
	result := UnlockResult{Session: session, Agent: sessionAgent.AgentName}
	if releaseResult != nil && releaseResult.ReleasedAt != nil {
		t := releaseResult.ReleasedAt.Time
		result.ReleasedAt = &t
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		if releaseResult == nil {
			result.Success = false
			result.Error = "release returned no result"
		} else {
			result.Released = releaseResult.Released
			if !all && result.Released == 0 {
				result.Success = false
				result.Error = fmt.Sprintf("released 0 reservations for %d requested pattern(s)", len(patterns))
			} else {
				result.Success = true
			}
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if result.Success {
		if all {
			if result.Released == 0 {
				fmt.Printf("No active reservations to release for %s\n", result.Agent)
			} else {
				fmt.Printf("Released %d reservation(s) for %s\n", result.Released, result.Agent)
			}
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
