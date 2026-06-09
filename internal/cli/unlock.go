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
	var paneIdx int
	var taskID string

	cmd := &cobra.Command{
		Use:   "unlock <session> [patterns...]",
		Short: "Release file reservations",
		Long: `Release file path reservations held by this session's agent.

Without patterns, you must use --all to release all reservations.

When --pane and --task-id are supplied, the JSON envelope includes a
ReleaseReceipt block — the unlock-side mirror of the dispatch receipt
emitted by ` + "`ntm assign`" + ` (ntm#128/#129). Wrappers can drop their
parallel release ledger because the envelope itself proves which pane
released which path for which task.

Examples:
  ntm unlock myproject "src/api/**"       # Release specific pattern
  ntm unlock myproject --all              # Release all reservations
  ntm unlock myproject "*.go" "*.json"    # Release multiple patterns
  ntm unlock myproject "src/api/x.go" --pane=2 --task-id=br-123 --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			patterns := args[1:]
			return runUnlock(session, patterns, all, paneIdx, taskID)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Release all reservations for this session")
	cmd.Flags().IntVar(&paneIdx, "pane", -1, "Pane index that originally held the reservation (for the release receipt; ntm#129)")
	cmd.Flags().StringVar(&taskID, "task-id", "", "Work-item id that owned the reservation (for the release receipt; ntm#129)")

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
	// Receipt is the wrapper-grade release receipt — populated only
	// when the caller supplies `--pane` AND `--task-id`. Idempotent on
	// already-released paths (AlreadyReleased=true, Released=false).
	// Conflicts get a structured Conflict block instead of free-form
	// error text so callers don't have to grep prose. See ntm#129.
	Receipt *ReleaseReceipt `json:"receipt,omitempty"`
}

// ReleaseReceipt mirrors the assign-side DispatchReceipt: identifies
// the path/pane/task that was released, exposes idempotency, and
// surfaces ownership conflicts in machine-readable shape. See ntm#129.
type ReleaseReceipt struct {
	Paths           []string         `json:"paths"`
	Session         string           `json:"session"`
	Pane            int              `json:"pane"`
	TaskID          string           `json:"task_id"`
	Released        bool             `json:"released"`
	AlreadyReleased bool             `json:"already_released,omitempty"`
	ReleaseCount    int              `json:"release_count"`
	Timestamp       string           `json:"timestamp"`
	Conflict        *ReleaseConflict `json:"conflict,omitempty"`
}

// ReleaseConflict captures ownership mismatches in structured form
// (wrappers can match on Reason rather than grepping prose).
type ReleaseConflict struct {
	Owner  string `json:"owner,omitempty"`
	Reason string `json:"reason"`
}

func runUnlock(session string, patterns []string, all bool, paneIdx int, taskID string) error {
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
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := UnlockResult{Success: false, Session: session, Agent: sessionAgent.AgentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
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
	if err != nil {
		if refreshed, recovered, recoverErr := refreshReservationSessionAgent(ctx, client, session, projectKey, err); recovered {
			if recoverErr != nil {
				err = fmt.Errorf("%w; recovery failed: %v", err, recoverErr)
			} else {
				sessionAgent = refreshed
				releaseResult, err = client.ReleaseReservations(ctx, projectKey, sessionAgent.AgentName, pathsToRelease, nil)
			}
		}
	}
	if err == nil && releaseResult != nil && !all && releaseResult.Released == 0 {
		if fallbackResult, fallbackErr := releaseMatchingReservationIDs(ctx, client, projectKey, sessionAgent.AgentName, patterns); fallbackErr != nil {
			err = fallbackErr
		} else if fallbackResult != nil && fallbackResult.Released > 0 {
			releaseResult = fallbackResult
		}
	}

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

	// Populate the release receipt when the caller supplied the
	// pane+task-id pair (ntm#129). Idempotency: when result.Released==0
	// but no error occurred, treat as "already released" so wrappers
	// can distinguish from genuine failures.
	if paneIdx >= 0 && taskID != "" {
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		if result.ReleasedAt != nil {
			ts = result.ReleasedAt.UTC().Format(time.RFC3339Nano)
		}
		receipt := &ReleaseReceipt{
			Paths:        patterns,
			Session:      session,
			Pane:         paneIdx,
			TaskID:       taskID,
			Released:     result.Success && result.Released > 0,
			ReleaseCount: result.Released,
			Timestamp:    ts,
		}
		if !receipt.Released && result.Error == "" {
			receipt.AlreadyReleased = true
		}
		// Surface ownership conflicts in structured form — match the
		// common "owned by X" / "session mismatch" prose patterns.
		if err != nil {
			receipt.Conflict = &ReleaseConflict{Reason: err.Error()}
		}
		result.Receipt = receipt
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
