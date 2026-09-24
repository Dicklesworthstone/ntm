package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture speaks the real resources/read and tools/call protocol. In
// particular, Rust resource rows use agent and omit project_id (GH#328).
func reservationReadbackClient(t *testing.T, handle func(*http.Request, JSONRPCRequest) (any, *JSONRPCError)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result, rpcErr := handle(r, req)
		raw, err := json.Marshal(result)
		if err != nil {
			t.Errorf("encode fixture: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: raw, Error: rpcErr})
	}))
	t.Cleanup(server.Close)
	return NewClient(WithBaseURL(server.URL), WithToken(""))
}

func reservationResourceFixture(text string) any {
	return map[string]any{"contents": []map[string]any{{"text": text, "mimeType": "application/json"}}}
}

func reservationRowsFixture(offset, count int) []map[string]any {
	rows := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		id := offset + i + 1
		owner := "OtherAgent"
		if id == 41 {
			owner = "BlueLake"
		}
		rows = append(rows, map[string]any{
			"id": id, "agent": owner, "path_pattern": fmt.Sprintf("src/file%d.go", id),
			"exclusive": true, "reason": "bead assignment: bd-work",
			"expires_ts": "2099-01-01T01:00:00Z",
		})
	}
	return rows
}

func TestReservationReadbackPagesBeforeFiltering(t *testing.T) {
	t.Parallel()
	const key = "/test/project with space"
	var pageReads, projectReads atomic.Int32
	c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
		if req.Method != "resources/read" {
			t.Errorf("readback attempted mutation: %s", req.Method)
			return nil, &JSONRPCError{Code: -32601, Message: "read only"}
		}
		params, _ := req.Params.(map[string]interface{})
		uri, _ := params["uri"].(string)
		if uri == "resource://project/"+url.PathEscape(key) {
			projectReads.Add(1)
			return reservationResourceFixture(`{"id":73,"slug":"test-project","human_key":"/test/project with space"}`), nil
		}
		prefix := "resource://file_reservations/" + url.PathEscape(key) + "?"
		if !strings.HasPrefix(uri, prefix) {
			t.Errorf("wrong project URI: %s", uri)
			return nil, &JSONRPCError{Code: -32602, Message: "wrong project"}
		}
		q, _ := url.ParseQuery(strings.TrimPrefix(uri, prefix))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if offset != 0 && q.Get("limit") != "20" {
			t.Error("continuation must specify its page size")
		}
		if q.Get("active_only") != "true" || q.Get("format") != "json" {
			t.Errorf("missing read contract: %s", uri)
		}
		count := 20
		if offset == 40 {
			count = 1
		} else if offset != 0 && offset != 20 {
			t.Errorf("wrong offset %d", offset)
			count = 0
		}
		pageReads.Add(1)
		raw, _ := json.Marshal(reservationRowsFixture(offset, count))
		return reservationResourceFixture(string(raw)), nil
	})
	rows, err := c.ListReservations(context.Background(), key, "BlueLake", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 41 || rows[0].ProjectID != 73 || rows[0].AgentName != "BlueLake" {
		t.Fatalf("lost last-page ownership: %+v", rows)
	}
	found, err := c.GetReservation(context.Background(), key, 41)
	if err != nil || found == nil || found.ProjectID != 73 {
		t.Fatalf("GetReservation missed last-page lease: %+v, %v", found, err)
	}
	if pageReads.Load() != 6 || projectReads.Load() != 2 {
		t.Fatalf("unexpected read counts: pages=%d projects=%d", pageReads.Load(), projectReads.Load())
	}
}

func TestReservationReadbackRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result any
	}{
		{"missing content", map[string]any{}},
		{"null envelope", nil},
		{"empty content", reservationResourceFixture("")},
		{"null data", reservationResourceFixture("null")},
		{"object instead of rows", reservationResourceFixture(`{}`)},
		{"malformed data", reservationResourceFixture(`[{`)},
		{"multiple content", map[string]any{"contents": []map[string]any{{"text": "[]"}, {"text": "[]"}}}},
		{"missing durable ID", reservationResourceFixture(`[{"agent":"BlueLake","path_pattern":"x.go"}]`)},
		{"missing owner", reservationResourceFixture(`[{"id":1,"path_pattern":"x.go"}]`)},
		{"conflicting owners", reservationResourceFixture(`[{"id":1,"path_pattern":"x.go","agent":"BlueLake","agent_name":"Other"}]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				requests.Add(1)
				return tc.result, nil
			})
			rows, err := c.ListReservations(context.Background(), "/test/project", "", true)
			if err == nil || rows != nil || requests.Load() != 1 {
				t.Fatalf("incomplete evidence accepted or retried: rows=%+v err=%v calls=%d", rows, err, requests.Load())
			}
		})
	}
}

func TestReservationReadbackNeverReturnsPartialPages(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"late error", "repeated page", "row limit"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				n := int(calls.Add(1))
				if req.Method != "resources/read" {
					t.Error("partial page failure must not fall back to legacy tools")
				}
				if mode == "late error" && n == 2 {
					return nil, &JSONRPCError{Code: -32000, Message: "later page unavailable"}
				}
				offset := (n - 1) * 20
				if mode == "repeated page" {
					offset = 0
				}
				raw, _ := json.Marshal(reservationRowsFixture(offset, 20))
				return reservationResourceFixture(string(raw)), nil
			})
			rows, err := c.ListReservations(context.Background(), "/test/project", "BlueLake", false)
			if err == nil || rows != nil {
				t.Fatalf("returned partial evidence: %+v, %v", rows, err)
			}
			want := int32(2)
			if mode == "row limit" {
				want = maxReservationReadbackRows/20 + 1
			}
			if calls.Load() != want {
				t.Fatalf("requests=%d, want %d", calls.Load(), want)
			}
		})
	}
}

func TestReservationReadbackValidatesServerProject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, project string
		explicitID    bool
	}{
		{"wrong project", `{"id":73,"slug":"elsewhere","human_key":"/different/project"}`, false},
		{"missing numeric ID", `{"slug":"test","human_key":"/test/project"}`, false},
		{"contradictory row", `{"id":73,"slug":"test","human_key":"/test/project"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					return reservationResourceFixture(tc.project), nil
				}
				rows := reservationRowsFixture(0, 2)
				if tc.explicitID {
					rows[0]["project_id"] = 99
				}
				raw, _ := json.Marshal(rows)
				return reservationResourceFixture(string(raw)), nil
			})
			rows, err := c.ListReservations(context.Background(), "/test/project", "", true)
			if err == nil || rows != nil {
				t.Fatalf("unbound rows accepted: %+v %v", rows, err)
			}
		})
	}
}

func TestReservationReadbackCancellationStopsContinuation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
		if calls.Add(1) == 2 {
			cancel()
			return nil, &JSONRPCError{Code: -32000, Message: "cancelled"}
		}
		raw, _ := json.Marshal(reservationRowsFixture(0, 20))
		return reservationResourceFixture(string(raw)), nil
	})
	rows, err := c.ListReservations(ctx, "/test/project", "", true)
	if !errors.Is(err, context.Canceled) || rows != nil || calls.Load() != 2 {
		t.Fatalf("cancellation lost: %+v %v calls=%d", rows, err, calls.Load())
	}
}

func TestReservationReadbackExplicitEmptyAndCompleteRows(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"[]", `[{"id":1,"project_id":17,"agent":"BlueLake","path_pattern":"a.go"}]`} {
		var calls atomic.Int32
		c := reservationReadbackClient(t, func(_ *http.Request, _ JSONRPCRequest) (any, *JSONRPCError) {
			calls.Add(1)
			return reservationResourceFixture(data), nil
		})
		rows, err := c.ListReservations(context.Background(), "/test/project", "", true)
		if err != nil || rows == nil || calls.Load() != 1 {
			t.Fatalf("explicit evidence rejected: %+v %v", rows, err)
		}
	}
	// A nil/cancelled context is rejected without a network request.
	c := reservationReadbackClient(t, func(_ *http.Request, _ JSONRPCRequest) (any, *JSONRPCError) {
		t.Error("unexpected request")
		return nil, nil
	})
	if _, err := c.ListReservations(nil, "/test/project", "", true); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	cancel()
	if _, err := c.ListReservations(ctx, "/test/project", "", true); err == nil {
		t.Fatal("cancelled context accepted")
	}
}

func reservationCompactGrant() map[string]any {
	return map[string]any{"id": 41, "path_pattern": "src/file41.go", "exclusive": true, "reason": "bead assignment: bd-work", "expires_ts": "2099-01-01T01:00:00Z"}
}

