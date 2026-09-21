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
