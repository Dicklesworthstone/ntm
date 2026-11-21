package cli

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/session"
)

// PersonaSpec represents a parsed persona specification with optional count.
type PersonaSpec = session.PersonaSpec

// PersonaSpecs is a slice of PersonaSpec that implements the flag.Value interface.
type PersonaSpecs []PersonaSpec

// String implements flag.Value.
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

// Set implements flag.Value for parsing and accumulating persona specs.
func (s *PersonaSpecs) Set(value string) error {
	spec, err := ParsePersonaSpec(value)
	if err != nil {
		return err
	}
	*s = append(*s, spec)
	return nil
}

// Type returns the type name for pflag.
func (s *PersonaSpecs) Type() string {
	return "name[:count]"
}

// ParsePersonaSpec delegates to the shared spawn engine parser.
func ParsePersonaSpec(value string) (PersonaSpec, error) {
	return session.ParsePersonaSpec(value)
}

// TotalCount returns the sum of all persona counts.
func (s PersonaSpecs) TotalCount() int {
	total := 0
	for _, spec := range s {
		total += spec.Count
	}
	return total
}

// ResolvedPersonaAgent represents a resolved persona ready to spawn.
type ResolvedPersonaAgent struct {
	Persona *persona.Persona
	Type    AgentType
	Model   string
	Index   int // 1-based index within this persona
}

// ResolvePersonas delegates to the shared ORI spawn engine persona resolver.
func ResolvePersonas(specs PersonaSpecs, projectDir string) ([]ResolvedPersonaAgent, error) {
	slots, err := session.ResolveSpawnPersonas(specs, projectDir)
	if err != nil {
		return nil, err
	}
	agents := make([]ResolvedPersonaAgent, 0, len(slots))
	for _, slot := range slots {
		agents = append(agents, ResolvedPersonaAgent{
			Persona: slot.Persona,
			Type:    AgentType(slot.AgentType),
			Model:   slot.Model,
			Index:   slot.Index,
		})
	}
	return agents, nil
}

// PersonaAgentInfo holds information about an agent spawned from a persona.
type PersonaAgentInfo struct {
	PersonaName string
	AgentType   AgentType
	Model       string
	Index       int
}

// FlattenPersonas expands resolved personas into individual agent entries.
func FlattenPersonas(resolved []ResolvedPersonaAgent) []PersonaAgentInfo {
	var result []PersonaAgentInfo
	indices := make(map[AgentType]int)

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
