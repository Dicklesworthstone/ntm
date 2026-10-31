package bv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkBeadsDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
}

func TestBeadsReadArgsMakesReadsWithoutALocalTrackerReadOnly(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"list", "--json"},
		{"ready", "--json"},
		{"show", "bd-1", "--json"},
		{"stats", "--json"},
		{"blocked", "--json"},
		{"dep", "list", "bd-1", "--json"},
		{"dep", "tree", "bd-1", "--json"},
	} {
		got := BeadsReadArgs(dir, args)
		want := append(append([]string(nil), args...), beadsReadOnlyFlags...)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("BeadsReadArgs(%v) = %v, want %v", args, got, want)
		}
	}
}

func TestBeadsReadArgsLeavesLocalTrackersAndWritesAlone(t *testing.T) {
	local := t.TempDir()
	mkBeadsDir(t, local)
	if got := BeadsReadArgs(local, []string{"list", "--json"}); strings.Join(got, " ") != "list --json" {
		t.Errorf("read of a local tracker changed: %v", got)
	}

	noTracker := t.TempDir()
	for _, args := range [][]string{
		{"create", "title", "--json"},
		{"update", "bd-1", "--json"},
		{"close", "bd-1"},
		{"dep", "add", "bd-1", "bd-2"},
		{"sync", "--flush-only", "--json"},
		// br rewrites an older-schema tracker under `info` even with the flags.
		{"info", "--json"},
	} {
		if got := BeadsReadArgs(noTracker, args); strings.Join(got, " ") != strings.Join(args, " ") {
			t.Errorf("BeadsReadArgs(%v) = %v, want no change", args, got)
		}
	}
}

func TestBeadsReadArgsDoesNotRepeatFlagsOrShareTheCallersSlice(t *testing.T) {
	args := make([]string, 2, 8)
	args[0], args[1] = "list", "--no-auto-import"
	got := BeadsReadArgs(t.TempDir(), args)
	if strings.Join(got, " ") != "list --no-auto-import --no-auto-flush" {
		t.Fatalf("BeadsReadArgs = %v", got)
	}
	if extra := args[:3][2]; extra != "" {
		t.Fatalf("BeadsReadArgs wrote into the caller's slice: %q", extra)
	}
}

// A caller that resolved the project first passes the tracker root, so only the
// process's own directory shows that the tracker was found by walking up.
func TestBeadsReadArgsSeesAWalkUpTheCallerAlreadyResolved(t *testing.T) {
	root := t.TempDir()
	mkBeadsDir(t, root)
	nested := filepath.Join(root, "project", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	t.Chdir(nested)
	if got := BeadsReadArgs(root, []string{"stats", "--json"}); strings.Join(got, " ") != "stats --json --no-auto-import --no-auto-flush" {
		t.Errorf("resolved walk-up read = %v", got)
	}
	if got := BeadsReadArgs(root, []string{"close", "bd-1"}); strings.Join(got, " ") != "close bd-1" {
		t.Errorf("resolved walk-up write changed: %v", got)
	}

	other := t.TempDir()
	mkBeadsDir(t, other)
	if got := BeadsReadArgs(other, []string{"stats", "--json"}); strings.Join(got, " ") != "stats --json" {
		t.Errorf("another project's local tracker changed: %v", got)
	}

	t.Chdir(root)
	if got := BeadsReadArgs(root, []string{"stats", "--json"}); strings.Join(got, " ") != "stats --json" {
		t.Errorf("read from the tracker's own directory changed: %v", got)
	}
}

// A read from inside the tracker's own repository is the project's own read and
// keeps importing. Anything else stays read-only, including a tree where no
// repository can be found. Paths are relative to a base holding the tracker at
// "project".
func TestBeadsReadArgsGitBoundary(t *testing.T) {
	const plain, flagged = "stats --json", "stats --json --no-auto-import --no-auto-flush"
	for _, tc := range []struct {
		name     string
		gitDirs  []string
		gitFiles []string
		from     string
		want     string
	}{
		{name: "repo subdirectory", gitDirs: []string{"project"}, from: "project/src/deep", want: plain},
		{name: "worktree checkout holding the tracker", gitFiles: []string{"project"}, from: "project/src", want: plain},
		{name: "tracker below the repo root", gitDirs: []string{"."}, from: "project/src", want: plain},
		{name: "clone under a parent tracker", gitDirs: []string{"project/clone"}, from: "project/clone/src", want: flagged},
		{name: "clone root under a parent tracker", gitDirs: []string{"project/clone"}, from: "project/clone", want: flagged},
		{name: "clone under a tracker in its own repo", gitDirs: []string{"project", "project/clone"}, from: "project/clone/src", want: flagged},
		{name: "worktree under a parent tracker", gitFiles: []string{"project/wt"}, from: "project/wt/src", want: flagged},
		{name: "submodule in the tracker's repo", gitDirs: []string{"project"}, gitFiles: []string{"project/vendor/sub"}, from: "project/vendor/sub/src", want: flagged},
		{name: "no git at all", from: "project/nested", want: flagged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "project")
			from := filepath.Join(base, tc.from)
			if err := os.MkdirAll(from, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			mkBeadsDir(t, root)
			for _, dir := range tc.gitDirs {
				if err := os.MkdirAll(filepath.Join(base, dir, ".git"), 0o755); err != nil {
					t.Fatalf("mkdir .git: %v", err)
				}
			}
			for _, dir := range tc.gitFiles {
				path := filepath.Join(base, dir)
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
					t.Fatalf("write .git file: %v", err)
				}
			}

			if got := strings.Join(BeadsReadArgs(from, []string{"stats", "--json"}), " "); got != tc.want {
				t.Errorf("read for the literal directory = %q, want %q", got, tc.want)
			}
			t.Chdir(from)
			if got := strings.Join(BeadsReadArgs(root, []string{"stats", "--json"}), " "); got != tc.want {
				t.Errorf("read for the resolved tracker root = %q, want %q", got, tc.want)
			}
		})
	}
}

// A tracker found by walking up must get the read-only flags even though
// normalization resolves the command's directory to that tracker's root.
func TestRunBdWalkUpReadReachesBrWithReadOnlyFlags(t *testing.T) {
	parent := t.TempDir()
	mkBeadsDir(t, parent)
	child := filepath.Join(parent, "project", "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}

	binDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + calls + "\"\nprintf '[]\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := RunBdContext(context.Background(), child, "ready", "--json"); err != nil {
		t.Fatalf("walk-up read: %v", err)
	}
	if _, err := RunBdContext(context.Background(), parent, "ready", "--json"); err != nil {
		t.Fatalf("local read: %v", err)
	}
	if _, err := RunBdContext(context.Background(), child, "close", "bd-1"); err != nil {
		t.Fatalf("walk-up write: %v", err)
	}

	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	want := "--lock-timeout 5000 ready --json --no-auto-import --no-auto-flush\n" +
		"--lock-timeout 5000 ready --json\n" +
		"--lock-timeout 5000 close bd-1\n"
	if string(data) != want {
		t.Fatalf("br calls:\n%s\nwant:\n%s", data, want)
	}
}
