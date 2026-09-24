package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestReservePathsRejectsIncompleteOrAmbiguousCoverage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"empty", "partial", "duplicate ID", "duplicate path", "foreign path", "missing ID", "ambiguous conflict"} {
		t.Run(mode, func(t *testing.T) {
			grant := map[string]any{"id": 41, "project_id": 7, "agent_name": "BlueLake", "path_pattern": "a.go", "expires_ts": "2099-01-01T00:00:00Z"}
			grants := []any{grant}
			paths := []string{"a.go"}
			conflicts := []any{}
			switch mode {
			case "empty":
				grants = []any{}
			case "partial":
				paths = append(paths, "b.go")
			case "duplicate ID":
				grants = append(grants, grant)
			case "duplicate path", "ambiguous conflict":
				grants = append(grants, map[string]any{"id": 42, "project_id": 7, "agent_name": "BlueLake", "path_pattern": "a.go", "expires_ts": "2099-01-01T00:00:00Z"})
				if mode == "ambiguous conflict" {
					conflicts = append(conflicts, map[string]any{"path": "b.go", "holders": []string{"Other"}})
				}
			case "foreign path":
				grant["path_pattern"] = "b.go"
			case "missing ID":
				grant["id"] = 0
			}
			reply := map[string]any{"granted": grants, "conflicts": conflicts}
			raw, _ := json.Marshal(reply)
			original, err := decodeReservationReply(raw)
			if err != nil {
				t.Fatal(err)
			}
			var writes, reads atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				if req.Method == "tools/call" {
					writes.Add(1)
					return reply, nil
				}
				reads.Add(1)
				return nil, &JSONRPCError{Code: -32000, Message: "invalid receipt must fail before readback"}
			})
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test", AgentName: "BlueLake", Paths: paths})
			if !errors.Is(err, ErrReservationUnverified) || !reflect.DeepEqual(result, original) {
				t.Fatalf("unsafe coverage was accepted or its handles changed: %+v %v", result, err)
			}
			if mode == "ambiguous conflict" && !errors.Is(err, ErrReservationConflict) {
				t.Fatal("conflict cause was lost")
			}
			if writes.Load() != 1 || reads.Load() != 0 {
				t.Fatalf("bad receipt caused more work: writes=%d reads=%d", writes.Load(), reads.Load())
			}
		})
	}
}

func TestReservePathsConflictOnlyCoverageRemainsAConflict(t *testing.T) {
	t.Parallel()
	c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
		if req.Method != "tools/call" {
			t.Error("conflict-only receipt must not read nonexistent grants")
		}
		return map[string]any{"granted": []any{}, "conflicts": []any{map[string]any{"path": "a.go", "holders": []string{"Other"}}}}, nil
	})
	result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test", AgentName: "BlueLake", Paths: []string{"a.go"}})
	if !errors.Is(err, ErrReservationConflict) || errors.Is(err, ErrReservationUnverified) || result == nil || len(result.Granted) != 0 || len(result.Conflicts) != 1 {
		t.Fatalf("valid conflict evidence was reclassified: %+v %v", result, err)
	}
}
