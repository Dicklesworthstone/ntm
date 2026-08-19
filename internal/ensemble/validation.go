package ensemble

import (
	"fmt"
	"sort"
	"strings"
)

// ValidationSeverity indicates how serious a validation issue is.
type ValidationSeverity string

const (
	SeverityError   ValidationSeverity = "error"
	SeverityWarning ValidationSeverity = "warning"
	SeverityInfo    ValidationSeverity = "info"
)

// ValidationIssue represents a single validation finding.
type ValidationIssue struct {
	Code        string             `json:"code"`
	Severity    ValidationSeverity `json:"severity"`
	Message     string             `json:"message"`
	Field       string             `json:"field,omitempty"`
	Value       interface{}        `json:"value,omitempty"`
	Hint        string             `json:"hint,omitempty"`
	Suggestions []string           `json:"suggestions,omitempty"`
}

// ValidationReport aggregates validation findings.
type ValidationReport struct {
	Errors   []ValidationIssue `json:"errors"`
	Warnings []ValidationIssue `json:"warnings"`
	Infos    []ValidationIssue `json:"infos"`
}

// NewValidationReport creates an empty report with non-nil slices.
func NewValidationReport() *ValidationReport {
	return &ValidationReport{
		Errors:   []ValidationIssue{},
		Warnings: []ValidationIssue{},
		Infos:    []ValidationIssue{},
	}
}

// Error returns a summary error if any errors are present.
func (r *ValidationReport) Error() error {
	if r == nil || len(r.Errors) == 0 {
		return nil
	}
	first := issueString(r.Errors[0])
	if len(r.Errors) == 1 {
		return fmt.Errorf("%s", first)
	}
	return fmt.Errorf("%s (and %d more)", first, len(r.Errors)-1)
}

// Merge appends findings from another report.
func (r *ValidationReport) Merge(other *ValidationReport) {
	if r == nil || other == nil {
		return
	}
	r.Errors = append(r.Errors, other.Errors...)
	r.Warnings = append(r.Warnings, other.Warnings...)
	r.Infos = append(r.Infos, other.Infos...)
}

func (r *ValidationReport) add(issue ValidationIssue) {
	if r == nil {
		return
	}
	switch issue.Severity {
	case SeverityWarning:
		r.Warnings = append(r.Warnings, issue)
	case SeverityInfo:
		r.Infos = append(r.Infos, issue)
	default:
		issue.Severity = SeverityError
		r.Errors = append(r.Errors, issue)
	}
}

func issueString(issue ValidationIssue) string {
	if issue.Field != "" {
		return fmt.Sprintf("%s: %s", issue.Field, issue.Message)
	}
	return issue.Message
}

// ValidateEnsemblePreset validates an ensemble preset, returning a report with
// actionable errors and warnings. Registry can be nil; extension validation
// will be skipped in that case.
func ValidateEnsemblePreset(preset *EnsemblePreset, catalog *ModeCatalog, registry *EnsembleRegistry) *ValidationReport {
	report := NewValidationReport()

	if preset == nil {
		report.add(ValidationIssue{
			Code:     "NIL_PRESET",
			Severity: SeverityError,
			Message:  "preset is nil",
		})
		return report
	}
	if catalog == nil {
		report.add(ValidationIssue{
			Code:     "MISSING_CATALOG",
			Severity: SeverityError,
			Message:  "mode catalog is required for validation",
		})
		return report
	}

	if preset.Name == "" {
		report.add(ValidationIssue{
			Code:     "MISSING_NAME",
			Severity: SeverityError,
			Field:    "name",
			Message:  "preset name is required",
		})
	} else if err := ValidateModeID(preset.Name); err != nil {
		report.add(ValidationIssue{
			Code:     "INVALID_NAME",
			Severity: SeverityError,
			Field:    "name",
			Message:  err.Error(),
		})
	}

	if preset.Extends != "" && preset.Extends == preset.Name {
		report.add(ValidationIssue{
			Code:     "EXTENDS_SELF",
			Severity: SeverityError,
			Field:    "extends",
			Message:  "preset cannot extend itself",
			Value:    preset.Extends,
		})
	}

	resolved := validateModeRefs(preset.Modes, catalog, preset.AllowAdvanced, report)
	uniqueCount := len(resolved)

	if uniqueCount > 0 && (uniqueCount < 2 || uniqueCount > 10) {
		report.add(ValidationIssue{
			Code:     "MODE_COUNT",
			Severity: SeverityError,
			Field:    "modes",
			Message:  fmt.Sprintf("mode count must be between 2 and 10 (got %d)", uniqueCount),
			Value:    uniqueCount,
		})
	}

	validateCategoryDiversity(resolved, catalog, report)
	validateSynthesisConfig(preset.Synthesis, catalog, preset.AllowAdvanced, report)
	validateBudgetConfig(preset.Budget, report)

	if preset.Extends != "" && registry == nil {
		report.add(ValidationIssue{
			Code:     "EXTENDS_UNCHECKED",
			Severity: SeverityWarning,
			Field:    "extends",
			Message:  "extension validation skipped (registry unavailable)",
			Value:    preset.Extends,
		})
	}

	if preset.Extends != "" && registry != nil && registry.Get(preset.Extends) == nil {
		report.add(ValidationIssue{
			Code:        "EXTENDS_NOT_FOUND",
			Severity:    SeverityError,
			Field:       "extends",
			Message:     fmt.Sprintf("extended preset %q not found", preset.Extends),
			Value:       preset.Extends,
			Suggestions: suggestPresetNames(preset.Extends, registry),
			Hint:        "Check ensemble preset names in your config or embedded presets",
		})
	}

	return report
}

