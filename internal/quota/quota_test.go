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

func TestQuotaInfoHealth(t *testing.T) {
	tests := []struct {
		name     string
		info     *QuotaInfo
		expected HealthState
	}{
		{
			name:     "nil info is unknown",
			info:     nil,
			expected: HealthUnknown,
		},
		{
			name: "failed fetch with all-zero usage is unknown, not healthy",
			info: &QuotaInfo{
				Error: "timeout waiting for usage data",
			},
			expected: HealthUnknown,
		},
		{
			name: "failed fetch with nonzero usage is still unknown",
			info: &QuotaInfo{
				SessionUsage: 95,
				Error:        "context cancelled",
			},
			expected: HealthUnknown,
		},
		{
			name:     "successful fetch at 0% usage is healthy",
			info:     &QuotaInfo{},
			expected: HealthHealthy,
		},
		{
			name: "limited is unhealthy",
			info: &QuotaInfo{
				IsLimited: true,
			},
			expected: HealthUnhealthy,
		},
		{
			name: "usage at 90% threshold is unhealthy",
			info: &QuotaInfo{
				PeriodUsage: 90,
			},
			expected: HealthUnhealthy,
		},
		{
			name: "usage just under threshold is healthy",
			info: &QuotaInfo{
				SessionUsage: 89.9,
				WeeklyUsage:  89.9,
				PeriodUsage:  89.9,
			},
			expected: HealthHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.Health()
			if got != tt.expected {
				t.Errorf("Health() = %v, want %v", got, tt.expected)
			}
			wantHealthy := tt.expected == HealthHealthy
			if gotHealthy := tt.info.IsHealthy(); gotHealthy != wantHealthy {
				t.Errorf("IsHealthy() = %v, want %v", gotHealthy, wantHealthy)
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
			name: "failed all-zero fetch is not healthy",
			info: &QuotaInfo{
				Error: "failed to capture initial output: exit 1",
			},
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
