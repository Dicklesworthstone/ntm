package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// WorkflowsListResult is the JSON output for workflows list command.
type WorkflowsListResult struct {
	Workflows []WorkflowInfo `json:"workflows"`
	Total     int            `json:"total"`
}

// WorkflowInfo is a workflow summary for JSON output.
type WorkflowInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`
	Coordination string   `json:"coordination"`
	AgentCount   int      `json:"agent_count"`
	Roles        []string `json:"roles,omitempty"`
}

// WorkflowTransitionDiagnostic reports runtime trigger validation for a flow transition.
type WorkflowTransitionDiagnostic struct {
	From        string `json:"from"`
	To          string `json:"to"`
	RuntimeType string `json:"runtime_type"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
}

// WorkflowShowResult is the JSON output for workflows show command.
type WorkflowShowResult struct {
	Name              string                              `json:"name"`
	Description       string                              `json:"description"`
	Source            string                              `json:"source"`
	Coordination      string                              `json:"coordination"`
	AgentCount        int                                 `json:"agent_count"`
	Roles             []string                            `json:"roles,omitempty"`
	RoleAgents        map[string][]workflow.WorkflowAgent `json:"role_agents,omitempty"`
	Agents            []workflow.WorkflowAgent            `json:"agents"`
	Flow              *workflow.FlowConfig                `json:"flow,omitempty"`
	ValidatedTriggers []WorkflowTransitionDiagnostic      `json:"validated_triggers,omitempty"`
	Prompts           []workflow.SetupPrompt              `json:"prompts,omitempty"`
	ErrorHandling     *workflow.ErrorConfig               `json:"error_handling,omitempty"`
}

// RobotSequenceOutput is the durable per-pane prompt-sequence response. A
// create action returns Sequence; next and advance return Pane with the prompt
// currently assigned to that pane.
type RobotSequenceOutput struct {
	robot.RobotResponse
	Action   string                         `json:"action"`
	Sequence *workflow.PaneSequence         `json:"sequence,omitempty"`
	Pane     *workflow.PaneSequencePosition `json:"pane,omitempty"`
}

// robotSequenceExpectedPositionFlag is the root --sequence-expected-position
// flag. It is looked up once in init rather than through rootCmd inside
// printRobotSequence: rootCmd's initializer reaches printRobotSequence via its
// Run closure, so a direct rootCmd reference there is an initialization cycle
// that fails the whole package build.
var robotSequenceExpectedPositionFlag *pflag.Flag

func init() {
	rootCmd.Flags().Int("sequence-expected-position", 0,
		"Zero-based position being completed; makes retries safe without skipping prompts. Optional with --robot-sequence and --sequence-action=advance")
	robotSequenceExpectedPositionFlag = rootCmd.Flags().Lookup("sequence-expected-position")
	robot.MustRegisterSchemaCommand("sequence", RobotSequenceOutput{})
	robot.MustRegisterSchemaPagination("sequence", robot.SchemaPaginationFlag{
		Reason: "bounded: one pane sequence echo with its nested step list",
	})
}

