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
	"time"

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// installFakeListPanesTmux installs a tmux stand-in that runs listPanes (a
// shell command) for list-panes and succeeds silently for any other
// subcommand. A fresh default client keeps the fixture's failures out of the
// circuit breaker other tests share.
func installFakeListPanesTmux(t *testing.T, listPanes string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tmux")
	script := "#!/bin/sh\ncase \"$1\" in\n  list-panes) " + listPanes + " ;;\n  *) : ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
}

// list-panes answers for installFakeListPanesTmux: tmux with no server
// running, tmux without the requested session ($4 is the -t target), and a
// wedged tmux server that never answers.
const (
	fakeTmuxNoServer  = `echo "no server running on /tmp/tmux-0/default" >&2; exit 1`
	fakeTmuxNoSession = `echo "can't find session: $4" >&2; exit 1`
	fakeTmuxHung      = `exec sleep 30`
)

// installFakeAgentPanesTmux installs a tmux stand-in whose list-panes reports
// the given pane lines for every session.
func installFakeAgentPanesTmux(t *testing.T, paneLines []string) {
	t.Helper()
	payload := filepath.Join(t.TempDir(), "panes.txt")
	if err := os.WriteFile(payload, []byte(strings.Join(paneLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write pane fixture: %v", err)
	}
	installFakeListPanesTmux(t, fmt.Sprintf("cat %q", payload))
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

// isolateAgentRegistry points the Agent Mail session registry lookup at fresh
// directories, so no developer registry renames a fixture pane, and returns
// the server's new project directory.
func isolateAgentRegistry(t *testing.T, srv *Server) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	srv.projectDir = t.TempDir()
	return srv.projectDir
}

// seedProjectedAgents records runtime projection rows for a session the way
// the serve refresh loop (robot.RefreshNormalizedProjection) does: a session
// row, then one row per agent pane, fresh for a minute unless the row sets
// its own freshness.
func seedProjectedAgents(t *testing.T, store *state.Store, session string, rows ...state.RuntimeAgent) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         session,
		AgentCount:   len(rows),
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now,
		StaleAfter:   now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("upsert runtime session %q: %v", session, err)
	}
	for _, row := range rows {
		row.ID = session + ":" + row.Pane
		row.SessionName = session
		if row.TypeMethod == "" {
			row.TypeMethod = "title"
		}
		if row.HealthStatus == "" {
			row.HealthStatus = state.HealthStatusHealthy
		}
		if row.CollectedAt.IsZero() {
			row.CollectedAt = now
		}
		if row.StaleAfter.IsZero() {
			row.StaleAfter = now.Add(time.Minute)
		}
		if err := store.UpsertRuntimeAgent(&row); err != nil {
			t.Fatalf("upsert runtime agent %s: %v", row.ID, err)
		}
	}
}

