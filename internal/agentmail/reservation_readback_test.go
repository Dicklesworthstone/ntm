package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
