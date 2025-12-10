package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// modelPattern restricts model/alias values to a safe charset to prevent shell injection.
	// Allows common tokens: letters, numbers, dot, dash, underscore, slash, colon, plus, at.
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+-]+$`)
)

// AgentType represents the type of AI agent
type AgentType string

const (
	AgentTypeClaude AgentType = "cc"
	AgentTypeCodex  AgentType = "cod"
	AgentTypeGemini AgentType = "gmi"
)

// AgentSpec represents a parsed agent specification with optional model
type AgentSpec struct {
	Type  AgentType
	Count int
	Model string // Optional, empty = use default model
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
		if spec.Model != "" {
			parts = append(parts, fmt.Sprintf("%d:%s", spec.Count, spec.Model))
		} else {
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
	return "N[:model]"
}

// ParseAgentSpec parses a single agent specification string
// Format: "N" or "N:model" where N is count, model is optional alias
func ParseAgentSpec(value string) (AgentSpec, error) {
	var spec AgentSpec

	parts := strings.SplitN(value, ":", 2)
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
	Type  AgentType
	Index int    // 1-based index within type
	Model string // Resolved model (may be empty for default)
}

// Flatten expands all specs into individual agent entries
func (s AgentSpecs) Flatten() []FlatAgent {
	var result []FlatAgent
	indices := make(map[AgentType]int) // Track index per type

	for _, spec := range s {
		for i := 0; i < spec.Count; i++ {
			indices[spec.Type]++
			result = append(result, FlatAgent{
				Type:  spec.Type,
				Index: indices[spec.Type],
				Model: spec.Model,
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

	// If no model specified, use default
	if modelSpec == "" {
		switch agentType {
		case AgentTypeClaude:
			return cfg.Models.DefaultClaude
		case AgentTypeCodex:
			return cfg.Models.DefaultCodex
		case AgentTypeGemini:
			return cfg.Models.DefaultGemini
		}
		return ""
	}

	// Try to resolve alias
	var aliases map[string]string
	switch agentType {
	case AgentTypeClaude:
		aliases = cfg.Models.Claude
	case AgentTypeCodex:
		aliases = cfg.Models.Codex
	case AgentTypeGemini:
		aliases = cfg.Models.Gemini
	}

	if aliases != nil {
		if fullName, ok := aliases[modelSpec]; ok {
			return fullName
		}
	}

	// Assume it's already a full model name
	return modelSpec
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
	}

	if aliases == nil {
		return nil // No aliases configured
	}

	// Check if it's a known alias
	if _, ok := aliases[alias]; ok {
		return nil
	}

	// List available aliases for error message
	var available []string
	for k := range aliases {
		available = append(available, k)
	}

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
	return "N[:model]"
}
