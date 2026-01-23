package ensemble

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// CategoryAffinities maps reasoning categories to preferred agent types (ordered).
// These preferences are applied when assigning modes to panes by category.
var CategoryAffinities = map[ModeCategory][]string{
	CategoryFormal:      {string(tmux.AgentClaude), string(tmux.AgentCodex)},
	CategoryUncertainty: {string(tmux.AgentCodex), string(tmux.AgentClaude)},
	CategoryCausal:      {string(tmux.AgentClaude), string(tmux.AgentCodex)},
	CategoryPractical:   {string(tmux.AgentCodex), string(tmux.AgentClaude)},
	CategoryStrategic:   {string(tmux.AgentClaude), string(tmux.AgentCodex)},
	CategoryDialectical: {string(tmux.AgentClaude)},
	CategoryModal:       {string(tmux.AgentClaude), string(tmux.AgentCodex)},
	CategoryDomain:      {string(tmux.AgentClaude), string(tmux.AgentGemini)},
	CategoryMeta:        {string(tmux.AgentClaude)},
	CategoryAmpliative:  {string(tmux.AgentGemini), string(tmux.AgentClaude)},
	CategoryVagueness:   {string(tmux.AgentGemini), string(tmux.AgentClaude)},
	CategoryChange:      {string(tmux.AgentClaude), string(tmux.AgentCodex)},
}

var defaultPreferredTypes = []string{
	string(tmux.AgentClaude),
	string(tmux.AgentCodex),
	string(tmux.AgentGemini),
}

// AssignRoundRobin distributes modes evenly across available panes.
func AssignRoundRobin(modes []string, panes []tmux.Pane) []ModeAssignment {
	logger := slog.Default()

	normalized, err := normalizeModeKeys(modes)
	if err != nil {
		logger.Error("round-robin assignment failed", "error", err)
		return nil
	}
	orderedPanes := sortAssignablePanes(panes)

	if len(normalized) > len(orderedPanes) {
		logger.Error("round-robin assignment failed: more modes than panes",
			"modes", len(normalized),
			"panes", len(orderedPanes),
		)
		return nil
	}

	assignments := make([]ModeAssignment, 0, len(normalized))
	now := time.Now().UTC()
	for i, modeID := range normalized {
		pane := orderedPanes[i]
		assignments = append(assignments, ModeAssignment{
			ModeID:     modeID,
			PaneName:   pane.Title,
			AgentType:  string(pane.Type),
			Status:     AssignmentPending,
			AssignedAt: now,
		})
		logger.Info("ensemble assignment decided",
			"strategy", "round_robin",
			"mode_id", modeID,
			"pane", pane.Title,
			"agent_type", string(pane.Type),
		)
	}

	if err := ValidateAssignments(assignments, normalized); err != nil {
		logger.Error("round-robin assignment failed validation", "error", err)
		return nil
	}

	return assignments
}

// AssignByCategory assigns modes based on category-to-agent affinities.
func AssignByCategory(modes []string, panes []tmux.Pane, catalog *ModeCatalog) []ModeAssignment {
	logger := slog.Default()

	items, err := resolveModeItems(modes, catalog)
	if err != nil {
		logger.Error("category assignment failed", "error", err)
		return nil
	}

	orderedPanes := sortAssignablePanes(panes)
	if len(items) > len(orderedPanes) {
		logger.Error("category assignment failed: more modes than panes",
			"modes", len(items),
			"panes", len(orderedPanes),
		)
		return nil
	}

	byType := groupPanesByType(orderedPanes)
	assignments := make([]ModeAssignment, 0, len(items))
	now := time.Now().UTC()

	for _, item := range items {
		preferred := CategoryAffinities[item.category]
		if len(preferred) == 0 {
			preferred = defaultPreferredTypes
		}
		choice, fallback, reason := pickAvailablePaneWithReason(byType, preferred, assignments)
		if choice.Title == "" {
			logger.Error("category assignment failed: no available pane",
				"mode_id", item.modeID,
				"category", item.category.String(),
			)
			return nil
		}

		assignments = append(assignments, ModeAssignment{
			ModeID:     item.modeID,
			PaneName:   choice.Title,
			AgentType:  string(choice.Type),
			Status:     AssignmentPending,
			AssignedAt: now,
		})

		logger.Info("ensemble assignment decided",
			"strategy", "category_affinity",
			"mode_id", item.modeID,
			"category", item.category.String(),
			"preferred_types", preferred,
			"pane", choice.Title,
			"agent_type", string(choice.Type),
			"fallback", fallback,
			"fallback_reason", reason,
		)
	}

	modeIDs := make([]string, 0, len(items))
	for _, item := range items {
		modeIDs = append(modeIDs, item.modeID)
	}
	if err := ValidateAssignments(assignments, modeIDs); err != nil {
		logger.Error("category assignment failed validation", "error", err)
		return nil
	}

	return assignments
}

