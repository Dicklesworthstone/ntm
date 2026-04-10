// Package workflow provides workflow template definitions and coordination for multi-agent patterns.
package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// WorkflowTemplate defines a multi-agent workflow pattern.
type WorkflowTemplate struct {
	Name          string            `toml:"name"`
	Description   string            `toml:"description"`
	Agents        []WorkflowAgent   `toml:"agents"`
	Coordination  CoordinationType  `toml:"coordination"`
	Flow          *FlowConfig       `toml:"flow,omitempty"`
	Routing       map[string]string `toml:"routing,omitempty"`
	Prompts       []SetupPrompt     `toml:"prompts,omitempty"`
	ErrorHandling *ErrorConfig      `toml:"error_handling,omitempty"`
	Source        string            `toml:"-"` // "builtin", "user", "project" - set at load time
}

// WorkflowAgent defines an agent role within a workflow.
type WorkflowAgent struct {
	Profile     string `toml:"profile"`
	Role        string `toml:"role"`
	Count       int    `toml:"count,omitempty"`
	Description string `toml:"description,omitempty"`
}

// CoordinationType specifies how agents coordinate in a workflow.
type CoordinationType string

const (
	// CoordPingPong alternates work between agents.
	CoordPingPong CoordinationType = "ping-pong"
	// CoordPipeline sequences work through stages.
	CoordPipeline CoordinationType = "pipeline"
	// CoordParallel runs agents simultaneously.
	CoordParallel CoordinationType = "parallel"
	// CoordReviewGate requires approval before proceeding.
	CoordReviewGate CoordinationType = "review-gate"
)

// IsValid checks if the coordination type is valid.
func (c CoordinationType) IsValid() bool {
	switch c {
	case CoordPingPong, CoordPipeline, CoordParallel, CoordReviewGate:
		return true
	default:
		return false
	}
}

// FlowConfig defines the state machine for workflow progression.
type FlowConfig struct {
	Initial             string       `toml:"initial"`
	Stages              []string     `toml:"stages,omitempty"`
	Transitions         []Transition `toml:"transitions"`
	RequireApproval     bool         `toml:"require_approval,omitempty"`
	ApprovalMode        string       `toml:"approval_mode,omitempty"` // any, all, quorum
	Quorum              int          `toml:"quorum,omitempty"`
	ParallelWithinStage bool         `toml:"parallel_within_stage,omitempty"`
}

// Transition defines a state change in the workflow.
type Transition struct {
	From    string  `toml:"from"`
	To      string  `toml:"to"`
	Trigger Trigger `toml:"trigger"`
}

// TriggerType specifies the kind of trigger for a transition.
type TriggerType string

const (
	TriggerFileCreated    TriggerType = "file_created"
	TriggerFileModified   TriggerType = "file_modified"
	TriggerCommandSuccess TriggerType = "command_success"
	TriggerCommandFailure TriggerType = "command_failure"
	TriggerAgentSays      TriggerType = "agent_says"
	TriggerAllAgentsIdle  TriggerType = "all_agents_idle"
	TriggerManual         TriggerType = "manual"
	TriggerTimeElapsed    TriggerType = "time_elapsed"
)

// IsValid checks if the trigger type is valid.
func (t TriggerType) IsValid() bool {
	switch t {
	case TriggerFileCreated, TriggerFileModified, TriggerCommandSuccess,
		TriggerCommandFailure, TriggerAgentSays, TriggerAllAgentsIdle,
		TriggerManual, TriggerTimeElapsed:
		return true
	default:
		return false
	}
}

// Trigger defines when a transition should occur.
type Trigger struct {
	Type        TriggerType `toml:"type"`
	Pattern     string      `toml:"pattern,omitempty"`      // For file/agent_says triggers
	Command     string      `toml:"command,omitempty"`      // For command triggers
	Role        string      `toml:"role,omitempty"`         // For agent-specific triggers
	Label       string      `toml:"label,omitempty"`        // For manual triggers
	Minutes     int         `toml:"minutes,omitempty"`      // For time-based triggers
	IdleMinutes int         `toml:"idle_minutes,omitempty"` // For idle triggers
}

// SetupPrompt defines a question to ask when starting a workflow.
type SetupPrompt struct {
	Key        string `toml:"key"`
	Question   string `toml:"question"`
	Default    string `toml:"default,omitempty"`
	Validation string `toml:"validation,omitempty"` // regex pattern
	Required   bool   `toml:"required,omitempty"`
}

// ErrorAction specifies what to do when an error occurs.
type ErrorAction string

const (
	ErrorActionRestartAgent ErrorAction = "restart_agent"
	ErrorActionPause        ErrorAction = "pause"
	ErrorActionSkipStage    ErrorAction = "skip_stage"
	ErrorActionAbort        ErrorAction = "abort"
	ErrorActionNotify       ErrorAction = "notify"
)

