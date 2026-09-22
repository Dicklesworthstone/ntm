package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func verifiedWorkFixture(t *testing.T) (string, string, *worksource.Snapshot) {
	t.Helper()
	project := t.TempDir()
	path := filepath.Join(project, ".beads", "issues.jsonl")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"id":"join","status":"open","dependencies":[{"depends_on_id":"builder","type":"blocks"}]}` + "\n" + `{"id":"builder","status":"in_progress"}` + "\n" + `{"id":"ready","status":"open"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := worksource.Read(context.Background(), project, worksource.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	return project, path, s
}

func workProjectionFixture() *WorkSection {
	work := NewWorkSection()
	work.Available = true
	work.Ready = []WorkItem{{ID: "join"}, {ID: "ready"}}
	work.InProgress = []WorkItem{{ID: "builder"}}
	work.Summary = &WorkSummary{Ready: 42, InProgress: 1, Total: 44}
	work.Triage = &WorkTriage{ReadyCount: 42, TopRecommendation: &WorkRecommendation{ID: "join"}}
	return work
}

func TestWorkProjectionCanonicalEligibilityPreservesEvidenceWithoutMutation(t *testing.T) {
	_, _, source := verifiedWorkFixture(t)
	work := workProjectionFixture()
	out := filterVerifiedWork(work, source, worksource.EligibilityPolicy{})
	if len(out.Ready) != 1 || out.Ready[0].ID != "ready" || out.Summary.Ready != 1 || out.Triage.ReadyCount != 1 || out.Triage.TopRecommendation != nil {
		t.Fatalf("false-ready projection: %+v", out)
	}
	if out.Verification.Source.JSONLSHA256 == "" || out.Verification.ReportedReady != 42 || out.Verification.CountScope != "verified_preview" || len(out.Verification.Excluded) != 1 {
		t.Fatalf("verification receipt: %+v", out.Verification)
	}
	if !reflect.DeepEqual(out.InProgress, work.InProgress) || work.Summary.Ready != 42 || work.Triage.TopRecommendation == nil || len(work.Ready) != 2 {
		t.Fatal("filter mutated input or discarded in-progress evidence")
	}
	if got := out.Verification.Excluded[0].BlockedBy; !reflect.DeepEqual(got, []string{"builder"}) {
		t.Fatalf("missing blocker evidence: %v", got)
	}
}

func TestWorkProjectionCollectionBindsSourceAndNormalizesReadyIDs(t *testing.T) {
	project, _, _ := verifiedWorkFixture(t)
	work := workProjectionFixture()
	work.Ready = []WorkItem{{ID: " ready ", BlockedBy: []string{"stale"}}, {ID: "ready"}, {ID: "join"}}
	out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) { return work, nil })
	if err != nil || len(out.Ready) != 1 || out.Ready[0].ID != "ready" || len(out.Ready[0].BlockedBy) != 0 || out.Verification.Source.ProjectDir != project {
		t.Fatalf("source-bound collection: %+v, %v", out, err)
	}
	if !reflect.DeepEqual(work.Ready[0].BlockedBy, []string{"stale"}) {
		t.Fatal("normalization mutated original blocker evidence")
	}
}