func TestReservePathsVerifiesRustGrantsByExactID(t *testing.T) {
	t.Parallel()
	for _, envelope := range []string{"raw", "structured", "text"} {
		t.Run(envelope, func(t *testing.T) {
			var mutations, reads atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				if req.Method == "tools/call" {
					mutations.Add(1)
					if params["name"] != "file_reservation_paths" {
						t.Errorf("unexpected mutation: %v", params["name"])
					}
					args, _ := params["arguments"].(map[string]interface{})
					if args["registration_token"] != "lease-token" {
						t.Error("reservation lost agent credentials")
					}
					data := map[string]any{"granted": []any{reservationCompactGrant()}, "conflicts": []any{}}
					switch envelope {
					case "structured":
						return map[string]any{"structuredContent": data}, nil
					case "text":
						raw, _ := json.Marshal(data)
						return map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}}, nil
					default:
						return data, nil
					}
				}
				reads.Add(1)
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					return reservationResourceFixture(`{"id":73,"slug":"test","human_key":"/test/project"}`), nil
				}
				q, _ := url.ParseQuery(strings.SplitN(uri, "?", 2)[1])
				offset, _ := strconv.Atoi(q.Get("offset"))
				count := 20
				if offset == 40 {
					count = 1
				}
				rows := reservationRowsFixture(offset, count)
				// Earlier rows deliberately have the requested path but the wrong
				// durable ID and owner. Path-only matching would accept a impostor.
				for _, row := range rows {
					row["path_pattern"] = "src/file41.go"
				}
				raw, _ := json.Marshal(rows)
				return reservationResourceFixture(string(raw)), nil
			})
			c.SetRegistrationToken("/test/project", "BlueLake", "lease-token")
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}, Exclusive: true, Reason: "bead assignment: bd-work"})
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || len(result.Granted) != 1 {
				t.Fatalf("no verified grant: %+v", result)
			}
			grant := result.Granted[0]
			if grant.ID != 41 || grant.ProjectID != 73 || grant.AgentName != "BlueLake" {
				t.Fatalf("wrong verified identity: %+v", grant)
			}
			if mutations.Load() != 1 || reads.Load() != 5 {
				t.Fatalf("unexpected side effects/reads: writes=%d reads=%d", mutations.Load(), reads.Load())
			}
		})
	}
}

func TestReservePathsReadbackRefusesMismatchesAndRetainsHandles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"wrong owner", "wrong project", "wrong identity resource", "missing ID", "wrong path", "unrequested path", "wrong reason", "wrong exclusivity", "expired row", "expired grant", "released row", "explicit zero project", "explicit null owner", "explicit wrong owner", "duplicate grant", "malformed grant"} {
		t.Run(mode, func(t *testing.T) {
			var mutations atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				if req.Method == "tools/call" {
					mutations.Add(1)
					if params["name"] != "file_reservation_paths" {
						t.Errorf("unexpected tool: %v", params["name"])
					}
					grant := reservationCompactGrant()
					switch mode {
					case "explicit zero project":
						grant["project_id"] = 0
					case "explicit null owner":
						grant["agent_name"] = nil
					case "explicit wrong owner":
						grant["agent_name"] = "OtherAgent"
					case "expired grant":
						grant["expires_ts"] = "2000-01-01T00:00:00Z"
					case "unrequested path":
						grant["path_pattern"] = "unexpected.go"
					case "malformed grant":
						grant["expires_ts"] = "not a timestamp"
					}
					grants := []any{grant}
					if mode == "duplicate grant" {
						grants = append(grants, grant)
					}
					return map[string]any{"granted": grants, "conflicts": []any{}}, nil
				}
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					key := "/test/project"
					if mode == "wrong identity resource" {
						key = "/another/project"
					}
					raw, _ := json.Marshal(map[string]any{"id": 73, "slug": "test", "human_key": key})
					return reservationResourceFixture(string(raw)), nil
				}
				row := reservationRowsFixture(40, 1)[0]
				switch mode {
				case "wrong owner":
					row["agent"] = "OtherAgent"
				case "wrong project":
					row["project_id"] = 99
				case "missing ID":
					row["id"] = 42
				case "wrong path":
					row["path_pattern"] = "different.go"
				case "unrequested path":
					row["path_pattern"] = "unexpected.go"
				case "wrong reason":
					row["reason"] = "bead assignment: unrelated"
				case "wrong exclusivity":
					row["exclusive"] = false
				case "expired row":
					row["expires_ts"] = "2000-01-01T00:00:00Z"
				case "released row":
					row["released_ts"] = "2026-01-01T00:00:00Z"
				}
				raw, _ := json.Marshal([]any{row})
				return reservationResourceFixture(string(raw)), nil
			})
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}, Exclusive: true, Reason: "bead assignment: bd-work"})
			if err == nil || result == nil || len(result.Granted) == 0 || result.Granted[0].ID != 41 {
				t.Fatalf("failure lost recovery handles or was accepted: %+v %v", result, err)
			}
			if result.Granted[0].ProjectID != 0 {
				t.Fatalf("failed readback changed original evidence: %+v", result.Granted)
			}
			if mutations.Load() != 1 {
				t.Fatalf("readback made %d mutations, want only the initial reservation", mutations.Load())
			}
		})
	}
}

