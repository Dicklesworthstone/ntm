package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
  lint     Parse, normalize, and validate a workflow file
  resume   Resume a workflow from saved state
  recover  Inspect and reconcile interrupted pipeline work
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

  # Lint without requiring a tmux session
  ntm pipeline lint workflow.yaml

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
		newPipelineLintCmd(),
		newPipelineStatusCmd(),
		newPipelineListCmd(),
		newPipelineCancelCmd(),
		newPipelineResumeCmd(),
		newPipelineRecoverCmd(),
		newPipelineCleanupCmd(),
		newPipelineExecCmd(), // Backward-compatible stage-based execution
	)

	return cmd
}

type pipelineLintOutput struct {
	Success            bool                    `json:"success"`
	WorkflowFile       string                  `json:"workflow_file"`
	Workflow           string                  `json:"workflow,omitempty"`
	Valid              bool                    `json:"valid"`
	StepCount          int                     `json:"step_count,omitempty"`
	ParallelSubsteps   int                     `json:"parallel_substeps,omitempty"`
	ExecutionOrder     []string                `json:"execution_order,omitempty"`
	ExecutionLevels    [][]string              `json:"execution_levels,omitempty"`
	Error              string                  `json:"error,omitempty"`
	ErrorCode          string                  `json:"error_code,omitempty"`
	Errors             []pipeline.ParseError   `json:"errors,omitempty"`
	Warnings           []pipeline.ParseError   `json:"warnings,omitempty"`
	NormalizedWorkflow *pipeline.Workflow      `json:"normalized_workflow,omitempty"`
	Summary            pipelineLintSummaryJSON `json:"summary"`
}

type pipelineLintSummaryJSON struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// newPipelineLintCmd creates the "pipeline lint" subcommand.
func newPipelineLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "lint <workflow-file>",
		Short:         "Parse, normalize, and validate a workflow file",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Parse, normalize, and validate a workflow file without requiring a
tmux session or dispatching any work.

The --json flag includes the normalized workflow so authoring tools can inspect
the canonical form that ntm would execute.

Examples:
  ntm pipeline lint workflow.yaml
  ntm --json pipeline lint workflow.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelineLint(args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	return cmd
}

func runPipelineLint(workflowFile string, out io.Writer, errOut io.Writer) error {
	workflowPath := workflowFile
	if abs, err := filepath.Abs(workflowFile); err == nil {
		workflowPath = abs
	}

	workflow, result, err := pipeline.LoadAndValidate(workflowPath)
	if err != nil {
		lintResult := pipelineLintOutput{
			Success:      false,
			WorkflowFile: workflowPath,
			Valid:        false,
			Error:        err.Error(),
			ErrorCode:    "PARSE_FAILED",
			Summary: pipelineLintSummaryJSON{
				Errors: 1,
			},
		}

		var parseErr *pipeline.ParseError
		if errors.As(err, &parseErr) {
			lintResult.Errors = []pipeline.ParseError{*parseErr}
		}

		if jsonOutput {
			return emitJSONFailureEnvelopeToWithCause(out, lintResult, err)
		} else {
			fmt.Fprintf(errOut, "Pipeline lint failed: %s\n", workflowPath)
			printPipelineLintErrors(errOut, lintResult.Errors)
			if len(lintResult.Errors) == 0 {
				fmt.Fprintf(errOut, "  %s\n", err)
			}
		}
		return fmt.Errorf("pipeline lint failed")
	}

	parallelSubsteps := 0
	for i := range workflow.Steps {
		parallelSubsteps += workflow.Steps[i].Parallel.Len()
	}

	lintResult := pipelineLintOutput{
		Success:            result.Valid,
		WorkflowFile:       workflowPath,
		Workflow:           workflow.Name,
		Valid:              result.Valid,
		StepCount:          len(workflow.Steps),
		ParallelSubsteps:   parallelSubsteps,
		Errors:             result.Errors,
		Warnings:           result.Warnings,
		NormalizedWorkflow: workflow,
		Summary: pipelineLintSummaryJSON{
			Errors:   len(result.Errors),
			Warnings: len(result.Warnings),
		},
	}
	if result.Valid {
		execPlan := pipeline.NewDependencyGraph(workflow).Resolve()
		if execPlan.Valid {
			lintResult.ExecutionOrder = execPlan.Order
			lintResult.ExecutionLevels = execPlan.Levels
		}
	} else {
		lintResult.Error = "workflow validation failed"
		lintResult.ErrorCode = "VALIDATION_FAILED"
	}

	if jsonOutput {
		if !result.Valid {
			cause := errors.New("workflow validation failed")
			return emitJSONFailureEnvelopeToWithCause(out, lintResult, cause)
		}
		return json.NewEncoder(out).Encode(lintResult)
	}

	fmt.Fprintf(out, "Pipeline lint: %s\n", workflowPath)
	fmt.Fprintf(out, "Workflow: %s\n", workflow.Name)
	fmt.Fprintf(out, "Steps: %d\n", len(workflow.Steps))
	if len(lintResult.ExecutionLevels) > 0 {
		fmt.Fprintf(out, "Execution levels: %d\n", len(lintResult.ExecutionLevels))
	}
	fmt.Fprintf(out, "Warnings: %d\n", len(result.Warnings))

	if len(result.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		printPipelineLintErrors(out, result.Warnings)
	}

	if !result.Valid {
		fmt.Fprintln(errOut, "\nValidation failed:")
		printPipelineLintErrors(errOut, result.Errors)
		return fmt.Errorf("workflow validation failed")
	}

	fmt.Fprintln(out, "Validation: ok")
	return nil
}

