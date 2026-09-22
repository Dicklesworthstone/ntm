package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func TestWorkReadySourceWindowIncludesFullCandidateRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tracker fixture uses /bin/sh")
	}
	for _, behavior := range []string{"eligible tail", "checked empty", "null", "failed", "source changed"} {
		t.Run(behavior, func(t *testing.T) {
			project := t.TempDir()
			if err := os.Mkdir(filepath.Join(project, ".beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			var source strings.Builder
			var candidates []map[string]interface{}
			limited := &WorkSection{Available: true, Summary: &WorkSummary{Ready: 14}, Triage: &WorkTriage{ReadyCount: 14}}
			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("blocked-%02d", i)
				fmt.Fprintf(&source, "{\"id\":%q,\"status\":\"open\",\"dependencies\":[{\"type\":\"blocks\",\"depends_on_id\":\"unfinished\"}]}\n", id)
				candidates = append(candidates, map[string]interface{}{"id": id, "title": id, "priority": 1})
				if i < 10 {
					limited.Ready = append(limited.Ready, WorkItem{ID: id})
				}
			}
			for _, id := range []string{"eligible-a", "eligible-b"} {
				fmt.Fprintf(&source, "{\"id\":%q,\"status\":\"open\",\"issue_type\":\"task\"}\n", id)
				candidates = append(candidates, map[string]interface{}{"id": id, "title": id, "priority": 2})
			}
			path := filepath.Join(project, ".beads", "issues.jsonl")
			if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(candidates)
			if err != nil {
				t.Fatal(err)
			}
			command := "printf '%s\\n' '" + string(payload) + "'\n"
			switch behavior {
			case "checked empty":
				command = "printf '%s\\n' '[]'\n"
			case "null":
				command = "printf '%s\\n' 'null'\n"
			case "failed":
				command = "exit 17\n"
			case "source changed":
				command = "printf '%s\\n' '{\"id\":\"changed\",\"status\":\"open\"}' >> .beads/issues.jsonl\n" + command
			}
			bin := t.TempDir()
			script := "#!/bin/sh\n[ \"$1\" = ready ] && [ \"$3\" = --limit ] && [ \"$4\" = 100001 ] || exit 23\n" + command
			if err := os.WriteFile(filepath.Join(bin, "br"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := collectWorkWithSource(ctx, project, WorkVerificationPolicy{}, func(ctx context.Context) (*WorkSection, error) {
				ready, err := bv.GetReadyCandidatesContext(ctx, project)
				if err != nil {
					return limited, err
				}
				return workWithReadyCandidates(limited, ready), nil
			})
			if behavior == "eligible tail" || behavior == "checked empty" {
				if err != nil || !out.Available {
					t.Fatalf("ready collection failed: %+v %v", out, err)
				}
				out = limitVerifiedWorkPreview(out, 1)
				if behavior == "eligible tail" {
					if len(out.Ready) != 1 || out.Ready[0].ID != "eligible-a" || out.Summary.Ready != 2 || out.Verification.VerifiedReady == nil || *out.Verification.VerifiedReady != 2 || !out.Verification.PreviewTruncated || out.Verification.CandidatesObserved != 14 || len(out.Verification.Excluded) != 12 {
						t.Fatalf("eligible tail starved behind the old cutoff: %+v verification=%+v", out, out.Verification)
					}
				} else if len(out.Ready) != 0 || out.Summary.Ready != 0 || out.Verification.ReasonCode != worksource.NoClaimableCode {
					t.Fatalf("empty direct ready set was replaced by stale preview/source rows: %+v", out)
				}
			} else {
				if err == nil || out.Available || len(out.Ready) != 0 || out.Summary.Ready != 0 {
					t.Fatalf("failed collection was advertised as ready: %+v %v", out, err)
				}
				if behavior == "null" && !errors.Is(err, bv.ErrReadyCandidatesIncomplete) {
					t.Fatalf("null response lost typed error: %v", err)
				}
				if behavior == "source changed" && !errors.Is(err, worksource.ErrStale) {
					t.Fatalf("candidate read escaped source validation: %v", err)
				}
			}
			if limited.Summary.Ready != 14 || len(limited.Ready) != 10 {
				t.Fatal("verification mutated the earlier preview")
			}
			if behavior != "source changed" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != source.String() {
					t.Fatalf("verification mutated tracker contents: %v", err)
				}
			}
		})
	}
}

func TestWorkReadyCandidatesReplaceLimitedMembershipWithoutMutation(t *testing.T) {
	score := 0.9
	original := &WorkSection{Available: true, Ready: []WorkItem{
		{ID: "a", Title: "old", Priority: 4, Score: &score, Unblocks: 3},
		{ID: "no-longer-ready", Title: "stale"},
	}, Summary: &WorkSummary{Ready: 99}}
	complete := workWithReadyCandidates(original, []bv.BeadPreview{
		{ID: "a", Title: "current", Priority: "P1"},
		{ID: "b", Title: "beyond old cutoff", Priority: "P2"},
	})
	if len(complete.Ready) != 2 || complete.Ready[0].Title != "current" || complete.Ready[0].Priority != 1 || complete.Ready[0].Score != &score || complete.Ready[0].Unblocks != 3 || complete.Ready[1].ID != "b" {
		t.Fatalf("complete membership or enrichment lost: %+v", complete.Ready)
	}
	if len(original.Ready) != 2 || original.Ready[0].Title != "old" || original.Ready[1].ID != "no-longer-ready" || original.Summary.Ready != 99 {
		t.Fatalf("summary input mutated: %+v", original)
	}
	if complete.Summary.Ready != 99 {
		t.Fatal("unchecked reported total was silently rewritten")
	}
}

func TestWorkReadyPreviewLimitPreservesVerifiedTotalAndReceipt(t *testing.T) {
	original := &WorkSection{
		Available:    true,
		Ready:        []WorkItem{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Summary:      &WorkSummary{Ready: 3},
		Triage:       &WorkTriage{ReadyCount: 3, TopRecommendation: &WorkRecommendation{ID: "c"}},
		Verification: &WorkVerification{CountScope: "verified_preview", ReportedReady: 15, Excluded: []worksource.Exclusion{{ID: "blocked"}}},
	}
	limited := limitVerifiedWorkPreview(original, 2)
	if !reflect.DeepEqual(limited.Ready, []WorkItem{{ID: "a"}, {ID: "b"}}) || limited.Summary.Ready != 3 || limited.Triage.ReadyCount != 3 {
		t.Fatalf("display limit changed verified total: %+v", limited)
	}
	v := limited.Verification
	if v.CountScope != "verified_candidates" || v.VerifiedReady == nil || *v.VerifiedReady != 3 || v.CandidatesObserved != 4 || v.PreviewLimit != 2 || !v.PreviewTruncated || v.ReportedReady != 15 {
		t.Fatalf("inaccurate verification receipt: %+v", v)
	}
	if limited.Triage.TopRecommendation != nil {
		t.Fatal("recommendation outside the returned preview remained visible")
	}
	if len(original.Ready) != 3 || original.Verification.CountScope != "verified_preview" || original.Verification.VerifiedReady != nil || original.Triage.TopRecommendation == nil {
		t.Fatal("limiting the output mutated its source")
	}
}

func TestWorkReadyPreviewDoesNotClaimVerificationForDatabaseOnly(t *testing.T) {
	work := &WorkSection{Available: true, Ready: []WorkItem{{ID: "a"}, {ID: "b"}}, Summary: &WorkSummary{Ready: 27}, Verification: &WorkVerification{CountScope: "tool_reported_unverified", ReportedReady: 27}}
	out := limitVerifiedWorkPreview(work, 1)
	if out.Verification.VerifiedReady != nil || out.Verification.CountScope != "tool_reported_unverified" || !out.Verification.PreviewTruncated || out.Summary.Ready != 27 {
		t.Fatalf("DB-only preview was misrepresented as source-verified: %+v", out)
	}
}

func TestWorkReadyIncompleteCandidatesFailClosedWithTypedReceipt(t *testing.T) {
	work := &WorkSection{Available: true, Ready: []WorkItem{{ID: "unsafe"}}, Summary: &WorkSummary{Ready: 4}, Triage: &WorkTriage{ReadyCount: 4, TopRecommendation: &WorkRecommendation{ID: "unsafe"}}}
	err := errors.Join(errors.New("tracker returned null"), bv.ErrReadyCandidatesIncomplete)
	out := rejectWorkSource(work, err)
	if out.Available || len(out.Ready) != 0 || out.Summary.Ready != 0 || out.Triage.ReadyCount != 0 || out.Triage.TopRecommendation != nil || out.Verification.ReasonCode != "WORK_CANDIDATES_INCOMPLETE" || out.Verification.ReportedReady != 4 {
		t.Fatalf("failed candidate collection became a healthy queue: %+v", out)
	}
}

func TestWorkReadyCheckedEmptyReceiptIsNotPreviewStarvation(t *testing.T) {
	work := &WorkSection{Available: true, Ready: []WorkItem{}, Summary: &WorkSummary{}, Verification: &WorkVerification{CountScope: "verified_preview", ReasonCode: worksource.NoClaimableCode, Excluded: []worksource.Exclusion{}}}
	out := limitVerifiedWorkPreview(work, 5)
	if out.Verification.VerifiedReady == nil || *out.Verification.VerifiedReady != 0 || out.Verification.PreviewTruncated || out.Verification.PreviewLimit != 5 || out.Verification.ReasonCode != worksource.NoClaimableCode {
		t.Fatalf("checked empty verification ambiguous: %+v", out.Verification)
	}
}