func TestReservePathsPartialConflictPreservesVerifiedGrants(t *testing.T) {
	t.Parallel()
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%v", unavailable), func(t *testing.T) {
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				if req.Method == "tools/call" {
					return map[string]any{"granted": []any{reservationCompactGrant()}, "conflicts": []any{map[string]any{"path": "blocked.go", "holders": []any{map[string]any{"agent": "OtherAgent", "exclusive": true}}}}}, nil
				}
				if unavailable {
					return nil, &JSONRPCError{Code: -32000, Message: "project readback unavailable"}
				}
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					return reservationResourceFixture(`{"id":73,"slug":"test","human_key":"/test/project"}`), nil
				}
				raw, _ := json.Marshal(reservationRowsFixture(40, 1))
				return reservationResourceFixture(string(raw)), nil
			})
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go", "blocked.go"}, Exclusive: true, Reason: "bead assignment: bd-work"})
			if !errors.Is(err, ErrReservationConflict) || result == nil || len(result.Granted) != 1 || len(result.Conflicts) != 1 {
				t.Fatalf("lost partial conflict evidence: %+v %v", result, err)
			}
			if result.Conflicts[0].Holders[0] != "OtherAgent" {
				t.Fatalf("Rust holder shape not decoded: %+v", result.Conflicts)
			}
			wantProject := 73
			if unavailable {
				wantProject = 0
				if !strings.Contains(err.Error(), "project readback unavailable") {
					t.Fatal("lost readback cause")
				}
			}
			if result.Granted[0].ProjectID != wantProject {
				t.Fatalf("grant ownership=%d, want %d", result.Granted[0].ProjectID, wantProject)
			}
		})
	}
}

func TestReservePathsReadbackNeverExtendsGrantExpiry(t *testing.T) {
	t.Parallel()
	for _, expiry := range []string{"2098-01-01T00:00:00Z", "2100-01-01T00:00:00Z"} {
		c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
			params, _ := req.Params.(map[string]interface{})
			if req.Method == "tools/call" {
				return map[string]any{"granted": []any{reservationCompactGrant()}}, nil
			}
			uri, _ := params["uri"].(string)
			if strings.HasPrefix(uri, "resource://project/") {
				return reservationResourceFixture(`{"id":73,"slug":"test","human_key":"/test/project"}`), nil
			}
			row := reservationRowsFixture(40, 1)[0]
			row["expires_ts"] = expiry
			raw, _ := json.Marshal([]any{row})
			return reservationResourceFixture(string(raw)), nil
		})
		result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}, Exclusive: true})
		if err != nil {
			t.Fatal(err)
		}
		want := expiry
		if strings.HasPrefix(expiry, "2100") {
			want = "2099-01-01T01:00:00Z"
		}
		if result.Granted[0].ExpiresTS.Format(time.RFC3339) != want {
			t.Fatalf("unsafe expiry: %+v", result.Granted[0])
		}
	}
}

func TestReservePathsReadbackCancellationRetainsMutationReceipt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writes, reads atomic.Int32
	c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
		if req.Method == "tools/call" {
			writes.Add(1)
			return map[string]any{"granted": []any{reservationCompactGrant()}}, nil
		}
		reads.Add(1)
		cancel()
		return nil, &JSONRPCError{Code: -32000, Message: "cancelled during readback"}
	})
	result, err := c.ReservePaths(ctx, FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}})
	if !errors.Is(err, context.Canceled) || result == nil || len(result.Granted) != 1 || result.Granted[0].ID != 41 {
		t.Fatalf("cancellation lost receipt: %+v %v", result, err)
	}
	if writes.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("unexpected work after cancellation: %d %d", writes.Load(), reads.Load())
	}
}

