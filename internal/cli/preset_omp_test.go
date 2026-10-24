package cli

import (
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/shellword"
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

func shippedAgentPresets(t *testing.T) map[string]plugins.AgentPlugin {
	t.Helper()
	loaded := registerAgentPluginTypes(shippedOmpPresetDir(t))
	names := make([]string, 0, len(loaded))
	presets := make(map[string]plugins.AgentPlugin, len(loaded))
	for _, preset := range loaded {
		names = append(names, preset.Name)
		presets[preset.Name] = preset
	}
	sort.Strings(names)
	if want := []string{"omp", "pi"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("examples/agents loaded presets = %v, want exactly %v", names, want)
	}
	return presets
}

// Captured omp v18 chrome (same fixtures the status package pins): in flight
// omp renders a spinner + `Working… ⟨esc⟩` above the composer; at idle the
// composer's bottom border is the last line.
const (
	ompPresetIdleLine = "╰─                         ─╯"
	ompPresetWorkLine = " ⠙ Working… ⟨esc⟩"
)

func TestShippedOmpPresetLoadsThroughProductionLoader(t *testing.T) {
	p := shippedAgentPresets(t)["omp"]
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
	tmpl := shippedAgentPresets(t)["omp"].Command

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

func TestShippedPiPresetLoadsThroughProductionLoader(t *testing.T) {
	p := shippedAgentPresets(t)["pi"]
	if p.Name != "pi" || p.Alias != "" {
		t.Fatalf("pi preset identity = %q/%q, want pi with no alias", p.Name, p.Alias)
	}
	if got := p.ProbeCommand(); got != "pi" {
		t.Fatalf("pi dependency probe = %q, want pi", got)
	}
	if p.Defaults.Model != "" {
		t.Fatalf("pi preset must preserve pi's configured model, got default %q", p.Defaults.Model)
	}
	if !agentpkg.IsPluginType("pi") {
		t.Fatal("shipped pi preset did not register its agent type")
	}
}

func TestShippedPiPresetCommandTemplate(t *testing.T) {
	tmpl := shippedAgentPresets(t)["pi"].Command
	const model = "anthropic/claude-sonnet-4-5"
	const prompt = "/work/persona's files/reviewer.md"
	for _, tc := range []struct {
		name string
		vars config.AgentTemplateVars
		want []string
	}{
		{"bare", config.AgentTemplateVars{}, []string{"pi"}},
		{"model", config.AgentTemplateVars{Model: model, ModelRequested: true}, []string{"pi", "--model", model}},
		{"effort", config.AgentTemplateVars{ReasoningEffort: "high"}, []string{"pi", "--thinking", "high"}},
		{"persona", config.AgentTemplateVars{PersonaName: "reviewer", SystemPromptFile: prompt}, []string{"pi", "--append-system-prompt", prompt}},
		{"all", config.AgentTemplateVars{Model: model, ModelRequested: true, ReasoningEffort: "high", PersonaName: "reviewer", SystemPromptFile: prompt},
			[]string{"pi", "--model", model, "--thinking", "high", "--append-system-prompt", prompt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.vars.AgentType = "pi"
			command, err := config.GenerateAgentCommand(tmpl, tc.vars)
			if err != nil {
				t.Fatalf("render pi preset: %v", err)
			}
			if tc.name == "bare" && command != "pi" {
				t.Fatalf("bare pi render = %q, want pi", command)
			}
			words, err := shellword.Literal(command)
			if err != nil {
				t.Fatalf("pi preset produced nonliteral shell syntax: %q: %v", command, err)
			}
			argv := make([]string, 0, len(words))
			for _, word := range words {
				argv = append(argv, word.Value)
			}
			if !reflect.DeepEqual(argv, tc.want) {
				t.Fatalf("pi preset arguments = %q, want %q", argv, tc.want)
			}
		})
	}
}

func TestShippedPiPresetWorkingChromeVetoesIdleComposer(t *testing.T) {
	shippedAgentPresets(t)
	const rule = "────────────────────────────────────────────────"
	const footer = "/work/project\n↑1.2k ↓450 $0.004 (0.3%) · claude-sonnet-4-5"
	idle := " ▀▀█  v1.3.0\nThe requested change is complete.\n\n" + rule + "\n\n" + rule + "\n" + footer
	working := " ▀▀█  v1.3.0\nInspecting the source.\n\n── ⠙ Working (esc to interrupt) ────────────────\n\n" + rule + "\n" + footer
	match, declared := agentpkg.MatchPluginReadiness(working, "pi")
	if !declared || !match.Working || !match.Idle {
		t.Fatalf("working pi fixture must contain both live work and persistent idle-border matches: %+v, declared=%v", match, declared)
	}
	parser := agentpkg.NewParser()
	for _, tc := range []struct {
		name, frame string
		working     bool
		idle        bool
	}{
		{"idle", idle, false, true},
		{"working with idle border", working, true, false},
		{"old working chrome", working + "\n" + strings.Repeat("historical output\n", 24) + idle, false, true},
		{"plain prose", "The word Working and a rule ───── are mentioned in prose.", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := parser.ParseWithHint(tc.frame, "pi")
			if err != nil {
				t.Fatal(err)
			}
			if state.Type != "pi" || state.IsWorking != tc.working || state.IsIdle != tc.idle {
				t.Fatalf("pi preset readiness = %+v; want working=%v idle=%v", state, tc.working, tc.idle)
			}
		})
	}
	if match, declared := agentpkg.MatchPluginReadiness(ompPresetWorkLine+"\n"+ompPresetIdleLine, "pi"); !declared || match.Working || match.Idle {
		t.Fatalf("pi preset classified omp-specific composer chrome: %+v, declared=%v", match, declared)
	}
}
