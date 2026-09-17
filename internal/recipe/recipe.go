// Package recipe provides session preset definitions (recipes) for NTM.
// Recipes define reusable session configurations specifying agent types,
// counts, and optional model, effort, and persona overrides.
package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
)

// Recipe defines a reusable session configuration preset.
type Recipe struct {
	Name        string      `toml:"name"`
	Description string      `toml:"description"`
	Agents      []AgentSpec `toml:"agents"`
	Source      string      `toml:"-"` // "builtin", "user", "project" - set at load time
}

// AgentSpec defines an agent configuration within a recipe.
type AgentSpec struct {
	Type            string `toml:"type"`                       // cc, cod, gmi, grok, etc.
	Count           int    `toml:"count"`                      // Number of agents
	Model           string `toml:"model,omitempty"`            // Optional model override
	ReasoningEffort string `toml:"reasoning_effort,omitempty"` // Optional reasoning-effort override
	Persona         string `toml:"persona,omitempty"`          // Optional persona name
}

// TotalAgents returns the total number of agents in the recipe.
func (r *Recipe) TotalAgents() int {
	total := 0
	for _, a := range r.Agents {
		total += a.Count
	}
	return total
}

// AgentCounts returns a map of agent type to count.
func (r *Recipe) AgentCounts() map[string]int {
	counts := make(map[string]int)
	for _, a := range r.Agents {
		agentType := strings.TrimSpace(a.Type)
		if canonical, err := normalizeRecipeAgentType(agentType); err == nil {
			agentType = canonical
		}
		counts[agentType] += a.Count
	}
	return counts
}

// recipesFile represents the structure of a recipes TOML file.
type recipesFile struct {
	Recipes []Recipe `toml:"recipes"`
}

func undecodedRecipeFields(md toml.MetaData) []string {
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

// builtinRecipes returns the default built-in recipes.
func builtinRecipes() []Recipe {
	return []Recipe{
		{
			Name:        "quick-claude",
			Description: "Quick start with 2 Claude agents",
			Agents: []AgentSpec{
				{Type: "cc", Count: 2},
			},
			Source: "builtin",
		},
		{
			Name:        "full-stack",
			Description: "Full-stack team: 3 Claude, 2 Codex, 1 Gemini",
			Agents: []AgentSpec{
				{Type: "cc", Count: 3},
				{Type: "cod", Count: 2},
				{Type: "gmi", Count: 1},
			},
			Source: "builtin",
		},
		{
			Name:        "minimal",
			Description: "Minimal setup with 1 Claude agent",
			Agents: []AgentSpec{
				{Type: "cc", Count: 1},
			},
			Source: "builtin",
		},
		{
			Name:        "codex-heavy",
			Description: "Codex-focused: 4 Codex, 1 Claude for review",
			Agents: []AgentSpec{
				{Type: "cod", Count: 4},
				{Type: "cc", Count: 1},
			},
			Source: "builtin",
		},
		{
			Name:        "balanced",
			Description: "Balanced team: 2 of each agent type",
			Agents: []AgentSpec{
				{Type: "cc", Count: 2},
				{Type: "cod", Count: 2},
				{Type: "gmi", Count: 2},
			},
			Source: "builtin",
		},
		{
			Name:        "review-team",
			Description: "Code review setup: 1 writer, 2 reviewers",
			Agents: []AgentSpec{
				{Type: "cod", Count: 1, Model: "gpt4"},
				{Type: "cc", Count: 2, Model: "sonnet"},
			},
			Source: "builtin",
		},
	}
}

// Loader loads recipes from multiple sources with proper precedence.
type Loader struct {
	// UserConfigDir is the user config directory (default: ~/.config/ntm)
	UserConfigDir string
	// ProjectDir is the current project directory (for .ntm/recipes.toml)
	ProjectDir string
}

// NewLoader creates a new recipe loader with default paths.
func NewLoader() *Loader {
	userConfigDir := defaultUserConfigDir()
	projectDir, _ := os.Getwd()
	return &Loader{
		UserConfigDir: userConfigDir,
		ProjectDir:    projectDir,
	}
}

// defaultUserConfigDir returns the default user config directory.
func defaultUserConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ntm")
}