func TestReservePathsRejectsExplicitInvalidMetadataWithoutRepair(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
		calls.Add(1)
		if req.Method == "resources/read" {
			params, _ := req.Params.(map[string]interface{})
			uri, _ := params["uri"].(string)
			if strings.HasPrefix(uri, "resource://project/") {
				return reservationResourceFixture(`{"id":73,"slug":"test","human_key":"/test/project"}`), nil
			}
			raw, _ := json.Marshal(reservationRowsFixture(40, 1))
			return reservationResourceFixture(string(raw)), nil
		}
		return ReservationResult{Granted: []FileReservation{{ID: 41, ProjectID: 0, AgentName: "BlueLake", PathPattern: "src/file41.go"}}}, nil
	})
	result, err := c.ReservePaths(context.Background(), FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}})
	if !errors.Is(err, ErrReservationUnverified) || calls.Load() < 3 || result == nil || len(result.Granted) != 1 || result.Granted[0].ProjectID != 0 {
		t.Fatalf("explicit invalid identity was laundered: %+v %v", result, err)
	}
}

// Explicit fields are assertions in a mutation receipt, not proof of current
// ownership. Exercise the public API so all dialects have the same contract.
func TestReservePathsExplicitGrantsRequireIndependentEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"valid", "foreign owner", "foreign project", "foreign project resource",
		"zero project", "null project", "empty owner", "null owner", "wrong receipt owner",
		"same path different ID", "different path", "different reason", "released row",
		"released receipt", "expired row", "expired receipt", "exclusive upgrade",
		"exclusive downgrade", "project unavailable", "listing unavailable", "cancelled",
		"shorter expiry", "longer expiry", "partial conflict", "unverified conflict",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			grant := reservationCompactGrant()
			grant["project_id"], grant["agent_name"] = 73, "BlueLake"
			row := reservationRowsFixture(40, 1)[0]
			row["project_id"] = 73
			row["created_ts"] = "2026-01-01T00:00:00Z"
			opts := FileReservationOptions{ProjectKey: "/test/project", AgentName: "BlueLake", Paths: []string{"src/file41.go"}, Exclusive: true, Reason: "bead assignment: bd-work"}
			conflicts := []any{}
			switch mode {
			case "foreign owner", "unverified conflict":
				row["agent"] = "OtherAgent"
			case "foreign project":
				row["project_id"] = 99
			case "zero project":
				grant["project_id"] = 0
			case "null project":
				grant["project_id"] = nil
			case "empty owner":
				grant["agent_name"] = ""
			case "null owner":
				grant["agent_name"] = nil
			case "wrong receipt owner":
				grant["agent_name"] = "OtherAgent"
			case "same path different ID":
				row["id"] = 42
			case "different path":
				row["path_pattern"] = "unrelated.go"
			case "different reason":
				row["reason"] = "different assignment"
			case "released row":
				row["released_ts"] = "2026-01-01T00:00:00Z"
			case "released receipt":
				grant["released_ts"] = "2026-01-01T00:00:00Z"
			case "expired row":
				row["expires_ts"] = "2000-01-01T00:00:00Z"
			case "expired receipt":
				grant["expires_ts"] = "2000-01-01T00:00:00Z"
			case "exclusive upgrade":
				opts.Exclusive = false
			case "exclusive downgrade":
				grant["exclusive"], row["exclusive"] = false, false
			case "shorter expiry":
				row["expires_ts"] = "2098-01-01T00:00:00Z"
			case "longer expiry":
				row["expires_ts"] = "2100-01-01T00:00:00Z"
			}
			if mode == "partial conflict" || mode == "unverified conflict" {
				opts.Paths = append(opts.Paths, "blocked.go")
				conflicts = append(conflicts, map[string]any{"path": "blocked.go", "holders": []string{"OtherAgent"}})
			}
			reply := map[string]any{"granted": []any{grant}, "conflicts": conflicts}
			raw, _ := json.Marshal(reply)
			original, err := decodeReservationReply(raw)
			if err != nil {
				t.Fatal(err)
			}
			var writes, reads atomic.Int32
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				params, _ := req.Params.(map[string]interface{})
				if req.Method == "tools/call" {
					writes.Add(1)
					if params["name"] != "file_reservation_paths" {
						t.Errorf("verification attempted mutation %v", params["name"])
					}
					return reply, nil
				}
				reads.Add(1)
				uri, _ := params["uri"].(string)
				if mode == "cancelled" {
					cancel()
					return nil, &JSONRPCError{Code: -32000, Message: "cancelled"}
				}
				if strings.HasPrefix(uri, "resource://project/") {
					if mode == "project unavailable" {
						return nil, &JSONRPCError{Code: -32000, Message: "identity unavailable"}
					}
					key := "/test/project"
					if mode == "foreign project resource" {
						key = "/another/project"
					}
					data, _ := json.Marshal(map[string]any{"id": 73, "slug": "test", "human_key": key})
					return reservationResourceFixture(string(data)), nil
				}
				if mode == "listing unavailable" {
					return nil, &JSONRPCError{Code: -32000, Message: "listing unavailable"}
				}
				data, _ := json.Marshal([]any{row})
				return reservationResourceFixture(string(data)), nil
			})
			result, err := c.ReservePaths(ctx, opts)
			verified := mode == "valid" || mode == "shorter expiry" || mode == "longer expiry" || mode == "partial conflict"
			if verified {
				if mode == "partial conflict" {
					if !errors.Is(err, ErrReservationConflict) || errors.Is(err, ErrReservationUnverified) {
						t.Fatalf("valid partial evidence rejected: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if result == nil || len(result.Granted) != 1 || result.Granted[0].CreatedTS.IsZero() {
					t.Fatalf("missing independent readback: %+v", result)
				}
				wantExpiry := "2099-01-01T01:00:00Z"
				if mode == "shorter expiry" {
					wantExpiry = "2098-01-01T00:00:00Z"
				}
				if result.Granted[0].ExpiresTS.Format(time.RFC3339) != wantExpiry {
					t.Fatalf("validity overstated: %+v", result.Granted)
				}
			} else if !errors.Is(err, ErrReservationUnverified) || !reflect.DeepEqual(original, result) {
				t.Fatalf("unverified explicit receipt accepted or rewritten: %+v %v", result, err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation cause: %v", err)
			}
			if mode == "unverified conflict" && !errors.Is(err, ErrReservationConflict) {
				t.Fatalf("lost conflict cause: %v", err)
			}
			if writes.Load() != 1 || reads.Load() == 0 {
				t.Fatalf("expected one mutation and independent reads: writes=%d reads=%d", writes.Load(), reads.Load())
			}
		})
	}
}

