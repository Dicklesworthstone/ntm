package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
)

func makeWorktrees(t *testing.T, dir, session string, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		if err := os.MkdirAll(filepath.Join(dir, ".ntm", "worktrees", session, agent), 0o755); err != nil {
			t.Fatalf("mkdir worktree for %s: %v", agent, err)
		}
	}
}

func TestRecorderConfigsSharedCheckoutUsesOneSessionRecorder(t *testing.T) {
	dir := t.TempDir()
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	configs := fileChangeRecorderConfigs(t.Context(), manifest)
	if len(configs) != 1 {
		t.Fatalf("shared checkout produced %d recorders, want 1", len(configs))
	}
	if configs[0].Identity != "sess" {
		t.Errorf("identity = %q, want the session name for a shared checkout", configs[0].Identity)
	}
}

// Worktree isolation is what makes per-agent attribution honest: each agent owns
// a tree, so git can say who touched a file.
func TestRecorderConfigsOnePerAgentWorktree(t *testing.T) {
	dir := t.TempDir()
	makeWorktrees(t, dir, "sess", "cc-1", "cod-2", "gmi-3")
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	configs := fileChangeRecorderConfigs(t.Context(), manifest)
	if len(configs) != 3 {
		t.Fatalf("three agent worktrees produced %d recorders, want 3", len(configs))
	}
	for _, cfg := range configs {
		if cfg.ProjectDir != dir {
			t.Errorf("worktree recorder keyed by %q, want the project %q", cfg.ProjectDir, dir)
		}
		if cfg.Identity == "sess" {
			t.Error("a worktree recorder must be attributed to its agent, not the session")
		}
	}
}

// A worktrees directory that exists but holds no agent trees must still record
// something rather than silently tracking nothing.
func TestRecorderConfigsEmptyWorktreeDirFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ntm", "worktrees", "sess"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	if got := len(fileChangeRecorderConfigs(t.Context(), manifest)); got != 1 {
		t.Errorf("empty worktree dir produced %d recorders, want 1 session-level fallback", got)
	}
}

func TestRecorderConfigsRejectIncompleteManifest(t *testing.T) {
	for name, manifest := range map[string]*resilience.SpawnManifest{
		"nil":            nil,
		"no session":     {ProjectDir: t.TempDir()},
		"no project dir": {Session: "sess"},
	} {
		if got := fileChangeRecorderConfigs(t.Context(), manifest); got != nil {
			t.Errorf("%s manifest produced %d recorders, want none", name, len(got))
		}
	}
}

// `ntm add` can give a running session new agents, and with worktree isolation
// new working trees. A recorder set fixed at startup would keep attributing
// everything to the session — and would never look inside the new trees at all,
// since worktrees live under the gitignored .ntm.
func TestSamplerPicksUpWorktreesCreatedAfterStart(t *testing.T) {
	dir := t.TempDir()
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}
	sampler := newFileChangeSampler(manifest)

	sampler.Sample(t.Context())
	if got := len(sampler.recorders); got != 1 {
		t.Fatalf("before any worktree exists: %d recorders, want the session fallback", got)
	}

	makeWorktrees(t, dir, "sess", "cc-1", "cod-2")
	sampler.Sample(t.Context())

	if got := len(sampler.recorders); got != 3 {
		t.Errorf("after worktrees appeared: %d recorders, want 3 (session fallback plus two agents)", got)
	}
	for _, agent := range []string{"cc-1", "cod-2"} {
		root := filepath.Join(dir, ".ntm", "worktrees", "sess", agent)
		if _, ok := sampler.recorders[root]; !ok {
			t.Errorf("no recorder adopted for the new worktree %s", root)
		}
	}
}

// A recorder must survive re-derivation, or every tick would re-baseline and
// silently discard the changes made since the previous one.
func TestSamplerReusesRecordersAcrossTicks(t *testing.T) {
	dir := t.TempDir()
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}
	sampler := newFileChangeSampler(manifest)

	sampler.Sample(t.Context())
	first := sampler.recorders[dir]
	if first == nil {
		t.Fatal("no recorder for the project root")
	}

	sampler.Sample(t.Context())
	if sampler.recorders[dir] != first {
		t.Error("the recorder was rebuilt between ticks, discarding its baseline")
	}
}

// Sampling is best-effort: a non-repository must be logged and skipped, never
// allowed to take down the monitor loop that drives it.
func TestSamplerToleratesFailuresAndNils(t *testing.T) {
	sampler := newFileChangeSampler(&resilience.SpawnManifest{Session: "sess", ProjectDir: t.TempDir()})
	sampler.Sample(t.Context())

	var nilSampler *fileChangeSampler
	nilSampler.Sample(t.Context())

	newFileChangeSampler(nil).Sample(t.Context())
}

func TestDescribeFileChangeSampler(t *testing.T) {
	dir := t.TempDir()
	if got := describeFileChangeSampler(t.Context(), nil); got == "" {
		t.Error("a nil manifest should still describe itself")
	}
	if got := describeFileChangeSampler(t.Context(), &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}); got == "" {
		t.Error("a shared checkout should describe itself")
	}
	makeWorktrees(t, dir, "sess", "cc-1", "cod-2")
	if got := describeFileChangeSampler(t.Context(), &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}); got == "" {
		t.Error("worktrees should describe themselves")
	}
}
