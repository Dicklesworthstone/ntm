package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// WorkflowCheckpointResult exposes execution evidence without printing setup
// variables or prompt contents, which may contain sensitive project context.
type WorkflowCheckpointResult struct {
	Success         bool                       `json:"success"`
	Workflow        string                     `json:"workflow"`
	Session         string                     `json:"session"`
	TmuxEndpoint    string                     `json:"tmux_endpoint,omitempty"`
	CurrentStage    string                     `json:"current_stage"`
	Paused          bool                       `json:"paused"`
	Completed       bool                       `json:"completed"`
	Dispatches      []workflow.StageDispatch   `json:"dispatches"`
	CheckpointToken string                     `json:"checkpoint_token"`
	ManualGate      *workflow.ManualGateStatus `json:"manual_gate,omitempty"`
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
					return emitJSONFailureEnvelopeToWithCause(cmd.OutOrStdout(), map[string]interface{}{"success": false, "error": err.Error()}, err)
				}
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "workflow %s session %s: stage %s (paused=%t completed=%t)\n",
				result.Workflow, result.Session, stageLabel(result.CurrentStage), result.Paused, result.Completed)
			if result.TmuxEndpoint != "" {
				fmt.Fprintf(out, "  tmux endpoint: %s\n", result.TmuxEndpoint)
			} else {
				fmt.Fprintln(out, "  tmux endpoint: unknown (legacy checkpoint; inspect the original agents before restarting)")
			}
			fmt.Fprintf(out, "  checkpoint token: %s\n", result.CheckpointToken)
			for _, delivery := range result.Dispatches {
				fmt.Fprintf(out, "  pane %s role %s turn %d: %s", delivery.Pane, delivery.Role, delivery.Turn, delivery.Status)
				if delivery.Resolution != "" {
					fmt.Fprintf(out, " (last reconciliation: %s)", delivery.Resolution)
				}
				fmt.Fprintln(out)
			}
			if gate := result.ManualGate; gate != nil {
				fmt.Fprintf(out, "  manual gate token: %s\n", gate.Token)
				for _, choice := range gate.Transitions {
					fmt.Fprintf(out, "    transition %d: %q -> %q\n", choice.Index, choice.Label, choice.To)
				}
				if gate.Decision != nil {
					fmt.Fprintf(out, "  recorded transition %d at %s; waiting for the runner to apply it\n",
						gate.Decision.TransitionIndex, gate.Decision.RequestedAt.Format(time.RFC3339))
				} else {
					fmt.Fprintln(out, "  Use workflow trigger --session SESSION --token TOKEN --transition INDEX --reason TEXT to select one transition.")
				}
			}
			if action == "recover" {
				fmt.Fprintln(out, "Checkpoint reconciled; no prompt sent. Continue with workflow run --resume.")
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
	manualGate, err := store.ManualStatus(state)
	if err != nil {
		return WorkflowCheckpointResult{}, err
	}
	return WorkflowCheckpointResult{
		Success: true, Workflow: state.WorkflowName, Session: state.SessionName,
		TmuxEndpoint: state.TmuxEndpoint,
		CurrentStage: state.CurrentStage, Paused: state.Paused, Completed: state.Completed,
		Dispatches:      append([]workflow.StageDispatch{}, state.Dispatches...),
		CheckpointToken: currentToken,
		ManualGate:      manualGate,
	}, nil
}

// newWorkflowTriggerCmd records one operator choice for the exact stage visit
// advertised by status. A separate decision journal lets this work while the
// runner owns its checkpoint lease, and while it is offline awaiting resume.
func newWorkflowTriggerCmd() *cobra.Command {
	var session, projectRoot, token, reason string
	var transition int
	cmd := &cobra.Command{
		Use: "trigger", Args: cobra.NoArgs,
		Short: "Advance one waiting workflow manual gate",
		Long: `Record one explicit manual transition for a waiting workflow stage.

Inspect 'ntm workflow status --session SESSION --json' and choose an index from
manual_gate.transitions. Supply that gate's token and your reason. The token is
stable while the same stage waits, and changes after a retry, agent replacement,
or another stage visit. It is distinct from the delivery-recovery checkpoint token.

The running workflow applies the decision on its next poll. If the runner is
offline or paused, continue with 'ntm workflow run TEMPLATE --session SESSION
--resume'. Recording a decision never resumes a paused runner by itself.
Repeating the same token, transition, and reason returns the recorded decision;
a conflicting choice is refused. Only an authored manual edge can be selected.

Example:
  ntm workflow trigger --session myproj --token=TOKEN_FROM_STATUS --transition=0 --reason="Design reviewed and ready for implementation"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fail := func(err error) error {
				if jsonOutput {
					return emitJSONFailureEnvelopeToWithCause(cmd.OutOrStdout(), map[string]interface{}{"success": false, "error": err.Error()}, err)
				}
				return err
			}
			if strings.TrimSpace(projectRoot) == "" {
				return fail(fmt.Errorf("workflow project root is required"))
			}
			root, err := filepath.Abs(projectRoot)
			if err != nil {
				return fail(err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			store := &workflow.StateStore{Dir: filepath.Join(root, ".ntm", "workflows", "state")}
			decision, err := store.RequestManualTransition(ctx, session, token, transition, reason)
			if err != nil {
				return fail(err)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Success bool `json:"success"`
					*workflow.ManualDecision
				}{Success: true, ManualDecision: decision})
			}
			verb := "Recorded"
			if decision.Replayed {
				verb = "Previously recorded"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s manual transition %d for session %q: %q -> %q.\n",
				verb, decision.TransitionIndex, decision.Session, decision.Stage, decision.To)
			if decision.Replayed {
				fmt.Fprintln(cmd.OutOrStdout(), "The original decision is unchanged; this retry does not authorize another stage. Inspect workflow status for the current gate.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "The active runner applies this decision on its next poll. If it is stopped, continue with workflow run --resume.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "session with the waiting workflow")
	cmd.Flags().StringVar(&projectRoot, "project-root", ".", "project containing .ntm/workflows/state")
	cmd.Flags().StringVar(&token, "token", "", "exact manual_gate.token from workflow status")
	cmd.Flags().IntVar(&transition, "transition", 0, "exact manual transition index from workflow status")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason for selecting this transition (1 to 4096 UTF-8 bytes)")
	for _, name := range []string{"session", "token", "transition", "reason"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
