// Package persona provides configuration and management for AI agent personas.
// Personas define agent characteristics including agent type, model, system prompts,
// and behavioral settings.
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
)

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Persona defines the configuration for an agent persona.
type Persona struct {
	Name         string   `toml:"name"`
	Description  string   `toml:"description"`
	AgentType    string   `toml:"agent_type"`    // claude, codex, gemini
	Model        string   `toml:"model"`         // Model alias or full name
	SystemPrompt string   `toml:"system_prompt"` // System prompt to inject
	Temperature  *float64 `toml:"temperature,omitempty"`
	ContextFiles []string `toml:"context_files,omitempty"` // Globs of files to include in context
	Tags         []string `toml:"tags,omitempty"`

	// FocusPatterns are glob patterns indicating which files this persona "owns".
	// Used for routing hints, dashboard display, and alert triggers.
	FocusPatterns []string `toml:"focus_patterns,omitempty"`

	// Extends specifies a parent persona to inherit from.
	// Child settings override parent settings.
	Extends string `toml:"extends,omitempty"`

	// SystemPromptAppend is appended to the parent's system prompt when extending.
	SystemPromptAppend string `toml:"system_prompt_append,omitempty"`

	// resolved tracks if inheritance has been resolved
	resolved bool
}

// PersonaSet defines a named group of personas for quick spawning.
type PersonaSet struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description,omitempty"`
	Personas    []string `toml:"personas"` // References to persona names
}

// PersonasConfig holds a collection of persona definitions.
type PersonasConfig struct {
	Personas    []Persona    `toml:"personas"`
	PersonaSets []PersonaSet `toml:"persona_sets,omitempty"`
}

// Validate checks if the persona set configuration is valid.
func (s *PersonaSet) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("persona set name is required")
	}
	if !nameRegex.MatchString(strings.TrimSpace(s.Name)) {
		return fmt.Errorf("persona set name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", s.Name)
	}
	if len(s.Personas) == 0 {
		return fmt.Errorf("persona set %q must contain at least one persona", s.Name)
	}
	for i, name := range s.Personas {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("persona set %q has empty persona reference at index %d", s.Name, i)
		}
	}
	return nil
}

// AgentTypeFlag returns the NTM flag for this persona's agent type.
// e.g., "claude" -> "cc", "openai-codex" -> "cod", "google-gemini" -> "gmi"
func (p *Persona) AgentTypeFlag() string {
	switch agentpkg.AgentType(p.AgentType).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return "cc"
	case agentpkg.AgentTypeCodex:
		return "cod"
	case agentpkg.AgentTypeGemini:
		return "gmi"
	case agentpkg.AgentTypeCursor:
		return "cursor"
	case agentpkg.AgentTypeWindsurf:
		return "windsurf"
	case agentpkg.AgentTypeAider:
		return "aider"
	case agentpkg.AgentTypeOllama:
		return "ollama"
	default:
		return "cc" // Default to Claude
	}
}

// Validate checks if the persona configuration is valid.
func (p *Persona) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("persona name is required")
	}
	if !nameRegex.MatchString(p.Name) {
		return fmt.Errorf("persona name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", p.Name)
	}
	if strings.TrimSpace(p.AgentType) == "" && strings.TrimSpace(p.Extends) == "" {
		return fmt.Errorf("persona %q: agent_type is required", p.Name)
	}

	if strings.TrimSpace(p.AgentType) != "" {
		// Validate agent type aliases against the shared canonical resolver.
		switch agentpkg.AgentType(p.AgentType).Canonical() {
		case agentpkg.AgentTypeClaudeCode, agentpkg.AgentTypeCodex, agentpkg.AgentTypeGemini,
			agentpkg.AgentTypeCursor, agentpkg.AgentTypeWindsurf, agentpkg.AgentTypeAider, agentpkg.AgentTypeOllama:
			// valid
		default:
			return fmt.Errorf("persona %q: invalid agent_type %q", p.Name, p.AgentType)
		}
	}

	// Validate temperature if set
	if p.Temperature != nil {
		if *p.Temperature < 0 || *p.Temperature > 2 {
			return fmt.Errorf("persona %q: temperature must be between 0 and 2", p.Name)
		}
	}

	return nil
}

