// Package ensemble provides types and utilities for multi-agent reasoning ensembles.
// An ensemble orchestrates multiple AI agents using different reasoning modes
// to analyze questions from multiple perspectives, then synthesizes their outputs.
package ensemble

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ModeCategory represents the taxonomy categories (A-L) for reasoning modes.
// Categories group related reasoning approaches to help users understand
// which modes address which types of problems.
type ModeCategory string

const (
	// CategoryFormal covers deductive, mathematical, and logical reasoning.
	CategoryFormal ModeCategory = "Formal"
	// CategoryAmpliative covers inductive and abductive reasoning.
	CategoryAmpliative ModeCategory = "Ampliative"
	// CategoryUncertainty covers probabilistic and statistical reasoning.
	CategoryUncertainty ModeCategory = "Uncertainty"
	// CategoryVagueness covers fuzzy and approximate reasoning.
	CategoryVagueness ModeCategory = "Vagueness"
	// CategoryChange covers temporal and belief revision reasoning.
	CategoryChange ModeCategory = "Change"
	// CategoryCausal covers causal inference and counterfactual reasoning.
	CategoryCausal ModeCategory = "Causal"
	// CategoryPractical covers decision-making and planning reasoning.
	CategoryPractical ModeCategory = "Practical"
	// CategoryStrategic covers game-theoretic and adversarial reasoning.
	CategoryStrategic ModeCategory = "Strategic"
	// CategoryDialectical covers argumentation and discourse reasoning.
	CategoryDialectical ModeCategory = "Dialectical"
	// CategoryModal covers necessity, possibility, and deontic reasoning.
	CategoryModal ModeCategory = "Modal"
	// CategoryDomain covers domain-specific (legal, scientific, ethical) reasoning.
	CategoryDomain ModeCategory = "Domain"
	// CategoryMeta covers reasoning about reasoning itself.
	CategoryMeta ModeCategory = "Meta"
)

// String returns the category as a string.
func (c ModeCategory) String() string {
	return string(c)
}

// IsValid returns true if this is a known category.
func (c ModeCategory) IsValid() bool {
	switch c {
	case CategoryFormal, CategoryAmpliative, CategoryUncertainty, CategoryVagueness,
		CategoryChange, CategoryCausal, CategoryPractical, CategoryStrategic,
		CategoryDialectical, CategoryModal, CategoryDomain, CategoryMeta:
		return true
	default:
		return false
	}
}

// AllCategories returns all valid mode categories.
func AllCategories() []ModeCategory {
	return []ModeCategory{
		CategoryFormal, CategoryAmpliative, CategoryUncertainty, CategoryVagueness,
		CategoryChange, CategoryCausal, CategoryPractical, CategoryStrategic,
		CategoryDialectical, CategoryModal, CategoryDomain, CategoryMeta,
	}
}

// ModeTier represents the visibility/maturity tier of a reasoning mode.
// Tiers control which modes appear in default listings vs advanced views.
type ModeTier string

const (
	// TierCore modes are stable, well-tested, and shown by default.
	TierCore ModeTier = "core"
	// TierAdvanced modes are functional but may require more expertise.
	TierAdvanced ModeTier = "advanced"
	// TierExperimental modes are in development and may change.
	TierExperimental ModeTier = "experimental"
)

// IsValid returns true if this is a known tier.
func (t ModeTier) IsValid() bool {
	switch t {
	case TierCore, TierAdvanced, TierExperimental:
		return true
	default:
		return false
	}
}

// String returns the tier as a string.
func (t ModeTier) String() string {
	return string(t)
}

// CategoryLetter returns the single-letter code (A-L) for the category.
// This maps to the taxonomy: A=Formal, B=Ampliative, ..., L=Meta.
func (c ModeCategory) CategoryLetter() string {
	switch c {
	case CategoryFormal:
		return "A"
	case CategoryAmpliative:
		return "B"
	case CategoryUncertainty:
		return "C"
	case CategoryVagueness:
		return "D"
	case CategoryChange:
		return "E"
	case CategoryCausal:
		return "F"
	case CategoryPractical:
		return "G"
	case CategoryStrategic:
		return "H"
	case CategoryDialectical:
		return "I"
	case CategoryModal:
		return "J"
	case CategoryDomain:
		return "K"
	case CategoryMeta:
		return "L"
	default:
		return ""
	}
}

// CategoryFromLetter returns the ModeCategory for a given letter (A-L).
// Returns empty string and false if the letter is not valid.
func CategoryFromLetter(letter string) (ModeCategory, bool) {
	switch letter {
	case "A":
		return CategoryFormal, true
	case "B":
		return CategoryAmpliative, true
	case "C":
		return CategoryUncertainty, true
	case "D":
		return CategoryVagueness, true
	case "E":
		return CategoryChange, true
	case "F":
		return CategoryCausal, true
	case "G":
		return CategoryPractical, true
	case "H":
		return CategoryStrategic, true
	case "I":
		return CategoryDialectical, true
	case "J":
		return CategoryModal, true
	case "K":
		return CategoryDomain, true
	case "L":
		return CategoryMeta, true
	default:
		return "", false
	}
}

