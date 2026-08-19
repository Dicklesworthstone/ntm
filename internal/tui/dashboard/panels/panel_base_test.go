package panels

import (
	"testing"
	"time"
)

// testPanelConfig mirrors the removed DefaultPanelConfig defaults for
// exercising live PanelBase behavior.
func testPanelConfig(id, title string) PanelConfig {
	return PanelConfig{
		ID:              id,
		Title:           title,
		Priority:        PriorityNormal,
		RefreshInterval: 5 * time.Second,
		MinWidth:        20,
		MinHeight:       5,
		Collapsible:     true,
	}
}

func TestPanelBaseSizeAndFocus(t *testing.T) {
	t.Parallel()

	cfg := testPanelConfig("alerts", "Alerts")
	base := NewPanelBase(cfg)

	base.SetSize(80, 20)
	if base.Width() != 80 {
		t.Errorf("Width() = %d", base.Width())
	}
	if base.Height() != 20 {
		t.Errorf("Height() = %d", base.Height())
	}

	base.Focus()
	if !base.IsFocused() {
		t.Error("IsFocused() should be true after Focus()")
	}
	base.Blur()
	if base.IsFocused() {
		t.Error("IsFocused() should be false after Blur()")
	}
}

func TestPanelBaseLastUpdate(t *testing.T) {
	t.Parallel()

	cfg := testPanelConfig("beads", "Beads")
	base := NewPanelBase(cfg)
	now := time.Now()
	base.SetLastUpdate(now)

	if got := base.LastUpdate(); !got.Equal(now) {
		t.Errorf("LastUpdate() = %v, want %v", got, now)
	}
}

func TestPanelBaseRetryTracking(t *testing.T) {
	t.Parallel()

	cfg := testPanelConfig("cass", "CASS")
	base := NewPanelBase(cfg)

	if base.IsRetrying() {
		t.Error("IsRetrying() should be false initially")
	}
	if base.RetryCount() != 0 {
		t.Errorf("RetryCount() = %d", base.RetryCount())
	}

	base.StartRetry()
	if !base.IsRetrying() {
		t.Error("IsRetrying() should be true after StartRetry()")
	}
	if base.RetryCount() != 1 {
		t.Errorf("RetryCount() = %d, want 1", base.RetryCount())
	}

	base.EndRetry(false)
	if base.IsRetrying() {
		t.Error("IsRetrying() should be false after EndRetry(false)")
	}
	if base.RetryCount() != 1 {
		t.Errorf("RetryCount() = %d, want 1 after failed retry", base.RetryCount())
	}

	base.StartRetry()
	base.EndRetry(true)
	if base.RetryCount() != 0 {
		t.Errorf("RetryCount() = %d, want 0 after EndRetry(true)", base.RetryCount())
	}

	base.StartRetry()
	base.ResetRetry()
	if base.IsRetrying() {
		t.Error("IsRetrying() should be false after ResetRetry()")
	}
	if base.RetryCount() != 0 {
		t.Errorf("RetryCount() = %d, want 0 after ResetRetry()", base.RetryCount())
	}
}

func TestPanelBaseMaxRetries(t *testing.T) {
	t.Parallel()

	cfg := testPanelConfig("history", "History")
	base := NewPanelBase(cfg)

	if base.MaxRetries() != 0 {
		t.Errorf("MaxRetries() = %d, want 0", base.MaxRetries())
	}

	base.SetMaxRetries(3)
	if base.MaxRetries() != 3 {
		t.Errorf("MaxRetries() = %d, want 3", base.MaxRetries())
	}
}

func TestPanelBaseKeybindingsDefault(t *testing.T) {
	t.Parallel()

	cfg := testPanelConfig("metrics", "Metrics")
	base := NewPanelBase(cfg)
	if base.Keybindings() != nil {
		t.Error("Keybindings() should return nil by default")
	}
}
