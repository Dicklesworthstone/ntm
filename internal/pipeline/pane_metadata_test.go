package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPaneMetadataSessionLookup(t *testing.T) {
	client := newCountingPaneMetadataClient([]tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Variant: "opus", Tags: []string{"role=lead"}},
		{ID: "%2", Index: 2, Type: tmux.AgentCodex, Variant: "gpt-5", Tags: []string{"role=investigator", "domain=H-002,H-005", "productive_ignorance=true"}},
		{ID: "%3", Index: 3, Type: tmux.AgentGemini, Variant: "pro", Tags: []string{"role=critic"}},
		{ID: "%4", Index: 4, Type: tmux.AgentCodex, Variant: "gpt-5-mini", Tags: []string{"role=builder"}},
		{ID: "%5", Index: 5, Type: tmux.AgentClaude, Variant: "sonnet", Tags: []string{"role=scribe"}},
	})

	cache, err := LoadPaneMetadataCache(client, "session", "")
	if err != nil {
		t.Fatalf("LoadPaneMetadataCache() error = %v", err)
	}
	meta, err := cache.Lookup("%2")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if meta.Role != "investigator" {
		t.Fatalf("Role = %q, want investigator", meta.Role)
	}
	if meta.Model != "gpt-5" {
		t.Fatalf("Model = %q, want gpt-5", meta.Model)
	}
	if len(meta.Domains) != 2 || meta.Domains[1] != "H-005" {
		t.Fatalf("Domains = %#v, want H-002/H-005", meta.Domains)
	}
	if !meta.ProductiveIgnorance || !meta.ProductiveIgnoranceOK {
		t.Fatalf("ProductiveIgnorance = %v/%v, want true/true", meta.ProductiveIgnorance, meta.ProductiveIgnoranceOK)
	}
}

func TestPaneMetadataRosterYAMLLookup(t *testing.T) {
	dir := t.TempDir()
	rosterDir := filepath.Join(dir, ".brenner_workspace")
	if err := os.MkdirAll(rosterDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(rosterDir, "roster.yaml"), `
panes:
  - pane: 2
    role: investigator
    model: codex
    domain: [H-001, H-005]
    productive_ignorance: true
`)

	cache, err := LoadPaneMetadataCache(newCountingPaneMetadataClient(nil), "session", dir)
	if err != nil {
		t.Fatalf("LoadPaneMetadataCache() error = %v", err)
	}
	meta, err := cache.Lookup("2")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if meta.Source != "roster_yaml" {
		t.Fatalf("Source = %q, want roster_yaml", meta.Source)
	}
	if meta.Role != "investigator" || meta.Model != "codex" {
		t.Fatalf("metadata = %#v, want roster role/model", meta)
	}
	if got := meta.variableMap()["domain"]; got != "H-001" {
		t.Fatalf("domain variable = %#v, want first domain", got)
	}
}

func TestPaneMetadataSourcePrioritySessionWins(t *testing.T) {
	dir := t.TempDir()
	rosterDir := filepath.Join(dir, ".brenner_workspace")
	if err := os.MkdirAll(rosterDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(rosterDir, "roster.yaml"), `
panes:
  - pane: 2
    role: stale-roster-role
    model: stale
`)
	client := newCountingPaneMetadataClient([]tmux.Pane{
		{ID: "%2", Index: 2, Type: tmux.AgentCodex, Variant: "gpt-5", Tags: []string{"role=session-role"}},
	})

	cache, err := LoadPaneMetadataCache(client, "session", dir)
	if err != nil {
		t.Fatalf("LoadPaneMetadataCache() error = %v", err)
	}
	meta, err := cache.Lookup("%2")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if meta.Source != "ntm_session" || meta.Role != "session-role" {
		t.Fatalf("metadata = %#v, want session source to win", meta)
	}
}

func TestPaneMetadataLoaderCachesFirstLookup(t *testing.T) {
	client := newCountingPaneMetadataClient([]tmux.Pane{
		{ID: "%2", Index: 2, Type: tmux.AgentCodex, Tags: []string{"role=investigator"}},
	})
	loader := NewPaneMetadataLoader(client, "session", "")

	for i := 0; i < 2; i++ {
		meta, err := loader.Lookup("%2")
		if err != nil {
			t.Fatalf("Lookup(%d) error = %v", i, err)
		}
		if meta.Role != "investigator" {
			t.Fatalf("Lookup(%d) role = %q, want investigator", i, meta.Role)
		}
	}
	if client.calls != 1 {
		t.Fatalf("GetPanes calls = %d, want 1 cache load", client.calls)
	}
}

