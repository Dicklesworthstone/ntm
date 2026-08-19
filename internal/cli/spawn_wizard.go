// Package cli provides NTM command-line interface.
// spawn_wizard.go provides an interactive huh-based wizard for configuring agent spawning.
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// SpawnWizardResult holds the agent configuration produced by the interactive wizard.
type SpawnWizardResult struct {
	CCCount       int
	CodCount      int
	GmiCount      int
	AgyCount      int
	GrokCount     int
	CursorCount   int
	WindsurfCount int
	AiderCount    int
	OpencodeCount int
	OllamaCount   int
	Recipe        string // empty = no recipe
	Template      string // empty = no template
	AutoRestart   bool
	Confirmed     bool
}

func wizardDeferredSelection(result SpawnWizardResult) bool {
	return strings.TrimSpace(result.Recipe) != "" || strings.TrimSpace(result.Template) != ""
}

func wizardLaunchAgentSpecs(result SpawnWizardResult) AgentSpecs {
	if wizardDeferredSelection(result) {
		return nil
	}
	return wizardAgentSpecs(result)
}

func wizardAgentSpecs(result SpawnWizardResult) AgentSpecs {
	specs := AgentSpecs{}
	counts := []struct {
		agentType AgentType
		count     int
	}{
		{agentType: AgentTypeClaude, count: result.CCCount},
		{agentType: AgentTypeCodex, count: result.CodCount},
		{agentType: AgentTypeGemini, count: result.GmiCount},
		{agentType: AgentTypeAntigravity, count: result.AgyCount},
		{agentType: AgentTypeGrok, count: result.GrokCount},
		{agentType: AgentTypeCursor, count: result.CursorCount},
		{agentType: AgentTypeWindsurf, count: result.WindsurfCount},
		{agentType: AgentTypeAider, count: result.AiderCount},
		{agentType: AgentTypeOpencode, count: result.OpencodeCount},
		{agentType: AgentTypeOllama, count: result.OllamaCount},
	}
	for _, entry := range counts {
		if entry.count <= 0 {
			continue
		}
		specs = append(specs, AgentSpec{Type: entry.agentType, Count: entry.count})
	}
	return specs
}

func spawnWizardResultFromCounts(counts map[string]int) SpawnWizardResult {
	return SpawnWizardResult{
		CCCount:       counts["cc"],
		CodCount:      counts["cod"],
		GmiCount:      counts["gmi"],
		AgyCount:      counts["agy"],
		GrokCount:     counts["grok"],
		CursorCount:   counts["cursor"],
		WindsurfCount: counts["windsurf"],
		AiderCount:    counts["aider"],
		OpencodeCount: counts["oc"],
		OllamaCount:   counts["ollama"],
	}
}

func formatWizardAgentCountSummary(counts map[string]int) string {
	entries := []struct {
		key string
	}{
		{key: "cc"},
		{key: "cod"},
		{key: "gmi"},
		{key: "agy"},
		{key: "grok"},
		{key: "cursor"},
		{key: "windsurf"},
		{key: "aider"},
		{key: "oc"},
		{key: "ollama"},
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if count := counts[entry.key]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", entry.key, count))
		}
	}
	if len(parts) == 0 {
		return "no agents"
	}
	return strings.Join(parts, " ")
}

// runSpawnWizard presents an interactive huh form for configuring a spawn session.
// Returns the wizard result or an error if the user cancels.
func runSpawnWizard(sessionName string) (SpawnWizardResult, error) {
	if !isTTY() {
		return SpawnWizardResult{}, fmt.Errorf("interactive wizard requires a terminal (TTY)")
	}

	var result SpawnWizardResult

	// Step 1: Choose configuration method
	var configMethod string
	methodForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("How would you like to configure agents?").
				Description(fmt.Sprintf("Session: %s", sessionName)).
				Options(
					huh.NewOption("Manual — pick agent types and counts", "manual"),
					huh.NewOption("Recipe — use a preset configuration", "recipe"),
					huh.NewOption("Template — use a workflow coordination pattern", "template"),
				).
				Value(&configMethod),
		),
	).WithTheme(theme.HuhTheme())

	if err := methodForm.Run(); err != nil {
		return result, fmt.Errorf("wizard cancelled")
	}

	switch configMethod {
	case "recipe":
		return runRecipeWizard(sessionName)
	case "template":
		return runTemplateWizard(sessionName)
	default:
		return runManualWizard(sessionName)
	}
}

