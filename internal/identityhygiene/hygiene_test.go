package identityhygiene

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedClock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

const projAHash = "" // computed once via ProjectHash("/data/projects/a")

func TestEvaluate_DryRunIsDefaultAndAlwaysTrue(t *testing.T) {
	t.Parallel()
	r := Evaluate(Inputs{Now: fixedClock()})
	if !r.DryRun {
		t.Fatalf("DryRun = false, want true (the package contract is dry-run only)")
	}
	if len(r.Notes) == 0 || !strings.Contains(strings.Join(r.Notes, " "), "dry-run only") {
		t.Errorf("expected dry-run note in Notes: %v", r.Notes)
	}
}

func TestEvaluate_CleanInputsHaveNoFindings(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			{Path: "/canonical/abc/%17", AgentName: "Alice", ProjectHash: ProjectHash("/data/projects/a"), PaneID: "%17", ModifiedAt: now.Add(-2 * time.Hour)},
		},
		LivePanes: []LivePane{
			{ID: "%17", Session: "proja"},
		},
		RegisteredAgents: []RegisteredAgent{
			{Name: "Alice", PaneID: "%17", LastActiveAt: now.Add(-5 * time.Minute)},
		},
		KnownProjectKeys: []string{"/data/projects/a"},
		StaleAfter:       1 * time.Hour,
	}
	r := Evaluate(in)
	if len(r.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", r.Findings)
	}
	if r.Summary.Warning != 0 || r.Summary.Info != 0 {
		t.Errorf("Summary = %+v, want zeros", r.Summary)
	}
}

func TestEvaluate_StaleIdentityFiresForDeadPane(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			// %99 is gone from the live set.
			{Path: "/canonical/abc/%99", AgentName: "GreenCastle", ProjectHash: ProjectHash("/data/projects/a"), PaneID: "%99", ModifiedAt: now.Add(-2 * time.Hour)},
		},
		LivePanes:        []LivePane{{ID: "%17"}},
		KnownProjectKeys: []string{"/data/projects/a"},
		StaleAfter:       1 * time.Hour,
	}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "stale_identity") {
		t.Fatalf("missing stale_identity finding: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "stale_identity" && f.Severity != SeverityWarning {
			t.Errorf("stale_identity severity = %s, want warning", f.Severity)
		}
	}
}

func TestEvaluate_FreshIdentityIsSkippedEvenIfPaneIsDead(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			// pane is gone but record is younger than StaleAfter.
			{Path: "/canonical/abc/%99", AgentName: "Fresh", ProjectHash: ProjectHash("/data/projects/a"), PaneID: "%99", ModifiedAt: now.Add(-10 * time.Minute)},
		},
		LivePanes:        []LivePane{{ID: "%17"}},
		KnownProjectKeys: []string{"/data/projects/a"},
		StaleAfter:       1 * time.Hour,
	}
	r := Evaluate(in)
	if findHasCode(r.Findings, "stale_identity") {
		t.Errorf("stale_identity fired on a fresh record: %+v", r.Findings)
	}
}

func TestEvaluate_UnknownProjectFires(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			{Path: "/canonical/legacy/%17", AgentName: "Old", ProjectHash: ProjectHash("/old/path"), PaneID: "%17", ModifiedAt: now.Add(-3 * time.Hour)},
		},
		LivePanes:        []LivePane{{ID: "%17"}},
		KnownProjectKeys: []string{"/data/projects/a"}, // /old/path is NOT here
		StaleAfter:       1 * time.Hour,
	}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "unknown_project") {
		t.Fatalf("missing unknown_project finding: %+v", r.Findings)
	}
}

func TestEvaluate_DeadPaneFiresForRegisteredAgent(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		LivePanes: []LivePane{
			{ID: "%17", Session: "proja"},
		},
		RegisteredAgents: []RegisteredAgent{
			{Name: "Zombie", PaneID: "%99", LastActiveAt: now.Add(-3 * time.Hour)},
		},
		StaleAfter: 1 * time.Hour,
	}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "dead_pane") {
		t.Fatalf("missing dead_pane finding: %+v", r.Findings)
	}
}

