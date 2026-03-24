package ensemble

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextCache_GetPut_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewContextPackCacheWithDir(dir, CacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	key := "abc123"
	pack := &ContextPack{
		Hash: "hash",
		ProjectBrief: &ProjectBrief{
			Name: "demo",
		},
	}
	fp := ContextFingerprint{ProjectRoot: "/tmp/demo", GitHead: "deadbeef"}

	if err := cache.Put(key, pack, fp); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	cache2, err := NewContextPackCacheWithDir(dir, CacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init 2: %v", err)
	}

	got, ok := cache2.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got == nil || got.ProjectBrief == nil || got.ProjectBrief.Name != "demo" {
		t.Fatalf("unexpected cache value: %#v", got)
	}
}

func TestContextCache_Get_CacheMiss(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewContextPackCacheWithDir(dir, CacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	if _, ok := cache.Get("missing"); ok {
		t.Fatal("expected cache miss")
	}
}

func TestContextCache_Get_Expired(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewContextPackCacheWithDir(dir, CacheConfig{Enabled: true, TTL: 10 * time.Millisecond, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	key := "expire"
	pack := &ContextPack{Hash: "hash"}
	fp := ContextFingerprint{ProjectRoot: "/tmp/demo"}
	if err := cache.Put(key, pack, fp); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	if _, ok := cache.Get(key); ok {
		t.Fatal("expected cache entry to expire")
	}

	if _, err := os.Stat(filepath.Join(dir, key+".json")); err == nil {
		t.Fatal("expected expired cache file to be removed")
	}
}

func TestContextCache_Prune(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewContextPackCacheWithDir(dir, CacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 2}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	fp := ContextFingerprint{ProjectRoot: "/tmp/demo"}
	if err := cache.Put("k1", &ContextPack{Hash: "1"}, fp); err != nil {
		t.Fatalf("cache put k1: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("k2", &ContextPack{Hash: "2"}, fp); err != nil {
		t.Fatalf("cache put k2: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := cache.Put("k3", &ContextPack{Hash: "3"}, fp); err != nil {
		t.Fatalf("cache put k3: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("expected <=2 cache files, found %d", count)
	}
}

func TestContextFingerprint_CacheKey_Deterministic(t *testing.T) {
	fp := ContextFingerprint{
		ProjectRoot:  "/tmp/demo",
		GitHead:      "deadbeef",
		ReadmeHash:   "abc",
		QuestionHash: "q1",
		ModeKey:      "mode-a",
	}

	key1 := fp.cacheKey()
	key2 := fp.cacheKey()
	if key1 != key2 {
		t.Fatalf("cacheKey not deterministic: %s vs %s", key1, key2)
	}

	fp2 := fp
	fp2.GitHead = "beadfeed"
	if fp2.cacheKey() == key1 {
		t.Fatal("expected cacheKey to change when fingerprint changes")
	}
}

func TestContextFingerprint_FallbackCacheKeyMaterial_UsesAllFields(t *testing.T) {
	fp := ContextFingerprint{
		ProjectRoot:  "/tmp/demo",
		GitHead:      "deadbeef",
		GitStatus:    "dirty",
		ReadmeHash:   "readme",
		QuestionHash: "question",
		ModeKey:      "mode-a",
	}

	got := fp.fallbackCacheKeyMaterial()
	want := "/tmp/demo:deadbeef:dirty:readme:question:mode-a"
	if got != want {
		t.Fatalf("fallbackCacheKeyMaterial() = %q, want %q", got, want)
	}
}

func TestNewContextPackCache_DefaultDirAndLogger(t *testing.T) {
	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)

	cache, err := NewContextPackCache(CacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 1}, nil)
	if err != nil {
		t.Fatalf("NewContextPackCache error: %v", err)
	}

	if cache == nil {
		t.Fatal("expected cache")
	}
	if cache.dir == "" {
		t.Fatal("expected cache dir to be set")
	}
	if cache.loggerSafe() == nil {
		t.Fatal("expected loggerSafe to return a logger")
	}

	// Coverage: ensure non-nil receiver returns configured logger.
	cache.logger = slog.Default()
	if cache.loggerSafe() != slog.Default() {
		t.Fatal("expected loggerSafe to return configured logger")
	}
}

func TestDefaultContextCacheDir_UsesXDGCacheHome(t *testing.T) {
	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)

	got, err := defaultContextCacheDir()
	if err != nil {
		t.Fatalf("defaultContextCacheDir error: %v", err)
	}
	want := filepath.Join(cacheBase, "ntm", "context-packs")
	if got != want {
		t.Fatalf("defaultContextCacheDir = %q, want %q", got, want)
	}
}

func TestContextPackCache_loggerSafe_NilReceiver(t *testing.T) {
	var cache *ContextPackCache
	if cache.loggerSafe() == nil {
		t.Fatal("expected loggerSafe to return slog.Default for nil receiver")
	}
}
