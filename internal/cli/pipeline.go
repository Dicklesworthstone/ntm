package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Run and manage workflow pipelines",
		Long: `Execute and manage multi-step workflow pipelines.

Pipelines define sequences of agent prompts that can run in parallel,
with dependencies, conditionals, and variable substitution.

Subcommands:
  run      Run a workflow from a YAML/TOML file
  status   Check the status of a running pipeline
  list     List all tracked pipelines
  cancel   Cancel a running pipeline

Quick ad-hoc pipeline:
  ntm pipeline exec <session> --stage "cc: prompt" --stage "cod: prompt"

Examples:
  # Run a workflow file
  ntm pipeline run workflow.yaml --session myproject

  # Run with variables
  ntm pipeline run workflow.yaml --session proj --var env=prod --var debug=true

  # Check status
  ntm pipeline status run-20241230-123456-abcd

  # List all pipelines
  ntm pipeline list

  # Cancel a running pipeline
  ntm pipeline cancel run-20241230-123456-abcd`,
	}

	cmd.AddCommand(
		newPipelineRunCmd(),
		newPipelineStatusCmd(),
		newPipelineListCmd(),
		newPipelineCancelCmd(),
		newPipelineExecCmd(), // Backward-compatible stage-based execution
	)

	return cmd
}

// newPipelineRunCmd creates the "pipeline run" subcommand
func newPipelineRunCmd() *cobra.Command {
	var (
		session    string
		varsFlag   []string
		varsFile   string
		dryRun     bool
		background bool
	)

	cmd := &cobra.Command{
		Use:   "run <workflow-file>",
		Short: "Run a workflow from a file",
		Long: `Execute a workflow defined in a YAML or TOML file.

The workflow file defines steps with prompts, dependencies, conditionals,
and agent routing. Variables can be passed via --var or --var-file.

Examples:
  # Basic execution
  ntm pipeline run workflow.yaml --session myproject

  # With variables
  ntm pipeline run workflow.yaml --session proj --var env=prod

  # Dry run (validate without executing)
  ntm pipeline run workflow.yaml --session proj --dry-run

  # Run in background
  ntm pipeline run workflow.yaml --session proj --background`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowFile := args[0]

			// Validate session
			if session == "" {
				return fmt.Errorf("--session is required")
			}

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q not found", session)
			}

			// Parse variables
			vars := make(map[string]interface{})

			// Load from file first
			if varsFile != "" {
				data, err := os.ReadFile(varsFile)
				if err != nil {
					return fmt.Errorf("failed to read var file: %w", err)
				}
				if err := json.Unmarshal(data, &vars); err != nil {
					return fmt.Errorf("failed to parse var file: %w", err)
				}
			}

			// Override with command-line vars
			for _, v := range varsFlag {
				parts := strings.SplitN(v, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid variable format: %q (expected key=value)", v)
				}
				vars[parts[0]] = parts[1]
			}

			// JSON mode
			if jsonOutput {
				opts := pipeline.PipelineRunOptions{
					WorkflowFile: workflowFile,
					Session:      session,
					Variables:    vars,
					DryRun:       dryRun,
					Background:   background,
				}
				exitCode := pipeline.PrintPipelineRun(opts)
				if exitCode != 0 {
					os.Exit(exitCode)
				}
				return nil
			}

			// Human-friendly mode
			fmt.Printf("🚀 Running workflow: %s\n", workflowFile)
			fmt.Printf("   Session: %s\n", session)
			if dryRun {
				fmt.Println("   Mode: dry-run (validate only)")
			}
			if background {
				fmt.Println("   Mode: background")
			}
			if len(vars) > 0 {
				fmt.Printf("   Variables: %d\n", len(vars))
			}
			fmt.Println()

			// Load and validate workflow
			workflow, result, err := pipeline.LoadAndValidate(workflowFile)
			if err != nil {
				return fmt.Errorf("failed to load workflow: %w", err)
			}

			if !result.Valid {
				fmt.Fprintln(os.Stderr, "Validation failed:")
				for _, e := range result.Errors {
					fmt.Printf("  ❌ %s\n", e.Message)
					if e.Hint != "" {
						fmt.Printf("     💡 %s\n", e.Hint)
					}
				}
				return fmt.Errorf("workflow validation failed")
			}

			for _, w := range result.Warnings {
				fmt.Printf("  ⚠️  %s\n", w.Message)
			}

			fmt.Printf("✓ Validated workflow: %s (%d steps)\n\n", workflow.Name, len(workflow.Steps))

			// Create executor
			execCfg := pipeline.DefaultExecutorConfig(session)
			execCfg.DryRun = dryRun
			executor := pipeline.NewExecutor(execCfg)

			// Create progress channel
			progress := make(chan pipeline.ProgressEvent, 100)
			ctx := context.Background()

			if background {
				// Background mode - register pipeline and start execution
				runID := pipeline.GenerateRunID()

				// Reconfigure executor with the pre-generated RunID
				execCfg.RunID = runID
				executor = pipeline.NewExecutor(execCfg)

				// Register pipeline in the registry
				exec := &pipeline.PipelineExecution{
					RunID:      runID,
					WorkflowID: workflow.Name,
					Session:    session,
					Status:     "running",
					StartedAt:  time.Now(),
					Steps:      make(map[string]pipeline.PipelineStep),
					Progress: pipeline.PipelineProgress{
						Total:   len(workflow.Steps),
						Pending: len(workflow.Steps),
					},
				}
				pipeline.RegisterPipeline(exec)

				go func() {
					defer close(progress)
					state, _ := executor.Run(ctx, workflow, vars, progress)
					pipeline.UpdatePipelineFromState(runID, state)
				}()

				fmt.Printf("✓ Pipeline started in background\n")
				fmt.Printf("   Run ID: %s\n", runID)
				fmt.Printf("\n   Check status: ntm pipeline status %s\n", runID)
				fmt.Printf("   Cancel: ntm pipeline cancel %s\n", runID)
				return nil
			}

			// Foreground mode - show progress
			done := make(chan *pipeline.ExecutionState)
			go func() {
				defer close(progress)
				state, _ := executor.Run(ctx, workflow, vars, progress)
				done <- state
			}()

			// Display progress
			for {
				select {
				case event, ok := <-progress:
					if !ok {
						continue
					}
					printProgressEvent(event)
				case state := <-done:
					// Drain remaining events
					for event := range progress {
						printProgressEvent(event)
					}

					fmt.Println()
					if state.Status == pipeline.StatusCompleted {
						output.SuccessCheck("Pipeline completed successfully!")
					} else {
						fmt.Fprintf(os.Stderr, "❌ Pipeline %s\n", state.Status)
						if len(state.Errors) > 0 {
							for _, e := range state.Errors {
								fmt.Printf("  ❌ %s\n", e.Message)
							}
						}
						return fmt.Errorf("pipeline %s", state.Status)
					}
					return nil
				}
			}
		},
	}

	cmd.Flags().StringVarP(&session, "session", "s", "", "Tmux session name (required)")
	cmd.Flags().StringArrayVar(&varsFlag, "var", nil, "Variable in key=value format")
	cmd.Flags().StringVar(&varsFile, "var-file", "", "JSON file with variables")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate without executing")
	cmd.Flags().BoolVarP(&background, "background", "b", false, "Run in background")

	return cmd
}

