package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// bd-2ubxp.15: end-to-end integration tests against the mock harness
// covering each major foreach pattern brennerbot pipelines depend on.
//
// Each test sets up a five-pane roster (mirroring brennerbot's typical
// adjudicator-plus-four-investigators shape), drives a foreach step with
// the iteration source + pane assignment strategy under test, and asserts
// the dispatched MOs land on exactly the right panes with the expected
// per-iteration substitution. These are the contract tests the operator
// runs before shipping a pipeline that depends on the foreach runtime.

// brennerbotRoster builds the canonical 5-pane fixture used by the
// brennerbot phase tests: two Claude investigators, one Codex
// investigator, one Gemini investigator, one Codex adjudicator. Domains
// are partitioned across the four investigators so round_robin_by_domain
// has unambiguous owners. Model families are tagged so by_model_family
// and by_model_family_difference route deterministically.
func brennerbotRoster() []tmux.Pane {
	return []tmux.Pane{
		{
			ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude,
			Tags: []string{"role=investigator", "model=cc", "domain=H-001,H-002"},
		},
		{
			ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentCodex,
			Tags: []string{"role=investigator", "model=cod", "domain=H-003,H-004"},
		},
		{
			ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini,
			Tags: []string{"role=investigator", "model=gmi", "domain=H-005,H-006"},
		},
		{
			ID: "%4", Index: 4, NTMIndex: 4, Type: tmux.AgentClaude,
			Tags: []string{"role=investigator", "model=cc", "domain=H-007,H-008"},
		},
		{
			ID: "%5", Index: 5, NTMIndex: 5, Type: tmux.AgentCodex,
			Tags: []string{"role=adjudicator", "model=cod", "domain=audit"},
		},
	}
}

// brennerbotPhase0Roster returns the phase0_scope_decision.md content the
// pipeline reads when it does not have live tag metadata to lean on. The
// roster YAML block matches brennerbotRoster.
const brennerbotPhase0Roster = `# Scope

## Roster
` + "```yaml" + `
panes:
  - pane: 1
    role: investigator
    model: cc
    domain: [H-001, H-002]
  - pane: 2
    role: investigator
    model: cod
    domain: [H-003, H-004]
  - pane: 3
    role: investigator
    model: gmi
    domain: [H-005, H-006]
  - pane: 4
    role: investigator
    model: cc
    domain: [H-007, H-008]
  - pane: 5
    role: adjudicator
    model: cod
    domain: audit
` + "```" + `
`

// setupBrennerbotIntegration wires a mock tmux client, projectDir with
// phase0 roster fixture, and a fixture template, returning the executor
// configured for foreach integration testing. The mock tmux client is
// returned so callers can install BeadQueryRunBr and assert paste history.
func setupBrennerbotIntegration(t *testing.T, templateBody string) (string, *MockTmuxClient, *Executor) {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(projectDir, "phase0_scope_decision.md"),
		[]byte(brennerbotPhase0Roster),
		0o644,
	); err != nil {
		t.Fatalf("write phase0 roster: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, "mo.md"),
		[]byte(templateBody),
		0o644,
	); err != nil {
		t.Fatalf("write template: %v", err)
	}

	mock := NewMockTmuxClient(brennerbotRoster()...)
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("brennerbot-integration")
	cfg.ProjectDir = projectDir
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)
	return projectDir, mock, executor
}

// runWorkflowWithDeadline runs the workflow with a tight deadline and
// fails the test if it does not complete cleanly.
func runWorkflowWithDeadline(t *testing.T, executor *Executor, workflow *Workflow, deadline time.Duration) *ExecutionState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}
	return state
}

// pasteContents returns the dispatched template contents for a pane,
// flattened from the mock paste history. Empty slice means no dispatch.
func pasteContents(t *testing.T, mock *MockTmuxClient, paneID string) []string {
	t.Helper()
	history, err := mock.PasteHistory(paneID)
	if err != nil {
		t.Fatalf("PasteHistory(%s) error = %v", paneID, err)
	}
	out := make([]string, len(history))
	for i, paste := range history {
		out[i] = paste.Content
	}
	return out
}