func printPipelineLintErrors(w io.Writer, errs []pipeline.ParseError) {
	for _, e := range errs {
		location := e.Field
		if e.File != "" {
			location = e.File
			if e.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, e.Line)
			}
			if e.Field != "" {
				location = fmt.Sprintf("%s:%s", location, e.Field)
			}
		}
		if location != "" {
			fmt.Fprintf(w, "  - %s: %s\n", location, e.Message)
		} else {
			fmt.Fprintf(w, "  - %s\n", e.Message)
		}
		if e.Hint != "" {
			fmt.Fprintf(w, "    Hint: %s\n", e.Hint)
		}
	}
}

// newPipelineRunCmd creates the "pipeline run" subcommand
func newPipelineRunCmd() *cobra.Command {
	var (
		session       string
		varsFlag      []string
		varsFile      string
		dryRun        bool
		background    bool
		startFromStep string
		fromState     string
	)

	cmd := &cobra.Command{
		Use:   "run <workflow-file>",
		Short: "Run a workflow from a file",
		Long: `Execute a workflow defined in a YAML or TOML file.

The workflow file defines steps with prompts, dependencies, conditionals,
and agent routing. Variables can be passed via --var or --var-file.

Background runs use a detached worker that survives this command's exit.
State and private worker logs are stored under .ntm/pipelines/ in the project.
Run status/cancel from that project directory. Dry runs never spawn a worker.
Templates must exist at launch or use absolute paths when generated later.

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

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			res, err := resolvePipelineSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			session = res.Session

			projectDir, err := resolvePipelineProjectDirForSession(cmd.Context(), session)
			if err != nil {
				return err
			}

			vars, err := parsePipelineRunVariables(varsFile, varsFlag)
			if err != nil {
				return err
			}

			// JSON mode
			if jsonOutput {
				opts := pipeline.PipelineRunOptions{
					WorkflowFile:  workflowPath,
					Session:       session,
					ProjectDir:    projectDir,
					Variables:     vars,
					DryRun:        dryRun,
					Background:    background,
					StartFromStep: startFromStep,
					FromState:     fromState,
				}
				exitCode := pipeline.PrintPipelineRun(opts)
				if exitCode != 0 {
					return newSilentExitError(exitCode)
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

			varValidation, varErr := pipeline.ValidateWorkflowVariables(workflow, vars)
			if varErr != nil {
				fmt.Fprintln(os.Stderr, "Variable validation failed:")
				fmt.Printf("  ❌ %s\n", varErr.Message)
				if varErr.Hint != "" {
					fmt.Printf("     💡 %s\n", varErr.Hint)
				}
				return fmt.Errorf("variable validation failed")
			}
			vars = varValidation.Variables

			for _, w := range result.Warnings {
				fmt.Printf("  ⚠️  %s\n", w.Message)
			}
			for _, w := range varValidation.Warnings {
				fmt.Printf("  ⚠️  %s\n", w.Message)
			}

			fmt.Printf("✓ Validated workflow: %s (%d steps)\n", workflow.Name, len(workflow.Steps))
			if desc := pipeline.SanitizeDescriptionForTerminal(strings.TrimSpace(workflow.Description)); desc != "" {
				fmt.Printf("   Description: %s\n", desc)
			}
			if dryRun {
				fmt.Print(pipeline.RenderSideEffectManifestText(pipeline.BuildSideEffectManifest(workflow)))
			}
			fmt.Println()

			// Create executor
			execCfg := pipeline.DefaultExecutorConfig(session)
			execCfg.DryRun = dryRun
			execCfg.ProjectDir = projectDir
			execCfg.WorkflowFile = workflowPath
			if startFromStep != "" {
				execCfg.StartFromStep = startFromStep
				if fromState != "" {
					prior, err := pipeline.LoadState(projectDir, fromState)
					if err != nil {
						return fmt.Errorf("--from-state: load run %q: %w", fromState, err)
					}
					execCfg.StartFromState = prior
				}
			} else if fromState != "" {
				return fmt.Errorf("--from-state requires --start-from")
			}
			// Create progress channel
			progress := make(chan pipeline.ProgressEvent, 100)
			ctx := cmd.Context()

			if background && !dryRun {
				exec := pipeline.StartBackgroundPipeline(workflow, vars, execCfg)
				if exec.Error != "" || exec.Status == "failed" {
					return fmt.Errorf("background pipeline %s failed to start: %s", exec.RunID, exec.Error)
				}

				fmt.Printf("✓ Pipeline started in background\n")
				fmt.Printf("   Run ID: %s\n", exec.RunID)
				fmt.Printf("\n   Check status: ntm pipeline status %s\n", exec.RunID)
				fmt.Printf("   Cancel: ntm pipeline cancel %s\n", exec.RunID)
				return nil
			}

			// Foreground mode - show progress
			type executionResult struct {
				state *pipeline.ExecutionState
				err   error
			}
			done := make(chan executionResult, 1)
			go func() {
				var result executionResult
				defer func() {
					if r := recover(); r != nil {
						result.err = fmt.Errorf("pipeline execution panicked: %v", r)
					}
					done <- result
				}()
				defer close(progress)
				result.state, result.err = pipeline.RunControlledPipeline(ctx, workflow, vars, execCfg, progress)
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
				case result := <-done:
					// Drain remaining events
					if progress != nil {
						for event := range progress {
							printProgressEvent(event)
						}
					}

					fmt.Println()
					if result.err != nil {
						return result.err
					}
					state := result.state
					if state == nil {
						fmt.Fprintf(os.Stderr, "❌ Pipeline crashed unexpectedly\n")
						return newSilentExitError(1)
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
	cmd.Flags().StringVar(&startFromStep, "start-from", "", "Begin execution at the given step ID; transitive dependencies are marked Skipped")
	cmd.Flags().StringVar(&fromState, "from-state", "", "Run ID whose persisted outputs should be reused for steps skipped by --start-from")

	return cmd
}

func parsePipelineRunVariables(varsFile string, varsFlag []string) (map[string]interface{}, error) {
	vars := make(map[string]interface{})

	if varsFile != "" {
		data, err := os.ReadFile(varsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read var file: %w", err)
		}
		// json.Unmarshal of a top-level "null" decodes into a nil map without
		// returning an error, which would panic on subsequent --var writes.
		// Decode into an interface{} first so we can validate the shape and
		// surface a user-facing error for null/non-object var files.
		var raw interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("failed to parse var file: %w", err)
		}
		if raw == nil {
			return nil, fmt.Errorf("var file %q decoded to null; expected a JSON object of variable names to values", varsFile)
		}
		obj, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("var file %q decoded to %T; expected a JSON object of variable names to values", varsFile, raw)
		}
		vars = obj
	}

	for _, v := range varsFlag {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid variable format: %q (expected key=value)", v)
		}
		vars[parts[0]] = parts[1]
	}

	return vars, nil
}

// newPipelineStatusCmd creates the "pipeline status" subcommand
func newPipelineStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [run-id]",
		Short: "Check pipeline status",
		Long: `Display the status of a running or completed pipeline.

Without a run-id, shows all running pipelines.

Examples:
  # Check specific pipeline
  ntm pipeline status run-20241230-123456-abcd

  # List all running pipelines
  ntm pipeline status`,
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
					return newSilentExitError(exitCode)
				}
				return nil
			}

			return showPipelineStatus(runID)
		},
	}

	return cmd
}

// newPipelineListCmd creates the "pipeline list" subcommand
func newPipelineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all tracked pipelines",
		Long: `List all pipelines that have been run in this session.

Live pipelines are tracked in memory; completed runs are also read from the
persisted state files in .ntm/pipelines/ (searched upward from the current
directory), so runs from earlier ntm processes remain visible.`,
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
					return newSilentExitError(exitCode)
				}
				return nil
			}

			// Human-friendly cancel
			fmt.Printf("Cancelling pipeline: %s\n", runID)
			exitCode := pipeline.PrintPipelineCancel(runID)
			if exitCode != 0 {
				return fmt.Errorf("pipeline cancellation was not confirmed")
			}
			output.SuccessCheck("Cancellation acknowledged; inspect status for cleanup completion")
			return nil
		},
	}
}

// newPipelineResumeCmd creates the "pipeline resume" subcommand
func newPipelineResumeCmd() *cobra.Command {
	var (
		session                     string
		background                  bool
		mode                        string
		keepState                   bool
		maxResumeAge                string
		onRosterChange              string
		stepID                      string
		iteration                   int
		maxSelectorOutputBytesTotal int64
		maxStepCountTotal           int
		maxMaterializedStepNodes    int
	)

	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a pipeline from saved state",
		Long: `Resume a previously interrupted pipeline from its last checkpoint.

Pipeline state is persisted to .ntm/pipelines/<run-id>.json after each step.
Confirmed prompt/template deliveries resume observation of the original physical
pane and process, without sending again. Saved completed responses are reused.
Unknown delivery outcomes, replaced panes, and lost output boundaries require
operator inspection. If the original agent received an uncertain delivery,
use 'ntm pipeline recover' to record that observation before resuming.
Commands with durable settled outcomes retain ordinary
retry behavior. A command interrupted by a worker crash is never automatically
repeated: its process group may still be running or have performed side effects.

After inspection and stopping any surviving command processes,
--mode=restart-failed deliberately repeats failed or interrupted steps;
--keep-state=false repeats the entire workflow.
--mode=force-iter deliberately repeats the selected iteration and later ones.
These explicit restart choices may repeat side effects.

If retained selector output exhausts its aggregate budget, raise
--max-selector-output-bytes-total for this resume attempt. Existing selector
outcomes stay intact; the saved workflow's limits remain unchanged.
Use --max-step-count-total to raise distinct runtime-step admission capacity,
or --max-materialized-step-nodes to raise transient planning capacity. These
overrides also apply only to this attempt and do not authorize replay.

Examples:
  # Resume a specific pipeline
  ntm pipeline resume run-20241230-123456-abcd --session myproject

  # Continue recovery after this terminal exits
  ntm pipeline resume run-20241230-123456-abcd --background --mode=restart-failed

  # Resume will pick up from the last incomplete step`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxSelectorOutputBytesTotal < 0 {
				return fmt.Errorf("--max-selector-output-bytes-total must be zero or positive")
			}
			if maxStepCountTotal < 0 {
				return fmt.Errorf("--max-step-count-total must be zero or positive")
			}
			if maxMaterializedStepNodes < 0 {
				return fmt.Errorf("--max-materialized-step-nodes must be zero or positive")
			}
			runID := args[0]
			resumeOpts := pipeline.ResumeOptions{
				Mode:                        pipeline.ResumeMode(mode),
				KeepState:                   keepState,
				OnRosterChange:              pipeline.ResumeRosterChangePolicy(onRosterChange),
				StepID:                      stepID,
				Iteration:                   iteration,
				MaxSelectorOutputBytesTotal: maxSelectorOutputBytesTotal,
				MaxStepCountTotal:           maxStepCountTotal,
				MaxMaterializedStepNodes:    maxMaterializedStepNodes,
			}
			// normalizeResumeOptions derives KeepState from Reset (bd-uyjdn), so a
			// KeepState:false set here alone was silently flipped back to true and
			// --keep-state=false never reset anything. Reset is how the caller
			// expresses that intent, and only an explicitly passed flag counts —
			// otherwise the documented keep-state default would invert.
			if cmd.Flags().Changed("keep-state") && !keepState {
				resumeOpts.Reset = true
			}
			if maxResumeAge != "" {
				age, err := parseDuration(maxResumeAge)
				if err != nil {
					return fmt.Errorf("--max-resume-age: %w", err)
				}
				resumeOpts.MaxResumeAge = age
			}

			resolvedSession := strings.TrimSpace(session)
			if resolvedSession != "" {
				if err := tmux.EnsureInstalled(); err != nil {
					return err
				}
				res, err := resolvePipelineSession(resolvedSession, cmd.OutOrStdout())
				if err != nil {
					return err
				}
				resolvedSession = res.Session
			}

			projectDir, err := resolvePipelineProjectDirForSession(cmd.Context(), resolvedSession)
			if err != nil {
				return err
			}

			if background {
				execution, err := pipeline.StartBackgroundResume(cmd.Context(), projectDir, runID, resolvedSession, resumeOpts)
				if err != nil {
					if jsonOutput {
						code := "RESUME_START_FAILED"
						if errors.Is(err, pipeline.ErrRunAlreadyOwned) {
							code = "PIPELINE_RUNNING"
						}
						return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
							"success": false, "run_id": runID, "error": err.Error(), "error_code": code,
						}, err)
					}
					return fmt.Errorf("start background pipeline resume: %w", err)
				}
				if jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
						"success": true, "run_id": execution.RunID, "status": execution.Status,
						"workflow": execution.WorkflowID, "session": execution.Session,
						"mode": string(resumeOpts.Mode), "background": true,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Pipeline %s resume started in background (%s).\n", execution.RunID, execution.Session)
				fmt.Fprintf(cmd.OutOrStdout(), "Inspect with 'ntm pipeline status %s'; cancel with 'ntm pipeline cancel %s'.\n", execution.RunID, execution.RunID)
				return nil
			}

			// Join the same ownership protocol as API and detached executions.
			// Load the checkpoint only AFTER acquiring it: no stale snapshot
			// may queue behind an owner and replay work the owner completed.
			control, err := pipeline.AcquireRunControl(cmd.Context(), projectDir, runID)
			if err != nil {
				if jsonOutput {
					code := "CONTROL_UNAVAILABLE"
					if errors.Is(err, pipeline.ErrRunAlreadyOwned) {
						code = "PIPELINE_RUNNING"
					}
					return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
						"success": false, "run_id": runID, "error": err.Error(), "error_code": code,
					}, err)
				}
				return fmt.Errorf("acquire pipeline resume ownership: %w", err)
			}
			defer control.Close()

			state, err := pipeline.LoadState(projectDir, runID)
			if err != nil {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    false,
						"error":      err.Error(),
						"error_code": "STATE_NOT_FOUND",
						"run_id":     runID,
					}
					return emitJSONFailureEnvelopeWithCause(result, err)
				}
				return fmt.Errorf("failed to load pipeline state: %w", err)
			}

			if resolvedSession == "" {
				resolvedSession = state.Session
			}
			if resolvedSession == "" {
				return fmt.Errorf("--session is required (or state must contain session)")
			}

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			res, err := resolvePipelineSession(resolvedSession, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			session = res.Session

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

			workflow, result, err := pipeline.LoadResumeWorkflow(workflowFile)
			if err != nil {
				return fmt.Errorf("failed to load workflow: %w", err)
			}

			if !result.Valid {
				if jsonOutput {
					cause := errors.New("workflow validation failed")
					result := map[string]interface{}{
						"success":    false,
						"error":      "workflow validation failed",
						"error_code": "INVALID_WORKFLOW",
						"run_id":     runID,
					}
					return emitJSONFailureEnvelopeWithCause(result, cause)
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

			state.WorkflowFile = workflowFile

			ctx := control.Context()

			if jsonOutput {
				finalState, err := executor.ResumeWithOptions(ctx, workflow, state, resumeOpts, nil)
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
					return emitJSONFailureEnvelopeWithCause(result, err)
				}

				result := map[string]interface{}{
					"success":  true,
					"run_id":   runID,
					"status":   string(finalState.Status),
					"workflow": workflow.Name,
					"session":  session,
					"mode":     string(resumeOpts.Mode),
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}

			fmt.Printf("📋 Resuming pipeline: %s\n", runID)
			fmt.Printf("   Session: %s\n", session)
			if resumeOpts.Mode != "" {
				fmt.Printf("   Mode: %s\n", resumeOpts.Mode)
			}
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
				state, _ := executor.ResumeWithOptions(ctx, workflow, state, resumeOpts, progress)
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
						return newSilentExitError(1)
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
	cmd.Flags().BoolVar(&background, "background", false, "Resume in a detached worker that survives this command's exit")
	cmd.Flags().StringVar(&mode, "mode", string(pipeline.ResumeModeContinue), "Resume mode: continue, restart-failed, force-iter")
	cmd.Flags().BoolVar(&keepState, "keep-state", true, "Preserve completed step outputs while resuming")
	cmd.Flags().StringVar(&maxResumeAge, "max-resume-age", "", "Refuse to resume state older than this duration (for example 7d, 24h)")
	cmd.Flags().Int64Var(&maxSelectorOutputBytesTotal, "max-selector-output-bytes-total", 0, "Override the selector output budget in bytes for this resume attempt (0 uses the saved workflow)")
	cmd.Flags().IntVar(&maxStepCountTotal, "max-step-count-total", 0, "Override distinct runtime-step admission capacity for this resume attempt (0 uses the saved workflow)")
	cmd.Flags().IntVar(&maxMaterializedStepNodes, "max-materialized-step-nodes", 0, "Override transient step-planning capacity for this resume attempt (0 uses the saved workflow)")
	cmd.Flags().StringVar(&onRosterChange, "on-roster-change", string(pipeline.ResumeRosterAbort), "Roster-change policy: abort or proceed")
	cmd.Flags().StringVar(&stepID, "step-id", "", "Step ID used with --mode=force-iter")
	cmd.Flags().IntVar(&iteration, "iteration", 0, "Iteration index used with --mode=force-iter")

	return cmd
}

// newPipelineRecoverCmd records explicit recovery decisions without running the
// workflow. Ordinary resume interprets the original outcome and runs dependents.
func newPipelineRecoverCmd() *cobra.Command {
	var stepID, receipt, reason, outputFile string
	var acceptDelivery, acceptCommand, acceptSelector bool
	var exitCode int
	cmd := &cobra.Command{
		Use:   "recover <run-id>",
		Short: "Inspect and reconcile interrupted pipeline deliveries, commands, and selectors",
		Long: `Inspect durable delivery, command, and selector receipts after a pipeline interruption.

With no flags, shows delivery identities and receipt tokens without changing the
run. After verifying that the original agent received its prompt, use
--accept-delivery with the exact --step-id, --receipt, and a --reason describing
your observation. Acceptance records that decision; it does not send a prompt,
declare the task complete, or execute downstream steps. Resume the run normally
to observe the original response and continue the workflow.

Acceptance requires the original tmux host, physical pane and process, intact
response evidence, and the frozen workflow. Resume retains its exact step
definition checks. A live pipeline owner or
stale receipt prevents recovery. Repeating the same receipt and reason returns
the existing decision. For an intentional resend, inspect the agent first and
use 'pipeline resume --mode=restart-failed', which may repeat side effects.

For an unresolved foreground command in the main workflow, first independently
verify that the original command AND all its descendants have finished. --accept-command records
your explicit attestation of that fact and the observed result. A saved PID is
diagnostic evidence only; recovery never assumes that a missing or reused PID
proves completion, and never signals a saved process ID.

Use the exact --step-id and --receipt, a non-empty --reason, an explicit
--exit-code (0 to 255), and --output-file containing the complete captured output
as UTF-8 text.
An empty regular file explicitly records no output. For a command with
output_parse, supply stdout; otherwise supply its captured combined stdout and
stderr. Output is bounded by the recovery limit and the frozen workflow's limit.
Acceptance does not launch the command, parse its output, or run dependent work.
Ordinary resume applies the original command's exit-code and output parsing
rules. A nonzero exit code uses normal failure handling without rerunning the
command.
Background commands, cleanup and post-pipeline commands, and commands whose
enclosing work already finished cannot use this recovery path.
The same receipt, reason, exit code, and exact output
replay the saved decision; changing any part is refused.

For an interrupted shell branch, external foreach source, or bead query, use
--accept-selector after independently verifying that the original selector AND
all its descendants have finished. Supply its exact --step-id and --receipt,
a non-empty --reason, an explicit --exit-code (0 to 255), and --output-file
containing its complete stdout. Selector stdout is retained byte-for-byte,
including non-UTF-8 data, and is subject to the saved capture limits.
Acceptance records an operator-observed outcome without launching the source,
choosing a branch, admitting items, or replacing already-consumed results.
Ordinary resume checks the original selector definition and expanded invocation
before using successful output. A nonzero accepted exit code follows normal
failure handling without rerunning the source. A receipt without the original
invocation identity, an already-admitted source, or a completed enclosing scope
cannot be accepted. The accepted outcome is immutable; intentional reevaluation
still requires an explicit replay scope.

Run from the project containing the saved pipeline state.

Examples:
  ntm pipeline recover run-20241230-123456-abcd
  ntm pipeline recover run-20241230-123456-abcd --step-id review \
    --receipt <token-from-inspection> --accept-delivery \
    --reason "The original agent is working on the submitted review"
  ntm pipeline recover run-20241230-123456-abcd --step-id migrate \
    --receipt <token-from-inspection> --accept-command --exit-code 0 \
    --output-file migration-output.txt \
    --reason "Verified the migration and all child processes finished successfully"
  ntm pipeline recover run-20241230-123456-abcd --step-id choose \
    --receipt <token-from-inspection> --accept-selector --exit-code 0 \
    --output-file selector-stdout.bin \
    --reason "Verified the original source and all descendants finished"
  ntm pipeline resume run-20241230-123456-abcd --mode=continue`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			fail := func(err error) error {
				if !jsonOutput {
					return err
				}
				code := "RECOVERY_REFUSED"
				if errors.Is(err, pipeline.ErrRunAlreadyOwned) {
					code = "PIPELINE_RUNNING"
				}
				return emitJSONFailureEnvelopeToWithCause(cmd.OutOrStdout(), map[string]interface{}{
					"success": false, "run_id": runID, "error": err.Error(), "error_code": code,
				}, err)
			}
			decisions := 0
			for _, selected := range []bool{acceptDelivery, acceptCommand, acceptSelector} {
				if selected {
					decisions++
				}
			}
			if decisions > 1 {
				return fail(errors.New("--accept-delivery, --accept-command, and --accept-selector are mutually exclusive"))
			}
			acceptOutcome := acceptCommand || acceptSelector
			commandFields := cmd.Flags().Changed("exit-code") || cmd.Flags().Changed("output-file")
			if decisions == 0 && (cmd.Flags().Changed("step-id") || cmd.Flags().Changed("receipt") || cmd.Flags().Changed("reason") || commandFields) {
				return fail(errors.New("recovery decision fields require --accept-delivery, --accept-command, or --accept-selector"))
			}
			if !acceptOutcome && commandFields {
				return fail(errors.New("--exit-code and --output-file require --accept-command or --accept-selector"))
			}
			if decisions > 0 && (strings.TrimSpace(stepID) == "" || strings.TrimSpace(receipt) == "" || strings.TrimSpace(reason) == "") {
				return fail(errors.New("acceptance requires --step-id, --receipt, and a non-empty --reason"))
			}
			if acceptOutcome && (!cmd.Flags().Changed("exit-code") || !cmd.Flags().Changed("output-file") || strings.TrimSpace(outputFile) == "" || exitCode < 0 || exitCode > 255) {
				return fail(errors.New("--accept-command or --accept-selector requires an explicit --exit-code from 0 to 255 and --output-file"))
			}
			projectDir, err := resolvePipelineProjectDirForSession(cmd.Context(), "")
			if err != nil {
				return fail(err)
			}
			if acceptOutcome {
				if err := cmd.Context().Err(); err != nil {
					return fail(err)
				}
				info, err := os.Stat(outputFile)
				if err != nil {
					return fail(fmt.Errorf("inspect captured command output: %w", err))
				}
				if !info.Mode().IsRegular() {
					return fail(errors.New("--output-file must name a regular file; use an empty file to explicitly record no output"))
				}
				file, err := os.Open(outputFile)
				if err != nil {
					return fail(fmt.Errorf("open captured command output: %w", err))
				}
				defer file.Close()
				info, err = file.Stat()
				if err != nil {
					return fail(fmt.Errorf("inspect captured command output: %w", err))
				}
				if !info.Mode().IsRegular() {
					return fail(errors.New("--output-file must name a regular file; use an empty file to explicitly record no output"))
				}
				if info.Size() > pipeline.CommandRecoveryMaxOutputBytes {
					return fail(fmt.Errorf("captured command output exceeds %d bytes", pipeline.CommandRecoveryMaxOutputBytes))
				}
				captured, err := io.ReadAll(io.LimitReader(file, pipeline.CommandRecoveryMaxOutputBytes+1))
				if err != nil {
					return fail(fmt.Errorf("read captured command output: %w", err))
				}
				if len(captured) > pipeline.CommandRecoveryMaxOutputBytes {
					return fail(fmt.Errorf("captured command output exceeds %d bytes", pipeline.CommandRecoveryMaxOutputBytes))
				}
				if err := cmd.Context().Err(); err != nil {
					return fail(err)
				}
				if acceptSelector {
					result, err := pipeline.AcceptCommandEvaluationContext(cmd.Context(), projectDir, runID, stepID, receipt, reason, exitCode, captured)
					if err != nil {
						return fail(err)
					}
					if jsonOutput {
						return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
							"success": true, "run_id": result.RunID, "workflow": result.WorkflowID,
							"session": result.Session, "selector": result.Selector, "replayed": result.Replayed,
							"resume_command": []string{"ntm", "pipeline", "resume", result.RunID, "--mode=continue"},
						})
					}
					verb := "Accepted"
					if result.Replayed {
						verb = "Previously accepted"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s selector result for step %s in run %s.\n", verb,
						pipeline.SanitizeDescriptionForTerminal(result.Selector.StepID), pipeline.SanitizeDescriptionForTerminal(result.RunID))
					fmt.Fprintln(cmd.OutOrStdout(), "Resume this run with 'ntm pipeline resume' to interpret the recorded outcome and continue.")
					return nil
				}
				result, err := pipeline.AcceptCommandExecutionContext(cmd.Context(), projectDir, runID, stepID, receipt, reason, exitCode, string(captured))
				if err != nil {
					return fail(err)
				}
				if jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
						"success": true, "run_id": result.RunID, "workflow": result.WorkflowID,
						"session": result.Session, "command": result.Command, "replayed": result.Replayed,
						"resume_command": []string{"ntm", "pipeline", "resume", result.RunID, "--mode=continue"},
					})
				}
				verb := "Accepted"
				if result.Replayed {
					verb = "Previously accepted"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s command result for step %s in run %s.\n", verb,
					pipeline.SanitizeDescriptionForTerminal(result.Command.StepID), pipeline.SanitizeDescriptionForTerminal(result.RunID))
				fmt.Fprintln(cmd.OutOrStdout(), "Resume this run with 'ntm pipeline resume' to interpret the recorded result and continue.")
				return nil
			}
			if acceptDelivery {
				result, err := pipeline.AcceptAgentDeliveryContext(cmd.Context(), projectDir, runID, stepID, receipt, reason)
				if err != nil {
					return fail(err)
				}
				if jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
						"success": true, "run_id": result.RunID, "workflow": result.WorkflowID,
						"session": result.Session, "delivery": result.Delivery, "replayed": result.Replayed,
						"resume_command": []string{"ntm", "pipeline", "resume", result.RunID, "--mode=continue"},
					})
				}
				verb := "Accepted"
				if result.Replayed {
					verb = "Previously accepted"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s delivery for step %s in run %s.\n", verb,
					pipeline.SanitizeDescriptionForTerminal(result.Delivery.StepID), pipeline.SanitizeDescriptionForTerminal(result.RunID))
				fmt.Fprintln(cmd.OutOrStdout(), "Resume this run with 'ntm pipeline resume' to observe the original response and continue.")
				return nil
			}
			inspection, err := pipeline.InspectAgentDeliveryRecovery(cmd.Context(), projectDir, runID)
			if err != nil {
				return fail(err)
			}
			commands, err := pipeline.InspectCommandExecutionRecovery(cmd.Context(), projectDir, runID)
			if err != nil {
				return fail(err)
			}
			selectors, err := pipeline.InspectCommandEvaluationRecovery(cmd.Context(), projectDir, runID)
			if err != nil {
				return fail(err)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
					"success": true, "run_id": inspection.RunID, "workflow": inspection.WorkflowID,
					"session": inspection.Session, "deliveries": inspection.Deliveries, "commands": commands.Commands,
					"selectors": selectors.Selectors,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pipeline %s (%s)\n", pipeline.SanitizeDescriptionForTerminal(inspection.RunID), pipeline.SanitizeDescriptionForTerminal(inspection.Session))
			if len(inspection.Deliveries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No agent-delivery receipts are available for recovery.")
			}
			for _, delivery := range inspection.Deliveries {
				fmt.Fprintf(cmd.OutOrStdout(), "\nStep: %s  Status: %s\n  Target: %s / %s (PID %d)\n  Receipt: %s\n",
					pipeline.SanitizeDescriptionForTerminal(delivery.StepID), pipeline.SanitizeDescriptionForTerminal(delivery.Status),
					pipeline.SanitizeDescriptionForTerminal(delivery.Endpoint), pipeline.SanitizeDescriptionForTerminal(delivery.PaneID),
					delivery.PanePID, delivery.ReceiptToken)
				if delivery.ResolutionAction != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Decision: %s at %s\n  Reason: %s\n",
						pipeline.SanitizeDescriptionForTerminal(delivery.ResolutionAction), delivery.ResolvedAt.Format(time.RFC3339),
						pipeline.SanitizeDescriptionForTerminal(delivery.ResolutionReason))
				}
			}
			if len(inspection.Deliveries) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nFor an uncertain 'sending' receipt, verify that the original agent received it, then repeat with --step-id, --receipt, --accept-delivery, and --reason. Confirmed deliveries can resume normally.")
			}
			for _, command := range commands.Commands {
				fmt.Fprintf(cmd.OutOrStdout(), "\nCommand step: %s  Status: %s\n  Recorded PID: %d  Background: %t\n  Receipt: %s\n  Can accept: %t\n",
					pipeline.SanitizeDescriptionForTerminal(command.StepID), pipeline.SanitizeDescriptionForTerminal(command.Status),
					command.ProcessID, command.Background, command.ReceiptToken, command.CanAccept)
				if command.RefusalReason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Recovery unavailable: %s\n", pipeline.SanitizeDescriptionForTerminal(command.RefusalReason))
				}
				if command.ResolutionAction != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Decision: %s at %s\n  Reason: %s\n",
						pipeline.SanitizeDescriptionForTerminal(command.ResolutionAction), command.ResolvedAt.Format(time.RFC3339),
						pipeline.SanitizeDescriptionForTerminal(command.ResolutionReason))
				}
			}
			if len(commands.Commands) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nTo accept an unresolved foreground command, verify the original command and all descendants have finished, then supply --accept-command, --step-id, --receipt, --reason, --exit-code, and --output-file. Acceptance never runs the command.")
			}
			for _, selector := range selectors.Selectors {
				fmt.Fprintf(cmd.OutOrStdout(), "\nSelector step: %s  Status: %s\n  Recorded PID: %d\n  Receipt: %s\n  Can accept: %t\n",
					pipeline.SanitizeDescriptionForTerminal(selector.StepID), pipeline.SanitizeDescriptionForTerminal(selector.Status),
					selector.ProcessID, selector.ReceiptToken, selector.CanAccept)
				if selector.RefusalReason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Recovery unavailable: %s\n", pipeline.SanitizeDescriptionForTerminal(selector.RefusalReason))
				}
				if selector.ResolutionAction != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Decision: %s at %s\n  Reason: %s\n",
						pipeline.SanitizeDescriptionForTerminal(selector.ResolutionAction), selector.ResolvedAt.Format(time.RFC3339),
						pipeline.SanitizeDescriptionForTerminal(selector.ResolutionReason))
					if selector.ExitCode != nil {
						fmt.Fprintf(cmd.OutOrStdout(), "  Exit code: %d  Stdout bytes: %d\n  Stdout SHA-256: %s\n",
							*selector.ExitCode, selector.OutputBytes, selector.OutputHash)
					}
				}
			}
			if len(selectors.Selectors) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nTo accept an interrupted selector, verify the original source and all descendants have finished, then supply --accept-selector, --step-id, --receipt, --reason, --exit-code, and --output-file containing complete stdout. Acceptance never evaluates the source.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&stepID, "step-id", "", "Exact runtime step ID from recovery inspection")
	cmd.Flags().StringVar(&receipt, "receipt", "", "Exact receipt token from recovery inspection")
	cmd.Flags().BoolVar(&acceptDelivery, "accept-delivery", false, "Record that the original agent received the uncertain delivery")
	cmd.Flags().BoolVar(&acceptCommand, "accept-command", false, "Attest that the original command and all descendants finished, and record their observed result")
	cmd.Flags().BoolVar(&acceptSelector, "accept-selector", false, "Attest that the original selector and all descendants finished, and record their observed result")
	cmd.Flags().IntVar(&exitCode, "exit-code", 0, "Observed exit code (0 to 255); explicitly required with --accept-command or --accept-selector")
	cmd.Flags().StringVar(&outputFile, "output-file", "", "Regular file containing complete captured output; explicitly required with --accept-command or --accept-selector")
	cmd.Flags().StringVar(&reason, "reason", "", "Observation confirming the original delivery or completed command outcome")
	return cmd
}

