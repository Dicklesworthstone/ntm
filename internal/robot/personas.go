package robot

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/persona"
)

const (
	personasSchemaID      = "ntm:robot:personas:v1"
	personasSchemaVersion = "1.0.0"

	PersonasSchemaID      = personasSchemaID
	PersonasSchemaVersion = personasSchemaVersion
)

// PersonasOptions configures GetPersonas.
type PersonasOptions struct {
	ProjectDir string
	Agent      string
	Tag        string
}

// PersonasOutput is the canonical robot envelope for --robot-personas and GET /api/v1/personas.
type PersonasOutput struct {
	RobotResponse
	SchemaID      string                `json:"schema_id"`
	SchemaVersion string                `json:"schema_version"`
	ProjectDir    string                `json:"project_dir,omitempty"`
	FilterAgent   string                `json:"filter_agent,omitempty"`
	FilterTag     string                `json:"filter_tag,omitempty"`
	Personas      []*persona.Persona    `json:"personas"`
	ProfileSets   []*persona.PersonaSet `json:"profile_sets"`
	Count         int                   `json:"count"`
	BuiltinCount  int                   `json:"builtin_count"`
	AgentHints    *AgentHints           `json:"_agent_hints,omitempty"`
}

// NormalizePersonaAgentType canonicalizes any agent alias across all 11 harnesses + user.
func NormalizePersonaAgentType(agentType string) string {
	return string(agent.AgentType(agentType).Canonical())
}

// MatchesPersonaAgentFilter reports whether agentType matches filter after canonical alias normalization.
func MatchesPersonaAgentFilter(agentType, filter string) bool {
	trimmed := strings.TrimSpace(filter)
	if strings.Compare(trimmed, "") == 0 {
		return true
	}
	return strings.EqualFold(NormalizePersonaAgentType(agentType), NormalizePersonaAgentType(trimmed))
}

// GetPersonas loads built-in, user, and project personas and profile sets with optional 11-harness agent and tag filtering.
func GetPersonas(opts PersonasOptions) (*PersonasOutput, error) {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if strings.Compare(projectDir, "") == 0 {
		if cwd, err := os.Getwd(); err == nil {
			projectDir = cwd
		}
	}

	filterAgent := strings.TrimSpace(opts.Agent)
	filterTag := strings.TrimSpace(opts.Tag)

	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return &PersonasOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Inspect personas.toml syntax in ~/.config/ntm/ or .ntm/"),
			SchemaID:      personasSchemaID,
			SchemaVersion: personasSchemaVersion,
			ProjectDir:    projectDir,
			Personas:      []*persona.Persona{},
			ProfileSets:   []*persona.PersonaSet{},
		}, nil
	}

	all := registry.List()
	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})

	filtered := make([]*persona.Persona, 0, len(all))
	for _, p := range all {
		if !MatchesPersonaAgentFilter(p.AgentType, filterAgent) {
			continue
		}
		if strings.Compare(filterTag, "") != 0 {
			hasTag := false
			for _, tag := range p.Tags {
				if strings.EqualFold(tag, filterTag) {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
		}
		filtered = append(filtered, p)
	}

	sets := registry.ListSets()
	if sets == nil {
		sets = []*persona.PersonaSet{}
	} else {
		sort.Slice(sets, func(i, j int) bool {
			return sets[i].Name < sets[j].Name
		})
	}

	builtinCount := len(persona.BuiltinPersonas())
	return &PersonasOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      personasSchemaID,
		SchemaVersion: personasSchemaVersion,
		ProjectDir:    projectDir,
		FilterAgent:   filterAgent,
		FilterTag:     filterTag,
		Personas:      filtered,
		ProfileSets:   sets,
		Count:         len(filtered),
		BuiltinCount:  builtinCount,
		AgentHints: &AgentHints{
			Summary: fmt.Sprintf("Loaded %d persona(s) (%d built-in) and %d profile set(s).", len(filtered), builtinCount, len(sets)),
		},
	}, nil
}

// PrintPersonas writes the terminal JSON/TOON response for --robot-personas.
func PrintPersonas(opts PersonasOptions) error {
	out, err := GetPersonas(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot personas failed")
}

func personasCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "personas",
		Flag:        "--robot-personas",
		Category:    "spawn",
		Summary:     "List agent personas and profile sets.",
		Description: "List built-in, user, and project agent personas and profile sets with 11-harness alias and tag filtering.",
		Parameters: []RobotParameter{
			{Name: "personas-agent", Flag: "--personas-agent", Type: "string", Required: false, Description: "Filter personas by harness alias (cc, cod, gmi, agy, grok, cursor, windsurf, aider, oc, omp, ollama)"},
			{Name: "personas-tag", Flag: "--personas-tag", Type: "string", Required: false, Description: "Filter personas by tag"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory for .ntm/personas.toml overrides (defaults to CWD)"},
		},
		Examples: []string{
			"ntm --robot-personas",
			"ntm --robot-personas --personas-agent=claude",
			"ntm --robot-personas --personas-tag=review",
		},
	}
}