// TestPhase4InvestigateForeachBeadsRoundRobinByDomain covers the
// brennerbot Phase-4 contract: foreach over hypothesis beads dispatches
// MO-04a-investigate to the pane that owns the bead's domain. Without
// round_robin_by_domain matching, this would fall back to round-robin
// and route H-005 to whichever pane happens to be next in iteration
// order — silently misrouting domain-owning work.
func TestPhase4InvestigateForeachBeadsRoundRobinByDomain(t *testing.T) {
	body := "Investigate <H_ID>: <TITLE>\n"
	_, mock, executor := setupBrennerbotIntegration(t, body)

	beadsResponse := `{"issues":[
		{"id":"H-001","title":"hypo one","domain":"H-001","labels":["hypothesis"],"status":"active"},
		{"id":"H-003","title":"hypo three","domain":"H-003","labels":["hypothesis"],"status":"active"},
		{"id":"H-005","title":"hypo five","domain":"H-005","labels":["hypothesis"],"status":"active"},
		{"id":"H-007","title":"hypo seven","domain":"H-007","labels":["hypothesis"],"status":"active"}
	]}`
	executor.config.BeadQueryRunBr = func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(beadsResponse), nil
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase4-investigate",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase4_investigate",
			Foreach: &ForeachConfig{
				Beads:        "hypothesis,state:active",
				PaneStrategy: "round_robin_by_domain",
				Steps: []Step{{
					ID:       "send_mo",
					Template: "mo.md",
					Params: map[string]interface{}{
						"H_ID":  "${item.id}",
						"TITLE": "${item.title}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	wantPerPane := map[string]string{
		"%1": "Investigate H-001: hypo one",   // pane 1 owns H-001
		"%2": "Investigate H-003: hypo three", // pane 2 owns H-003
		"%3": "Investigate H-005: hypo five",  // pane 3 owns H-005
		"%4": "Investigate H-007: hypo seven", // pane 4 owns H-007
	}
	for paneID, want := range wantPerPane {
		pastes := pasteContents(t, mock, paneID)
		if len(pastes) != 1 {
			t.Fatalf("pane %s: got %d pastes, want 1", paneID, len(pastes))
		}
		if !strings.Contains(pastes[0], want) {
			t.Fatalf("pane %s missing %q:\n%s", paneID, want, pastes[0])
		}
	}
	if pastes := pasteContents(t, mock, "%5"); len(pastes) != 0 {
		t.Fatalf("adjudicator pane received %d investigator dispatches, want 0", len(pastes))
	}
}

// TestPhase4DevilsAdvocateForeachBeadsByModelFamilyDifference covers
// the brennerbot devil's-advocate phase: each hypothesis carries its
// authoring model family, and the adversarial review must dispatch to
// a pane whose family DIFFERS. The strategy must never route a Claude
// hypothesis back to a Claude pane. Asserts cross-family routing for
// each item and that no same-family pane received the dispatch.
func TestPhase4DevilsAdvocateForeachBeadsByModelFamilyDifference(t *testing.T) {
	body := "Devils-advocate <H_ID> authored by <AUTHOR>\n"
	_, mock, executor := setupBrennerbotIntegration(t, body)

	beadsResponse := `{"issues":[
		{"id":"H-001","title":"hypo one","author_model":"cc","labels":["hypothesis"],"status":"active"},
		{"id":"H-003","title":"hypo three","author_model":"cod","labels":["hypothesis"],"status":"active"},
		{"id":"H-005","title":"hypo five","author_model":"gmi","labels":["hypothesis"],"status":"active"}
	]}`
	executor.config.BeadQueryRunBr = func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(beadsResponse), nil
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase4-devils-advocate",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase4_devils",
			Foreach: &ForeachConfig{
				Beads:        "hypothesis,state:active",
				PaneStrategy: "by_model_family_difference",
				Steps: []Step{{
					ID:       "send_mo",
					Template: "mo.md",
					Params: map[string]interface{}{
						"H_ID":   "${item.id}",
						"AUTHOR": "${item.author_model}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	authorByItem := map[string]string{
		"H-001": "cc",
		"H-003": "cod",
		"H-005": "gmi",
	}
	paneFamilies := map[string]string{
		"%1": "cc", "%2": "cod", "%3": "gmi", "%4": "cc", "%5": "cod",
	}

	for hID, author := range authorByItem {
		marker := fmt.Sprintf("Devils-advocate %s authored by %s", hID, author)
		var routed []string
		for _, paneID := range []string{"%1", "%2", "%3", "%4", "%5"} {
			for _, paste := range pasteContents(t, mock, paneID) {
				if strings.Contains(paste, marker) {
					routed = append(routed, paneID)
				}
			}
		}
		if len(routed) != 1 {
			t.Fatalf("item %s dispatched to %v, want exactly one pane", hID, routed)
		}
		if family := paneFamilies[routed[0]]; family == author {
			t.Fatalf("item %s (author=%s) routed to same-family pane %s",
				hID, author, routed[0])
		}
	}
}

// TestPhase5DebateForeachPairsMaxRounds covers brennerbot Phase-5 debate:
// the iteration source is a $(...) shell command emitting pipe-delimited
// pair lines, each pair iterates max_rounds=3 times, and each round
// dispatches MO-05-debate-round to a single pane. Asserts the total paste
// count is pairs*rounds and the round counter advances per iteration.
func TestPhase5DebateForeachPairsMaxRounds(t *testing.T) {
	body := "Debate <DEBATE_ID> round <ROUND>: <H1> vs <H2>\n"
	projectDir, mock, executor := setupBrennerbotIntegration(t, body)

	pairsFixture := `DEBATE-001|H-001|H-003|%1|%2
DEBATE-002|H-005|H-007|%3|%4
`
	pairsPath := filepath.Join(projectDir, "pairs.txt")
	if err := os.WriteFile(pairsPath, []byte(pairsFixture), 0o644); err != nil {
		t.Fatalf("write pairs fixture: %v", err)
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase5-debate",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase5_debate",
			Foreach: &ForeachConfig{
				Pairs:     fmt.Sprintf("$(cat %s)", pairsPath),
				MaxRounds: IntOrExpr{Value: 3},
				Steps: []Step{{
					ID:       "send_round",
					Template: "mo.md",
					Pane:     PaneSpec{Index: 5},
					Params: map[string]interface{}{
						"DEBATE_ID": "${item.debate_id}",
						"ROUND":     "${round}",
						"H1":        "${item.h1}",
						"H2":        "${item.h2}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	pastes := pasteContents(t, mock, "%5")
	if len(pastes) != 2*3 {
		t.Fatalf("pane %%5 paste count = %d, want %d (2 pairs * 3 rounds)", len(pastes), 2*3)
	}

	for i, debateID := range []string{"DEBATE-001", "DEBATE-002"} {
		for round := 1; round <= 3; round++ {
			marker := fmt.Sprintf("Debate %s round %d", debateID, round)
			idx := i*3 + (round - 1)
			if idx >= len(pastes) {
				t.Fatalf("missing paste at index %d for marker %q", idx, marker)
			}
			if !strings.Contains(pastes[idx], marker) {
				t.Fatalf("paste %d missing %q:\n%s", idx, marker, pastes[idx])
			}
		}
	}
}

// TestPhase5AdjudicateForeachDebatesRotateAdjudicator covers brennerbot
// Phase-5 adjudication: foreach over DEBATE-* IDs with rotate_adjudicator
// must pick a non-champion pane per debate and rotate so subsequent
// debates get a different adjudicator when possible.
func TestPhase5AdjudicateForeachDebatesRotateAdjudicator(t *testing.T) {
	body := "Adjudicate <DEBATE_ID> by <CHAMPIONS>\n"
	projectDir, mock, executor := setupBrennerbotIntegration(t, body)

	debatesFixture := `[
		{"id":"DEBATE-001","champions":["%1","%2"]},
		{"id":"DEBATE-002","champions":["%3","%4"]},
		{"id":"DEBATE-003","champions":["%1","%3"]}
	]`
	debatesPath := filepath.Join(projectDir, "debates.json")
	if err := os.WriteFile(debatesPath, []byte(debatesFixture), 0o644); err != nil {
		t.Fatalf("write debates fixture: %v", err)
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase5-adjudicate",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase5_adjudicate",
			Foreach: &ForeachConfig{
				Debates:      fmt.Sprintf("$(cat %s)", debatesPath),
				PaneStrategy: "rotate_adjudicator",
				Steps: []Step{{
					ID:       "send_adjudication",
					Template: "mo.md",
					Params: map[string]interface{}{
						"DEBATE_ID": "${item.id}",
						"CHAMPIONS": "${item.champions}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	dispatchedTo := func(debateID string) string {
		marker := fmt.Sprintf("Adjudicate %s ", debateID)
		var hits []string
		for _, paneID := range []string{"%1", "%2", "%3", "%4", "%5"} {
			for _, paste := range pasteContents(t, mock, paneID) {
				if strings.Contains(paste, marker) {
					hits = append(hits, paneID)
				}
			}
		}
		if len(hits) != 1 {
			t.Fatalf("debate %s dispatched to %v, want exactly one pane", debateID, hits)
		}
		return hits[0]
	}

	adj1 := dispatchedTo("DEBATE-001")
	if adj1 == "%1" || adj1 == "%2" {
		t.Fatalf("DEBATE-001 adjudicator %s overlaps champions [%%1 %%2]", adj1)
	}
	adj2 := dispatchedTo("DEBATE-002")
	if adj2 == "%3" || adj2 == "%4" {
		t.Fatalf("DEBATE-002 adjudicator %s overlaps champions [%%3 %%4]", adj2)
	}
	adj3 := dispatchedTo("DEBATE-003")
	if adj3 == "%1" || adj3 == "%3" {
		t.Fatalf("DEBATE-003 adjudicator %s overlaps champions [%%1 %%3]", adj3)
	}
	if adj1 == adj2 && adj2 == adj3 {
		t.Fatalf("rotate_adjudicator did not rotate: all three debates went to %s", adj1)
	}
}

// TestPhase6DistillForeachModels covers brennerbot Phase-6 distill:
// foreach over model-family slugs with by_model_family routes the
// distill MO to one pane per family. Asserts each family pane received
// the dispatch and no off-family pane did.
func TestPhase6DistillForeachModels(t *testing.T) {
	body := "Distill family <FAMILY>\n"
	_, mock, executor := setupBrennerbotIntegration(t, body)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase6-distill",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase6_distill",
			Foreach: &ForeachConfig{
				Models:       StringOrList{"cc", "cod", "gmi"},
				PaneStrategy: "by_model_family",
				Steps: []Step{{
					ID:       "send_distill",
					Template: "mo.md",
					Params: map[string]interface{}{
						"FAMILY": "${item}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	// Each family has at least one pane; by_model_family picks the first
	// available match, which is the lowest-indexed pane carrying that family.
	wantFamilyToPane := map[string]string{
		"cc":  "%1",
		"cod": "%2",
		"gmi": "%3",
	}
	for family, wantPane := range wantFamilyToPane {
		marker := fmt.Sprintf("Distill family %s", family)
		var routed []string
		for _, paneID := range []string{"%1", "%2", "%3", "%4", "%5"} {
			for _, paste := range pasteContents(t, mock, paneID) {
				if strings.Contains(paste, marker) {
					routed = append(routed, paneID)
				}
			}
		}
		if len(routed) != 1 || routed[0] != wantPane {
			t.Fatalf("distill family %s routed to %v, want [%s]", family, routed, wantPane)
		}
	}
}

// TestPhase7TrioForeachPane covers brennerbot Phase-7: foreach_pane
// dispatches the audit MO to every pane in the roster simultaneously.
// Each pane must receive exactly one dispatch carrying its own pane.id
// substituted into the rendered MO.
func TestPhase7TrioForeachPane(t *testing.T) {
	body := "Audit pane <PANE_ID> role=<ROLE>\n"
	_, mock, executor := setupBrennerbotIntegration(t, body)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "brennerbot-phase7-trio",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{{
			ID: "phase7_audit",
			ForeachPane: &ForeachConfig{
				Steps: []Step{{
					ID:       "send_audit",
					Template: "mo.md",
					Params: map[string]interface{}{
						"PANE_ID": "${pane.id}",
						"ROLE":    "${pane.role}",
					},
					Wait: WaitNone,
				}},
			},
		}},
	}

	runWorkflowWithDeadline(t, executor, workflow, 3*time.Second)

	wantPerPane := map[string]string{
		"%1": "Audit pane %1 role=investigator",
		"%2": "Audit pane %2 role=investigator",
		"%3": "Audit pane %3 role=investigator",
		"%4": "Audit pane %4 role=investigator",
		"%5": "Audit pane %5 role=adjudicator",
	}
	for paneID, want := range wantPerPane {
		pastes := pasteContents(t, mock, paneID)
		if len(pastes) != 1 {
			t.Fatalf("pane %s: got %d pastes, want 1", paneID, len(pastes))
		}
		if !strings.Contains(pastes[0], want) {
			t.Fatalf("pane %s missing %q:\n%s", paneID, want, pastes[0])
		}
	}
}
