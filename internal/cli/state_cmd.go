package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

type stateGCOptions struct {
	Checkpoint     bool
	CheckpointMode string
}

type StateGCResponse struct {
	output.TimestampedResponse
	Success    bool                       `json:"success"`
	Path       string                     `json:"path"`
	GC         state.RuntimeGCResult      `json:"gc"`
	Checkpoint *state.WALCheckpointResult `json:"checkpoint,omitempty"`
	Error      string                     `json:"error,omitempty"`
}

func newStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Manage NTM runtime state",
	}
	cmd.AddCommand(newStateGCCmd())
	return cmd
}

func newStateGCCmd() *cobra.Command {
	opts := stateGCOptions{
		Checkpoint:     true,
		CheckpointMode: "TRUNCATE",
	}
	noCheckpoint := false

	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Prune expired runtime state and checkpoint the state database",
		Long: `Prune expired runtime rows from the NTM state database and checkpoint WAL data.

Examples:
  ntm state gc --json
  ntm state gc --checkpoint-mode=FULL`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Checkpoint = !noCheckpoint
			resp, err := runStateGC(opts)
			if err != nil {
				if IsJSONOutput() {
					return emitJSONFailureEnvelope(resp)
				}
				return err
			}
			return printStateGCResponse(resp)
		},
	}

	cmd.Flags().BoolVar(&noCheckpoint, "no-checkpoint", false, "Skip WAL checkpoint after pruning")
	cmd.Flags().StringVar(&opts.CheckpointMode, "checkpoint-mode", opts.CheckpointMode, "WAL checkpoint mode: PASSIVE, FULL, RESTART, or TRUNCATE")
	return cmd
}

func runStateGC(opts stateGCOptions) (StateGCResponse, error) {
	resp := StateGCResponse{
		TimestampedResponse: output.NewTimestamped(),
		Success:             false,
	}

	if opts.Checkpoint {
		mode, err := state.NormalizeWALCheckpointMode(opts.CheckpointMode)
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
		opts.CheckpointMode = mode
	}

	store, err := state.Open("")
	if err != nil {
		resp.Error = fmt.Sprintf("open state store: %v", err)
		return resp, fmt.Errorf("open state store: %w", err)
	}
	resp.Path = store.Path()
	defer store.Close()

	if err := store.Migrate(); err != nil {
		resp.Error = fmt.Sprintf("migrate state store: %v", err)
		return resp, fmt.Errorf("migrate state store: %w", err)
	}

	gc, err := store.RunGC(state.DefaultRuntimeGCConfig())
	if err != nil {
		resp.Error = fmt.Sprintf("run state gc: %v", err)
		return resp, fmt.Errorf("run state gc: %w", err)
	}
	resp.GC = gc

	if opts.Checkpoint {
		checkpoint, err := store.CheckpointWAL(opts.CheckpointMode)
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
		resp.Checkpoint = &checkpoint
	}

	resp.Success = true
	return resp, nil
}

func printStateGCResponse(resp StateGCResponse) error {
	if IsJSONOutput() {
		return output.PrintJSON(resp)
	}
	fmt.Printf("State DB: %s\n", resp.Path)
	fmt.Printf("Expired attention events: %d\n", resp.GC.ExpiredAttentionEvents)
	fmt.Printf("Expired audit events: %d\n", resp.GC.ExpiredAuditEvents)
	fmt.Printf("Expired audit decisions: %d\n", resp.GC.ExpiredAuditDecisions)
	fmt.Printf("Expired incidents: %d\n", resp.GC.ExpiredIncidents)
	if resp.Checkpoint != nil {
		fmt.Printf("WAL checkpoint: mode=%s busy=%d log_frames=%d checkpointed_frames=%d\n",
			resp.Checkpoint.Mode,
			resp.Checkpoint.Busy,
			resp.Checkpoint.LogFrames,
			resp.Checkpoint.CheckpointedFrames,
		)
	}
	return nil
}