// ValidateEnsemblePresets validates a list of presets as a registry-wide check.
// It includes extension cycle/depth validation in addition to per-preset checks.
func ValidateEnsemblePresets(presets []EnsemblePreset, catalog *ModeCatalog) *ValidationReport {
	report := NewValidationReport()
	if catalog == nil {
		report.add(ValidationIssue{
			Code:     "MISSING_CATALOG",
			Severity: SeverityError,
			Message:  "mode catalog is required for validation",
		})
		return report
	}

	seenNames := make(map[string]struct{}, len(presets))
	for i := range presets {
		name := presets[i].Name
		if name == "" {
			continue
		}
		if _, exists := seenNames[name]; exists {
			report.add(ValidationIssue{
				Code:     "DUPLICATE_PRESET",
				Severity: SeverityError,
				Field:    "name",
				Message:  fmt.Sprintf("duplicate preset name %q", name),
				Value:    name,
			})
			continue
		}
		seenNames[name] = struct{}{}
	}

	registry := NewEnsembleRegistry(presets, catalog)
	for i := range presets {
		report.Merge(ValidateEnsemblePreset(&presets[i], catalog, registry))
	}
	validateEnsembleExtensions(presets, registry, report)
	return report
}

func validateModeRefs(refs []ModeRef, catalog *ModeCatalog, allowAdvanced bool, report *ValidationReport) []string {
	if len(refs) == 0 {
		report.add(ValidationIssue{
			Code:     "NO_MODES",
			Severity: SeverityError,
			Field:    "modes",
			Message:  "preset must have at least one mode",
		})
		return nil
	}

	seen := make(map[string]struct{}, len(refs))
	resolved := make([]string, 0, len(refs))

	for i, ref := range refs {
		field := fmt.Sprintf("modes[%d]", i)
		if ref.ID != "" && ref.Code != "" {
			report.add(ValidationIssue{
				Code:     "MODE_REF_CONFLICT",
				Severity: SeverityError,
				Field:    field,
				Message:  "mode ref must specify id or code, not both",
				Value:    fmt.Sprintf("id=%q code=%q", ref.ID, ref.Code),
			})
			continue
		}
		if ref.ID == "" && ref.Code == "" {
			report.add(ValidationIssue{
				Code:     "MODE_REF_EMPTY",
				Severity: SeverityError,
				Field:    field,
				Message:  "mode ref must specify either id or code",
			})
			continue
		}

		modeID, err := resolveModeRef(ref, catalog, field, report)
		if err != nil {
			continue
		}
		if _, exists := seen[modeID]; exists {
			report.add(ValidationIssue{
				Code:     "DUPLICATE_MODE",
				Severity: SeverityError,
				Field:    field,
				Message:  fmt.Sprintf("duplicate mode %q", modeID),
				Value:    modeID,
			})
			continue
		}
		seen[modeID] = struct{}{}
		resolved = append(resolved, modeID)

		if !allowAdvanced {
			mode := catalog.GetMode(modeID)
			if mode != nil && mode.Tier != TierCore {
				report.add(ValidationIssue{
					Code:     "TIER_NOT_ALLOWED",
					Severity: SeverityError,
					Field:    field,
					Message:  fmt.Sprintf("mode %q is tier %q but allow_advanced is false", modeID, mode.Tier),
					Value:    mode.Tier,
					Hint:     "Enable allow_advanced to include advanced or experimental modes",
				})
			}
		}
	}

	return resolved
}

