package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// findRepoGoMod walks up from the working directory to the repository's own
// go.mod (the source of truth the H8 round-2 refinement pins the template
// version against).
func findRepoGoMod(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		p := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository go.mod not found above working directory")
		}
		dir = parent
	}
}

var goDirectiveRe = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)

func goDirectiveVersion(t *testing.T, gomod []byte) string {
	t.Helper()
	m := goDirectiveRe.FindSubmatch(gomod)
	if m == nil {
		t.Fatalf("no go directive found in go.mod:\n%s", gomod)
	}
	return string(m[1])
}

// compareGoVersions returns -1, 0, or 1 comparing dotted Go versions.
func compareGoVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// TestQuickGoTemplateVersionDerivedNotHandPinned is the H8 round-2 proof:
// the go template's version directive is DERIVED (toolchain-sourced via
// goTemplateVersion) rather than a hand-updated literal, and can never lag
// behind NTM's own go.mod directive — the audited failure mode was a stale
// hardcoded "go 1.25" while go.mod required 1.26.x.
func TestQuickGoTemplateVersionDerivedNotHandPinned(t *testing.T) {
	dir := t.TempDir()
	if err := applyGoTemplate(dir); err != nil {
		t.Fatalf("applyGoTemplate: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read generated go.mod: %v", err)
	}
	genVersion := goDirectiveVersion(t, generated)

	// The template must emit exactly what the derivation helper produces.
	if want := goTemplateVersion(); genVersion != want {
		t.Fatalf("template pinned go %q but goTemplateVersion() = %q — version is not derived", genVersion, want)
	}

	// Source-of-truth check: the derived version must be >= the repo's own
	// go.mod directive. The tests themselves compile under that directive
	// with the same toolchain the template derives from, so a regression to a
	// hand-pinned stale literal fails here.
	repoGoMod, err := os.ReadFile(findRepoGoMod(t))
	if err != nil {
		t.Fatalf("read repo go.mod: %v", err)
	}
	repoVersion := goDirectiveVersion(t, repoGoMod)
	if compareGoVersions(genVersion, repoVersion) < 0 {
		t.Fatalf("template go version %s is stale: repo go.mod requires %s", genVersion, repoVersion)
	}
	t.Logf("TEST: QuickGoTemplateVersion | template=go %s | repo go.mod=go %s", genVersion, repoVersion)
}

// TestQuickCreatesSessionMatchingQpsAlias pins the H8 qps-semantics decision:
// `ntm quick` (aliased qps — "quick project session") now actually creates
// the detached tmux session its name implies, titles the pane as the user
// pane via the canonical maker, and enables pane-border-status
// session-locally. `--no-session` (quickOptions.NoSession) opts out.
func TestQuickCreatesSessionMatchingQpsAlias(t *testing.T) {
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed, skipping test")
	}
	base := t.TempDir()
	t.Setenv("NTM_PROJECTS_BASE", base)

	name := fmt.Sprintf("ntmquick%d", time.Now().UnixNano()%1e9)
	t.Cleanup(func() { _ = tmux.KillSession(name) })

	if err := runQuick(name, quickOptions{NoGit: true, NoVSCode: true, NoClaudeConfig: true}); err != nil {
		t.Fatalf("runQuick: %v", err)
	}

	if !tmux.SessionExists(name) {
		t.Fatalf("quick did not create tmux session %q — qps alias semantics broken", name)
	}
	panes, err := tmux.GetPanes(name)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes(%q): panes=%d err=%v", name, len(panes), err)
	}
	if want := config.UserPaneTitle(name); panes[0].Title != want {
		t.Errorf("user pane title = %q, want %q", panes[0].Title, want)
	}
	status, err := tmux.DefaultClient.Run("show-options", "-A", "-w", "-v", "-t", name, "pane-border-status")
	if err != nil {
		t.Fatalf("show-options pane-border-status: %v", err)
	}
	switch strings.TrimSpace(status) {
	case "top", "bottom":
	default:
		t.Errorf("pane-border-status = %q, want top or bottom (titles invisible)", strings.TrimSpace(status))
	}

	// Opt-out path: --no-session scaffolds without touching tmux.
	name2 := name + "b"
	t.Cleanup(func() { _ = tmux.KillSession(name2) })
	if err := runQuick(name2, quickOptions{NoGit: true, NoVSCode: true, NoClaudeConfig: true, NoSession: true}); err != nil {
		t.Fatalf("runQuick --no-session: %v", err)
	}
	if tmux.SessionExists(name2) {
		t.Errorf("--no-session still created tmux session %q", name2)
	}
}
