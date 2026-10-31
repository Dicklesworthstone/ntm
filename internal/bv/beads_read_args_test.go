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
