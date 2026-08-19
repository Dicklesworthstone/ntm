package ensemble

import (
	"os"
	"testing"
	"time"
)

func TestModeOutputCache_PutGet(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	mode := sampleMode(t)
	cfg := ModeOutputConfig{Question: "cache question", AgentType: "cc", SchemaVersion: SchemaVersion}
	fingerprint, err := BuildModeOutputFingerprint("context-hash", mode, cfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	output := &ModeOutput{
		ModeID: mode.ID,
		Thesis: "Cached output",
		TopFindings: []Finding{{
			Finding:    "Cache hit",
			Impact:     ImpactLow,
			Confidence: 0.5,
		}},
		Confidence:  0.6,
		GeneratedAt: time.Now().UTC(),
	}

	if err := cache.Put(fingerprint, output); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	lookup := cache.Lookup(fingerprint)
	if !lookup.Hit {
		t.Fatalf("expected cache hit, got miss (%s)", lookup.Reason)
	}
	if lookup.Output == nil || lookup.Output.ModeID != mode.ID {
		t.Fatalf("unexpected cached output: %#v", lookup.Output)
	}
}

func TestModeOutputCache_PersistsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	mode := sampleMode(t)
	cfg := ModeOutputConfig{Question: "persist", AgentType: "cc", SchemaVersion: SchemaVersion}
	fingerprint, err := BuildModeOutputFingerprint("context-hash", mode, cfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	output := &ModeOutput{
		ModeID: mode.ID,
		Thesis: "Persisted output",
		TopFindings: []Finding{{
			Finding:    "Persist",
			Impact:     ImpactLow,
			Confidence: 0.5,
		}},
		Confidence:  0.7,
		GeneratedAt: time.Now().UTC(),
	}

	cache1, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init 1: %v", err)
	}
	if err := cache1.Put(fingerprint, output); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	cache2, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init 2: %v", err)
	}
	lookup := cache2.Lookup(fingerprint)
	if !lookup.Hit {
		t.Fatalf("expected cache hit after reload, got miss (%s)", lookup.Reason)
	}
}

func TestModeOutputCache_Expires(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: 10 * time.Millisecond, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	mode := sampleMode(t)
	cfg := ModeOutputConfig{Question: "expire", AgentType: "cc", SchemaVersion: SchemaVersion}
	fingerprint, err := BuildModeOutputFingerprint("context-hash", mode, cfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	output := &ModeOutput{
		ModeID: mode.ID,
		Thesis: "Expired",
		TopFindings: []Finding{{
			Finding:    "Expire",
			Impact:     ImpactLow,
			Confidence: 0.5,
		}},
		Confidence:  0.6,
		GeneratedAt: time.Now().UTC(),
	}
	if err := cache.Put(fingerprint, output); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	lookup := cache.Lookup(fingerprint)
	if lookup.Hit {
		t.Fatal("expected cache entry to expire")
	}

	if _, err := os.Stat(cache.filePath(fingerprint.CacheKey())); !os.IsNotExist(err) {
		t.Fatalf("expected cache file removed, err=%v", err)
	}
}

func TestModeOutputCache_InvalidationReason_ConfigMismatch(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 5}, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	mode := sampleMode(t)
	cfg := ModeOutputConfig{Question: "config", AgentType: "cc", SchemaVersion: SchemaVersion}
	fingerprint, err := BuildModeOutputFingerprint("context-hash", mode, cfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	output := &ModeOutput{
		ModeID: mode.ID,
		Thesis: "Config mismatch",
		TopFindings: []Finding{{
			Finding:    "Config",
			Impact:     ImpactLow,
			Confidence: 0.5,
		}},
		Confidence:  0.6,
		GeneratedAt: time.Now().UTC(),
	}
	if err := cache.Put(fingerprint, output); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	altCfg := ModeOutputConfig{Question: "config", AgentType: "cod", SchemaVersion: SchemaVersion}
	altFingerprint, err := BuildModeOutputFingerprint("context-hash", mode, altCfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	lookup := cache.Lookup(altFingerprint)
	if lookup.Hit {
		t.Fatal("expected cache miss for config mismatch")
	}
	if lookup.Reason != "config_mismatch" {
		t.Fatalf("expected config_mismatch, got %s", lookup.Reason)
	}
}

func sampleMode(t *testing.T) *ReasoningMode {
	t.Helper()
	catalog, err := LoadModeCatalog()
	if err != nil {
		t.Fatalf("load mode catalog: %v", err)
	}
	modes := catalog.ListModes()
	if len(modes) == 0 {
		t.Fatal("no modes available")
	}
	mode := modes[0]
	return &mode
}

func TestNewModeOutputCache_EmptyDir(t *testing.T) {
	t.Parallel()
	_, err := NewModeOutputCacheWithDir("", ModeOutputCacheConfig{Enabled: true}, nil)
	if err == nil {
		t.Error("expected error for empty dir")
	}
}

func TestNewModeOutputCache_DefaultTTLAndMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCacheWithDir: %v", err)
	}
	if cache.ttl != defaultOutputCacheTTL {
		t.Errorf("ttl = %v, want %v", cache.ttl, defaultOutputCacheTTL)
	}
	if cache.maxEntries != defaultOutputCacheMax {
		t.Errorf("maxEntries = %d, want %d", cache.maxEntries, defaultOutputCacheMax)
	}
}