// AssignExplicit assigns modes based on explicit user-specified mapping.
// Specs are formatted as "mode:agent" entries (comma-separated).
func AssignExplicit(specs []string, panes []tmux.Pane) ([]ModeAssignment, error) {
	expanded := expandSpecs(specs)
	if len(expanded) == 0 {
		return nil, errors.New("explicit assignment requires at least one mapping")
	}

	orderedPanes := sortAssignablePanes(panes)
	if len(expanded) > len(orderedPanes) {
		return nil, fmt.Errorf("explicit assignment requires %d panes, only %d available", len(expanded), len(orderedPanes))
	}

	byType := groupPanesByType(orderedPanes)
	modeToAgent := make(map[string]string, len(expanded))

	for _, spec := range expanded {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid assignment %q: expected mode:agent", spec)
		}
		modeID := normalizeModeKey(parts[0])
		if modeID == "" {
			return nil, fmt.Errorf("invalid assignment %q: empty mode", spec)
		}
		agentType := strings.TrimSpace(parts[1])
		if agentType == "" {
			return nil, fmt.Errorf("invalid assignment %q: empty agent type", spec)
		}
		if _, exists := modeToAgent[modeID]; exists {
			return nil, fmt.Errorf("duplicate assignment for mode %q", modeID)
		}
		modeToAgent[modeID] = agentType
	}

	modeIDs := make([]string, 0, len(modeToAgent))
	for modeID := range modeToAgent {
		modeIDs = append(modeIDs, modeID)
	}
	sort.Strings(modeIDs)

	assignments := make([]ModeAssignment, 0, len(modeIDs))
	now := time.Now().UTC()
	for _, modeID := range modeIDs {
		agentType := modeToAgent[modeID]
		choice, _, _ := pickAvailablePaneWithReason(byType, []string{agentType}, assignments)
		if choice.Title == "" {
			return nil, fmt.Errorf("no available pane for mode %q with agent type %q", modeID, agentType)
		}

		assignments = append(assignments, ModeAssignment{
			ModeID:     modeID,
			PaneName:   choice.Title,
			AgentType:  string(choice.Type),
			Status:     AssignmentPending,
			AssignedAt: now,
		})
		slog.Default().Info("ensemble assignment decided",
			"strategy", "explicit",
			"mode_id", modeID,
			"pane", choice.Title,
			"agent_type", string(choice.Type),
		)
	}

	if err := ValidateAssignments(assignments, modeIDs); err != nil {
		return nil, err
	}

	return assignments, nil
}

// groupPanesByType groups panes by agent type with deterministic ordering per type.
func groupPanesByType(panes []tmux.Pane) map[string][]tmux.Pane {
	result := make(map[string][]tmux.Pane)
	for _, pane := range panes {
		if !isAssignablePane(pane) {
			continue
		}
		agentType := string(pane.Type)
		result[agentType] = append(result[agentType], pane)
	}
	for agentType := range result {
		sort.SliceStable(result[agentType], func(i, j int) bool {
			return paneLess(result[agentType][i], result[agentType][j])
		})
	}
	return result
}

// pickAvailablePane selects an unused pane based on preferred types.
func pickAvailablePane(byType map[string][]tmux.Pane, preferred []string, used []ModeAssignment) tmux.Pane {
	choice, _, _ := pickAvailablePaneWithReason(byType, preferred, used)
	return choice
}

// ValidateAssignments checks assignments for determinism and configuration issues.
func ValidateAssignments(assignments []ModeAssignment, modes []string) error {
	normalized, err := normalizeModeKeys(modes)
	if err != nil {
		return err
	}
	if len(assignments) != len(normalized) {
		return fmt.Errorf("assignment count mismatch: %d assignments for %d modes", len(assignments), len(normalized))
	}

	modeCounts := make(map[string]int, len(normalized))
	for _, modeID := range normalized {
		modeCounts[modeID]++
	}

	paneCounts := make(map[string]int, len(assignments))
	for _, assignment := range assignments {
		modeID := normalizeModeKey(assignment.ModeID)
		if modeID == "" {
			return errors.New("assignment missing mode ID")
		}
		if assignment.PaneName == "" {
			return fmt.Errorf("assignment for mode %q missing pane name", assignment.ModeID)
		}
		if assignment.AgentType == "" {
			return fmt.Errorf("assignment for mode %q missing agent type", assignment.ModeID)
		}
		if assignment.Status == "" {
			return fmt.Errorf("assignment for mode %q missing status", assignment.ModeID)
		}

		if _, ok := modeCounts[modeID]; !ok {
			return fmt.Errorf("assignment for unknown mode %q", assignment.ModeID)
		}
		modeCounts[modeID]--
		if modeCounts[modeID] < 0 {
			return fmt.Errorf("mode %q assigned more than once", assignment.ModeID)
		}

		paneCounts[assignment.PaneName]++
		if paneCounts[assignment.PaneName] > 1 {
			return fmt.Errorf("pane %q assigned more than once", assignment.PaneName)
		}
	}

	for modeID, count := range modeCounts {
		if count != 0 {
			return fmt.Errorf("mode %q not assigned", modeID)
		}
	}

	return nil
}

