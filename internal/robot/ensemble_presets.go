// Package robot provides machine-readable output for AI agents.
// ensemble_presets.go implements --robot-ensemble-presets for listing ensemble presets.
package robot

import (
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

// EnsemblePresetsOutput is the structured response for --robot-ensemble-presets.
type EnsemblePresetsOutput struct {
	RobotResponse
	Action     string       `json:"action"`
	Presets    []PresetInfo `json:"presets"`
	Count      int          `json:"count"`
	AgentHints *AgentHints  `json:"_agent_hints,omitempty"`
}

// PresetInfo represents an ensemble preset in the API response.
type PresetInfo struct {
	Name          string        `json:"name"`
	Display       string        `json:"display,omitempty"`
	Description   string        `json:"description"`
	Modes         []string      `json:"modes"`
	ModeCount     int           `json:"mode_count"`
	Synthesis     SynthesisInfo `json:"synthesis"`
	Budget        BudgetInfo    `json:"budget"`
	AllowAdvanced bool          `json:"allow_advanced"`
	Tags          []string      `json:"tags,omitempty"`
	Source        string        `json:"source,omitempty"`
}

// SynthesisInfo represents synthesis configuration in preset output.
type SynthesisInfo struct {
	Strategy string `json:"strategy"`
}

// BudgetInfo represents budget configuration in preset output.
type BudgetInfo struct {
	Total int `json:"total"`
}

// GetEnsemblePresets retrieves the list of available ensemble presets.
func GetEnsemblePresets() (*EnsemblePresetsOutput, error) {
	output := &EnsemblePresetsOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_presets",
		Presets:       []PresetInfo{},
	}

	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Failed to load ensemble registry",
		)
		return output, nil
	}
	if registry == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ensemble registry is nil"),
			ErrCodeInternalError,
			"Ensemble registry is nil",
		)
		return output, nil
	}

	presets := registry.List()
	catalog, _ := ensemble.GlobalCatalog()

	for _, p := range presets {
		// Resolve mode IDs
		modeIDs := make([]string, 0, len(p.Modes))
		for _, mref := range p.Modes {
			modeIDs = append(modeIDs, mref.ID)
		}

		// Get mode codes if catalog available
		modeCodes := make([]string, 0, len(p.Modes))
		if catalog != nil {
			for _, id := range modeIDs {
				if mode := catalog.GetMode(id); mode != nil {
					modeCodes = append(modeCodes, mode.Code)
				} else {
					modeCodes = append(modeCodes, id)
				}
			}
		} else {
			modeCodes = modeIDs
		}

		preset := PresetInfo{
			Name:        p.Name,
			Display:     p.DisplayName,
			Description: p.Description,
			Modes:       modeCodes,
			ModeCount:   len(p.Modes),
			Synthesis: SynthesisInfo{
				Strategy: p.Synthesis.Strategy.String(),
			},
			Budget: BudgetInfo{
				Total: p.Budget.MaxTotalTokens,
			},
			AllowAdvanced: p.AllowAdvanced,
			Tags:          p.Tags,
			Source:        p.Source,
		}

		// Ensure tags is never nil
		if preset.Tags == nil {
			preset.Tags = []string{}
		}

		output.Presets = append(output.Presets, preset)
	}

	output.Count = registry.Count()

	// Build agent hints
	output.AgentHints = buildPresetsHints(output)

	return output, nil
}

// PrintEnsemblePresets outputs the presets list as JSON.
func PrintEnsemblePresets() error {
	output, err := GetEnsemblePresets()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ensemble presets failed")
}

// buildPresetsHints creates agent hints for the presets output.
func buildPresetsHints(output *EnsemblePresetsOutput) *AgentHints {
	if output == nil {
		return nil
	}

	hints := &AgentHints{}

	if output.Count > 0 {
		hints.Summary = "Use presets: ntm ensemble <preset-name> <question>"
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "spawn_ensemble",
			Target:   "project-diagnosis",
			Reason:   "Good default for comprehensive analysis",
			Priority: 1,
		})
	} else {
		hints.Warnings = append(hints.Warnings, "No presets available")
	}

	if hints.Summary == "" && len(hints.SuggestedActions) == 0 && len(hints.Warnings) == 0 {
		return nil
	}
	return hints
}
