package ensemble

import "time"

// EmbeddedEnsembles contains the 9 built-in ensemble presets.
// These use only core-tier modes and are the primary user interface.
var EmbeddedEnsembles = []EnsemblePreset{
	{
		Name:        "project-diagnosis",
		DisplayName: "Project Diagnosis",
		Description: "Comprehensive project health analysis",
		Modes: []ModeRef{
			ModeRefFromID("systems-thinking"),
			ModeRefFromID("worst-case"),
			ModeRefFromID("dependency-mapping"),
			ModeRefFromID("failure-mode"),
			ModeRefFromID("perspective-taking"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyAdversarial,
			MinConfidence: 0.6,
			MaxFindings:   10,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 5000,
			MaxTotalTokens:   30000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"analysis", "health"},
	},
	{
		Name:        "idea-forge",
		DisplayName: "Idea Forge",
		Description: "Divergent feature generation and innovation",
		Modes: []ModeRef{
			ModeRefFromID("conceptual-blending"),
			ModeRefFromID("analogical"),
			ModeRefFromID("option-generation"),
			ModeRefFromID("second-order-effects"),
			ModeRefFromID("prototype-reasoning"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyCreative,
			MinConfidence: 0.5,
			MaxFindings:   15,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4000,
			MaxTotalTokens:   30000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"creative", "ideation"},
	},
	{
		Name:        "spec-critique",
		DisplayName: "Spec Critique",
		Description: "Requirements interrogation and edge case hunting",
		Modes: []ModeRef{
			ModeRefFromID("deductive"),
			ModeRefFromID("ambiguity-detection"),
			ModeRefFromID("edge-case"),
			ModeRefFromID("test-plan"),
			ModeRefFromID("perspective-taking"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyConsensus,
			MinConfidence: 0.7,
			MaxFindings:   12,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4000,
			MaxTotalTokens:   25000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     8 * time.Minute,
		},
		Tags: []string{"requirements", "validation"},
	},
	{
		Name:        "safety-risk",
		DisplayName: "Safety / Risk Analysis",
		Description: "Threat modeling and security review",
		Modes: []ModeRef{
			ModeRefFromID("worst-case"),
			ModeRefFromID("adversarial-review"),
			ModeRefFromID("compliance"),
			ModeRefFromID("failure-mode"),
			ModeRefFromID("root-cause"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyAdversarial,
			MinConfidence: 0.7,
			MaxFindings:   10,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 5000,
			MaxTotalTokens:   30000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"security", "risk"},
	},
	{
		Name:        "architecture-review",
		DisplayName: "Architecture Review",
		Description: "Multi-perspective architecture analysis",
		Modes: []ModeRef{
			ModeRefFromID("argument-mapping"),
			ModeRefFromID("root-cause"),
			ModeRefFromID("systems-thinking"),
			ModeRefFromID("perspective-taking"),
			ModeRefFromID("strategic-planning"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyDeliberative,
			MinConfidence: 0.65,
			MaxFindings:   12,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 5000,
			MaxTotalTokens:   30000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"architecture", "design"},
	},
	{
		Name:        "tech-debt-triage",
		DisplayName: "Tech Debt Triage",
		Description: "Technical debt assessment and prioritization",
		Modes: []ModeRef{
			ModeRefFromID("dependency-mapping"),
			ModeRefFromID("failure-mode"),
			ModeRefFromID("resource-allocation"),
			ModeRefFromID("prioritization"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyPrioritized,
			MinConfidence: 0.6,
			MaxFindings:   15,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4000,
			MaxTotalTokens:   20000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     8 * time.Minute,
		},
		Tags: []string{"maintenance", "priority"},
	},
	{
		Name:        "bug-hunt",
		DisplayName: "Bug Hunt",
		Description: "Multi-angle bug detection and verification",
		Modes: []ModeRef{
			ModeRefFromID("clinical-operational"),
			ModeRefFromID("inductive"),
			ModeRefFromID("adversarial-review"),
			ModeRefFromID("deductive"),
			ModeRefFromID("causal-inference"),
			ModeRefFromID("type-theoretic"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyAnalytical,
			MinConfidence: 0.7,
			MaxFindings:   10,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4500,
			MaxTotalTokens:   28000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"debugging", "verification"},
	},
	{
		Name:        "root-cause-analysis",
		DisplayName: "Root Cause Analysis",
		Description: "Why did this fail? Causal + counterfactual analysis",
		Modes: []ModeRef{
			ModeRefFromID("clinical-operational"),
			ModeRefFromID("causal-inference"),
			ModeRefFromID("counterfactual"),
			ModeRefFromID("inductive"),
			ModeRefFromID("debiasing"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyDeliberative,
			MinConfidence: 0.7,
			MaxFindings:   8,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4500,
			MaxTotalTokens:   28000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     10 * time.Minute,
		},
		Tags: []string{"analysis", "debugging"},
	},
	{
		Name:        "strategic-planning",
		DisplayName: "Strategic Planning",
		Description: "Long-term planning under uncertainty and constraints",
		Modes: []ModeRef{
			ModeRefFromID("strategic-planning"),
			ModeRefFromID("systems-thinking"),
			ModeRefFromID("decision-under-uncertainty"),
			ModeRefFromID("second-order-effects"),
			ModeRefFromID("resource-allocation"),
			ModeRefFromID("prioritization"),
			ModeRefFromID("perspective-taking"),
		},
		Synthesis: SynthesisConfig{
			Strategy:      StrategyDeliberative,
			MinConfidence: 0.6,
			MaxFindings:   12,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 4500,
			MaxTotalTokens:   32000,
			TimeoutPerMode:   2 * time.Minute,
			TotalTimeout:     12 * time.Minute,
		},
		Tags: []string{"planning", "strategy"},
	},
}

// EnsembleNames returns the names of all embedded ensembles in order.
func EnsembleNames() []string {
	names := make([]string, len(EmbeddedEnsembles))
	for i, e := range EmbeddedEnsembles {
		names[i] = e.Name
	}
	return names
}

// GetEmbeddedEnsemble returns an embedded ensemble by name, or nil if not found.
func GetEmbeddedEnsemble(name string) *EnsemblePreset {
	for i := range EmbeddedEnsembles {
		if EmbeddedEnsembles[i].Name == name {
			return &EmbeddedEnsembles[i]
		}
	}
	return nil
}