type modeItem struct {
	modeID   string
	category ModeCategory
}

func resolveModeItems(modes []string, catalog *ModeCatalog) ([]modeItem, error) {
	if catalog == nil {
		return nil, errors.New("mode catalog is required for category assignment")
	}

	items := make([]modeItem, 0, len(modes))
	for _, raw := range modes {
		modeID, mode, err := resolveMode(raw, catalog)
		if err != nil {
			return nil, err
		}
		items = append(items, modeItem{
			modeID:   modeID,
			category: mode.Category,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].modeID < items[j].modeID
	})

	return items, nil
}

func resolveMode(raw string, catalog *ModeCatalog) (string, *ReasoningMode, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil, errors.New("mode key is empty")
	}
	normalized := normalizeModeKey(key)

	if catalog == nil {
		return normalized, nil, nil
	}

	if mode := catalog.GetMode(normalized); mode != nil {
		return mode.ID, mode, nil
	}

	mode, err := resolveModeByCode(key, catalog)
	if err != nil {
		return "", nil, err
	}
	if mode != nil {
		return mode.ID, mode, nil
	}

	return "", nil, fmt.Errorf("mode %q not found in catalog", key)
}

func resolveModeByCode(code string, catalog *ModeCatalog) (*ReasoningMode, error) {
	category, index, ok := parseModeCode(code)
	if !ok {
		return nil, nil
	}

	modes := catalog.ListByCategory(category)
	if index < 1 || index > len(modes) {
		return nil, fmt.Errorf("mode code %q out of range for category %s", code, category.String())
	}

	modeID := modes[index-1].ID
	return catalog.GetMode(modeID), nil
}

func parseModeCode(code string) (ModeCategory, int, bool) {
	code = strings.TrimSpace(code)
	if len(code) < 2 {
		return "", 0, false
	}

	letter := unicode.ToUpper(rune(code[0]))
	category, ok := CategoryFromLetter(string(letter))
	if !ok {
		return "", 0, false
	}

	index, err := strconv.Atoi(code[1:])
	if err != nil {
		return "", 0, false
	}
	return category, index, true
}

func normalizeModeKeys(modes []string) ([]string, error) {
	result := make([]string, 0, len(modes))
	for _, mode := range modes {
		key := normalizeModeKey(mode)
		if key == "" {
			return nil, errors.New("mode key cannot be empty")
		}
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeModeKey(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func expandSpecs(specs []string) []string {
	var expanded []string
	for _, spec := range specs {
		parts := strings.Split(spec, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			expanded = append(expanded, part)
		}
	}
	return expanded
}

func sortAssignablePanes(panes []tmux.Pane) []tmux.Pane {
	result := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		if !isAssignablePane(pane) {
			continue
		}
		result = append(result, pane)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return paneLess(result[i], result[j])
	})
	return result
}

func isAssignablePane(pane tmux.Pane) bool {
	if pane.Type == tmux.AgentUser {
		return false
	}
	if pane.Title == "" {
		return false
	}
	return true
}

func paneLess(a, b tmux.Pane) bool {
	ai := paneIndex(a)
	bi := paneIndex(b)
	if ai != bi {
		return ai < bi
	}
	if a.Index != b.Index {
		return a.Index < b.Index
	}
	return a.Title < b.Title
}

func paneIndex(pane tmux.Pane) int {
	if pane.NTMIndex > 0 {
		return pane.NTMIndex
	}
	return pane.Index
}

func pickAvailablePaneWithReason(byType map[string][]tmux.Pane, preferred []string, used []ModeAssignment) (tmux.Pane, bool, string) {
	usedPanes := make(map[string]bool, len(used))
	for _, assignment := range used {
		if assignment.PaneName != "" {
			usedPanes[assignment.PaneName] = true
		}
	}

	// Preferred types first
	for _, agentType := range preferred {
		for _, pane := range byType[agentType] {
			if !usedPanes[pane.Title] {
				return pane, false, ""
			}
		}
	}

	// Fallback to any available pane (deterministic order by type)
	types := make([]string, 0, len(byType))
	for agentType := range byType {
		types = append(types, agentType)
	}
	sort.Strings(types)
	for _, agentType := range types {
		for _, pane := range byType[agentType] {
			if !usedPanes[pane.Title] {
				return pane, true, fmt.Sprintf("preferred panes unavailable; fell back to %s", agentType)
			}
		}
	}

	return tmux.Pane{}, true, "no panes available"
}