// ReasoningMode defines a named reasoning approach.
// Each mode represents a distinct way of analyzing a problem, with
// specific strengths, outputs, and failure modes.
type ReasoningMode struct {
	// ID is the unique identifier for this mode (e.g., "deductive", "bayesian").
	// Must be lowercase alphanumeric with optional hyphens.
	ID string `json:"id" toml:"id"`

	// Code is the taxonomy code (e.g., "A1", "B3") mapping to the category letter
	// and a numeric index within that category. Format: [A-L][0-9]+.
	Code string `json:"code" toml:"code"`

	// Name is the human-readable name (e.g., "Deductive Logic").
	Name string `json:"name" toml:"name"`

	// Category is the taxonomy category this mode belongs to.
	Category ModeCategory `json:"category" toml:"category"`

	// Tier indicates the maturity/visibility level (core, advanced, experimental).
	// Core modes are shown by default; advanced and experimental require opt-in.
	Tier ModeTier `json:"tier" toml:"tier"`

	// ShortDesc is a one-line description for listings (max 80 chars).
	ShortDesc string `json:"short_desc" toml:"short_desc"`

	// Description is the full explanation of this reasoning approach.
	Description string `json:"description" toml:"description"`

	// Outputs describes what this mode produces (e.g., "Proof or counterexample").
	Outputs string `json:"outputs" toml:"outputs"`

	// BestFor lists problem types where this mode excels.
	BestFor []string `json:"best_for" toml:"best_for"`

	// FailureModes lists common pitfalls when using this mode.
	FailureModes []string `json:"failure_modes" toml:"failure_modes"`

	// Differentiator explains what makes this mode unique vs similar modes.
	Differentiator string `json:"differentiator" toml:"differentiator"`

	// Icon is a single emoji or Nerd Font glyph for UI display.
	Icon string `json:"icon" toml:"icon"`

	// Color is the hex color code for UI display (e.g., "#cba6f7").
	Color string `json:"color" toml:"color"`

	// PreambleKey is the key to lookup the preamble template for this mode.
	// The preamble is injected into the agent prompt to set its reasoning approach.
	PreambleKey string `json:"preamble_key" toml:"preamble_key"`

	// Source indicates where this mode was loaded from (embedded, user, project).
	// Set at load time, not persisted in TOML.
	Source string `json:"source,omitempty" toml:"-"`
}

// Validate checks that the mode has all required fields and valid values.
func (m *ReasoningMode) Validate() error {
	if m.ID == "" {
		return errors.New("mode ID is required")
	}
	if err := ValidateModeID(m.ID); err != nil {
		return err
	}
	if m.Name == "" {
		return errors.New("mode name is required")
	}
	if !m.Category.IsValid() {
		return fmt.Errorf("invalid category %q", m.Category)
	}
	if m.ShortDesc == "" {
		return errors.New("mode short_desc is required")
	}
	if len(m.ShortDesc) > 80 {
		return fmt.Errorf("short_desc exceeds 80 characters (got %d)", len(m.ShortDesc))
	}
	if m.Code != "" {
		if err := ValidateModeCode(m.Code, m.Category); err != nil {
			return err
		}
	}
	if m.Tier != "" && !m.Tier.IsValid() {
		return fmt.Errorf("invalid tier %q: must be core, advanced, or experimental", m.Tier)
	}
	return nil
}

// ModeAssignment maps a mode to an agent pane.
// This tracks which agent is using which reasoning mode in an ensemble session.
type ModeAssignment struct {
	// ModeID references the ReasoningMode.ID.
	ModeID string `json:"mode_id"`

	// PaneName is the tmux pane identifier (e.g., "myproject__cc_1").
	PaneName string `json:"pane_name"`

	// AgentType is the type of agent in this pane (cc, cod, gmi).
	AgentType string `json:"agent_type"`

	// Status tracks the assignment lifecycle.
	Status AssignmentStatus `json:"status"`

	// OutputPath is where captured output will be stored.
	OutputPath string `json:"output_path,omitempty"`

	// AssignedAt is when this assignment was created.
	AssignedAt time.Time `json:"assigned_at"`

	// CompletedAt is when the agent finished (status = done).
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Error holds any error message if status = error.
	Error string `json:"error,omitempty"`
}

// AssignmentStatus tracks the lifecycle of a mode assignment.
type AssignmentStatus string

const (
	// AssignmentPending means the mode is queued but not yet sent to the agent.
	AssignmentPending AssignmentStatus = "pending"
	// AssignmentInjecting means the preamble is being sent to the agent.
	AssignmentInjecting AssignmentStatus = "injecting"
	// AssignmentActive means the agent is actively working with this mode.
	AssignmentActive AssignmentStatus = "active"
	// AssignmentDone means the agent has completed its analysis.
	AssignmentDone AssignmentStatus = "done"
	// AssignmentError means an error occurred during this assignment.
	AssignmentError AssignmentStatus = "error"
)

// String returns the status as a string.
func (s AssignmentStatus) String() string {
	return string(s)
}

// IsTerminal returns true if this is a final status (done or error).
func (s AssignmentStatus) IsTerminal() bool {
	return s == AssignmentDone || s == AssignmentError
}

// EnsembleStatus tracks the overall ensemble session state.
type EnsembleStatus string

const (
	// EnsembleSpawning means agents are being created.
	EnsembleSpawning EnsembleStatus = "spawning"
	// EnsembleInjecting means preambles are being sent to agents.
	EnsembleInjecting EnsembleStatus = "injecting"
	// EnsembleActive means agents are analyzing the question.
	EnsembleActive EnsembleStatus = "active"
	// EnsembleSynthesizing means outputs are being combined.
	EnsembleSynthesizing EnsembleStatus = "synthesizing"
	// EnsembleComplete means the ensemble run is finished.
	EnsembleComplete EnsembleStatus = "complete"
	// EnsembleError means an error occurred during the run.
	EnsembleError EnsembleStatus = "error"
)

// String returns the status as a string.
func (s EnsembleStatus) String() string {
	return string(s)
}

