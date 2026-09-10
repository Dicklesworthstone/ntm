package robot

// Regression test for the second half of GitHub issue #321.
//
// GetRestartPaneContext used to invoke the Agent Mail identity callback only
// AFTER its relaunch loop had already typed each pane's agent launch command.
// By then the worker had booted, resolved (or failed to resolve) its pane
// identity, and inherited whatever registration token was on disk before the
// restart — so re-registering afterwards refreshed the registry and the server
// while leaving the running process holding a superseded credential.
//
// The identity callback must run after the shell respawn and BEFORE any agent
// CLI is launched, exactly once. The relaunch loop is driven by real tmux
// functions passed as literals inside GetRestartPaneContext, so there is no
// behavioral seam to drive this ordering from a test without a tmux server;
// this pins the call order structurally, the same technique
// internal/cli/spawn_identity_order_test.go uses for the spawn path (#255).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// restartPaneFuncDecl parses restart_pane.go and returns the named top-level
// function declaration.
func restartPaneFuncDecl(t *testing.T, funcName string) (*token.FileSet, *ast.FuncDecl) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate robot test source")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(testFile), "restart_pane.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse restart_pane.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == funcName {
			return fset, fn
		}
	}
	t.Fatalf("%s not found in restart_pane.go", funcName)
	return nil, nil
}

// TestRestartPublishesIdentityBeforeAgentLaunch asserts that inside
// GetRestartPaneContext the identity callback is invoked exactly once and
// textually precedes the agent relaunch call, so a restarted worker never
// starts before its refreshed identity and registration token are durable.
func TestRestartPublishesIdentityBeforeAgentLaunch(t *testing.T) {
	fset, fn := restartPaneFuncDecl(t, "GetRestartPaneContext")

	var firstRelaunch, identityCall token.Pos
	identityCalls := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "relaunchRestartPaneAgentContext":
			if firstRelaunch == token.NoPos {
				firstRelaunch = call.Pos()
			}
		case "notifyRestartPaneIdentityHook":
			identityCalls++
			if identityCall == token.NoPos {
				identityCall = call.Pos()
			}
		}
		return true
	})

	if identityCalls != 1 {
		t.Fatalf("GetRestartPaneContext calls notifyRestartPaneIdentityHook %d time(s), want exactly 1 (the identity callback must fire once per restart, never twice)", identityCalls)
	}
	if firstRelaunch == token.NoPos {
		t.Fatal("GetRestartPaneContext must call relaunchRestartPaneAgentContext")
	}
	if fset.Position(identityCall).Offset >= fset.Position(firstRelaunch).Offset {
		t.Errorf("notifyRestartPaneIdentityHook (%v) must precede the first relaunchRestartPaneAgentContext (%v): a restarted agent may not launch before its Agent Mail identity and rotated registration token are persisted (#321)",
			fset.Position(identityCall), fset.Position(firstRelaunch))
	}
}

// TestRestartIdentityHookRunsAfterRespawn guards the other end of the
// window: the callback must come after the panes have actually been
// respawned, otherwise it would refresh bindings for processes that are about
// to be killed.
func TestRestartIdentityHookRunsAfterRespawn(t *testing.T) {
	fset, fn := restartPaneFuncDecl(t, "GetRestartPaneContext")

	var respawnCall, identityCall token.Pos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "respawnRestartPaneTargetsContext":
			if respawnCall == token.NoPos {
				respawnCall = call.Pos()
			}
		case "notifyRestartPaneIdentityHook":
			if identityCall == token.NoPos {
				identityCall = call.Pos()
			}
		}
		return true
	})

	if respawnCall == token.NoPos {
		t.Fatal("GetRestartPaneContext must call respawnRestartPaneTargetsContext")
	}
	if identityCall == token.NoPos {
		t.Fatal("GetRestartPaneContext must call notifyRestartPaneIdentityHook")
	}
	if fset.Position(identityCall).Offset <= fset.Position(respawnCall).Offset {
		t.Errorf("notifyRestartPaneIdentityHook (%v) must follow respawnRestartPaneTargetsContext (%v): identities are refreshed for the new pane processes, not the ones being replaced",
			fset.Position(identityCall), fset.Position(respawnCall))
	}
}
