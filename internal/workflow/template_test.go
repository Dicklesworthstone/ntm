package workflow

import (
	"strings"
	"testing"
)

func TestCoordinationType_IsValid(t *testing.T) {
	tests := []struct {
		coord CoordinationType
		valid bool
	}{
		{CoordPingPong, true},
		{CoordPipeline, true},
		{CoordParallel, true},
		{CoordReviewGate, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.coord), func(t *testing.T) {
			if got := tt.coord.IsValid(); got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestTriggerType_IsValid(t *testing.T) {
	tests := []struct {
		trigger TriggerType
		valid   bool
	}{
		{TriggerFileCreated, true},
		{TriggerFileModified, true},
		{TriggerCommandSuccess, true},
		{TriggerCommandFailure, true},
		{TriggerAgentSays, true},
		{TriggerAllAgentsIdle, true},
		{TriggerManual, true},
		{TriggerTimeElapsed, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.trigger), func(t *testing.T) {
			if got := tt.trigger.IsValid(); got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestWorkflowAgent_Validate(t *testing.T) {
	tests := []struct {
		name    string
		agent   WorkflowAgent
		wantErr bool
	}{
		{
			name:    "valid agent",
			agent:   WorkflowAgent{Profile: "tester", Role: "red"},
			wantErr: false,
		},
		{
			name:    "valid agent with count",
			agent:   WorkflowAgent{Profile: "implementer", Role: "green", Count: 2},
			wantErr: false,
		},
		{
			name:    "missing profile",
			agent:   WorkflowAgent{Role: "red"},
			wantErr: true,
		},
		{
			name:    "missing role",
			agent:   WorkflowAgent{Profile: "tester"},
			wantErr: true,
		},
		{
			name:    "negative count",
			agent:   WorkflowAgent{Profile: "tester", Role: "red", Count: -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.agent.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTrigger_Validate(t *testing.T) {
	tests := []struct {
		name    string
		trigger Trigger
		wantErr bool
	}{
		{
			name:    "valid file_created",
			trigger: Trigger{Type: TriggerFileCreated, Pattern: "*_test.go"},
			wantErr: false,
		},
		{
			name:    "file_created missing pattern",
			trigger: Trigger{Type: TriggerFileCreated},
			wantErr: true,
		},
		{
			name:    "valid command_success",
			trigger: Trigger{Type: TriggerCommandSuccess, Command: "go test ./..."},
			wantErr: false,
		},
		{
			name:    "command_success missing command",
			trigger: Trigger{Type: TriggerCommandSuccess},
			wantErr: true,
		},
		{
			name:    "valid agent_says",
			trigger: Trigger{Type: TriggerAgentSays, Pattern: "test.*written", Role: "red"},
			wantErr: false,
		},
		{
			name:    "agent_says missing pattern",
			trigger: Trigger{Type: TriggerAgentSays, Role: "red"},
			wantErr: true,
		},
		{
			name:    "valid all_agents_idle",
			trigger: Trigger{Type: TriggerAllAgentsIdle, IdleMinutes: 5},
			wantErr: false,
		},
		{
			name:    "all_agents_idle zero minutes",
			trigger: Trigger{Type: TriggerAllAgentsIdle, IdleMinutes: 0},
			wantErr: true,
		},
		{
			name:    "valid manual",
			trigger: Trigger{Type: TriggerManual, Label: "Approve"},
			wantErr: false,
		},
		{
			name:    "manual without label is ok",
			trigger: Trigger{Type: TriggerManual},
			wantErr: false,
		},
		{
			name:    "valid time_elapsed",
			trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 30},
			wantErr: false,
		},
		{
			name:    "time_elapsed zero minutes",
			trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 0},
			wantErr: true,
		},
		{
			name:    "invalid trigger type",
			trigger: Trigger{Type: "invalid"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.trigger.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSetupPrompt_Validate(t *testing.T) {
	tests := []struct {
		name    string
		prompt  SetupPrompt
		wantErr bool
	}{
		{
			name:    "valid prompt",
			prompt:  SetupPrompt{Key: "feature", Question: "What feature?"},
			wantErr: false,
		},
		{
			name:    "valid with default",
			prompt:  SetupPrompt{Key: "pattern", Question: "File pattern?", Default: "*.go"},
			wantErr: false,
		},
		{
			name:    "valid with validation",
			prompt:  SetupPrompt{Key: "name", Question: "Name?", Validation: `^[a-z]+$`},
			wantErr: false,
		},
		{
			name:    "missing key",
			prompt:  SetupPrompt{Question: "What feature?"},
			wantErr: true,
		},
		{
			name:    "missing question",
			prompt:  SetupPrompt{Key: "feature"},
			wantErr: true,
		},
		{
			name:    "invalid validation regex",
			prompt:  SetupPrompt{Key: "name", Question: "Name?", Validation: "[invalid"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.prompt.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWorkflowTemplate_Validate(t *testing.T) {
	validAgent := WorkflowAgent{Profile: "tester", Role: "red"}
	validTransition := Transition{
		From:    "red",
		To:      "green",
		Trigger: Trigger{Type: TriggerManual},
	}

	tests := []struct {
		name    string
		tmpl    WorkflowTemplate
		wantErr bool
	}{
		{
			name: "valid ping-pong workflow",
			tmpl: WorkflowTemplate{
				Name:         "red-green",
				Agents:       []WorkflowAgent{validAgent},
				Coordination: CoordPingPong,
				Flow: &FlowConfig{
					Initial:     "red",
					Transitions: []Transition{validTransition},
				},
			},
			wantErr: false,
		},
		{
			name: "valid parallel workflow without flow",
			tmpl: WorkflowTemplate{
				Name:         "parallel-work",
				Agents:       []WorkflowAgent{validAgent, validAgent},
				Coordination: CoordParallel,
			},
			wantErr: false,
		},
		{
			name: "valid pipeline workflow",
			tmpl: WorkflowTemplate{
				Name:         "my-pipeline",
				Agents:       []WorkflowAgent{validAgent},
				Coordination: CoordPipeline,
				Flow: &FlowConfig{
					Stages:      []string{"design", "build", "test"},
					Transitions: []Transition{{From: "design", To: "build", Trigger: Trigger{Type: TriggerManual}}},
				},
			},
			wantErr: false,
		},
		{
			name: "valid name with underscore",
			tmpl: WorkflowTemplate{
				Name:         "my_workflow",
				Agents:       []WorkflowAgent{validAgent},
				Coordination: CoordParallel,
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			tmpl:    WorkflowTemplate{Agents: []WorkflowAgent{validAgent}, Coordination: CoordParallel},
			wantErr: true,
		},
		{
			name:    "invalid name format",
			tmpl:    WorkflowTemplate{Name: "Invalid_Name", Agents: []WorkflowAgent{validAgent}, Coordination: CoordParallel},
			wantErr: true,
		},
		{
			name:    "no agents",
			tmpl:    WorkflowTemplate{Name: "test", Coordination: CoordParallel},
			wantErr: true,
		},
		{
			name:    "invalid coordination type",
			tmpl:    WorkflowTemplate{Name: "test", Agents: []WorkflowAgent{validAgent}, Coordination: "invalid"},
			wantErr: true,
		},
		{
			name: "non-parallel missing flow",
			tmpl: WorkflowTemplate{
				Name:         "test",
				Agents:       []WorkflowAgent{validAgent},
				Coordination: CoordPingPong,
			},
			wantErr: true,
		},
		{
			name: "pipeline missing stages",
			tmpl: WorkflowTemplate{
				Name:         "test",
				Agents:       []WorkflowAgent{validAgent},
				Coordination: CoordPipeline,
				Flow: &FlowConfig{
					Initial:     "start",
					Transitions: []Transition{validTransition},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tmpl.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFlowConfigValidatePipelineStageGraph(t *testing.T) {
	validTransition := Transition{From: "design", To: "build", Trigger: Trigger{Type: TriggerManual}}
	tests := []struct {
		name    string
		flow    FlowConfig
		wantErr bool
	}{
		{
			name: "valid declared stage graph",
			flow: FlowConfig{
				Initial:     "design",
				Stages:      []string{"design", "build", "test"},
				Transitions: []Transition{validTransition},
			},
		},
		{
			name: "initial stage is undeclared",
			flow: FlowConfig{
				Initial:     "review",
				Stages:      []string{"design", "build"},
				Transitions: []Transition{validTransition},
			},
			wantErr: true,
		},
		{
			name: "transition source is undeclared",
			flow: FlowConfig{
				Stages:      []string{"design", "build"},
				Transitions: []Transition{{From: "review", To: "build", Trigger: Trigger{Type: TriggerManual}}},
			},
			wantErr: true,
		},
		{
			name: "transition destination is undeclared",
			flow: FlowConfig{
				Stages:      []string{"design", "build"},
				Transitions: []Transition{{From: "design", To: "review", Trigger: Trigger{Type: TriggerManual}}},
			},
			wantErr: true,
		},
		{
			name: "duplicate stage is rejected",
			flow: FlowConfig{
				Stages:      []string{"design", "design"},
				Transitions: []Transition{{From: "design", To: "design", Trigger: Trigger{Type: TriggerManual}}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.flow.Validate(CoordPipeline)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestWorkflowTemplate_GetAgentCount(t *testing.T) {
	tmpl := WorkflowTemplate{
		Agents: []WorkflowAgent{
			{Profile: "tester", Role: "red"},           // count 0 → 1
			{Profile: "impl", Role: "green", Count: 3}, // count 3
			{Profile: "reviewer", Role: "qa"},          // count 0 → 1
		},
	}

	if got := tmpl.GetAgentCount(); got != 5 {
		t.Errorf("GetAgentCount() = %d, want 5", got)
	}
}

func TestWorkflowTemplate_GetRoles(t *testing.T) {
	tmpl := WorkflowTemplate{
		Agents: []WorkflowAgent{
			{Profile: "tester", Role: "red"},
			{Profile: "impl1", Role: "green"},
			{Profile: "impl2", Role: "green"}, // duplicate role
			{Profile: "reviewer", Role: "qa"},
		},
	}

	roles := tmpl.GetRoles()
	if len(roles) != 3 {
		t.Errorf("GetRoles() returned %d roles, want 3", len(roles))
	}

	expected := map[string]bool{"red": true, "green": true, "qa": true}
	for _, role := range roles {
		if !expected[role] {
			t.Errorf("Unexpected role: %s", role)
		}
	}
}

func TestWorkflowTemplate_GetAgentsByRole(t *testing.T) {
	tmpl := WorkflowTemplate{
		Agents: []WorkflowAgent{
			{Profile: "tester", Role: "red"},
			{Profile: "impl1", Role: "green"},
			{Profile: "impl2", Role: "green"},
			{Profile: "reviewer", Role: "qa"},
		},
	}

	green := tmpl.GetAgentsByRole("green")
	if len(green) != 2 {
		t.Errorf("GetAgentsByRole(\"green\") returned %d agents, want 2", len(green))
	}

	red := tmpl.GetAgentsByRole("red")
	if len(red) != 1 {
		t.Errorf("GetAgentsByRole(\"red\") returned %d agents, want 1", len(red))
	}

	empty := tmpl.GetAgentsByRole("nonexistent")
	if len(empty) != 0 {
		t.Errorf("GetAgentsByRole(\"nonexistent\") returned %d agents, want 0", len(empty))
	}
}

func TestParseWorkflows(t *testing.T) {
	content := `
[[workflows]]
name = "red-green"
description = "TDD workflow"
coordination = "ping-pong"

[[workflows.agents]]
profile = "tester"
role = "red"

[[workflows.agents]]
profile = "implementer"
role = "green"

[workflows.flow]
initial = "red"

[[workflows.flow.transitions]]
from = "red"
to = "green"
[workflows.flow.transitions.trigger]
type = "manual"
label = "Tests written"

[[workflows.flow.transitions]]
from = "green"
to = "red"
[workflows.flow.transitions.trigger]
type = "command_success"
command = "go test ./..."
`

	workflows, err := ParseWorkflows(content)
	if err != nil {
		t.Fatalf("ParseWorkflows() error = %v", err)
	}

	if len(workflows) != 1 {
		t.Fatalf("ParseWorkflows() returned %d workflows, want 1", len(workflows))
	}

	w := workflows[0]
	if w.Name != "red-green" {
		t.Errorf("Name = %q, want %q", w.Name, "red-green")
	}
	if w.Coordination != CoordPingPong {
		t.Errorf("Coordination = %q, want %q", w.Coordination, CoordPingPong)
	}
	if len(w.Agents) != 2 {
		t.Errorf("len(Agents) = %d, want 2", len(w.Agents))
	}
	if w.Flow == nil {
		t.Fatal("Flow is nil")
	}
	if len(w.Flow.Transitions) != 2 {
		t.Errorf("len(Transitions) = %d, want 2", len(w.Flow.Transitions))
	}
}

func TestParseWorkflow_SingleFormat(t *testing.T) {
	content := `
name = "simple-workflow"
description = "A simple workflow"
coordination = "parallel"

[[agents]]
profile = "worker"
role = "doer"
`

	w, err := ParseWorkflow(content)
	if err != nil {
		t.Fatalf("ParseWorkflow() error = %v", err)
	}

	if w.Name != "simple-workflow" {
		t.Errorf("Name = %q, want %q", w.Name, "simple-workflow")
	}
	if w.Coordination != CoordParallel {
		t.Errorf("Coordination = %q, want %q", w.Coordination, CoordParallel)
	}
}

func TestParseWorkflows_RejectsUnknownFields(t *testing.T) {
	content := `
legacy = true

[[workflows]]
name = "red-green"
description = "TDD workflow"
coordination = "parallel"

[[workflows.agents]]
profile = "tester"
role = "red"
`

	_, err := ParseWorkflows(content)
	if err == nil {
		t.Fatal("ParseWorkflows() expected unknown field error")
	}
	if !strings.Contains(err.Error(), "unknown field(s): legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWorkflow_RejectsUnknownFields(t *testing.T) {
	content := `
name = "simple-workflow"
description = "A simple workflow"
coordination = "parallel"
legacy = true

[[agents]]
profile = "worker"
role = "doer"
`

	_, err := ParseWorkflow(content)
	if err == nil {
		t.Fatal("ParseWorkflow() expected unknown field error")
	}
	if !strings.Contains(err.Error(), "unknown field(s): legacy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseAndValidateWorkflow(t *testing.T) {
	valid := `
name = "valid-workflow"
coordination = "parallel"

[[agents]]
profile = "worker"
role = "doer"
`

	_, err := ParseAndValidateWorkflow(valid)
	if err != nil {
		t.Errorf("ParseAndValidateWorkflow() valid workflow error = %v", err)
	}

	invalid := `
name = "INVALID"
coordination = "parallel"

[[agents]]
profile = "worker"
role = "doer"
`

	_, err = ParseAndValidateWorkflow(invalid)
	if err == nil {
		t.Error("ParseAndValidateWorkflow() invalid workflow should error")
	}
}

func TestWorkflowTemplate_String(t *testing.T) {
	tmpl := WorkflowTemplate{
		Name:        "test-workflow",
		Description: "A test workflow",
		Agents: []WorkflowAgent{
			{Profile: "a", Role: "r1"},
			{Profile: "b", Role: "r2", Count: 2},
		},
		Coordination: CoordPipeline,
	}

	s := tmpl.String()
	if s != "test-workflow: A test workflow (3 agents, pipeline)" {
		t.Errorf("String() = %q", s)
	}
}

func TestFlowConfig_ValidateApprovalMode(t *testing.T) {
	tests := []struct {
		name    string
		flow    FlowConfig
		wantErr bool
	}{
		{
			name: "valid any mode",
			flow: FlowConfig{
				Initial:         "start",
				Transitions:     []Transition{{From: "a", To: "b", Trigger: Trigger{Type: TriggerManual}}},
				RequireApproval: true,
				ApprovalMode:    "any",
			},
			wantErr: false,
		},
		{
			name: "valid quorum mode",
			flow: FlowConfig{
				Initial:         "start",
				Transitions:     []Transition{{From: "a", To: "b", Trigger: Trigger{Type: TriggerManual}}},
				RequireApproval: true,
				ApprovalMode:    "quorum",
				Quorum:          2,
			},
			wantErr: false,
		},
		{
			name: "invalid approval mode",
			flow: FlowConfig{
				Initial:         "start",
				Transitions:     []Transition{{From: "a", To: "b", Trigger: Trigger{Type: TriggerManual}}},
				RequireApproval: true,
				ApprovalMode:    "invalid",
			},
			wantErr: true,
		},
		{
			name: "quorum mode without quorum value",
			flow: FlowConfig{
				Initial:         "start",
				Transitions:     []Transition{{From: "a", To: "b", Trigger: Trigger{Type: TriggerManual}}},
				RequireApproval: true,
				ApprovalMode:    "quorum",
				Quorum:          0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.flow.Validate(CoordPingPong)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestErrorConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  ErrorConfig
		wantErr bool
	}{
		{
			name:    "empty config is valid",
			config:  ErrorConfig{},
			wantErr: false,
		},
		{
			name: "valid config",
			config: ErrorConfig{
				OnAgentCrash:        ErrorActionRestartAgent,
				OnAgentError:        ErrorActionPause,
				OnTimeout:           ErrorActionNotify,
				StageTimeoutMinutes: 30,
				MaxRetriesPerStage:  3,
			},
			wantErr: false,
		},
		{
			name: "invalid action",
			config: ErrorConfig{
				OnAgentCrash: "invalid_action",
			},
			wantErr: true,
		},
		{
			name: "negative timeout",
			config: ErrorConfig{
				StageTimeoutMinutes: -1,
			},
			wantErr: true,
		},
		{
			name: "negative retries",
			config: ErrorConfig{
				MaxRetriesPerStage: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseWorkflows_WithErrorHandling(t *testing.T) {
	content := `
[[workflows]]
name = "robust-workflow"
coordination = "parallel"

[[workflows.agents]]
profile = "worker"
role = "doer"

[workflows.error_handling]
on_agent_crash = "restart_agent"
on_agent_error = "pause"
on_timeout = "notify"
stage_timeout_minutes = 30
max_retries_per_stage = 3
`

	workflows, err := ParseWorkflows(content)
	if err != nil {
		t.Fatalf("ParseWorkflows() error = %v", err)
	}

	if len(workflows) != 1 {
		t.Fatalf("ParseWorkflows() returned %d workflows, want 1", len(workflows))
	}

	w := workflows[0]
	if w.ErrorHandling == nil {
		t.Fatal("ErrorHandling is nil")
	}
	if w.ErrorHandling.OnAgentCrash != ErrorActionRestartAgent {
		t.Errorf("OnAgentCrash = %q, want %q", w.ErrorHandling.OnAgentCrash, ErrorActionRestartAgent)
	}
	if w.ErrorHandling.StageTimeoutMinutes != 30 {
		t.Errorf("StageTimeoutMinutes = %d, want 30", w.ErrorHandling.StageTimeoutMinutes)
	}
}
