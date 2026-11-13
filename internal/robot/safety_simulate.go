package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// SafetySimulationOptions configures --robot-safety-simulate.
type SafetySimulationOptions struct {
	Command string
	Steps   []string
}

// SafetySimulationOutput is the robot envelope for a no-execution policy simulation.
type SafetySimulationOutput struct {
	RobotResponse
	GeneratedAt string                   `json:"generated_at"`
	Commands    []string                 `json:"commands"`
	Steps       []policy.SimulationStep  `json:"steps"`
	Summary     policy.SimulationSummary `json:"summary"`
	SafeToRun   bool                     `json:"safe_to_run"`
	Notes       []string                 `json:"notes,omitempty"`
}

// GetSafetySimulation evaluates a proposed command plan without executing it.
func GetSafetySimulation(opts SafetySimulationOptions) (*SafetySimulationOutput, error) {
	meta, finish := StartResponseMeta("robot-safety-simulate")
	defer finish()

	commands := normalizeSafetySimulationCommands(opts)
	p, err := policy.LoadOrDefault()
	if err != nil {
		output := &SafetySimulationOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Fix the NTM safety policy file, then rerun the simulation.",
			),
			GeneratedAt: FormatTimestamp(time.Now()),
			Commands:    commands,
			Steps:       []policy.SimulationStep{},
			SafeToRun:   false,
			Notes:       []string{"simulation only; no command was executed"},
		}
		output.Meta = meta.WithExitCode(1)
		return output, nil
	}

	report := policy.SimulatePlan(p, commands)
	output := &SafetySimulationOutput{
		RobotResponse: NewRobotResponseWithMeta(true, meta.WithExitCode(0)),
		GeneratedAt:   FormatTimestamp(report.GeneratedAt),
		Commands:      commands,
		Steps:         report.Steps,
		Summary:       report.Summary,
		SafeToRun:     report.SafeToRun,
		Notes:         report.Notes,
	}
	if output.Commands == nil {
		output.Commands = []string{}
	}
	if output.Steps == nil {
		output.Steps = []policy.SimulationStep{}
	}
	return output, nil
}

func normalizeSafetySimulationCommands(opts SafetySimulationOptions) []string {
	commands := make([]string, 0, len(opts.Steps)+1)
	if strings.TrimSpace(opts.Command) != "" {
		commands = append(commands, opts.Command)
	}
	commands = append(commands, opts.Steps...)
	return commands
}

// PrintSafetySimulation emits a robot JSON simulation report.
func PrintSafetySimulation(opts SafetySimulationOptions) error {
	output, err := GetSafetySimulation(opts)
	if err != nil {
		return fmt.Errorf("safety simulation: %w", err)
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot safety simulation failed")
}

// PolicyDecisionsOptions configures --robot-policy-decisions and GET /api/v1/policy/decisions.
type PolicyDecisionsOptions struct {
	EntityType string
	EntityID   string
	Limit      int
}

// PolicyDecisionsOutput is the robot envelope for durable audit decision history.
type PolicyDecisionsOutput struct {
	RobotResponse
	Decisions  []state.AuditDecision `json:"decisions"`
	Count      int                   `json:"count"`
	EntityType string                `json:"entity_type,omitempty"`
	EntityID   string                `json:"entity_id,omitempty"`
	Limit      int                   `json:"limit"`
}

// GetPolicyDecisions queries the runtime projection store's audit decision log.
func GetPolicyDecisions(opts PolicyDecisionsOptions) (*PolicyDecisionsOutput, error) {
	return GetPolicyDecisionsForStore(currentProjectionStore(), opts)
}

// GetPolicyDecisionsForStore queries audit decision history from an explicit state.Store.
func GetPolicyDecisionsForStore(store *state.Store, opts PolicyDecisionsOptions) (*PolicyDecisionsOutput, error) {
	meta, finish := StartResponseMeta("robot-policy-decisions")
	defer finish()

	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > state.DefaultMaxAuditDecisionRows {
		limit = state.DefaultMaxAuditDecisionRows
	}
	entityType := strings.TrimSpace(opts.EntityType)
	entityID := strings.TrimSpace(opts.EntityID)

	if store == nil {
		output := &PolicyDecisionsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("runtime state store is not initialized"),
				ErrCodeNotImplemented,
				"Start ntm serve or run with a writable NTM state directory.",
			),
			Decisions:  make([]state.AuditDecision, 0),
			Count:      0,
			EntityType: entityType,
			EntityID:   entityID,
			Limit:      limit,
		}
		output.Meta = meta.WithExitCode(ExitCodeForResponse(output.RobotResponse))
		return output, nil
	}

	decisions, err := store.GetDecisionHistory(entityType, entityID, limit)
	if err != nil {
		output := &PolicyDecisionsOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check runtime state database health via ntm --robot-health.",
			),
			Decisions:  make([]state.AuditDecision, 0),
			Count:      0,
			EntityType: entityType,
			EntityID:   entityID,
			Limit:      limit,
		}
		output.Meta = meta.WithExitCode(1)
		return output, nil
	}
	if decisions == nil {
		decisions = make([]state.AuditDecision, 0)
	}

	return &PolicyDecisionsOutput{
		RobotResponse: NewRobotResponseWithMeta(true, meta.WithExitCode(0)),
		Decisions:     decisions,
		Count:         len(decisions),
		EntityType:    entityType,
		EntityID:      entityID,
		Limit:         limit,
	}, nil
}

// PrintPolicyDecisions emits the durable audit decision history as robot JSON.
func PrintPolicyDecisions(opts PolicyDecisionsOptions) error {
	output, err := GetPolicyDecisions(opts)
	if err != nil {
		return fmt.Errorf("policy decisions: %w", err)
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot policy decisions failed")
}
