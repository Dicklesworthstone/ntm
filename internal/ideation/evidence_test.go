package ideation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewIdeaEvidenceSnapshotEmptyQueueStableJSON(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true

	got := mustMarshalJSON(t, snapshot)
	want := `{"project":"/repo","queue":{"open_count":0,"actionable_count":0,"blocked_count":0,"in_progress_count":0,"ready_count":0,"counts_verified":true,"source_ids":[]},"triage":{"top_ids":[],"graph_health":{"total_nodes":0,"total_edges":0,"density":0,"metrics":{},"evidence":[]},"status_summary":[],"warnings":[],"source_ids":[]},"documents":[],"closeout_proof":[],"git":[],"sources":[],"existing_work":[],"candidates":[],"optional_signals":[],"degraded_sources":[],"validation_notes":[]}`
	if got != want {
		t.Fatalf("stable JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestRecordSourceCapturesOptionalDegradation(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.RecordSource(CandidateSource{
		ID:        "cass",
		Kind:      SourceCASS,
		Available: false,
		Error:     "cass unavailable",
		Evidence:  []string{"optional context adapter timed out"},
	})

	if len(snapshot.Sources) != 1 {
		t.Fatalf("sources len=%d, want 1", len(snapshot.Sources))
	}
	if len(snapshot.DegradedSources) != 1 {
		t.Fatalf("degraded len=%d, want 1", len(snapshot.DegradedSources))
	}
	note := snapshot.DegradedSources[0]
	if note.Severity != ValidationWarning {
		t.Fatalf("severity=%q, want warning", note.Severity)
	}
	if note.SourceID != "cass" {
		t.Fatalf("source_id=%q, want cass", note.SourceID)
	}
	if !strings.Contains(note.Message, "cass unavailable") {
		t.Fatalf("message=%q, want cass unavailable", note.Message)
	}
	if len(note.Evidence) != 1 || note.Evidence[0] != "optional context adapter timed out" {
		t.Fatalf("evidence=%v, want preserved degraded-source evidence", note.Evidence)
	}
}

func TestEvaluateOverlapExactDuplicateAgainstClosedIdeaWizardFamily(t *testing.T) {
	existing := []ExistingWorkFingerprint{
		{
			ID:       "bd-2mb03.5",
			FamilyID: "bd-2mb03",
			Title:    "Queue-dry operator autopilot",
			Summary:  "closed queue dry operator autopilot work",
			Status:   WorkStatusClosed,
			Labels:   []string{"queue-dry", "operator"},
			Keywords: []string{"queue", "dry", "autopilot"},
			Evidence: []string{"closed by prior idea-wizard tranche"},
		},
	}
	candidate := IdeaCandidate{
		ID:       "candidate-1",
		Title:    "Queue dry operator autopilot",
		Labels:   []string{"operator", "queue-dry"},
		Keywords: []string{"autopilot", "dry", "queue"},
		Evidence: []string{"candidate generated from queue-dry gap"},
	}

	verdict := EvaluateOverlap(candidate, existing)
	if verdict.Kind != OverlapExactDuplicate {
		t.Fatalf("kind=%q, want exact duplicate", verdict.Kind)
	}
	if verdict.WorkID != "bd-2mb03.5" || verdict.FamilyID != "bd-2mb03" {
		t.Fatalf("work=%s family=%s, want bd-2mb03.5 bd-2mb03", verdict.WorkID, verdict.FamilyID)
	}
	if !containsEvidence(verdict.Evidence, "bd-2mb03") {
		t.Fatalf("evidence=%v, want closed family marker", verdict.Evidence)
	}
	if !containsEvidence(verdict.Evidence, "candidate generated from queue-dry gap") {
		t.Fatalf("evidence=%v, want candidate evidence preserved", verdict.Evidence)
	}
}

