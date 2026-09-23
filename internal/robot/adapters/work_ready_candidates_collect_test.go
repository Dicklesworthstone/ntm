package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// Exercise the actual live Collect surface, not only its filtering helper. The
// default br response ends before the eligible tail, exactly as a finite tool
// default or a small WorkItemLimit does on a heavily gated project.
func TestWorkReadyCollectFullCandidatesBeforePreviewLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tool fixtures require /bin/sh")
	}
	for _, behavior := range []string{"eligible tail", "null", "command failure", "source changed"} {
		t.Run(behavior, func(t *testing.T) {
			bv.InvalidateTriageCache()
			t.Cleanup(bv.InvalidateTriageCache)
			project := fastBeadsProject(t, 14, 0)
			var source strings.Builder
			var candidates []map[string]interface{}
			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("blocked-%02d", i)
				fmt.Fprintf(&source, "{\"id\":%q,\"status\":\"open\",\"dependencies\":[{\"type\":\"blocks\",\"depends_on_id\":\"unfinished\"}]}\n", id)
				candidates = append(candidates, map[string]interface{}{"id": id, "title": id, "priority": 1})
			}
			for _, id := range []string{"eligible-a", "eligible-b"} {
				fmt.Fprintf(&source, "{\"id\":%q,\"status\":\"open\",\"issue_type\":\"task\"}\n", id)
				candidates = append(candidates, map[string]interface{}{"id": id, "title": id, "priority": 2})
			}
			path := filepath.Join(project, ".beads", "issues.jsonl")
			if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			all, err := json.Marshal(candidates)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := json.Marshal(candidates[:10])
			if err != nil {
				t.Fatal(err)
			}
			fullRead := "printf '%s\\n' '" + string(all) + "'"
			switch behavior {
			case "null":
				fullRead = "printf '%s\\n' 'null'"
			case "command failure":
				fullRead = "exit 17"
			case "source changed":
				fullRead = "printf '%s\\n' '{\"id\":\"new\",\"status\":\"open\"}' >> .beads/issues.jsonl; " + fullRead
			}
			callsPath := filepath.Join(t.TempDir(), "calls")
			t.Setenv("NTM_READY_CANDIDATE_TEST_CALLS", callsPath)
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NTM_READY_CANDIDATE_TEST_CALLS\"\ncase \"$*\" in\n" +
				"  *stats*) printf '%s\\n' '{\"summary\":{\"total_issues\":14,\"open_issues\":14,\"in_progress_issues\":0,\"blocked_issues\":12,\"ready_issues\":14,\"closed_issues\":0}}' ;;\n" +
				"  *ready*--limit*) " + fullRead + " ;;\n" +
				"  *ready*) printf '%s\\n' '" + string(prefix) + "' ;;\n" +
				"  *) printf '%s\\n' '[]' ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "br"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			var fetches atomic.Int64
			mail := slowMailServer(t, 0, 0, &fetches)
			cfg := DefaultWorkCoordinationAdapterConfig(project)
			cfg.WorkItemLimit = 1
			cfg.AgentMailClient = agentmail.NewClient(agentmail.WithBaseURL(mail.URL+"/"), agentmail.WithProjectKey(project))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			batch, err := NewWorkCoordinationAdapter(cfg).Collect(ctx)
			if batch == nil || batch.Work == nil || batch.Work.Verification == nil {
				t.Fatalf("Collect lost its work receipt: %+v %v", batch, err)
			}
			work := batch.Work
			if behavior == "eligible tail" {
				v := work.Verification
				if err != nil || !work.Available || len(work.Ready) != 1 || work.Ready[0].ID != "eligible-a" || work.Summary.Ready != 2 || v.VerifiedReady == nil || *v.VerifiedReady != 2 || v.CountScope != "verified_candidates" || !v.PreviewTruncated || v.CandidatesObserved != 14 || len(v.Excluded) != 12 || v.ReportedReady != 14 {
					t.Fatalf("display cutoff hid the verified backlog: work=%+v receipt=%+v err=%v", work, v, err)
				}
			} else {
				if err == nil || work.Available || len(work.Ready) != 0 || work.Summary.Ready != 0 {
					t.Fatalf("incomplete collection became a healthy queue: %+v %v", work, err)
				}
				if behavior == "null" && (!errors.Is(err, bv.ErrReadyCandidatesIncomplete) || work.Verification.ReasonCode != "WORK_CANDIDATES_INCOMPLETE") {
					t.Fatalf("null response lost its typed receipt: %+v %v", work.Verification, err)
				}
				if behavior == "source changed" && !errors.Is(err, worksource.ErrStale) {
					t.Fatalf("full read escaped the source window: %v", err)
				}
			}
			calls, readErr := os.ReadFile(callsPath)
			if readErr != nil || !strings.Contains(string(calls), "ready --json --limit 100001") {
				t.Fatalf("Collect never requested the bounded full set: %s %v", calls, readErr)
			}
			if behavior != "source changed" {
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != source.String() {
					t.Fatalf("collection mutated the tracker: %v", readErr)
				}
			}
		})
	}
}
