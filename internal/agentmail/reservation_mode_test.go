package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Model a server whose omitted exclusive parameter defaults to true. The
// public acquisition method must preserve the requested mode in both cases;
// otherwise a shared handoff group can unexpectedly exclude all its peers.
func TestReservePathsForwardsExactReservationMode(t *testing.T) {
	t.Parallel()
	for _, exclusive := range []bool{false, true} {
		t.Run(fmt.Sprintf("exclusive=%v", exclusive), func(t *testing.T) {
			grant := func(mode bool) map[string]any {
				return map[string]any{
					"id": 91, "project_id": 7, "agent_name": "BlueLake",
					"path_pattern": "docs/**", "exclusive": mode,
					"reason": "handoff transfer from OldLake", "expires_ts": "2099-01-01T00:00:00Z",
				}
			}
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				if req.Method == "tools/call" {
					args, _ := params["arguments"].(map[string]interface{})
					mode := true
					if supplied, ok := args["exclusive"]; ok {
						var valid bool
						mode, valid = supplied.(bool)
						if !valid {
							t.Errorf("exclusive must be a JSON boolean, got %#v", supplied)
						}
					}
					return map[string]any{"granted": []any{grant(mode)}}, nil
				}
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					return reservationResourceFixture(`{"id":7,"slug":"test","human_key":"/test/project"}`), nil
				}
				raw, _ := json.Marshal([]any{grant(exclusive)})
				return reservationResourceFixture(string(raw)), nil
			})
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{
				ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"docs/**"},
				Exclusive: exclusive, Reason: "handoff transfer from OldLake",
			})
			if err != nil || result == nil || len(result.Granted) != 1 || result.Granted[0].Exclusive != exclusive {
				t.Fatalf("requested mode %v was changed: %+v %v", exclusive, result, err)
			}
		})
	}
}
