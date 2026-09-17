package cli

import (
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Oh My Pi (omp) CLI surfaces: every command that lists or switches on agent
// types accepts omp the same way it accepts the other operable harnesses.

func TestOmpSpawnAddAdoptSendFlags(t *testing.T) {
	if f := newSpawnCmd().Flags().Lookup("omp"); f == nil || !strings.Contains(f.Usage, "--thinking") {
		t.Fatalf("spawn --omp missing or undocumented: %+v", f)
	}
	if newAddCmd().Flags().Lookup("omp") == nil {
		t.Fatal("add command omits --omp")
	}
	if newAdoptCmd().Flags().Lookup("omp") == nil {
		t.Fatal("adopt command omits --omp")
	}
	if f := newSendCmd().Flags().Lookup("omp"); f == nil || f.NoOptDefVal != "true" {
		t.Fatalf("send --omp selector missing or not bool-style: %+v", f)
	}
	if newActivityCmd().Flags().Lookup("omp") == nil {
		t.Fatal("activity command omits --omp filter")
	}
	if newResumeCmd().Flags().Lookup("omp") == nil {
		t.Fatal("resume command omits --omp")
	}
	if f := newRespawnCmd().Flags().Lookup("type"); f == nil || !strings.Contains(f.Usage, "omp") {
		t.Fatalf("respawn --type help must list omp: %+v", f)
	}
}

func TestOmpAgentSpecGrammar(t *testing.T) {
	tests := []struct {
		value      string
		wantCount  int
		wantModel  string
		wantEffort string
	}{
		{value: "8", wantCount: 8},
		{value: "2:opus", wantCount: 2, wantModel: "opus"},
		{value: "2:opus:high", wantCount: 2, wantModel: "opus", wantEffort: "high"},
		// omp has a reasoning knob (--thinking), so model@effort is the
		// effort shorthand exactly like cc/cod/grok.
		{value: "1:openrouter/stealth/union-alpha@xhigh", wantCount: 1, wantModel: "openrouter/stealth/union-alpha", wantEffort: "xhigh"},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			var specs AgentSpecs
			if err := NewAgentSpecsValue(AgentTypeOmp, &specs).Set(tc.value); err != nil {
				t.Fatalf("Set(%q): %v", tc.value, err)
			}
			if len(specs) != 1 || specs[0].Type != AgentTypeOmp || specs[0].Count != tc.wantCount ||
				specs[0].Model != tc.wantModel || specs[0].ReasoningEffort != tc.wantEffort {
				t.Fatalf("spec = %+v", specs)
			}
		})
	}
	spec, err := parseRobotSpawnAgentFlag("--spawn-omp", "8:opus@high", AgentTypeOmp)
	if err != nil || spec.Count != 8 || spec.Model != "opus" || spec.ReasoningEffort != "high" {
		t.Fatalf("parseRobotSpawnAgentFlag(--spawn-omp) = (%+v, %v)", spec, err)
	}
}

func TestOmpSpawnCommandTemplate(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = config.Default()
	tmpl, env, err := spawnAgentCommandTemplate(AgentTypeOmp, nil, "")
	if err != nil || env != nil || tmpl != config.DefaultOmpCommand {
		t.Fatalf("default omp template = (%q, %v, %v)", tmpl, env, err)
	}
	rendered, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{AgentType: string(AgentTypeOmp)})
	if err != nil || rendered != "omp --auto-approve" {
		t.Fatalf("bare omp launch = (%q, %v), want omp --auto-approve", rendered, err)
	}

	// An [agents] omp override applies to spawn, add, and restore alike.
	cfg.Agents.Omp = "omp --auto-approve --profile swarm{{if .Model}} --model {{shellQuote .Model}}{{end}}"
	for name, resolve := range map[string]func() (string, error){
		"spawn": func() (string, error) { s, _, err := spawnAgentCommandTemplate(AgentTypeOmp, nil, ""); return s, err },
		"add": func() (string, error) {
			s, _, err := resolveAddAgentCommandTemplate(AgentTypeOmp, nil, "")
			return s, err
		},
		"restore": func() (string, error) {
			s, typ, ok := agentTemplateAndType("omp")
			if !ok || typ != AgentTypeOmp {
				t.Fatalf("agentTemplateAndType(omp) = (%q, %q, %v)", s, typ, ok)
			}
			return s, nil
		},
	} {
		got, err := resolve()
		if err != nil || got != cfg.Agents.Omp {
			t.Fatalf("%s omp template = (%q, %v), want the [agents] omp override", name, got, err)
		}
	}

	// A blank override falls back to the default rather than launching "".
	cfg.Agents.Omp = ""
	if got, _, _ := spawnAgentCommandTemplate(AgentTypeOmp, nil, ""); got != config.DefaultOmpCommand {
		t.Fatalf("blank [agents] omp = %q, want default", got)
	}
}

