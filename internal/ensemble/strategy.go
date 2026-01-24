package ensemble

import "fmt"

// StrategyConfig provides metadata and configuration for a synthesis strategy.
type StrategyConfig struct {
	// Name is the canonical strategy identifier.
	Name SynthesisStrategy `json:"name" toml:"name" yaml:"name"`

	// Description explains what this strategy does.
	Description string `json:"description" toml:"description" yaml:"description"`

	// RequiresAgent indicates whether a synthesizer agent is needed.
	RequiresAgent bool `json:"requires_agent" toml:"requires_agent" yaml:"requires_agent"`

	// SynthesizerMode is the recommended mode for the synthesizer agent (if any).
	SynthesizerMode string `json:"synthesizer_mode,omitempty" toml:"synthesizer_mode" yaml:"synthesizer_mode,omitempty"`

	// OutputFocus describes what the synthesis output emphasizes.
	OutputFocus []string `json:"output_focus" toml:"output_focus" yaml:"output_focus"`

	// BestFor describes ideal use cases for this strategy.
	BestFor []string `json:"best_for" toml:"best_for" yaml:"best_for"`

	// TemplateKey is the key for looking up the synthesis prompt template.
	TemplateKey string `json:"template_key,omitempty" toml:"template_key" yaml:"template_key,omitempty"`
}

// strategyRegistry holds the canonical strategy configurations.
var strategyRegistry = []*StrategyConfig{
	{
		Name:          StrategyManual,
		Description:   "Mechanical merge of outputs without a synthesizer agent",
		RequiresAgent: false,
		OutputFocus:   []string{"concatenated findings", "merged recommendations"},
		BestFor:       []string{"Simple aggregation", "Debugging", "When synthesis overhead is unwanted"},
		TemplateKey:   "synthesis_manual",
	},
	{
		Name:            StrategyAdversarial,
		Description:     "Challenge/defense synthesis where outputs are stress-tested",
		RequiresAgent:   true,
		SynthesizerMode: "adversarial-review",
		OutputFocus:     []string{"vulnerabilities", "counterarguments", "robust conclusions"},
		BestFor:         []string{"Security review", "Risk assessment", "Stress-testing proposals"},
		TemplateKey:     "synthesis_adversarial",
	},
	{
		Name:            StrategyConsensus,
		Description:     "Find agreement points across mode outputs",
		RequiresAgent:   true,
		SynthesizerMode: "meta-evaluation",
		OutputFocus:     []string{"agreement areas", "confidence-weighted conclusions", "minority dissents"},
		BestFor:         []string{"Multi-perspective validation", "Building confidence", "Reducing individual bias"},
		TemplateKey:     "synthesis_consensus",
	},
	{
		Name:            StrategyCreative,
		Description:     "Recombine outputs into novel insights and unexpected connections",
		RequiresAgent:   true,
		SynthesizerMode: "conceptual-blending",
		OutputFocus:     []string{"novel combinations", "emergent patterns", "unexpected connections"},
		BestFor:         []string{"Innovation", "Ideation", "Cross-domain discovery"},
		TemplateKey:     "synthesis_creative",
	},
	{
		Name:            StrategyAnalytical,
		Description:     "Systematic decomposition and structured comparison of outputs",
		RequiresAgent:   true,
		SynthesizerMode: "systems-thinking",
		OutputFocus:     []string{"structured comparison", "gap analysis", "systematic coverage"},
		BestFor:         []string{"Architecture review", "Comprehensive analysis", "Coverage verification"},
		TemplateKey:     "synthesis_analytical",
	},
	{
		Name:            StrategyDeliberative,
		Description:     "Structured deliberation weighing tradeoffs between outputs",
		RequiresAgent:   true,
		SynthesizerMode: "decision-analysis",
		OutputFocus:     []string{"tradeoff analysis", "decision rationale", "weighted recommendations"},
		BestFor:         []string{"Decision making", "Policy design", "Tradeoff-heavy problems"},
		TemplateKey:     "synthesis_deliberative",
	},
	{
		Name:            StrategyPrioritized,
		Description:     "Rank and select outputs by quality, confidence, and relevance",
		RequiresAgent:   true,
		SynthesizerMode: "meta-evaluation",
		OutputFocus:     []string{"ranked findings", "quality scores", "filtered top insights"},
		BestFor:         []string{"Information triage", "Best-of selection", "Time-constrained reviews"},
		TemplateKey:     "synthesis_prioritized",
	},
	{
		Name:            StrategyDialectical,
		Description:     "Agent-led thesis/antithesis debate driving toward synthesis",
		RequiresAgent:   true,
		SynthesizerMode: "dialectical",
		OutputFocus:     []string{"thesis/antithesis pairs", "resolved tensions", "higher-order conclusions"},
		BestFor:         []string{"Controversial topics", "Exploring opposing views", "Resolving contradictions"},
		TemplateKey:     "synthesis_dialectical",
	},
	{
		Name:            StrategyMetaReasoning,
		Description:     "Meta-cognitive synthesizer that reasons about the reasoning itself",
		RequiresAgent:   true,
		SynthesizerMode: "meta-evaluation",
		OutputFocus:     []string{"reasoning quality assessment", "epistemic status", "confidence calibration"},
		BestFor:         []string{"High-stakes decisions", "Reasoning audits", "Calibration checks"},
		TemplateKey:     "synthesis_meta",
	},
	{
		Name:          StrategyVoting,
		Description:   "Structured score/vote aggregation across mode outputs",
		RequiresAgent: false,
		OutputFocus:   []string{"vote tallies", "score distributions", "majority/minority positions"},
		BestFor:       []string{"Democratic aggregation", "Multi-agent consensus", "Quick polls"},
		TemplateKey:   "synthesis_voting",
	},
	{
		Name:            StrategyArgumentation,
		Description:     "Build support/attack graph mapping relationships between claims",
		RequiresAgent:   true,
		SynthesizerMode: "argumentation",
		OutputFocus:     []string{"support/attack edges", "grounded claims", "defeated arguments"},
		BestFor:         []string{"Argument mapping", "Debate analysis", "Legal/policy reasoning"},
		TemplateKey:     "synthesis_argumentation",
	},
}