// getThroughRouter issues GET path against the served router (the mux `ntm
// serve` mounts, middleware and route table included) and decodes the body.
func getThroughRouter(t *testing.T, srv *Server, path string) (int, map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: decode %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

// agentsByPane checks an agents response succeeded with a count matching its
// list, and indexes the agents by pane id.
func agentsByPane(t *testing.T, path string, code int, body map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	if code != http.StatusOK || body["success"] != true {
		t.Fatalf("GET %s = %d %v, want 200 success", path, code, body)
	}
	list, ok := body["agents"].([]interface{})
	if !ok {
		t.Fatalf("GET %s: agents = %#v, want an array", path, body["agents"])
	}
	if body["count"] != float64(len(list)) {
		t.Fatalf("GET %s: count = %v, want %d", path, body["count"], len(list))
	}
	byPane := make(map[string]map[string]interface{}, len(list))
	for _, raw := range list {
		agent := raw.(map[string]interface{})
		byPane[agent["id"].(string)] = agent
	}
	return byPane
}

// The web Agents page lists GET /api/v1/sessions/{name}/agents for every
// session. The agents endpoints used to read the legacy `agents` table, which
// nothing in production writes, so they answered "no agents" while agents
// ran. Both now report the live tmux agent panes, each with the state the
// runtime projection last observed for it.
func TestSessionAgentsEndpoints_ReportLiveAgentsWithProjectedState(t *testing.T) {
	srv, store := setupTestServer(t)
	projectDir := isolateAgentRegistry(t, srv)

	const session = "liveproj"
	installFakeAgentPanesTmux(t, []string{
		paneLine("%0", 0, "shell", "user", 100, false),
		paneLine("%1", 1, session+"__cc_1", "cc", 111, false),
		paneLine("%2", 2, session+"__cod_1", "cod", 222, false),
		paneLine("%3", 3, session+"__cc_2", "cc", 333, true),
		// A pane ntm tagged as the cm service: not an agent.
		strings.Join([]string{"%4", "4", session + "__cc_3", "cm", "80", "24", "0", "444", "0", "", "", "cm", "0"}, tmux.FieldSeparator),
	})
	registry := agentmail.NewSessionAgentRegistry(session, projectDir)
	registry.AddAgent(session+"__cc_1", "%1", "GreenLake")
	registry.SetPanePID("%1", 111)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	now := time.Now().UTC()
	lastOutput := now.Add(-90 * time.Second).Truncate(time.Second)
	seedProjectedAgents(t, store, session,
		state.RuntimeAgent{Pane: "%1", AgentType: "claude", State: state.AgentStateBusy, StateReason: "output changing",
			LastOutputAt: &lastOutput, CurrentBead: "bd-1aae9.1", PendingMail: 2},
		// Expired: the refresh loop has not observed this pane recently.
		state.RuntimeAgent{Pane: "%2", AgentType: "codex", State: state.AgentStateIdle,
			CollectedAt: now.Add(-2 * time.Minute), StaleAfter: now.Add(-time.Minute)},
		state.RuntimeAgent{Pane: "%3", AgentType: "claude", State: state.AgentStateIdle},
		// Gone from tmux since the last refresh.
		state.RuntimeAgent{Pane: "%9", AgentType: "claude", State: state.AgentStateBusy},
	)

	v1Path := "/api/v1/sessions/" + session + "/agents"
	code, body := getThroughRouter(t, srv, v1Path)
	agents := agentsByPane(t, v1Path, code, body)
	if len(agents) != 3 || agents["%1"] == nil || agents["%2"] == nil || agents["%3"] == nil {
		t.Fatalf("agents = %v, want panes %%1 %%2 %%3 (user, service and vanished panes excluded)", agents)
	}

	busy := agents["%1"]
	for key, want := range map[string]interface{}{
		"session_id":      session,
		"name":            "GreenLake",
		"agent_mail_name": "GreenLake",
		"type":            "cc",
		"tmux_pane_id":    "%1",
		"status":          "busy",
		"state_reason":    "output changing",
		"last_seen":       lastOutput.Format(time.RFC3339),
		"current_task_id": "bd-1aae9.1",
		"pending_mail":    float64(2),
		"health_status":   "healthy",
	} {
		if busy[key] != want {
			t.Errorf("busy agent %s = %#v, want %#v (agent %v)", key, busy[key], want, busy)
		}
	}
	if _, ok := busy["projected_at"].(string); !ok {
		t.Errorf("busy agent lacks projected_at: %v", busy)
	}
	for _, key := range []string{"status", "last_seen", "health_status", "projected_at"} {
		if _, ok := agents["%2"][key]; ok {
			t.Errorf("agent %%2 reports %s from an expired projection row: %v", key, agents["%2"])
		}
	}
	if agents["%2"]["type"] != "cod" || agents["%2"]["name"] != session+"__cod_1" {
		t.Errorf("unprojected live agent = %v", agents["%2"])
	}
	if agents["%3"]["status"] != "dead" {
		t.Errorf("dead pane status = %v, want dead over the projected idle", agents["%3"]["status"])
	}

	legacyPath := "/api/sessions/" + session + "/agents"
	code, body = getThroughRouter(t, srv, legacyPath)
	legacy := agentsByPane(t, legacyPath, code, body)
	if len(legacy) != len(agents) {
		t.Fatalf("legacy agents = %v, want the v1 roster %v", legacy, agents)
	}
	for id, agent := range agents {
		if legacy[id]["status"] != agent["status"] || legacy[id]["name"] != agent["name"] {
			t.Errorf("legacy agent %s = %v, want %v", id, legacy[id], agent)
		}
	}

	t.Run("no state store", func(t *testing.T) {
		srv.stateStore = nil
		code, body := getThroughRouter(t, srv, v1Path)
		agents := agentsByPane(t, v1Path, code, body)
		if len(agents) != 3 || agents["%1"]["name"] != "GreenLake" {
			t.Fatalf("agents without a store = %v, want the 3 live agent panes", agents)
		}
		if _, ok := agents["%1"]["status"]; ok {
			t.Fatalf("agent without a projection claims a status: %v", agents["%1"])
		}
	})
}

// A wedged tmux server must neither hang the poll nor blank the roster: the
// pane listing is bounded, and fresh projection rows stand in for it.
func TestSessionAgentsEndpoints_FallBackToProjectionWhenTmuxHangs(t *testing.T) {
	srv, store := setupTestServer(t)
	isolateAgentRegistry(t, srv)
	srv.liveSessionsTimeout = 35 * time.Millisecond
	installFakeListPanesTmux(t, fakeTmuxHung)

	const session = "wedged"
	seedProjectedAgents(t, store, session,
		state.RuntimeAgent{Pane: "%1", AgentType: "claude", Variant: "opus", AgentMailName: "BlueRiver", State: state.AgentStateBusy},
		state.RuntimeAgent{Pane: "%2", AgentType: "codex", State: state.AgentStateIdle},
		state.RuntimeAgent{Pane: "%0", AgentType: "user", State: state.AgentStateIdle},
	)

	for _, path := range []string{"/api/v1/sessions/" + session + "/agents", "/api/sessions/" + session + "/agents"} {
		start := time.Now()
		code, body := getThroughRouter(t, srv, path)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("GET %s took %v on a hung tmux", path, elapsed)
		}
		agents := agentsByPane(t, path, code, body)
		if len(agents) != 2 {
			t.Fatalf("GET %s agents = %v, want the 2 projected agents (user pane excluded)", path, agents)
		}
		if a := agents["%1"]; a["type"] != "cc" || a["name"] != "BlueRiver" || a["agent_mail_name"] != "BlueRiver" ||
			a["status"] != "busy" || a["variant"] != "opus" || a["tmux_pane_id"] != "%1" {
			t.Errorf("GET %s projected agent %%1 = %v", path, a)
		}
		if a := agents["%2"]; a["type"] != "cod" || a["name"] != "%2" || a["status"] != "idle" {
			t.Errorf("GET %s projected agent %%2 = %v", path, a)
		}
		warnings, _ := body["warnings"].([]interface{})
		if len(warnings) != 1 || !strings.Contains(fmt.Sprint(warnings[0]), "runtime projection") {
			t.Errorf("GET %s warnings = %v, want one naming the projection fallback", path, body["warnings"])
		}
	}

	// With no projection either, the endpoint reports the timeout instead of
	// claiming the session has no agents.
	code, body := getThroughRouter(t, srv, "/api/v1/sessions/unprojected/agents")
	if code != http.StatusGatewayTimeout || body["error_code"] != ErrCodeTimeout {
		t.Fatalf("unprojected session on a hung tmux = %d %v, want 504 %s", code, body, ErrCodeTimeout)
	}
}