func TestDefaultModeOutputCacheDir(t *testing.T) {
	t.Parallel()
	dir := defaultModeOutputCacheDir("/tmp/myproject")
	expected := "/tmp/myproject/.ntm/ensemble-cache"
	if dir != expected {
		t.Errorf("defaultModeOutputCacheDir = %q, want %q", dir, expected)
	}

	// Empty project dir should use cwd
	dir = defaultModeOutputCacheDir("")
	if dir == "" {
		t.Error("defaultModeOutputCacheDir with empty should not be empty")
	}
}

func TestModeOutputCache_LoggerSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// nil logger should not panic
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCacheWithDir: %v", err)
	}
	logger := cache.loggerSafe()
	if logger == nil {
		t.Error("loggerSafe should return non-nil logger")
	}

	// nil receiver should also be safe
	var nilCache *ModeOutputCache
	logger = nilCache.loggerSafe()
	if logger == nil {
		t.Error("loggerSafe on nil receiver should return default logger")
	}
}

func TestModeOutputCache_Clear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 10}, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCacheWithDir: %v", err)
	}

	mode := sampleMode(t)

	// Put 3 entries
	for i := 0; i < 3; i++ {
		cfg := ModeOutputConfig{Question: "q" + string(rune('0'+i)), AgentType: "cc", SchemaVersion: SchemaVersion}
		fp, _ := BuildModeOutputFingerprint("ctx", mode, cfg)
		_ = cache.Put(fp, &ModeOutput{ModeID: mode.ID, Thesis: "t", Confidence: 0.5, GeneratedAt: time.Now()})
	}

	removed := cache.Clear()
	if removed != 3 {
		t.Errorf("Clear() = %d, want 3", removed)
	}

	// After clear, directory should be empty (or only dirs)
	entries, _ := os.ReadDir(dir)
	fileCount := 0
	for _, e := range entries {
		if !e.IsDir() {
			fileCount++
		}
	}
	if fileCount != 0 {
		t.Errorf("expected 0 files after clear, got %d", fileCount)
	}
}

func TestModeOutputCache_Clear_Nil(t *testing.T) {
	t.Parallel()
	var nilCache *ModeOutputCache
	removed := nilCache.Clear()
	if removed != 0 {
		t.Errorf("Clear on nil = %d, want 0", removed)
	}
}

func TestModeOutputCache_Stats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cache, err := NewModeOutputCacheWithDir(dir, ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 10}, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCacheWithDir: %v", err)
	}

	// Empty cache stats
	stats := cache.Stats()
	if stats.Entries != 0 {
		t.Errorf("empty Entries = %d", stats.Entries)
	}
	if stats.MaxEntries != 10 {
		t.Errorf("MaxEntries = %d, want 10", stats.MaxEntries)
	}

	// Add an entry
	mode := sampleMode(t)
	cfg := ModeOutputConfig{Question: "stats-q", AgentType: "cc", SchemaVersion: SchemaVersion}
	fp, _ := BuildModeOutputFingerprint("ctx", mode, cfg)
	_ = cache.Put(fp, &ModeOutput{ModeID: mode.ID, Thesis: "t", Confidence: 0.5, GeneratedAt: time.Now()})

	stats = cache.Stats()
	if stats.Entries != 1 {
		t.Errorf("Entries = %d, want 1", stats.Entries)
	}
	if stats.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", stats.SizeBytes)
	}
	if stats.Oldest.IsZero() {
		t.Error("Oldest should not be zero")
	}
	if stats.Newest.IsZero() {
		t.Error("Newest should not be zero")
	}
}

