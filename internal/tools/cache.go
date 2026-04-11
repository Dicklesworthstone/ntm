package tools

import (
	"sync"
	"time"
)

// DefaultCacheTTL is the default cache entry time-to-live.
const DefaultCacheTTL = 5 * time.Minute

// MinCacheTTL is the minimum allowed TTL to ensure the cleanup ticker is valid.
// time.NewTicker requires a positive duration, and ttl/2 must be > 0.
const MinCacheTTL = 2 * time.Millisecond

// cacheEntry holds a cached value with expiration
type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

// Cache provides thread-safe caching with TTL
type Cache struct {
	entries   map[string]cacheEntry
	ttl       time.Duration
	mu        sync.RWMutex
	done      chan struct{}
	closed    bool
	cleanupWg sync.WaitGroup
}

// NewCache creates a new cache with the specified TTL.
// If ttl is 0, DefaultCacheTTL is used.
// If ttl is less than MinCacheTTL, MinCacheTTL is used to ensure
// the cleanup goroutine's ticker has a valid positive duration.
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	} else if ttl < MinCacheTTL {
		ttl = MinCacheTTL
	}

	c := &Cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		done:    make(chan struct{}),
	}

	// Start cleanup goroutine
	c.cleanupWg.Add(1)
	go c.cleanupLoop()

	return c
}

// Close stops the cleanup goroutine and releases resources.
// The cache should not be used after Close is called.
func (c *Cache) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.cleanupWg.Wait()
		return
	}
	c.closed = true
	done := c.done
	c.mu.Unlock()

	close(done)
	c.cleanupWg.Wait()
}

// Get retrieves a value from the cache
func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		return nil, false
	}

	return entry.value, true
}

// Set stores a value in the cache with the default TTL
func (c *Cache) Set(key string, value interface{}) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores a value with a custom TTL
func (c *Cache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// Delete removes a key from the cache
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Clear removes all entries from the cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry)
}

// cleanupLoop periodically removes expired entries
func (c *Cache) cleanupLoop() {
	defer c.cleanupWg.Done()
	ticker := time.NewTicker(c.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

// cleanup removes expired entries
func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

// Size returns the number of entries in the cache
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