// newPipelineStatusCmd creates the "pipeline status" subcommand
func newPipelineStatusCmd() *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "status [run-id]",
		Short: "Check pipeline status",
		Long: `Display the status of a running or completed pipeline.

Without a run-id, shows all running pipelines.

Examples:
  # Check specific pipeline
  ntm pipeline status run-20241230-123456-abcd

  # List all running pipelines
  ntm pipeline status

  # Watch for updates (TODO)
  ntm pipeline status run-20241230-123456-abcd --watch`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// Show all running pipelines
				if jsonOutput {
					pipeline.PrintPipelineList()
					return nil
				}
				return showPipelineList()
			}

			runID := args[0]

			if jsonOutput {
				exitCode := pipeline.PrintPipelineStatus(runID)
				if exitCode != 0 {
					os.Exit(exitCode)
				}
				return nil
			}

			return showPipelineStatus(runID)
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for updates")

	return cmd
}

// newPipelineListCmd creates the "pipeline list" subcommand
func newPipelineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all tracked pipelines",
		Long: `List all pipelines that have been run in this session.

Pipelines are tracked in memory and reset when ntm exits.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOutput {
				pipeline.PrintPipelineList()
				return nil
			}
			return showPipelineList()
		},
	}
}

// newPipelineCancelCmd creates the "pipeline cancel" subcommand
func newPipelineCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a running pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]

			if jsonOutput {
				exitCode := pipeline.PrintPipelineCancel(runID)
				if exitCode != 0 {
					os.Exit(exitCode)
				}
				return nil
			}

			// Human-friendly cancel
			fmt.Printf("Cancelling pipeline: %s\n", runID)
			exitCode := pipeline.PrintPipelineCancel(runID)
			if exitCode == 0 {
				output.SuccessCheck("Pipeline cancelled")
			}
			return nil
		},
	}
}

// newPipelineExecCmd creates the backward-compatible "pipeline exec" command
func newPipelineExecCmd() *cobra.Command {
	var stages []string

	cmd := &cobra.Command{
		Use:   "exec <session>",
		Short: "Run ad-hoc stage pipeline (legacy)",
		Long: `Execute a sequence of agent prompts, passing output from one to the next.

This is the legacy command-line pipeline format. For complex workflows,
use 'ntm pipeline run' with a YAML/TOML workflow file.

Stages are defined using --stage flags:
  --stage "type: prompt"
  --stage "type:model: prompt"