// ErrorConfig defines error handling behavior for a workflow.
type ErrorConfig struct {
	OnAgentCrash        ErrorAction `toml:"on_agent_crash,omitempty"`
	OnAgentError        ErrorAction `toml:"on_agent_error,omitempty"`
	OnTimeout           ErrorAction `toml:"on_timeout,omitempty"`
	StageTimeoutMinutes int         `toml:"stage_timeout_minutes,omitempty"`
	MaxRetriesPerStage  int         `toml:"max_retries_per_stage,omitempty"`
}

// namePattern validates workflow template names.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-_]*$`)

// Validate checks that the workflow template is valid.
func (t *WorkflowTemplate) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("workflow name is required")
	}
	if !namePattern.MatchString(t.Name) {
		return fmt.Errorf("workflow name must be lowercase alphanumeric with hyphens or underscores: %q", t.Name)
	}

	if len(t.Agents) == 0 {
		return fmt.Errorf("at least one agent is required")
	}

	for i, agent := range t.Agents {
		if err := agent.Validate(); err != nil {
			return fmt.Errorf("agent[%d]: %w", i, err)
		}
	}

	if !t.Coordination.IsValid() {
		return fmt.Errorf("invalid coordination type: %q", t.Coordination)
	}

	// Non-parallel coordination requires flow config
	if t.Coordination != CoordParallel && t.Flow == nil {
		return fmt.Errorf("flow config required for %q coordination", t.Coordination)
	}

	if t.Flow != nil {
		if err := t.Flow.Validate(t.Coordination); err != nil {
			return fmt.Errorf("flow: %w", err)
		}
	}

	for i, prompt := range t.Prompts {
		if err := prompt.Validate(); err != nil {
			return fmt.Errorf("prompt[%d]: %w", i, err)
		}
	}

	if t.ErrorHandling != nil {
		if err := t.ErrorHandling.Validate(); err != nil {
			return fmt.Errorf("error_handling: %w", err)
		}
	}

	return nil
}

// Validate checks that the workflow agent is valid.
func (a *WorkflowAgent) Validate() error {
	if a.Profile == "" {
		return fmt.Errorf("profile is required")
	}
	if a.Role == "" {
		return fmt.Errorf("role is required")
	}
	if a.Count < 0 {
		return fmt.Errorf("count cannot be negative")
	}
	return nil
}

// Validate checks that the flow config is valid.
func (f *FlowConfig) Validate(coordType CoordinationType) error {
	// Pipeline coordination requires stages
	if coordType == CoordPipeline {
		if len(f.Stages) == 0 {
			return fmt.Errorf("stages required for pipeline coordination")
		}
	} else {
		// Non-pipeline requires initial state
		if f.Initial == "" {
			return fmt.Errorf("initial state is required")
		}
	}

	if len(f.Transitions) == 0 {
		return fmt.Errorf("at least one transition is required")
	}

	for i, trans := range f.Transitions {
		if err := trans.Validate(); err != nil {
			return fmt.Errorf("transition[%d]: %w", i, err)
		}
	}

	// Validate approval mode if set
	if f.RequireApproval {
		switch f.ApprovalMode {
		case "", "any", "all", "quorum":
			// valid
		default:
			return fmt.Errorf("invalid approval_mode: %q (must be any, all, or quorum)", f.ApprovalMode)
		}
		if f.ApprovalMode == "quorum" && f.Quorum < 1 {
			return fmt.Errorf("quorum must be at least 1 when approval_mode is quorum")
		}
	}

	return nil
}

// Validate checks that the transition is valid.
func (t *Transition) Validate() error {
	if t.From == "" {
		return fmt.Errorf("from state is required")
	}
	if t.To == "" {
		return fmt.Errorf("to state is required")
	}
	return t.Trigger.Validate()
}

// Validate checks that the trigger is valid.
func (t *Trigger) Validate() error {
	if !t.Type.IsValid() {
		return fmt.Errorf("invalid trigger type: %q", t.Type)
	}

	switch t.Type {
	case TriggerFileCreated, TriggerFileModified:
		if t.Pattern == "" {
			return fmt.Errorf("%s trigger requires pattern", t.Type)
		}
	case TriggerCommandSuccess, TriggerCommandFailure:
		if t.Command == "" {
			return fmt.Errorf("%s trigger requires command", t.Type)
		}
	case TriggerAgentSays:
		if t.Pattern == "" {
			return fmt.Errorf("agent_says trigger requires pattern")
		}
	case TriggerAllAgentsIdle:
		if t.IdleMinutes < 1 {
			return fmt.Errorf("all_agents_idle trigger requires idle_minutes >= 1")
		}
	case TriggerManual:
		// Label is optional
	case TriggerTimeElapsed:
		if t.Minutes < 1 {
			return fmt.Errorf("time_elapsed trigger requires minutes >= 1")
		}
	}

	return nil
}

