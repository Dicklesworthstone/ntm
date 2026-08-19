package tools

import (
	"context"
	"testing"
	"time"
)

type slowMockAdapter struct {
	*mockAdapter
	delay time.Duration
}

func (s *slowMockAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	time.Sleep(s.delay)
	return s.mockAdapter.Info(ctx)
}

func TestGetAllInfo_Concurrency(t *testing.T) {
	// Setup registry with slow adapters
	reg := newTestRegistry()
	delay := 100 * time.Millisecond
	count := 10

	for i := 0; i < count; i++ {
		name := ToolName(string(ToolBV) + "-" + string(rune(i))) // dummy names
		adapter := &slowMockAdapter{
			mockAdapter: newMockAdapter(name, true),
			delay:       delay,
		}
		reg.Register(adapter)
	}

	start := time.Now()
	reg.GetAllInfo(context.Background())
	duration := time.Since(start)

	// If sequential, duration ~= count * delay (1s)
	// If parallel, duration ~= delay (100ms) + overhead
	// We check if it took longer than half of sequential time to prove it's not parallel enough
	expectedSequential := time.Duration(count) * delay
	if duration > expectedSequential/2 {
		t.Logf("GetAllInfo took %v (expected ~%v for sequential, ~%v for parallel)", duration, expectedSequential, delay)
		// This confirms it IS sequential currently.
		// We want to fail if we expected parallel, but here we just demonstrate it.
		// For the purpose of "fixing", I will assert that it should be faster.
		t.Errorf("GetAllInfo is too slow (%v), expected closer to %v", duration, delay)
	}
}
