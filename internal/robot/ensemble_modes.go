// Package robot provides machine-readable output for AI agents.
// ensemble_modes.go implements --robot-ensemble-modes for listing reasoning modes.
package robot

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

// EnsembleModesOutput is the structured response for --robot-ensemble-modes.
type EnsembleModesOutput struct {
	RobotResponse
	Action            string          `json:"action"`
	CatalogVersion    string          `json:"catalog_version,omitempty"`
	Modes             []ModeInfo      `json:"modes"`
	Categories        []CategoryInfo  `json:"categories"`
	DefaultTier       string          `json:"default_tier"`
	TotalModes        int             `json:"total_modes"`
	CoreModes         int             `json:"core_modes"`
	AdvancedModes     int             `json:"advanced_modes"`
	ExperimentalModes int             `json:"experimental_modes"`
	Pagination        *PaginationInfo `json:"pagination,omitempty"`
	AgentHints        *AgentHints     `json:"_agent_hints,omitempty"`
}

// ModeInfo represents a reasoning mode in the API response.
type ModeInfo struct {
	ID             string      `json:"id"`
	Code           string      `json:"code"`
	Tier           string      `json:"tier"`
	Name           string      `json:"name"`
	Category       CategoryRef `json:"category"`
	ShortDesc      string      `json:"short_desc"`
	Description    string      `json:"description,omitempty"`
	Outputs        string      `json:"outputs,omitempty"`
	BestFor        []string    `json:"best_for,omitempty"`
	Differentiator string      `json:"differentiator,omitempty"`
	FailureModes   []string    `json:"failure_modes,omitempty"`
	Icon           string      `json:"icon,omitempty"`
	Color          string      `json:"color,omitempty"`
}

// CategoryRef represents a category reference in mode output.
type CategoryRef struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// CategoryInfo provides category summary in the response.
type CategoryInfo struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	ModeCount int    `json:"mode_count"`
}

// EnsembleModesOptions configures the modes query.
type EnsembleModesOptions struct {
	Category string
	Tier     string
	Search   string
	Limit    int
	Offset   int
}

// GetEnsembleModes retrieves the list of available reasoning modes.
func GetEnsembleModes(opts EnsembleModesOptions) (*EnsembleModesOutput, error) {
	output := &EnsembleModesOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_modes",
		Modes:         []ModeInfo{},
		Categories:    []CategoryInfo{},
		DefaultTier:   "core",
	}

	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Failed to load mode catalog",
		)
		return output, nil
	}
	if catalog == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("mode catalog is nil"),
			ErrCodeInternalError,
			"Mode catalog is nil",
		)
		return output, nil
	}

	// Query modes from catalog using tier/search helpers
	tier := strings.ToLower(strings.TrimSpace(opts.Tier))
	var allModes []ensemble.ReasoningMode
	if searchTerm := strings.TrimSpace(opts.Search); searchTerm != "" {
		allModes = catalog.SearchModes(searchTerm)
		switch tier {
		case "", "core":
			allModes = filterModesByTier(allModes, ensemble.TierCore)
		case "advanced":
			allModes = filterModesByTier(allModes, ensemble.TierAdvanced)
		case "experimental":
			allModes = filterModesByTier(allModes, ensemble.TierExperimental)
		}
	} else {
		switch tier {
		case "", "core":
			allModes = catalog.ListDefault()
		case "advanced":
			allModes = catalog.ListByTier(ensemble.TierAdvanced)
		case "experimental":
			allModes = catalog.ListByTier(ensemble.TierExperimental)
		default:
			allModes = catalog.ListModes()
		}
	}

	// Apply category filter
	if opts.Category != "" {
		allModes = filterModesByCategory(allModes, opts.Category)
	}

	// Count totals before pagination
	totalCount := len(allModes)

	// Apply pagination
	if opts.Limit <= 0 {
		opts.Limit = 50 // Default limit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	start := opts.Offset
	if start > len(allModes) {
		start = len(allModes)
	}
	end := start + opts.Limit
	if end > len(allModes) {
		end = len(allModes)
	}
	pagedModes := allModes[start:end]

	// Build mode info list
	for _, mode := range pagedModes {
		output.Modes = append(output.Modes, ModeInfo{
			ID:             mode.ID,
			Code:           mode.Code,
			Tier:           mode.Tier.String(),
			Name:           mode.Name,
			Category:       CategoryRef{Code: mode.Category.CategoryLetter(), Name: mode.Category.String()},
			ShortDesc:      mode.ShortDesc,
			Description:    mode.Description,
			Outputs:        mode.Outputs,
			BestFor:        mode.BestFor,
			Differentiator: mode.Differentiator,
			FailureModes:   mode.FailureModes,
			Icon:           mode.Icon,
			Color:          mode.Color,
		})
	}

	// Build category summary from full catalog (unfiltered)
	output.Categories = buildCategorySummary(catalog)

	// Count tier totals and metadata from full catalog
	output.CatalogVersion = catalog.Version()
	output.CoreModes = len(catalog.ListDefault())
	output.AdvancedModes = len(catalog.ListByTier(ensemble.TierAdvanced))
	output.ExperimentalModes = len(catalog.ListByTier(ensemble.TierExperimental))
	output.TotalModes = catalog.Count()

	// Add pagination info
	if totalCount > opts.Limit || opts.Offset > 0 {
		output.Pagination = &PaginationInfo{
			Limit:   opts.Limit,
			Offset:  opts.Offset,
			Count:   len(pagedModes),
			Total:   totalCount,
			HasMore: end < totalCount,
		}
	}

	// Build agent hints
	output.AgentHints = buildModesHints(output)

	return output, nil
}

