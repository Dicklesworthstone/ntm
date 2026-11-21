package cli

// workflow_run.go wires the shared workflow runner (internal/workflowrun) to
// the Cobra CLI (`ntm workflow run <name-or-path>`), delivering stage prompts
// through the same gated `ntm send` path (`runSendWithTargets`).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
	"github.com/Dicklesworthstone/ntm/internal/workflowrun"
)

const workflowEvidenceMaxBytes = workflowrun.EvidenceMaxBytes

// WorkflowRunAgent reports one role→pane assignment in the run result.
type WorkflowRunAgent = workflowrun.RunAgent

// WorkflowRunResult is the JSON output for `ntm workflow run`.
type WorkflowRunResult = workflowrun.Result

// WorkflowRunFault reports one agent fault raised into the template's
// error_handling policy.
type WorkflowRunFault = workflowrun.RunFault

type (
	workflowAgentFault = workflowrun.AgentFault
	workflowRunOptions = workflowrun.Options
	workflowRunner     = workflowrun.Runner
	workflowRunActions = workflowrun.RunActions
)

func stageLabel(stage string) string {
	return workflowrun.StageLabel(stage)
}

func resolveWorkflowForRun(ref string) (*workflow.WorkflowTemplate, error) {
	return workflowrun.ResolveTemplate("", ref)
}

func resolveWorkflowVars(template *workflow.WorkflowTemplate, assignments []string) (map[string]string, error) {
	return workflowrun.ResolveVars(template, assignments)
}

func assignWorkflowPanes(template *workflow.WorkflowTemplate, panes []tmux.Pane) ([]workflow.CoordinatorAgent, error) {
	return workflowrun.AssignPanes(template, panes)
}

func validateWorkflowPaneLifetimes(ctx context.Context, session string, expected map[string]int) error {
	return workflowrun.ValidatePaneLifetimes(ctx, session, expected)
}

func workflowTmuxEndpoint() string {
	return workflowrun.TmuxEndpoint()
}

func validateWorkflowEndpoint(template *workflow.WorkflowTemplate, endpoint string) error {
	return workflowrun.ValidateEndpoint(template, endpoint)
}

// workflowGatedDispatch is the CLI production dispatch port: a single-pane
// send through runSendWithTargets, i.e. the same liveness-gated,
// composer-verified path as `ntm send` — never raw send-keys.
func workflowGatedDispatch(workflowName string, beforeDispatch func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error) func(ctx context.Context, session, paneID, prompt string) error {
	return func(ctx context.Context, session, paneID, prompt string) error {
		collected := &sendExecutionResult{}
		err := runSendWithTargets(SendOptions{
			Context:             ctx,
			Session:             session,
			Prompt:              prompt,
			PromptSource:        "workflow",
			TemplateName:        "workflow:" + workflowName,
			PaneSelector:        paneID,
			ForceNonInteractive: true,
			executionPolicy:     sendExecutionCollect,
			executionResult:     collected,
			beforeDispatch:      beforeDispatch,
		})
		if err != nil {
			return err
		}
		if collected.recorded && !collected.result.Success {
			if collected.result.Error != "" {
				return errors.New(collected.result.Error)
			}
			return fmt.Errorf("delivery to pane %s failed", paneID)
		}
		return nil
	}
}