func TestWorkProjectionSourceChangeRejectsWithoutRepair(t *testing.T) {
	project, path, _ := verifiedWorkFixture(t)
	work := workProjectionFixture()
	changed := []byte(`{"id":"ready","status":"closed"}` + "\n")
	out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		return work, nil
	})
	if !errors.Is(err, worksource.ErrStale) || out.Available || len(out.Ready) != 0 || out.Summary.Ready != 0 || out.Verification.Mismatch == nil || out.Triage.TopRecommendation != nil {
		t.Fatalf("stale projection remained dispatchable: %+v %v", out, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !reflect.DeepEqual(after, changed) {
		t.Fatalf("verification repaired tracker: %v", readErr)
	}
	if len(work.Ready) != 2 || work.Summary.Ready != 42 {
		t.Fatal("verification mutated original evidence")
	}
}

func TestWorkProjectionExpectedIdentityRejectsBeforeToolCollection(t *testing.T) {
	project, _, source := verifiedWorkFixture(t)
	other, _, _ := verifiedWorkFixture(t)
	for _, dir := range []string{other, t.TempDir()} {
		calls := 0
		out, err := collectWorkWithSource(context.Background(), dir, WorkVerificationPolicy{Source: worksource.Policy{Expected: &source.Identity}}, func(context.Context) (*WorkSection, error) {
			calls++
			return workProjectionFixture(), nil
		})
		if !errors.Is(err, worksource.ErrStale) || out.Available || len(out.Ready) != 0 || calls != 0 {
			t.Fatalf("identity for %s accepted at %s: %+v, %v, calls=%d", project, dir, out, err, calls)
		}
	}
}

func TestWorkProjectionDBOnlyIsExplicitlyUnverified(t *testing.T) {
	project := t.TempDir()
	out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) { return workProjectionFixture(), nil })
	if err != nil || !out.Available || len(out.Ready) != 2 || out.Summary.Ready != 42 || out.Verification.Source != nil || out.Verification.CountScope != "tool_reported_unverified" {
		t.Fatalf("DB-only workspace regressed or acquired false provenance: %+v, %v", out, err)
	}
}

func TestWorkProjectionDBOnlyCannotBypassProgramPolicy(t *testing.T) {
	calls := 0
	out, err := collectWorkWithSource(context.Background(), t.TempDir(), WorkVerificationPolicy{ProgramLabels: []string{"program:allowed"}}, func(context.Context) (*WorkSection, error) {
		calls++
		return workProjectionFixture(), nil
	})
	if !errors.Is(err, worksource.ErrStale) || out.Available || calls != 0 {
		t.Fatalf("unverifiable program policy bypassed: %+v, %v, calls=%d", out, err, calls)
	}
}

func TestWorkProjectionExportAppearingDuringCollectionRejects(t *testing.T) {
	project := t.TempDir()
	out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		if err := os.Mkdir(filepath.Join(project, ".beads"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(`{"id":"ready","status":"open"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return workProjectionFixture(), nil
	})
	if !errors.Is(err, worksource.ErrStale) || out.Available || len(out.Ready) != 0 || out.Verification.ReasonCode != worksource.StaleCode {
		t.Fatalf("mixed source accepted: %+v %v", out, err)
	}
}

func TestWorkProjectionAllExcludedIsExplicitNotUnavailable(t *testing.T) {
	_, _, source := verifiedWorkFixture(t)
	work := workProjectionFixture()
	work.Ready = []WorkItem{{ID: "join"}}
	out := filterVerifiedWork(work, source, worksource.EligibilityPolicy{})
	if !out.Available || len(out.Ready) != 0 || out.Verification.ReasonCode != worksource.NoClaimableCode || len(out.Verification.Excluded) != 1 {
		t.Fatalf("all-excluded receipt: %+v", out)
	}
	if filterVerifiedWork(nil, nil, worksource.EligibilityPolicy{}).Available {
		t.Fatal("missing source became available")
	}
}

func TestWorkProjectionMalformedSourceNeverCollectsTools(t *testing.T) {
	project, path, _ := verifiedWorkFixture(t)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		calls++
		return workProjectionFixture(), nil
	})
	if !errors.Is(err, worksource.ErrStale) || out.Available || calls != 0 {
		t.Fatalf("malformed export accepted: %+v %v calls=%d", out, err, calls)
	}
}

func TestWorkProjectionCancellationAndDependencyErrorsAreNotSourceMismatches(t *testing.T) {
	project, _, _ := verifiedWorkFixture(t)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("br failed")} {
		out, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) { return workProjectionFixture(), cause })
		if !errors.Is(err, cause) || errors.Is(err, worksource.ErrStale) || out.Available || out.Verification.ReasonCode != "" || out.Summary.Ready != 0 {
			t.Fatalf("wrong failure classification: %+v, %v", out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := collectWorkWithSource(ctx, project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) { calls++; return nil, nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled collector ran: %v calls=%d", err, calls)
	}
}
