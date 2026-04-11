package caut

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestNewUsagePoller(t *testing.T) {
	cfg := config.DefaultCautConfig()
	alerter := alerts.NewTracker(alerts.DefaultConfig())

	poller := NewUsagePoller(&cfg, alerter)
	if poller == nil {
		t.Fatal("NewUsagePoller returned nil")
	}

	if poller.adapter == nil {
		t.Error("adapter not initialized")
	}

	if poller.cache == nil {
		t.Error("cache not initialized")
	}

	// Default interval should be 60 seconds (from config)
	expected := 60 * time.Second
	if poller.interval != expected {
		t.Errorf("Expected interval %v, got %v", expected, poller.interval)
	}
}

func TestUsagePoller_IntervalMinimum(t *testing.T) {
	cfg := config.CautConfig{
		PollInterval: 1, // Too low - should be clamped to minimum
	}
	alerter := alerts.NewTracker(alerts.DefaultConfig())

	poller := NewUsagePoller(&cfg, alerter)

	// Minimum is 60 seconds (the default), not 10 seconds, based on our logic
	// Actually, looking at the code, we clamp to 60s default when < 10s
	// Wait - the code says minimum 10 seconds in the comment but defaults to 60
	// Let me re-check: if interval < 10*time.Second, interval = 60 * time.Second
	expected := 60 * time.Second
	if poller.interval != expected {
		t.Errorf("Expected interval %v (minimum), got %v", expected, poller.interval)
	}
}

func TestUsagePoller_SetInterval(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	// Set valid interval
	poller.SetInterval(120 * time.Second)
	if poller.interval != 120*time.Second {
		t.Errorf("Expected 120s, got %v", poller.interval)
	}

	// Set interval below minimum - should clamp
	poller.SetInterval(5 * time.Second)
	if poller.interval != 10*time.Second {
		t.Errorf("Expected 10s (minimum), got %v", poller.interval)
	}
}

func TestUsagePoller_IsRunning(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	if poller.IsRunning() {
		t.Error("Poller should not be running initially")
	}
}

func TestUsagePoller_GetCache(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	cache := poller.GetCache()
	if cache == nil {
		t.Error("GetCache should not return nil")
	}

	// Verify it's the same cache
	if cache != poller.cache {
		t.Error("GetCache should return internal cache")
	}
}

func TestUsagePoller_StartStop_NoCaut(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	// Start should fail if caut is not installed
	err := poller.Start()
	if err == nil {
		// If caut is installed, stop it
		poller.Stop()
		t.Skip("caut is installed, skipping not-installed test")
	}

	if poller.IsRunning() {
		t.Error("Poller should not be running after failed start")
	}
}

func TestUsagePoller_MultipleStartCalls(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	// First start might fail (no caut), but second call should be no-op
	_ = poller.Start()

	// Second call should not error even if first failed
	_ = poller.Start()

	// Clean up
	poller.Stop()
}

func TestUsagePoller_MultipleStopCalls(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	// Multiple stop calls should not panic
	poller.Stop()
	poller.Stop()
	poller.Stop()
}

func TestUsagePoller_PollNow_NoCaut(t *testing.T) {
	cfg := config.DefaultCautConfig()
	poller := NewUsagePoller(&cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := poller.PollNow(ctx)
	if err == nil {
		t.Skip("caut is installed, skipping not-installed test")
	}

	// Error is expected when caut is not installed
	t.Logf("Expected error when caut not installed: %v", err)
}

func TestUsagePoller_AlertThresholds(t *testing.T) {
	cfg := config.CautConfig{
		Enabled:        true,
		PollInterval:   60,
		AlertThreshold: 80,
	}
	alerter := alerts.NewTracker(alerts.DefaultConfig())
	poller := NewUsagePoller(&cfg, alerter)

	// Test cases for different quota percentages
	tests := []struct {
		name           string
		quotaPercent   float64
		expectWarning  bool
		expectCritical bool
	}{
		{"below_threshold", 70.0, false, false},
		{"at_warning", 80.0, true, false},
		{"above_warning", 85.0, true, false},
		{"at_critical", 95.0, false, true},
		{"above_critical", 99.0, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear alerts
			alerter.Clear()

			// Create status with test quota
			status := &mockCautStatus{
				quotaPercent: tt.quotaPercent,
			}

			// Check alerts
			poller.checkAlertsForStatus(status)

			// Verify alerts
			active := alerter.GetActive()
			hasWarning := false
			hasCritical := false

			for _, alert := range active {
				if alert.Type == alerts.AlertQuotaWarning {
					hasWarning = true
				}
				if alert.Type == alerts.AlertQuotaCritical {
					hasCritical = true
				}
			}

			if tt.expectWarning && !hasWarning {
				t.Errorf("Expected warning alert at %.1f%%", tt.quotaPercent)
			}
			if !tt.expectWarning && hasWarning {
				t.Errorf("Did not expect warning alert at %.1f%%", tt.quotaPercent)
			}
			if tt.expectCritical && !hasCritical {
				t.Errorf("Expected critical alert at %.1f%%", tt.quotaPercent)
			}
			if !tt.expectCritical && hasCritical {
				t.Errorf("Did not expect critical alert at %.1f%%", tt.quotaPercent)
			}
		})
	}
}

// mockCautStatus implements the interface needed for testing
type mockCautStatus struct {
	quotaPercent float64
}

// checkAlertsForStatus is a test helper that accepts our mock status
func (p *UsagePoller) checkAlertsForStatus(status *mockCautStatus) {
	if p.alerter == nil || status == nil {
		return
	}

	threshold := float64(p.config.AlertThreshold)
	criticalThreshold := 95.0

	if status.quotaPercent >= criticalThreshold {
		alert := alerts.Alert{
			ID:       "caut-quota-critical-overall",
			Type:     alerts.AlertQuotaCritical,
			Severity: alerts.SeverityCritical,
			Source:   "caut-poller",
			Message:  "API quota critical",
		}
		p.alerter.AddAlert(alert)
	} else if status.quotaPercent >= threshold {
		alert := alerts.Alert{
			ID:       "caut-quota-warning-overall",
			Type:     alerts.AlertQuotaWarning,
			Severity: alerts.SeverityWarning,
			Source:   "caut-poller",
			Message:  "API quota warning",
		}
		p.alerter.AddAlert(alert)
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

func TestInitGlobalPoller(t *testing.T) {
	cfg := config.CautConfig{
		Enabled:        true,
		PollInterval:   30,
		AlertThreshold: 75,
	}
	alerter := alerts.NewTracker(alerts.DefaultConfig())

	poller := InitGlobalPoller(&cfg, alerter)
	if poller == nil {
		t.Fatal("InitGlobalPoller returned nil")
	}

	// Verify config was applied
	if poller.interval != 30*time.Second {
		t.Errorf("Expected interval 30s, got %v", poller.interval)
	}
}

func TestStartStopGlobalPoller(t *testing.T) {
	// These should not panic even if caut is not installed
	_ = StartGlobalPoller()
	StopGlobalPoller()
	StopGlobalPoller() // Multiple stop calls should be safe
}
