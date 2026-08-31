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

func TestInvocationProjectKeyOverrideIsScopedAndExplicit(t *testing.T) {
	physical := "/home/ubuntu/work/bbi-infrastructure"
	canonical := "/repos/github.com/biji-biji-initiative/bbi-infrastructure"

	t.Setenv(ProjectKeyOverrideEnv, "  "+canonical+"  ")
	if got := InvocationProjectKey(physical); got != canonical {
		t.Fatalf("InvocationProjectKey() = %q, want canonical public key %q", got, canonical)
	}

	t.Setenv(ProjectKeyOverrideEnv, " \t ")
	if got := InvocationProjectKey(physical); got != physical {
		t.Fatalf("blank override must preserve physical key: got %q want %q", got, physical)
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
}