// Registry holds loaded personas and provides lookup functionality.
type Registry struct {
	personas    map[string]*Persona
	personaSets map[string]*PersonaSet
}

// NewRegistry creates a new empty persona registry.
func NewRegistry() *Registry {
	return &Registry{
		personas:    make(map[string]*Persona),
		personaSets: make(map[string]*PersonaSet),
	}
}

// Add adds a persona to the registry, overwriting any existing persona with the same name.
func (r *Registry) Add(p *Persona) {
	r.personas[strings.ToLower(p.Name)] = p
}

// Get retrieves a persona by name (case-insensitive).
func (r *Registry) Get(name string) (*Persona, bool) {
	p, ok := r.personas[strings.ToLower(name)]
	return p, ok
}

// List returns all personas in the registry.
func (r *Registry) List() []*Persona {
	result := make([]*Persona, 0, len(r.personas))
	for _, p := range r.personas {
		result = append(result, p)
	}
	return result
}

// AddSet adds a persona set to the registry.
func (r *Registry) AddSet(s *PersonaSet) {
	r.personaSets[strings.ToLower(s.Name)] = s
}

// GetSet retrieves a persona set by name (case-insensitive).
func (r *Registry) GetSet(name string) (*PersonaSet, bool) {
	s, ok := r.personaSets[strings.ToLower(name)]
	return s, ok
}

// ListSets returns all persona sets in the registry.
func (r *Registry) ListSets() []*PersonaSet {
	result := make([]*PersonaSet, 0, len(r.personaSets))
	for _, s := range r.personaSets {
		result = append(result, s)
	}
	return result
}

// ResolveInheritance resolves inheritance chains for all personas in the registry.
// Should be called after all personas are loaded.
func (r *Registry) ResolveInheritance() error {
	// Track resolution to detect cycles
	resolving := make(map[string]bool)

	var resolve func(name string) (*Persona, error)
	resolve = func(name string) (*Persona, error) {
		p, ok := r.Get(name)
		if !ok {
			return nil, fmt.Errorf("persona %q not found", name)
		}

		if p.resolved {
			return p, nil
		}

		if resolving[strings.ToLower(name)] {
			return nil, fmt.Errorf("circular inheritance detected: %s", name)
		}

		if p.Extends == "" {
			p.resolved = true
			return p, nil
		}

		resolving[strings.ToLower(name)] = true

		parent, err := resolve(p.Extends)
		if err != nil {
			return nil, fmt.Errorf("resolving parent %q for %q: %w", p.Extends, name, err)
		}

		// Merge parent into child (child overrides parent)
		merged := mergePersonas(parent, p)
		merged.resolved = true

		// Update registry with resolved persona
		r.personas[strings.ToLower(name)] = merged

		delete(resolving, strings.ToLower(name))
		return merged, nil
	}

	for name := range r.personas {
		if _, err := resolve(name); err != nil {
			return err
		}
	}

	return nil
}