// tmux saying the session does not exist is definitive: it has no agents,
// whatever an unexpired projection row still says. A session the store
// records is reported empty; any other session is not found.
func TestSessionAgentsEndpoints_SessionNotRunning(t *testing.T) {
	srv, store := setupTestServer(t)
	isolateAgentRegistry(t, srv)
	installFakeListPanesTmux(t, fakeTmuxNoSession)

	createTestSessionForServe(t, store, "stopped")
	seedProjectedAgents(t, store, "stopped", state.RuntimeAgent{Pane: "%1", AgentType: "claude", State: state.AgentStateBusy})

	for _, path := range []string{"/api/v1/sessions/stopped/agents", "/api/sessions/stopped/agents"} {
		code, body := getThroughRouter(t, srv, path)
		if agents := agentsByPane(t, path, code, body); len(agents) != 0 {
			t.Fatalf("GET %s agents = %v, want none for a session tmux is not running", path, agents)
		}
	}

	code, body := getThroughRouter(t, srv, "/api/v1/sessions/never-started/agents")
	if code != http.StatusNotFound || body["error_code"] != ErrCodeNotFound {
		t.Fatalf("unknown session = %d %v, want 404 %s", code, body, ErrCodeNotFound)
	}
	code, body = getThroughRouter(t, srv, "/api/sessions/never-started/agents")
	if code != http.StatusNotFound || body["success"] != false {
		t.Fatalf("legacy unknown session = %d %v, want 404", code, body)
	}
}

// br hierarchical issues are "<parent>.<n>" (bd-1aae9.1, bd-1aae9.1.2) and
// prefixes may contain hyphens; both used to be rejected with 400.
func TestBeadIDPattern(t *testing.T) {
	t.Parallel()
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

// A wedged tmux server must not hang the sessions endpoints: the live listing
// is bounded, the list endpoint degrades to the stored rows, and the detail
// endpoint answers instead of blocking.
func TestHandleSessionsV1_HungTmuxDegradesToStoredSessions(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "stored")
	srv.liveSessionsTimeout = 50 * time.Millisecond
	srv.listLiveSessions = func(ctx context.Context) ([]tmux.Session, error) {
		<-ctx.Done() // tmux never answers
		return nil, ctx.Err()
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.handleSessionsV1(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
		done <- rec
	}()
	select {
	case rec := <-done:
		sessions, count := decodeSessionsList(t, rec)
		if count != 1 || sessions[0]["name"] != "stored" {
			t.Fatalf("sessions = %#v (count %d), want the stored row only", sessions, count)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("list endpoint hung on an unresponsive tmux")
	}

	detail := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/absent", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "absent")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		srv.handleSessionV1(rec, req)
		detail <- rec
	}()
	select {
	case rec := <-detail:
		// The bounded tmux listing timed out: a gateway timeout, not an
		// internal server error.
		assertSessionDetailError(t, rec, http.StatusGatewayTimeout, ErrCodeTimeout)
	case <-time.After(10 * time.Second):
		t.Fatal("detail endpoint hung on an unresponsive tmux")
	}
}

// Any other tmux failure behind the detail endpoint is an unavailable
// dependency (503), not an internal server error.
func TestHandleSessionV1_TmuxFailureIsServiceUnavailable(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return nil, errors.New("tmux circuit breaker open")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/absent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "absent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionV1(rec, req)
	assertSessionDetailError(t, rec, http.StatusServiceUnavailable, ErrCodeServiceUnavail)
}

func assertSessionDetailError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("detail status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode detail error: %v; body: %s", err, rec.Body.String())
	}
	if body.ErrorCode != code {
		t.Fatalf("detail error_code = %q, want %q", body.ErrorCode, code)
	}
}
