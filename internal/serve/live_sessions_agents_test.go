package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func decodeSessionsList(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]interface{}, int) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Sessions []map[string]interface{} `json:"sessions"`
		Count    int                      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Sessions, resp.Count
}

// Sessions started by `ntm spawn` are never written to the runtime store, so
// the list endpoint must also report what tmux is running; otherwise the web
// dashboard shows no sessions (and therefore no agents) while agents are live.
func TestHandleSessionsV1_MergesLiveTmuxSessions(t *testing.T) {
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "stored-only")
	createTestSessionForServe(t, store, "both")
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return []tmux.Session{
			{Name: "zeta-live", Windows: 1},
			{Name: "both", Windows: 2},
			{Name: "alpha-live", Windows: 3, Attached: true},
		}, nil
	}

	rec := httptest.NewRecorder()
	srv.handleSessionsV1(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	sessions, count := decodeSessionsList(t, rec)

	var names []string
	for _, s := range sessions {
		names = append(names, s["name"].(string))
	}
	// Stored rows first (store order), then live-only sessions sorted by name;
	// a session present in both appears once, as its stored row.
	storedNames := names[:2]
	if !(strings.Contains(strings.Join(storedNames, ","), "stored-only") && strings.Contains(strings.Join(storedNames, ","), "both")) {
		t.Fatalf("stored sessions not listed first: %v", names)
	}
	if got := strings.Join(names[2:], ","); got != "alpha-live,zeta-live" {
		t.Fatalf("live-only sessions = %q, want alpha-live,zeta-live (all: %v)", got, names)
	}
	if count != 4 || len(sessions) != 4 {
		t.Fatalf("count = %d, len = %d, want 4", count, len(sessions))
	}
	alpha := sessions[2]
	if alpha["id"] != "alpha-live" || alpha["status"] != string(state.SessionActive) ||
		alpha["attached"] != true || alpha["windows"] != float64(3) || alpha["source"] != "tmux" {
		t.Fatalf("live session record = %#v", alpha)
	}
	for _, s := range sessions[:2] {
		if _, isLive := s["source"]; isLive {
			t.Fatalf("stored session %v was replaced by its live record", s["name"])
		}
	}
}

func TestHandleSessionsV1_TmuxFailureKeepsStoredSessions(t *testing.T) {
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "stored")
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return nil, errors.New("tmux exploded")
	}

	rec := httptest.NewRecorder()
	srv.handleSessionsV1(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	sessions, count := decodeSessionsList(t, rec)
	if count != 1 || sessions[0]["name"] != "stored" {
		t.Fatalf("sessions = %#v (count %d), want the stored row only", sessions, count)
	}
}

func TestHandleSessionV1_FallsBackToLiveTmuxSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return []tmux.Session{{Name: "live-one", Windows: 1}}, nil
	}

	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+id, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		srv.handleSessionV1(rec, req)
		return rec
	}

	rec := get("live-one")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Session map[string]interface{} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Session["name"] != "live-one" || resp.Session["source"] != "tmux" {
		t.Fatalf("session = %#v", resp.Session)
	}

	if rec := get("not-running"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", rec.Code)
	}
}

