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
	contextCacheVersion    = 1
	defaultContextCacheTTL = time.Hour
	defaultContextCacheMax = 32
)

// ContextFingerprint captures inputs that should invalidate cached packs.
type ContextFingerprint struct {
	ProjectRoot  string `json:"project_root"`
	GitHead      string `json:"git_head,omitempty"`
	GitStatus    string `json:"git_status,omitempty"`
	ReadmeHash   string `json:"readme_hash,omitempty"`
	QuestionHash string `json:"question_hash,omitempty"`
	ModeKey      string `json:"mode_key,omitempty"`
}

// cacheKey returns a stable hash for the fingerprint.
func (f ContextFingerprint) cacheKey() string {
	data, err := json.Marshal(f)
	if err != nil {
		// Fallback to field-based key if marshal fails
		fallback := f.fallbackCacheKeyMaterial()
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func (f ContextFingerprint) fallbackCacheKeyMaterial() string {
	return fmt.Sprintf(
		"%s:%s:%s:%s:%s:%s",
		f.ProjectRoot,
		f.GitHead,
		f.GitStatus,
		f.ReadmeHash,
		f.QuestionHash,
		f.ModeKey,
	)
}

type cachedContextPack struct {
	Version     int                `json:"version"`
	Key         string             `json:"key"`
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   time.Time          `json:"expires_at"`
	Fingerprint ContextFingerprint `json:"fingerprint"`
	Pack        *ContextPack       `json:"pack"`
}

// ContextPackCache stores context packs on disk with TTL and an in-memory hot cache.
type ContextPackCache struct {
	dir        string
	ttl        time.Duration
	maxEntries int
	logger     *slog.Logger
	mem        *tools.Cache
	mu         sync.Mutex
}

// NewContextPackCache creates a cache rooted under the user's cache directory.
func NewContextPackCache(cfg CacheConfig, logger *slog.Logger) (*ContextPackCache, error) {
	return NewContextPackCacheWithDir(cfg.CacheDir, cfg, logger)
}

// NewContextPackCacheWithDir creates a cache rooted in a specific directory (test override).
func NewContextPackCacheWithDir(dir string, cfg CacheConfig, logger *slog.Logger) (*ContextPackCache, error) {
	if dir == "" {
		cacheDir, err := defaultContextCacheDir()
		if err != nil {
			return nil, err
		}
		dir = cacheDir
	}
	if err := util.EnsureDir(dir); err != nil {
		return nil, fmt.Errorf("ensure cache dir: %w", err)
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultContextCacheTTL
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultContextCacheMax
	}

	cache := &ContextPackCache{
		dir:        dir,
		ttl:        ttl,
		maxEntries: maxEntries,
		logger:     logger,
		mem:        tools.NewCache(ttl),
	}
	return cache, nil
}

func defaultContextCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("cache dir unavailable: %w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "ntm", "context-packs"), nil
}

func (c *ContextPackCache) loggerSafe() *slog.Logger {
	if c != nil && c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

func (c *ContextPackCache) Get(key string) (*ContextPack, bool) {
	if c == nil || key == "" {
		return nil, false
	}

	if cached, ok := c.mem.Get(key); ok {
		if pack, ok := cached.(*ContextPack); ok && pack != nil {
			return pack, true
		}
	}

	path := c.filePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var stored cachedContextPack
	if err := json.Unmarshal(data, &stored); err != nil {
		c.loggerSafe().Warn("context pack cache decode failed", "key", key, "error", err)
		return nil, false
	}

	if stored.Version != contextCacheVersion {
		return nil, false
	}

	if time.Now().After(stored.ExpiresAt) {
		_ = os.Remove(path)
		return nil, false
	}

	if stored.Pack == nil {
		return nil, false
	}

	c.mem.Set(key, stored.Pack)
	// Update mtime to implement LRU pruning behavior on disk
	_ = os.Chtimes(path, time.Now(), time.Now())
	return stored.Pack, true
}

func (c *ContextPackCache) Put(key string, pack *ContextPack, fingerprint ContextFingerprint) error {
	if c == nil || key == "" || pack == nil {
		return nil
	}

	stored := cachedContextPack{
		Version:     contextCacheVersion,
		Key:         key,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(c.ttl),
		Fingerprint: fingerprint,
		Pack:        pack,
	}

	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}

	if err := util.AtomicWriteFile(c.filePath(key), data, 0644); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}

	c.mem.Set(key, pack)
	c.pruneIfNeeded()
	return nil
}

func (c *ContextPackCache) filePath(key string) string {
	filename := fmt.Sprintf("%s.json", key)
	return filepath.Join(c.dir, filename)
}

func (c *ContextPackCache) pruneIfNeeded() {
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

func hashString(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func hashFileContents(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}
