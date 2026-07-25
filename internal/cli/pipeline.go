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

	lintResult := pipelineLintOutput{
		Success:            result.Valid,
		WorkflowFile:       workflowPath,
		Workflow:           workflow.Name,
		Valid:              result.Valid,
		StepCount:          len(workflow.Steps),
		Errors:             result.Errors,
		Warnings:           result.Warnings,
		NormalizedWorkflow: workflow,
		Summary: pipelineLintSummaryJSON{
			Errors:   len(result.Errors),
			Warnings: len(result.Warnings),
		},
	}
	if !result.Valid {
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
			executor := pipeline.NewExecutor(execCfg)

			// Create progress channel
			progress := make(chan pipeline.ProgressEvent, 100)
			ctx := context.Background()

			if background {
				exec := pipeline.StartBackgroundPipeline(workflow, vars, execCfg)

				fmt.Printf("✓ Pipeline started in background\n")
				fmt.Printf("   Run ID: %s\n", exec.RunID)
				fmt.Printf("\n   Check status: ntm pipeline status %s\n", exec.RunID)
				fmt.Printf("   Cancel: ntm pipeline cancel %s\n", exec.RunID)
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
					os.Exit(exitCode)
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
	var (
		session        string
		mode           string
		keepState      bool
		maxResumeAge   string
		onRosterChange string
		stepID         string
		iteration      int
	)

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
			resumeOpts := pipeline.ResumeOptions{
				Mode:           pipeline.ResumeMode(mode),
				KeepState:      keepState,
				OnRosterChange: pipeline.ResumeRosterChangePolicy(onRosterChange),
				StepID:         stepID,
				Iteration:      iteration,
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

			workflow, result, err := pipeline.LoadAndValidate(workflowFile)
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
			execCfg.ResumeOptions = resumeOpts
			executor := pipeline.NewExecutor(execCfg)

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
	cmd.Flags().StringVar(&mode, "mode", string(pipeline.ResumeModeContinue), "Resume mode: continue, restart-failed, force-iter")
	cmd.Flags().BoolVar(&keepState, "keep-state", true, "Preserve completed step outputs while resuming")
	cmd.Flags().StringVar(&maxResumeAge, "max-resume-age", "", "Refuse to resume state older than this duration (for example 7d, 24h)")
	cmd.Flags().StringVar(&onRosterChange, "on-roster-change", string(pipeline.ResumeRosterAbort), "Roster-change policy: abort or proceed")
	cmd.Flags().StringVar(&stepID, "step-id", "", "Step ID used with --mode=force-iter")
	cmd.Flags().IntVar(&iteration, "iteration", 0, "Iteration index used with --mode=force-iter")

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
			projectDir, err := resolvePipelineProjectDirForSession(cmd.Context(), "")
			if err != nil {
				return err
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
					return emitJSONFailureEnvelopeWithCause(result, err)
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