// installFakeAgentPanesTmux installs a tmux stand-in whose list-panes reports
// the given pane lines for every session.
func installFakeAgentPanesTmux(t *testing.T, paneLines []string) {
	t.Helper()
	dir := t.TempDir()
	payload := filepath.Join(dir, "panes.txt")
	if err := os.WriteFile(payload, []byte(strings.Join(paneLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write pane fixture: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  list-panes) cat %q ;;\n  *) : ;;\nesac\nexit 0\n", payload)
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
}

// paneLine renders one list-panes row in the field order GetPanesContext asks
// for: id, index, title, command, width, height, active, pid, window, agent
// type option, acfs service, ntm service, dead.
func paneLine(id string, index int, title, agentType string, pid int, dead bool) string {
	deadFlag := "0"
	if dead {
		deadFlag = "1"
	}
	fields := []string{id, fmt.Sprint(index), title, "node", "80", "24", "0", fmt.Sprint(pid), "0", agentType, "", "", deadFlag}
	return strings.Join(fields, tmux.FieldSeparator)
}

// The web Agents page renders id/session_id/name/type/tmux_pane_id; the live
// endpoint used to return only pane_* fields, so every row was nameless and
// typeless and React keyed all rows on an undefined id.
func TestHandleListAgentsV1_ReturnsAgentRecordFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	projectDir := t.TempDir()
	srv.projectDir = projectDir

	const session = "webagents"
	installFakeAgentPanesTmux(t, []string{
		paneLine("%0", 0, "shell", "user", 100, false),
		paneLine("%1", 1, session+"__cc_1", "cc", 111, false),
		paneLine("%2", 2, session+"__cod_1", "cod", 222, false),
		paneLine("%3", 3, session+"__cc_2", "cc", 333, true),
	})

	registry := agentmail.NewSessionAgentRegistry(session, projectDir)
	registry.AddAgent(session+"__cc_1", "%1", "GreenLake")
	registry.SetPanePID("%1", 111)
	// A mapping recorded for a different process: tmux reused the pane id.
	registry.AddAgent(session+"__cod_1", "%2", "BlueRiver")
	registry.SetPanePID("%2", 999)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+session+"/agents", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", session)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleListAgentsV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Agents []map[string]interface{} `json:"agents"`
		Count  int                      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 3 || len(resp.Agents) != 3 {
		t.Fatalf("agents = %d (count %d), want 3 (user pane excluded): %#v", len(resp.Agents), resp.Count, resp.Agents)
	}

	byID := map[string]map[string]interface{}{}
	for _, a := range resp.Agents {
		for _, key := range []string{"id", "session_id", "name", "type", "tmux_pane_id", "pane_id", "pane_index", "agent_type", "title"} {
			if _, ok := a[key]; !ok {
				t.Fatalf("agent %#v lacks %q", a, key)
			}
		}
		if a["session_id"] != session || a["id"] != a["tmux_pane_id"] || a["type"] != a["agent_type"] {
			t.Fatalf("inconsistent agent record: %#v", a)
		}
		byID[a["id"].(string)] = a
	}

	if a := byID["%1"]; a["name"] != "GreenLake" || a["agent_mail_name"] != "GreenLake" || a["type"] != "cc" {
		t.Fatalf("registered agent = %#v, want name GreenLake", a)
	}
	if a := byID["%2"]; a["name"] != session+"__cod_1" || a["agent_mail_name"] != nil {
		t.Fatalf("stale registry mapping was trusted: %#v", a)
	}
	if a := byID["%3"]; a["status"] != "dead" || a["dead"] != true {
		t.Fatalf("dead pane = %#v, want status dead", a)
	}
	if _, hasStatus := byID["%1"]["status"]; hasStatus {
		t.Fatalf("live pane must not claim a status it cannot observe: %#v", byID["%1"])
	}
}

// br hierarchical issues are "<parent>.<n>" (bd-1aae9.1, bd-1aae9.1.2) and
// prefixes may contain hyphens; both used to be rejected with 400.
func TestBeadIDPattern(t *testing.T) {
	valid := []string{
		"bd-2euwg", "ntm-y9cd", "beads_rust-orko", "bd-1aae9.1", "bd-1aae9.1.2",
		"bc-fwh.14", "my-proj-a1b2", "coding-agent-search-x9.3",
	}
	for _, id := range valid {
		if !beadIDPattern.MatchString(id) {
			t.Errorf("valid bead ID %q rejected", id)
		}
	}
	invalid := []string{
		"", "bd", "-bd-1", "--db=/etc/shadow", "--file", "bd-", "bd-1.", "bd-1..2",
		"bd-.1", "bd-1/2", "bd-1 --json", "bd-1=x", "1bd-2", "bd--1", "bd-1.-2", "../bd-1",
	}
	for _, id := range invalid {
		if beadIDPattern.MatchString(id) {
			t.Errorf("invalid bead ID %q accepted", id)
		}
	}
}
