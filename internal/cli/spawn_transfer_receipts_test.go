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
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"gopkg.in/yaml.v3"
)

// Use the spawn recovery entry point, a real on-disk handoff, and the real
// Agent Mail HTTP client. An empty successful acquisition reply must not be
// promoted into recovered file coverage or trigger further remote mutations.
func TestSpawnRecoveryTransferRejectsEmptyGrantReceipt(t *testing.T) {
	project := t.TempDir()
	h := handoff.New("recover").WithGoalAndNow("Preserve active work", "Continue the previous task")
	h.ReservationTransfer = &handoff.ReservationTransfer{
		FromAgent: "old", ProjectKey: project,
		Reservations: []handoff.ReservationSnapshot{
			{PathPattern: "a.go", Exclusive: true},
			{PathPattern: "b.go", Exclusive: true},
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Agent Mail request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls = append(calls, request.Params.Name)
		mu.Unlock()
		var result any
		switch request.Params.Name {
		case "release_file_reservations":
			if request.Params.Arguments["agent_name"] != "old" {
				t.Errorf("untrusted grant reply authorized a cleanup: %+v", request.Params.Arguments)
			}
			result = map[string]any{"released": 2}
		case "file_reservation_paths":
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
	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL))
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
}
