package cli

// Regression tests for GitHub issue #255: ntm spawn used to launch each agent
// process (tmux.SendKeysContext) before creating and publishing its Agent
// Mail pane identity, so an agent that resolved its identity during startup
// could read a previous occupant's name or find none. The text output path
// also called registerSpawnedAgents twice.
//
// The fix introduces spawnIdentityCoordinator: identities are prepared and
// published per-pane immediately before the launch keystrokes, and the three
// late batch registration call sites were removed.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// --- Structural ordering tests -------------------------------------------

// spawnFuncDecl parses spawn.go and returns the named top-level function.
func spawnFuncDecl(t *testing.T, funcName string) (*token.FileSet, *ast.FuncDecl) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cli test source")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(testFile), "spawn.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse spawn.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == funcName {
			return fset, fn
		}
	}
	t.Fatalf("%s not found in spawn.go", funcName)
	return nil, nil
}

// TestSpawnPublishesIdentityBeforeLaunch asserts that inside the spawn
// lifecycle every identity preparation call (prepareAgent) textually precedes
// the first agent launch call (tmux.SendKeysContext), and that no late batch
// registration call remains.
func TestSpawnPublishesIdentityBeforeLaunch(t *testing.T) {
	fset, fn := spawnFuncDecl(t, "spawnSessionLogicContextWithOutput")

	var firstSendKeys, prepareCall token.Pos
	registerCalls := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name == "SendKeysContext" && firstSendKeys == token.NoPos {
				firstSendKeys = call.Pos()
			}
			if fun.Sel.Name == "prepareAgent" {
				prepareCall = call.Pos()
			}
		case *ast.Ident:
			if fun.Name == "registerSpawnedAgents" {
				registerCalls++
			}
		}
		return true
	})

	if registerCalls != 0 {
		t.Errorf("spawnSessionLogicContextWithOutput calls registerSpawnedAgents %d time(s), want 0 (identities must be published pre-launch, and the duplicate text-mode registration must stay removed)", registerCalls)
	}
	if prepareCall == token.NoPos {
		t.Fatal("spawnSessionLogicContextWithOutput must call identityCoordinator.prepareAgent")
	}
	if firstSendKeys == token.NoPos {
		t.Fatal("spawnSessionLogicContextWithOutput must call tmux.SendKeysContext")
	}
	if fset.Position(prepareCall).Offset >= fset.Position(firstSendKeys).Offset {
		t.Errorf("prepareAgent (%v) must precede the first tmux.SendKeysContext (%v): agents may not launch before their pane identity is published",
			fset.Position(prepareCall), fset.Position(firstSendKeys))
	}
}

// --- Behavioral coordinator tests -----------------------------------------

// fakeSpawnMailServer is an MCP JSON-RPC stub (root endpoint, same contract
// as mcp-agent-mail) answering health_check, ensure_project, and
// create_agent_identity.
func fakeSpawnMailServer(t *testing.T, agentName string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var tools []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Method  string      `json:"method"`
			Params  struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "tools/call" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "unknown method"},
			})
			return
		}
		mu.Lock()
		tools = append(tools, req.Params.Name)
		mu.Unlock()

		var result interface{}
		switch req.Params.Name {
		case "health_check":
			result = map[string]interface{}{"status": "ok"}
		case "ensure_project":
			result = map[string]interface{}{"id": 1, "slug": "proj", "human_key": "proj"}
		case "create_agent_identity", "register_agent":
			result = map[string]interface{}{
				"id": 42, "name": agentName,
				"program": req.Params.Arguments["program"],
				"model":   req.Params.Arguments["model"],
			}
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": -32601, "message": "unknown tool: " + req.Params.Name},
			})
			return
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw),
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(tools))
		copy(out, tools)
		return out
	}
}

func isolateIdentityDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
}