func newWorkflowsRunCmd() *cobra.Command {
	var (
		sessionFlag    string
		projectRoot    string
		varFlags       []string
		maxTransitions int
		interval       time.Duration
		timeout        time.Duration
		fireManual     bool
		triggerTimeout time.Duration
		resumeFlag     bool
		restartFlag    bool
	)

	cmd := &cobra.Command{
		Use:   "run <name-or-path>",
		Short: "Run a workflow template against a live session",
		Long: `Run a workflow template's coordination loop against a live session.

The workflow's roles are mapped onto the session's agent panes in pane order,
each stage's prompt is delivered through the same gated dispatch path as
'ntm send' (dead-pane gate + composer-verified submission), and the workflow's
triggers (file_created, command_success, agent_says, manual, ...) advance the
stages until the flow completes, the transition budget is spent, or --timeout
elapses.

The argument is a template name (builtin, then ~/.config/ntm/workflows/, then
.ntm/workflows/ — 'ntm workflows list' shows all) or a path to a workflow TOML
file (anything containing a path separator or ending in .toml). An unknown
name is an error listing the builtins — there is no fallback.

Stage-to-role resolution: a stage engages the role with the same name; else
the role the template's routing table names for the stage; else the role an
outgoing transition's trigger names; else the first declared role. Stages in
parallel workflows or with parallel_within_stage engage every pane of the
role.

Note: 'ntm spawn -t <workflow>' only uses a template's agent COUNTS to size a
session; this command is what actually runs the coordination.

--resume continues the saved stage with the same template, variables, and
pane lifetimes. Confirmed prompts are not sent again; pending prompts resume
their saved plan. An interrupted send with an unknown outcome is refused.
Unfinished checkpoints require --resume, or --restart to deliberately repeat
the entire workflow. --resume never silently means restart.

For an authored manual transition, inspect 'ntm workflow status --session SESSION
--json', then use 'ntm workflow trigger --session SESSION --token TOKEN
--transition INDEX --reason TEXT' with the exact manual_gate token and transition
index. The runner applies that one decision on its next poll. A recorded decision
survives interruption and can be applied by --resume. --fire-manual explicitly
automates all manual gates instead of waiting for individual operator choices.

Every poll checks all of the workflow's agent panes. A crash (the pane left
the session or died, or the agent CLI exited back to its shell) runs the
template's error_handling on_agent_crash action; an agent error (rate limited,
authentication failure, a blocking dialog, or the status detector's error
state, lasting [resilience] crash_threshold polls) runs on_agent_error. Unset
actions only report the fault. restart_agent restarts the pane with the same
engine as 'ntm respawn' and sends the current stage prompt again, at most
[resilience] max_restarts times per pane across the entire run, including
--resume; the next fault aborts. Stage retry allowances also survive resume.

stage_timeout_minutes records a deadline for each prompt round before delivery.
Downtime counts toward it. The owner loop applies on_timeout once per round
(notify when unset); retry starts another bounded round and skip commits its
destination before sending new prompts. Handled pause/notify timeouts do not
repeat after --resume. An interrupted timeout action with an unknown outcome
refuses resume until the work is inspected and deliberately restarted.

Checkpoints bind the configured tmux endpoint (local or the exact SSH target).
Resume requires the original endpoint even when pane IDs and PIDs match.
SSH workflows support agent-output, idle, elapsed-time, and manual triggers.
File and command triggers require running ntm on the host that owns the agents;
they never inspect a same-named local directory for a remote session.
For SSH workflows, --project-root selects the local checkpoint directory's
project root and defaults to the current local directory.

Examples:
  ntm workflow run red-green --var feature="parser rewrite"
  ntm workflow run specialist-team --session myproj --fire-manual
  ntm workflow run ./my-flow.toml --session myproj --max-transitions 4
  ntm workflow run review-pipeline --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowRun(cmd.Context(), args[0], workflowRunCLIFlags{
				Session:        sessionFlag,
				ProjectRoot:    projectRoot,
				Vars:           varFlags,
				MaxTransitions: maxTransitions,
				Interval:       interval,
				Timeout:        timeout,
				FireManual:     fireManual,
				TriggerTimeout: triggerTimeout,
				Resume:         resumeFlag,
				Restart:        restartFlag,
			})
		},
	}

	cmd.Flags().StringVarP(&sessionFlag, "session", "s", "", "target session (default: inferred like 'ntm send')")
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "local project root for checkpoints and file/command triggers (default: local session working dir, else cwd)")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "workflow setup variable key=value (repeatable); required prompts without defaults must be supplied")
	cmd.Flags().IntVar(&maxTransitions, "max-transitions", 8, "stop after this many stage transitions")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "trigger poll interval")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "overall run deadline")
	cmd.Flags().BoolVar(&fireManual, "fire-manual", false, "fire manual triggers automatically instead of waiting for an operator")
	cmd.Flags().DurationVar(&triggerTimeout, "trigger-timeout", 0, "per-check deadline for command triggers (0 = library default)")
	cmd.Flags().BoolVar(&resumeFlag, "resume", false, "continue the saved stage without repeating confirmed prompt deliveries")
	cmd.Flags().BoolVar(&restartFlag, "restart", false, "explicitly replace an unfinished checkpoint and repeat the workflow from the beginning")
	cmd.MarkFlagsMutuallyExclusive("resume", "restart")
	return cmd
}

type workflowRunCLIFlags struct {
	Session        string
	ProjectRoot    string
	Vars           []string
	MaxTransitions int
	Interval       time.Duration
	Timeout        time.Duration
	FireManual     bool
	TriggerTimeout time.Duration
	Resume         bool
	Restart        bool
}

func runWorkflowRun(ctx context.Context, ref string, flags workflowRunCLIFlags) error {
	emit := func(result WorkflowRunResult, cause error) error {
		if jsonOutput {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
				return encodeErr
			}
			return cause
		}
		if cause == nil {
			fmt.Printf("workflow %s %s: stages %s, %d transition(s)\n",
				result.Workflow, result.Reason, strings.Join(result.Stages, " → "), result.Transitions)
		}
		return cause
	}
	failEarly := func(err error) error {
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}, err)
		}
		return err
	}

	endpoint := workflowTmuxEndpoint()
	template, err := resolveWorkflowForRun(ref)
	if err != nil {
		return failEarly(err)
	}
	if err := validateWorkflowEndpoint(template, endpoint); err != nil {
		return failEarly(err)
	}
	varsTemplate := *template
	if flags.Resume {
		varsTemplate.Prompts = nil
	}
	vars, err := resolveWorkflowVars(&varsTemplate, flags.Vars)
	if err != nil {
		return failEarly(err)
	}

	if flags.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, flags.Timeout, fmt.Errorf("workflow run --timeout %s elapsed", flags.Timeout))
		defer cancel()
	}

	session, _, err := resolveSendSessionForCommandContext(ctx, flags.Session)
	if err != nil {
		return failEarly(err)
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return failEarly(err)
	}
	agents, err := assignWorkflowPanes(template, panes)
	if err != nil {
		return failEarly(err)
	}
	panePIDs := make(map[string]int, len(agents))
	paneTypes := make(map[string]string, len(agents))
	for _, agent := range agents {
		for _, pane := range panes {
			if pane.ID == agent.ID {
				panePIDs[pane.ID] = pane.PID
				paneTypes[pane.ID] = string(pane.Type)
				break
			}
		}
	}
	if err := validateWorkflowPaneLifetimes(ctx, session, panePIDs); err != nil {
		return failEarly(err)
	}

	projectRoot := strings.TrimSpace(flags.ProjectRoot)
	if projectRoot == "" && endpoint == "local" {
		projectRoot = getSessionWorkingDir(ctx, session, false)
	}
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	resilienceCfg := config.Default().Resilience
	if cfg != nil {
		resilienceCfg = cfg.Resilience
	}
	opts := workflowRunOptions{
		Session:          session,
		ProjectRoot:      projectRoot,
		TmuxEndpoint:     endpoint,
		Vars:             vars,
		PanePIDs:         panePIDs,
		PaneTypes:        paneTypes,
		MaxAgentRestarts: resilienceCfg.MaxRestarts,
		AgentFaultPolls:  resilienceCfg.CrashThreshold,
		MaxTransitions:   flags.MaxTransitions,
		Interval:         flags.Interval,
		FireManual:       flags.FireManual,
		TriggerTimeout:   flags.TriggerTimeout,
		StateDir:         filepath.Join(projectRoot, ".ntm", "workflows", "state"),
		Resume:           flags.Resume,
		Restart:          flags.Restart,
	}

	notify := func(format string, args ...any) {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	runner, err := workflowrun.NewProductionRunner(template, agents, opts, workflowrun.ProductionConfig{
		Config:          cfg,
		DispatchFactory: workflowGatedDispatch,
		Notify:          notify,
	})
	if err != nil {
		return failEarly(err)
	}

	result, runErr := runner.Run(ctx)
	return emit(result, runErr)
}
