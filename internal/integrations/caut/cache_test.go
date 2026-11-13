package caut

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestNewUsageCache(t *testing.T) {
	cache := NewUsageCache()
	if cache == nil {
		t.Fatal("NewUsageCache returned nil")
	}

	if cache.usage == nil {
		t.Error("usage map not initialized")
	}
}

func TestUsageCache_UpdateStatus(t *testing.T) {
	cache := NewUsageCache()

	status := &tools.CautStatus{
		Running:       true,
		Tracking:      true,
		ProviderCount: 2,
		TotalSpend:    125.50,
		QuotaPercent:  45.5,
	}

	cache.UpdateStatus(status)

	got := cache.GetStatus()
	if got == nil {
		t.Fatal("GetStatus returned nil after UpdateStatus")
	}

	if got.ProviderCount != 2 {
		t.Errorf("Expected ProviderCount 2, got %d", got.ProviderCount)
	}

	if got.TotalSpend != 125.50 {
		t.Errorf("Expected TotalSpend 125.50, got %f", got.TotalSpend)
	}

	// Verify LastUpdated was set
	if cache.GetLastUpdated().IsZero() {
		t.Error("LastUpdated should be set after UpdateStatus")
	}

	// Verify UpdateCount was incremented
	if cache.GetUpdateCount() != 1 {
		t.Errorf("Expected UpdateCount 1, got %d", cache.GetUpdateCount())
	}
}

func TestUsageCache_UpdateUsage(t *testing.T) {
	cache := NewUsageCache()

	usage := &tools.CautUsage{
		Provider:     "anthropic",
		RequestCount: 1500,
		TokensIn:     500000,
		TokensOut:    250000,
		Cost:         45.50,
		Period:       "day",
	}

	cache.UpdateUsage("anthropic", usage)

	got := cache.GetUsage("anthropic")
	if got == nil {
		t.Fatal("GetUsage returned nil after UpdateUsage")
	}

	if got.Provider != "anthropic" {
		t.Errorf("Expected Provider 'anthropic', got %s", got.Provider)
	}

	if got.Cost != 45.50 {
		t.Errorf("Expected Cost 45.50, got %f", got.Cost)
	}

	// Verify non-existent provider returns nil
	if cache.GetUsage("nonexistent") != nil {
		t.Error("GetUsage should return nil for non-existent provider")
	}
}

func TestUsageCache_UpdateAllUsage(t *testing.T) {
	cache := NewUsageCache()

	// Add some initial usage
	cache.UpdateUsage("old_provider", &tools.CautUsage{Provider: "old_provider"})

	usages := []tools.CautUsage{
		{Provider: "anthropic", Cost: 50.0},
		{Provider: "openai", Cost: 75.0},
	}

	cache.UpdateAllUsage(usages)

	// Check new providers exist
	if cache.GetUsage("anthropic") == nil {
		t.Error("anthropic usage not found after UpdateAllUsage")
	}

	if cache.GetUsage("openai") == nil {
		t.Error("openai usage not found after UpdateAllUsage")
	}

	// Check old provider was cleared
	if cache.GetUsage("old_provider") != nil {
		t.Error("old_provider should have been cleared by UpdateAllUsage")
	}

	// Verify GetAllUsage returns all providers
	all := cache.GetAllUsage()
	if len(all) != 2 {
		t.Errorf("Expected 2 usages, got %d", len(all))
	}
}

func TestUsageCache_UpdateAllUsage_CopiesCallerData(t *testing.T) {
	cache := NewUsageCache()

	usages := []tools.CautUsage{
		{Provider: "anthropic", Cost: 50.0},
		{Provider: "openai", Cost: 75.0},
	}

	cache.UpdateAllUsage(usages)

	usages[0].Cost = 999.0
	usages[1].Provider = "mutated"

	gotAnthropic := cache.GetUsage("anthropic")
	if gotAnthropic == nil {
		t.Fatal("anthropic usage not found after UpdateAllUsage")
	}
	if gotAnthropic.Cost != 50.0 {
		t.Errorf("Expected cached anthropic cost 50.0, got %f", gotAnthropic.Cost)
	}

	if cache.GetUsage("mutated") != nil {
		t.Error("cache should not reflect caller-side provider mutation")
	}
}

func TestUsageCache_GetAllUsage_SortedByProvider(t *testing.T) {
	cache := NewUsageCache()

	cache.UpdateAllUsage([]tools.CautUsage{
		{Provider: "openai", Cost: 75.0},
		{Provider: "anthropic", Cost: 50.0},
		{Provider: "gemini", Cost: 25.0},
	})

	all := cache.GetAllUsage()
	if len(all) != 3 {
		t.Fatalf("Expected 3 usages, got %d", len(all))
	}

	gotProviders := []string{all[0].Provider, all[1].Provider, all[2].Provider}
	wantProviders := []string{"anthropic", "gemini", "openai"}
	for i := range wantProviders {
		if gotProviders[i] != wantProviders[i] {
			t.Fatalf("provider order = %v, want %v", gotProviders, wantProviders)
		}
	}
}