func TestEvaluateOverlapLikelyDuplicatePreservesEvidence(t *testing.T) {
	existing := []ExistingWorkFingerprint{
		{
			ID:       "bd-fxj4f.3",
			FamilyID: "bd-fxj4f",
			Title:    "Robot contract replay harness",
			Summary:  "replay robot json fixtures for contract drift",
			Status:   WorkStatusClosed,
			Labels:   []string{"robot", "testing"},
			Keywords: []string{"contract", "replay", "robot"},
			Paths:    []string{"internal/robot/contract"},
			Evidence: []string{"closed robot contract replay bead"},
		},
	}
	candidate := IdeaCandidate{
		ID:       "candidate-robot-replay",
		Title:    "Robot replay contract tests",
		Summary:  "add json replay fixtures for robot contract stability",
		Labels:   []string{"robot"},
		Keywords: []string{"contract", "replay", "robot"},
		Paths:    []string{"internal/robot/contract"},
		Evidence: []string{"candidate from test posture scan"},
	}

	verdict := EvaluateOverlap(candidate, existing)
	if verdict.Kind != OverlapLikelyDuplicate {
		t.Fatalf("kind=%q, want likely duplicate; evidence=%v", verdict.Kind, verdict.Evidence)
	}
	if verdict.Confidence < 0.6 {
		t.Fatalf("confidence=%f, want >= 0.6", verdict.Confidence)
	}
	for _, want := range []string{"shared keywords", "shared labels", "shared paths", "candidate from test posture scan", "bd-fxj4f"} {
		if !containsEvidence(verdict.Evidence, want) {
			t.Fatalf("evidence=%v, want marker %q", verdict.Evidence, want)
		}
	}
}

func TestEvaluateOverlapAdjacentFollowUpAndNovel(t *testing.T) {
	existing := []ExistingWorkFingerprint{
		{
			ID:       "bd-8kglp.4",
			FamilyID: "bd-8kglp",
			Title:    "RCH build storm backpressure",
			Status:   WorkStatusClosed,
			Keywords: []string{"rch", "backpressure"},
		},
	}
	followUp := IdeaCandidate{
		ID:    "candidate-follow-up",
		Title: "Queue-dry plan uses build storm history as evidence",
		RelatedWork: []RelatedWorkReference{
			{
				ID:           "bd-8kglp.4",
				Relationship: RelationshipFollowUp,
				Evidence:     []string{"uses shipped RCH backpressure results instead of duplicating it"},
			},
		},
	}

	attached := AttachOverlap(followUp, existing)
	if attached.Overlap.Kind != OverlapAdjacentFollowUp {
		t.Fatalf("kind=%q, want adjacent follow-up", attached.Overlap.Kind)
	}
	if attached.Novelty.Level != NoveltyMedium {
		t.Fatalf("novelty=%q, want medium", attached.Novelty.Level)
	}

	novel := AttachOverlap(IdeaCandidate{
		ID:       "candidate-novel",
		Title:    "Queue-dry idea effectiveness feedback",
		Keywords: []string{"effectiveness", "feedback"},
	}, existing)
	if novel.Overlap.Kind != OverlapNovel {
		t.Fatalf("kind=%q, want novel", novel.Overlap.Kind)
	}
	if novel.Novelty.Level != NoveltyHigh {
		t.Fatalf("novelty=%q, want high", novel.Novelty.Level)
	}
}

func TestKnownClosedIdeaWizardFamilies(t *testing.T) {
	got := KnownClosedIdeaWizardFamilies()
	for _, want := range []string{"bd-2mb03", "bd-3v1gs", "bd-fxj4f", "bd-8kglp", "bd-e7xm1"} {
		if !containsString(got, want) {
			t.Fatalf("families=%v, want %s", got, want)
		}
		if !IsKnownClosedIdeaWizardFamily(want) {
			t.Fatalf("IsKnownClosedIdeaWizardFamily(%q)=false, want true", want)
		}
	}
	got[0] = "mutated"
	if IsKnownClosedIdeaWizardFamily("mutated") {
		t.Fatalf("KnownClosedIdeaWizardFamilies exposed mutable package state")
	}
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	return string(data)
}

func containsEvidence(evidence []string, needle string) bool {
	for _, item := range evidence {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