func TestOmpSpawnCounts(t *testing.T) {
	opts := SpawnOptions{OmpCount: 3}
	normalizeSpawnOptions(&opts)
	if opts.OmpCount != 3 || len(opts.Agents) != 3 {
		t.Fatalf("normalized omp spawn = count:%d agents:%+v", opts.OmpCount, opts.Agents)
	}
	for i, spec := range opts.Agents {
		if spec.Type != AgentTypeOmp || spec.Index != i+1 {
			t.Fatalf("omp agent[%d] = %+v", i, spec)
		}
	}
	if got := legacySpawnTotalAgentCount(SpawnOptions{CCCount: 1, OmpCount: 8}); got != 9 {
		t.Fatalf("total = %d, want 9", got)
	}
	env := spawnHookCountEnv(8, SpawnOptions{OmpCount: 8})
	if env["NTM_AGENT_COUNT_OMP"] != "8" {
		t.Fatalf("NTM_AGENT_COUNT_OMP = %q", env["NTM_AGENT_COUNT_OMP"])
	}
	if fields := spawnSessionCreatedEventFields(SpawnOptions{OmpCount: 8}, "/tmp/p"); fields["agent_omp"] != "8" || fields["agent_count"] != "8" {
		t.Fatalf("session-created fields = %+v", fields)
	}
	if got, err := parseLocalFallbackProvider("oh-my-pi"); err != nil || got != AgentTypeOmp {
		t.Fatalf("local fallback provider = (%q, %v), want omp", got, err)
	}
	if err := validateSpawnAgentTypes([]FlatAgent{{Type: AgentTypeOmp, Index: 1}}, nil, nil); err != nil {
		t.Fatalf("omp must be a valid spawn type: %v", err)
	}

	result := spawnWizardResultFromCounts(map[string]int{"omp": 8})
	if result.OmpCount != 8 {
		t.Fatalf("wizard OmpCount = %d", result.OmpCount)
	}
	if specs := wizardAgentSpecs(result); len(specs) != 1 || specs[0] != (AgentSpec{Type: AgentTypeOmp, Count: 8}) {
		t.Fatalf("wizard specs = %+v", specs)
	}
	if got := formatWizardAgentCountSummary(map[string]int{"cc": 1, "omp": 8}); got != "cc:1 omp:8" {
		t.Fatalf("wizard summary = %q", got)
	}

	profileOpts := SpawnOptions{}
	ApplySessionProfileToSpawnOptions(&profileOpts, &SessionProfile{Omp: 4})
	if profileOpts.OmpCount != 4 {
		t.Fatalf("session profile omp count = %d, want 4", profileOpts.OmpCount)
	}
	if err := (SessionProfile{Omp: -1}).Validate(); err == nil {
		t.Fatal("negative omp profile count must be rejected")
	}
}

func TestOmpPaneCountsAndNames(t *testing.T) {
	counts := output.AgentCountsResponse{}
	incrementAgentCounts(&counts, tmux.AgentType("oh-my-pi"))
	if counts.Omp != 1 || counts.Other != 0 || counts.Total != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if got := agentTypeToString(tmux.AgentOmp); got != "omp" {
		t.Fatalf("agentTypeToString(omp) = %q", got)
	}

	adopted := AdoptedAgentCounts{}
	adopted.increment(agentpkg.AgentType(" OMP "))
	if adopted.Omp != 1 || adopted.countFor(agentpkg.AgentType("oh_my_pi")) != 1 || adopted.Total() != 1 {
		t.Fatalf("adopted counts = %+v", adopted)
	}

	pane := tmux.Pane{Type: tmux.AgentOmp, Title: "proj__omp_2", NTMIndex: 2}
	if got := detectAgentTypeFromPane(pane); got != "omp" {
		t.Fatalf("detectAgentTypeFromPane = %q", got)
	}
	if got := detectAgentTypeFromTitle("proj__omp_2"); got != "omp" {
		t.Fatalf("detectAgentTypeFromTitle = %q", got)
	}
	if !passesFilter("omp", pane, activityOptions{filterOmp: true}, false) {
		t.Fatal("--omp activity filter must pass omp panes")
	}
	if passesFilter("claude", tmux.Pane{Type: tmux.AgentClaude}, activityOptions{filterOmp: true}, false) {
		t.Fatal("--omp activity filter must drop non-omp panes")
	}
	if got := string(activityAgentTypeColor("omp", theme.Current())); got != string(theme.Current().Sky) {
		t.Fatalf("omp activity color = %q, want sky", got)
	}
	if !isInterruptibleAgentPane(pane) {
		t.Fatal("omp panes are interruptible agent panes")
	}
	if !supportsProfileSwitchAgentType(tmux.AgentOmp) || !isSupportedWorkAgentType(agentpkg.AgentTypeOmp) {
		t.Fatal("omp must support profile switch and rebalancing")
	}
	if got := getAgentTypeShort(tmux.AgentOmp); got != "omp" {
		t.Fatalf("getAgentTypeShort(omp) = %q", got)
	}
	if got := normalizeEnsembleAgentType("oh-my-pi"); got != "omp" {
		t.Fatalf("normalizeEnsembleAgentType = %q", got)
	}
	if got := dashboardPaneTypeSummary([]tmux.Pane{pane}); !strings.Contains(got, "Omp=1") || !strings.Contains(got, "Other=0") {
		t.Fatalf("dashboard summary = %q", got)
	}
	if !isAnalyticsAgentType("omp") {
		t.Fatal("omp must be an analytics agent type")
	}
}

func TestOmpDepsAndDoctor(t *testing.T) {
	found := false
	for _, c := range builtinDepChecks() {
		if c.Command == "omp" {
			found = c.Category == "AI Agents" && !c.Required && strings.Contains(c.InstallHint, "omp setup")
		}
	}
	if !found {
		t.Fatal("ntm deps must probe the omp binary as an optional AI agent with a setup hint")
	}
}
