package assign

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// GH#371: filter inferred endpoint references before any reservation call,
// but never weaken a caller's explicit durable path set after a rejection.
func TestAssignmentReservationEndpointDiscoveryContract(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		name := "inferred endpoints filtered"
		if explicit {
			name = "explicit rejected pattern stays fail-closed"
		}
		t.Run(name, func(t *testing.T) {
			var reservations atomic.Int32
			projectKey := agentmail.CanonicalProjectKey(t.TempDir())
			project := agentmail.Project{ID: 1, HumanKey: projectKey}
			grant := reconcileReservation(71, "BlueLake", "src/soap/client.go", "bead assignment: bd-371")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req agentmail.JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				params, _ := req.Params.(map[string]interface{})
				var result any
				var rpcErr *agentmail.JSONRPCError
				switch {
				case req.Method == "tools/call" && params["name"] == "ensure_project":
					result = project
				case req.Method == "tools/call" && params["name"] == "file_reservation_paths":
					reservations.Add(1)
					args, _ := params["arguments"].(map[string]interface{})
					paths, _ := args["paths"].([]interface{})
					if explicit {
						if len(paths) != 2 || paths[0] != "/IRRIS.asmx" || paths[1] != "src/soap/client.go" {
							t.Errorf("explicit path set was silently reduced: %v", paths)
						}
						rpcErr = &agentmail.JSONRPCError{Code: -32602, Message: "SUSPICIOUS_PATTERN: absolute path"}
					} else if len(paths) != 1 || paths[0] != "src/soap/client.go" {
						t.Errorf("discovery sent endpoint references to Agent Mail: %v", paths)
						rpcErr = &agentmail.JSONRPCError{Code: -32602, Message: "SUSPICIOUS_PATTERN: absolute path"}
					} else {
						result = agentmail.ReservationResult{Granted: []agentmail.FileReservation{grant}}
					}
				case req.Method == "resources/read":
					var data any
					switch params["uri"] {
					case "resource://project/" + url.PathEscape(projectKey):
						data = project
					case "resource://file_reservations/" + url.PathEscape(projectKey) + "?active_only=true&format=json":
						data = []agentmail.FileReservation{grant}
					default:
						t.Errorf("unexpected resource: %v", params["uri"])
					}
					text, _ := json.Marshal(data)
					result = map[string]any{"contents": []map[string]string{{"text": string(text)}}}
				default:
					t.Errorf("unexpected request: %+v", req)
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: raw, Error: rpcErr})
			}))
			t.Cleanup(server.Close)
			manager := NewFileReservationManager(agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken("")), projectKey)
			paths, err := ReservationPathsForAssignment("Implement SOAP client",
				"Implement `/IRRIS.asmx` and `/api/v1/foo.json` in `src/soap/client.go`.")
			if err != nil {
				t.Fatal(err)
			}
			if explicit {
				paths = []string{"src/soap/client.go", "/IRRIS.asmx"}
			}
			result, err := manager.ReservePathsForBead(t.Context(), "bd-371", "BlueLake", paths)
			if result == nil {
				t.Fatalf("missing reservation result: %v", err)
			}
			if explicit {
				if result.Success || err == nil || !strings.Contains(err.Error(), "SUSPICIOUS_PATTERN") || len(result.RequestedPaths) != 2 {
					t.Fatalf("rejected explicit reservation was weakened: %+v, %v", result, err)
				}
			} else if err != nil || !result.Success || len(result.ReservationIDs) != 1 || result.ReservationIDs[0] != 71 {
				t.Fatalf("valid project reservation failed: %+v, %v", result, err)
			}
			if reservations.Load() != 1 {
				t.Fatalf("reservation attempted %d times, want exactly one", reservations.Load())
			}
		})
	}
}

