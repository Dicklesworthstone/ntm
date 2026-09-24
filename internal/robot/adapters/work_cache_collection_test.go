package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestCollectWorkRejectsInvalidOrCancelledRequest(t *testing.T) {
	var absent *WorkCoordinationAdapter
	if _, err := absent.CollectWork(context.Background()); err == nil {
		t.Fatal("nil adapter accepted")
	}
	a := NewWorkCoordinationAdapter(DefaultWorkCoordinationAdapterConfig(t.TempDir()))
	if _, err := a.CollectWork(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if work, err := a.CollectWork(ctx); work != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled work request consulted sources: %+v %v", work, err)
	}
	batch, err := (workSnapshotCollector{a}).Collect(ctx)
	if !errors.Is(err, context.Canceled) || batch == nil || batch.Work != nil {
		t.Fatalf("selective collector lost cancellation: %+v %v", batch, err)
	}
}

func TestSnapshotFreezePreservesLiveIDNormalization(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte("{\"id\":\"a\",\"status\":\"open\"}\n{\"id\":\"b\",\"status\":\"open\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := NewWorkSection()
	input.Available = true
	input.Summary = &WorkSummary{Ready: 3}
	input.Ready = []WorkItem{{ID: " a ", Title: "first"}, {ID: "a", Title: "duplicate"}, {ID: "b", Title: "second"}}
	work, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) {
		return input, nil
	})
	if err != nil || work == nil || !work.Available || len(work.Ready) != 2 {
		t.Fatalf("freezing broke the live verifier's normalized identity contract: %+v %v", work, err)
	}
	_, payload, err := MarshalWorkSnapshot(work)
	if err != nil {
		t.Fatal(err)
	}
	var saved workSnapshotEnvelope
	if err := json.Unmarshal(payload, &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Work.Ready, []WorkItem{{ID: "a", Title: "first"}, {ID: "b", Title: "second"}}) {
		t.Fatalf("wrong candidate identity/order: %+v", saved.Work.Ready)
	}
	if input.Ready[0].ID != " a " || len(input.Ready) != 3 {
		t.Fatal("freeze mutated its source collection")
	}
	restored, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, payload)
	if err != nil || restored.Summary.Ready != 2 || len(restored.Ready) != 1 || restored.Ready[0].ID != "a" {
		t.Fatalf("normalized full candidate set did not survive restore: %+v %v", restored, err)
	}
}

func TestSnapshotFreezeStillRejectsIncompleteCandidates(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte("{\"id\":\"a\",\"status\":\"open\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, items := range map[string][]WorkItem{
		"missing":          nil,
		"blank ID":         {{ID: "  "}},
		"invalid priority": {{ID: "a", Priority: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			input := &WorkSection{Available: true, Ready: items}
			work, err := collectWorkWithSource(context.Background(), project, WorkVerificationPolicy{}, func(context.Context) (*WorkSection, error) { return input, nil })
			if err == nil || work == nil || work.Available || len(work.Ready) != 0 {
				t.Fatalf("incomplete candidate evidence became a healthy snapshot: %+v %v", work, err)
			}
		})
	}
}

// This is a native adapter/SQLite/HTTP regression. The selective query must
// read peers' reservations, but never inboxes, handoffs, or mutation endpoints.
func TestDurableWorkCollectsOnlyWorkAndStillExcludesPeerReservations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tool fixtures require /bin/sh")
	}
	project, bin := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte("{\"id\":\"a\",\"status\":\"open\"}\n{\"id\":\"b\",\"status\":\"open\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_MAIL_TOKEN", "")
	bv.InvalidateTriageCache()
	t.Cleanup(bv.InvalidateTriageCache)
	br := `#!/bin/sh
case "$*" in
 *stats*) printf '%s\n' '{"summary":{"total_issues":2,"open_issues":2,"in_progress_issues":0,"blocked_issues":0,"ready_issues":2,"closed_issues":0}}' ;;
 *ready*) printf '%s\n' '[{"id":"a","title":"Task A","priority":2},{"id":"b","title":"Task B","priority":2}]' ;;
 *) printf '%s\n' '[]' ;;
esac
`
	for name, script := range map[string]string{"br": br, "bv": "#!/bin/sh\nprintf '%s\\n' '{\"triage\":{\"recommendations\":[]}}'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var unrelated, reservationReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				URI string `json:"uri"`
			} `json:"params"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "resources/read" {
			unrelated.Add(1)
			http.Error(w, "unrelated coordination read", http.StatusServiceUnavailable)
			return
		}
		var data any
		switch {
		case strings.HasPrefix(request.Params.URI, "resource://project/"):
			data = map[string]any{"id": 7, "slug": "test", "human_key": project}
		case strings.HasPrefix(request.Params.URI, "resource://file_reservations/"):
			reservationReads.Add(1)
			data = []map[string]any{{"id": 1, "project_id": 7, "agent_name": "Peer", "path_pattern": "src/**", "reason": "bead assignment: a", "expires_ts": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}
		default:
			unrelated.Add(1)
			http.Error(w, "unrelated resource", http.StatusServiceUnavailable)
			return
		}
		text, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"contents": []map[string]string{{"uri": request.Params.URI, "mimeType": "application/json", "text": string(text)}}}})
	}))
	defer server.Close()
	t.Setenv("AGENT_MAIL_URL", server.URL+"/mcp/")
	store, err := state.Open(filepath.Join(t.TempDir(), "work.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultWorkCoordinationAdapterConfig(project)
	cfg.WorkItemLimit = 1
	for _, refresh := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		work, err := CollectDurableWork(ctx, store, cfg, refresh)
		cancel()
		if err != nil || work == nil || !work.Available || len(work.Ready) != 1 || work.Ready[0].ID != "b" || work.Summary.Ready != 1 {
			t.Fatalf("work-only query skipped eligibility: %+v %v", work, err)
		}
		if work.Verification.Reservations == nil || work.Verification.Reservations.State != "observed" || work.Verification.FromCache == refresh {
			t.Fatalf("missing live reservation/cache evidence: %+v", work.Verification)
		}
	}
	if unrelated.Load() != 0 || reservationReads.Load() != 2 {
		t.Fatalf("read unrelated coordination or skipped reservation refresh: unrelated=%d reservations=%d", unrelated.Load(), reservationReads.Load())
	}
}