// mergePersonas merges parent into child, with child values taking precedence.
func mergePersonas(parent, child *Persona) *Persona {
	merged := &Persona{
		Name:               child.Name,
		Description:        child.Description,
		AgentType:          child.AgentType,
		Model:              child.Model,
		SystemPrompt:       child.SystemPrompt,
		Temperature:        child.Temperature,
		Extends:            child.Extends,
		SystemPromptAppend: child.SystemPromptAppend,
	}

	// Deep copy slices to avoid aliasing with child
	if len(child.ContextFiles) > 0 {
		merged.ContextFiles = make([]string, len(child.ContextFiles))
		copy(merged.ContextFiles, child.ContextFiles)
	}
	if len(child.Tags) > 0 {
		merged.Tags = make([]string, len(child.Tags))
		copy(merged.Tags, child.Tags)
	}
	if len(child.FocusPatterns) > 0 {
		merged.FocusPatterns = make([]string, len(child.FocusPatterns))
		copy(merged.FocusPatterns, child.FocusPatterns)
	}

	// Inherit from parent if child doesn't specify
	if merged.Description == "" {
		merged.Description = parent.Description
	}
	if merged.AgentType == "" {
		merged.AgentType = parent.AgentType
	}
	if merged.Model == "" {
		merged.Model = parent.Model
	}
	if merged.Temperature == nil && parent.Temperature != nil {
		temp := *parent.Temperature
		merged.Temperature = &temp
	}

	// For system prompt: use child if specified, otherwise parent
	// Then append SystemPromptAppend if specified
	if merged.SystemPrompt == "" {
		merged.SystemPrompt = parent.SystemPrompt
	}
	if merged.SystemPromptAppend != "" {
		merged.SystemPrompt = merged.SystemPrompt + "\n\n" + merged.SystemPromptAppend
	}

	// Merge arrays (child extends parent)
	// Deep copy parent slices when inheriting to avoid aliasing
	if len(merged.ContextFiles) == 0 && len(parent.ContextFiles) > 0 {
		merged.ContextFiles = make([]string, len(parent.ContextFiles))
		copy(merged.ContextFiles, parent.ContextFiles)
	}
	if len(merged.Tags) == 0 {
		if len(parent.Tags) > 0 {
			merged.Tags = make([]string, len(parent.Tags))
			copy(merged.Tags, parent.Tags)
		}
	} else if len(parent.Tags) > 0 {
		// Merge tags, avoiding duplicates
		tagSet := make(map[string]bool)
		for _, t := range parent.Tags {
			tagSet[t] = true
		}
		for _, t := range child.Tags {
			tagSet[t] = true
		}
		merged.Tags = make([]string, 0, len(tagSet))
		for t := range tagSet {
			merged.Tags = append(merged.Tags, t)
		}
	}
	if len(merged.FocusPatterns) == 0 && len(parent.FocusPatterns) > 0 {
		merged.FocusPatterns = make([]string, len(parent.FocusPatterns))
		copy(merged.FocusPatterns, parent.FocusPatterns)
	}

	return merged
}

func undecodedPersonaFields(md toml.MetaData) []string {
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

// LoadFromFile loads personas from a TOML file.
func LoadFromFile(path string) (*PersonasConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg PersonasConfig
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing personas file %s: %w", path, err)
	}
	if fields := undecodedPersonaFields(md); len(fields) > 0 {
		return nil, fmt.Errorf("parsing personas file %s: unknown field(s): %s", path, strings.Join(fields, ", "))
	}

	seenPersonas := make(map[string]struct{}, len(cfg.Personas))
	// Validate all personas
	for i := range cfg.Personas {
		key := strings.ToLower(strings.TrimSpace(cfg.Personas[i].Name))
		if key != "" {
			if _, exists := seenPersonas[key]; exists {
				return nil, fmt.Errorf("duplicate persona name %q", cfg.Personas[i].Name)
			}
			seenPersonas[key] = struct{}{}
		}
		if err := cfg.Personas[i].Validate(); err != nil {
			return nil, err
		}
	}

	seenSets := make(map[string]struct{}, len(cfg.PersonaSets))
	for i := range cfg.PersonaSets {
		key := strings.ToLower(strings.TrimSpace(cfg.PersonaSets[i].Name))
		if key != "" {
			if _, exists := seenSets[key]; exists {
				return nil, fmt.Errorf("duplicate persona set name %q", cfg.PersonaSets[i].Name)
			}
			seenSets[key] = struct{}{}
		}
		if err := cfg.PersonaSets[i].Validate(); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

func userPathCandidates() []string {
	seen := make(map[string]struct{})
	candidates := make([]string, 0, 3)
	add := func(path string) {
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		candidates = append(candidates, path)
	}

	if env := os.Getenv("NTM_CONFIG"); env != "" {
		dir := filepath.Dir(env)
		// Expand ~/ in path if present.
		if strings.HasPrefix(dir, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, dir[2:])
			}
		}
		add(filepath.Join(dir, "personas.toml"))
	}

	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		add(filepath.Join(xdg, "ntm", "personas.toml"))
	}

	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config", "ntm", "personas.toml"))
	}

	return candidates
}

