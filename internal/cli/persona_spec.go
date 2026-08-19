package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/persona"
)

// PersonaSpec represents a parsed persona specification with optional count
type PersonaSpec struct {
	Name  string
	Count int // Default 1
}

// PersonaSpecs is a slice of PersonaSpec that implements the flag.Value interface
type PersonaSpecs []PersonaSpec

// String implements flag.Value
func (s *PersonaSpecs) String() string {
	if s == nil || len(*s) == 0 {
		return ""
	}
	var parts []string
	for _, spec := range *s {
		if spec.Count > 1 {
			parts = append(parts, fmt.Sprintf("%s:%d", spec.Name, spec.Count))
		} else {
			parts = append(parts, spec.Name)
		}
	}
	return strings.Join(parts, ",")
}

// Set implements flag.Value for parsing and accumulating persona specs
func (s *PersonaSpecs) Set(value string) error {
	spec, err := ParsePersonaSpec(value)
	if err != nil {
		return err
	}
	*s = append(*s, spec)
	return nil
}

// Type returns the type name for pflag
func (s *PersonaSpecs) Type() string {
	return "name[:count]"
}

// ParsePersonaSpec parses a single persona specification string
// Format: "name" or "name:count" where count defaults to 1
func ParsePersonaSpec(value string) (PersonaSpec, error) {
	spec := PersonaSpec{Count: 1}

	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return spec, fmt.Errorf("invalid persona spec: %q", value)
	}

	spec.Name = strings.TrimSpace(parts[0])

	if len(parts) > 1 {
		countStr := strings.TrimSpace(parts[1])
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return spec, fmt.Errorf("invalid count in persona spec %q: %w", value, err)
		}
		if count < 1 {
			return spec, fmt.Errorf("count must be at least 1 in persona spec: %q", value)
		}
		spec.Count = count
	}

	return spec, nil
}

// TotalCount returns the sum of all persona counts
func (s PersonaSpecs) TotalCount() int {
	total := 0
	for _, spec := range s {
		total += spec.Count
	}
	return total
}

// ResolvedPersonaAgent represents a resolved persona ready to spawn
type ResolvedPersonaAgent struct {
	Persona *persona.Persona
	Type    AgentType
	Model   string
	Index   int // 1-based index within this persona
}

// ResolvePersonas converts persona specs into resolved agents using the registry
// Returns the resolved agents and any validation errors
func ResolvePersonas(specs PersonaSpecs, projectDir string) ([]ResolvedPersonaAgent, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	// Load persona registry
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil, fmt.Errorf("loading persona registry: %w", err)
	}

	var agents []ResolvedPersonaAgent

	for _, spec := range specs {
		p, found := registry.Get(spec.Name)
		if !found {
			// List available personas for error message
			available := registry.List()
			var names []string
			for _, ap := range available {
				names = append(names, ap.Name)
			}
			return nil, fmt.Errorf("unknown persona %q (available: %s)", spec.Name, strings.Join(names, ", "))
		}

		// Convert agent type string to AgentType
		agentType := AgentType(p.AgentTypeFlag())

		// Create entries for each count
		for i := 1; i <= spec.Count; i++ {
			agents = append(agents, ResolvedPersonaAgent{
				Persona: p,
				Type:    agentType,
				Model:   p.Model,
				Index:   i,
			})
		}
	}

	return agents, nil
}

// PersonaAgentInfo holds information about an agent spawned from a persona
type PersonaAgentInfo struct {
	PersonaName string
	AgentType   AgentType
	Model       string
	Index       int
}

// FlattenPersonas expands resolved personas into individual agent entries
func FlattenPersonas(resolved []ResolvedPersonaAgent) []PersonaAgentInfo {
	var result []PersonaAgentInfo
	indices := make(map[AgentType]int) // Track index per type

	for _, r := range resolved {
		indices[r.Type]++
		result = append(result, PersonaAgentInfo{
			PersonaName: r.Persona.Name,
			AgentType:   r.Type,
			Model:       r.Model,
			Index:       indices[r.Type],
		})
	}

	return result
}
