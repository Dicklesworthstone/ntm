package context

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildRefreshesEditedSourceAndGlobMatches(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	opts := BuildOptions{AgentType: "cod", RepoRev: "unchanged", ProjectDir: dir, Files: []string{"*.go"}}
	first, err := b.Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// Same path, revision, size, and modification time; only content changes.
	if err := os.WriteFile(file, []byte("package later\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	second, err := b.Build(context.Background(), opts)
	if err != nil || second == first || !strings.Contains(second.RenderedPrompt, "package later") || strings.Contains(second.RenderedPrompt, "package first") {
		t.Fatalf("source edit reused stale context: err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.go"), []byte("// newly-matched-source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := b.Build(context.Background(), opts)
	if err != nil || !strings.Contains(third.RenderedPrompt, "newly-matched-source") {
		t.Fatalf("new glob match omitted: err=%v", err)
	}
	if size, _ := b.CacheStats(); size != 0 {
		t.Fatal("mutable source packs were cached without content fingerprints")
	}
}

func TestBuildRecoversWhenSelectedFileAppears(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	dir := t.TempDir()
	opts := BuildOptions{AgentType: "cod", ProjectDir: dir, Files: []string{"created.go"}}
	first, err := b.Build(context.Background(), opts)
	if err != nil || first.Components["s2p"].Error == "" {
		t.Fatalf("expected unavailable missing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "created.go"), []byte("// source-now-available"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := b.Build(context.Background(), opts)
	if err != nil || second.Components["s2p"].Error != "" || !strings.Contains(second.RenderedPrompt, "source-now-available") {
		t.Fatalf("cached failure hid new file: %v", err)
	}
}

func TestBuildCacheSeparatesWorkingDirectories(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	firstDir, secondDir := t.TempDir(), t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chdir(firstDir); err != nil {
		t.Fatal(err)
	}
	opts := BuildOptions{AgentType: "cod", RepoRev: "same-revision"}
	first, err := b.Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(secondDir); err != nil {
		t.Fatal(err)
	}
	second, err := b.Build(context.Background(), opts)
	if err != nil || second == first {
		t.Fatalf("relative project path reused another project's pack: %v", err)
	}
	opts.ProjectDir = firstDir
	again, err := b.Build(context.Background(), opts)
	if err != nil || again != first {
		t.Fatalf("absolute and resolved project paths did not share the correct cache: %v", err)
	}
}

// Trigger cancellation exactly at the second observation without timers or
// sleeps. Done and Err retain the ordinary cancellation contract.
type cancelOnSecondContextCheck struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *cancelOnSecondContextCheck) Err() error {
	if c.checks.Add(1) == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestBuildDoesNotPublishCancelledAssembly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelOnSecondContextCheck{Context: base, cancel: cancel}
	pack, err := b.Build(ctx, BuildOptions{AgentType: "cod", ProjectDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) || pack != nil {
		t.Fatalf("cancelled assembly returned success: pack=%v err=%v", pack != nil, err)
	}
	if size, _ := b.CacheStats(); size != 0 {
		t.Fatal("cancelled assembly entered the cache")
	}
}

func TestBuildCancellationTakesPrecedenceOverCache(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewContextPackBuilder(nil)
	b.ClearCache()
	t.Cleanup(b.ClearCache)
	opts := BuildOptions{AgentType: "cod", ProjectDir: t.TempDir()}
	if _, err := b.Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelOnSecondContextCheck{Context: base, cancel: cancel}
	if pack, err := b.Build(ctx, opts); !errors.Is(err, context.Canceled) || pack != nil {
		t.Fatalf("cache hit hid cancellation: pack=%v err=%v", pack != nil, err)
	}
	if pack, err := b.Build(base, opts); !errors.Is(err, context.Canceled) || pack != nil {
		t.Fatalf("pre-cancelled request returned cache: pack=%v err=%v", pack != nil, err)
	}
}