func TestReservePathsLateVerificationFailureLeavesEntireReceiptUntouched(t *testing.T) {
	t.Parallel()
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed_dialect=%v", mixed), func(t *testing.T) {
			first := reservationCompactGrant()
			first["project_id"], first["agent_name"] = 73, "BlueLake"
			second := reservationCompactGrant()
			second["id"], second["path_pattern"] = 42, "src/file42.go"
			if !mixed {
				second["project_id"], second["agent_name"] = 73, "BlueLake"
			}
			reply := map[string]any{"granted": []any{first, second}}
			raw, _ := json.Marshal(reply)
			original, err := decodeReservationReply(raw)
			if err != nil {
				t.Fatal(err)
			}
			c := reservationReadbackClient(t, func(_ *http.Request, req JSONRPCRequest) (any, *JSONRPCError) {
				if req.Method == "tools/call" {
					return reply, nil
				}
				params, _ := req.Params.(map[string]interface{})
				uri, _ := params["uri"].(string)
				if strings.HasPrefix(uri, "resource://project/") {
					return reservationResourceFixture(`{"id":73,"slug":"test","human_key":"/test/project"}`), nil
				}
				rows := reservationRowsFixture(40, 2)
				rows[0]["expires_ts"] = "2098-01-01T00:00:00Z"
				// Row 42 belongs to OtherAgent. Row 41 must not be rewritten
				// to its verified (shorter) expiry before row 42 is rejected.
				data, _ := json.Marshal(rows)
				return reservationResourceFixture(string(data)), nil
			})
			result, err := c.ReservePaths(context.Background(), FileReservationOptions{
				ProjectKey: "/test/project", AgentName: "BlueLake", Exclusive: true,
				Paths: []string{"src/file41.go", "src/file42.go"}, Reason: "bead assignment: bd-work",
			})
			if !errors.Is(err, ErrReservationUnverified) || !reflect.DeepEqual(original, result) {
				t.Fatalf("partially published verification or accepted foreign lease: %+v %v", result, err)
			}
		})
	}
}
