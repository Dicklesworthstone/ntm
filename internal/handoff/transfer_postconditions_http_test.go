package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Native package integration: the real Agent Mail client must issue independent
// resource reads after mutation, and must preserve ID-only wire selectors.
func TestTransferPostconditionsThroughAgentMailHTTP(t *testing.T) {
	for _, mode := range []string{"renewal success", "renewal owner changed", "cleanup success", "cleanup retained", "cleanup unreadable"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("NTM_CONFIG", "")
			fixture, opts := transferEvidenceFixture()
			live := fixture.live
			renewMode := strings.HasPrefix(mode, "renewal")
			if renewMode {
				opts.ToAgent = opts.FromAgent
				opts.TTLSeconds = 600
			} else {
				opts.Reservations[1].Exclusive = true
				row := live[1002]
				row.Exclusive = true
				live[1002] = row
			}
			var mu sync.Mutex
			var mutations []string
			acquisitions := 0
			renewed, cleaned := false, false
			observedRenewal, observedCleanup := false, false
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
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var reply any
				args := rpc.Params.Arguments
				switch {
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://project/"):
					reply = map[string]any{"contents": []map[string]any{{"text": `{"id":73,"slug":"project","human_key":"/project"}`}}}
				case rpc.Method == "resources/read" && strings.HasPrefix(rpc.Params.URI, "resource://file_reservations/"):
					observedRenewal = observedRenewal || renewed
					observedCleanup = observedCleanup || cleaned
					rows := make([]agentmail.FileReservation, 0, len(live))
					for _, row := range live {
						rows = append(rows, row)
					}
					sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
					text, err := json.Marshal(rows)
					if err != nil {
						t.Errorf("encode resource: %v", err)
					}
					if mode == "cleanup unreadable" && cleaned {
						text = []byte("null")
					}
					reply = map[string]any{"contents": []map[string]any{{"text": string(text)}}}
				case rpc.Method == "tools/call" && (rpc.Params.Name == "release_file_reservations" || rpc.Params.Name == "renew_file_reservations"):
					owner, _ := args["agent_name"].(string)
					mutations = append(mutations, rpc.Params.Name+":"+owner)
					wantIDs := []any{float64(1001), float64(1002)}
					if owner == "new" {
						wantIDs = []any{float64(201)}
					}
					if _, paths := args["paths"]; paths || !reflect.DeepEqual(args["file_reservation_ids"], wantIDs) || args["project_key"] != "project" {
						t.Errorf("mutation broadened its exact selector: %+v", args)
					}
					if rpc.Params.Name == "renew_file_reservations" {
						renewed = true
						for _, id := range []int{1001, 1002} {
							row := live[id]
							row.ExpiresTS.Time = time.Now().UTC().Truncate(time.Second).Add(600 * time.Second)
							if mode == "renewal owner changed" && id == 1002 {
								row.AgentName = "peer"
							}
							live[id] = row
						}
						reply = map[string]any{"renewed": 2}
					} else {
						cleaned = cleaned || owner == "new"
						for _, value := range wantIDs {
							if owner == "old" || mode != "cleanup retained" {
								delete(live, int(value.(float64)))
							}
						}
						reply = map[string]any{"released": len(wantIDs)}
					}
				case rpc.Method == "tools/call" && rpc.Params.Name == "file_reservation_paths":
					mutations = append(mutations, "file_reservation_paths")
					acquisitions++
					if args["agent_name"] != "new" {
						t.Error("failed postcondition authorized source reacquisition")
					}
					result := &agentmail.ReservationResult{}
					for i, path := range []string{"a.go", "b.go"} {
						id := 301 + i
						if acquisitions == 1 {
							id = 201
						}
						row := transferEvidenceLease(id, path, "new", true)
						live[id] = row
						result.Granted = append(result.Granted, row)
						if acquisitions == 1 {
							result.Conflicts = []agentmail.ReservationConflict{{Path: "b.go", Holders: []string{"peer"}}}
							break
						}
					}
					reply = result
				default:
					t.Errorf("unexpected request: %s %s %s", rpc.Method, rpc.Params.Name, rpc.Params.URI)
					http.Error(w, "unexpected", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": reply}); err != nil {
					t.Errorf("encode reply: %v", err)
				}
			}))
			defer server.Close()
			client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := TransferReservations(ctx, client, opts)
			mu.Lock()
			defer mu.Unlock()
			wantSuccess := strings.HasSuffix(mode, "success")
			if result.Success != wantSuccess || (err == nil) != wantSuccess || result.OutcomeUnknown == wantSuccess || result.RolledBack {
				t.Fatalf("wrong postcondition outcome: %+v %v mutations=%v", result, err, mutations)
			}
			if !wantSuccess && !errors.Is(err, ErrTransferPostcondition) {
				t.Fatalf("lost typed verification failure: %v", err)
			}
			if renewMode {
				if !observedRenewal || !reflect.DeepEqual(mutations, []string{"renew_file_reservations:old"}) || result.RenewalVerified != wantSuccess {
					t.Fatalf("renewal omitted independent read or repeated mutation: %+v %v", result, mutations)
				}
			} else {
				wantAttempts := 1
				if wantSuccess {
					wantAttempts = 2
				}
				if !observedCleanup || acquisitions != wantAttempts || result.Attempts != wantAttempts || len(mutations) != 2+wantAttempts {
					t.Fatalf("cleanup omitted read or authorized extra work: %+v %v", result, mutations)
				}
			}
		})
	}
}