// GH#328: exercise the production assignment reservation manager with the
// Rust server's compact grants and paginated, project-ID-less resource rows.
// The existing exact ownership validator is intentionally not bypassed.
func TestAssignmentReservationCompactServerContract(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"verified", "wrong owner", "wrong project", "explicit invalid grant", "malformed receipt"} {
		t.Run(mode, func(t *testing.T) {
			projectKey := agentmail.CanonicalProjectKey(t.TempDir())
			var reservations, releases atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req agentmail.JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				params, _ := req.Params.(map[string]interface{})
				project := map[string]any{"id": 73, "slug": "test", "human_key": projectKey}
				var result any
				if req.Method == "tools/call" {
					name, _ := params["name"].(string)
					switch name {
					case "ensure_project":
						result = project
					case "file_reservation_paths":
						reservations.Add(1)
						args, _ := params["arguments"].(map[string]interface{})
						if args["agent_name"] != "BlueLake" || args["reason"] != "bead assignment: bd-work" || args["exclusive"] != true {
							t.Errorf("assignment lost reservation policy: %+v", args)
						}
						grant := map[string]any{"id": 22, "path_pattern": "src/work.go", "reason": "bead assignment: bd-work", "exclusive": true, "expires_ts": "2099-01-01T00:00:00Z"}
						if mode == "explicit invalid grant" {
							grant["project_id"] = 0
							grant["agent_name"] = "BlueLake"
						}
						if mode == "malformed receipt" {
							grant["expires_ts"] = "bad timestamp"
						}
						result = map[string]any{"granted": []any{grant}, "conflicts": []any{}}
					default:
						releases.Add(1)
						t.Errorf("unexpected actuation: %s", name)
					}
				} else if req.Method == "resources/read" {
					uri, _ := params["uri"].(string)
					var text []byte
					if uri == "resource://project/"+url.PathEscape(projectKey) {
						if mode == "wrong project" {
							project["human_key"] = "/somewhere/else"
						}
						text, _ = json.Marshal(project)
					} else {
						prefix := "resource://file_reservations/" + url.PathEscape(projectKey) + "?"
						if !strings.HasPrefix(uri, prefix) {
							t.Errorf("unexpected resource URI: %q", uri)
						}
						q, _ := url.ParseQuery(strings.TrimPrefix(uri, prefix))
						offset, _ := strconv.Atoi(q.Get("offset"))
						count := 20
						if offset == 20 {
							count = 2
						} else if offset != 0 {
							t.Errorf("unexpected offset: %d", offset)
							count = 0
						}
						rows := make([]map[string]any, 0, count)
						for i := 0; i < count; i++ {
							id := offset + i + 1
							owner := "OtherAgent"
							path := fmt.Sprintf("src/other%d.go", id)
							if id == 22 {
								owner = "BlueLake"
								path = "src/work.go"
								if mode == "wrong owner" {
									owner = "OtherAgent"
								}
							}
							rows = append(rows, map[string]any{"id": id, "agent": owner, "path_pattern": path, "exclusive": true, "reason": "bead assignment: bd-work", "expires_ts": "2099-01-01T00:00:00Z"})
						}
						text, _ = json.Marshal(rows)
					}
					result = map[string]any{"contents": []map[string]string{{"text": string(text)}}}
				} else {
					t.Errorf("unexpected method: %s", req.Method)
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: raw})
			}))
			t.Cleanup(server.Close)
			client := agentmail.NewClient(agentmail.WithBaseURL(server.URL), agentmail.WithToken(""))
			manager := NewFileReservationManager(client, projectKey)
			result, err := manager.ReservePathsForBead(context.Background(), "bd-work", "BlueLake", []string{"src/work.go"})
			if result == nil || len(result.ReservationIDs) != 1 || result.ReservationIDs[0] != 22 {
				t.Fatalf("lost durable assignment handles: %+v %v", result, err)
			}
			if mode == "verified" {
				if err != nil || !result.Success || result.ExpiresAt == nil {
					t.Fatalf("real compact grant rejected by assignment: %+v %v", result, err)
				}
				recovered, recoverErr := manager.ReconcileForBead(context.Background(), "bd-work", "BlueLake", []string{"src/work.go"})
				if recoverErr != nil || recovered == nil || !recovered.Success || len(recovered.ReservationIDs) != 1 || recovered.ReservationIDs[0] != 22 {
					t.Fatalf("reconciliation cannot recover verified lease: %+v %v", recovered, recoverErr)
				}
			} else if err == nil || result.Success {
				t.Fatalf("unverified assignment authorized: %+v %v", result, err)
			}
			if reservations.Load() != 1 || releases.Load() != 0 {
				t.Fatalf("readback repeated/released a lease: reserves=%d other=%d", reservations.Load(), releases.Load())
			}
		})
	}
}