// runManualWizard lets the user pick agent types and counts interactively.
func runManualWizard(sessionName string) (SpawnWizardResult, error) {
	var result SpawnWizardResult

	var ccStr, codStr, gmiStr, agyStr, grokStr string
	var autoRestart bool

	agentForm := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Claude agents (cc)").
				Description("Number of Claude Code agents to spawn").
				Placeholder("0").
				Value(&ccStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Codex agents (cod)").
				Description("Number of OpenAI Codex agents to spawn").
				Placeholder("0").
				Value(&codStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Gemini agents (gmi) (legacy)").
				Description("Number of Google Gemini agents to spawn").
				Placeholder("0").
				Value(&gmiStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Antigravity agents (agy)").
				Description("Number of Antigravity agents to spawn (model pinned to Gemini 3.7 Flash (High))").
				Placeholder("0").
				Value(&agyStr).
				Validate(validateAgentCount),
			huh.NewInput().
				Title("Grok Build agents (grok)").
				Description("Number of Grok Build agents to launch").
				Placeholder("0").
				Value(&grokStr).
				Validate(validateAgentCount),
		).Title("Agent Configuration"),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable auto-restart?").
				Description("Automatically restart agents that crash").
				Value(&autoRestart),
		),
	).WithTheme(theme.HuhTheme())

	if err := agentForm.Run(); err != nil {
		return result, fmt.Errorf("wizard cancelled")
	}

	result.CCCount = parseCount(ccStr)
	result.CodCount = parseCount(codStr)
	result.GmiCount = parseCount(gmiStr)
	result.AgyCount = parseCount(agyStr)
	result.GrokCount = parseCount(grokStr)
	result.AutoRestart = autoRestart

	if result.CCCount+result.CodCount+result.GmiCount+result.AgyCount+result.GrokCount == 0 {
		return result, fmt.Errorf("no agents specified — at least one agent is required")
	}

	// Confirmation
	total := result.CCCount + result.CodCount + result.GmiCount + result.AgyCount + result.GrokCount
	summary := fmt.Sprintf("Spawn %d agent(s) in session %q:\n  Claude: %d, Codex: %d, Gemini: %d, Antigravity: %d, Grok Build: %d",
		total, sessionName, result.CCCount, result.CodCount, result.GmiCount, result.AgyCount, result.GrokCount)
	if result.AutoRestart {
		summary += "\n  Auto-restart: enabled"
	}

	var confirmed bool
	confirmForm := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Confirm spawn").
				Description(summary).
				Affirmative("Spawn").
				Negative("Cancel").
				Value(&confirmed),
		),
	).WithTheme(theme.HuhTheme())

	if err := confirmForm.Run(); err != nil || !confirmed {
		return result, fmt.Errorf("spawn cancelled")
	}

	result.Confirmed = true
	return result, nil
}

// runRecipeWizard lets the user select from available recipes.
func runRecipeWizard(sessionName string) (SpawnWizardResult, error) {
	var result SpawnWizardResult

	loader := recipe.NewLoader()
	names := recipe.BuiltinNames()

	if len(names) == 0 {
		return result, fmt.Errorf("no recipes available")
	}

	// Build options with descriptions
	options := make([]huh.Option[string], 0, len(names))
	for _, name := range names {
		r, err := loader.Get(name)
		if err != nil {
			continue
		}
		counts := r.AgentCounts()
		desc := fmt.Sprintf("%s (%s)", r.Description, formatWizardAgentCountSummary(counts))
		options = append(options, huh.NewOption(desc, name))
	}

	var selected string
	recipeForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a recipe").
				Description(fmt.Sprintf("Session: %s", sessionName)).
				Options(options...).
				Value(&selected),
		),
	).WithTheme(theme.HuhTheme())

	if err := recipeForm.Run(); err != nil {
		return result, fmt.Errorf("wizard cancelled")
	}

	r, err := loader.Get(selected)
	if err != nil {
		return result, err
	}
	counts := r.AgentCounts()
	result = spawnWizardResultFromCounts(counts)
	result.Recipe = selected
	result.Confirmed = true
	return result, nil
}

// runTemplateWizard lets the user select from available workflow templates.
func runTemplateWizard(sessionName string) (SpawnWizardResult, error) {
	var result SpawnWizardResult

	wfLoader := workflow.NewLoader()
	names := workflow.BuiltinNames()

	if len(names) == 0 {
		return result, fmt.Errorf("no templates available")
	}

	options := make([]huh.Option[string], 0, len(names))
	for _, name := range names {
		tmpl, err := wfLoader.Get(name)
		if err != nil {
			continue
		}
		counts := tmpl.AgentCounts()
		desc := fmt.Sprintf("%s — %s (%s)",
			tmpl.Description, tmpl.Coordination, formatWizardAgentCountSummary(counts))
		options = append(options, huh.NewOption(desc, name))
	}

	var selected string
	templateForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a workflow template").
				Description(fmt.Sprintf("Session: %s", sessionName)).
				Options(options...).
				Value(&selected),
		),
	).WithTheme(theme.HuhTheme())

	if err := templateForm.Run(); err != nil {
		return result, fmt.Errorf("wizard cancelled")
	}

	tmpl, err := wfLoader.Get(selected)
	if err != nil {
		return result, err
	}
	counts := tmpl.AgentCounts()
	result = spawnWizardResultFromCounts(counts)
	result.Template = selected
	result.Confirmed = true
	return result, nil
}

// validateAgentCount validates that input is a non-negative integer or empty.
func validateAgentCount(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a number")
	}
	if n < 0 {
		return fmt.Errorf("must be non-negative")
	}
	if n > 20 {
		return fmt.Errorf("max 20 agents per type")
	}
	return nil
}

// parseCount converts a string to int, returning 0 for empty strings.
func parseCount(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}
