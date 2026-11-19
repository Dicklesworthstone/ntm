package robot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

const (
	workflowsSchemaID      = "ntm:robot:workflows:v1"
	workflowsSchemaVersion = "1.0.0"

	WorkflowsSchemaID      = workflowsSchemaID
	WorkflowsSchemaVersion = workflowsSchemaVersion

	ErrCodeWorkflowNotFound = "WORKFLOW_NOT_FOUND"
)

// WorkflowsListResult is the CLI-compatible list payload for workflow templates.
type WorkflowsListResult struct {
	Workflows []WorkflowInfo `json:"workflows"`
	Total     int            `json:"total"`
}

// WorkflowInfo summarizes a workflow template.
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

// WorkflowShowResult contains detailed inspection and trigger validation for a workflow template.
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

// WorkflowsOptions configures GetWorkflows.
type WorkflowsOptions struct {
	ProjectDir string
	Name       string
	Role       string
}

// WorkflowsOutput is the canonical robot envelope for --robot-workflows and /api/v1/workflows*.
type WorkflowsOutput struct {
	RobotResponse
	SchemaID      string              `json:"schema_id"`
	SchemaVersion string              `json:"schema_version"`
	Action        string              `json:"action"`
	ProjectDir    string              `json:"project_dir,omitempty"`
	FilterRole    string              `json:"filter_role,omitempty"`
	Workflows     []WorkflowInfo      `json:"workflows,omitempty"`
	Total         *int                `json:"total,omitempty"`
	Workflow      *WorkflowShowResult `json:"workflow,omitempty"`
	AgentHints    *AgentHints         `json:"_agent_hints,omitempty"`
}

// InspectWorkflowTransitions validates every declared flow transition against workflow.NewTriggerRegistry.
func InspectWorkflowTransitions(flow *workflow.FlowConfig) []WorkflowTransitionDiagnostic {
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

// GetWorkflows lists workflow templates (with optional role filter) or inspects one template by name.
func GetWorkflows(opts WorkflowsOptions) (*WorkflowsOutput, error) {
	loader := workflow.NewLoader()
	if trimmedProj := strings.TrimSpace(opts.ProjectDir); strings.Compare(trimmedProj, "") != 0 {
		loader.ProjectDir = trimmedProj
	}
	roleFilter := strings.TrimSpace(opts.Role)
	targetName := strings.TrimSpace(opts.Name)

	if strings.Compare(targetName, "") != 0 {
		w, err := loader.Get(targetName)
		if err != nil {
			code := ErrCodeInternalError
			if strings.Contains(err.Error(), "not found") {
				code = ErrCodeWorkflowNotFound
			}
			return &WorkflowsOutput{
				RobotResponse: NewErrorResponse(err, code, "Run ntm --robot-workflows to list available workflow templates"),
				SchemaID:      workflowsSchemaID,
				SchemaVersion: workflowsSchemaVersion,
				Action:        "show",
				ProjectDir:    loader.ProjectDir,
				FilterRole:    roleFilter,
			}, nil
		}
		roles := w.GetRoles()
		roleAgents := make(map[string][]workflow.WorkflowAgent, len(roles))
		for _, r := range roles {
			roleAgents[r] = w.GetAgentsByRole(r)
		}
		displayAgents := w.Agents
		if strings.Compare(roleFilter, "") != 0 {
			displayAgents = w.GetAgentsByRole(roleFilter)
		}
		if displayAgents == nil {
			displayAgents = []workflow.WorkflowAgent{}
		}
		triggers := InspectWorkflowTransitions(w.Flow)
		detail := &WorkflowShowResult{
			Name:              w.Name,
			Description:       w.Description,
			Source:            w.Source,
			Coordination:      string(w.Coordination),
			AgentCount:        w.GetAgentCount(),
			Roles:             roles,
			RoleAgents:        roleAgents,
			Agents:            displayAgents,
			Flow:              w.Flow,
			ValidatedTriggers: triggers,
			Prompts:           w.Prompts,
			ErrorHandling:     w.ErrorHandling,
		}
		return &WorkflowsOutput{
			RobotResponse: NewRobotResponse(true),
			SchemaID:      workflowsSchemaID,
			SchemaVersion: workflowsSchemaVersion,
			Action:        "show",
			ProjectDir:    loader.ProjectDir,
			FilterRole:    roleFilter,
			Workflow:      detail,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("Loaded workflow %q (%s coordination, %d agent(s), %d validated transition(s)).", w.Name, w.Coordination, w.GetAgentCount(), len(triggers)),
			},
		}, nil
	}

	items, err := loader.LoadAll()
	if err != nil {
		return &WorkflowsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Inspect workflow TOML files in ~/.config/ntm/workflows or .ntm/workflows"),
			SchemaID:      workflowsSchemaID,
			SchemaVersion: workflowsSchemaVersion,
			Action:        "list",
			ProjectDir:    loader.ProjectDir,
			FilterRole:    roleFilter,
			Workflows:     []WorkflowInfo{},
		}, nil
	}
	if strings.Compare(roleFilter, "") != 0 {
		filtered := make([]workflow.WorkflowTemplate, 0, len(items))
		for _, w := range items {
			if len(w.GetAgentsByRole(roleFilter)) > 0 {
				filtered = append(filtered, w)
			}
		}
		items = filtered
	}
	summaries := make([]WorkflowInfo, len(items))
	for i, w := range items {
		summaries[i] = WorkflowInfo{
			Name:         w.Name,
			Description:  w.Description,
			Source:       w.Source,
			Coordination: string(w.Coordination),
			AgentCount:   w.GetAgentCount(),
			Roles:        w.GetRoles(),
		}
	}
	total := len(summaries)
	return &WorkflowsOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      workflowsSchemaID,
		SchemaVersion: workflowsSchemaVersion,
		Action:        "list",
		ProjectDir:    loader.ProjectDir,
		FilterRole:    roleFilter,
		Workflows:     summaries,
		Total:         &total,
		AgentHints: &AgentHints{
			Summary: fmt.Sprintf("Loaded %d workflow template(s).", total),
		},
	}, nil
}