func TestUsageCache_Error(t *testing.T) {
	cache := NewUsageCache()

	errTime, err := cache.GetLastError()
	if err != nil {
		t.Error("Expected no error initially")
	}
	if !errTime.IsZero() {
		t.Error("Expected zero error time initially")
	}

	testErr := &testError{msg: "test error"}
	cache.SetError(testErr)

	errTime, err = cache.GetLastError()
	if err == nil {
		t.Error("Expected error after SetError")
	}
	if err.Error() != "test error" {
		t.Errorf("Expected 'test error', got %s", err.Error())
	}
	if errTime.IsZero() {
		t.Error("Expected error time to be set")
	}

	cache.ClearError()
	_, err = cache.GetLastError()
	if err != nil {
		t.Error("Expected nil error after ClearError")
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestUsageCache_IsStale(t *testing.T) {
	cache := NewUsageCache()

	// Empty cache is always stale
	if !cache.IsStale(time.Minute) {
		t.Error("Empty cache should be stale")
	}

	// Update cache
	cache.UpdateStatus(&tools.CautStatus{Running: true})

	// Fresh cache should not be stale
	if cache.IsStale(time.Minute) {
		t.Error("Fresh cache should not be stale")
	}

	// Use a short maxAge to test staleness
	if !cache.IsStale(0) {
		t.Error("Cache should be stale with maxAge of 0")
	}
}

func TestUsageCache_Snapshot(t *testing.T) {
	cache := NewUsageCache()

	status := &tools.CautStatus{
		Running:       true,
		ProviderCount: 2,
		QuotaPercent:  65.0,
	}
	cache.UpdateStatus(status)

	usages := []tools.CautUsage{
		{Provider: "anthropic", Cost: 50.0},
	}
	cache.UpdateAllUsage(usages)

	snapshot := cache.Snapshot()

	if snapshot.Status == nil {
		t.Error("Snapshot Status should not be nil")
	}

	if snapshot.Status.QuotaPercent != 65.0 {
		t.Errorf("Expected QuotaPercent 65.0, got %f", snapshot.Status.QuotaPercent)
	}

	if len(snapshot.Usage) != 1 {
		t.Errorf("Expected 1 usage, got %d", len(snapshot.Usage))
	}

	if snapshot.UpdateCount != 2 {
		t.Errorf("Expected UpdateCount 2, got %d", snapshot.UpdateCount)
	}

	if snapshot.HasError {
		t.Error("Should not have error")
	}
}

func TestUsageCache_SnapshotWithError(t *testing.T) {
	cache := NewUsageCache()

	cache.SetError(&testError{msg: "snapshot test error"})

	snapshot := cache.Snapshot()

	if !snapshot.HasError {
		t.Error("Snapshot should indicate HasError")
	}

	if snapshot.ErrorMessage != "snapshot test error" {
		t.Errorf("Expected 'snapshot test error', got %s", snapshot.ErrorMessage)
	}

	if snapshot.ErrorTime == nil {
		t.Error("ErrorTime should be set")
	}
}

func TestUsageCache_Clear(t *testing.T) {
	cache := NewUsageCache()

	// Populate cache
	cache.UpdateStatus(&tools.CautStatus{Running: true})
	cache.UpdateUsage("test", &tools.CautUsage{Provider: "test"})
	cache.SetError(&testError{msg: "test"})

	// Verify populated
	if cache.GetStatus() == nil {
		t.Error("Status should be set before Clear")
	}

	cache.Clear()

	// Verify cleared
	if cache.GetStatus() != nil {
		t.Error("Status should be nil after Clear")
	}

	if len(cache.GetAllUsage()) != 0 {
		t.Error("Usage should be empty after Clear")
	}

	if cache.GetUpdateCount() != 0 {
		t.Error("UpdateCount should be 0 after Clear")
	}

	_, err := cache.GetLastError()
	if err != nil {
		t.Error("Error should be nil after Clear")
	}
}

func TestUsageCache_Concurrency(t *testing.T) {
	cache := NewUsageCache()

	// Run concurrent operations
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.UpdateStatus(&tools.CautStatus{ProviderCount: i})
			cache.UpdateUsage("test", &tools.CautUsage{RequestCount: i})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = cache.GetStatus()
			_ = cache.GetUsage("test")
			_ = cache.GetAllUsage()
			_ = cache.Snapshot()
		}
		done <- true
	}()

	// Wait for both to finish
	<-done
	<-done

	// If we get here without panic/race, test passed
}

func TestUsageCache_ReturnsCopies(t *testing.T) {
	cache := NewUsageCache()

	status := &tools.CautStatus{Running: true, ProviderCount: 5}
	cache.UpdateStatus(status)

	// Modify original
	status.ProviderCount = 99

	// Get from cache - should not see modification
	got := cache.GetStatus()
	if got.ProviderCount == 99 {
		t.Error("Cache should return copy, not reference to original")
	}

	// Modify returned value
	got.ProviderCount = 100

	// Get again - should not see modification
	got2 := cache.GetStatus()
	if got2.ProviderCount == 100 {
		t.Error("Modifying returned value should not affect cache")
	}
}

func (c *UsageCache) GetLastUpdated() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUpdated
}
