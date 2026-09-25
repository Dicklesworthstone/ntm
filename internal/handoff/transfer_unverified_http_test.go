package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Exercise the public transfer entry point with the real Agent Mail client,
// including the decoder and independent ownership readback. A failed check
// must not turn the preserved raw receipt into another release or acquisition.
func TestTransferUnverifiedAgentMailReceiptStopsRemoteMutations(t *testing.T) {
	for _, mode := range []string{"malformed timestamp", "foreign owner"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var mutations []string
			var resources []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name      string         `json:"name"`
						URI       string         `json:"uri"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
					t.Errorf("decode fixture request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				if rpc.Method == "tools/call" {
					mutations = append(mutations, rpc.Params.Name)
				} else {
					resources = append(resources, rpc.Params.URI)
				}
				beforeRelease := len(mutations) == 0
				mu.Unlock()

				var reply any
				switch {
				case rpc.Method == "tools/call" && rpc.Params.Name == "release_file_reservations":
					if rpc.Params.Arguments["agent_name"] != "old" {
						t.Error("ownership failure authorized destination cleanup")
					}
					if _, hasPaths := rpc.Params.Arguments["paths"]; hasPaths || !reflect.DeepEqual(rpc.Params.Arguments["file_reservation_ids"], []any{float64(1001), float64(1002)}) {
						t.Errorf("source release lost its ID-only wire scope: %+v", rpc.Params.Arguments)
					}
					reply = map[string]any{"released": 2}
				case rpc.Method == "tools/call" && rpc.Params.Name == "file_reservation_paths":
					if rpc.Params.Arguments["agent_name"] != "new" {
						t.Error("unverified ownership authorized source reacquisition")
					}
					expiry := "2099-01-01T00:00:00Z"
					if mode == "malformed timestamp" {
						expiry = "invalid"
					}
					reply = map[string]any{
						"granted":   []map[string]any{{"id": 7, "path_pattern": "a.go", "exclusive": true, "reason": "handoff transfer from old", "expires_ts": expiry}},
						"conflicts": []map[string]any{{"path": "b.go", "holders": []string{"peer"}}},
					}
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://project/"):
					reply = map[string]any{"contents": []map[string]any{{"text": `{"id":73,"slug":"project","human_key":"/project"}`}}}
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
					reply = map[string]any{"contents": []map[string]any{{"text": `[{"id":7,"project_id":73,"agent_name":"unrelated","path_pattern":"a.go","exclusive":true,"reason":"handoff transfer from old","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
					if beforeRelease {
						reply = map[string]any{"contents": []map[string]any{{"text": `[{"id":1001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"},{"id":1002,"project_id":73,"agent_name":"old","path_pattern":"b.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
					}
				default:
					t.Errorf("unexpected fixture call: %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
					t.Errorf("encode fixture response: %v", err)
				}
			}))
			defer server.Close()

			client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
			result, err := TransferReservations(context.Background(), client, receiptTransferOptions())
			if !errors.Is(err, ErrTransferGrantEvidence) || !errors.Is(err, agentmail.ErrReservationUnverified) || !errors.Is(err, agentmail.ErrReservationConflict) {
				t.Fatalf("error classifications lost across client/transfer boundary: %v", err)
			}
			if result.Success || result.RolledBack || !result.OutcomeUnknown || result.Attempts != 1 || !reflect.DeepEqual(result.GrantedPaths, []string{"a.go"}) {
				t.Fatalf("invalid remote receipt used or discarded: %+v", result)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(mutations, []string{"release_file_reservations", "file_reservation_paths"}) {
				t.Fatalf("unverified receipt triggered further mutations: %v", mutations)
			}
			if mode == "foreign owner" && len(resources) != 3 {
				t.Fatalf("expected source preflight plus independent grant project and reservation reads, got %v", resources)
			}
			if mode == "malformed timestamp" && len(resources) != 1 {
				t.Fatalf("bad receipt initiated reads beyond source preflight: %v", resources)
			}
		})
	}
}

func TestTransferSourceReplacementIsFencedOnAgentMailWire(t *testing.T) {
	for _, operation := range []string{"release", "renew"} {
		t.Run(operation, func(t *testing.T) {
			var mu sync.Mutex
			liveID := 1001
			replacementUntouched := true
			reads, mutations := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name      string         `json:"name"`
						URI       string         `json:"uri"`
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
					t.Errorf("decode source lease request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var reply any
				switch {
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
					reads++
					reply = map[string]any{"contents": []map[string]any{{"text": `[{"id":1001,"project_id":73,"agent_name":"old","path_pattern":"a.go","exclusive":true,"created_ts":"2026-01-01T00:00:00Z","expires_ts":"2099-01-01T00:00:00Z"}]`}}}
					liveID = 2001 // Another process replaces the observed lease before mutation.
				case rpc.Method == "tools/call" && rpc.Params.Name == operation+"_file_reservations":
					mutations++
					args := rpc.Params.Arguments
					if args["agent_name"] != "old" || args["project_key"] != "project" {
						t.Errorf("wire changed source ownership: %+v", args)
					}
					if _, hasPaths := args["paths"]; hasPaths || !reflect.DeepEqual(args["file_reservation_ids"], []any{float64(1001)}) {
						t.Errorf("wire broadened the captured lease selector: %+v", args)
					}
					// Model a server that unions selectors. Sending the old path,
					// even alongside its ID, would mutate the replacement lease.
					selected := false
					if paths, ok := args["paths"].([]any); ok {
						for _, path := range paths {
							selected = selected || path == "a.go"
						}
					}
					if ids, ok := args["file_reservation_ids"].([]any); ok {
						for _, id := range ids {
							selected = selected || id == float64(liveID)
						}
					}
					count := 0
					if selected {
						count = 1
						replacementUntouched = false
					}
					field := "released"
					if operation == "renew" {
						field = "renewed"
					}
					reply = map[string]any{field: count}
				default:
					mutations++
					t.Errorf("source uncertainty authorized another call: %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
					reply = map[string]any{}
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
					t.Errorf("encode source lease response: %v", err)
				}
			}))
			defer server.Close()
			client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
			opts := receiptTransferOptions()
			opts.Reservations = opts.Reservations[:1]
			if operation == "renew" {
				opts.ToAgent = opts.FromAgent
			}
			result, err := TransferReservations(context.Background(), client, opts)
			if err == nil || result.Success || result.RolledBack || !result.OutcomeUnknown || result.Attempts != 0 || result.Stage != operation {
				t.Fatalf("unconfirmed source lease permitted transfer: %+v %v", result, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !replacementUntouched || liveID != 2001 || reads != 1 || mutations != 1 {
				t.Fatalf("replacement changed or unexpected I/O: untouched=%v ID=%d reads=%d mutations=%d", replacementUntouched, liveID, reads, mutations)
			}
		})
	}
}
