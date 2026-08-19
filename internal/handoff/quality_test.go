package handoff

import (
	"math"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestScoreQualityHighQualityHandoffIsDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	h := highQualityHandoff(now)

	first := h.ScoreQuality(now)
	second := h.ScoreQuality(now)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ScoreQuality is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Status != QualityStatusHigh {
		t.Fatalf("status = %q, want %q (score=%d reasons=%v)", first.Status, QualityStatusHigh, first.Score, first.Reasons)
	}
	if first.Score < 80 {
		t.Fatalf("score = %d, want >= 80; reasons=%v", first.Score, first.Reasons)
	}
	if len(first.Dimensions) != 4 {
		t.Fatalf("dimensions = %d, want 4", len(first.Dimensions))
	}
	if hasQualityReason(first, "coverage.missing_goal") {
		t.Fatalf("unexpected missing goal reason: %+v", first)
	}
}

func TestScoreQualitySparseContextHasReasonCodesAndHints(t *testing.T) {
	now := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	h := New("sparse")
	h.CreatedAt = now
	h.UpdatedAt = now
	h.Goal = "Touched some files"

	report := h.ScoreQuality(now)
	if report.Status != QualityStatusLow {
		t.Fatalf("status = %q, want %q (score=%d)", report.Status, QualityStatusLow, report.Score)
	}
	for _, reason := range []string{
		"coverage.missing_now",
		"coverage.missing_test",
		"actionability.no_next_steps",
		"traceability.no_beads",
	} {
		if !hasQualityReason(report, reason) {
			t.Fatalf("missing reason %q in %+v", reason, report.Reasons)
		}
	}
	if len(report.Hints) == 0 {
		t.Fatal("expected sparse report to include remediation hints")
	}
}

func TestScoreQualityStaleContextLowersRecency(t *testing.T) {
	now := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	h := highQualityHandoff(now.Add(-10 * 24 * time.Hour))

	report := h.ScoreQuality(now)
	recency := qualityDimension(report, "recency")
	if recency == nil {
		t.Fatal("recency dimension missing")
	}
	if recency.Score >= 60 {
		t.Fatalf("recency score = %d, want below 60", recency.Score)
	}
	if !hasQualityReason(report, "recency.stale_week_plus") {
		t.Fatalf("expected stale reason, got %v", report.Reasons)
	}
}

func TestUpdateQualityStoresMachineReadableYAML(t *testing.T) {
	now := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	h := highQualityHandoff(now)
	h.UpdateQuality(now)

	if h.Quality == nil {
		t.Fatal("Quality was not stored on handoff")
	}
	data, err := yaml.Marshal(h)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	var decoded map[string]interface{}
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal quality yaml: %v", err)
	}
	quality, ok := decoded["quality"].(map[string]interface{})
	if !ok {
		t.Fatalf("quality field missing or wrong type in YAML:\n%s", string(data))
	}
	if quality["score"] == nil || quality["status"] == nil {
		t.Fatalf("quality score/status missing in YAML: %+v", quality)
	}
}

func TestQualityStatusIsMonotonicInScore(t *testing.T) {
	prev := QualityStatusLow
	rank := map[string]int{
		QualityStatusLow:    0,
		QualityStatusMedium: 1,
		QualityStatusHigh:   2,
	}
	for s := 0; s <= 100; s++ {
		got := qualityStatus(s)
		if rank[got] < rank[prev] {
			t.Fatalf("status went backward at score=%d: %q -> %q", s, prev, got)
		}
		prev = got
	}
	if qualityStatus(0) != QualityStatusLow {
		t.Fatalf("score=0 should map to %q, got %q", QualityStatusLow, qualityStatus(0))
	}
	if qualityStatus(59) != QualityStatusLow || qualityStatus(60) != QualityStatusMedium {
		t.Fatalf("low/medium boundary broken: 59=%q 60=%q", qualityStatus(59), qualityStatus(60))
	}
	if qualityStatus(79) != QualityStatusMedium || qualityStatus(80) != QualityStatusHigh {
		t.Fatalf("medium/high boundary broken: 79=%q 80=%q", qualityStatus(79), qualityStatus(80))
	}
}

func TestQualityStatusClampsOutOfRangeScores(t *testing.T) {
	if got := qualityStatus(clampQuality(150)); got != QualityStatusHigh {
		t.Fatalf("clamped over-100 score should map to %q, got %q", QualityStatusHigh, got)
	}
	if got := qualityStatus(clampQuality(-50)); got != QualityStatusLow {
		t.Fatalf("clamped negative score should map to %q, got %q", QualityStatusLow, got)
	}
}

func TestScoreQualityRoundsToNearestInsteadOfTruncating(t *testing.T) {
	now := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	h := New("rounding")
	h.CreatedAt = now
	h.UpdatedAt = now
	h.WithGoalAndNow("g", "n")
	h.Test = "t"
	h.AddTask("done", "internal/handoff/types.go")
	h.MarkModified("internal/handoff/types.go")
	h.AddDecision("d", "x")
	h.Next = []string{"keep going"}
	h.ActiveBeads = []string{"bd-x"}
	h.AgentMailThreads = []string{"thread"}
	h.CMMemories = []string{"mem"}
	h.SetAgentInfo("a", AgentTypeCodex, "%1")

	report := h.ScoreQuality(now)

	manualTotal := 0
	for _, dim := range report.Dimensions {
		manualTotal += dim.Score
	}
	expected := int(math.Round(float64(manualTotal) / float64(len(report.Dimensions))))
	if expected < 0 {
		expected = 0
	}
	if expected > 100 {
		expected = 100
	}
	if report.Score != expected {
		t.Fatalf("score = %d, want rounded mean %d (total=%d dims=%d)", report.Score, expected, manualTotal, len(report.Dimensions))
	}
	truncated := manualTotal / len(report.Dimensions)
	if expected != truncated && report.Score == truncated {
		t.Fatalf("score still uses integer truncation: got %d, rounded want %d", report.Score, expected)
	}
}

func highQualityHandoff(now time.Time) *Handoff {
	h := New("quality")
	h.CreatedAt = now
	h.UpdatedAt = now
	h.WithGoalAndNow("Implemented the handoff quality scorer", "Run the focused handoff package tests")
	h.WithStatus(StatusComplete, OutcomeSucceeded)
	h.Test = "rch exec -- go test -short -count=1 ./internal/handoff/..."
	h.AddTask("Added scoring helpers", "internal/handoff/quality.go", "internal/handoff/quality_test.go")
	h.AddDecision("scoring", "Use deterministic coverage, recency, actionability, and traceability dimensions")
	h.AddFinding("artifact", "Quality report is emitted in YAML")
	h.Next = []string{"Wire score into additional robot surfaces"}
	h.ActiveBeads = []string{"bd-2mb03.6.3"}
	h.AgentMailThreads = []string{"bd-2mb03.6.3"}
	h.CMMemories = []string{"memory:handoff-quality"}
	h.SetAgentInfo("YellowBluff", AgentTypeCodex, "%2")
	h.MarkModified("internal/handoff/types.go")
	return h
}

func hasQualityReason(report QualityReport, reason string) bool {
	for _, got := range report.Reasons {
		if got == reason {
			return true
		}
	}
	return false
}

func qualityDimension(report QualityReport, name string) *QualityDimensionScore {
	for i := range report.Dimensions {
		if report.Dimensions[i].Name == name {
			return &report.Dimensions[i]
		}
	}
	return nil
}