func printRobotSequence(projectDir, name, action, pane, rawSteps string) error {
	store, err := workflow.NewPaneSequenceStore(projectDir)
	if err != nil {
		return err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "next"
	}
	var expectedPosition []int
	if robotSequenceExpectedPositionFlag != nil && robotSequenceExpectedPositionFlag.Changed {
		expected, err := strconv.Atoi(robotSequenceExpectedPositionFlag.Value.String())
		if err != nil {
			return fmt.Errorf("--sequence-expected-position: %w", err)
		}
		if expected < 0 {
			return fmt.Errorf("--sequence-expected-position must be non-negative")
		}
		if action != "advance" {
			return fmt.Errorf("--sequence-expected-position requires --sequence-action=advance")
		}
		expectedPosition = []int{expected}
	}
	output := RobotSequenceOutput{
		RobotResponse: robot.NewRobotResponse(true),
		Action:        action,
	}
	switch action {
	case "create":
		var steps []string
		if err := json.Unmarshal([]byte(rawSteps), &steps); err != nil {
			return fmt.Errorf("decode --sequence-steps as a JSON string array: %w", err)
		}
		sequence, err := store.Create(name, steps)
		if err != nil {
			return err
		}
		output.Sequence = sequence
	case "next":
		position, err := store.Next(name, pane)
		if err != nil {
			return err
		}
		output.Pane = &position
	case "advance":
		position, err := store.Advance(name, pane, expectedPosition...)
		if err != nil {
			return err
		}
		output.Pane = &position
	default:
		return fmt.Errorf("invalid sequence action %q: use create, next, or advance", action)
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}

func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflows",
		Aliases: []string{"workflow", "wf"},
		Short:   "Manage workflow templates (orchestration patterns)",
		Long: `List and view workflow templates (orchestration patterns).

Workflow templates define multi-agent coordination patterns like:
  - ping-pong: Alternating work between agents (e.g., TDD red-green)
  - pipeline: Sequential stages with handoff (e.g., design -> build -> qa)
  - parallel: Simultaneous independent work
  - review-gate: Work with approval gates

Sources (in precedence order):
  1. Built-in templates (lowest priority)
  2. User templates (~/.config/ntm/workflows/)
  3. Project templates (.ntm/workflows/) (highest priority)

For durable per-pane review progress, use --robot-sequence. Complete a prompt
with --sequence-action=advance --sequence-expected-position=N, where N is the
position returned by the preceding query. Repeating that request is a no-op
once N has been consumed, even after restart; future positions are rejected.

Examples:
  ntm workflows list                # List all available templates
  ntm workflows show red-green      # Show details of a template
  ntm workflow run red-green        # Execute a template's coordination loop
  ntm workflows list --json         # JSON output for scripts`,
	}

	cmd.AddCommand(newWorkflowsListCmd())
	cmd.AddCommand(newWorkflowsShowCmd())
	cmd.AddCommand(newWorkflowsRunCmd())
	cmd.AddCommand(newWorkflowCheckpointCmd("status"))
	cmd.AddCommand(newWorkflowCheckpointCmd("recover"))
	cmd.AddCommand(newWorkflowTriggerCmd())

	return cmd
}

func newWorkflowsListCmd() *cobra.Command {
	var roleFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available workflow templates",
		Long: `List all available workflow templates from all sources.

Templates are shown with name, description, coordination type, roles, and agent count.

Examples:
  ntm workflows list                  # Human-readable table
  ntm workflows list --role=reviewer  # Filter workflows declaring a role
  ntm workflows list --json           # JSON output for scripts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowsListFiltered(roleFilter)
		},
	}

	cmd.Flags().StringVar(&roleFilter, "role", "", "Filter workflow templates by agent role")

	return cmd
}

func runWorkflowsListFiltered(roleFilter string) error {
	loader := workflow.NewLoader()
	workflows, err := loader.LoadAll()
	if err != nil {
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}, err)
		}
		return err
	}

	roleFilter = strings.TrimSpace(roleFilter)
	if roleFilter != "" {
		filtered := make([]workflow.WorkflowTemplate, 0, len(workflows))
		for _, w := range workflows {
			if len(w.GetAgentsByRole(roleFilter)) > 0 {
				filtered = append(filtered, w)
			}
		}
		workflows = filtered
	}

	if jsonOutput {
		result := WorkflowsListResult{
			Workflows: make([]WorkflowInfo, len(workflows)),
			Total:     len(workflows),
		}
		for i, w := range workflows {
			result.Workflows[i] = WorkflowInfo{
				Name:         w.Name,
				Description:  w.Description,
				Source:       w.Source,
				Coordination: string(w.Coordination),
				AgentCount:   w.GetAgentCount(),
				Roles:        w.GetRoles(),
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	if len(workflows) == 0 {
		fmt.Println("No workflow templates found.")
		return nil
	}

	t := theme.Current()
	fmt.Printf("%sAvailable Workflow Templates%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 70), "\033[0m")

	// Group by source
	bySource := make(map[string][]workflow.WorkflowTemplate)
	for _, w := range workflows {
		bySource[w.Source] = append(bySource[w.Source], w)
	}

	// Print in order: builtin, user, project
	sources := []string{"builtin", "user", "project"}
	sourceLabels := map[string]string{
		"builtin": "Built-in",
		"user":    "User (~/.config/ntm/workflows/)",
		"project": "Project (.ntm/workflows/)",
	}

	for _, source := range sources {
		group := bySource[source]
		if len(group) == 0 {
			continue
		}

		fmt.Printf("  %s%s:%s\n", colorize(t.Info), sourceLabels[source], "\033[0m")
		for _, w := range group {
			coordIcon := coordinationIcon(w.Coordination)
			roles := w.GetRoles()
			fmt.Printf("    %s%-20s%s %s  %s\n",
				colorize(t.Primary), w.Name, "\033[0m",
				coordIcon, w.Description)
			if len(roles) > 0 {
				fmt.Printf("    %s                     [%d agents, %s, roles: %s]%s\n",
					"\033[2m", w.GetAgentCount(), w.Coordination, strings.Join(roles, ", "), "\033[0m")
			} else {
				fmt.Printf("    %s                     [%d agents, %s]%s\n",
					"\033[2m", w.GetAgentCount(), w.Coordination, "\033[0m")
			}
		}
		fmt.Println()
	}

	fmt.Printf("%sTotal: %d template(s)%s\n", "\033[2m", len(workflows), "\033[0m")

	return nil
}

func coordinationIcon(coord workflow.CoordinationType) string {
	switch coord {
	case workflow.CoordPingPong:
		return "\u21c4" // ⇄ bidirectional arrows
	case workflow.CoordPipeline:
		return "\u2192" // → right arrow
	case workflow.CoordParallel:
		return "\u2261" // ≡ parallel lines
	case workflow.CoordReviewGate:
		return "\u2713" // ✓ checkmark
	default:
		return "\u2022" // • bullet
	}
}

func newWorkflowsShowCmd() *cobra.Command {
	var roleFilter string

	cmd := &cobra.Command{
		Use:   "show <template-name>",
		Short: "Show details of a workflow template",
		Long: `Show detailed information about a specific workflow template.

Examples:
  ntm workflows show red-green              # Show red-green TDD template
  ntm workflows show red-green --role=red   # Filter agents to a single role
  ntm workflows show review-pipeline --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowsShowFiltered(args[0], roleFilter)
		},
	}

	cmd.Flags().StringVar(&roleFilter, "role", "", "Filter displayed agents by role")

	return cmd
}

