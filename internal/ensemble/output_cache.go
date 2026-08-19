package ensemble

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	modeOutputCacheVersion = 1
	defaultOutputCacheTTL  = 24 * time.Hour
	defaultOutputCacheMax  = 64
)

// ModeOutputCacheConfig configures the mode output cache.
type ModeOutputCacheConfig struct {
	Enabled    bool          `json:"enabled"`
	TTL        time.Duration `json:"ttl"`
	CacheDir   string        `json:"cache_dir,omitempty"`
	MaxEntries int           `json:"max_entries,omitempty"`
}

// DefaultModeOutputCacheConfig returns default settings for mode output caching.
func DefaultModeOutputCacheConfig() ModeOutputCacheConfig {
	return ModeOutputCacheConfig{
		Enabled:    true,
		TTL:        defaultOutputCacheTTL,
		MaxEntries: defaultOutputCacheMax,
	}
}

// ModeOutputConfig captures config inputs that affect mode output generation.
type ModeOutputConfig struct {
	Question          string `json:"question,omitempty"`
	AgentType         string `json:"agent_type,omitempty"`
	TokenCap          int    `json:"token_cap,omitempty"`
	SynthesisStrategy string `json:"synthesis_strategy,omitempty"`
	SchemaVersion     string `json:"schema_version,omitempty"`
}

