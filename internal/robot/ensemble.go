// Package robot provides machine-readable output for AI agents.
// ensemble.go implements --robot-ensemble for ensemble state querying.
package robot

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ErrCodeEnsembleNotFound indicates the session has no ensemble state.
const ErrCodeEnsembleNotFound = "ENSEMBLE_NOT_FOUND"

// EnsembleOutput is the structured response for --robot-ensemble.
type EnsembleOutput struct {
	RobotResponse
	Ensemble   EnsembleState   `json:"ensemble"`
	Summary    EnsembleSummary `json:"summary"`
	AgentHints *AgentHints     `json:"_agent_hints,omitempty"`
}

// EnsembleState represents ensemble state for a session.
type EnsembleState struct {
	Session   string            `json:"session"`
	Question  string            `json:"question,omitempty"`
	Preset    string            `json:"preset,omitempty"`
	Status    string            `json:"status,omitempty"`
	StartedAt string            `json:"started_at,omitempty"`
	Modes     []EnsembleMode    `json:"modes"`
	Synthesis EnsembleSynthesis `json:"synthesis"`
	Budget    EnsembleBudget    `json:"budget"`
	Metrics   EnsembleMetrics   `json:"metrics"`
}

// EnsembleMode represents a single mode assignment in the ensemble.
type EnsembleMode struct {
	ID        string `json:"id"`
	Code      string `json:"code,omitempty"`
	Tier      string `json:"tier,omitempty"`
	Name      string `json:"name,omitempty"`
	Pane      string `json:"pane,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	Status    string `json:"status,omitempty"`
	Icon      string `json:"icon,omitempty"`
}

// EnsembleSynthesis represents synthesis strategy and status.
type EnsembleSynthesis struct {
	Strategy string `json:"strategy,omitempty"`
	Status   string `json:"status,omitempty"`
}

// EnsembleBudget reports token budget usage for the ensemble.
type EnsembleBudget struct {
	TotalTokens     int `json:"total_tokens,omitempty"`
	SpentTokens     int `json:"spent_tokens,omitempty"`
	RemainingTokens int `json:"remaining_tokens,omitempty"`
}

// EnsembleMetrics represents coverage and quality metrics for the ensemble.
type EnsembleMetrics struct {
	CoverageMap      map[string]int `json:"coverage_map"`
	RedundancyScore  float64        `json:"redundancy_score,omitempty"`
	FindingsVelocity float64        `json:"findings_velocity,omitempty"`
	ConflictDensity  float64        `json:"conflict_density,omitempty"`
}

// EnsembleSummary provides quick counts for ensemble status.
type EnsembleSummary struct {
	TotalModes    int `json:"total_modes"`
	Completed     int `json:"completed"`
	Working       int `json:"working"`
	Pending       int `json:"pending"`
	Errors        int `json:"errors,omitempty"`
	CoreModes     int `json:"core_modes"`
	AdvancedModes int `json:"advanced_modes"`
}

// GetEnsemble retrieves ensemble state for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetEnsemble(session string) (*EnsembleOutput, error) {
	output := &EnsembleOutput{
		RobotResponse: NewRobotResponse(true),
		Ensemble: EnsembleState{
			Session:   session,
			Modes:     []EnsembleMode{},
			Synthesis: EnsembleSynthesis{},
			Budget:    EnsembleBudget{},
			Metrics: EnsembleMetrics{
				CoverageMap: map[string]int{},
			},
		},
		Summary: EnsembleSummary{},
	}

	if strings.TrimSpace(session) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-ensemble=myproject",
		)
		return output, nil
	}

	state, err := ensemble.LoadSession(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !tmux.SessionExists(session) {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("session '%s' not found", session),
					ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				)
				return output, nil
			}
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("ensemble state not found for session '%s'", session),
				ErrCodeEnsembleNotFound,
				"Spawn an ensemble first: ntm ensemble <preset> <question>",
			)
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to load ensemble state: %w", err),
			ErrCodeInternalError,
			"Check state store availability",
		)
		return output, nil
	}

	output.Ensemble.Session = session
	output.Ensemble.Question = state.Question
	output.Ensemble.Preset = state.PresetUsed
	output.Ensemble.Status = state.Status.String()
	output.Ensemble.StartedAt = FormatTimestamp(state.CreatedAt)
	output.Ensemble.Synthesis = EnsembleSynthesis{
		Strategy: state.SynthesisStrategy.String(),
		Status:   synthesisStatus(state.Status, state.Assignments),
	}

	budget := resolveEnsembleBudget(state.PresetUsed)
	output.Ensemble.Budget = EnsembleBudget{
		TotalTokens:     budget.MaxTotalTokens,
		SpentTokens:     0,
		RemainingTokens: budget.MaxTotalTokens,
	}

	catalog, _ := ensemble.GlobalCatalog()
	coreCount := 0
	advancedCount := 0

	for _, assignment := range state.Assignments {
		modeOut := EnsembleMode{
			ID:        assignment.ModeID,
			Pane:      assignment.PaneName,
			AgentType: normalizeAgentType(assignment.AgentType),
			Status:    string(assignment.Status),
		}

		if catalog != nil {
			if mode := catalog.GetMode(assignment.ModeID); mode != nil {
				modeOut.Code = mode.Code
				modeOut.Tier = mode.Tier.String()
				modeOut.Name = mode.Name
				modeOut.Icon = mode.Icon
				if letter := mode.Category.CategoryLetter(); letter != "" {
					output.Ensemble.Metrics.CoverageMap[letter]++
				}
				if mode.Tier == ensemble.TierCore {
					coreCount++
				} else {
					advancedCount++
				}
			}
		}

		output.Ensemble.Modes = append(output.Ensemble.Modes, modeOut)
		switch assignment.Status {
		case ensemble.AssignmentDone:
			output.Summary.Completed++
		case ensemble.AssignmentActive, ensemble.AssignmentInjecting:
			output.Summary.Working++
		case ensemble.AssignmentPending:
			output.Summary.Pending++
		case ensemble.AssignmentError:
			output.Summary.Errors++
		default:
			output.Summary.Pending++
		}
	}

	output.Summary.TotalModes = len(state.Assignments)
	output.Summary.CoreModes = coreCount
	output.Summary.AdvancedModes = advancedCount

	output.AgentHints = buildEnsembleHints(*output)

	return output, nil
}

// PrintEnsemble outputs ensemble state for a session.
func PrintEnsemble(session string) error {
	output, err := GetEnsemble(session)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

func resolveEnsembleBudget(preset string) ensemble.BudgetConfig {
	budget := ensemble.DefaultBudgetConfig()
	name := strings.TrimSpace(preset)
	if name == "" {
		return budget
	}
	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil || registry == nil {
		return budget
	}
	presetCfg := registry.Get(name)
	if presetCfg == nil {
		return budget
	}

	budget = mergeBudgetConfig(budget, presetCfg.Budget)
	return budget
}

func mergeBudgetConfig(base, override ensemble.BudgetConfig) ensemble.BudgetConfig {
	if override.MaxTokensPerMode > 0 {
		base.MaxTokensPerMode = override.MaxTokensPerMode
	}
	if override.MaxTotalTokens > 0 {
		base.MaxTotalTokens = override.MaxTotalTokens
	}
	if override.SynthesisReserveTokens > 0 {
		base.SynthesisReserveTokens = override.SynthesisReserveTokens
	}
	if override.ContextReserveTokens > 0 {
		base.ContextReserveTokens = override.ContextReserveTokens
	}
	if override.TimeoutPerMode > 0 {
		base.TimeoutPerMode = override.TimeoutPerMode
	}
	if override.TotalTimeout > 0 {
		base.TotalTimeout = override.TotalTimeout
	}
	if override.MaxRetries > 0 {
		base.MaxRetries = override.MaxRetries
	}
	return base
}

func synthesisStatus(status ensemble.EnsembleStatus, assignments []ensemble.ModeAssignment) string {
	switch status {
	case ensemble.EnsembleSynthesizing:
		return "running"
	case ensemble.EnsembleComplete:
		return "complete"
	case ensemble.EnsembleError:
		return "error"
	}

	if len(assignments) == 0 {
		return "not_started"
	}

	for _, assignment := range assignments {
		if assignment.Status == ensemble.AssignmentError {
			return "error"
		}
		if assignment.Status == ensemble.AssignmentPending || assignment.Status == ensemble.AssignmentInjecting || assignment.Status == ensemble.AssignmentActive {
			return "not_started"
		}
	}

	return "ready"
}

func buildEnsembleHints(output EnsembleOutput) *AgentHints {
	hints := &AgentHints{}
	total := output.Summary.TotalModes
	if total > 0 {
		hints.Summary = fmt.Sprintf("%d/%d modes complete, %d working, %d pending", output.Summary.Completed, total, output.Summary.Working, output.Summary.Pending)
		if output.Summary.Errors > 0 {
			hints.Summary += fmt.Sprintf(", %d errors", output.Summary.Errors)
		}
	}

	if output.Summary.Pending > 0 || output.Summary.Working > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "wait",
			Target:   "ensemble modes",
			Reason:   "modes are still running or pending",
			Priority: 1,
		})
	} else if output.Summary.Errors > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "review",
			Target:   "failed ensemble modes",
			Reason:   "one or more modes failed and need investigation",
			Priority: 1,
		})
	} else if total > 0 && output.Ensemble.Synthesis.Status == "ready" {
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "synthesize",
			Target:   "ensemble",
			Reason:   "all mode outputs complete",
			Priority: 1,
		})
	}

	for _, mode := range output.Ensemble.Modes {
		if mode.Status == string(ensemble.AssignmentError) {
			hints.Warnings = append(hints.Warnings, "one or more modes reported errors; review pane output")
			break
		}
	}
	hints.Warnings = append(hints.Warnings, "metrics not fully computed yet; coverage_map only")

	if hints.Summary == "" && len(hints.SuggestedActions) == 0 && len(hints.Warnings) == 0 {
		return nil
	}
	return hints
}
