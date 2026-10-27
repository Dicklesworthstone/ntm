package bv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func sourceBoundTriageFixture(t *testing.T) (dir, tracker, calls string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bv fixture uses a POSIX shell")
	}
	dir = t.TempDir()
	tracker = filepath.Join(dir, ".beads", "issues.jsonl")
	calls = filepath.Join(t.TempDir(), "calls")
	if err := os.MkdirAll(filepath.Dir(tracker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracker, []byte(`{"id":"old","status":"open"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
echo run >> "$NTM_TRIAGE_SOURCE_CALLS"
printf '{"triage":{"recommendations":['
cat "$NTM_TRIAGE_SOURCE_TRACKER"
printf ']}}\n'
if [ "${NTM_TRIAGE_SOURCE_MUTATE:-}" = 1 ]; then
  printf '{"id":"new","status":"open"}\n' > "$NTM_TRIAGE_SOURCE_TRACKER"
fi
`
	if err := os.WriteFile(filepath.Join(bin, "bv"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_TRIAGE_SOURCE_TRACKER", tracker)
	t.Setenv("NTM_TRIAGE_SOURCE_CALLS", calls)
	t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "")
	InvalidateTriageCache()
	t.Cleanup(InvalidateTriageCache)
	return dir, tracker, calls
}

func assertSourceTriageID(t *testing.T, dir, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := GetTriageContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Triage.Recommendations) != 1 || got.Triage.Recommendations[0].ID != id {
		t.Fatalf("triage = %+v, want %s", got, id)
	}
}

func sourceTriageCalls(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run\n")
}

func TestTriageSourceCacheInvalidatesBeforeTTL(t *testing.T) {
	dir, tracker, calls := sourceBoundTriageFixture(t)
	assertSourceTriageID(t, dir, "old")
	assertSourceTriageID(t, dir, "old")
	if n := sourceTriageCalls(t, calls); n != 1 {
		t.Fatalf("unchanged source ran bv %d times", n)
	}
	info, err := os.Stat(tracker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracker, []byte(`{"id":"new","status":"open"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tracker, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if IsCacheValid() {
		t.Fatal("TTL accepted a different tracker digest")
	}
	assertSourceTriageID(t, dir, "new")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("changed source ran bv %d times, want 2", n)
	}
}

func TestTriageSourceDriftDuringExecutionFailsClosed(t *testing.T) {
	dir, _, calls := sourceBoundTriageFixture(t)
	t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "1")
	got, err := GetTriageContext(context.Background(), dir)
	if got != nil || !errors.Is(err, ErrStaleWorkCoordination) || !errors.Is(err, worksource.ErrSourceChanged) {
		t.Fatalf("mixed-source result=%+v error=%v", got, err)
	}
	if IsCacheValid() {
		t.Fatal("mixed-source result was cached")
	}
	t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "")
	assertSourceTriageID(t, dir, "new")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("explicit retry ran bv %d times", n)
	}
}

func TestActionableSourceDriftPreservesTransientCause(t *testing.T) {
	dir, _, _ := sourceBoundTriageFixture(t)
	t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "1")
	got, err := GetActionableRecommendationsContext(context.Background(), dir, 0)
	if got != nil || !errors.Is(err, ErrActionablePlanUnverified) ||
		!errors.Is(err, ErrStaleWorkCoordination) || !errors.Is(err, worksource.ErrSourceChanged) {
		t.Fatalf("actionable mixed-source result=%+v error=%v; want wrapped transient plan failure", got, err)
	}
	if IsCacheValid() {
		t.Fatal("mixed-source actionable result was cached")
	}
}

func TestExpireTriageCacheRefetchesSameSource(t *testing.T) {
	dir, _, calls := sourceBoundTriageFixture(t)
	assertSourceTriageID(t, dir, "old")
	assertSourceTriageID(t, dir, "old")
	if n := sourceTriageCalls(t, calls); n != 1 {
		t.Fatalf("unchanged source ran bv %d times before expiration", n)
	}
	ExpireTriageCache()
	if IsCacheValid() {
		t.Fatal("expired cache remained valid")
	}
	assertSourceTriageID(t, dir, "old")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("expired source ran bv %d times, want fresh tool read", n)
	}
	if !IsCacheValid() {
		t.Fatal("fresh source did not restore cache validity")
	}
}

func TestTriageSourceDisappearanceDoesNotDowngradeToUnboundCache(t *testing.T) {
	dir, tracker, calls := sourceBoundTriageFixture(t)
	assertSourceTriageID(t, dir, "old")
	if err := os.Rename(tracker, tracker+".saved"); err != nil {
		t.Fatal(err)
	}
	got, err := GetTriageContext(context.Background(), dir)
	if got != nil || !errors.Is(err, ErrStaleWorkCoordination) {
		t.Fatalf("missing tracker result=%+v error=%v", got, err)
	}
	if IsCacheValid() {
		t.Fatal("missing tracker left a valid cache")
	}
	if n := sourceTriageCalls(t, calls); n != 1 {
		t.Fatalf("missing source attempted %d bv calls", n)
	}
	// A watcher refreshing after drift must retain this source-binding guard.
	ExpireTriageCache()
	got, err = GetTriageContext(context.Background(), dir)
	if got != nil || !errors.Is(err, ErrStaleWorkCoordination) || errors.Is(err, worksource.ErrSourceChanged) {
		t.Fatalf("expiration lost missing-source guard: result=%+v error=%v", got, err)
	}
	if n := sourceTriageCalls(t, calls); n != 1 {
		t.Fatalf("expired missing source attempted %d bv calls", n)
	}
	if err := os.Rename(tracker+".saved", tracker); err != nil {
		t.Fatal(err)
	}
	assertSourceTriageID(t, dir, "old")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("restored source ran bv %d times, want fresh tool read", n)
	}
}

func TestTriageSourceFailedFirstReadRetainsBinding(t *testing.T) {
	for _, phase := range []string{"triage", "capture"} {
		t.Run(phase, func(t *testing.T) {
			dir, tracker, calls := sourceBoundTriageFixture(t)
			alias := filepath.Join(t.TempDir(), "project-alias")
			if err := os.Symlink(dir, alias); err != nil {
				t.Skipf("project symlink unavailable: %v", err)
			}
			wantCalls := 1
			if phase == "triage" {
				t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "1")
			} else {
				// Replace the export on the first Git probe, before Capture
				// can return a bound identity or any bv result can be cached.
				wantCalls = 0
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				script := `#!/bin/sh
set -eu
if [ ! -e "$NTM_TRIAGE_CAPTURE_CHANGED" ]; then
  : > "$NTM_TRIAGE_CAPTURE_CHANGED"
  mv "$NTM_TRIAGE_SOURCE_TRACKER" "$NTM_TRIAGE_SOURCE_TRACKER.original"
  printf '{"id":"new","status":"open"}\n' > "$NTM_TRIAGE_SOURCE_TRACKER"
fi
printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
`
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("NTM_TRIAGE_CAPTURE_CHANGED", filepath.Join(t.TempDir(), "changed"))
			}
			got, err := GetTriageContext(context.Background(), alias)
			if got != nil || !errors.Is(err, worksource.ErrSourceChanged) {
				t.Fatalf("first read result=%+v error=%v; want source drift", got, err)
			}
			if IsCacheValid() || sourceTriageCalls(t, calls) != wantCalls {
				t.Fatalf("failed first read cached data or made unexpected tool calls: %d", sourceTriageCalls(t, calls))
			}
			saved := tracker + ".after-stale"
			if err := os.Rename(tracker, saved); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TRIAGE_SOURCE_MUTATE", "")
			// The fake tool can still return DB-only data if called. The
			// source guard, not a missing-input tool error, must refuse it.
			t.Setenv("NTM_TRIAGE_SOURCE_TRACKER", saved)
			for _, refresh := range []struct {
				name string
				run  func()
			}{{"expire", ExpireTriageCache}, {"invalidate", InvalidateTriageCache}} {
				refresh.run()
				for _, project := range []string{dir, alias} {
					got, err = GetTriageContext(context.Background(), project)
					if got != nil || !errors.Is(err, ErrStaleWorkCoordination) || errors.Is(err, worksource.ErrSourceChanged) {
						t.Fatalf("%s lost binding for %s: result=%+v error=%v", refresh.name, project, got, err)
					}
					if n := sourceTriageCalls(t, calls); n != wantCalls {
						t.Fatalf("%s invoked bv after losing the source: calls=%d, want %d", refresh.name, n, wantCalls)
					}
				}
			}
			if err := os.Rename(saved, tracker); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TRIAGE_SOURCE_TRACKER", tracker)
			assertSourceTriageID(t, dir, "new")
			if n := sourceTriageCalls(t, calls); n != wantCalls+1 {
				t.Fatalf("restored source ran bv %d times, want %d", n, wantCalls+1)
			}
		})
	}
}

func TestTriageSourceBindingSurvivesOtherProjects(t *testing.T) {
	dir, tracker, calls := sourceBoundTriageFixture(t)
	assertSourceTriageID(t, dir, "old")
	// A genuinely DB-only project remains supported and replaces the shared
	// response cache without erasing another project's observed binding.
	dbOnly := t.TempDir()
	if err := os.Mkdir(filepath.Join(dbOnly, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertSourceTriageID(t, dbOnly, "old")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("two project reads ran bv %d times, want 2", n)
	}
	saved := tracker + ".saved"
	if err := os.Rename(tracker, saved); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TRIAGE_SOURCE_TRACKER", saved)
	got, err := GetTriageContext(context.Background(), dir)
	if got != nil || !errors.Is(err, ErrStaleWorkCoordination) || errors.Is(err, worksource.ErrSourceChanged) {
		t.Fatalf("other project erased source binding: result=%+v error=%v", got, err)
	}
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("lost source reached bv after cache eviction: calls=%d", n)
	}
	ExpireTriageCache()
	InvalidateTriageCache()
	assertSourceTriageID(t, dbOnly, "old")
	if n := sourceTriageCalls(t, calls); n != 3 {
		t.Fatalf("DB-only project did not refresh independently: calls=%d", n)
	}
}

func TestTriageSourceConcurrentReadersUseOneRunner(t *testing.T) {
	dir, _, calls := sourceBoundTriageFixture(t)
	const readers = 16
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := GetTriageContext(ctx, dir)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := sourceTriageCalls(t, calls); n != 1 {
		t.Fatalf("same-source readers ran bv %d times", n)
	}
}

func TestTriageSourceHeadOnlyChangeInvalidatesCache(t *testing.T) {
	dir, _, calls := sourceBoundTriageFixture(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=NTM Test", "GIT_COMMITTER_NAME=NTM Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "--quiet")
	git("add", ".beads/issues.jsonl")
	git("-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "initial")
	assertSourceTriageID(t, dir, "old")
	git("-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "new HEAD")
	if IsCacheValid() {
		t.Fatal("HEAD-only change left cache valid")
	}
	assertSourceTriageID(t, dir, "old")
	if n := sourceTriageCalls(t, calls); n != 2 {
		t.Fatalf("HEAD-only change ran bv %d times", n)
	}
}