// DefaultUserPath returns the default user personas file path.
func DefaultUserPath() string {
	candidates := userPathCandidates()
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// DefaultProjectPath returns the default project personas file path.
func DefaultProjectPath() string {
	return ".ntm/personas.toml"
}

// LoadUserConfig loads the first available user personas config from the active
// candidate path list. If no user personas file exists, it returns nil, "", nil.
// If a candidate file exists but is invalid, it returns an error instead of
// silently falling through to a later path.
func LoadUserConfig() (*PersonasConfig, string, error) {
	for _, path := range userPathCandidates() {
		cfg, err := LoadFromFile(path)
		if err == nil {
			return cfg, path, nil
		}
		if os.IsNotExist(err) {
			continue
		}
		return nil, path, fmt.Errorf("loading user personas: %w", err)
	}
	return nil, "", nil
}

// LoadRegistry loads personas from all sources and returns a registry.
// Loading precedence: built-in -> user -> project (later sources override earlier).
func LoadRegistry(projectDir string) (*Registry, error) {
	registry := NewRegistry()

	// 1. Load built-in personas and sets
	for _, p := range BuiltinPersonas() {
		registry.Add(&p)
	}
	for _, s := range BuiltinPersonaSets() {
		registry.AddSet(&s)
	}

	// 2. Load user personas (missing is fine; invalid content is an error)
	if cfg, _, err := LoadUserConfig(); err == nil && cfg != nil {
		for i := range cfg.Personas {
			registry.Add(&cfg.Personas[i])
		}
		for i := range cfg.PersonaSets {
			registry.AddSet(&cfg.PersonaSets[i])
		}
	} else if err != nil {
		return nil, err
	}

	// 3. Load project personas (ignore file-not-found; invalid content is an error)
	if projectDir != "" {
		projectPath := filepath.Join(projectDir, DefaultProjectPath())
		if cfg, err := LoadFromFile(projectPath); err == nil {
			for i := range cfg.Personas {
				registry.Add(&cfg.Personas[i])
			}
			for i := range cfg.PersonaSets {
				registry.AddSet(&cfg.PersonaSets[i])
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading project personas: %w", err)
		}
	}

	// 4. Resolve inheritance chains
	if err := registry.ResolveInheritance(); err != nil {
		return nil, fmt.Errorf("resolving persona inheritance: %w", err)
	}

	if err := registry.ValidatePersonaSets(); err != nil {
		return nil, err
	}

	return registry, nil
}

// ValidatePersonaSets ensures persona sets only reference personas that exist in the merged registry.
func (r *Registry) ValidatePersonaSets() error {
	for _, set := range r.personaSets {
		for _, name := range set.Personas {
			if _, ok := r.Get(name); !ok {
				return fmt.Errorf("persona set %q references unknown persona %q", set.Name, name)
			}
		}
	}
	return nil
}

// BuiltinPersonas returns the default built-in persona definitions.
func BuiltinPersonas() []Persona {
	return []Persona{
		{
			Name:        "architect",
			Description: "Senior software architect focused on system design and high-level decisions",
			AgentType:   "claude",
			Model:       "opus",
			SystemPrompt: `You are a senior software architect. Your focus is on:
- System design and architecture decisions
- Code organization and module boundaries
- Design patterns and best practices
- Performance and scalability considerations
- Security architecture

When reviewing code, focus on structural issues rather than implementation details.
Propose refactoring strategies and architectural improvements.
Consider long-term maintainability and extensibility.`,
			Tags:          []string{"design", "review", "architecture"},
			FocusPatterns: []string{"*.md", "docs/**", "api/**", "pkg/**"},
		},
		{
			Name:        "implementer",
			Description: "Fast implementation engineer focused on writing working code quickly",
			AgentType:   "claude",
			Model:       "sonnet",
			SystemPrompt: `You are a fast implementation engineer. Your focus is on:
- Writing working code quickly and efficiently
- Following existing patterns in the codebase
- Implementing features to specification
- Writing clean, readable code
- Adding appropriate error handling

Prioritize getting things working over perfection.
Follow the existing code style and conventions.
Ask clarifying questions if requirements are unclear.`,
			Tags:          []string{"implementation", "coding", "fast"},
			FocusPatterns: []string{"internal/**", "cmd/**", "src/**"},
		},
		{
			Name:        "reviewer",
			Description: "Code reviewer focused on quality, bugs, and best practices",
			AgentType:   "claude",
			Model:       "sonnet",
			SystemPrompt: `You are a thorough code reviewer. Your focus is on:
- Finding bugs and logical errors
- Identifying security vulnerabilities
- Checking for edge cases and error handling
- Ensuring code follows best practices
- Reviewing test coverage

Be constructive in your feedback.
Explain the reasoning behind your suggestions.
Prioritize issues by severity.`,
			Tags:          []string{"review", "quality", "bugs"},
			FocusPatterns: []string{"**/*.go", "**/*.ts", "**/*.py"},
		},
		{
			Name:        "tester",
			Description: "QA engineer focused on testing and test coverage",
			AgentType:   "claude",
			Model:       "sonnet",
			SystemPrompt: `You are a QA engineer focused on testing. Your focus is on:
- Writing comprehensive test cases
- Identifying edge cases and boundary conditions
- Creating unit, integration, and e2e tests
- Improving test coverage
- Finding bugs through systematic testing

Think about what could go wrong.
Test both happy paths and error cases.
Consider performance and stress testing where appropriate.`,
			Tags:          []string{"testing", "qa", "quality"},
			FocusPatterns: []string{"**/*_test.go", "**/test_*.py", "**/*.test.ts", "tests/**"},
		},
		{
			Name:        "documenter",
			Description: "Technical writer focused on documentation and clarity",
			AgentType:   "claude",
			Model:       "sonnet",
			SystemPrompt: `You are a technical writer focused on documentation. Your focus is on:
- Writing clear, concise documentation
- Adding helpful code comments
- Creating README files and guides
- Documenting APIs and interfaces
- Explaining complex concepts simply

Write for your audience - other developers.
Include examples where helpful.
Keep documentation up to date with code changes.`,
			Tags:          []string{"documentation", "writing", "clarity"},
			FocusPatterns: []string{"*.md", "docs/**", "README*"},
		},
	}
}

// BuiltinPersonaSets returns the default built-in persona set definitions.
func BuiltinPersonaSets() []PersonaSet {
	return []PersonaSet{
		{
			Name:        "backend-team",
			Description: "Full backend development team with architect, implementers, and tester",
			Personas:    []string{"architect", "implementer", "implementer", "tester"},
		},
		{
			Name:        "review-team",
			Description: "Code review focused team",
			Personas:    []string{"reviewer", "reviewer", "documenter"},
		},
		{
			Name:        "full-stack",
			Description: "Complete development team for full-stack work",
			Personas:    []string{"architect", "implementer", "implementer", "tester", "documenter"},
		},
		{
			Name:        "quick-impl",
			Description: "Fast implementation pair",
			Personas:    []string{"implementer", "implementer"},
		},
	}
}
