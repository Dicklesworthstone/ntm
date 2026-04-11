package tools

import (
	"testing"
	"time"
)

func TestCache_NewCache_TTLDefaults(t *testing.T) {
	t.Parallel()

	c := NewCache(0)
	t.Cleanup(c.Close)

	if c.ttl != DefaultCacheTTL {
		t.Fatalf("ttl = %v, want %v", c.ttl, DefaultCacheTTL)
	}

	c2 := NewCache(MinCacheTTL / 2)
	t.Cleanup(c2.Close)

	if c2.ttl != MinCacheTTL {
		t.Fatalf("ttl = %v, want %v", c2.ttl, MinCacheTTL)
	}
}

func TestCache_Get_ExpiredEntry(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Hour)
	t.Cleanup(c.Close)

	c.SetWithTTL("k", "v", -time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatalf("expected expired entry to not be returned")
	}
}

func TestCache_Set_Get_Delete_Clear_Cleanup_Size(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Hour)
	t.Cleanup(c.Close)

	c.Set("a", 123)
	if v, ok := c.Get("a"); !ok || v.(int) != 123 {
		t.Fatalf("Get(a) = (%v, %v), want (123, true)", v, ok)
	}

	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected deleted entry to not be returned")
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

	if c.Size() != 1 {
		t.Fatalf("Size() = %d, want 1", c.Size())
	}

	c.Clear()
	if c.Size() != 0 {
		t.Fatalf("Size() after Clear() = %d, want 0", c.Size())
	}
}

func TestCache_Close_Idempotent(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Hour)
	c.Close()
	c.Close()

	if !c.closed {
		t.Fatalf("expected cache to be marked closed")
	}
	select {
	case <-c.done:
		// ok
	default:
		t.Fatalf("expected done channel to be closed")
	}

	cleanupStopped := make(chan struct{})
	go func() {
		c.cleanupWg.Wait()
		close(cleanupStopped)
	}()

	select {
	case <-cleanupStopped:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("expected Close() to wait for cleanup loop exit")
	}
}