// strategyMap provides O(1) lookup by strategy name.
var strategyMap map[SynthesisStrategy]*StrategyConfig

func init() {
	strategyMap = make(map[SynthesisStrategy]*StrategyConfig, len(strategyRegistry))
	for _, s := range strategyRegistry {
		strategyMap[s.Name] = s
	}
}

// GetStrategy returns the configuration for a named strategy.
// Returns an error if the strategy is not recognized.
func GetStrategy(name string) (*StrategyConfig, error) {
	s, ok := strategyMap[SynthesisStrategy(name)]
	if !ok {
		return nil, fmt.Errorf("unknown synthesis strategy %q; use ListStrategies() for valid options", name)
	}
	return s, nil
}

// ListStrategies returns all strategy configurations in canonical order.
func ListStrategies() []*StrategyConfig {
	out := make([]*StrategyConfig, len(strategyRegistry))
	copy(out, strategyRegistry)
	return out
}

// ValidateStrategy returns true if the given name is a valid synthesis strategy.
func ValidateStrategy(name string) bool {
	_, ok := strategyMap[SynthesisStrategy(name)]
	return ok
}

// deprecatedStrategies maps removed strategy names to their replacements.
var deprecatedStrategies = map[string]string{
	"debate":     "dialectical",
	"weighted":   "prioritized",
	"sequential": "manual",
	"best-of":    "prioritized",
}

// MigrateStrategy converts deprecated strategy names to their canonical replacements.
// Returns the canonical name and true if migration occurred, or the original name and false.
func MigrateStrategy(name string) (string, bool) {
	if replacement, ok := deprecatedStrategies[name]; ok {
		return replacement, true
	}
	return name, false
}

// ValidateOrMigrateStrategy checks a strategy name, attempting migration for deprecated names.
// Returns the canonical strategy name or an error with migration guidance.
func ValidateOrMigrateStrategy(name string) (SynthesisStrategy, error) {
	if ValidateStrategy(name) {
		return SynthesisStrategy(name), nil
	}
	if replacement, migrated := MigrateStrategy(name); migrated {
		return "", fmt.Errorf("strategy %q is deprecated; use %q instead", name, replacement)
	}
	return "", fmt.Errorf("unknown synthesis strategy %q; use ListStrategies() for valid options", name)
}
