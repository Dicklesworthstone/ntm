package agentmail

import (
	"os"
	"path/filepath"
	"testing"
)

// makeSymlinkedDir returns (realDir, linkPath) where linkPath is a symlink to
// realDir, or skips the test when the platform cannot create symlinks.
func makeSymlinkedDir(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}
	return real, link
}

func TestCanonicalProjectKeyResolvesSymlink(t *testing.T) {
	t.Parallel()
	real, link := makeSymlinkedDir(t)
	got := CanonicalProjectKey(link)
	// TempDir itself may sit behind symlinks (e.g. /tmp on macOS), so compare
	// against the fully resolved real path.
	wantResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("resolve real: %v", err)
	}
	if got != wantResolved {
		t.Errorf("CanonicalProjectKey(%q) = %q, want %q", link, got, wantResolved)
	}
}

func TestCanonicalProjectKeyFallsBackOnMissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := CanonicalProjectKey(missing); got != missing {
		t.Errorf("CanonicalProjectKey(missing) = %q, want raw %q", got, missing)
	}
	if got := CanonicalProjectKey("  "); got != "" {
		t.Errorf("CanonicalProjectKey(blank) = %q, want empty", got)
	}
}

func TestProjectKeysEquivalent(t *testing.T) {
	t.Parallel()
	real, link := makeSymlinkedDir(t)

	if !ProjectKeysEquivalent(real, real) {
		t.Error("identical keys must be equivalent")
	}
	if !ProjectKeysEquivalent(link, real) {
		t.Errorf("symlink %q and target %q must be equivalent", link, real)
	}
	if !ProjectKeysEquivalent(real, link) {
		t.Error("equivalence must be symmetric")
	}

	other := t.TempDir()
	if ProjectKeysEquivalent(real, other) {
		t.Errorf("distinct directories %q and %q must NOT be equivalent", real, other)
	}

	// Fail-closed: unresolvable + different strings are not equivalent.
	if ProjectKeysEquivalent(real, filepath.Join(other, "missing")) {
		t.Error("missing path must not be equivalent to a real one")
	}
	if ProjectKeysEquivalent("", real) {
		t.Error("empty key must not be equivalent to a real one")
	}
	if !ProjectKeysEquivalent("", "") {
		t.Error("two empty keys are trivially equivalent")
	}
	if !ProjectKeysEquivalent("/nonexistent/proj/", "/nonexistent/proj") {
		t.Error("cleaned paths must remain equivalent")
	}

	// Symlink-plus-dotdot isolation: link -> base/a/b means link/.. resolves
	// to base/a, NOT base. Lexical filepath.Clean must never conflate them.
	nestedBase := t.TempDir()
	nestedB := filepath.Join(nestedBase, "a", "b")
	if err := os.MkdirAll(nestedB, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	nestedLink := filepath.Join(nestedBase, "link")
	if err := os.Symlink(nestedB, nestedLink); err == nil {
		if ProjectKeysEquivalent(nestedLink+"/..", nestedBase) {
			t.Errorf("ProjectKeysEquivalent(%q, %q) = true, want false (symlink parent != lexical parent)", nestedLink+"/..", nestedBase)
		}
		if !ProjectKeysEquivalent(nestedLink+"/..", filepath.Join(nestedBase, "a")) {
			t.Errorf("ProjectKeysEquivalent(%q, %q) = false, want true", nestedLink+"/..", filepath.Join(nestedBase, "a"))
		}
	}
}

func TestSymlinkedProjectPathRegistrySessionAndTokenHydration(t *testing.T) {
	realDir, linkDir := makeSymlinkedDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const sessName = "symlink-test-sess"
	info := &SessionAgentInfo{
		AgentName:         "BlueLake",
		ProjectKey:        realDir,
		RegistrationToken: "tok-coord-1",
	}
	if err := SaveSessionAgent(sessName, realDir, info); err != nil {
		t.Fatalf("SaveSessionAgent: %v", err)
	}
	loadedInfo, err := LoadSessionAgent(sessName, linkDir)
	if err != nil || loadedInfo == nil || loadedInfo.AgentName != "BlueLake" {
		t.Fatalf("LoadSessionAgent via symlink = (%+v, %v), want BlueLake", loadedInfo, err)
	}

	reg := &SessionAgentRegistry{
		SessionName: sessName,
		ProjectKey:  realDir,
		Agents:      map[string]string{"%1": "GreenCastle"},
	}
	reg.SetRegistrationToken("GreenCastle", "tok-agent-1")
	if err := SaveSessionAgentRegistry(reg); err != nil {
		t.Fatalf("SaveSessionAgentRegistry: %v", err)
	}

	loadedReg, err := LoadSessionAgentRegistry(sessName, linkDir)
	if err != nil || loadedReg == nil || loadedReg.Agents["%1"] != "GreenCastle" {
		t.Fatalf("LoadSessionAgentRegistry via symlink = (%+v, %v), want GreenCastle", loadedReg, err)
	}

	client := NewClient()
	HydrateClientTokensForProject(client, linkDir)
	if got := client.RegistrationToken(linkDir, "GreenCastle"); got != "tok-agent-1" {
		t.Fatalf("RegistrationToken via symlink = %q, want tok-agent-1", got)
	}
	if got := client.RegistrationToken(realDir, "GreenCastle"); got != "tok-agent-1" {
		t.Fatalf("RegistrationToken via real path = %q, want tok-agent-1", got)
	}

	// Mutating via the symlink alias must update the same canonical registry
	// file rather than creating a second split-brain slug directory.
	loadedReg.ProjectKey = linkDir
	loadedReg.SetRegistrationToken("GreenCastle", "tok-agent-2")
	if err := SaveSessionAgentRegistry(loadedReg); err != nil {
		t.Fatalf("SaveSessionAgentRegistry via symlink: %v", err)
	}
	reloadedReal, err := LoadSessionAgentRegistry(sessName, realDir)
	if err != nil || reloadedReal == nil || reloadedReal.RegistrationToken("GreenCastle") != "tok-agent-2" {
		t.Fatalf("reloaded real registry = (%+v, %v), want tok-agent-2", reloadedReal, err)
	}
}
