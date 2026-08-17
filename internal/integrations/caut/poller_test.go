package caut

import (
	"testing"
)

func TestNewUsagePoller(t *testing.T) {
	poller := NewUsagePoller()
	if poller == nil {
		t.Fatal("NewUsagePoller returned nil")
	}

	if poller.cache == nil {
		t.Error("cache not initialized")
	}
}

func TestUsagePoller_GetCache(t *testing.T) {
	poller := NewUsagePoller()

	cache := poller.GetCache()
	if cache == nil {
		t.Error("GetCache should not return nil")
	}

	// Verify it's the same cache
	if cache != poller.cache {
		t.Error("GetCache should return internal cache")
	}
}

func TestGlobalPoller(t *testing.T) {
	// GetGlobalPoller should return non-nil
	poller := GetGlobalPoller()
	if poller == nil {
		t.Error("GetGlobalPoller returned nil")
	}

	// Multiple calls should return same instance
	poller2 := GetGlobalPoller()
	if poller != poller2 {
		t.Error("GetGlobalPoller should return singleton")
	}
}
