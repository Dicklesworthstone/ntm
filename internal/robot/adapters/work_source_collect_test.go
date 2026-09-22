package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// Exercise the same Collect entry point used by live robot projections. The
// tracker/tool disagreement is deliberate; no real Beads or agent is mutated.
func TestWorkProjectionCollectSurfaceVerifiesCandidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executables require /bin/sh")
	}
	for _, changingSource := range []bool{false, true} {
		name := "canonical eligibility"
		if changingSource {
			name = "source changes during triage"
		}
		t.Run(name, func(t *testing.T) {
			bv.InvalidateTriageCache()
			t.Cleanup(bv.InvalidateTriageCache)
			project := fastBeadsProject(t, 2, 1)
			brScript := `#!/bin/sh
case "$*" in
  *stats*) printf '%s\n' '{"summary":{"total_issues":3,"open_issues":2,"in_progress_issues":1,"blocked_issues":1,"ready_issues":2,"closed_issues":0}}' ;;
  *ready*) printf '%s\n' '[{"id":"join","title":"Blocked join","priority":1},{"id":"ready","title":"Ready task","priority":2}]' ;;
  *in_progress*) printf '%s\n' '[{"id":"builder","title":"Running builder","status":"in_progress","assignee":"worker"}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "br"), []byte(brScript), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(project, ".beads", "issues.jsonl")
			data := `{"id":"join","status":"open","dependencies":[{"depends_on_id":"builder","type":"blocks"}]}` + "\n" + `{"id":"builder","status":"in_progress"}` + "\n" + `{"id":"ready","status":"open"}` + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if changingSource {
				// Simulate a concurrent tracker writer while bv is collecting.
				// A pre-call change is not enough: the cache correctly refreshes
				// those entries instead of serving the old source.
				bvScript := `#!/bin/sh
printf '%s\n' '{"id":"added","status":"open"}' >> .beads/issues.jsonl
printf '%s\n' '{"triage":{"recommendations":[]}}'
`
				data += `{"id":"added","status":"open"}` + "\n"
				if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "bv"), []byte(bvScript), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var fetches atomic.Int64
			mail := slowMailServer(t, 0, 0, &fetches)
			cfg := DefaultWorkCoordinationAdapterConfig(project)
			cfg.AgentMailClient = agentmail.NewClient(agentmail.WithBaseURL(mail.URL+"/"), agentmail.WithProjectKey(project))
			batch, err := NewWorkCoordinationAdapter(cfg).Collect(ctx)
			if batch == nil || batch.Work == nil || batch.Work.Verification == nil {
				t.Fatalf("Collect omitted verification: %+v, %v", batch, err)
			}
			work := batch.Work
			if changingSource {
				if !errors.Is(err, worksource.ErrStale) || work.Available || len(work.Ready) != 0 || work.Verification.ReasonCode != worksource.StaleCode {
					t.Fatalf("stale triage was swallowed: %+v, %v", work, err)
				}
			} else {
				if err != nil || !work.Available || len(work.Ready) != 1 || work.Ready[0].ID != "ready" || work.Summary == nil || work.Summary.Ready != 1 {
					t.Fatalf("Collect advertised false-ready work: %+v, %v", work, err)
				}
				if work.Verification.Source == nil || work.Verification.Source.JSONLSHA256 == "" || work.Verification.ReportedReady != 2 || len(work.Verification.Excluded) != 1 || work.Verification.Excluded[0].ID != "join" {
					t.Fatalf("Collect lost verification evidence: %+v", work.Verification)
				}
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != data {
				t.Fatalf("Collect mutated the canonical export: %v", readErr)
			}
		})
	}
}