func inspectWorkflowTransitions(flow *workflow.FlowConfig) []WorkflowTransitionDiagnostic {
	if flow == nil || len(flow.Transitions) == 0 {
		return nil
	}
	reg := workflow.NewTriggerRegistry()
	diagnostics := make([]WorkflowTransitionDiagnostic, 0, len(flow.Transitions))
	for _, tr := range flow.Transitions {
		diag := WorkflowTransitionDiagnostic{
			From:        tr.From,
			To:          tr.To,
			RuntimeType: string(tr.Trigger.Type),
		}
		rt, err := reg.Create(tr.Trigger)
		if err != nil {
			diag.Valid = false
			diag.Error = err.Error()
		} else {
			diag.Valid = true
			diag.RuntimeType = string(rt.Type())
			if manual, ok := rt.(*workflow.ManualTrigger); ok {
				diag.RuntimeType = string(manual.Type())
			}
			_ = rt.Stop()
		}
		diagnostics = append(diagnostics, diag)
	}
	return diagnostics
}

func runWorkflowsShowFiltered(name, roleFilter string) error {
	loader := workflow.NewLoader()
	w, err := loader.Get(name)
	if err != nil {
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}, err)
		}
		return err
	}

	roles := w.GetRoles()
	roleAgents := make(map[string][]workflow.WorkflowAgent, len(roles))
	for _, r := range roles {
		roleAgents[r] = w.GetAgentsByRole(r)
	}
	displayAgents := w.Agents
	roleFilter = strings.TrimSpace(roleFilter)
	if roleFilter != "" {
		displayAgents = w.GetAgentsByRole(roleFilter)
	}
	triggerDiagnostics := inspectWorkflowTransitions(w.Flow)

	if jsonOutput {
		result := WorkflowShowResult{
			Name:              w.Name,
			Description:       w.Description,
			Source:            w.Source,
			Coordination:      string(w.Coordination),
			AgentCount:        w.GetAgentCount(),
			Roles:             roles,
			RoleAgents:        roleAgents,
			Agents:            displayAgents,
			Flow:              w.Flow,
			ValidatedTriggers: triggerDiagnostics,
			Prompts:           w.Prompts,
			ErrorHandling:     w.ErrorHandling,
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	t := theme.Current()
	fmt.Printf("%sWorkflow Template: %s%s\n", "\033[1m", w.Name, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2500", 60), "\033[0m")

	fmt.Printf("  Description:  %s\n", w.Description)
	fmt.Printf("  Source:       %s\n", workflow.SourceDescription(w.Source))
	fmt.Printf("  Coordination: %s\n", w.Coordination)
	if len(roles) > 0 {
		fmt.Printf("  Roles:        %s\n", strings.Join(roles, ", "))
	}
	fmt.Printf("  Total Agents: %d\n\n", w.GetAgentCount())

	// Agents section
	fmt.Printf("  %sAgents:%s\n", "\033[1m", "\033[0m")
	for _, a := range displayAgents {
		count := a.Count
		if count == 0 {
			count = 1
		}
		line := fmt.Sprintf("    %s%s%s (role: %s) x %d",
			colorize(t.Primary), a.Profile, "\033[0m", a.Role, count)
		if a.Description != "" {
			line += fmt.Sprintf("\n      %s%s%s", "\033[2m", a.Description, "\033[0m")
		}
		fmt.Println(line)
	}
	fmt.Println()

	// Flow section
	if w.Flow != nil {
		fmt.Printf("  %sFlow:%s\n", "\033[1m", "\033[0m")
		if w.Flow.Initial != "" {
			fmt.Printf("    Initial state: %s\n", w.Flow.Initial)
		}
		if len(w.Flow.Stages) > 0 {
			fmt.Printf("    Stages: %s\n", strings.Join(w.Flow.Stages, " → "))
		}
		if w.Flow.RequireApproval {
			fmt.Printf("    Requires approval: %s mode\n", w.Flow.ApprovalMode)
		}
		if w.Flow.ParallelWithinStage {
			fmt.Printf("    Parallel within stage: yes\n")
		}

		if len(w.Flow.Transitions) > 0 {
			fmt.Printf("    Transitions:\n")
			for i, tr := range w.Flow.Transitions {
				triggerDesc := formatTrigger(tr.Trigger)
				if i < len(triggerDiagnostics) && !triggerDiagnostics[i].Valid {
					triggerDesc += fmt.Sprintf(" [invalid: %s]", triggerDiagnostics[i].Error)
				}
				fmt.Printf("      %s → %s [%s]\n", tr.From, tr.To, triggerDesc)
			}
		}
		fmt.Println()
	}

	// Prompts section
	if len(w.Prompts) > 0 {
		fmt.Printf("  %sSetup Prompts:%s\n", "\033[1m", "\033[0m")
		for _, p := range w.Prompts {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Printf("    %s: %s%s\n", p.Key, p.Question, req)
			if p.Default != "" {
				fmt.Printf("      %sDefault: %s%s\n", "\033[2m", p.Default, "\033[0m")
			}
		}
		fmt.Println()
	}

	// Error handling section
	if w.ErrorHandling != nil {
		fmt.Printf("  %sError Handling:%s\n", "\033[1m", "\033[0m")
		if w.ErrorHandling.OnAgentCrash != "" {
			fmt.Printf("    On crash: %s\n", w.ErrorHandling.OnAgentCrash)
		}
		if w.ErrorHandling.OnAgentError != "" {
			fmt.Printf("    On error: %s\n", w.ErrorHandling.OnAgentError)
		}
		if w.ErrorHandling.OnTimeout != "" {
			fmt.Printf("    On timeout: %s\n", w.ErrorHandling.OnTimeout)
		}
		if w.ErrorHandling.StageTimeoutMinutes > 0 {
			fmt.Printf("    Stage timeout: %dm\n", w.ErrorHandling.StageTimeoutMinutes)
		}
		if w.ErrorHandling.MaxRetriesPerStage > 0 {
			fmt.Printf("    Max retries: %d\n", w.ErrorHandling.MaxRetriesPerStage)
		}
		fmt.Println()
	}

	return nil
}

func formatTrigger(tr workflow.Trigger) string {
	switch tr.Type {
	case workflow.TriggerFileCreated:
		return fmt.Sprintf("file_created: %s", tr.Pattern)
	case workflow.TriggerFileModified:
		return fmt.Sprintf("file_modified: %s", tr.Pattern)
	case workflow.TriggerCommandSuccess:
		return fmt.Sprintf("command_success: %s", tr.Command)
	case workflow.TriggerCommandFailure:
		return fmt.Sprintf("command_failure: %s", tr.Command)
	case workflow.TriggerAgentSays:
		desc := fmt.Sprintf("agent_says: %q", tr.Pattern)
		if tr.Role != "" {
			desc += fmt.Sprintf(" (role: %s)", tr.Role)
		}
		return desc
	case workflow.TriggerAllAgentsIdle:
		return fmt.Sprintf("all_idle: %dm", tr.IdleMinutes)
	case workflow.TriggerManual:
		if tr.Label != "" {
			return fmt.Sprintf("manual: %s", tr.Label)
		}
		return "manual"
	case workflow.TriggerTimeElapsed:
		return fmt.Sprintf("time: %dm", tr.Minutes)
	default:
		return string(tr.Type)
	}
}
