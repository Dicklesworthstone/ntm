package util

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFindGitRootCachesPerDirectory proves the bd-6afgy TTL cache: repeated
// FindGitRoot calls for the same start directory spawn `git rev-parse` once,
// and distinct directories get distinct cache entries.
func TestFindGitRootCachesPerDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim uses a shell script")
	}

	shimDir := t.TempDir()
	countFile := filepath.Join(shimDir, "count")
	script := "#!/bin/sh\necho x >> " + countFile + "\necho /fake/root\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir)

	resetGitRootCache := func() {
		gitRootCache.Lock()
		gitRootCache.entries = make(map[string]gitRootCacheEntry)
		gitRootCache.Unlock()
	}
	resetGitRootCache()
	t.Cleanup(resetGitRootCache)

	dirA := t.TempDir()
	dirB := t.TempDir()

	invocations := func() int {
		data, err := os.ReadFile(countFile)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "x")
	}

	for i := 0; i < 3; i++ {
		root, err := FindGitRoot(dirA)
		if err != nil {
			t.Fatalf("FindGitRoot(dirA) call %d error: %v", i, err)
		}
		if root != "/fake/root" {
			t.Fatalf("FindGitRoot(dirA) = %q, want /fake/root", root)
		}
	}
	if got := invocations(); got != 1 {
		t.Fatalf("git spawned %d times for one directory, want 1 (cached)", got)
	}

	if _, err := FindGitRoot(dirB); err != nil {
		t.Fatalf("FindGitRoot(dirB) error: %v", err)
	}
	if got := invocations(); got != 2 {
		t.Fatalf("git spawned %d times for two directories, want 2", got)
	}
}
