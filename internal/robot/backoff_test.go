package robot

import (
	"testing"
)

func TestBackoffManager_Basic(t *testing.T) {
	manager := NewBackoffManager("test-session")

	// Initially, no backoff
	if remaining := manager.GetBackoffRemaining("%1"); remaining != 0 {
		t.Errorf("Expected 0 remaining, got %v", remaining)
	}
}
