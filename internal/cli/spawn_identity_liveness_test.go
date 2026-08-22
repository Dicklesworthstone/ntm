package cli

// Regression tests for ntm#256 (occupied identity slots re-issued to a new
// pane) and ntm#257 (worktree panes cannot find the identity NTM published).
//
// The coordinator used to look identities up title-first, so a freshly
// composed title such as sess__cc_1 matched whatever agent last held it —
// even one still running in another pane. Occupancy is now decided by the
// liveness of the recorded binding (pane exists, same pid), and a worktree
// pane's identity is published under its own directory too.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// stubPaneProbe replaces the coordinator's tmux pane listing for the test.
func stubPaneProbe(t *testing.T, panes []tmux.Pane, err error) {
	t.Helper()
	old := spawnIdentityPaneProbe
	spawnIdentityPaneProbe = func(context.Context, string) ([]tmux.Pane, error) {
		return panes, err
	}
	t.Cleanup(func() { spawnIdentityPaneProbe = old })
}

func enableFakeAgentMail(t *testing.T, url string) {
	t.Helper()
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.URL = url + "/"
}

// seedRegistry persists a registry where paneID holds title as agentName
// with the given pid, the state left behind by an earlier spawn.
func seedRegistry(t *testing.T, session, projectKey, title, paneID, agentName string, pid int) {
	t.Helper()
	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	registry.AddAgent(title, paneID, agentName)
	registry.SetPanePID(paneID, pid)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if _, err := agentmail.WriteIdentity(projectKey, paneID, agentName); err != nil {
		t.Fatalf("seed identity file: %v", err)
	}
}

func readIdentity(t *testing.T, projectKey, paneID string) string {
	t.Helper()
	raw, err := os.ReadFile(agentmail.CanonicalIdentityPath(projectKey, paneID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func countCreates(tools []string) int {
	n := 0
	for _, tool := range tools {
		if tool == "create_agent_identity" || tool == "register_agent" {
			n++
		}
	}
	return n
}

// TestSpawnIdentityCoordinator_LiveHolderKeepsItsName is the ntm#256
// reproduction: %5 registered as sess__cc_1 and was retitled; `ntm add`
// composes sess__cc_1 again for %9. %5 is alive, so %9 must get a FRESH
// identity and %5 must keep its name, registry row, and identity file.
func TestSpawnIdentityCoordinator_LiveHolderKeepsItsName(t *testing.T) {
	isolateIdentityDirs(t)
	srv, calledTools := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)

	projectKey := t.TempDir()
	session := "spawn_identity_live_holder"
	seedRegistry(t, session, projectKey, session+"__cc_1", "%5", "OldTenant", 111)
	stubPaneProbe(t, []tmux.Pane{
		{ID: "%5", PID: 111, Title: "my custom title"},
		{ID: "%9", PID: 999, Title: session + "__cc_1"},
	}, nil)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 2, paneID: "%9", paneTitle: session + "__cc_1", agentType: "cc", model: "opus",
	})

	status := coordinator.finalStatus()
	if status == nil || status.AgentsRegistered != 1 || status.AgentMap["%9"] != "BraveFalcon" {
		t.Fatalf("status = %+v, want a fresh BraveFalcon for %%9", status)
	}
	if creates := countCreates(calledTools()); creates != 1 {
		t.Fatalf("identity creations = %d, want 1 (occupied slot must not be reused)", creates)
	}
	if got := readIdentity(t, projectKey, "%5"); got != "OldTenant" {
		t.Fatalf("live holder identity file = %q, want OldTenant untouched", got)
	}
	if got := readIdentity(t, projectKey, "%9"); got != "BraveFalcon" {
		t.Fatalf("new pane identity file = %q, want BraveFalcon", got)
	}

	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		t.Fatalf("load registry: %v", err)
	}
	if name, _ := registry.GetAgentByID("%5"); name != "OldTenant" {
		t.Fatalf("registry row for live holder %%5 = %q, want OldTenant", name)
	}
	if name, _ := registry.GetAgentByID("%9"); name != "BraveFalcon" {
		t.Fatalf("registry row for %%9 = %q, want BraveFalcon", name)
	}
	if registry.PanePID("%9") != 999 {
		t.Fatalf("recorded pid for %%9 = %d, want 999", registry.PanePID("%9"))
	}
}

