package ensemble

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestCache_HitMiss(t *testing.T) {
	input := map[string]any{"mode": "deductive"}
	logTestStartCache(t, input)

	cache, err := NewModeOutputCacheWithDir(t.TempDir(), DefaultModeOutputCacheConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	assertNoErrorCache(t, "new cache", err)

	mode := &ReasoningMode{ID: "deductive", Code: "A1", Category: CategoryFormal, Tier: TierCore}
	fp, err := BuildModeOutputFingerprint("hash", mode, ModeOutputConfig{Question: "Q"})
	assertNoErrorCache(t, "build fingerprint", err)

	output := &ModeOutput{ModeID: "deductive", Thesis: "Cached output"}
	assertNoErrorCache(t, "put cache", cache.Put(fp, output))

	hit := cache.Lookup(fp)
	logTestResultCache(t, hit)
	assertTrueCache(t, "cache hit", hit.Hit)

	miss := cache.Lookup(ModeOutputFingerprint{ContextHash: "other", ModeID: "deductive", ModeVersion: fp.ModeVersion, ConfigHash: fp.ConfigHash})
	logTestResultCache(t, miss)
	assertTrueCache(t, "cache miss", !miss.Hit)
}

func TestCache_DiskPersistence(t *testing.T) {
	input := map[string]any{"mode": "deductive"}
	logTestStartCache(t, input)

	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Hour, MaxEntries: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assertNoErrorCache(t, "new cache", err)

	mode := &ReasoningMode{ID: "deductive", Code: "A1", Category: CategoryFormal, Tier: TierCore}
	fp, err := BuildModeOutputFingerprint("hash", mode, ModeOutputConfig{Question: "Q"})
	assertNoErrorCache(t, "build fingerprint", err)
	assertNoErrorCache(t, "put cache", cache.Put(fp, &ModeOutput{ModeID: "deductive", Thesis: "Persisted"}))

	fresh, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Hour, MaxEntries: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assertNoErrorCache(t, "new cache instance", err)

	lookup := fresh.Lookup(fp)
	logTestResultCache(t, lookup)
	assertTrueCache(t, "disk hit", lookup.Hit)
	assertEqualCache(t, "hit reason", lookup.Reason, "disk")
}

func logTestStartCache(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultCache(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertNoErrorCache(t *testing.T, desc string, err error) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
}

func assertTrueCache(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualCache(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}
