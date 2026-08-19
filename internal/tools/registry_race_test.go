package tools

import (
	"context"
	"sync"
	"testing"
)

func TestRegistry_ParallelRace(t *testing.T) {
	// Setup registry with many adapters to maximize chance of race
	reg := newTestRegistry()
	count := 100

	for i := 0; i < count; i++ {
		name := ToolName("tool-" + string(rune(i)))
		// Reuse mockAdapter from mock_test.go
		adapter := newMockAdapter(name, true)
		reg.Register(adapter)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrently call GetAllInfo and GetHealthReport
	go func() {
		defer wg.Done()
		reg.GetAllInfo(context.Background())
	}()

	go func() {
		defer wg.Done()
		reg.GetHealthReport(context.Background())
	}()

	wg.Wait()
}