// IsTerminal returns true if this is a final status (complete or error).
func (s EnsembleStatus) IsTerminal() bool {
	return s == EnsembleComplete || s == EnsembleError
}

// EnsembleSession tracks a reasoning ensemble session.
// An ensemble session spawns multiple agents with different reasoning modes,
// captures their outputs, and synthesizes them into a combined analysis.
type EnsembleSession struct {
	// SessionName is the tmux session name hosting this ensemble.
	SessionName string `json:"session_name"`

	// Question is the user's question or problem being analyzed.
	Question string `json:"question"`

	// PresetUsed is the name of the preset used (if any).
	PresetUsed string `json:"preset_used,omitempty"`

	// Assignments maps modes to agent panes.
	Assignments []ModeAssignment `json:"assignments"`

	// Status is the overall ensemble state.
	Status EnsembleStatus `json:"status"`

	// SynthesisStrategy is how outputs will be combined.
	SynthesisStrategy SynthesisStrategy `json:"synthesis_strategy"`

	// CreatedAt is when the ensemble was started.
	CreatedAt time.Time `json:"created_at"`

	// SynthesizedAt is when synthesis completed.
	SynthesizedAt *time.Time `json:"synthesized_at,omitempty"`

	// SynthesisOutput is the final combined output.
	SynthesisOutput string `json:"synthesis_output,omitempty"`

	// Error holds the error message if status = error.
	Error string `json:"error,omitempty"`
}

// SynthesisStrategy defines how ensemble outputs are combined.
type SynthesisStrategy string

const (
	// StrategyManual performs mechanical merge only; no synthesizer agent.
	StrategyManual SynthesisStrategy = "manual"
	// StrategyAdversarial uses adversarial challenge/defense synthesis.
	StrategyAdversarial SynthesisStrategy = "adversarial"
	// StrategyConsensus combines outputs by finding agreement points.
	StrategyConsensus SynthesisStrategy = "consensus"
	// StrategyCreative synthesizes through creative recombination.
	StrategyCreative SynthesisStrategy = "creative"
	// StrategyAnalytical uses systematic analytical decomposition.
	StrategyAnalytical SynthesisStrategy = "analytical"
	// StrategyDeliberative uses structured deliberation to weigh tradeoffs.
	StrategyDeliberative SynthesisStrategy = "deliberative"
	// StrategyPrioritized ranks and selects outputs by priority/quality.
	StrategyPrioritized SynthesisStrategy = "prioritized"
	// StrategyDialectical synthesizes through agent-led thesis/antithesis debate.
	StrategyDialectical SynthesisStrategy = "dialectical"
	// StrategyMetaReasoning uses a meta-cognitive synthesizer agent.
	StrategyMetaReasoning SynthesisStrategy = "meta-reasoning"
	// StrategyVoting uses structured score/vote aggregation.
	StrategyVoting SynthesisStrategy = "voting"
	// StrategyArgumentation builds support/attack graph from outputs.
	StrategyArgumentation SynthesisStrategy = "argumentation-graph"
)

// allStrategies is the canonical ordered list of valid strategies.
var allStrategies = []SynthesisStrategy{
	StrategyManual, StrategyAdversarial, StrategyConsensus,
	StrategyCreative, StrategyAnalytical, StrategyDeliberative,
	StrategyPrioritized, StrategyDialectical, StrategyMetaReasoning,
	StrategyVoting, StrategyArgumentation,
}

// String returns the strategy as a string.
func (s SynthesisStrategy) String() string {
	return string(s)
}

// IsValid returns true if this is a known synthesis strategy.
func (s SynthesisStrategy) IsValid() bool {
	for _, valid := range allStrategies {
		if s == valid {
			return true
		}
	}
	return false
}

// ModeRef references a reasoning mode by ID or taxonomy code.
// It canonicalizes to a mode ID at load time for consistent internal usage.
type ModeRef struct {
	// ID is the mode identifier (e.g., "deductive"). Mutually exclusive with Code.
	ID string `json:"id,omitempty" toml:"id,omitempty" yaml:"id,omitempty"`

	// Code is the taxonomy code (e.g., "A1"). Mutually exclusive with ID.
	Code string `json:"code,omitempty" toml:"code,omitempty" yaml:"code,omitempty"`
}

// Resolve canonicalizes a ModeRef to a mode ID using the catalog.
// Returns the resolved mode ID or an error if the reference is invalid.
func (r ModeRef) Resolve(catalog *ModeCatalog) (string, error) {
	if r.ID != "" && r.Code != "" {
		return "", fmt.Errorf("mode ref must specify id or code, not both (id=%q, code=%q)", r.ID, r.Code)
	}
	if r.ID == "" && r.Code == "" {
		return "", errors.New("mode ref must specify either id or code")
	}
	if r.ID != "" {
		if catalog.GetMode(r.ID) == nil {
			return "", fmt.Errorf("mode id %q not found in catalog", r.ID)
		}
		return r.ID, nil
	}
	// Resolve by code
	mode := catalog.GetModeByCode(r.Code)
	if mode == nil {
		return "", fmt.Errorf("mode code %q not found in catalog", r.Code)
	}
	return mode.ID, nil
}

// String returns a human-readable representation.
func (r ModeRef) String() string {
	if r.ID != "" {
		return r.ID
	}
	return "code:" + r.Code
}

// ModeRefFromID creates a ModeRef from a mode ID.
func ModeRefFromID(id string) ModeRef {
	return ModeRef{ID: id}
}

// ModeRefFromCode creates a ModeRef from a taxonomy code.
func ModeRefFromCode(code string) ModeRef {
	return ModeRef{Code: code}
}

