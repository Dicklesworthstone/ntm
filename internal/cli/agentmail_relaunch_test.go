package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestRelaunchRegistrationInputs(t *testing.T) {
	got := relaunchRegistrationInputs([]robot.RestartedAgentPane{
		{PaneIndex: 1, PaneID: "%3", PaneTitle: "demo__cc_1", AgentType: "cc", Variant: "sonnet"},
		{PaneIndex: 2, PaneID: "%4", PaneTitle: "demo__cod_1", AgentType: "cod"},
	})
	if len(got) != 2 {
		t.Fatalf("inputs = %d, want 2", len(got))
	}
	if got[0].paneID != "%3" || got[0].paneIndex != 1 || got[0].paneTitle != "demo__cc_1" ||
		got[0].agentType != "cc" || got[0].model != "sonnet" {
		t.Fatalf("first input = %+v, want the Claude pane with variant carried as model", got[0])
	}
	if got[1].paneID != "%4" || got[1].agentType != "cod" || got[1].model != "" {
		t.Fatalf("second input = %+v, want the Codex pane with empty model", got[1])
	}
}

// registerRelaunchedPanes must fail closed: with no config (or Agent Mail
// disabled) it may not resolve project dirs, contact tmux, or touch the
// network — a restart must never degrade because Agent Mail is off (bd-vb7s3).
func TestRegisterRelaunchedPanesFailsClosedWithoutConfig(t *testing.T) {
	oldCfg := cfg
	cfg = nil
	defer func() { cfg = oldCfg }()

	// Must return immediately without panicking or shelling out.
	registerRelaunchedPanes(t.Context(), "no-such-session", []robot.RestartedAgentPane{
		{PaneIndex: 1, PaneID: "%3", PaneTitle: "demo__cc_1", AgentType: "cc"},
	})
}

// countCallsTo parses a cli source file and counts direct calls to callee
// inside the named top-level function — the same structural technique as
// TestAddRegistersAgentsWithAgentMail (registration paths need a live tmux
// session to drive end-to-end).
func countCallsTo(t *testing.T, sourceFile, funcName, callee string) int {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cli test source")
	}
	path := filepath.Join(filepath.Dir(testFile), sourceFile)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sourceFile, err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == funcName {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatalf("%s not found in %s", funcName, sourceFile)
	}
	calls := 0
	ast.Inspect(target, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == callee {
				calls++
			}
		}
		return true
	})
	return calls
}

// bd-vb7s3 parity guards: add and adopt must register the session-level
// coordinator identity the same way spawn does (spawn.go:3507), and the
// relaunch hook must run both gated helpers so restarted panes regain
// resolvable identities.
func TestAddRegistersSessionAgentWithAgentMail(t *testing.T) {
	if got := countCallsTo(t, "add.go", "executeAdd", "registerSessionAgent"); got != 1 {
		t.Fatalf("executeAdd calls registerSessionAgent %d time(s), want exactly 1 (session-level identity parity with spawn)", got)
	}
}

func TestAdoptRegistersSessionAgentWithAgentMail(t *testing.T) {
	if got := countCallsTo(t, "adopt.go", "runAdopt", "registerSessionAgent"); got != 1 {
		t.Fatalf("runAdopt calls registerSessionAgent %d time(s), want exactly 1 (adopted sessions have no spawn-time coordinator identity)", got)
	}
}

func TestRelaunchHookRunsGatedRegistrationFlow(t *testing.T) {
	if got := countCallsTo(t, "agentmail_relaunch.go", "registerRelaunchedPanes", "registerSessionAgent"); got != 1 {
		t.Fatalf("registerRelaunchedPanes calls registerSessionAgent %d time(s), want exactly 1", got)
	}
	if got := countCallsTo(t, "agentmail_relaunch.go", "registerRelaunchedPanes", "registerSpawnedAgents"); got != 1 {
		t.Fatalf("registerRelaunchedPanes calls registerSpawnedAgents %d time(s), want exactly 1", got)
	}
}