// TestSpawnIdentityCoordinator_DeadHolderIsReused keeps the #69 contract:
// after the holder of a title died, a same-session respawn gets its name
// back without a server round-trip, and the binding moves to the new pane.
func TestSpawnIdentityCoordinator_DeadHolderIsReused(t *testing.T) {
	isolateIdentityDirs(t)
	srv, calledTools := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)

	projectKey := t.TempDir()
	session := "spawn_identity_dead_holder"
	seedRegistry(t, session, projectKey, session+"__cc_1", "%5", "OldTenant", 111)
	// %5 is gone; %9 is the respawned pane.
	stubPaneProbe(t, []tmux.Pane{{ID: "%9", PID: 999, Title: session + "__cc_1"}}, nil)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%9", paneTitle: session + "__cc_1", agentType: "cc",
	})

	status := coordinator.finalStatus()
	if status == nil || status.AgentsRegistered != 1 || status.AgentMap["%9"] != "OldTenant" {
		t.Fatalf("status = %+v, want OldTenant reused for %%9", status)
	}
	if creates := countCreates(calledTools()); creates != 0 {
		t.Fatalf("identity creations = %d, want 0 (dead slot must be reused)", creates)
	}
	if got := readIdentity(t, projectKey, "%9"); got != "OldTenant" {
		t.Fatalf("identity file for %%9 = %q, want OldTenant", got)
	}
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		t.Fatalf("load registry: %v", err)
	}
	if _, stale := registry.GetAgentByID("%5"); stale {
		t.Fatal("dead pane %5 must be unbound after its name moved")
	}
	if registry.PanePID("%5") != 0 || registry.PanePID("%9") != 999 {
		t.Fatalf("pids after move: %%5=%d %%9=%d, want 0 and 999", registry.PanePID("%5"), registry.PanePID("%9"))
	}
}

// TestSpawnIdentityCoordinator_RecycledPaneIDIsDead: tmux restarted, so the
// old holder's %N exists again under a different pid. The recorded binding
// is dead and the name is free for the new occupant of that very pane.
func TestSpawnIdentityCoordinator_RecycledPaneIDIsDead(t *testing.T) {
	isolateIdentityDirs(t)
	srv, calledTools := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)

	projectKey := t.TempDir()
	session := "spawn_identity_recycled"
	seedRegistry(t, session, projectKey, session+"__cc_1", "%5", "OldTenant", 111)
	stubPaneProbe(t, []tmux.Pane{
		{ID: "%5", PID: 222, Title: session + "__user_0"}, // recycled id, different process
		{ID: "%9", PID: 999, Title: session + "__cc_1"},
	}, nil)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%9", paneTitle: session + "__cc_1", agentType: "cc",
	})

	if status := coordinator.finalStatus(); status == nil || status.AgentMap["%9"] != "OldTenant" {
		t.Fatalf("status = %+v, want OldTenant recovered from the dead %%5 binding", status)
	}
	if creates := countCreates(calledTools()); creates != 0 {
		t.Fatalf("identity creations = %d, want 0", creates)
	}
}

// TestSpawnIdentityCoordinator_UnobservableLivenessNeverShares: when the
// pane topology cannot be read, a title match must not hand out a possibly
// live name; the pane gets a fresh identity instead.
func TestSpawnIdentityCoordinator_UnobservableLivenessNeverShares(t *testing.T) {
	isolateIdentityDirs(t)
	srv, calledTools := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)

	projectKey := t.TempDir()
	session := "spawn_identity_unobservable"
	seedRegistry(t, session, projectKey, session+"__cc_1", "%5", "OldTenant", 111)
	stubPaneProbe(t, nil, errors.New("tmux unreachable"))

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%9", paneTitle: session + "__cc_1", agentType: "cc",
	})

	if status := coordinator.finalStatus(); status == nil || status.AgentMap["%9"] != "BraveFalcon" {
		t.Fatalf("status = %+v, want a fresh identity when liveness is unobservable", status)
	}
	if creates := countCreates(calledTools()); creates != 1 {
		t.Fatalf("identity creations = %d, want 1", creates)
	}
	if got := readIdentity(t, projectKey, "%5"); got != "OldTenant" {
		t.Fatalf("possibly-live holder's identity file = %q, want OldTenant untouched", got)
	}
}

