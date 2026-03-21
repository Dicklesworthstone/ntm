package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
  resume   Resume a workflow from saved state
  status   Check the status of a running pipeline
  list     List all tracked pipelines
  cancel   Cancel a running pipeline
  cleanup  Remove old pipeline state files

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
  ntm pipeline cancel run-20241230-123456-abcd

  # Resume a pipeline
  ntm pipeline resume run-20241230-123456-abcd

  # Cleanup old state files
  ntm pipeline cleanup --older=7d`,
	}

	cmd.AddCommand(
		newPipelineRunCmd(),
		newPipelineStatusCmd(),
		newPipelineListCmd(),
		newPipelineCancelCmd(),
		newPipelineResumeCmd(),
		newPipelineCleanupCmd(),
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
			workflowPath := workflowFile
			if abs, err := filepath.Abs(workflowFile); err == nil {
				workflowPath = abs
			}

			// Validate session
			if session == "" {
				return fmt.Errorf("--session is required")
			}

			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
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
					WorkflowFile: workflowPath,
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
			fmt.Printf("🚀 Running workflow: %s\n", workflowPath)
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
			workflow, result, err := pipeline.LoadAndValidate(workflowPath)
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
			execCfg.ProjectDir = projectDir
			execCfg.WorkflowFile = workflowPath
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
				defer func() {
					if r := recover(); r != nil {
						close(done)
					}
				}()
				defer close(progress)
				state, _ := executor.Run(ctx, workflow, vars, progress)
				done <- state
			}()

			// Display progress
			for {
				select {
				case event, ok := <-progress:
					if !ok {
						progress = nil
						continue
					}
					printProgressEvent(event)
				case state := <-done:
					// Drain remaining events
					if progress != nil {
						for event := range progress {
							printProgressEvent(event)
						}
					}

					fmt.Println()
					if state == nil {
						fmt.Fprintf(os.Stderr, "❌ Pipeline crashed unexpectedly\n")
						os.Exit(1)
					} else if state.Status == pipeline.StatusCompleted {
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

// newPipelineResumeCmd creates the "pipeline resume" subcommand
func newPipelineResumeCmd() *cobra.Command {
	var session string

	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a pipeline from saved state",
		Long: `Resume a previously interrupted pipeline from its last checkpoint.

Pipeline state is persisted to .ntm/pipelines/<run-id>.json after each step.
This allows resuming from the last completed step if a pipeline is interrupted.

Examples:
  # Resume a specific pipeline
  ntm pipeline resume run-20241230-123456-abcd --session myproject

  # Resume will pick up from the last incomplete step`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]

			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			state, err := pipeline.LoadState(projectDir, runID)
			if err != nil {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    false,
						"error":      err.Error(),
						"error_code": "STATE_NOT_FOUND",
						"run_id":     runID,
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}
				return fmt.Errorf("failed to load pipeline state: %w", err)
			}

			if session == "" {
				session = state.Session
			}
			if session == "" {
				return fmt.Errorf("--session is required (or state must contain session)")
			}

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q not found", session)
			}

			if state.Status == pipeline.StatusCompleted {
				if jsonOutput {
					result := map[string]interface{}{
						"success": true,
						"message": "Pipeline already completed",
						"run_id":  runID,
						"status":  string(state.Status),
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}
				fmt.Printf("Pipeline %s already completed\n", runID)
				return nil
			}

			workflowFile := strings.TrimSpace(state.WorkflowFile)
			if workflowFile == "" {
				return fmt.Errorf("state missing workflow file (run %s)", runID)
			}
			if !filepath.IsAbs(workflowFile) {
				workflowFile = filepath.Join(projectDir, workflowFile)
			}

			workflow, result, err := pipeline.LoadAndValidate(workflowFile)
			if err != nil {
				return fmt.Errorf("failed to load workflow: %w", err)
			}

			if !result.Valid {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    false,
						"error":      "workflow validation failed",
						"error_code": "INVALID_WORKFLOW",
						"run_id":     runID,
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}
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

			execCfg := pipeline.DefaultExecutorConfig(session)
			execCfg.RunID = state.RunID
			execCfg.ProjectDir = projectDir
			execCfg.WorkflowFile = workflowFile
			executor := pipeline.NewExecutor(execCfg)

			state.Session = session
			state.WorkflowFile = workflowFile

			ctx := context.Background()

			if jsonOutput {
				finalState, err := executor.Resume(ctx, workflow, state, nil)
				if err != nil {
					status := "unknown"
					if finalState != nil {
						status = string(finalState.Status)
					}
					result := map[string]interface{}{
						"success":  false,
						"error":    err.Error(),
						"run_id":   runID,
						"status":   status,
						"workflow": workflow.Name,
						"session":  session,
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}

				result := map[string]interface{}{
					"success":  true,
					"run_id":   runID,
					"status":   string(finalState.Status),
					"workflow": workflow.Name,
					"session":  session,
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}

			fmt.Printf("📋 Resuming pipeline: %s\n", runID)
			fmt.Printf("   Session: %s\n", session)
			fmt.Printf("   Status: %s\n", state.Status)
			if state.CurrentStep != "" {
				fmt.Printf("   Current step: %s\n", state.CurrentStep)
			}
			fmt.Println()

			progress := make(chan pipeline.ProgressEvent, 100)
			done := make(chan *pipeline.ExecutionState)

			go func() {
				defer func() {
					if r := recover(); r != nil {
						close(done)
					}
				}()
				defer close(progress)
				state, _ := executor.Resume(ctx, workflow, state, progress)
				done <- state
			}()

			for {
				select {
				case event, ok := <-progress:
					if !ok {
						progress = nil
						continue
					}
					printProgressEvent(event)
				case finalState := <-done:
					if progress != nil {
						for event := range progress {
							printProgressEvent(event)
						}
					}

					fmt.Println()
					if finalState == nil {
						fmt.Fprintf(os.Stderr, "❌ Pipeline crashed unexpectedly\n")
						os.Exit(1)
					} else if finalState.Status == pipeline.StatusCompleted {
						output.SuccessCheck("Pipeline completed successfully!")
					} else {
						fmt.Fprintf(os.Stderr, "❌ Pipeline %s\n", finalState.Status)
						if len(finalState.Errors) > 0 {
							for _, e := range finalState.Errors {
								fmt.Printf("  ❌ %s\n", e.Message)
							}
						}
						return fmt.Errorf("pipeline %s", finalState.Status)
					}
					return nil
				}
			}
		},
	}

	cmd.Flags().StringVarP(&session, "session", "s", "", "Tmux session name (uses saved session if not specified)")

	return cmd
}

