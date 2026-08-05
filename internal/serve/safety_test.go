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
// had never been approved, though it had been approved and performed.
func TestApprovalResolvedRecordSurvivesLateRetry(t *testing.T) {
	s, _ := setupTestServer(t)

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

	// Push the record past its TTL, as wall-clock would.
	approvalsLock.Lock()
	approvals[id].ExpiresAt = time.Now().Add(-time.Hour)
	approvalsLock.Unlock()

	// A late retry must be refused as already-resolved, and must NOT rewrite
	// the record.
	if got := approve(); got.Code != http.StatusConflict {
		t.Fatalf("late retry: got %d, want 409", got.Code)
	}

	approvalsLock.Lock()
	status := approvals[id].Status
	approvedBy := approvals[id].ApprovedBy
	approvalsLock.Unlock()

	if status != "approved" {
		t.Fatalf("status = %q after a late retry, want it to stay \"approved\" (approved_by=%q)", status, approvedBy)
	}
}