// newPipelineCleanupCmd creates the "pipeline cleanup" subcommand
func newPipelineCleanupCmd() *cobra.Command {
	var olderThan string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up old, settled pipeline state files",
		Long: `Remove old pipeline state files only when execution has safely settled.

State files are stored in .ntm/pipelines/ and can accumulate over time.
Cleanup checks ownership and saved execution receipts before removing a file.
Live runs, unresolved receipts, and states that cannot be safely inspected are
retained for recovery. The dry run performs the same checks and lists the result
for each saved run without removing files.

Examples:
  # Remove eligible state files older than 7 days
  ntm pipeline cleanup --older 7d

  # Remove eligible state files older than 30 days
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
			projectDir, err := resolvePipelineProjectDirForSession(cmd.Context(), "")
			if err != nil {
				return err
			}

			report, err := pipeline.CleanupStates(cmd.Context(), projectDir, duration, dryRun)
			if err != nil {
				if jsonOutput {
					result := map[string]interface{}{
						"success":    false,
						"error":      err.Error(),
						"error_code": "CLEANUP_FAILED",
					}
					if report != nil {
						result["deleted"] = report.Deleted
						result["eligible"] = report.Eligible
						result["retained"] = report.Retained
						result["entries"] = report.Entries
					}
					return emitJSONFailureEnvelopeWithCause(result, err)
				}
				return fmt.Errorf("cleanup failed: %w", err)
			}

			if jsonOutput {
				result := map[string]interface{}{
					"success":     true,
					"dry_run":     dryRun,
					"deleted":     report.Deleted,
					"eligible":    report.Eligible,
					"retained":    report.Retained,
					"entries":     report.Entries,
					"older_than":  olderThan,
					"duration":    duration.String(),
					"project_dir": projectDir,
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}

			if dryRun {
				fmt.Printf("Dry run: %d state file(s) eligible for cleanup; %d retained.\n", report.Eligible, report.Retained)
			} else if report.Deleted == 0 {
				fmt.Printf("No eligible state files to clean up; %d retained.\n", report.Retained)
			} else {
				output.SuccessCheck(fmt.Sprintf("Cleaned up %d state file(s) older than %s; %d retained", report.Deleted, duration, report.Retained))
			}
			for _, entry := range report.Entries {
				if dryRun || entry.Action == "retained" {
					fmt.Printf("  %s: %s (%s)\n", entry.RunID, entry.Action, entry.Reason)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&olderThan, "older", "", "Remove eligible files older than duration (e.g., 7d, 30d)")
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

			res, err := resolvePipelineSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			session = res.Session

			return pipeline.Execute(context.Background(), pipeline.Pipeline{
				Session: session,
				Stages:  pipeStages,
			})
		},
	}

	cmd.Flags().StringArrayVar(&stages, "stage", nil, "Pipeline stage (format: 'type: prompt')")

	return cmd
}

func resolvePipelineProjectDirForSession(ctx context.Context, session string) (string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
		if err != nil {
			return "", err
		}
		session = resolved
		return resolveExplicitProjectDirForSessionContext(ctx, session)
	}

	projectDir := GetProjectRoot()
	if projectDir == "" {
		return "", fmt.Errorf("getting project root failed")
	}
	return projectDir, nil
}

func resolvePipelineSession(session string, w io.Writer) (SessionResolution, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return SessionResolution{}, fmt.Errorf("session is required")
	}
	res, err := ResolveSessionWithOptions(session, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		return SessionResolution{}, err
	}
	if res.Session == "" {
		return SessionResolution{}, fmt.Errorf("session is required")
	}
	if !tmux.SessionExists(res.Session) {
		return SessionResolution{}, fmt.Errorf("session %q not found", res.Session)
	}
	return res, nil
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
	exec := pipeline.GetPipelineSnapshot(runID)
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

	if len(exec.Progress.SkipKindCounts) > 0 {
		kinds := make([]string, 0, len(exec.Progress.SkipKindCounts))
		for k := range exec.Progress.SkipKindCounts {
			kinds = append(kinds, string(k))
		}
		sort.Strings(kinds)
		fmt.Println("\nSkipped by kind:")
		for _, k := range kinds {
			fmt.Printf("  %-32s %d\n", k, exec.Progress.SkipKindCounts[pipeline.SkipKind(k)])
		}
	}

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
			line := fmt.Sprintf("  [%s] %s", id, status)
			if step.SkipKind != "" {
				line += fmt.Sprintf(" (%s)", step.SkipKind)
			}
			if step.SkipReason != "" {
				line += fmt.Sprintf(": %s", step.SkipReason)
			}
			fmt.Println(line)
		}
	}

	if exec.Error != "" {
		fmt.Printf("\nError: %s\n", exec.Error)
	}

	return nil
}

func showPipelineList() error {
	pipelines := pipeline.GetAllPipelineSnapshots()

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
