package quota

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQuotaInfoIsStale(t *testing.T) {
	tests := []struct {
		name     string
		info     *QuotaInfo
		maxAge   time.Duration
		expected bool
	}{
		{
			name:     "nil info is stale",
			info:     nil,
			maxAge:   time.Minute,
			expected: true,
		},
		{
			name: "fresh info is not stale",
			info: &QuotaInfo{
				FetchedAt: time.Now(),
			},
			maxAge:   time.Minute,
			expected: false,
		},
		{
			name: "old info is stale",
			info: &QuotaInfo{
				FetchedAt: time.Now().Add(-2 * time.Minute),
			},
			maxAge:   time.Minute,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.IsStale(tt.maxAge)
			if got != tt.expected {
				t.Errorf("IsStale() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaInfoIsHealthy(t *testing.T) {
	tests := []struct {
		name     string
		info     *QuotaInfo
		expected bool
	}{
		{
			name:     "nil info is unhealthy",
			info:     nil,
			expected: false,
		},
		{
			name: "limited info is unhealthy",
			info: &QuotaInfo{
				IsLimited: true,
			},
			expected: false,
		},
		{
			name: "high session usage is unhealthy",
			info: &QuotaInfo{
				SessionUsage: 95,
			},
			expected: false,
		},
		{
			name: "high weekly usage is unhealthy",
			info: &QuotaInfo{
				WeeklyUsage: 92,
			},
			expected: false,
		},
		{
			name: "low usage is healthy",
			info: &QuotaInfo{
				SessionUsage: 50,
				WeeklyUsage:  60,
				PeriodUsage:  40,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.IsHealthy()
			if got != tt.expected {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaInfoHighestUsage(t *testing.T) {
	tests := []struct {
		name     string
		info     *QuotaInfo
		expected float64
	}{
		{
			name:     "nil info returns 100",
			info:     nil,
			expected: 100,
		},
		{
			name: "returns session when highest",
			info: &QuotaInfo{
				SessionUsage: 80,
				WeeklyUsage:  60,
				PeriodUsage:  50,
			},
			expected: 80,
		},
		{
			name: "returns weekly when highest",
			info: &QuotaInfo{
				SessionUsage: 40,
				WeeklyUsage:  90,
				PeriodUsage:  50,
			},
			expected: 90,
		},
		{
			name: "returns sonnet when highest",
			info: &QuotaInfo{
				SessionUsage: 40,
				WeeklyUsage:  50,
				SonnetUsage:  85,
			},
			expected: 85,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.HighestUsage()
			if got != tt.expected {
				t.Errorf("HighestUsage() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// MockFetcher implements Fetcher for testing
type MockFetcher struct {
	mu        sync.Mutex
	calls     int
	returnVal *QuotaInfo
	returnErr error
}

func (m *MockFetcher) FetchQuota(ctx context.Context, paneID string, provider Provider) (*QuotaInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.returnVal, m.returnErr
}

func (m *MockFetcher) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestTrackerGetQuota(t *testing.T) {
	tracker := NewTracker(WithCacheTTL(time.Minute))

	// No cache initially
	if got := tracker.GetQuota("pane1"); got != nil {
		t.Errorf("Expected nil for uncached pane, got %+v", got)
	}

	// Add to cache manually
	info := &QuotaInfo{SessionUsage: 50}
	tracker.updateCache("pane1", info)

	// Should retrieve from cache
	got := tracker.GetQuota("pane1")
	if got == nil {
		t.Fatal("Expected cached value, got nil")
	}
	if got.SessionUsage != 50 {
		t.Errorf("SessionUsage = %v, want 50", got.SessionUsage)
	}
}

func TestTrackerQueryQuota(t *testing.T) {
	mockFetcher := &MockFetcher{
		returnVal: &QuotaInfo{
			Provider:     ProviderClaude,
			SessionUsage: 75,
			FetchedAt:    time.Now(),
		},
	}

	tracker := NewTracker(WithFetcher(mockFetcher))

	info, err := tracker.QueryQuota(context.Background(), "pane1", ProviderClaude)
	if err != nil {
		t.Fatalf("QueryQuota failed: %v", err)
	}

	if info.SessionUsage != 75 {
		t.Errorf("SessionUsage = %v, want 75", info.SessionUsage)
	}

	if mockFetcher.CallCount() != 1 {
		t.Errorf("Expected 1 fetch call, got %d", mockFetcher.CallCount())
	}

	// Should be cached now
	cached := tracker.GetQuota("pane1")
	if cached == nil {
		t.Error("Expected cached value after query")
	}
}

func TestTrackerCacheExpiry(t *testing.T) {
	tracker := NewTracker(WithCacheTTL(50 * time.Millisecond))

	info := &QuotaInfo{SessionUsage: 50}
	tracker.updateCache("pane1", info)

	// Should be available immediately
	if got := tracker.GetQuota("pane1"); got == nil {
		t.Error("Expected cached value immediately after update")
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	// Should be expired now
	if got := tracker.GetQuota("pane1"); got != nil {
		t.Error("Expected nil after cache expiry")
	}
}

func TestTrackerClearCache(t *testing.T) {
	tracker := NewTracker()

	tracker.updateCache("pane1", &QuotaInfo{})
	tracker.updateCache("pane2", &QuotaInfo{})

	tracker.ClearCache()

	if got := tracker.GetQuota("pane1"); got != nil {
		t.Error("Expected nil after clear")
	}
	if got := tracker.GetQuota("pane2"); got != nil {
		t.Error("Expected nil after clear")
	}
}

func TestTrackerInvalidatePane(t *testing.T) {
	tracker := NewTracker()

	tracker.updateCache("pane1", &QuotaInfo{})
	tracker.updateCache("pane2", &QuotaInfo{})

	tracker.InvalidatePane("pane1")

	if got := tracker.GetQuota("pane1"); got != nil {
		t.Error("Expected nil for invalidated pane")
	}
	if got := tracker.GetQuota("pane2"); got == nil {
		t.Error("Expected value for non-invalidated pane")
	}
}

func TestTrackerGetAllQuotas(t *testing.T) {
	tracker := NewTracker()

	tracker.updateCache("pane1", &QuotaInfo{SessionUsage: 10})
	tracker.updateCache("pane2", &QuotaInfo{SessionUsage: 20})

	all := tracker.GetAllQuotas()
	if len(all) != 2 {
		t.Errorf("Expected 2 quotas, got %d", len(all))
	}
}

func TestTrackerPollingControl(t *testing.T) {
	mockFetcher := &MockFetcher{
		returnVal: &QuotaInfo{SessionUsage: 50},
	}

	// Use MinPollInterval to ensure polling is fast enough for the test
	tracker := NewTracker(
		WithFetcher(mockFetcher),
		WithPollInterval(MinPollInterval),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracker.StartPolling(ctx, "pane1", ProviderClaude)

	// Wait for a few poll cycles (3x the poll interval)
	time.Sleep(3 * MinPollInterval)

	// Should have been called multiple times
	if mockFetcher.CallCount() < 2 {
		t.Errorf("Expected multiple fetch calls during polling, got %d", mockFetcher.CallCount())
	}

	// Stop polling
	tracker.StopPolling("pane1")

	callsBefore := mockFetcher.CallCount()
	time.Sleep(2 * MinPollInterval)
	callsAfter := mockFetcher.CallCount()

	if callsAfter != callsBefore {
		t.Errorf("Polling should have stopped, but calls went from %d to %d", callsBefore, callsAfter)
	}
}

func TestTrackerStopAllPolling(t *testing.T) {
	mockFetcher := &MockFetcher{
		returnVal: &QuotaInfo{SessionUsage: 50},
	}

	// Use MinPollInterval to ensure polling is fast enough for the test
	tracker := NewTracker(
		WithFetcher(mockFetcher),
		WithPollInterval(MinPollInterval),
	)

	ctx := context.Background()
	tracker.StartPolling(ctx, "pane1", ProviderClaude)
	tracker.StartPolling(ctx, "pane2", ProviderClaude)

	// Wait for polling to establish
	time.Sleep(MinPollInterval / 2)

	tracker.StopAllPolling()

	callsBefore := mockFetcher.CallCount()
	time.Sleep(2 * MinPollInterval)
	callsAfter := mockFetcher.CallCount()

	if callsAfter != callsBefore {
		t.Errorf("All polling should have stopped, but calls went from %d to %d", callsBefore, callsAfter)
	}
}
