package robot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// resetSharedReservationCaches empties the process-wide reservation cache
// registry so tests that wire affinity against the same resolved project key
// cannot leak fetched reservations into each other.
func resetSharedReservationCaches(t *testing.T) {
	t.Helper()
	reset := func() {
		sharedReservationCachesMu.Lock()
		sharedReservationCaches = make(map[string]*ReservationCache)
		sharedReservationCachesMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// TestSharedReservationCache_TTLAmortizes pins the bd-2rtl8 cache fix: the
// reservation cache for a project key is process-shared, so a fetch performed
// by one GetRoute/GetRouteRecommendation call amortizes across subsequent
// calls for the configured TTL. The old code built a fresh cache per call
// (lastFetch zero => NeedsRefresh always true), so the "30s TTL" never
// avoided a single blocking Agent Mail round-trip.
func TestSharedReservationCache_TTLAmortizes(t *testing.T) {
	key := filepath.Join(t.TempDir(), "projA")

	rc1 := sharedReservationCache(nil, key, 30*time.Second)
	if !rc1.NeedsRefresh() {
		t.Fatal("brand-new cache must need a refresh")
	}

	// Simulate invocation 1 completing a fetch.
	rc1.mu.Lock()
	rc1.lastFetch = time.Now()
	rc1.mu.Unlock()

	// Invocation 2 (fresh scorer, same project) must get the SAME cache and
	// see the TTL still warm.
	rc2 := sharedReservationCache(nil, key, 30*time.Second)
	if rc2 != rc1 {
		t.Fatal("same project key returned a different cache instance; TTL cannot amortize")
	}
	if rc2.NeedsRefresh() {
		t.Fatal("warm shared cache reports NeedsRefresh; every send would pay a blocking fetch")
	}

	// A different project key is isolated.
	other := sharedReservationCache(nil, filepath.Join(t.TempDir(), "projB"), 30*time.Second)
	if other == rc1 {
		t.Fatal("distinct project keys share one cache")
	}
	if !other.NeedsRefresh() {
		t.Fatal("new project's cache must start cold")
	}

	// A reconfigured TTL takes effect on the existing cache.
	rc3 := sharedReservationCache(nil, key, time.Nanosecond)
	time.Sleep(2 * time.Nanosecond)
	if rc3 != rc1 {
		t.Fatal("TTL change must not discard the shared cache")
	}
	if !rc3.NeedsRefresh() {
		t.Fatal("shrunken TTL must expire the cache")
	}
}

// TestAffinityProjectKeyPreference pins the strict session-first precedence
// (bd-2rtl8): the first USABLE candidate wins, and unusable candidates
// (empty, nonexistent) are skipped rather than resolved to garbage.
func TestAffinityProjectKeyPreference(t *testing.T) {
	existing1 := t.TempDir()
	existing2 := t.TempDir()
	missing := filepath.Join(existing1, "does-not-exist")

	if got := affinityProjectKeyPreference(existing1, existing2); got != existing1 {
		t.Fatalf("preference = %q, want first usable %q", got, existing1)
	}
	if got := affinityProjectKeyPreference("", missing, existing2); got != existing2 {
		t.Fatalf("preference = %q, want %q (skip empty and nonexistent)", got, existing2)
	}
	if got := affinityProjectKeyPreference("", missing); got != "" {
		t.Fatalf("preference = %q, want empty when nothing is usable", got)
	}
}

// TestResolveAffinityProjectKey_SessionFirst pins the bd-2rtl8 project-key
// fix: wireReservationAffinity must key Agent Mail queries session-first (the
// session's configured project dir when it exists) instead of blindly using
// the invoker's cwd — an orchestrator sending from outside the repo
// previously queried reservations for the WRONG project.
func TestResolveAffinityProjectKey_SessionFirst(t *testing.T) {
	base := t.TempDir()
	session := "ntm-affinity-key-test-8f3a1"
	sessionDir := filepath.Join(base, session)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	cfg := config.Default()
	cfg.ProjectsBase = base

	got := resolveAffinityProjectKey(cfg, session)
	if got != sessionDir {
		t.Fatalf("resolveAffinityProjectKey = %q, want session dir %q (cwd was %q)",
			got, sessionDir, util.ResolveProjectDir(""))
	}

	// Without a usable session dir, resolution falls back to the invoker's
	// project root — the pre-fix behavior, now the LAST resort.
	gotFallback := resolveAffinityProjectKey(cfg, "ntm-affinity-key-test-no-such-sess-8f3a1")
	if want := util.ResolveProjectDir(""); gotFallback != want {
		t.Fatalf("fallback = %q, want cwd project root %q", gotFallback, want)
	}
}