// ResolveModeRefs resolves a slice of ModeRefs to mode IDs.
// Returns an error if any ref is invalid or resolves to a duplicate.
func ResolveModeRefs(refs []ModeRef, catalog *ModeCatalog) ([]string, error) {
	seen := make(map[string]bool, len(refs))
	result := make([]string, 0, len(refs))
	for i, ref := range refs {
		id, err := ref.Resolve(catalog)
		if err != nil {
			return nil, fmt.Errorf("modes[%d]: %w", i, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("modes[%d]: duplicate mode %q", i, id)
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

// CacheConfig defines context pack cache control for ensemble execution.
type CacheConfig struct {
	// Enabled controls whether context pack caching is active.
	Enabled bool `json:"enabled" toml:"enabled" yaml:"enabled"`

	// TTL is how long cached context packs remain valid.
	TTL time.Duration `json:"ttl,omitempty" toml:"ttl,omitempty" yaml:"ttl,omitempty"`

	// MaxEntries is the maximum number of cached context packs.
	MaxEntries int `json:"max_entries,omitempty" toml:"max_entries,omitempty" yaml:"max_entries,omitempty"`

	// ShareAcrossModes controls whether modes in the same ensemble share cache.
	ShareAcrossModes bool `json:"share_across_modes,omitempty" toml:"share_across_modes,omitempty" yaml:"share_across_modes,omitempty"`
}

// DefaultCacheConfig returns sensible default cache settings.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		Enabled:          true,
		TTL:              15 * time.Minute,
		MaxEntries:       32,
		ShareAcrossModes: true,
	}
}

// AgentDistribution configures how modes are distributed across agents.
type AgentDistribution struct {
	// Strategy controls how modes map to agents ("one-per-agent", "round-robin", "packed").
	Strategy string `json:"strategy" toml:"strategy" yaml:"strategy"`

	// MaxAgents limits the number of agents spawned (0 = one per mode).
	MaxAgents int `json:"max_agents,omitempty" toml:"max_agents,omitempty" yaml:"max_agents,omitempty"`

	// PreferredAgentType is the default agent type for modes (cc, cod, gmi).
	PreferredAgentType string `json:"preferred_agent_type,omitempty" toml:"preferred_agent_type,omitempty" yaml:"preferred_agent_type,omitempty"`
}

// DefaultAgentDistribution returns the default agent distribution (one mode per agent).
func DefaultAgentDistribution() AgentDistribution {
	return AgentDistribution{
		Strategy: "one-per-agent",
	}
}

// EnsemblePreset is a pre-configured mode combination.
// Presets make it easy to quickly start an ensemble with a curated
// set of modes for common use cases.
type EnsemblePreset struct {
	// Name is the unique identifier for this preset.
	Name string `json:"name" toml:"name" yaml:"name"`

	// DisplayName is the human-facing name (e.g., "Architecture Review").
	DisplayName string `json:"display_name,omitempty" toml:"display_name,omitempty" yaml:"display_name,omitempty"`

	// Description explains what this preset is for.
	Description string `json:"description" toml:"description" yaml:"description"`

	// Modes lists mode references (by id or code) for this preset.
	Modes []ModeRef `json:"modes" toml:"modes" yaml:"modes"`

	// Synthesis configures how outputs are combined. Reuses SynthesisConfig.
	Synthesis SynthesisConfig `json:"synthesis,omitempty" toml:"synthesis,omitempty" yaml:"synthesis,omitempty"`

	// Budget defines resource limits. Reuses BudgetConfig.
	Budget BudgetConfig `json:"budget,omitempty" toml:"budget,omitempty" yaml:"budget,omitempty"`

	// Cache configures context pack caching.
	Cache CacheConfig `json:"cache,omitempty" toml:"cache,omitempty" yaml:"cache,omitempty"`

	// AllowAdvanced enables advanced-tier modes (default: only core modes).
	AllowAdvanced bool `json:"allow_advanced,omitempty" toml:"allow_advanced,omitempty" yaml:"allow_advanced,omitempty"`

	// AgentDistribution configures how modes are distributed across agents.
	AgentDistribution *AgentDistribution `json:"agent_distribution,omitempty" toml:"agent_distribution,omitempty" yaml:"agent_distribution,omitempty"`

	// Tags are optional categories for organization.
	Tags []string `json:"tags,omitempty" toml:"tags,omitempty" yaml:"tags,omitempty"`

	// Source indicates where this preset was loaded from (embedded, user, project).
	// Set at load time, not persisted in TOML.
	Source string `json:"source,omitempty" toml:"-" yaml:"source,omitempty"`
}

// Validate checks that the preset is valid and all mode refs resolve against the catalog.
func (p *EnsemblePreset) Validate(catalog *ModeCatalog) error {
	if p.Name == "" {
		return errors.New("preset name is required")
	}
	if len(p.Modes) == 0 {
		return errors.New("preset must have at least one mode")
	}

	// Resolve all mode refs
	resolved, err := ResolveModeRefs(p.Modes, catalog)
	if err != nil {
		return fmt.Errorf("preset %q: %w", p.Name, err)
	}

	// Check tier restrictions
	if !p.AllowAdvanced {
		for _, modeID := range resolved {
			mode := catalog.GetMode(modeID)
			if mode != nil && mode.Tier == TierAdvanced {
				return fmt.Errorf("preset %q: mode %q is advanced tier but AllowAdvanced is false", p.Name, modeID)
			}
		}
	}

	// Validate synthesis config if strategy is set
	if p.Synthesis.Strategy != "" && !p.Synthesis.Strategy.IsValid() {
		return fmt.Errorf("preset %q: invalid synthesis strategy %q", p.Name, p.Synthesis.Strategy)
	}

	return nil
}

// ResolveIDs resolves all ModeRefs to mode IDs using the catalog.
func (p *EnsemblePreset) ResolveIDs(catalog *ModeCatalog) ([]string, error) {
	return ResolveModeRefs(p.Modes, catalog)
}

// modeIDRegex validates mode IDs (lowercase alphanumeric with hyphens).
var modeIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidateModeID checks if a mode ID is valid.
// Valid IDs are lowercase, start with a letter, and contain only
// alphanumeric characters and hyphens.
func ValidateModeID(id string) error {
	if id == "" {
		return errors.New("mode ID cannot be empty")
	}
	if len(id) > 64 {
		return fmt.Errorf("mode ID exceeds 64 characters (got %d)", len(id))
	}
	if !modeIDRegex.MatchString(id) {
		return fmt.Errorf("invalid mode ID %q: must be lowercase, start with a letter, and contain only alphanumeric characters and hyphens", id)
	}
	return nil
}

// modeCodeRegex validates mode codes (letter A-L followed by digits).
var modeCodeRegex = regexp.MustCompile(`^[A-L][0-9]+$`)

// ValidateModeCode checks if a mode code is valid and consistent with its category.
// Valid codes are a single letter A-L followed by one or more digits.
// The letter must match the category's letter mapping.
func ValidateModeCode(code string, category ModeCategory) error {
	if code == "" {
		return errors.New("mode code cannot be empty")
	}
	if !modeCodeRegex.MatchString(code) {
		return fmt.Errorf("invalid mode code %q: must match format [A-L][0-9]+ (e.g., A1, B3)", code)
	}
	expectedLetter := category.CategoryLetter()
	if expectedLetter == "" {
		return fmt.Errorf("cannot validate code %q: category %q has no letter mapping", code, category)
	}
	codeLetter := string(code[0])
	if codeLetter != expectedLetter {
		return fmt.Errorf("mode code %q letter %q does not match category %q (expected %q)", code, codeLetter, category, expectedLetter)
	}
	return nil
}

// ValidatePreset checks if a preset is valid and all its modes exist.
// This is an alias for EnsemblePreset.Validate for convenience.
func ValidatePreset(preset EnsemblePreset, catalog *ModeCatalog) error {
	return preset.Validate(catalog)
}

// ModeCatalog holds a collection of reasoning modes.
// It provides lookup and filtering capabilities.
type ModeCatalog struct {
	modes   []ReasoningMode
	byID    map[string]*ReasoningMode
	byCode  map[string]*ReasoningMode
	byCat   map[ModeCategory][]*ReasoningMode
	byTier  map[ModeTier][]*ReasoningMode
	version string
}

// NewModeCatalog creates a new catalog from a slice of modes.
func NewModeCatalog(modes []ReasoningMode, version string) (*ModeCatalog, error) {
	c := &ModeCatalog{
		modes:   make([]ReasoningMode, 0, len(modes)),
		byID:    make(map[string]*ReasoningMode),
		byCode:  make(map[string]*ReasoningMode),
		byCat:   make(map[ModeCategory][]*ReasoningMode),
		byTier:  make(map[ModeTier][]*ReasoningMode),
		version: version,
	}

	for i := range modes {
		mode := modes[i]
		if err := mode.Validate(); err != nil {
			return nil, fmt.Errorf("invalid mode %q: %w", mode.ID, err)
		}
		if _, exists := c.byID[mode.ID]; exists {
			return nil, fmt.Errorf("duplicate mode ID %q", mode.ID)
		}
		if mode.Code != "" {
			if _, exists := c.byCode[mode.Code]; exists {
				return nil, fmt.Errorf("duplicate mode code %q", mode.Code)
			}
		}
		c.modes = append(c.modes, mode)
		ptr := &c.modes[len(c.modes)-1]
		c.byID[mode.ID] = ptr
		if mode.Code != "" {
			c.byCode[mode.Code] = ptr
		}
		c.byCat[mode.Category] = append(c.byCat[mode.Category], ptr)
		if mode.Tier != "" {
			c.byTier[mode.Tier] = append(c.byTier[mode.Tier], ptr)
		}
	}

	return c, nil
}

// GetMode returns a mode by ID, or nil if not found.
func (c *ModeCatalog) GetMode(id string) *ReasoningMode {
	return c.byID[id]
}

// ListModes returns all modes in the catalog.
func (c *ModeCatalog) ListModes() []ReasoningMode {
	result := make([]ReasoningMode, len(c.modes))
	copy(result, c.modes)
	return result
}

// ListByCategory returns all modes in a specific category.
func (c *ModeCatalog) ListByCategory(cat ModeCategory) []ReasoningMode {
	ptrs := c.byCat[cat]
	result := make([]ReasoningMode, len(ptrs))
	for i, p := range ptrs {
		result[i] = *p
	}
	return result
}

// SearchModes finds modes matching a search term in name, description, or best_for.
func (c *ModeCatalog) SearchModes(term string) []ReasoningMode {
	term = strings.ToLower(term)
	var result []ReasoningMode
	for _, m := range c.modes {
		if strings.Contains(strings.ToLower(m.Name), term) ||
			strings.Contains(strings.ToLower(m.Description), term) ||
			strings.Contains(strings.ToLower(m.ShortDesc), term) {
			result = append(result, m)
			continue
		}
		for _, bf := range m.BestFor {
			if strings.Contains(strings.ToLower(bf), term) {
				result = append(result, m)
				break
			}
		}
	}
	return result
}

// Version returns the catalog version string.
func (c *ModeCatalog) Version() string {
	return c.version
}

// Count returns the total number of modes.
func (c *ModeCatalog) Count() int {
	return len(c.modes)
}

// GetModeByCode returns a mode by its taxonomy code (e.g., "A1"), or nil if not found.
func (c *ModeCatalog) GetModeByCode(code string) *ReasoningMode {
	return c.byCode[code]
}

// ListByTier returns all modes with the specified tier.
func (c *ModeCatalog) ListByTier(tier ModeTier) []ReasoningMode {
	ptrs := c.byTier[tier]
	result := make([]ReasoningMode, len(ptrs))
	for i, p := range ptrs {
		result[i] = *p
	}
	return result
}

// ListDefault returns all core-tier modes (the default subset for UX).
func (c *ModeCatalog) ListDefault() []ReasoningMode {
	return c.ListByTier(TierCore)
}

// =============================================================================
// Output Schema Types
// =============================================================================
// These types define the mandatory structure for all mode outputs.
// Every mode must produce output conforming to ModeOutput to enable
// consistent synthesis and comparison across reasoning approaches.

// ImpactLevel categorizes the significance of findings and risks.
type ImpactLevel string

const (
	// ImpactCritical indicates a showstopper finding requiring immediate attention.
	ImpactCritical ImpactLevel = "critical"
	// ImpactHigh indicates a significant finding requiring prompt attention.
	ImpactHigh ImpactLevel = "high"
	// ImpactMedium indicates a notable finding worth addressing.
	ImpactMedium ImpactLevel = "medium"
	// ImpactLow indicates a minor finding for consideration.
	ImpactLow ImpactLevel = "low"
)

// String returns the impact level as a string.
func (i ImpactLevel) String() string {
	return string(i)
}

// IsValid returns true if this is a known impact level.
func (i ImpactLevel) IsValid() bool {
	switch i {
	case ImpactCritical, ImpactHigh, ImpactMedium, ImpactLow:
		return true
	default:
		return false
	}
}

// ParseConfidenceString converts a string confidence value to a float.
// Accepts floats ("0.8"), percentages ("80%"), or qualitative levels ("high", "medium", "low").
// Qualitative mappings: high=0.8, medium=0.5, low=0.2.
func ParseConfidenceString(s string) (Confidence, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	// Handle qualitative levels
	switch s {
	case "high":
		return 0.8, nil
	case "medium", "med":
		return 0.5, nil
	case "low":
		return 0.2, nil
	}

	// Handle percentage format (e.g., "80%")
	if strings.HasSuffix(s, "%") {
		s = strings.TrimSuffix(s, "%")
		var pct float64
		if _, err := fmt.Sscanf(s, "%f", &pct); err != nil {
			return 0, fmt.Errorf("invalid confidence percentage: %q", s)
		}
		return Confidence(pct / 100), nil
	}

	// Handle float format
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, fmt.Errorf("invalid confidence value: %q", s)
	}
	return Confidence(f), nil
}

// Confidence represents a confidence score between 0.0 and 1.0.
type Confidence float64

// Validate checks that the confidence is in the valid range [0.0, 1.0].
func (c Confidence) Validate() error {
	if c < 0.0 || c > 1.0 {
		return fmt.Errorf("confidence must be between 0.0 and 1.0, got %f", c)
	}
	return nil
}

// String returns confidence as a percentage string.
func (c Confidence) String() string {
	return fmt.Sprintf("%.0f%%", c*100)
}

// Finding represents a specific discovery or insight from reasoning.
type Finding struct {
	// Finding is the description of what was discovered.
	Finding string `json:"finding" yaml:"finding"`

	// Impact is the significance level of this finding.
	Impact ImpactLevel `json:"impact" yaml:"impact"`

	// Confidence is how certain the mode is about this finding (0.0-1.0).
	Confidence Confidence `json:"confidence" yaml:"confidence"`

	// EvidencePointer is a reference to supporting evidence (e.g., "file.go:42").
	EvidencePointer string `json:"evidence_pointer,omitempty" yaml:"evidence_pointer,omitempty"`

	// Reasoning explains how this finding was reached.
	Reasoning string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
}

// Validate checks that the finding is properly formed.
func (f *Finding) Validate() error {
	if f.Finding == "" {
		return errors.New("finding description is required")
	}
	if !f.Impact.IsValid() {
		return fmt.Errorf("invalid impact level %q", f.Impact)
	}
	if err := f.Confidence.Validate(); err != nil {
		return fmt.Errorf("invalid confidence: %w", err)
	}
	return nil
}

// Risk represents a potential problem or threat identified by reasoning.
type Risk struct {
	// Risk is the description of the potential problem.
	Risk string `json:"risk" yaml:"risk"`

	// Impact is the severity if this risk materializes.
	Impact ImpactLevel `json:"impact" yaml:"impact"`

	// Likelihood is the probability this risk will occur (0.0-1.0).
	Likelihood Confidence `json:"likelihood" yaml:"likelihood"`

	// Mitigation describes how to address this risk.
	Mitigation string `json:"mitigation,omitempty" yaml:"mitigation,omitempty"`

	// AffectedAreas lists components or areas impacted by this risk.
	AffectedAreas []string `json:"affected_areas,omitempty" yaml:"affected_areas,omitempty"`
}

// Validate checks that the risk is properly formed.
func (r *Risk) Validate() error {
	if r.Risk == "" {
		return errors.New("risk description is required")
	}
	if !r.Impact.IsValid() {
		return fmt.Errorf("invalid impact level %q", r.Impact)
	}
	if err := r.Likelihood.Validate(); err != nil {
		return fmt.Errorf("invalid likelihood: %w", err)
	}
	return nil
}

// Recommendation represents a suggested action from reasoning.
type Recommendation struct {
	// Recommendation is the suggested action.
	Recommendation string `json:"recommendation" yaml:"recommendation"`

	// Priority indicates how urgent this recommendation is.
	Priority ImpactLevel `json:"priority" yaml:"priority"`

	// Rationale explains why this is recommended.
	Rationale string `json:"rationale,omitempty" yaml:"rationale,omitempty"`

	// Effort is an estimate of implementation complexity (low/medium/high).
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`

	// RelatedFindings lists finding indices that support this recommendation.
	RelatedFindings []int `json:"related_findings,omitempty" yaml:"related_findings,omitempty"`
}

// Validate checks that the recommendation is properly formed.
func (r *Recommendation) Validate() error {
	if r.Recommendation == "" {
		return errors.New("recommendation text is required")
	}
	if !r.Priority.IsValid() {
		return fmt.Errorf("invalid priority %q", r.Priority)
	}
	return nil
}

// Question represents an unresolved question for the user.
type Question struct {
	// Question is the query for the user.
	Question string `json:"question" yaml:"question"`

	// Context explains why this question matters.
	Context string `json:"context,omitempty" yaml:"context,omitempty"`

	// Blocking indicates if this question blocks further analysis.
	Blocking bool `json:"blocking,omitempty" yaml:"blocking,omitempty"`

	// SuggestedAnswers provides possible responses if applicable.
	SuggestedAnswers []string `json:"suggested_answers,omitempty" yaml:"suggested_answers,omitempty"`
}

// Validate checks that the question is properly formed.
func (q *Question) Validate() error {
	if q.Question == "" {
		return errors.New("question text is required")
	}
	return nil
}

// FailureModeWarning represents a potential failure mode to watch for.
type FailureModeWarning struct {
	// Mode is the failure mode identifier.
	Mode string `json:"mode" yaml:"mode"`

	// Description explains what this failure mode entails.
	Description string `json:"description" yaml:"description"`

	// Indicators are signs that this failure mode may be occurring.
	Indicators []string `json:"indicators,omitempty" yaml:"indicators,omitempty"`

	// Prevention describes how to avoid this failure mode.
	Prevention string `json:"prevention,omitempty" yaml:"prevention,omitempty"`
}

// Validate checks that the failure mode warning is properly formed.
func (f *FailureModeWarning) Validate() error {
	if f.Mode == "" {
		return errors.New("failure mode identifier is required")
	}
	if f.Description == "" {
		return errors.New("failure mode description is required")
	}
	return nil
}

// ModeOutput is the mandatory output schema for all reasoning modes.
// Every mode must produce output conforming to this structure to enable
// consistent synthesis and comparison across different reasoning approaches.
type ModeOutput struct {
	// ModeID identifies which reasoning mode produced this output.
	ModeID string `json:"mode_id" yaml:"mode_id"`

	// Thesis is the main conclusion or argument from this mode.
	Thesis string `json:"thesis" yaml:"thesis"`

	// TopFindings are the key discoveries ranked by importance.
	TopFindings []Finding `json:"top_findings" yaml:"top_findings"`

	// Risks are potential problems or threats identified.
	Risks []Risk `json:"risks,omitempty" yaml:"risks,omitempty"`

	// Recommendations are suggested actions.
	Recommendations []Recommendation `json:"recommendations,omitempty" yaml:"recommendations,omitempty"`

	// QuestionsForUser are unresolved queries needing user input.
	QuestionsForUser []Question `json:"questions_for_user,omitempty" yaml:"questions_for_user,omitempty"`

	// FailureModesToWatch are warnings about reasoning pitfalls.
	FailureModesToWatch []FailureModeWarning `json:"failure_modes_to_watch,omitempty" yaml:"failure_modes_to_watch,omitempty"`

	// Confidence is the overall confidence in this analysis (0.0-1.0).
	Confidence Confidence `json:"confidence" yaml:"confidence"`

	// RawOutput is the original unstructured output from the agent.
	RawOutput string `json:"raw_output,omitempty" yaml:"raw_output,omitempty"`

	// GeneratedAt is when this output was produced.
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
}

// Validate checks that the mode output is properly formed.
func (m *ModeOutput) Validate() error {
	if m.ModeID == "" {
		return errors.New("mode_id is required")
	}
	if m.Thesis == "" {
		return errors.New("thesis is required")
	}
	if len(m.TopFindings) == 0 {
		return errors.New("at least one finding is required")
	}
	if err := m.Confidence.Validate(); err != nil {
		return fmt.Errorf("invalid confidence: %w", err)
	}

	// Validate all findings
	for i, f := range m.TopFindings {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("finding[%d]: %w", i, err)
		}
	}

	// Validate all risks
	for i, r := range m.Risks {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("risk[%d]: %w", i, err)
		}
	}

	// Validate all recommendations
	for i, r := range m.Recommendations {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("recommendation[%d]: %w", i, err)
		}
	}

	// Validate all questions
	for i, q := range m.QuestionsForUser {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("question[%d]: %w", i, err)
		}
	}

	// Validate all failure mode warnings
	for i, f := range m.FailureModesToWatch {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("failure_mode[%d]: %w", i, err)
		}
	}

	return nil
}

// =============================================================================
// Configuration Types
// =============================================================================

// BudgetConfig defines resource limits for ensemble execution.
type BudgetConfig struct {
	// MaxTokensPerMode is the token limit for each mode's response.
	MaxTokensPerMode int `json:"max_tokens_per_mode,omitempty" toml:"max_tokens_per_mode" yaml:"max_tokens_per_mode,omitempty"`

	// MaxTotalTokens is the total token budget across all modes.
	MaxTotalTokens int `json:"max_total_tokens,omitempty" toml:"max_total_tokens" yaml:"max_total_tokens,omitempty"`

	// TimeoutPerMode is the max duration for each mode to complete.
	TimeoutPerMode time.Duration `json:"timeout_per_mode,omitempty" toml:"timeout_per_mode" yaml:"timeout_per_mode,omitempty"`

	// TotalTimeout is the max duration for the entire ensemble.
	TotalTimeout time.Duration `json:"total_timeout,omitempty" toml:"total_timeout" yaml:"total_timeout,omitempty"`

	// MaxRetries is how many times to retry failed modes.
	MaxRetries int `json:"max_retries,omitempty" toml:"max_retries" yaml:"max_retries,omitempty"`
}

// DefaultBudgetConfig returns sensible default budget limits.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxTokensPerMode: 4000,
		MaxTotalTokens:   50000,
		TimeoutPerMode:   5 * time.Minute,
		TotalTimeout:     30 * time.Minute,
		MaxRetries:       2,
	}
}

// SynthesisConfig defines how ensemble outputs are combined.
type SynthesisConfig struct {
	// Strategy is the synthesis approach to use.
	Strategy SynthesisStrategy `json:"strategy" toml:"strategy" yaml:"strategy"`

	// MinConfidence is the minimum confidence threshold for inclusion.
	MinConfidence Confidence `json:"min_confidence,omitempty" toml:"min_confidence" yaml:"min_confidence,omitempty"`

	// MaxFindings limits how many findings to include in synthesis.
	MaxFindings int `json:"max_findings,omitempty" toml:"max_findings" yaml:"max_findings,omitempty"`

	// IncludeRawOutputs includes original mode outputs in synthesis.
	IncludeRawOutputs bool `json:"include_raw_outputs,omitempty" toml:"include_raw_outputs" yaml:"include_raw_outputs,omitempty"`

	// ConflictResolution specifies how to handle disagreements.
	ConflictResolution string `json:"conflict_resolution,omitempty" toml:"conflict_resolution" yaml:"conflict_resolution,omitempty"`
}

// DefaultSynthesisConfig returns sensible default synthesis settings.
func DefaultSynthesisConfig() SynthesisConfig {
	return SynthesisConfig{
		Strategy:           StrategyConsensus,
		MinConfidence:      0.5,
		MaxFindings:        10,
		IncludeRawOutputs:  false,
		ConflictResolution: "highlight",
	}
}

// Ensemble is a curated collection of modes for a specific use case.
// This is the primary user-facing interface - users select ensembles,
// not individual modes. Modes are internal implementation details.
type Ensemble struct {
	// Name is the unique identifier (e.g., "project-diagnosis").
	Name string `json:"name" toml:"name" yaml:"name"`

	// DisplayName is the user-facing name (e.g., "Project Diagnosis").
	DisplayName string `json:"display_name" toml:"display_name" yaml:"display_name"`

	// Description explains what this ensemble is for.
	Description string `json:"description" toml:"description" yaml:"description"`

	// ModeIDs lists the reasoning modes in this ensemble.
	ModeIDs []string `json:"mode_ids" toml:"mode_ids" yaml:"mode_ids"`

	// Synthesis configures how outputs are combined.
	Synthesis SynthesisConfig `json:"synthesis" toml:"synthesis" yaml:"synthesis"`

	// Budget defines resource limits.
	Budget BudgetConfig `json:"budget" toml:"budget" yaml:"budget"`

	// Cache configures context pack caching.
	Cache CacheConfig `json:"cache,omitempty" toml:"cache" yaml:"cache,omitempty"`

	// AllowAdvanced enables advanced-tier modes.
	AllowAdvanced bool `json:"allow_advanced,omitempty" toml:"allow_advanced" yaml:"allow_advanced,omitempty"`

	// AgentDistribution configures how modes are distributed across agents.
	AgentDistribution *AgentDistribution `json:"agent_distribution,omitempty" toml:"agent_distribution" yaml:"agent_distribution,omitempty"`

	// Tags enable filtering and discovery.
	Tags []string `json:"tags,omitempty" toml:"tags" yaml:"tags,omitempty"`

	// Icon is a single emoji or glyph for UI display.
	Icon string `json:"icon,omitempty" toml:"icon" yaml:"icon,omitempty"`

	// Source indicates where this ensemble was loaded from.
	Source string `json:"source,omitempty" toml:"-" yaml:"source,omitempty"`
}

// Validate checks that the ensemble is valid and all mode IDs exist in the catalog.
func (e *Ensemble) Validate(catalog *ModeCatalog) error {
	if e.Name == "" {
		return errors.New("ensemble name is required")
	}
	if err := ValidateModeID(e.Name); err != nil {
		return fmt.Errorf("invalid ensemble name: %w", err)
	}
	if e.DisplayName == "" {
		return errors.New("ensemble display_name is required")
	}
	if len(e.ModeIDs) == 0 {
		return errors.New("ensemble must have at least one mode")
	}

	// Verify all modes exist
	for _, modeID := range e.ModeIDs {
		if catalog.GetMode(modeID) == nil {
			return fmt.Errorf("mode %q not found in catalog", modeID)
		}
	}

	return nil
}
