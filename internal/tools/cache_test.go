package tools

import (
	"testing"
	"time"
)

func TestCache_NewCache_TTLDefaults(t *testing.T) {
	t.Parallel()

	c := NewCache(0)
	defer c.Close()

	if c.ttl != DefaultCacheTTL {
		t.Fatalf("ttl = %v, want %v", c.ttl, DefaultCacheTTL)
	}

	c2 := NewCache(MinCacheTTL / 2)
	defer c2.Close()

	if c2.ttl != MinCacheTTL {
		t.Fatalf("ttl = %v, want %v", c2.ttl, MinCacheTTL)
	}
}

func TestCache_Get_ExpiredEntry(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Hour)
	defer c.Close()

	c.SetWithTTL("k", "v", -time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatalf("expected expired entry to not be returned")
	}
}

func TestCache_Set_Get_Cleanup(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Hour)
	defer c.Close()

	c.Set("a", 123)
	if v, ok := c.Get("a"); !ok || v.(int) != 123 {
		t.Fatalf("Get(a) = (%v, %v), want (123, true)", v, ok)
	}

	c.SetWithTTL("expired", "x", -time.Second)
	c.SetWithTTL("fresh", "y", time.Hour)
	c.cleanup()

	if _, ok := c.Get("expired"); ok {
		t.Fatalf("expected cleanup to remove expired entry")
	}
	if v, ok := c.Get("fresh"); !ok || v.(string) != "y" {
		t.Fatalf("Get(fresh) = (%v, %v), want (y, true)", v, ok)
	}
}

func TestCache_Close_IdempotentAndNilSafe(t *testing.T) {
	t.Parallel()

	var nilCache *Cache
	nilCache.Close()

	c := NewCache(MinCacheTTL)
	c.Set("k", "v")
	c.Close()
	c.Close() // must not panic or hang on second Close
	if v, ok := c.Get("k"); !ok || v.(string) != "v" {
		t.Fatalf("Get after Close = (%v, %v), want (v, true)", v, ok)
	}
}
