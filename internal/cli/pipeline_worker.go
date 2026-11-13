package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func init() {
	rootCmd.AddCommand(newPipelineWorkerCmd())
	rootCmd.AddCommand(newInternalStepReaperCmd())
	pipeline.BackgroundWorkerFlags = pipelineWorkerFlags
}

func pipelineWorkerFlags() ([]string, error) {
	var flags []string
	if cfgFile != "" {
		path, err := filepath.Abs(cfgFile)
		if err != nil {
			return nil, err
		}
		flags = append(flags, "--config="+path)
	}
	if sshHost != "" {
		flags = append(flags, "--ssh="+sshHost)
	}
	return flags, nil
}

// This is an implementation endpoint, not a second pipeline command family.
// Ordinary `pipeline run --background` and robot mode re-exec it after freezing
// the request; stdin carries the parent's final authorization, never prompts.
func newPipelineWorkerCmd() *cobra.Command {
	return &cobra.Command{
		Use: "__pipeline-worker <project-dir> <run-id>", Hidden: true,
		Args: cobra.ExactArgs(2), SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return pipeline.RunBackgroundWorker(cmd.Context(), args[0], args[1], os.Stdin)
		},
	}
}

// newInternalStepReaperCmd runs a pipeline command step under a Linux
// PR_SET_CHILD_SUBREAPER supervisor (Tier 2 containment) when writable
// cgroup v2 delegation is unavailable.
func newInternalStepReaperCmd() *cobra.Command {
	return &cobra.Command{
		Use:                pipeline.StepReaperSubcommand + " [flags] -- <command> [args...]",
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
		PersistentPostRunE: func(*cobra.Command, []string) error { return nil },
		Run: func(_ *cobra.Command, args []string) {
			pipeline.ExitFromStepReaper(pipeline.RunStepReaper(args))
		},
	}
}