func TestModeOutputCache_Lookup_NilCache(t *testing.T) {
	t.Parallel()
	var nilCache *ModeOutputCache
	lookup := nilCache.Lookup(ModeOutputFingerprint{})
	if lookup.Hit {
		t.Error("expected miss on nil cache")
	}
	if lookup.Reason != "cache_disabled" {
		t.Errorf("reason = %q, want cache_disabled", lookup.Reason)
	}
}

func TestModeOutputCache_Put_NilCache(t *testing.T) {
	t.Parallel()
	var nilCache *ModeOutputCache
	err := nilCache.Put(ModeOutputFingerprint{}, &ModeOutput{})
	if err != nil {
		t.Errorf("Put on nil cache should return nil, got: %v", err)
	}
}

func TestModeOutputConfig_Hash(t *testing.T) {
	t.Parallel()

	cfg1 := ModeOutputConfig{Question: "q1", AgentType: "cc"}
	cfg2 := ModeOutputConfig{Question: "q1", AgentType: "cc"}
	cfg3 := ModeOutputConfig{Question: "q2", AgentType: "cod"}

	if cfg1.Hash() != cfg2.Hash() {
		t.Error("same config should produce same hash")
	}
	if cfg1.Hash() == cfg3.Hash() {
		t.Error("different configs should produce different hashes")
	}
	if len(cfg1.Hash()) != 16 {
		t.Errorf("hash length = %d, want 16", len(cfg1.Hash()))
	}
}

func TestModeOutputFingerprint_CacheKey(t *testing.T) {
	t.Parallel()

	fp1 := ModeOutputFingerprint{ContextHash: "ctx1", ModeID: "m1", ConfigHash: "cfg1"}
	fp2 := ModeOutputFingerprint{ContextHash: "ctx1", ModeID: "m1", ConfigHash: "cfg1"}
	fp3 := ModeOutputFingerprint{ContextHash: "ctx2", ModeID: "m2", ConfigHash: "cfg2"}

	if fp1.CacheKey() != fp2.CacheKey() {
		t.Error("same fingerprint should produce same key")
	}
	if fp1.CacheKey() == fp3.CacheKey() {
		t.Error("different fingerprints should produce different keys")
	}
}

func TestBuildModeOutputFingerprint_NilMode(t *testing.T) {
	t.Parallel()
	_, err := BuildModeOutputFingerprint("ctx", nil, ModeOutputConfig{})
	if err == nil {
		t.Error("expected error for nil mode")
	}
}

// =============================================================================
// NewModeOutputCache (convenience constructor) coverage
// =============================================================================

func TestNewModeOutputCache_Valid(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	cfg := ModeOutputCacheConfig{Enabled: true, TTL: time.Minute, MaxEntries: 10}

	cache, err := NewModeOutputCache(projectDir, cfg, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCache: %v", err)
	}
	if cache == nil {
		t.Fatal("expected non-nil cache")
	}

	// Verify the cache directory was created under the project dir
	expectedDir := projectDir + "/.ntm/ensemble-cache"
	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("cache dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("cache path is not a directory")
	}
}

func TestNewModeOutputCache_EmptyProjectDir(t *testing.T) {
	t.Parallel()
	cfg := ModeOutputCacheConfig{Enabled: true}

	// Empty projectDir should use cwd-based path (still works)
	cache, err := NewModeOutputCache("", cfg, nil)
	if err != nil {
		t.Fatalf("NewModeOutputCache with empty projectDir: %v", err)
	}
	if cache == nil {
		t.Fatal("expected non-nil cache")
	}
}

func TestDefaultModeOutputCacheConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultModeOutputCacheConfig()
	if !cfg.Enabled {
		t.Error("default should be enabled")
	}
	if cfg.TTL != defaultOutputCacheTTL {
		t.Errorf("TTL = %v, want %v", cfg.TTL, defaultOutputCacheTTL)
	}
	if cfg.MaxEntries != defaultOutputCacheMax {
		t.Errorf("MaxEntries = %d, want %d", cfg.MaxEntries, defaultOutputCacheMax)
	}
}
