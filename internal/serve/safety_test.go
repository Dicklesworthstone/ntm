package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	approvalpkg "github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestSafetyEscapeYAMLSingleQuote(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "hello world", "hello world"},
		{"single quote", "it's fine", "it''s fine"},
		{"multiple quotes", "it's Bob's", "it''s Bob''s"},
		{"empty string", "", ""},
		{"only quote", "'", "''"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safetyEscapeYAMLSingleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLSingleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSafetyEscapeYAMLDoubleQuote(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "hello world", "hello world"},
		{"backslash", `path\to\file`, `path\\to\\file`},
		{"double quote", `say "hello"`, `say \"hello\"`},
		{"newline", "line1\nline2", `line1\nline2`},
		{"carriage return", "line1\rline2", `line1\rline2`},
		{"tab", "col1\tcol2", `col1\tcol2`},
		{"empty string", "", ""},
		{"all specials", "\"\\\n\r\t", `\"\\\n\r\t`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safetyEscapeYAMLDoubleQuote(tc.input)
			if got != tc.want {
				t.Errorf("safetyEscapeYAMLDoubleQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestClaudeHookScriptReadsCurrentStdinPayload(t *testing.T) {
	for _, want := range []string{
		"HOOK_INPUT=\"$(cat)\"",
		"'.tool_name // empty'",
		"'.tool_input.command // empty'",
		"exit 2",
	} {
		if !strings.Contains(claudeHookScript, want) {
			t.Fatalf("claude hook script missing %q", want)
		}
	}
	if strings.Contains(claudeHookScript, "exit 1\nfi\n\nexit 0") {
		t.Fatal("claude hook script still uses non-blocking exit 1 for denied commands")
	}
}

// ExpiresAt is never cleared when an approval resolves, so a resolved record
// stays past its TTL forever. Checking expiry before terminal state therefore
// let a retry hours later rewrite an APPROVED record to "expired" while
// leaving approved_by set — the audit trail then claimed a dangerous action
// had never been approved, though it had been approved and performed. The
// durable engine preserves this ordering (terminal-state check before expiry).
func TestApprovalResolvedRecordSurvivesLateRetry(t *testing.T) {
	s, store := setupTestServer(t)

	body := strings.NewReader(`{"action":"git push --force","resource":"main","reason":"deploy"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/request", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("request approval: got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no approval id: %s", rec.Body.String())
	}

	approve := func() *httptest.ResponseRecorder {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		r := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+id+"/approve", nil)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		s.handleApprovalApproveV1(w, r)
		return w
	}

	if got := approve(); got.Code != http.StatusOK {
		t.Fatalf("first approve: got %d: %s", got.Code, got.Body.String())
	}

	// Push the durable record past its TTL, as wall-clock would.
	rec2, err := store.GetApproval(id)
	if err != nil || rec2 == nil {
		t.Fatalf("reload approval: %v (record=%v)", err, rec2)
	}
	rec2.ExpiresAt = time.Now().Add(-time.Hour)
	if err := store.UpdateApproval(rec2); err != nil {
		t.Fatalf("age approval: %v", err)
	}

	// A late retry must be refused as already-resolved, and must NOT rewrite
	// the record.
	if got := approve(); got.Code != http.StatusConflict {
		t.Fatalf("late retry: got %d, want 409", got.Code)
	}

	final, err := store.GetApproval(id)
	if err != nil || final == nil {
		t.Fatalf("reload approval: %v (record=%v)", err, final)
	}
	if final.Status != state.ApprovalApproved {
		t.Fatalf("status = %q after a late retry, want it to stay \"approved\" (approved_by=%q)", final.Status, final.ApprovedBy)
	}
}

func TestApprovalsHistoryTransitionsExpiredPendingApproval(t *testing.T) {
	s, store := setupTestServer(t)

	const approvalID = "apr-history-expired-regression"
	if err := store.CreateApproval(&state.Approval{
		ID:        approvalID,
		Action:    "dangerous-operation",
		Status:    state.ApprovalPending,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/history", nil)
	rec := httptest.NewRecorder()
	s.handleApprovalsHistoryV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: got %d: %s", rec.Code, rec.Body.String())
	}

	var response ApprovalsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	for _, approval := range response.Approvals {
		if approval.ID == approvalID {
			if approval.Status != "expired" {
				t.Fatalf("history status = %q, want expired", approval.Status)
			}
			return
		}
	}
	t.Fatalf("expired approval %q missing from history: %+v", approvalID, response.Approvals)
}

func TestApprovalDecisionResponseSnapshotsResolvedStatus(t *testing.T) {
	s, store := setupTestServer(t)

	for _, tc := range []struct {
		name      string
		id        string
		handler   func(http.ResponseWriter, *http.Request)
		wantState string
	}{
		{
			name: "approve", id: "apr-snapshot-approve",
			handler: s.handleApprovalApproveV1, wantState: "approved",
		},
		{
			name: "deny", id: "apr-snapshot-deny",
			handler: s.handleApprovalDenyV1, wantState: "denied",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.CreateApproval(&state.Approval{
				ID:        tc.id,
				Action:    "snapshot-action",
				Status:    state.ApprovalPending,
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatalf("seed approval: %v", err)
			}

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tc.id)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/approvals/"+tc.id+"/"+tc.name, nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var response ApprovalDecisionResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.ID != tc.id || response.Status != tc.wantState || response.Decision != tc.wantState {
				t.Fatalf("response = %+v, want resolved %q snapshot", response, tc.wantState)
			}
		})
	}
}

// bd-d2uxt: serve's approval surface is a view/controller over the durable
// approval engine. An approval created over HTTP must be visible to the
// engine (the same store `ntm approve list` reads), and an approval created
// by the engine must be visible over HTTP.
func TestApprovalsUnifiedWithDurableEngine(t *testing.T) {
	s, store := setupTestServer(t)
	eng := s.approvalEngine()
	if eng == nil {
		t.Fatal("approvalEngine() = nil with a configured state store")
	}

	// Engine-created approval shows up in the HTTP list.
	created, err := eng.Request(context.Background(), approvalpkg.RequestParams{
		Action:      "dangerous-op",
		Resource:    "res-1",
		Reason:      "test",
		RequestedBy: "cli-agent",
	})
	if err != nil {
		t.Fatalf("engine request: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals?status=pending", nil)
	rec := httptest.NewRecorder()
	s.handleApprovalsListV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d: %s", rec.Code, rec.Body.String())
	}
	var listResp ApprovalsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, a := range listResp.Approvals {
		if a.ID == created.ID && a.Requestor == "cli-agent" && a.Status == "pending" {
			found = true
		}
	}
	if !found {
		t.Fatalf("engine-created approval %s missing from HTTP list: %+v", created.ID, listResp.Approvals)
	}

	// HTTP-created approval lands in the durable store the engine reads.
	body := strings.NewReader(`{"action":"http-op","resource":"res-2","reason":"via http"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/safety/approvals/request", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withRoleContext(req.Context(), &RoleContext{Role: RoleOperator, UserID: "web-user"}))
	rec = httptest.NewRecorder()
	s.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("request: got %d: %s", rec.Code, rec.Body.String())
	}
	var createResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	httpID, _ := createResp["id"].(string)
	durable, err := store.GetApproval(httpID)
	if err != nil || durable == nil {
		t.Fatalf("HTTP-created approval %q not in durable store: %v (record=%v)", httpID, err, durable)
	}
	if durable.RequestedBy != "web-user" {
		t.Fatalf("requested_by = %q, want RBAC principal \"web-user\"", durable.RequestedBy)
	}
}

// bd-d2uxt: the engine's SLB two-person rule must surface through HTTP — a
// second principal can approve, the requesting principal cannot.
func TestApprovalSLBRuleSurfacesThroughHTTP(t *testing.T) {
	s, store := setupTestServer(t)

	approveAs := func(id, user string) *httptest.ResponseRecorder {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/approvals/"+id+"/approve", nil)
		ctx := withRoleContext(req.Context(), &RoleContext{Role: RoleAdmin, UserID: user})
		req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		s.handleApprovalApproveV1(rec, req)
		return rec
	}

	seed := func(id string) {
		t.Helper()
		if err := store.CreateApproval(&state.Approval{
			ID:          id,
			Action:      "force_release lock-1",
			RequestedBy: "requestor-a",
			RequiresSLB: true,
			Status:      state.ApprovalPending,
			CreatedAt:   time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("seed approval: %v", err)
		}
	}

	// Self-approval: the engine's SLB rule rejects it with 403.
	seed("apr-slb-http-self")
	if rec := approveAs("apr-slb-http-self", "requestor-a"); rec.Code != http.StatusForbidden {
		t.Fatalf("self-approve: got %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// A second principal approves successfully; the durable record carries
	// the approver identity.
	seed("apr-slb-http-second")
	if rec := approveAs("apr-slb-http-second", "approver-b"); rec.Code != http.StatusOK {
		t.Fatalf("second-party approve: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	a, err := store.GetApproval("apr-slb-http-second")
	if err != nil || a == nil {
		t.Fatalf("reload approval: %v (record=%v)", err, a)
	}
	if a.Status != state.ApprovalApproved || a.ApprovedBy != "approver-b" {
		t.Fatalf("durable record = status %q approved_by %q, want approved by approver-b", a.Status, a.ApprovedBy)
	}
}

// Without a state store the approval endpoints fail closed (503) instead of
// silently keeping decisions in process-local memory.
func TestApprovalEndpointsFailClosedWithoutStateStore(t *testing.T) {
	s := New(Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals", nil)
	rec := httptest.NewRecorder()
	s.handleApprovalsListV1(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list without store: got %d, want 503: %s", rec.Code, rec.Body.String())
	}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-x")
	req = httptest.NewRequest(http.MethodPost, "/api/v1/safety/approvals/apr-x/approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	s.handleApprovalApproveV1(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("approve without store: got %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