// TestSpawnIdentityCoordinator_WorktreePanePublishesUnderBothKeys covers
// ntm#257: a pane launched in a linked worktree must find its assigned name
// whether it resolves by the session key or by its own cwd.
func TestSpawnIdentityCoordinator_WorktreePanePublishesUnderBothKeys(t *testing.T) {
	isolateIdentityDirs(t)
	srv, _ := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)

	projectKey := t.TempDir()
	worktree := filepath.Join(projectKey, ".ntm", "worktrees", "sess", "cc-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	session := "spawn_identity_worktree"
	stubPaneProbe(t, []tmux.Pane{{ID: "%3", PID: 333, Title: session + "__cc_1"}}, nil)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%3", paneTitle: session + "__cc_1", agentType: "cc", paneDir: worktree,
	})

	if got := readIdentity(t, projectKey, "%3"); got != "BraveFalcon" {
		t.Fatalf("identity under session key = %q, want BraveFalcon", got)
	}
	if got := readIdentity(t, worktree, "%3"); got != "BraveFalcon" {
		t.Fatalf("identity under worktree key = %q, want BraveFalcon (cwd-derived resolution)", got)
	}
	// Both hashes must genuinely differ, or the second check proves nothing.
	if agentmail.CanonicalIdentityPath(projectKey, "%3") == agentmail.CanonicalIdentityPath(worktree, "%3") {
		t.Fatal("test setup: session and worktree keys hash to the same identity path")
	}
	// Legacy compat files are published under both keys as well.
	for _, key := range []string{projectKey, worktree} {
		legacy := agentmail.WriteLegacyCompatIdentity(key, "%3", "probe")
		if legacy == "" {
			t.Fatalf("legacy compat path for %s not writable", key)
		}
		_ = os.Remove(legacy)
	}

	// A plain (non-worktree) pane publishes only under the session key.
	plain := newSpawnIdentityCoordinator(projectKey, session)
	plain.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 2, paneID: "%4", paneTitle: session + "__cc_2", agentType: "cc",
	})
	if got := readIdentity(t, projectKey, "%4"); got != "BraveFalcon" {
		t.Fatalf("plain pane identity under session key = %q", got)
	}
	if got := readIdentity(t, worktree, "%4"); got != "" {
		t.Fatalf("plain pane must not publish under the worktree key, got %q", got)
	}
}

// TestSpawnWorktreeInjectsAgentMailProject pins the env contract from
// ntm#257: with --worktrees every pane command carries
// AGENT_MAIL_PROJECT=<session dir>, unless a plugin env var or --pane-env
// already sets it.
func TestSpawnWorktreeInjectsAgentMailProject(t *testing.T) {
	// The launch loop needs a real tmux session to drive, so assert
	// structurally (same technique as TestSpawnPublishesIdentityBeforeLaunch):
	// the injection and the --pane-env override check must both live inside
	// the spawn lifecycle function.
	fset, fn := spawnFuncDecl(t, "spawnSessionLogicContextWithOutput")
	src, err := os.ReadFile(fset.Position(fn.Pos()).Filename)
	if err != nil {
		t.Fatal(err)
	}
	body := string(src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])
	if !strings.Contains(body, `envVars["AGENT_MAIL_PROJECT"] = dir`) {
		t.Fatal("spawn must inject AGENT_MAIL_PROJECT=<session dir> into worktree pane commands (ntm#257)")
	}
	if !strings.Contains(body, `opts.PaneEnv["AGENT_MAIL_PROJECT"]`) {
		t.Fatal("spawn must defer to an explicit --pane-env AGENT_MAIL_PROJECT value")
	}
	if !strings.Contains(body, `paneDir:`) {
		t.Fatal("spawn must hand the pane's launch directory to prepareAgent so worktree identities publish under both keys")
	}
}
