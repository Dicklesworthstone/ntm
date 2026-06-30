package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

var (
	// modelPattern restricts model/alias values to a safe charset to prevent shell injection.
	// Allows common tokens: letters, numbers, dot, dash, underscore, slash, colon, plus, at.
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+-]+$`)
)

// AgentType represents the type of AI agent
type AgentType string

const (
	AgentTypeClaude      AgentType = "cc"
	AgentTypeCodex       AgentType = "cod"
	AgentTypeGemini      AgentType = "gmi"
	AgentTypeAntigravity AgentType = "agy"
	AgentTypeOllama      AgentType = "ollama"
	AgentTypeCursor      AgentType = "cursor"
	AgentTypeWindsurf    AgentType = "windsurf"
	AgentTypeAider       AgentType = "aider"
	// AgentTypeOpencode covers https://opencode.ai panes. See ntm#116 — the
	// `--oc` flag on spawn/add lets users mix opencode panes alongside the
	// other built-in agent types without overloading `--cursor` and getting
	// the wrong pane title / detection. Launch command resolves through the
	// `[agents] oc = "..."` config key (or the default `opencode` binary).
	AgentTypeOpencode AgentType = "oc"
)

// AgentSpec represents a parsed agent specification with optional model
type AgentSpec struct {
	Type  AgentType
	Count int
	Model string // Optional, empty = use default model
	// ReasoningEffort is the model reasoning-budget hint (Codex's
	// `-c model_reasoning_effort=...` knob and any future
	// equivalents). Empty = template default. Set via the third
	// colon-separated field in the spec (`N:model:effort`) or
	// from per-persona configuration. See ntm#140.
	ReasoningEffort string
}

// AgentSpecs is a slice of AgentSpec that implements the flag.Value interface
// for accumulating multiple agent specifications
type AgentSpecs []AgentSpec

// String implements flag.Value
func (s *AgentSpecs) String() string {
	if s == nil || len(*s) == 0 {
		return ""
	}
	var parts []string
	for _, spec := range *s {
		switch {
		case spec.Model != "" && spec.ReasoningEffort != "":
			parts = append(parts, fmt.Sprintf("%d:%s:%s", spec.Count, spec.Model, spec.ReasoningEffort))
		case spec.Model != "":
			parts = append(parts, fmt.Sprintf("%d:%s", spec.Count, spec.Model))
		default:
			parts = append(parts, strconv.Itoa(spec.Count))
		}
	}
	return strings.Join(parts, ",")
}

// Set implements flag.Value for parsing and accumulating specs
func (s *AgentSpecs) Set(value string) error {
	spec, err := ParseAgentSpec(value)
	if err != nil {
		return err
	}
	*s = append(*s, spec)
	return nil
}

// Type returns the type name for pflag
func (s *AgentSpecs) Type() string {
	return "N[:model[:effort]]"
}

// ParseAgentSpec parses a single agent specification string.
// Format: "N", "N:model", or "N:model:effort" where N is count, model is
// optional alias, and effort is a reasoning-effort hint passed through to
// the agent template (currently consumed by Codex's
// `model_reasoning_effort` knob — see ntm#140).
func ParseAgentSpec(value string) (AgentSpec, error) {
	var spec AgentSpec

	parts := strings.SplitN(value, ":", 3)
	if len(parts) == 0 || parts[0] == "" {
		return spec, fmt.Errorf("invalid agent spec: %q", value)
	}

	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return spec, fmt.Errorf("invalid count in agent spec: %q", parts[0])
	}
	if count < 1 {
		return spec, fmt.Errorf("count must be at least 1, got %d", count)
	}
	spec.Count = count

	if len(parts) > 1 {
		model := strings.TrimSpace(parts[1])
		if model == "" {
			return spec, fmt.Errorf("empty model in agent spec: %q", value)
		}
		if !modelPattern.MatchString(model) {
			return spec, fmt.Errorf("invalid characters in model %q; allowed: letters, numbers, . _ / @ : + -", model)
		}
		spec.Model = model
	}

	if len(parts) > 2 {
		effort := strings.TrimSpace(parts[2])
		if effort == "" {
			return spec, fmt.Errorf("empty reasoning effort in agent spec: %q", value)
		}
		if !modelPattern.MatchString(effort) {
			return spec, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ / @ : + -", effort)
		}
		spec.ReasoningEffort = effort
	}

	return spec, nil
}

// TotalCount returns the sum of all agent counts
func (s AgentSpecs) TotalCount() int {
	total := 0
	for _, spec := range s {
		total += spec.Count
	}
	return total
}

// ByType returns specs filtered by agent type
func (s AgentSpecs) ByType(t AgentType) AgentSpecs {
	var result AgentSpecs
	for _, spec := range s {
		if spec.Type == t {
			result = append(result, spec)
		}
	}
	return result
}

// Flatten expands specs into individual agents with their models
type FlatAgent struct {
	Type            AgentType
	Index           int    // 1-based index within type
	Model           string // Resolved model (may be empty for default)
	ReasoningEffort string // Reasoning-effort hint (Codex `model_reasoning_effort`)
	// Persona is non-nil when this agent was produced by expanding a
	// --profile-set / --profiles persona list (ntm#149). When set, Type
	// already reflects the persona's own agent_type and the spawn loop uses
	// the persona's model/system-prompt/name directly, instead of overlaying
	// a persona onto a generic agent by position.
	Persona *persona.Persona
}

// Flatten expands all specs into individual agent entries
func (s AgentSpecs) Flatten() []FlatAgent {
	var result []FlatAgent
	indices := make(map[AgentType]int) // Track index per type

	for _, spec := range s {
		for i := 0; i < spec.Count; i++ {
			indices[spec.Type]++
			result = append(result, FlatAgent{
				Type:            spec.Type,
				Index:           indices[spec.Type],
				Model:           spec.Model,
				ReasoningEffort: spec.ReasoningEffort,
			})
		}
	}
	return result
}

// ResolveModel resolves a model alias to its full name using config
// Returns the default model if alias is empty
func ResolveModel(agentType AgentType, modelSpec string) string {
	if cfg == nil {
		return modelSpec
	}
	return cfg.Models.GetModelName(string(agentType), modelSpec)
}

// ResolveModelWithPlugins resolves a model alias and, for plugin agent types
// with no per-spawn model, falls back to the plugin's default model.
func ResolveModelWithPlugins(agentType AgentType, modelSpec string, pluginMap map[string]plugins.AgentPlugin) string {
	resolved := ResolveModel(agentType, modelSpec)
	if strings.TrimSpace(modelSpec) != "" || strings.TrimSpace(resolved) != "" {
		return resolved
	}
	if pluginMap == nil {
		return resolved
	}
	if p, ok := pluginMap[string(agentType)]; ok {
		return strings.TrimSpace(p.Defaults.Model)
	}
	return resolved
}

// ValidateModelAlias checks if a model alias exists in config
func ValidateModelAlias(agentType AgentType, alias string) error {
	if cfg == nil || alias == "" {
		return nil // Can't validate without config, or nothing to validate
	}

	var aliases map[string]string
	switch agentType {
	case AgentTypeClaude:
		aliases = cfg.Models.Claude
	case AgentTypeCodex:
		aliases = cfg.Models.Codex
	case AgentTypeGemini:
		aliases = cfg.Models.Gemini
	case AgentTypeAntigravity:
		// agy's model is hard-pinned to "Gemini 3.1 Pro (High)" (see
		// config.AntigravityRequiredModel), so there is no per-user alias map to
		// validate against. Any requested alias is ignored rather than rejected:
		// spawn.go warns and proceeds with the pinned model, so validation must
		// not hard-fail here. Return nil unconditionally.
		return nil
	case AgentTypeOllama:
		aliases = cfg.Models.Ollama
	case AgentTypeCursor:
		aliases = cfg.Models.Cursor
	case AgentTypeWindsurf:
		aliases = cfg.Models.Windsurf
	case AgentTypeAider:
		aliases = cfg.Models.Aider
	case AgentTypeOpencode:
		aliases = cfg.Models.Opencode
	}

	if aliases == nil {
		return nil // No aliases configured
	}

	// Check if it's a known alias
	if _, ok := aliases[strings.ToLower(alias)]; ok {
		return nil
	}

	// List available aliases for error message
	var available []string
	for k := range aliases {
		available = append(available, k)
	}
	sort.Strings(available)

	return fmt.Errorf("unknown model alias %q for %s (available: %s)",
		alias, agentType, strings.Join(available, ", "))
}

// AgentSpecsValue creates a flag value that accumulates into the given slice
// with the specified agent type
func NewAgentSpecsValue(agentType AgentType, specs *AgentSpecs) *agentSpecsValue {
	return &agentSpecsValue{
		agentType: agentType,
		specs:     specs,
	}
}

// agentSpecsValue wraps AgentSpecs with a specific type for flag parsing
type agentSpecsValue struct {
	agentType AgentType
	specs     *AgentSpecs
}

func (v *agentSpecsValue) String() string {
	return v.specs.String()
}

func (v *agentSpecsValue) Set(value string) error {
	spec, err := ParseAgentSpec(value)
	if err != nil {
		return err
	}
	spec.Type = v.agentType
	*v.specs = append(*v.specs, spec)
	return nil
}

func (v *agentSpecsValue) Type() string {
	return "N[:model[:effort]]"
}