// TestSpawnIdentityCoordinator_PublishesIdentityAtPrepareTime is the core
// #255 property: after prepareAgent returns (which spawn now calls BEFORE
// sending the launch keystrokes), the pane's canonical identity file and the
// session registry mapping already contain the assigned name — the identity
// a booting agent resolves equals the one reported in AgentMap.
func TestSpawnIdentityCoordinator_PublishesIdentityAtPrepareTime(t *testing.T) {
	isolateIdentityDirs(t)
	srv, calledTools := fakeSpawnMailServer(t, "BraveFalcon")

	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.URL = srv.URL + "/"

	projectKey := t.TempDir()
	session := "spawn_identity_order_test"
	paneID := "%7"

	// Seed a stale identity from a previous pane occupant: the exact hazard
	// from #255. prepareAgent must overwrite it before launch.
	if _, err := agentmail.WriteIdentity(projectKey, paneID, "StaleOldTenant"); err != nil {
		t.Fatalf("seed stale identity: %v", err)
	}

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1,
		paneID:    paneID,
		paneTitle: session + "__cc_1",
		agentType: "cc",
		model:     "opus",
	})

	// The status a booting agent would act on.
	status := coordinator.finalStatus()
	if status == nil || !status.Available || !status.ProjectRegistered {
		t.Fatalf("status = %+v, want available and project registered", status)
	}
	if status.AgentsRegistered != 1 || status.AgentMap[paneID] != "BraveFalcon" {
		t.Fatalf("status = %+v, want 1 registered agent named BraveFalcon for %s", status, paneID)
	}

	// Canonical identity file must already hold the new name.
	raw, err := os.ReadFile(agentmail.CanonicalIdentityPath(projectKey, paneID))
	if err != nil {
		t.Fatalf("canonical identity must exist before launch: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "BraveFalcon" {
		t.Fatalf("canonical identity = %q, want BraveFalcon (stale value must be replaced before launch)", got)
	}

	// Registry mapping must be durable before launch.
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		t.Fatalf("LoadSessionAgentRegistry: %v (registry=%v)", err, registry)
	}
	if name, ok := registry.GetAgentByID(paneID); !ok || name != "BraveFalcon" {
		t.Fatalf("registry name for %s = %q (ok=%v), want BraveFalcon", paneID, name, ok)
	}

	// The server must have been asked to create exactly one identity.
	creates := 0
	for _, tool := range calledTools() {
		if tool == "health_check" {
			t.Fatal("spawn identity registration must not be gated on the slower diagnostic health_check; ensure_project is the availability probe")
		}
		if tool == "create_agent_identity" || tool == "register_agent" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("identity creations = %d, want exactly 1 (no duplicate registration)", creates)
	}
}

// TestSpawnIdentityCoordinator_DisabledIsInert: with no config (or Agent Mail
// off) the coordinator must not touch the network or filesystem and must
// report a nil status, matching the historical registerSpawnedAgents
// contract — spawn proceeds without Agent Mail.
func TestSpawnIdentityCoordinator_DisabledIsInert(t *testing.T) {
	isolateIdentityDirs(t)
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = nil

	coordinator := newSpawnIdentityCoordinator(t.TempDir(), "spawn_disabled_test")
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%3", paneTitle: "x__cc_1", agentType: "cc",
	})
	if status := coordinator.finalStatus(); status != nil {
		t.Fatalf("disabled coordinator status = %+v, want nil", status)
	}
}

// TestSpawnIdentityCoordinator_UnavailableFailsOpen: an unreachable Agent
// Mail server must not block launch preparation; failures are counted and
// spawn continues (graceful degradation preserved by the reordering).
func TestSpawnIdentityCoordinator_UnavailableFailsOpen(t *testing.T) {
	isolateIdentityDirs(t)
	// Health check fails: server answers every tool call with an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"down"}}`))
	}))
	t.Cleanup(srv.Close)

	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.URL = srv.URL + "/"

	coordinator := newSpawnIdentityCoordinator(t.TempDir(), "spawn_unavailable_test")
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%5", paneTitle: "x__cc_1", agentType: "cc",
	})
	status := coordinator.finalStatus()
	if status == nil {
		t.Fatal("enabled-but-unavailable coordinator must report a status")
	}
	if status.Available {
		t.Fatalf("status.Available = true, want false: %+v", status)
	}
	if status.AgentsFailed != 1 {
		t.Fatalf("status.AgentsFailed = %d, want 1", status.AgentsFailed)
	}
}
