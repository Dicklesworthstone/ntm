package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"gopkg.in/yaml.v3"
)

// Use the spawn recovery entry point, a real on-disk handoff, and the real
// Agent Mail HTTP client. An empty successful acquisition reply must not be
// promoted into recovered file coverage or trigger further remote mutations.
func TestSpawnRecoveryTransferRejectsEmptyGrantReceipt(t *testing.T) {
	project := t.TempDir()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
	h.ReservationTransfer = &handoff.ReservationTransfer{
		FromAgent: "old", ProjectKey: project,
		Reservations: []handoff.ReservationSnapshot{
			{ID: 1001, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "a.go", Exclusive: true},
			{ID: 1002, ProjectID: 73, AgentName: "old", CreatedAt: created, ExpiresAt: expires, PathPattern: "b.go", Exclusive: true},
		},
	}
	data, err := yaml.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(project, ".ntm", "handoffs", "recover")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260923-transfer.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var calls []string
	var reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				URI       string         `json:"uri"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Agent Mail request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if request.Method == "resources/read" {
			reads++
		} else {
			calls = append(calls, request.Params.Name)
		}
		mu.Unlock()
		var result any
		switch {
		case request.Method == "resources/read" && strings.HasPrefix(request.Params.URI, "resource://file_reservations/"):
			result = map[string]any{"contents": []map[string]any{{"text": `[{"id":1001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"},{"id":1002,"project_id":73,"agent_name":"old","path_pattern":"b.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
		case request.Method == "tools/call" && request.Params.Name == "release_file_reservations":
			if request.Params.Arguments["agent_name"] != "old" {
				t.Errorf("untrusted grant reply authorized a cleanup: %+v", request.Params.Arguments)
			}
			if _, hasPaths := request.Params.Arguments["paths"]; hasPaths || !reflect.DeepEqual(request.Params.Arguments["file_reservation_ids"], []any{float64(1001), float64(1002)}) {
				t.Errorf("spawn recovery lost captured source IDs: %+v", request.Params.Arguments)
			}
			result = map[string]any{"released": 2}
		case request.Method == "tools/call" && request.Params.Name == "file_reservation_paths":
			result = map[string]any{"granted": []any{}, "conflicts": []any{}}
		default:
			t.Errorf("unexpected recovery call: %s %s", request.Method, request.Params.Name)
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Errorf("encode Agent Mail response: %v", err)
		}
	}))
	defer server.Close()
	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
	result, err := attemptReservationTransfer(context.Background(), client, "recover", "new", project)
	if !errors.Is(err, handoff.ErrTransferGrantEvidence) || result == nil || result.Success || result.RolledBack || !result.OutcomeUnknown {
		t.Fatalf("spawn accepted incomplete reservation coverage: result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(result.ReleasedPaths, []string{"a.go", "b.go"}) || result.Attempts != 1 {
		t.Fatalf("spawn lost partial recovery evidence: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"release_file_reservations", "file_reservation_paths"}) {
		t.Fatalf("unexpected recovery mutations: %v", calls)
	}
	if reads != 1 {
		t.Fatalf("expected independent source preflight, got %d reads", reads)
	}
}

func TestSpawnRecoveryTransferRefusesStaleSourceWithoutChangingHandoff(t *testing.T) {
	for _, mode := range []string{"legacy", "replacement"} {
		for _, target := range []string{"new", "old"} {
			t.Run(mode+"/"+target, func(t *testing.T) {
				project := t.TempDir()
				lease := handoff.ReservationSnapshot{PathPattern: "a.go", Exclusive: true}
				if mode == "replacement" {
					lease.ID, lease.ProjectID, lease.AgentName = 1001, 73, "old"
					lease.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					lease.ExpiresAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
				h.ReservationTransfer = &handoff.ReservationTransfer{FromAgent: "old", ProjectKey: project, Reservations: []handoff.ReservationSnapshot{lease}}
				path, err := handoff.NewWriter(project).Write(h, "source-identity")
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				reads, mutations := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var rpc struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Params struct {
							URI string `json:"uri"`
						} `json:"params"`
					}
					if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
						t.Errorf("decode recovery preflight: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					var reply any = map[string]any{}
					mu.Lock()
					if rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/") {
						reads++
						reply = map[string]any{"contents": []map[string]any{{"text": `[{"id":2001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:01:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
					} else {
						mutations++
						t.Errorf("stale handoff authorized remote call: %s %s", rpc.Method, rpc.Params.URI)
					}
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
						t.Errorf("encode recovery preflight: %v", err)
					}
				}))
				defer server.Close()
				client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
				result, err := attemptReservationTransfer(context.Background(), client, "recover", target, project)
				if !errors.Is(err, handoff.ErrTransferSourceEvidence) || result == nil || result.Success || result.RolledBack || result.OutcomeUnknown || result.Attempts != 0 {
					t.Fatalf("recovery accepted or misclassified stale ownership: %+v %v", result, err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(before) != string(after) {
					t.Fatalf("refused recovery rewrote its handoff evidence: %v", readErr)
				}
				mu.Lock()
				defer mu.Unlock()
				wantReads := 1
				if mode == "legacy" {
					wantReads = 0
				}
				if mutations != 0 || reads != wantReads {
					t.Fatalf("unexpected recovery I/O: reads=%d mutations=%d", reads, mutations)
				}
			})
		}
	}
}
