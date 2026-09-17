package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// WorkflowCheckpointResult exposes execution evidence without printing setup
// variables or prompt contents, which may contain sensitive project context.
type WorkflowCheckpointResult struct {
	Success         bool                     `json:"success"`
	Workflow        string                   `json:"workflow"`
	Session         string                   `json:"session"`
	CurrentStage    string                   `json:"current_stage"`
	Paused          bool                     `json:"paused"`
	Completed       bool                     `json:"completed"`
	Dispatches      []workflow.StageDispatch `json:"dispatches"`
	CheckpointToken string                   `json:"checkpoint_token"`
}

// newWorkflowCheckpointCmd builds the read-only status and explicit recovery
// surfaces over the same checkpoint used by workflow run. Neither sends keys
// or needs a running tmux server; --project-root defaults to the current dir.
func newWorkflowCheckpointCmd(action string) *cobra.Command {
	var session, projectRoot, pane, token, outcome string
	cmd := &cobra.Command{
		Use:   action,
		Args:  cobra.NoArgs,
		Short: "Inspect a saved workflow execution checkpoint",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			result, err := getWorkflowCheckpoint(ctx, projectRoot, session, pane, token, outcome, action == "recover")
			if err != nil {
				if jsonOutput {
					return emitJSONFailureEnvelopeWithCause(map[string]interface{}{"success": false, "error": err.Error()}, err)
				}
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(result)
			}
			fmt.Printf("workflow %s session %s: stage %s (paused=%t completed=%t)\n",
				result.Workflow, result.Session, stageLabel(result.CurrentStage), result.Paused, result.Completed)
			fmt.Printf("  checkpoint token: %s\n", result.CheckpointToken)
			for _, delivery := range result.Dispatches {
				fmt.Printf("  pane %s role %s turn %d: %s", delivery.Pane, delivery.Role, delivery.Turn, delivery.Status)
				if delivery.Resolution != "" {
					fmt.Printf(" (last reconciliation: %s)", delivery.Resolution)
				}
				fmt.Println()
			}
			if action == "recover" {
				fmt.Println("Checkpoint reconciled; no prompt sent. Continue with workflow run --resume.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "session whose checkpoint to inspect or reconcile")
	cmd.Flags().StringVar(&projectRoot, "project-root", ".", "project containing .ntm/workflows/state (works with offline sessions)")
	_ = cmd.MarkFlagRequired("session")
	if action == "recover" {
		cmd.Short = "Reconcile one interrupted prompt delivery after independent verification"
		cmd.Long = `Record the verified outcome of one uncertain workflow prompt delivery.

First inspect 'ntm workflow status --session SESSION --json', and independently
verify what reached the target pane. Supply the exact pane and checkpoint_token
from that snapshot. Use --outcome=delivered when the prompt reached the agent,
or --outcome=not-sent only when you verified it never arrived. Incorrectly
asserting not-sent can duplicate work. This command never sends a prompt.

Recovery takes the same session lock as the runner, refuses changed checkpoints,
and cannot turn confirmed deliveries into pending ones. Retrying
an identical recovery is a no-op. It preserves other panes and prior stages.
After reconciliation, use 'ntm workflow run TEMPLATE --session SESSION --resume'.
Example:
  ntm workflow recover --session myproj --pane=%2 --token=TOKEN_FROM_STATUS --outcome=delivered`
		cmd.Flags().StringVar(&pane, "pane", "", "exact saved pane ID from workflow status")
		cmd.Flags().StringVar(&token, "token", "", "exact checkpoint_token from workflow status; guards against concurrent changes")
		cmd.Flags().StringVar(&outcome, "outcome", "", "independently verified outcome: delivered or not-sent")
		for _, name := range []string{"pane", "token", "outcome"} {
			_ = cmd.MarkFlagRequired(name)
		}
	}
	return cmd
}

func getWorkflowCheckpoint(ctx context.Context, projectRoot, session, pane, token, outcome string, recoverDelivery bool) (WorkflowCheckpointResult, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return WorkflowCheckpointResult{}, fmt.Errorf("workflow project root is required")
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return WorkflowCheckpointResult{}, err
	}
	store := &workflow.StateStore{Dir: filepath.Join(root, ".ntm", "workflows", "state")}
	var state *workflow.WorkflowState
	if recoverDelivery {
		state, err = store.ReconcileDelivery(ctx, session, pane, token, outcome)
	} else {
		state, err = store.Load(session)
	}
	if err != nil {
		return WorkflowCheckpointResult{}, err
	}
	if state == nil {
		return WorkflowCheckpointResult{}, fmt.Errorf("no workflow checkpoint for session %q in %s; check --project-root", session, root)
	}
	currentToken, err := state.CheckpointToken()
	if err != nil {
		return WorkflowCheckpointResult{}, err
	}
	return WorkflowCheckpointResult{
		Success: true, Workflow: state.WorkflowName, Session: state.SessionName,
		CurrentStage: state.CurrentStage, Paused: state.Paused, Completed: state.Completed,
		Dispatches:      append([]workflow.StageDispatch{}, state.Dispatches...),
		CheckpointToken: currentToken,
	}, nil
}
