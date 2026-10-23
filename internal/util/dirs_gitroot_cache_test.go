package util

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestResolveProjectDirContextCancelsHungGitAndRetries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim uses a shell script")
	}
	shimDir, projectDir := t.TempDir(), t.TempDir()
	startDir := filepath.Join(projectDir, "component")
	if err := os.Mkdir(startDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(shimDir, "calls")
	script := `#!/bin/sh
printf 'lookup\n' >> "$NTM_TEST_PROJECT_ROOT_LOG"
if [ "$NTM_TEST_PROJECT_ROOT_HANG" = 1 ]; then exec sleep 30; fi
printf '%s\n' "$NTM_TEST_PROJECT_ROOT_RESULT"
`
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_TEST_PROJECT_ROOT_LOG", logPath)
	t.Setenv("NTM_TEST_PROJECT_ROOT_HANG", "1")
	t.Setenv("NTM_TEST_PROJECT_ROOT_RESULT", projectDir)

	type outcome struct {
		root string
		err  error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan outcome, 1)
	go func() {
		root, err := ResolveProjectDirContext(ctx, startDir)
		result <- outcome{root: root, err: err}
	}()
	// Cancel only after the external discovery has started, so this proves
	// an in-flight lookup releases its caller rather than only preflight.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(logPath); err == nil && len(data) > 0 {
			break
		}
		select {
		case got := <-result:
			t.Fatalf("discovery finished before hung Git started: %+v", got)
		case <-ctx.Done():
			t.Fatal("hung Git did not start before the fixture deadline")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case got := <-result:
		if got.root != "" || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled lookup chose a project fallback: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled lookup retained its background Git timeout")
	}

	// A fresh request must discover the actual root, and ordinary callers
	// must then reuse that same cache rather than spawn another Git command.
	t.Setenv("NTM_TEST_PROJECT_ROOT_HANG", "0")
	root, err := ResolveProjectDirContext(context.Background(), startDir)
	if err != nil || root != projectDir {
		t.Fatalf("cancelled discovery poisoned retry: root=%q, err=%v", root, err)
	}
	if cached, err := FindGitRoot(startDir); err != nil || cached != projectDir {
		t.Fatalf("ordinary discovery did not share the successful cache: root=%q, err=%v", cached, err)
	}
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if root, err := ResolveProjectDirContext(expired, startDir); root != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired request accepted a cached project: root=%q, err=%v", root, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(data), "lookup\n") != 2 {
		t.Fatalf("unexpected discovery calls after cancellation/retry/cache: %q, %v", data, err)
	}
}