// LoadAll loads recipes from all sources with proper precedence.
// Order: builtin < user (~/.config/ntm/recipes.toml) < project (.ntm/recipes.toml)
// Later sources override earlier ones by name.
func (l *Loader) LoadAll() ([]Recipe, error) {
	recipes := make(map[string]Recipe)

	// 1. Load builtin recipes
	for _, r := range builtinRecipes() {
		recipes[r.Name] = r
	}

	// 2. Load user recipes
	userPath := filepath.Join(l.UserConfigDir, "recipes.toml")
	if userRecipes, err := loadFromFile(userPath, "user"); err == nil {
		for _, r := range userRecipes {
			recipes[r.Name] = r
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// 3. Load project recipes (highest priority)
	projectPath := filepath.Join(l.ProjectDir, ".ntm", "recipes.toml")
	if projectRecipes, err := loadFromFile(projectPath, "project"); err == nil {
		for _, r := range projectRecipes {
			recipes[r.Name] = r
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Convert map to slice, preserving a reasonable order
	result := make([]Recipe, 0, len(recipes))

	// Add builtins first (in original order)
	for _, r := range builtinRecipes() {
		if recipe, ok := recipes[r.Name]; ok {
			result = append(result, recipe)
			delete(recipes, r.Name)
		}
	}

	// Add remaining (user/project-defined) recipes
	for _, r := range recipes {
		result = append(result, r)
	}

	return result, nil
}

// Get returns a recipe by name, or nil if not found.
func (l *Loader) Get(name string) (*Recipe, error) {
	recipes, err := l.LoadAll()
	if err != nil {
		return nil, err
	}

	for _, r := range recipes {
		if strings.EqualFold(r.Name, name) {
			return &r, nil
		}
	}

	return nil, fmt.Errorf("recipe not found: %s", name)
}

// loadFromFile loads recipes from a TOML file.
func loadFromFile(path, source string) ([]Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var rf recipesFile
	md, err := toml.Decode(string(data), &rf)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if fields := undecodedRecipeFields(md); len(fields) > 0 {
		return nil, fmt.Errorf("parsing %s: unknown field(s): %s", path, strings.Join(fields, ", "))
	}

	// Set source for all loaded recipes
	for i := range rf.Recipes {
		rf.Recipes[i].Source = source
		if err := normalizeRecipe(&rf.Recipes[i]); err != nil {
			return nil, fmt.Errorf("validating %s recipe[%d]: %w", path, i, err)
		}
		if err := rf.Recipes[i].Validate(); err != nil {
			return nil, fmt.Errorf("validating %s recipe[%d]: %w", path, i, err)
		}
	}

	return rf.Recipes, nil
}

// BuiltinNames returns the names of all builtin recipes.
func BuiltinNames() []string {
	builtin := builtinRecipes()
	names := make([]string, len(builtin))
	for i, r := range builtin {
		names[i] = r.Name
	}
	return names
}

// ValidateAgentSpec validates an agent specification.
func ValidateAgentSpec(spec AgentSpec) error {
	if strings.TrimSpace(spec.Type) == "" {
		return fmt.Errorf("agent type is required")
	}
	if _, err := normalizeRecipeAgentType(spec.Type); err != nil {
		return err
	}
	if spec.Count < 1 {
		return fmt.Errorf("agent count must be at least 1, got %d", spec.Count)
	}
	if spec.Count > 20 {
		return fmt.Errorf("agent count too high: %d (max 20)", spec.Count)
	}
	return nil
}

func normalizeRecipe(recipe *Recipe) error {
	for i := range recipe.Agents {
		if err := normalizeRecipeAgentSpec(&recipe.Agents[i]); err != nil {
			return fmt.Errorf("agent[%d]: %w", i, err)
		}
	}
	return nil
}

func normalizeRecipeAgentSpec(spec *AgentSpec) error {
	spec.Type = strings.TrimSpace(spec.Type)
	spec.Model = strings.TrimSpace(spec.Model)
	spec.ReasoningEffort = strings.TrimSpace(spec.ReasoningEffort)
	spec.Persona = strings.TrimSpace(spec.Persona)

	canonical, err := normalizeRecipeAgentType(spec.Type)
	if err != nil {
		return err
	}
	spec.Type = canonical
	return nil
}

func normalizeRecipeAgentType(raw string) (string, error) {
	switch agentpkg.AgentType(raw).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return string(agentpkg.AgentTypeClaudeCode), nil
	case agentpkg.AgentTypeCodex:
		return string(agentpkg.AgentTypeCodex), nil
	case agentpkg.AgentTypeGemini:
		return string(agentpkg.AgentTypeGemini), nil
	case agentpkg.AgentTypeAntigravity:
		return string(agentpkg.AgentTypeAntigravity), nil
	case agentpkg.AgentTypeGrok:
		return string(agentpkg.AgentTypeGrok), nil
	case agentpkg.AgentTypeCursor:
		return string(agentpkg.AgentTypeCursor), nil
	case agentpkg.AgentTypeWindsurf:
		return string(agentpkg.AgentTypeWindsurf), nil
	case agentpkg.AgentTypeAider:
		return string(agentpkg.AgentTypeAider), nil
	case agentpkg.AgentTypeOmp:
		return string(agentpkg.AgentTypeOmp), nil
	case agentpkg.AgentTypeOllama:
		return string(agentpkg.AgentTypeOllama), nil
	default:
		return "", fmt.Errorf("unsupported recipe agent type %q", strings.TrimSpace(raw))
	}
}

// Validate validates a recipe.
func (r *Recipe) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("recipe name is required")
	}
	if len(r.Agents) == 0 {
		return fmt.Errorf("recipe must have at least one agent")
	}
	for _, spec := range r.Agents {
		if err := ValidateAgentSpec(spec); err != nil {
			return fmt.Errorf("in recipe %q: %w", r.Name, err)
		}
	}
	if r.TotalAgents() > 50 {
		return fmt.Errorf("recipe %q has too many agents: %d (max 50)", r.Name, r.TotalAgents())
	}
	return nil
}