// Validate checks that the setup prompt is valid.
func (p *SetupPrompt) Validate() error {
	if p.Key == "" {
		return fmt.Errorf("key is required")
	}
	if p.Question == "" {
		return fmt.Errorf("question is required")
	}
	if p.Validation != "" {
		if _, err := regexp.Compile(p.Validation); err != nil {
			return fmt.Errorf("invalid validation regex: %w", err)
		}
	}
	return nil
}

// Validate checks that the error config is valid.
func (e *ErrorConfig) Validate() error {
	actions := []ErrorAction{e.OnAgentCrash, e.OnAgentError, e.OnTimeout}
	for _, action := range actions {
		if action != "" && !isValidErrorAction(action) {
			return fmt.Errorf("invalid error action: %q", action)
		}
	}
	if e.StageTimeoutMinutes < 0 {
		return fmt.Errorf("stage_timeout_minutes cannot be negative")
	}
	if e.MaxRetriesPerStage < 0 {
		return fmt.Errorf("max_retries_per_stage cannot be negative")
	}
	return nil
}

func isValidErrorAction(action ErrorAction) bool {
	switch action {
	case ErrorActionRestartAgent, ErrorActionPause, ErrorActionSkipStage,
		ErrorActionAbort, ErrorActionNotify:
		return true
	default:
		return false
	}
}

// GetAgentCount returns the total number of agent instances in the workflow.
func (t *WorkflowTemplate) GetAgentCount() int {
	count := 0
	for _, agent := range t.Agents {
		if agent.Count > 0 {
			count += agent.Count
		} else {
			count++ // Default to 1
		}
	}
	return count
}

// GetRoles returns a list of all unique roles in the workflow.
func (t *WorkflowTemplate) GetRoles() []string {
	seen := make(map[string]bool)
	var roles []string
	for _, agent := range t.Agents {
		if !seen[agent.Role] {
			seen[agent.Role] = true
			roles = append(roles, agent.Role)
		}
	}
	return roles
}

// GetAgentsByRole returns all agents with the specified role.
func (t *WorkflowTemplate) GetAgentsByRole(role string) []WorkflowAgent {
	var agents []WorkflowAgent
	for _, agent := range t.Agents {
		if agent.Role == role {
			agents = append(agents, agent)
		}
	}
	return agents
}

// WorkflowsFile represents a TOML file containing workflow templates.
type WorkflowsFile struct {
	Workflows []WorkflowTemplate `toml:"workflows"`
}

func undecodedWorkflowFields(md toml.MetaData) []string {
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, key.String())
	}
	sort.Strings(fields)
	return fields
}

// ParseWorkflows parses workflow templates from TOML content.
func ParseWorkflows(content string) ([]WorkflowTemplate, error) {
	var file WorkflowsFile
	md, err := toml.Decode(content, &file)
	if err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	if fields := undecodedWorkflowFields(md); len(fields) > 0 {
		return nil, fmt.Errorf("unknown field(s): %s", strings.Join(fields, ", "))
	}
	return file.Workflows, nil
}

// ParseWorkflow parses a single workflow template from TOML content.
func ParseWorkflow(content string) (*WorkflowTemplate, error) {
	// Try array format first
	var file WorkflowsFile
	md, err := toml.Decode(content, &file)
	if err == nil && len(file.Workflows) > 0 && len(undecodedWorkflowFields(md)) == 0 {
		return &file.Workflows[0], nil
	}

	// Try single workflow format
	var tmpl WorkflowTemplate
	md, err = toml.Decode(content, &tmpl)
	if err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	if fields := undecodedWorkflowFields(md); len(fields) > 0 {
		return nil, fmt.Errorf("unknown field(s): %s", strings.Join(fields, ", "))
	}
	return &tmpl, nil
}

// ParseAndValidateWorkflow parses and validates a workflow template.
func ParseAndValidateWorkflow(content string) (*WorkflowTemplate, error) {
	tmpl, err := ParseWorkflow(content)
	if err != nil {
		return nil, err
	}
	if err := tmpl.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return tmpl, nil
}

// String returns a human-readable summary of the workflow.
func (t *WorkflowTemplate) String() string {
	var b strings.Builder
	b.WriteString(t.Name)
	if t.Description != "" {
		b.WriteString(": ")
		b.WriteString(t.Description)
	}
	fmt.Fprintf(&b, " (%d agents, %s)", t.GetAgentCount(), t.Coordination)
	return b.String()
}