// Hash returns a stable hash of the config.
func (c ModeOutputConfig) Hash() string {
	data, err := json.Marshal(c)
	if err != nil {
		fallback := fmt.Sprintf("%s:%s:%d", c.AgentType, c.SynthesisStrategy, c.TokenCap)
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// ModeOutputFingerprint identifies a cached mode output.
type ModeOutputFingerprint struct {
	ContextHash string `json:"context_hash"`
	ModeID      string `json:"mode_id"`
	ModeVersion string `json:"mode_version"`
	ConfigHash  string `json:"config_hash"`
}

// CacheKey returns a stable key for the fingerprint.
func (f ModeOutputFingerprint) CacheKey() string {
	data, err := json.Marshal(f)
	if err != nil {
		fallback := fmt.Sprintf("%s:%s:%s:%s", f.ContextHash, f.ModeID, f.ModeVersion, f.ConfigHash)
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// BuildModeOutputFingerprint constructs a fingerprint for caching.
func BuildModeOutputFingerprint(contextHash string, mode *ReasoningMode, cfg ModeOutputConfig) (ModeOutputFingerprint, error) {
	if mode == nil {
		return ModeOutputFingerprint{}, fmt.Errorf("mode is nil")
	}
	if contextHash == "" {
		contextHash = hashString(cfg.Question)
	}
	return ModeOutputFingerprint{
		ContextHash: contextHash,
		ModeID:      mode.ID,
		ModeVersion: modeVersion(mode),
		ConfigHash:  cfg.Hash(),
	}, nil
}

type cachedModeOutput struct {
	Version     int                   `json:"version"`
	Key         string                `json:"key"`
	CreatedAt   time.Time             `json:"created_at"`
	ExpiresAt   time.Time             `json:"expires_at"`
	Fingerprint ModeOutputFingerprint `json:"fingerprint"`
	Output      *ModeOutput           `json:"output"`
}

// ModeOutputCache stores mode outputs on disk with TTL and an in-memory hot cache.
type ModeOutputCache struct {
	dir        string
	ttl        time.Duration
	maxEntries int
	logger     *slog.Logger
	mem        *tools.Cache
	mu         sync.Mutex
}

// ModeOutputLookup captures cache lookup results.
type ModeOutputLookup struct {
	Output *ModeOutput
	Hit    bool
	Reason string
}

// ModeOutputCacheStats summarizes cache contents.
type ModeOutputCacheStats struct {
	Entries    int           `json:"entries"`
	Expired    int           `json:"expired"`
	SizeBytes  int64         `json:"size_bytes"`
	Oldest     time.Time     `json:"oldest,omitempty"`
	Newest     time.Time     `json:"newest,omitempty"`
	MaxEntries int           `json:"max_entries"`
	TTL        time.Duration `json:"ttl"`
}

// NewModeOutputCache creates a cache rooted under the project directory.
func NewModeOutputCache(projectDir string, cfg ModeOutputCacheConfig, logger *slog.Logger) (*ModeOutputCache, error) {
	return NewModeOutputCacheWithDir(defaultModeOutputCacheDir(projectDir), cfg, logger)
}

// NewModeOutputCacheWithDir creates a cache rooted in a specific directory (test override).
func NewModeOutputCacheWithDir(dir string, cfg ModeOutputCacheConfig, logger *slog.Logger) (*ModeOutputCache, error) {
	if dir == "" {
		return nil, fmt.Errorf("cache dir is empty")
	}
	if err := util.EnsureDir(dir); err != nil {
		return nil, fmt.Errorf("ensure cache dir: %w", err)
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultOutputCacheTTL
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultOutputCacheMax
	}

	cache := &ModeOutputCache{
		dir:        dir,
		ttl:        ttl,
		maxEntries: maxEntries,
		logger:     logger,
		mem:        tools.NewCache(ttl),
	}
	return cache, nil
}

func defaultModeOutputCacheDir(projectDir string) string {
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	return filepath.Join(projectDir, ".ntm", "ensemble-cache")
}

func (c *ModeOutputCache) loggerSafe() *slog.Logger {
	if c != nil && c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Lookup retrieves a cached output and includes a reason on misses.
func (c *ModeOutputCache) Lookup(fingerprint ModeOutputFingerprint) ModeOutputLookup {
	if c == nil {
		return ModeOutputLookup{Hit: false, Reason: "cache_disabled"}
	}
	key := fingerprint.CacheKey()
	if key == "" {
		return ModeOutputLookup{Hit: false, Reason: "invalid_key"}
	}

	if cached, ok := c.mem.Get(key); ok {
		if output, ok := cached.(*ModeOutput); ok && output != nil {
			return ModeOutputLookup{Output: output, Hit: true, Reason: "memory"}
		}
	}

	path := c.filePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		reason := c.invalidationReason(fingerprint)
		if reason == "" {
			reason = "miss"
		}
		return ModeOutputLookup{Hit: false, Reason: reason}
	}

	var stored cachedModeOutput
	if err := json.Unmarshal(data, &stored); err != nil {
		c.loggerSafe().Warn("mode output cache decode failed", "key", key, "error", err)
		return ModeOutputLookup{Hit: false, Reason: "decode_failed"}
	}

	if stored.Version != modeOutputCacheVersion {
		return ModeOutputLookup{Hit: false, Reason: "version_mismatch"}
	}

	if time.Now().After(stored.ExpiresAt) {
		_ = os.Remove(path)
		return ModeOutputLookup{Hit: false, Reason: "expired"}
	}

	if stored.Output == nil {
		return ModeOutputLookup{Hit: false, Reason: "empty"}
	}

	if stored.Fingerprint != fingerprint {
		return ModeOutputLookup{Hit: false, Reason: "fingerprint_mismatch"}
	}

	c.mem.Set(key, stored.Output)
	_ = os.Chtimes(path, time.Now(), time.Now())
	return ModeOutputLookup{Output: stored.Output, Hit: true, Reason: "disk"}
}

// Put stores a mode output in the cache.
func (c *ModeOutputCache) Put(fingerprint ModeOutputFingerprint, output *ModeOutput) error {
	if c == nil || output == nil {
		return nil
	}
	key := fingerprint.CacheKey()
	if key == "" {
		return nil
	}

	stored := cachedModeOutput{
		Version:     modeOutputCacheVersion,
		Key:         key,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(c.ttl),
		Fingerprint: fingerprint,
		Output:      output,
	}

	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}

	if err := util.AtomicWriteFile(c.filePath(key), data, 0644); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}

	c.mem.Set(key, output)
	c.pruneIfNeeded()
	return nil
}

// Clear removes all cached outputs. Returns number removed.
func (c *ModeOutputCache) Clear() int {
	if c == nil {
		return 0
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, entry.Name())); err == nil {
			removed++
		}
	}
	return removed
}