// PrintEnsembleModes outputs the modes list as JSON.
func PrintEnsembleModes(opts EnsembleModesOptions) error {
	output, err := GetEnsembleModes(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ensemble modes failed")
}

// filterModesByCategory filters modes by category code or name.
func filterModesByCategory(modes []ensemble.ReasoningMode, category string) []ensemble.ReasoningMode {
	category = strings.TrimSpace(category)
	if category == "" {
		return modes
	}

	// Try to match by code letter first
	upper := strings.ToUpper(category)
	if len(upper) == 1 && upper[0] >= 'A' && upper[0] <= 'L' {
		cat, ok := ensemble.CategoryFromLetter(upper)
		if ok {
			var result []ensemble.ReasoningMode
			for _, m := range modes {
				if m.Category == cat {
					result = append(result, m)
				}
			}
			return result
		}
	}

	// Try to match by category name
	lower := strings.ToLower(category)
	var result []ensemble.ReasoningMode
	for _, m := range modes {
		if strings.ToLower(m.Category.String()) == lower {
			result = append(result, m)
		}
	}
	return result
}

// filterModesByTier filters modes by tier.
func filterModesByTier(modes []ensemble.ReasoningMode, tier ensemble.ModeTier) []ensemble.ReasoningMode {
	var result []ensemble.ReasoningMode
	for _, m := range modes {
		if m.Tier == tier {
			result = append(result, m)
		}
	}
	return result
}

// buildCategorySummary creates category info from the catalog.
func buildCategorySummary(catalog *ensemble.ModeCatalog) []CategoryInfo {
	if catalog == nil {
		return []CategoryInfo{}
	}

	categories := ensemble.AllCategories()
	result := make([]CategoryInfo, 0, len(categories))

	for _, cat := range categories {
		modes := catalog.ListByCategory(cat)
		if len(modes) > 0 {
			result = append(result, CategoryInfo{
				Code:      cat.CategoryLetter(),
				Name:      cat.String(),
				ModeCount: len(modes),
			})
		}
	}

	return result
}

// buildModesHints creates agent hints for the modes output.
func buildModesHints(output *EnsembleModesOutput) *AgentHints {
	if output == nil {
		return nil
	}

	hints := &AgentHints{}

	// Provide summary
	if len(output.Modes) > 0 {
		hints.Summary = "Use modes in ensembles via: ntm ensemble <preset> <question>"
	}

	// Suggest actions
	if output.CoreModes > 0 && len(output.Modes) == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "list_modes",
			Target:   "all",
			Reason:   "No modes matched current filter; try --tier=all",
			Priority: 1,
		})
	}

	if output.Pagination != nil {
		if next, pages := paginationHintOffsets(output.Pagination); next != nil {
			hints.NextOffset = next
			hints.PagesRemaining = pages
		}
	}

	if hints.Summary == "" && len(hints.SuggestedActions) == 0 &&
		hints.NextOffset == nil && len(hints.Warnings) == 0 && len(hints.Notes) == 0 {
		return nil
	}
	return hints
}