// newPipelineCleanupCmd creates the "pipeline cleanup" subcommand
func newPipelineCleanupCmd() *cobra.Command {
	var olderThan string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up old pipeline state files",
		Long: `Remove pipeline state files older than the specified duration.

State files are stored in .ntm/pipelines/ and can accumulate over time.
Use this command to clean up old files and free disk space.

Examples:
  # Remove state files older than 7 days
  ntm pipeline cleanup --older 7d

  # Remove state files older than 30 days
  ntm pipeline cleanup --older 30d

  # Dry run - show what would be deleted
  ntm pipeline cleanup --older 7d --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if olderThan == "" {
				return fmt.Errorf("--older is required (e.g., --older 7d)")
			}

			// Parse duration
			duration, err := parseDuration(olderThan)
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w", olderThan, err)
			}

			// Get project directory
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			if dryRun {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    true,
						"dry_run":    true,
						"older_than": olderThan,
						"duration":   duration.String(),
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}
				fmt.Printf("Dry run: would clean up state files older than %s\n", duration)
				return nil
			}

			// Perform cleanup
			deleted, err := pipeline.CleanupStates(projectDir, duration)
			if err != nil {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    false,
						"error":      err.Error(),
						"error_code": "CLEANUP_FAILED",
					}
					return json.NewEncoder(os.Stdout).Encode(result)
				}
				return fmt.Errorf("cleanup failed: %w", err)
			}

			if jsonOutput {
				result := map[string]interface{}{
					"success":     true,
					"deleted":     deleted,
					"older_than":  olderThan,
					"duration":    duration.String(),
					"project_dir": projectDir,
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}

			if deleted == 0 {
				fmt.Println("No state files to clean up.")
			} else {
				output.SuccessCheck(fmt.Sprintf("Cleaned up %d state file(s) older than %s", deleted, duration))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&olderThan, "older", "", "Remove files older than duration (e.g., 7d, 30d)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without deleting")

	return cmd
}

// parseDuration parses duration strings like "7d", "30d", "24h"
func parseDuration(s string) (time.Duration, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("duration is empty")
	}

	normalized := strings.ToLower(trimmed)
	if strings.HasSuffix(normalized, "d") {
		value := strings.TrimSuffix(normalized, "d")
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("invalid day count: %s", value)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}

	if strings.HasSuffix(normalized, "w") {
		value := strings.TrimSuffix(normalized, "w")
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("invalid week count: %s", value)
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}

	return time.ParseDuration(normalized)
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