func TestPaneSubstitutionUsesBoundMetadata(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	scope := BindPaneMetadata(state, PaneMetadata{
		PaneID:              "%2",
		Index:               2,
		Role:                "investigator",
		Model:               "codex",
		Domains:             []string{"H-005"},
		ProductiveIgnorance: true,
	})
	defer scope.Restore(state.Variables)

	sub := NewSubstitutor(state, "session", "workflow")
	got, err := sub.SubstituteStrict("role=${pane.role} model=${pane.model} domain=${pane.domain} pane=${pane.index} pi=${pane.productive_ignorance}")
	if err != nil {
		t.Fatalf("SubstituteStrict() error = %v", err)
	}
	want := "role=investigator model=codex domain=H-005 pane=2 pi=true"
	if got != want {
		t.Fatalf("SubstituteStrict() = %q, want %q", got, want)
	}
}

func TestPaneSubstitutionUnknownFieldErrors(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{}}
	BindPaneMetadata(state, PaneMetadata{PaneID: "%2", Index: 2, Role: "investigator"})

	sub := NewSubstitutor(state, "session", "workflow")
	_, err := sub.SubstituteStrict("${pane.unknown}")
	if err == nil {
		t.Fatal("SubstituteStrict() error = nil, want unknown pane field error")
	}
	if !strings.Contains(err.Error(), "field 'unknown' not found") {
		t.Fatalf("SubstituteStrict() error = %q, want unknown field", err.Error())
	}
}

func TestPaneMetadataFallbackMarkdownStrictRosterBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "phase0_scope_decision.md"), `
# Scope

## Roster
`+"```yaml"+`
panes:
  - pane: 3
    role: reviewer
    model: claude
    domain: H-010
`+"```"+`

## Notes
free-form text here should not be parsed
`)

	cache, err := LoadPaneMetadataCache(newCountingPaneMetadataClient(nil), "session", dir)
	if err != nil {
		t.Fatalf("LoadPaneMetadataCache() error = %v", err)
	}
	meta, err := cache.Lookup("3")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if meta.Source != "phase0_roster" || meta.Role != "reviewer" {
		t.Fatalf("metadata = %#v, want strict markdown roster block", meta)
	}
}

func TestPaneMetadataResumeRosterKeepsIndentedFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "RESUME.md"), `
# Resume

## Roster
panes:
  - pane: 4
    role: resumed-reviewer
    model: sonnet
    domain:
      - H-020
      - H-021

## Context
human notes after the roster
`)

	cache, err := LoadPaneMetadataCache(newCountingPaneMetadataClient(nil), "session", dir)
	if err != nil {
		t.Fatalf("LoadPaneMetadataCache() error = %v", err)
	}
	meta, err := cache.Lookup("4")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if meta.Source != "resume_roster" || meta.Role != "resumed-reviewer" || len(meta.Domains) != 2 {
		t.Fatalf("metadata = %#v, want resume roster with nested domain list", meta)
	}
}