Examples:
  ntm pipeline exec myproject \
    --stage "cc: Design the API" \
    --stage "cod: Implement the API" \
    --stage "gmi: Write tests"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			if len(stages) == 0 {
				return fmt.Errorf("no stages defined (use --stage)")
			}

			var pipeStages []pipeline.Stage
			for _, s := range stages {
				parts := strings.SplitN(s, ": ", 2)
				if len(parts) < 2 {
					parts = strings.SplitN(s, ":", 2)
					if len(parts) < 2 {
						return fmt.Errorf("invalid stage format: %q (expected 'type: prompt')", s)
					}
				}

				agentSpec := strings.TrimSpace(parts[0])
				prompt := strings.TrimSpace(parts[1])

				agentType := agentSpec
				model := ""

				if strings.Contains(agentSpec, ":") {
					sub := strings.SplitN(agentSpec, ":", 2)
					agentType = sub[0]
					model = sub[1]
				}

				pipeStages = append(pipeStages, pipeline.Stage{
					AgentType: agentType,
					Prompt:    prompt,
					Model:     model,
				})
			}

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q not found", session)
			}

			return pipeline.Execute(context.Background(), pipeline.Pipeline{
				Session: session,
				Stages:  pipeStages,
			})
		},
	}

	cmd.Flags().StringArrayVar(&stages, "stage", nil, "Pipeline stage (format: 'type: prompt')")

	return cmd
}

// Helper functions for human-friendly output

func printProgressEvent(event pipeline.ProgressEvent) {
	switch event.Type {
	case "workflow_start":
		fmt.Printf("📋 %s\n", event.Message)
	case "workflow_complete":
		fmt.Printf("✅ %s\n", event.Message)
	case "workflow_error":
		fmt.Printf("❌ %s\n", event.Message)
	case "step_start":
		fmt.Printf("  ▶ [%s] %s\n", event.StepID, event.Message)
	case "step_complete":
		fmt.Printf("  ✓ [%s] %s\n", event.StepID, event.Message)
	case "step_error":
		fmt.Printf("  ✗ [%s] %s\n", event.StepID, event.Message)
	case "step_skip":
		fmt.Printf("  ⊘ [%s] %s\n", event.StepID, event.Message)
	case "step_retry":
		fmt.Printf("  ↻ [%s] %s\n", event.StepID, event.Message)
	case "parallel_start":
		fmt.Printf("  ⫘ [%s] %s\n", event.StepID, event.Message)
	default:
		if event.StepID != "" {
			fmt.Printf("  • [%s] %s\n", event.StepID, event.Message)
		} else {
			fmt.Printf("• %s\n", event.Message)
		}
	}
}

func showPipelineStatus(runID string) error {
	// Get pipeline from registry
	exec := pipeline.GetPipelineExecution(runID)
	if exec == nil {
		return fmt.Errorf("pipeline %q not found (use 'ntm pipeline list' to see available pipelines)", runID)
	}

	fmt.Printf("Pipeline: %s\n", runID)
	fmt.Printf("Workflow: %s\n", exec.WorkflowID)
	fmt.Printf("Session:  %s\n", exec.Session)
	fmt.Printf("Status:   %s\n", exec.Status)
	fmt.Printf("Started:  %s\n", exec.StartedAt.Format(time.RFC3339))
	if exec.FinishedAt != nil {
		fmt.Printf("Finished: %s\n", exec.FinishedAt.Format(time.RFC3339))
		fmt.Printf("Duration: %s\n", exec.FinishedAt.Sub(exec.StartedAt))
	} else {
		fmt.Printf("Duration: %s (running)\n", time.Since(exec.StartedAt).Round(time.Second))
	}
	fmt.Printf("Progress: %d/%d (%.0f%%)\n",
		exec.Progress.Completed+exec.Progress.Failed+exec.Progress.Skipped,
		exec.Progress.Total,
		exec.Progress.Percent)

	if len(exec.Steps) > 0 {
		fmt.Println("\nSteps:")
		for id, step := range exec.Steps {
			status := step.Status
			switch status {
			case "completed":
				status = "✓ completed"
			case "failed":
				status = "✗ failed"
			case "running":
				status = "▶ running"
			case "skipped":
				status = "⊘ skipped"
			}
			fmt.Printf("  [%s] %s\n", id, status)
		}
	}

	if exec.Error != "" {
		fmt.Printf("\nError: %s\n", exec.Error)
	}

	return nil
}

func showPipelineList() error {
	pipelines := pipeline.GetAllPipelines()

	if len(pipelines) == 0 {
		fmt.Println("No pipelines tracked.")
		fmt.Println("\nStart a pipeline with:")
		fmt.Println("  ntm pipeline run workflow.yaml --session <session>")
		return nil
	}

	fmt.Println("Tracked Pipelines")
	fmt.Println("=================")
	fmt.Println()

	for _, p := range pipelines {
		status := p.Status
		switch status {
		case "completed":
			status = "✓ completed"
		case "failed":
			status = "✗ failed"
		case "running":
			status = "▶ running"
		case "cancelled":
			status = "⊘ cancelled"
		}

		fmt.Printf("%s  [%s]\n", p.RunID, status)
		fmt.Printf("  Workflow: %s\n", p.WorkflowID)
		fmt.Printf("  Session:  %s\n", p.Session)
		fmt.Printf("  Progress: %.0f%%\n", p.Progress.Percent)
		fmt.Println()
	}

	return nil
}