func TestEvaluate_DeadContactLinkIsInfoSeverity(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			// A contact link pointing at "GhostAgent" who is not registered.
			{Path: "/canonical/contact/ghost", LinkedAgent: "GhostAgent", ModifiedAt: now.Add(-3 * time.Hour)},
		},
		RegisteredAgents: []RegisteredAgent{
			{Name: "Alive", PaneID: "%17", LastActiveAt: now},
		},
		LivePanes: []LivePane{{ID: "%17"}},
	}
	r := Evaluate(in)
	if !findHasCode(r.Findings, "dead_contact_link") {
		t.Fatalf("missing dead_contact_link finding: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "dead_contact_link" && f.Severity != SeverityInfo {
			t.Errorf("dead_contact_link severity = %s, want info", f.Severity)
		}
	}
}

func TestEvaluate_LiveContactLinkIsClean(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			{Path: "/canonical/contact/alive", LinkedAgent: "Alive", ModifiedAt: now.Add(-3 * time.Hour)},
		},
		RegisteredAgents: []RegisteredAgent{
			{Name: "Alive", PaneID: "%17", LastActiveAt: now},
		},
		LivePanes: []LivePane{{ID: "%17"}},
	}
	r := Evaluate(in)
	if findHasCode(r.Findings, "dead_contact_link") {
		t.Errorf("dead_contact_link fired for a live linked agent: %+v", r.Findings)
	}
}

func TestEvaluate_FindingsSortedBySeverityThenCode(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			{Path: "/p/%99", AgentName: "Stale", ProjectHash: ProjectHash("/data/projects/a"), PaneID: "%99", ModifiedAt: now.Add(-3 * time.Hour)},
			{Path: "/p/legacy", AgentName: "Old", ProjectHash: ProjectHash("/old/path"), PaneID: "%17", ModifiedAt: now.Add(-3 * time.Hour)},
			{Path: "/p/contact/ghost", LinkedAgent: "Ghost", ModifiedAt: now.Add(-3 * time.Hour)},
		},
		LivePanes:        []LivePane{{ID: "%17"}},
		RegisteredAgents: []RegisteredAgent{{Name: "Alive", PaneID: "%17", LastActiveAt: now}},
		KnownProjectKeys: []string{"/data/projects/a"},
		StaleAfter:       1 * time.Hour,
	}
	r := Evaluate(in)
	if len(r.Findings) < 2 {
		t.Fatalf("expected multiple findings, got %d", len(r.Findings))
	}
	for i := 1; i < len(r.Findings); i++ {
		prev := severityRank(r.Findings[i-1].Severity)
		cur := severityRank(r.Findings[i].Severity)
		if cur > prev {
			t.Errorf("findings out of order: severity rank %d at index %d follows %d at %d",
				cur, i, prev, i-1)
		}
		if cur == prev && r.Findings[i].Code < r.Findings[i-1].Code {
			t.Errorf("findings out of order: code %q at index %d follows %q at %d",
				r.Findings[i].Code, i, r.Findings[i-1].Code, i-1)
		}
	}
}

func TestEvaluate_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	in := Inputs{
		Now: now,
		Identities: []IdentityRecord{
			{Path: "/p/%99", AgentName: "Stale", ProjectHash: ProjectHash("/data/projects/a"), PaneID: "%99", ModifiedAt: now.Add(-3 * time.Hour)},
		},
		LivePanes:        []LivePane{{ID: "%17"}},
		KnownProjectKeys: []string{"/data/projects/a"},
		StaleAfter:       1 * time.Hour,
	}
	a, _ := json.Marshal(Evaluate(in))
	b, _ := json.Marshal(Evaluate(in))
	if string(a) != string(b) {
		t.Errorf("JSON drifted across two Evaluate calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	if !strings.Contains(string(a), `"dry_run":true`) {
		t.Errorf("expected dry_run:true in JSON: %s", a)
	}
}

func TestProjectHash_DeterministicAndCanonical(t *testing.T) {
	t.Parallel()
	if got := ProjectHash(""); got != "" {
		t.Errorf("ProjectHash(\"\") = %q, want empty", got)
	}
	a := ProjectHash("/data/projects/a")
	b := ProjectHash("/data/projects/a")
	if a == "" {
		t.Fatal("ProjectHash returned empty for non-empty key")
	}
	if a != b {
		t.Errorf("ProjectHash not deterministic: %q != %q", a, b)
	}
	if len(a) != 12 {
		t.Errorf("ProjectHash length = %d, want 12", len(a))
	}
}

func findHasCode(findings []Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// keep the unused-symbol warning quiet in case projAHash was removed
// from a future iteration of this file; it serves as a reminder of
// the canonical hash used by the test fixtures above.
var _ = projAHash
