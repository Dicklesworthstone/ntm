package cli

// Precedence guard for GitHub issue #319.
//
// #319 asks for a spawn-time seat selection that fills in an account persona
// when the operator did not name one, and states the hard constraint that an
// explicit --persona=codex-* / claude-* "still wins and is never overridden
// mid-pane".
//
// That constraint holds today because of the SHAPE of persona dispatch rather
// than any explicit check: a --persona spec becomes its own AgentSpec whose
// Model IS the persona name, and that name is the key the persona map is
// looked up under. A bare --cod=N spec carries a model alias (or nothing) that
// is not a persona name, so it misses the map and leaves PersonaName empty —
// which is exactly the gap #319 is about, and exactly the slot any seat
// selection may fill.
//
// This pins both halves, so seat selection cannot later be wired in a way that
// displaces an operator's explicit choice.

import (
	"os"
	"path/filepath"
	"testing"
)

// writePersonaRegistry installs a project-local persona registry with two
// account personas shaped like the reporter's shallow CAAM profiles.
func writePersonaRegistry(t *testing.T) string {
	t.Helper()
	// Isolate HOME so the developer's own persona config cannot change the
	// registry this test reasons about.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ntm"), 0o755); err != nil {
		t.Fatalf("create project persona dir: %v", err)
	}

	body := `[[personas]]
name = "codex-spend-me"
description = "CAAM shallow seat spend-me"
agent_type = "codex"
system_prompt = "You are a worker on seat codex-spend-me."

[[personas]]
name = "codex-reserve"
description = "CAAM shallow seat reserve"
agent_type = "codex"
system_prompt = "You are a worker on seat codex-reserve."
`
	path := filepath.Join(dir, ".ntm", "personas.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write project personas: %v", err)
	}
	return dir
}

// TestExplicitPersonaBecomesItsOwnSpecAndWins: the persona name travels as the
// spec's Model, which is the key the persona map is keyed by. That identity is
// what makes an explicit choice authoritative, and seat selection must never
// rewrite it.
func TestExplicitPersonaBecomesItsOwnSpecAndWins(t *testing.T) {
	dir := writePersonaRegistry(t)

	var specs PersonaSpecs
	if err := specs.Set("codex-reserve"); err != nil {
		t.Fatalf("parse --persona=codex-reserve: %v", err)
	}

	resolved, err := ResolvePersonas(specs, dir)
	if err != nil {
		t.Fatalf("ResolvePersonas: %v", err)
	}
	agents := FlattenPersonas(resolved)
	if len(agents) != 1 {
		t.Fatalf("persona agents = %d, want 1", len(agents))
	}

	if agents[0].PersonaName != "codex-reserve" {
		t.Fatalf("persona name = %q, want codex-reserve", agents[0].PersonaName)
	}
	if agents[0].AgentType != AgentTypeCodex {
		t.Fatalf("agent type = %v, want the codex pane type", agents[0].AgentType)
	}

	// The persona map add/spawn build is keyed by persona name, and the spec
	// they append carries that same name as its Model. An operator's explicit
	// seat therefore resolves by identity, with nothing to override it.
	personaMap := map[string]bool{}
	for _, r := range resolved {
		personaMap[r.Persona.Name] = true
	}
	if !personaMap[agents[0].PersonaName] {
		t.Errorf("persona %q is not in the map keyed by name; the explicit pin would not resolve", agents[0].PersonaName)
	}
	if personaMap["codex-spend-me"] {
		t.Error("an unrequested persona leaked into the map: explicit selection must not pull in the whole registry")
	}
}

// TestBareAgentSpecCarriesNoPersona is the #319 gap itself: without
// --persona, nothing populates PersonaName, so the host command template gets
// an empty value and falls through to whatever it pins statically. This is the
// slot a caam-ranked seat would fill, and the test states plainly that it is
// empty today.
func TestBareAgentSpecCarriesNoPersona(t *testing.T) {
	dir := writePersonaRegistry(t)

	// No --persona at all: the resolver has nothing to resolve.
	resolved, err := ResolvePersonas(nil, dir)
	if err != nil {
		t.Fatalf("ResolvePersonas(nil): %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %d, want 0 for a bare add", len(resolved))
	}

	// A bare `--cod=1` spec: its Model is a model alias or empty, never a
	// persona name, so the persona map lookup add/spawn perform misses.
	personaMap := map[string]bool{"codex-spend-me": true, "codex-reserve": true}
	for _, bare := range []AgentSpec{
		{Type: AgentTypeCodex, Count: 1},
		{Type: AgentTypeCodex, Count: 1, Model: "gpt-5"},
		{Type: AgentTypeClaude, Count: 1, Model: "opus"},
	} {
		if personaMap[bare.Model] {
			t.Errorf("bare spec %+v resolved to a persona; a bare add must not silently inherit one", bare)
		}
	}
}