// PrintWorkflows writes the terminal JSON/TOON response for --robot-workflows.
func PrintWorkflows(opts WorkflowsOptions) error {
	out, err := GetWorkflows(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot workflows failed")
}

// SequenceOptions configures ExecuteSequence.
type SequenceOptions struct {
	ProjectDir       string
	Name             string
	Action           string // "create", "next" (default), "advance"
	Pane             string
	Steps            []string
	RawStepsJSON     string
	ExpectedPosition *int
}

// SequenceOutput is the canonical durable per-pane prompt-sequence response.
type SequenceOutput struct {
	RobotResponse
	Action   string                         `json:"action"`
	Sequence *workflow.PaneSequence         `json:"sequence,omitempty"`
	Pane     *workflow.PaneSequencePosition `json:"pane,omitempty"`
}

// ExecuteSequence creates, inspects, or retry-safely advances a durable per-pane prompt sequence.
func ExecuteSequence(opts SequenceOptions) (*SequenceOutput, error) {
	store, err := workflow.NewPaneSequenceStore(strings.TrimSpace(opts.ProjectDir))
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if strings.Compare(action, "") == 0 {
		action = "next"
	}

	var expectedPos []int
	if opts.ExpectedPosition != nil {
		if *opts.ExpectedPosition < 0 {
			return nil, fmt.Errorf("--sequence-expected-position must be non-negative")
		}
		if !strings.EqualFold(action, "advance") {
			return nil, fmt.Errorf("--sequence-expected-position requires --sequence-action=advance")
		}
		expectedPos = []int{*opts.ExpectedPosition}
	}

	out := &SequenceOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        action,
	}
	switch action {
	case "create":
		steps := opts.Steps
		if steps == nil {
			if err := json.Unmarshal([]byte(opts.RawStepsJSON), &steps); err != nil {
				return nil, fmt.Errorf("decode --sequence-steps as a JSON string array: %w", err)
			}
		}
		seq, err := store.Create(opts.Name, steps)
		if err != nil {
			return nil, err
		}
		out.Sequence = seq
	case "next":
		pos, err := store.Next(opts.Name, opts.Pane)
		if err != nil {
			return nil, err
		}
		out.Pane = &pos
	case "advance":
		pos, err := store.Advance(opts.Name, opts.Pane, expectedPos...)
		if err != nil {
			return nil, err
		}
		out.Pane = &pos
	default:
		return nil, fmt.Errorf("invalid sequence action %q: use create, next, or advance", action)
	}
	return out, nil
}

func workflowsCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "workflows",
		Flag:        "--robot-workflows",
		Category:    "pipeline",
		Summary:     "List or inspect workflow templates.",
		Description: "List built-in, user, and project workflow coordination templates (ping-pong, pipeline, parallel, review-gate) or inspect one template with trigger validation.",
		Parameters: []RobotParameter{
			{Name: "workflows-name", Flag: "--workflows-name", Type: "string", Required: false, Description: "Workflow template name to inspect in detail (omit to list all)"},
			{Name: "workflows-role", Flag: "--workflows-role", Type: "string", Required: false, Description: "Filter workflows or displayed agents by declared role"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory for .ntm/workflows overrides"},
		},
		Examples: []string{
			"ntm --robot-workflows",
			"ntm --robot-workflows --workflows-role=reviewer",
			"ntm --robot-workflows --workflows-name=red-green",
		},
	}
}