func validateModeIDs(modeIDs []string, catalog *ModeCatalog, allowAdvanced bool, report *ValidationReport) {
	if len(modeIDs) == 0 {
		report.add(ValidationIssue{
			Code:     "NO_MODES",
			Severity: SeverityError,
			Field:    "modes",
			Message:  "at least one mode is required",
		})
		return
	}

	seen := make(map[string]struct{}, len(modeIDs))
	for i, modeID := range modeIDs {
		field := fmt.Sprintf("modes[%d]", i)
		if modeID == "" {
			report.add(ValidationIssue{
				Code:     "MODE_REF_EMPTY",
				Severity: SeverityError,
				Field:    field,
				Message:  "mode id is empty",
			})
			continue
		}
		if _, exists := seen[modeID]; exists {
			report.add(ValidationIssue{
				Code:     "DUPLICATE_MODE",
				Severity: SeverityError,
				Field:    field,
				Message:  fmt.Sprintf("duplicate mode %q", modeID),
				Value:    modeID,
			})
			continue
		}
		seen[modeID] = struct{}{}

		mode := catalog.GetMode(modeID)
		if mode == nil {
			report.add(ValidationIssue{
				Code:        "MODE_ID_NOT_FOUND",
				Severity:    SeverityError,
				Field:       field,
				Message:     fmt.Sprintf("mode id %q not found", modeID),
				Value:       modeID,
				Suggestions: suggestModeIDs(modeID, catalog),
			})
			continue
		}
		if !allowAdvanced && mode.Tier != TierCore {
			report.add(ValidationIssue{
				Code:     "TIER_NOT_ALLOWED",
				Severity: SeverityError,
				Field:    field,
				Message:  fmt.Sprintf("mode %q is tier %q but allow_advanced is false", modeID, mode.Tier),
				Value:    mode.Tier,
				Hint:     "Enable allow_advanced to include advanced or experimental modes",
			})
		}
	}

	if len(seen) > 0 && (len(seen) < 2 || len(seen) > 10) {
		report.add(ValidationIssue{
			Code:     "MODE_COUNT",
			Severity: SeverityError,
			Field:    "modes",
			Message:  fmt.Sprintf("mode count must be between 2 and 10 (got %d)", len(seen)),
			Value:    len(seen),
		})
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	validateCategoryDiversity(ids, catalog, report)
}

func resolveModeRef(ref ModeRef, catalog *ModeCatalog, field string, report *ValidationReport) (string, error) {
	if ref.ID != "" {
		if catalog.GetMode(ref.ID) == nil {
			report.add(ValidationIssue{
				Code:        "MODE_ID_NOT_FOUND",
				Severity:    SeverityError,
				Field:       field,
				Message:     fmt.Sprintf("mode id %q not found", ref.ID),
				Value:       ref.ID,
				Suggestions: suggestModeIDs(ref.ID, catalog),
				Hint:        "Use a valid mode id from the catalog",
			})
			return "", fmt.Errorf("mode id not found")
		}
		return ref.ID, nil
	}

	code := strings.ToUpper(ref.Code)
	if !modeCodeRegex.MatchString(code) {
		report.add(ValidationIssue{
			Code:     "MODE_CODE_INVALID",
			Severity: SeverityError,
			Field:    field,
			Message:  fmt.Sprintf("invalid mode code %q", ref.Code),
			Value:    ref.Code,
			Hint:     "Mode codes must match format [A-L][0-9]+ (e.g., A1)",
		})
		return "", fmt.Errorf("mode code invalid")
	}
	mode := catalog.GetModeByCode(code)
	if mode == nil {
		report.add(ValidationIssue{
			Code:        "MODE_CODE_NOT_FOUND",
			Severity:    SeverityError,
			Field:       field,
			Message:     fmt.Sprintf("mode code %q not found", code),
			Value:       code,
			Suggestions: suggestModeCodes(code, catalog),
			Hint:        "Use a valid mode code from the catalog",
		})
		return "", fmt.Errorf("mode code not found")
	}
	return mode.ID, nil
}

func validateCategoryDiversity(modeIDs []string, catalog *ModeCatalog, report *ValidationReport) {
	if len(modeIDs) < 2 || catalog == nil {
		return
	}
	uniqueCats := make(map[ModeCategory]struct{})
	for _, modeID := range modeIDs {
		mode := catalog.GetMode(modeID)
		if mode == nil {
			continue
		}
		uniqueCats[mode.Category] = struct{}{}
	}
	if len(uniqueCats) == 1 {
		for cat := range uniqueCats {
			report.add(ValidationIssue{
				Code:     "CATEGORY_HOMOGENEOUS",
				Severity: SeverityWarning,
				Field:    "modes",
				Message:  fmt.Sprintf("all selected modes are from category %q; consider mixing categories", cat),
				Value:    cat,
			})
		}
	}
}

func validateSynthesisConfig(cfg SynthesisConfig, catalog *ModeCatalog, allowAdvanced bool, report *ValidationReport) {
	if cfg.Strategy == "" {
		return
	}

	strategy, err := ValidateOrMigrateStrategy(string(cfg.Strategy))
	if err != nil {
		report.add(ValidationIssue{
			Code:     "STRATEGY_INVALID",
			Severity: SeverityError,
			Field:    "synthesis.strategy",
			Message:  err.Error(),
			Value:    cfg.Strategy,
			Hint:     "Use one of the supported synthesis strategies",
		})
		return
	}

	info, err := GetStrategy(string(strategy))
	if err != nil {
		report.add(ValidationIssue{
			Code:     "STRATEGY_UNKNOWN",
			Severity: SeverityError,
			Field:    "synthesis.strategy",
			Message:  err.Error(),
			Value:    cfg.Strategy,
		})
		return
	}

	if info.RequiresAgent && info.SynthesizerMode == "" {
		report.add(ValidationIssue{
			Code:     "SYNTH_MODE_MISSING",
			Severity: SeverityError,
			Field:    "synthesis.strategy",
			Message:  fmt.Sprintf("strategy %q requires a synthesizer mode but none is configured", info.Name),
			Value:    cfg.Strategy,
		})
		return
	}

	if info.RequiresAgent && info.SynthesizerMode != "" && catalog != nil {
		mode := catalog.GetMode(info.SynthesizerMode)
		if mode == nil {
			report.add(ValidationIssue{
				Code:        "SYNTH_MODE_NOT_FOUND",
				Severity:    SeverityWarning,
				Field:       "synthesis.strategy",
				Message:     fmt.Sprintf("synthesizer mode %q not found in catalog", info.SynthesizerMode),
				Value:       info.SynthesizerMode,
				Suggestions: suggestModeIDs(info.SynthesizerMode, catalog),
				Hint:        "Add the synthesizer mode to the catalog or adjust the strategy mapping",
			})
			return
		}
		if !allowAdvanced && mode.Tier != TierCore {
			report.add(ValidationIssue{
				Code:     "SYNTH_MODE_TIER",
				Severity: SeverityWarning,
				Field:    "synthesis.strategy",
				Message:  fmt.Sprintf("synthesizer mode %q is tier %q but allow_advanced is false", mode.ID, mode.Tier),
				Value:    mode.Tier,
			})
		}
	}
}

func validateBudgetConfig(cfg BudgetConfig, report *ValidationReport) {
	if cfg.MaxTokensPerMode < 0 || cfg.MaxTotalTokens < 0 || cfg.SynthesisReserveTokens < 0 || cfg.ContextReserveTokens < 0 {
		report.add(ValidationIssue{
			Code:     "BUDGET_NEGATIVE",
			Severity: SeverityError,
			Field:    "budget",
			Message:  "budget values must be non-negative",
		})
		return
	}

	if cfg.MaxTokensPerMode > 0 && cfg.MaxTotalTokens > 0 && cfg.MaxTokensPerMode > cfg.MaxTotalTokens {
		report.add(ValidationIssue{
			Code:     "BUDGET_PER_MODE_EXCEEDS_TOTAL",
			Severity: SeverityError,
			Field:    "budget.max_tokens_per_mode",
			Message:  "per-mode budget exceeds total budget",
			Value:    cfg.MaxTokensPerMode,
			Hint:     "Reduce max_tokens_per_mode or increase max_total_tokens",
		})
	}

	if cfg.MaxTotalTokens > 0 {
		reserveTotal := cfg.SynthesisReserveTokens + cfg.ContextReserveTokens
		if reserveTotal > cfg.MaxTotalTokens {
			report.add(ValidationIssue{
				Code:     "BUDGET_RESERVE_EXCEEDS_TOTAL",
				Severity: SeverityError,
				Field:    "budget",
				Message:  "reserved tokens exceed total budget",
				Value:    reserveTotal,
				Hint:     "Reduce reserves or increase max_total_tokens",
			})
		}
	}

	const (
		maxReasonablePerMode = 200000
		maxReasonableTotal   = 1000000
	)

	if cfg.MaxTokensPerMode > maxReasonablePerMode {
		report.add(ValidationIssue{
			Code:     "BUDGET_PER_MODE_TOO_HIGH",
			Severity: SeverityError,
			Field:    "budget.max_tokens_per_mode",
			Message:  fmt.Sprintf("per-mode budget exceeds reasonable upper bound (%d)", maxReasonablePerMode),
			Value:    cfg.MaxTokensPerMode,
			Hint:     "Use a smaller per-mode token limit",
		})
	}

	if cfg.MaxTotalTokens > maxReasonableTotal {
		report.add(ValidationIssue{
			Code:     "BUDGET_TOTAL_TOO_HIGH",
			Severity: SeverityError,
			Field:    "budget.max_total_tokens",
			Message:  fmt.Sprintf("total budget exceeds reasonable upper bound (%d)", maxReasonableTotal),
			Value:    cfg.MaxTotalTokens,
			Hint:     "Use a smaller total token limit",
		})
	}
}

func validateEnsembleExtensions(presets []EnsemblePreset, registry *EnsembleRegistry, report *ValidationReport) {
	if registry == nil {
		return
	}

	extendsMap := make(map[string]string)
	for _, preset := range presets {
		if preset.Extends != "" {
			extendsMap[preset.Name] = preset.Extends
		}
	}

	for name, parent := range extendsMap {
		if parent == "" {
			continue
		}
		if registry.Get(parent) == nil {
			report.add(ValidationIssue{
				Code:        "EXTENDS_NOT_FOUND",
				Severity:    SeverityError,
				Field:       fmt.Sprintf("presets.%s.extends", name),
				Message:     fmt.Sprintf("extended preset %q not found", parent),
				Value:       parent,
				Suggestions: suggestPresetNames(parent, registry),
			})
		}
	}

	const maxDepth = 3
	visited := make(map[string]bool)

	var walk func(name string, depth int, stack map[string]bool)
	walk = func(name string, depth int, stack map[string]bool) {
		if depth > maxDepth {
			report.add(ValidationIssue{
				Code:     "EXTENDS_DEPTH",
				Severity: SeverityError,
				Field:    fmt.Sprintf("presets.%s.extends", name),
				Message:  fmt.Sprintf("extension depth exceeds %d", maxDepth),
				Value:    depth,
			})
			return
		}
		parent, ok := extendsMap[name]
		if !ok || parent == "" {
			visited[name] = true
			return
		}
		if stack[parent] {
			report.add(ValidationIssue{
				Code:     "EXTENDS_CYCLE",
				Severity: SeverityError,
				Field:    fmt.Sprintf("presets.%s.extends", name),
				Message:  "circular extension detected",
			})
			return
		}
		if visited[parent] {
			return
		}
		stack[parent] = true
		walk(parent, depth+1, stack)
		delete(stack, parent)
		visited[name] = true
	}

	for name := range extendsMap {
		if visited[name] {
			continue
		}
		stack := map[string]bool{name: true}
		walk(name, 1, stack)
	}
}

func suggestModeIDs(query string, catalog *ModeCatalog) []string {
	if catalog == nil {
		return nil
	}
	candidates := make([]string, 0, len(catalog.modes))
	for _, mode := range catalog.modes {
		candidates = append(candidates, mode.ID)
	}
	return closestMatches(strings.ToLower(query), candidates, 3)
}

func suggestModeCodes(query string, catalog *ModeCatalog) []string {
	if catalog == nil {
		return nil
	}
	candidates := make([]string, 0, len(catalog.byCode))
	for code := range catalog.byCode {
		candidates = append(candidates, code)
	}
	return closestMatches(strings.ToUpper(query), candidates, 3)
}

func suggestPresetNames(query string, registry *EnsembleRegistry) []string {
	if registry == nil {
		return nil
	}
	ensembles := registry.List()
	candidates := make([]string, 0, len(ensembles))
	for _, preset := range ensembles {
		candidates = append(candidates, preset.Name)
	}
	return closestMatches(strings.ToLower(query), candidates, 3)
}

func closestMatches(query string, candidates []string, limit int) []string {
	if query == "" || len(candidates) == 0 || limit <= 0 {
		return nil
	}
	normalized := strings.ToLower(query)

	type match struct {
		value    string
		distance int
	}
	matches := make([]match, 0, len(candidates))
	for _, candidate := range candidates {
		dist := editDistance(normalized, strings.ToLower(candidate))
		matches = append(matches, match{value: candidate, distance: dist})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].distance == matches[j].distance {
			return matches[i].value < matches[j].value
		}
		return matches[i].distance < matches[j].distance
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.value)
	}
	return out
}

// editDistance computes Levenshtein distance between two strings.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}

	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)

	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = minInt(del, ins, sub)
		}
		prev, curr = curr, prev
	}

	return prev[len(rb)]
}

func minInt(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
