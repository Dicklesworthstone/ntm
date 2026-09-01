package adapters

// Regression tests for #285: NTM status dropped completed Beads work state
// whenever Agent Mail enrichment (one fetch_inbox per historical identity,
// formerly sequential) outlived the shared collection deadline. Work and
// coordination must degrade independently, inbox reads must be bounded and
// concurrent, and no fetch may outlive the collection call.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// slowMailServer fakes the Agent Mail MCP surface: instant health_check and
// agent listing (identityCount identities), fetch_inbox delayed by inboxDelay.
func slowMailServer(t *testing.T, identityCount int, inboxDelay time.Duration, fetches *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
			Params struct {
				Name      string                 `json:"name"`
				URI       string                 `json:"uri"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		respond := func(result interface{}) {
			raw, _ := json.Marshal(result)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw),
			})
		}
		switch {
		case req.Method == "resources/read":
			agents := make([]map[string]interface{}, 0, identityCount)
			for i := range identityCount {
				agents = append(agents, map[string]interface{}{
					"id": i + 1, "name": fmt.Sprintf("Agent%03d", i), "program": "ntm", "model": "opus",
				})
			}
			text, _ := json.Marshal(map[string]interface{}{"agents": agents})
			respond(map[string]interface{}{
				"contents": []map[string]interface{}{{"text": string(text)}},
			})
		case req.Params.Name == "health_check":
			respond(map[string]interface{}{"status": "ok"})
		case req.Params.Name == "fetch_inbox":
			fetches.Add(1)
			time.Sleep(inboxDelay)
			respond(map[string]interface{}{"result": []interface{}{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "unknown tool: " + req.Params.Name},
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fastBeadsProject creates a project dir with .beads and puts fake bv/br
// binaries on PATH that answer instantly with a healthy backlog.
func fastBeadsProject(t *testing.T, ready, inProgress int) string {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	brScript := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *stats*) printf '%%s\n' '{"summary":{"total_issues":10,"open_issues":6,"in_progress_issues":%d,"blocked_issues":0,"ready_issues":%d,"closed_issues":4}}' ;;
  *) printf '%%s\n' '[]' ;;
esac
`, inProgress, ready)
	bvScript := `#!/bin/sh
printf '%s\n' '{"triage":{"recommendations":[]}}'
`
	for name, script := range map[string]string{"br": brScript, "bv": bvScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	return projectDir
}

// TestCollectPreservesWorkWhenMailEnrichmentTimesOut: fast Beads + 50 slow
// identities + a deadline the inbox sweep cannot meet. The completed work
// section must survive with correct counts; only coordination degrades.
func TestCollectPreservesWorkWhenMailEnrichmentTimesOut(t *testing.T) {
	projectDir := fastBeadsProject(t, 2, 4)
	var fetches atomic.Int64
	srv := slowMailServer(t, 50, 400*time.Millisecond, &fetches)

	cfg := DefaultWorkCoordinationAdapterConfig(projectDir)
	cfg.AgentMailClient = agentmail.NewClient(
		agentmail.WithBaseURL(srv.URL+"/"),
		agentmail.WithProjectKey(projectDir),
	)
	adapter := NewWorkCoordinationAdapter(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	batch, err := adapter.Collect(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Collect returned error %v; a coordination timeout must not fail a completed work section", err)
	}
	if batch == nil || batch.Work == nil || !batch.Work.Available || batch.Work.Summary == nil {
		t.Fatalf("work section missing/unavailable: %+v", batch)
	}
	if batch.Work.Summary.Ready != 2 || batch.Work.Summary.InProgress != 4 {
		t.Fatalf("work summary = ready %d / in_progress %d, want 2 / 4", batch.Work.Summary.Ready, batch.Work.Summary.InProgress)
	}
	// Coordination degrades honestly: either unavailable, or partial with an
	// explicit timeout reason — never silently presented as complete.
	if batch.Coordination == nil {
		t.Fatal("coordination section missing")
	}
	if batch.Coordination.Available && !strings.Contains(batch.Coordination.Reason, "timed out") {
		t.Fatalf("coordination = available with reason %q, want a timeout marker on a cut-short sweep", batch.Coordination.Reason)
	}
	// Bounded: the deadline plus at most one in-flight inbox round per worker.
	if elapsed > 4*time.Second {
		t.Fatalf("Collect took %v, want bounded near the 1.5s deadline", elapsed)
	}
	// No fetch outlives the call: the count is stable once Collect returns.
	after := fetches.Load()
	time.Sleep(600 * time.Millisecond)
	if final := fetches.Load(); final != after {
		t.Fatalf("inbox fetches continued after Collect returned: %d -> %d", after, final)
	}
}

// TestCollectAggregatorKeepsPartialBatchSections: even when an adapter DOES
// return an error, the aggregator must merge whatever sections its batch
// carries instead of discarding the batch wholesale.
func TestCollectAggregatorKeepsPartialBatchSections(t *testing.T) {
	agg := NewSignalAggregator(0)
	agg.RegisterAdapter(&partialBatchAdapter{})

	signals, err := agg.Collect(context.Background())
	if err != nil || signals == nil {
		t.Fatalf("aggregator Collect: signals=%v err=%v", signals, err)
	}
	if len(signals.CollectionErrors) != 1 {
		t.Fatalf("collection errors = %v, want the adapter's typed error recorded", signals.CollectionErrors)
	}
	if signals.Work == nil || !signals.Work.Available || signals.Work.Summary == nil || signals.Work.Summary.Ready != 7 {
		t.Fatalf("work section = %+v, want the partial batch's completed work merged", signals.Work)
	}
}

type partialBatchAdapter struct{}

func (p *partialBatchAdapter) Name() string                   { return "partial" }
func (p *partialBatchAdapter) Available(context.Context) bool { return true }
func (p *partialBatchAdapter) LastError() error               { return nil }
func (p *partialBatchAdapter) Collect(context.Context) (*SignalBatch, error) {
	work := NewWorkSection()
	work.Available = true
	work.Summary = &WorkSummary{Ready: 7}
	return &SignalBatch{Source: "partial", CollectedAt: time.Now(), Work: work}, context.DeadlineExceeded
}