// Stats returns a summary of cache contents.
func (c *ModeOutputCache) Stats() ModeOutputCacheStats {
	if c == nil {
		return ModeOutputCacheStats{}
	}
	stats := ModeOutputCacheStats{MaxEntries: c.maxEntries, TTL: c.ttl}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return stats
	}
	if len(entries) == 0 {
		return stats
	}

	now := time.Now()
	var oldest time.Time
	var newest time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stats.Entries++
		stats.SizeBytes += info.Size()
		mod := info.ModTime()
		if oldest.IsZero() || mod.Before(oldest) {
			oldest = mod
		}
		if newest.IsZero() || mod.After(newest) {
			newest = mod
		}

		data, err := os.ReadFile(filepath.Join(c.dir, entry.Name()))
		if err != nil {
			continue
		}
		var stored cachedModeOutput
		if err := json.Unmarshal(data, &stored); err != nil {
			continue
		}
		if now.After(stored.ExpiresAt) {
			stats.Expired++
		}
	}

	stats.Oldest = oldest
	stats.Newest = newest
	return stats
}

func (c *ModeOutputCache) filePath(key string) string {
	filename := fmt.Sprintf("%s.json", key)
	return filepath.Join(c.dir, filename)
}

func (c *ModeOutputCache) pruneIfNeeded() {
	if c == nil || c.maxEntries <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	if len(entries) <= c.maxEntries {
		return
	}

	type fileInfo struct {
		name string
		mod  time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: entry.Name(), mod: info.ModTime()})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.Before(files[j].mod)
	})

	toRemove := len(files) - c.maxEntries
	for i := 0; i < toRemove; i++ {
		_ = os.Remove(filepath.Join(c.dir, files[i].name))
	}
}

func (c *ModeOutputCache) invalidationReason(target ModeOutputFingerprint) string {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return ""
	}
	modeSeen := false
	contextSeen := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.dir, entry.Name()))
		if err != nil {
			continue
		}
		var stored cachedModeOutput
		if err := json.Unmarshal(data, &stored); err != nil {
			continue
		}
		if stored.Fingerprint.ModeID != target.ModeID {
			continue
		}
		modeSeen = true
		if stored.Fingerprint.ContextHash != target.ContextHash {
			continue
		}
		contextSeen = true
		if stored.Fingerprint.ModeVersion != target.ModeVersion {
			return "mode_version_mismatch"
		}
		if stored.Fingerprint.ConfigHash != target.ConfigHash {
			return "config_mismatch"
		}
	}
	if contextSeen {
		return "miss"
	}
	if modeSeen {
		return "context_mismatch"
	}
	return "miss"
}

func modeVersion(mode *ReasoningMode) string {
	if mode == nil {
		return ""
	}
	version := struct {
		ID             string       `json:"id"`
		Code           string       `json:"code"`
		Name           string       `json:"name"`
		Category       ModeCategory `json:"category"`
		Tier           ModeTier     `json:"tier"`
		ShortDesc      string       `json:"short_desc"`
		Description    string       `json:"description"`
		Outputs        string       `json:"outputs"`
		BestFor        []string     `json:"best_for"`
		FailureModes   []string     `json:"failure_modes"`
		Differentiator string       `json:"differentiator"`
		Icon           string       `json:"icon"`
		Color          string       `json:"color"`
		PreambleKey    string       `json:"preamble_key"`
		SchemaVersion  string       `json:"schema_version"`
	}{
		ID:             mode.ID,
		Code:           mode.Code,
		Name:           mode.Name,
		Category:       mode.Category,
		Tier:           mode.Tier,
		ShortDesc:      mode.ShortDesc,
		Description:    mode.Description,
		Outputs:        mode.Outputs,
		BestFor:        mode.BestFor,
		FailureModes:   mode.FailureModes,
		Differentiator: mode.Differentiator,
		Icon:           mode.Icon,
		Color:          mode.Color,
		PreambleKey:    mode.PreambleKey,
		SchemaVersion:  SchemaVersion,
	}
	data, err := json.Marshal(version)
	if err != nil {
		fallback := fmt.Sprintf("%s:%s:%s", version.Name, version.Code, version.SchemaVersion)
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}
