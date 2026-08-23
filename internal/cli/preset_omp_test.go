package cli

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// shippedOmpPresetDir points at the canonical Oh My Pi preset versioned in
// the repository (ntm#262). The test loads it through the PRODUCTION plugin
// loader so schema drift in either the preset or the loader fails here.
func shippedOmpPresetDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Captured omp v18 chrome (same fixtures the status package pins): in flight
// omp renders a spinner + `Working… ⟨esc⟩` above the composer; at idle the
// composer's bottom border is the last line.
const (
	ompPresetIdleLine = "╰─                         ─╯"
	ompPresetWorkLine = " ⠙ Working… ⟨esc⟩"
)

func TestShippedOmpPresetLoadsThroughProductionLoader(t *testing.T) {
	agentpkg.UnregisterPlugins()
	t.Cleanup(agentpkg.UnregisterPlugins)

	loaded := registerAgentPluginTypes(shippedOmpPresetDir(t))
	if len(loaded) != 1 {
		t.Fatalf("examples/agents must hold exactly the omp preset, got %d plugins", len(loaded))
	}
	p := loaded[0]
	if p.Name != "omp" || p.Alias != "om" {
		t.Fatalf("preset identity = %q/%q, want omp/om", p.Name, p.Alias)
	}
	if p.ProbeCommand() != "omp" {
		t.Fatalf("ProbeCommand = %q, want omp (drives `ntm deps`)", p.ProbeCommand())
	}
	// No default model: omitting it lets OMP's own configuration choose.
	if p.Defaults.Model != "" {
		t.Fatalf("preset must not pin a default model, got %q", p.Defaults.Model)
	}

	// Both the name and the alias register as recognised types (spawn/send
	// selectors, robot type reporting).
	for _, name := range []string{"omp", "om"} {
		if !agentpkg.IsPluginType(agentpkg.AgentType(name)) {
			t.Fatalf("%s must be a registered plugin type", name)
		}
	}
}

// Guards the readiness regexes against drift: the shipped patterns must keep
// classifying the captured omp v18 chrome (working veto + idle border).
func TestShippedOmpPresetReadinessMatchesCapturedChrome(t *testing.T) {
	agentpkg.UnregisterPlugins()
	t.Cleanup(agentpkg.UnregisterPlugins)

	registerAgentPluginTypes(shippedOmpPresetDir(t))
	pp, ok := agentpkg.LookupPluginPatterns("omp")
	if !ok || !pp.Declared() {
		t.Fatal("shipped preset must register readiness patterns")
	}

	matchAny := func(res []*regexp.Regexp, line string) bool {
		for _, re := range res {
			if re.MatchString(line) {
				return true
			}
		}
		return false
	}

	if !matchAny(pp.Working, ompPresetWorkLine) {
		t.Fatalf("working patterns must match captured chrome %q", ompPresetWorkLine)
	}
	if !matchAny(pp.Idle, ompPresetIdleLine) {
		t.Fatalf("idle patterns must match captured composer border %q", ompPresetIdleLine)
	}

	// Planted negatives: the idle border must not read as working, and plain
	// prose must match neither pattern set.
	if matchAny(pp.Working, ompPresetIdleLine) {
		t.Fatalf("idle border %q must not match a working pattern", ompPresetIdleLine)
	}
	const prose = "just some ordinary output line"
	if matchAny(pp.Working, prose) || matchAny(pp.Idle, prose) {
		t.Fatalf("preset patterns must not match ordinary prose %q", prose)
	}
}

// The preset's command template must render model and thinking overrides
// through the production renderer, and stay a bare `omp` without them.
func TestShippedOmpPresetCommandTemplate(t *testing.T) {
	agentpkg.UnregisterPlugins()
	t.Cleanup(agentpkg.UnregisterPlugins)

	loaded := registerAgentPluginTypes(shippedOmpPresetDir(t))
	if len(loaded) != 1 {
		t.Fatalf("expected the omp preset, got %d plugins", len(loaded))
	}
	tmpl := loaded[0].Command

	bare, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{AgentType: "omp"})
	if err != nil {
		t.Fatalf("render without overrides: %v", err)
	}
	if bare != "omp" {
		t.Fatalf("bare render = %q, want omp (OMP's own config chooses the model)", bare)
	}

	full, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{
		AgentType:       "omp",
		Model:           "gpt-6",
		ModelRequested:  true,
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("render with overrides: %v", err)
	}
	if !strings.Contains(full, "--model 'gpt-6'") || !strings.Contains(full, "--thinking 'high'") {
		t.Fatalf("override render = %q, want --model 'gpt-6' and --thinking 'high'", full)
	}
}
