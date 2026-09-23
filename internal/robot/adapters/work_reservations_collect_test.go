package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// Exercise the production Collect -> source checks -> Agent Mail pagination ->
// eligibility -> preview path. All external reads use temporary tool fixtures
// and a local MCP server; no live agent or tracker state is changed.
func TestWorkReservationCollectSurfaceReadsPeerOnLastPage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tool fixtures require /bin/sh")
	}
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%t", unavailable), func(t *testing.T) {
			bv.InvalidateTriageCache()
			t.Cleanup(bv.InvalidateTriageCache)
			project := fastBeadsProject(t, 4, 0)
			path := filepath.Join(project, ".beads", "issues.jsonl")
			if err := os.WriteFile(path, []byte(workReservationFixtureJSONL), 0o600); err != nil {
				t.Fatal(err)
			}
			br := `#!/bin/sh
case "$*" in
  *stats*) printf '%s\n' '{"summary":{"total_issues":5,"open_issues":4,"in_progress_issues":0,"blocked_issues":0,"ready_issues":4,"closed_issues":1}}' ;;
  *ready*) printf '%s\n' '[{"id":"reserved","title":"Reserved","priority":0},{"id":"mutex-task","title":"Mutex task","priority":1},{"id":"independent-a","title":"Independent A","priority":2},{"id":"independent-b","title":"Independent B","priority":3}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "br"), []byte(br), 0o700); err != nil {
				t.Fatal(err)
			}
			var pageReads, projectReads, mutations atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"status":"ok"}`))
					return
				}
				var req struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
					Params struct {
						URI  string `json:"uri"`
						Name string `json:"name"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 400)
					return
				}
				respond := func(value any) {
					text, _ := json.Marshal(value)
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"contents": []map[string]any{{"text": string(text)}}}})
				}
				if req.Method != "resources/read" {
					if req.Params.Name == "health_check" {
						_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"status": "ok"}})
						return
					}
					mutations.Add(1)
					http.Error(w, "only read-only resources are supported", 400)
					return
				}
				switch {
				case strings.HasPrefix(req.Params.URI, "resource://project/"):
					projectReads.Add(1)
					respond(map[string]any{"id": 17, "human_key": project, "slug": "fixture-project"})
				case strings.HasPrefix(req.Params.URI, "resource://file_reservations/"):
					pageReads.Add(1)
					if unavailable {
						http.Error(w, "reservation service unavailable", 503)
						return
					}
					u, err := url.Parse(req.Params.URI)
					if err != nil {
						t.Error(err)
						http.Error(w, "bad URI", 400)
						return
					}
					offset, _ := strconv.Atoi(u.Query().Get("offset"))
					rows := make([]map[string]any, 0)
					for i := offset; i < 21 && i < offset+20; i++ {
						reason := "unmapped path reservation"
						if i == 19 {
							reason = "bead assignment: closing"
						}
						if i == 20 {
							reason = "bead assignment: reserved"
						}
						// Omit project_id as the real resource does. The client
						// must resolve the independent project resource.
						rows = append(rows, map[string]any{"id": i + 1, "agent": "Peer", "path_pattern": fmt.Sprintf("src/%d.go", i), "exclusive": true, "reason": reason, "expires_ts": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
					}
					respond(rows)
				case strings.HasPrefix(req.Params.URI, "resource://agents/"):
					respond(map[string]any{"agents": []any{}})
				default:
					t.Errorf("unexpected resource: %s", req.Params.URI)
					http.Error(w, "unexpected resource", 400)
				}
			}))
			defer server.Close()
			cfg := DefaultWorkCoordinationAdapterConfig(project)
			cfg.WorkItemLimit = 1
			cfg.AgentMailClient = agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/"), agentmail.WithProjectKey(project))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			batch, err := NewWorkCoordinationAdapter(cfg).Collect(ctx)
			if err != nil || batch == nil || batch.Work == nil || !batch.Work.Available || batch.Work.Verification == nil {
				t.Fatalf("Collect: %+v, %v", batch, err)
			}
			work := batch.Work
			receipt := work.Verification.Reservations
			if receipt == nil {
				t.Fatal("Collect omitted reservation evidence")
			}
			if unavailable {
				if receipt.State != "unavailable" || receipt.Reason == "" || work.Summary.Ready != 4 {
					t.Fatalf("failed read erased backlog or claimed checked-empty: %+v, %+v", work, receipt)
				}
			} else {
				if len(work.Ready) != 1 || work.Ready[0].ID != "independent-a" || work.Summary.Ready != 2 || len(work.Verification.Excluded) != 2 || receipt.State != "observed" || receipt.Active != 21 || receipt.Unmapped != 19 || receipt.MappedBeads != 2 || pageReads.Load() < 2 || projectReads.Load() < 2 {
					t.Fatalf("peer/closing mutex/last-page evidence lost: %+v, %+v", work, receipt)
				}
			}
			if mutations.Load() != 0 {
				t.Fatalf("inspection made %d non-read calls", mutations.Load())
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != workReservationFixtureJSONL {
				t.Fatalf("Collect changed tracker: %v", readErr)
			}
		})
	}
}