func TestExecutorTemplateParamsResolveBoundPaneMetadata(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dispatch.md"), `
**Parameters:** <ROLE>, <MODEL>, <DOMAIN>
role=<ROLE> model=<MODEL> domain=<DOMAIN>
`)

	client := NewMockTmuxClient(tmux.Pane{
		ID:      "%2",
		Index:   2,
		Type:    tmux.AgentCodex,
		Variant: "gpt-5",
		Tags:    []string{"role=investigator", "domain=H-005"},
	})
	cfg := DefaultExecutorConfig("session")
	cfg.ProjectDir = dir
	e := NewExecutor(cfg)
	e.SetTmuxClient(client)
	e.state = &ExecutionState{
		RunID:      "run-pane-template",
		WorkflowID: "workflow",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}

	scope, err := e.pushPaneMetadataVars("2")
	if err != nil {
		t.Fatalf("pushPaneMetadataVars() error = %v", err)
	}
	defer e.popPaneMetadataVars(scope)

	result := e.executeTemplate(context.Background(), &Step{
		ID:       "tpl-pane",
		Template: "dispatch.md",
		Params: map[string]interface{}{
			"ROLE":   "${pane.role}",
			"MODEL":  "${pane.model}",
			"DOMAIN": "${pane.domain}",
		},
		Pane: PaneSpec{Index: 2},
		Wait: WaitNone,
	}, &Workflow{Name: "workflow"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want completed; error = %+v", result.Status, result.Error)
	}

	history, err := client.PasteHistory("%2")
	if err != nil {
		t.Fatalf("PasteHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory len = %d, want 1", len(history))
	}
	want := "role=investigator model=gpt-5 domain=H-005"
	if !strings.Contains(history[0].Content, want) {
		t.Fatalf("rendered template = %q, want %q", history[0].Content, want)
	}
}

type countingPaneMetadataClient struct {
	panes []tmux.Pane
	calls int
}

func newCountingPaneMetadataClient(panes []tmux.Pane) *countingPaneMetadataClient {
	return &countingPaneMetadataClient{panes: panes}
}

func (c *countingPaneMetadataClient) GetPanes(string) ([]tmux.Pane, error) {
	c.calls++
	out := make([]tmux.Pane, len(c.panes))
	copy(out, c.panes)
	return out, nil
}

func (c *countingPaneMetadataClient) PasteKeys(string, string, bool) error {
	return nil
}

func (c *countingPaneMetadataClient) CapturePaneOutput(string, int) (string, error) {
	return "", nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestPaneMetadataClientSatisfiesInterface(t *testing.T) {
	var _ TmuxClient = (*countingPaneMetadataClient)(nil)
}

func TestEnrichStrategyPanesFromRosterFillsMissingDomains(t *testing.T) {
	// foreachStrategyPanes only consults tmux pane tags for domains. Workflows
	// that document pane→domain ownership in roster.yaml or phase0_scope_decision.md
	// (per the bd-2ubxp.8 contract) would otherwise see empty Domains lists
	// and round_robin_by_domain would silently fall back to plain round-robin.
	dir := t.TempDir()
	rosterDir := filepath.Join(dir, ".brenner_workspace")
	if err := os.MkdirAll(rosterDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(rosterDir, "roster.yaml"), `
panes:
  - pane: 2
    role: investigator
    domain: [H-001, H-005]
  - pane: 3
    role: synthesizer
    domain: [H-002]
`)

	cfg := DefaultExecutorConfig("test-session")
	cfg.ProjectDir = dir
	e := NewExecutor(cfg)

	// Simulate strategyPanes built from tmux panes that lack domain tags.
	strategyPanes := []paneStrategyPane{
		{ID: "%2", ModelFamily: "cod"},
		{ID: "%3", ModelFamily: "gmi"},
	}
	got := e.enrichStrategyPanesFromRoster(strategyPanes)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if want := []string{"H-001", "H-005"}; !equalStringSlices(got[0].Domains, want) {
		t.Errorf("got[0].Domains = %v, want %v", got[0].Domains, want)
	}
	if want := []string{"H-002"}; !equalStringSlices(got[1].Domains, want) {
		t.Errorf("got[1].Domains = %v, want %v", got[1].Domains, want)
	}

	// Round-robin-by-domain over the enriched panes routes H-005 → %2.
	paneID, err := roundRobinByDomain(got, "H-005", 0)
	if err != nil {
		t.Fatalf("roundRobinByDomain() error = %v", err)
	}
	if paneID != "%2" {
		t.Fatalf("roundRobinByDomain() = %q, want %%2 (the H-005 owner)", paneID)
	}
}

func TestEnrichStrategyPanesFromRosterRespectsExistingDomains(t *testing.T) {
	// Session metadata wins per bd-6lkqr.1: when tmux tags already populated
	// Domains, the roster sources must NOT overwrite that value.
	dir := t.TempDir()
	rosterDir := filepath.Join(dir, ".brenner_workspace")
	if err := os.MkdirAll(rosterDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(rosterDir, "roster.yaml"), `
panes:
  - pane: 2
    domain: [roster-domain]
`)

	cfg := DefaultExecutorConfig("test-session")
	cfg.ProjectDir = dir
	e := NewExecutor(cfg)

	strategyPanes := []paneStrategyPane{
		{ID: "%2", ModelFamily: "cod", Domains: []string{"session-domain"}},
	}
	got := e.enrichStrategyPanesFromRoster(strategyPanes)
	if want := []string{"session-domain"}; !equalStringSlices(got[0].Domains, want) {
		t.Fatalf("got[0].Domains = %v, want %v (session metadata must win)", got[0].Domains, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
