package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func writePluginTOML(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "omp.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ompPluginTOML = `[agent]
name = "omp"
alias = "om"
command = "omp{{if .Model}} --model {{shellQuote .Model}}{{end}}"
description = "Oh My Pi coding agent"

[agent.readiness]
working_patterns = ['⟨esc⟩']
idle_patterns = ['^\s*╰─.*─╯\s*$']
`

func TestRegisterAgentPluginTypes(t *testing.T) {
	if got := registerAgentPluginTypes(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing dir must yield no plugins, got %v", got)
	}
	dir := filepath.Join(t.TempDir(), "agents")
	writePluginTOML(t, dir, ompPluginTOML)
	loaded := registerAgentPluginTypes(dir)
	if len(loaded) != 1 || loaded[0].Name != "omp" {
		t.Fatalf("loaded = %+v", loaded)
	}
	for _, name := range []string{"omp", "om"} {
		if !agentpkg.IsPluginType(agentpkg.AgentType(name)) {
			t.Fatalf("%s must be a registered plugin type", name)
		}
	}
	pp, _ := agentpkg.LookupPluginPatterns("omp")
	if len(pp.Working) != 1 || len(pp.Idle) != 1 {
		t.Fatalf("readiness patterns not registered: %+v", pp)
	}

	// An invalid pattern is reported and skipped without breaking the type.
	// Use a distinct plugin name so this assertion cannot inherit the valid
	// OMP registration from the first half of the test.
	invalidTOML := strings.NewReplacer(
		`name = "omp"`, `name = "badready"`,
		`alias = "om"`, `alias = "br"`,
		`'⟨esc⟩'`, `'('`,
	).Replace(ompPluginTOML)
	writePluginTOML(t, dir, invalidTOML)
	registerAgentPluginTypes(dir)
	if !agentpkg.IsPluginType("badready") {
		t.Fatal("a plugin with an invalid readiness pattern must still be a recognised type")
	}
	if pp, _ := agentpkg.LookupPluginPatterns("badready"); pp.Declared() {
		t.Fatal("invalid pattern set must not be partially registered")
	}
}

func TestRegisterPluginSendFlags(t *testing.T) {
	var targets SendTargets
	cmd := newSendCmd()
	p := plugins.AgentPlugin{Name: "hermes", Alias: "cc", Command: "hermes"} // alias collides with --cc
	registerPluginSendFlags(cmd, p, &targets)

	flag := cmd.Flags().Lookup("hermes")
	if flag == nil || flag.NoOptDefVal != "true" {
		t.Fatalf("--hermes must be registered as a bool-style selector, got %+v", flag)
	}
	if got := cmd.Flags().Lookup("cc").Usage; !strings.Contains(got, "Claude") {
		t.Fatalf("built-in --cc must win the collision, usage = %q", got)
	}
	if err := flag.Value.Set("true"); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Type != AgentType("hermes") || targets[0].Variant != "" {
		t.Fatalf("targets = %+v", targets)
	}
	if !targets.MatchesPane(tmux.Pane{Type: tmux.AgentType("hermes")}) {
		t.Fatal("plugin selector must match a pane of the plugin type")
	}
	if targets.MatchesPane(tmux.Pane{Type: tmux.AgentClaude}) {
		t.Fatal("plugin selector must not match other types")
	}
}

func TestSendCmdExposesOpencodeAndGrokSelectors(t *testing.T) {
	cmd := newSendCmd()
	for _, name := range []string{"oc", "grok", "agy", "cc"} {
		f := cmd.Flags().Lookup(name)
		if f == nil || f.NoOptDefVal != "true" {
			t.Fatalf("--%s selector missing or not bool-style: %+v", name, f)
		}
	}
}

func TestPluginDepChecks(t *testing.T) {
	checks := pluginDepChecks([]plugins.AgentPlugin{
		{Name: "omp", Command: "omp{{if .Model}} --model x{{end}}"},
		{Name: "dyn", Command: "{{.Binary}}"}, // undeterminable executable: skipped
	})
	if len(checks) != 1 {
		t.Fatalf("checks = %+v, want exactly the omp probe", checks)
	}
	c := checks[0]
	if c.Name != "omp (plugin)" || c.Command != "omp" || c.Category != "AI Agents" || c.Required {
		t.Fatalf("probe = %+v", c)
	}
}

func TestDefaultDepChecksIncludesOpencode(t *testing.T) {
	found := false
	for _, c := range builtinDepChecks() {
		if c.Command == "opencode" && c.Category == "AI Agents" {
			found = true
		}
	}
	if !found {
		t.Fatal("ntm deps must probe the opencode binary")
	}
}

func TestDelegatedModelPlaceholder(t *testing.T) {
	if got := delegatedModelPlaceholder("opencode"); got != "opencode/cli-default" {
		t.Fatalf("delegated model = %q", got)
	}
	if got := delegatedModelPlaceholder(""); got != "agent/cli-default" {
		t.Fatalf("empty program delegated model = %q", got)
	}
	if got := modelDefaultKeyForType("oc"); got != "opencode" {
		t.Fatalf("default key for oc = %q", got)
	}
	if got := modelDefaultKeyForType("omp"); got != "omp" {
		t.Fatalf("default key for plugin = %q", got)
	}
}
