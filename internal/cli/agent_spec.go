package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

var (
	// modelPattern restricts model/alias values to a safe charset to prevent shell injection.
	// Allows common tokens: letters, numbers, dot, dash, underscore, slash, colon, plus, at.
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+-]+$`)

	// agyModelPattern is the RELAXED model charset for the Antigravity (agy)
	// provider. Antigravity's model *display* names contain spaces and
	// parentheses — e.g. "Gemini 3.7 Flash (High)" — which the standard
	// modelPattern rejects. agy always renders its model through
	// {{shellQuote .Model}} in the launch template (and, in practice, the value
	// is hard-pinned by config.AntigravityRequiredModel before it ever reaches a
	// shell), so widening the charset to include spaces and parentheses is safe:
	// shell metacharacters (;, |, &, $, backtick, quotes, backslash, redirects,
	// newlines) remain rejected, preventing injection.
	agyModelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+ ()-]+$`)

	// effortPattern restricts the '@effort' suffix (ntm-mjf7) to a tighter
	// charset than models: effort hints are plain words like "high" or
	// "ultra", so '@', '/', and ':' are excluded to keep the grammar
	// unambiguous.
	effortPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
)

// AgentType represents the type of AI agent
type AgentType string

const (
	AgentTypeClaude      AgentType = "cc"
	AgentTypeCodex       AgentType = "cod"
	AgentTypeGemini      AgentType = "gmi"
	AgentTypeAntigravity AgentType = "agy"
	AgentTypeGrok        AgentType = "grok"
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

// agentTypeSupportsEffortSuffix reports whether an agent type has a
// reasoning-effort knob, making the `model@effort` suffix meaningful. For
// every other type '@' stays a literal model character (e.g. opencode
// `provider/model@tag` specs) instead of being reinterpreted as an effort
// separator (ntm-mjf7).
func agentTypeSupportsEffortSuffix(agentType AgentType) bool {
	switch agentType {
	case AgentTypeClaude, AgentTypeCodex, AgentTypeGrok:
		return true
	default:
		return false
	}
}

// ParseAgentSpec parses a single agent specification string.
// Format: "N", "N:model", "N:model:effort", or "N:model@effort" where N is
// count, model is an optional alias, and effort is a reasoning-effort hint
// passed through to the agent template (currently consumed by Codex's
// `model_reasoning_effort` knob — see ntm#140, ntm-mjf7).
func ParseAgentSpec(value string) (AgentSpec, error) {
	return parseAgentSpec(value, false, true)
}

// parseAgentSpec parses a spec string, optionally relaxing the model charset for
// the Antigravity (agy) provider whose display model names contain spaces and
// parentheses (e.g. "Gemini 3.7 Flash (High)"). See agyModelPattern for why the
// widened charset is injection-safe. effortAtSuffix enables the `model@effort`
// shorthand; callers that know the agent type pass it per
// agentTypeSupportsEffortSuffix.
func parseAgentSpec(value string, relaxedModel bool, effortAtSuffix bool) (AgentSpec, error) {
	var spec AgentSpec

	modelRe := modelPattern
	allowedDesc := "letters, numbers, . _ / @ : + -"
	if relaxedModel {
		modelRe = agyModelPattern
		allowedDesc = "letters, numbers, spaces, parentheses, . _ / @ : + -"
	}

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
		// `model@effort` is shorthand for `model:effort` (ntm-mjf7): the
		// suffix after the last '@' is the reasoning-effort hint. Combining
		// both forms (`N:model@effort:effort`) is ambiguous and rejected.
		if at := strings.LastIndex(model, "@"); effortAtSuffix && at >= 0 {
			effort := strings.TrimSpace(model[at+1:])
			model = strings.TrimSpace(model[:at])
			if model == "" {
				return spec, fmt.Errorf("empty model before '@' in agent spec: %q", value)
			}
			if effort == "" {
				return spec, fmt.Errorf("empty reasoning effort after '@' in agent spec: %q (use N:model or N:model@effort)", value)
			}
			if len(parts) > 2 {
				return spec, fmt.Errorf("agent spec %q sets reasoning effort twice (both '@%s' and ':%s'); use one form", value, effort, strings.TrimSpace(parts[2]))
			}
			if !effortPattern.MatchString(effort) {
				return spec, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ + -", effort)
			}
			spec.ReasoningEffort = effort
		}
		if !modelRe.MatchString(model) {
			return spec, fmt.Errorf("invalid characters in model %q; allowed: %s", model, allowedDesc)
		}
		spec.Model = model
	}

	if len(parts) > 2 {
		effort := strings.TrimSpace(parts[2])
		if effort == "" {
			return spec, fmt.Errorf("empty reasoning effort in agent spec: %q", value)
		}
		if !effortPattern.MatchString(effort) {
			return spec, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ + -", effort)
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

// resolveAgentModel resolves the concrete model name for an agent, layering a
// plugin's declared default underneath the standard config resolution.
//
// Precedence, highest to lowest:
//  1. explicit model on the agent spec (e.g. `--agent=1:model`)
//  2. global config default for the agent type (built-in types only)
//  3. the plugin's `[agent.defaults] model` from its TOML
//
// ResolveModel already covers (1) and (2): a non-empty modelSpec always
// resolves to a non-empty name, and built-in types with a configured default
// return it for an empty spec. It returns "" only when there is neither an
// explicit model nor a global default — the exact gap where a plugin agent type
// (e.g. `--hermes=1`) would otherwise spawn with an empty model and fail Agent
// Mail registration. In that case we fall back to the plugin default, if any.
func resolveAgentModel(agentType AgentType, modelSpec string, pluginMap map[string]plugins.AgentPlugin) string {
	resolved := ResolveModel(agentType, modelSpec)
	if resolved != "" {
		return resolved
	}
	if p, ok := pluginMap[string(agentType)]; ok {
		return strings.TrimSpace(p.Defaults.Model)
	}
	return resolved
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
	// agy carries a display model name with spaces/parentheses (e.g.
	// "Gemini 3.7 Flash (High)"), so it uses the relaxed model charset. The
	// model@effort shorthand only applies to types with an effort knob so
	// '@' stays literal in other providers' model names.
	spec, err := parseAgentSpec(value, v.agentType == AgentTypeAntigravity, agentTypeSupportsEffortSuffix(v.agentType))
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
