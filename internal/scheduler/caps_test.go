package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentCaps_TryAcquire(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 2,
		},
		PerAgent: map[string]AgentCapConfig{
			"cod": {
				MaxConcurrent: 1, // Only 1 cod at a time
			},
		},
	}

	caps := NewAgentCaps(cfg)

	// Should acquire first cod
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire first cod")
	}

	// Should NOT acquire second cod (at cap)
	if caps.TryAcquire("cod") {
		t.Error("expected to fail acquiring second cod")
	}

	// cc should still work (uses default cap of 2)
	if !caps.TryAcquire("cc") {
		t.Error("expected to acquire first cc")
	}
	if !caps.TryAcquire("cc") {
		t.Error("expected to acquire second cc")
	}
	// Third cc should fail
	if caps.TryAcquire("cc") {
		t.Error("expected to fail acquiring third cc")
	}

	// Release cod and try again
	caps.Release("cod")
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire cod after release")
	}
}

func TestAgentCaps_Acquire_Blocking(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 1,
		},
	}

	caps := NewAgentCaps(cfg)

	// Acquire first slot
	if err := caps.Acquire(context.Background(), "cc"); err != nil {
		t.Fatalf("failed to acquire first slot: %v", err)
	}

	// Second acquire should block until timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := caps.Acquire(ctx, "cc")
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Release and the next acquire should succeed
	caps.Release("cc")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()

	if err := caps.Acquire(ctx2, "cc"); err != nil {
		t.Errorf("expected acquire to succeed after release, got %v", err)
	}
}

func TestAgentCaps_RampUp(t *testing.T) {
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cod": {
				MaxConcurrent:  3,
				RampUpEnabled:  true,
				RampUpInitial:  1,
				RampUpStep:     1,
				RampUpInterval: 100 * time.Millisecond, // Short for testing
			},
		},
	}

	caps := NewAgentCaps(cfg)

	// Initially should only allow 1
	if cap := caps.GetCurrentCap("cod"); cap != 1 {
		t.Errorf("expected initial cap 1, got %d", cap)
	}

	// Acquire first - should succeed
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire first cod")
	}

	// Second should fail (at initial cap)
	if caps.TryAcquire("cod") {
		t.Error("expected to fail acquiring second cod before ramp-up")
	}

	// Wait for ramp-up
	caps.Release("cod")
	time.Sleep(150 * time.Millisecond)

	// Cap should have increased
	if cap := caps.GetCurrentCap("cod"); cap < 2 {
		t.Errorf("expected cap >= 2 after ramp-up, got %d", cap)
	}

	// Now should be able to acquire 2
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire cod after ramp-up")
	}
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire second cod after ramp-up")
	}
}

func TestAgentCaps_Cooldown(t *testing.T) {
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cod": {
				MaxConcurrent:     3,
				CooldownOnFailure: true,
				CooldownReduction: 1,
				CooldownRecovery:  200 * time.Millisecond, // Short for testing
			},
		},
	}

	caps := NewAgentCaps(cfg)

	// Initial cap should be 3
	if cap := caps.GetCurrentCap("cod"); cap != 3 {
		t.Errorf("expected initial cap 3, got %d", cap)
	}

	// Acquire one and record failure
	caps.TryAcquire("cod")
	caps.RecordFailure("cod")
	caps.Release("cod")

	// Cap should be reduced
	if cap := caps.GetCurrentCap("cod"); cap != 2 {
		t.Errorf("expected cap 2 after failure, got %d", cap)
	}

	// Wait for recovery
	time.Sleep(250 * time.Millisecond)

	// Cap should be restored
	if cap := caps.GetCurrentCap("cod"); cap != 3 {
		t.Errorf("expected cap restored to 3, got %d", cap)
	}
}

func TestAgentCaps_GlobalMax(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 5, // Per-type cap is higher than global
		},
		GlobalMax: 3, // But global max is 3
	}

	caps := NewAgentCaps(cfg)

	// Acquire 2 cc
	if !caps.TryAcquire("cc") {
		t.Error("expected to acquire first cc")
	}
	if !caps.TryAcquire("cc") {
		t.Error("expected to acquire second cc")
	}

	// Acquire 1 cod - should work (total = 3, at global max)
	if !caps.TryAcquire("cod") {
		t.Error("expected to acquire first cod")
	}

	// No more should be allowed even though per-type caps allow it
	if caps.TryAcquire("cc") {
		t.Error("expected global max to block cc")
	}
	if caps.TryAcquire("cod") {
		t.Error("expected global max to block cod")
	}
	if caps.TryAcquire("gmi") {
		t.Error("expected global max to block gmi")
	}

	// Release one and try again
	caps.Release("cc")
	if !caps.TryAcquire("gmi") {
		t.Error("expected gmi to work after release")
	}
}

func TestAgentCaps_Stats(t *testing.T) {
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cc":  {MaxConcurrent: 3},
			"cod": {MaxConcurrent: 2, RampUpEnabled: true, RampUpInitial: 1, RampUpStep: 1, RampUpInterval: time.Minute},
		},
	}

	caps := NewAgentCaps(cfg)

	// Acquire some slots
	caps.TryAcquire("cc")
	caps.TryAcquire("cc")
	caps.TryAcquire("cod")

	stats := caps.Stats()

	if stats.TotalRunning != 3 {
		t.Errorf("expected total running 3, got %d", stats.TotalRunning)
	}

	ccStats, ok := stats.PerAgent["cc"]
	if !ok {
		t.Fatal("expected cc in stats")
	}
	if ccStats.Running != 2 {
		t.Errorf("expected cc running 2, got %d", ccStats.Running)
	}
	if ccStats.MaxCap != 3 {
		t.Errorf("expected cc max cap 3, got %d", ccStats.MaxCap)
	}

	codStats, ok := stats.PerAgent["cod"]
	if !ok {
		t.Fatal("expected cod in stats")
	}
	if codStats.Running != 1 {
		t.Errorf("expected cod running 1, got %d", codStats.Running)
	}
	if !codStats.InRampUp {
		t.Error("expected cod to be in ramp-up")
	}
}

func TestAgentCaps_Concurrent(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 5,
		},
	}

	caps := NewAgentCaps(cfg)
	var acquired int32
	var wg sync.WaitGroup

	// Try to acquire 10 concurrently, but only 5 should succeed immediately
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if caps.TryAcquire("cc") {
				atomic.AddInt32(&acquired, 1)
			}
		}()
	}

	wg.Wait()

	if acquired != 5 {
		t.Errorf("expected 5 acquired, got %d", acquired)
	}

	if caps.GetRunning("cc") != 5 {
		t.Errorf("expected running 5, got %d", caps.GetRunning("cc"))
	}
}

func TestAgentCaps_WaiterNotification(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 1,
		},
	}

	caps := NewAgentCaps(cfg)

	// Acquire the only slot
	caps.TryAcquire("cc")

	var acquired atomic.Bool
	done := make(chan struct{})

	// Start a goroutine that will wait for a slot
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := caps.Acquire(ctx, "cc"); err == nil {
			acquired.Store(true)
		}
		close(done)
	}()

	// Give time for the goroutine to start waiting
	time.Sleep(50 * time.Millisecond)

	// Release the slot - should notify the waiter
	caps.Release("cc")

	// Wait for the goroutine to finish
	select {
	case <-done:
		if !acquired.Load() {
			t.Error("waiter should have acquired after release")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not complete in time")
	}
}

func TestCodexCapConfig(t *testing.T) {
	cfg := CodexCapConfig()

	if cfg.MaxConcurrent != 3 {
		t.Errorf("expected max concurrent 3, got %d", cfg.MaxConcurrent)
	}
	if !cfg.RampUpEnabled {
		t.Error("expected ramp-up enabled for codex")
	}
	if cfg.RampUpInitial != 1 {
		t.Errorf("expected ramp-up initial 1, got %d", cfg.RampUpInitial)
	}
	if cfg.RampUpInterval != 60*time.Second {
		t.Errorf("expected ramp-up interval 60s, got %v", cfg.RampUpInterval)
	}
	if !cfg.CooldownOnFailure {
		t.Error("expected cooldown on failure for codex")
	}
}

func TestDefaultAgentCapsConfig(t *testing.T) {
	cfg := DefaultAgentCapsConfig()

	// Check codex has special config
	codCfg, ok := cfg.PerAgent["cod"]
	if !ok {
		t.Fatal("expected cod in per-agent config")
	}
	if !codCfg.RampUpEnabled {
		t.Error("expected ramp-up enabled for cod")
	}
	if codCfg.MaxConcurrent != 3 {
		t.Errorf("expected cod max concurrent 3, got %d", codCfg.MaxConcurrent)
	}

	// Check cc has default config
	ccCfg, ok := cfg.PerAgent["cc"]
	if !ok {
		t.Fatal("expected cc in per-agent config")
	}
	if ccCfg.MaxConcurrent != 4 {
		t.Errorf("expected cc max concurrent 4, got %d", ccCfg.MaxConcurrent)
	}

	// Check gmi has higher limit
	gmiCfg, ok := cfg.PerAgent["gmi"]
	if !ok {
		t.Fatal("expected gmi in per-agent config")
	}
	if gmiCfg.MaxConcurrent != 5 {
		t.Errorf("expected gmi max concurrent 5, got %d", gmiCfg.MaxConcurrent)
	}
}

func TestAgentCaps_SetCap(t *testing.T) {
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{
			MaxConcurrent: 3,
		},
	}

	caps := NewAgentCaps(cfg)

	// Acquire 2 slots
	caps.TryAcquire("cc")
	caps.TryAcquire("cc")

	// Reduce cap to 2
	caps.SetCap("cc", 2)

	// Third acquire should fail now
	if caps.TryAcquire("cc") {
		t.Error("expected third acquire to fail after cap reduction")
	}

	// Increase cap to 4
	caps.SetCap("cc", 4)

	// Third and fourth should work
	if !caps.TryAcquire("cc") {
		t.Error("expected third acquire to work after cap increase")
	}
	if !caps.TryAcquire("cc") {
		t.Error("expected fourth acquire to work after cap increase")
	}
}

func TestAgentCaps_ForceRampUp(t *testing.T) {
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cod": {
				MaxConcurrent:  5,
				RampUpEnabled:  true,
				RampUpInitial:  1,
				RampUpStep:     1,
				RampUpInterval: time.Hour, // Very slow
			},
		},
	}

	caps := NewAgentCaps(cfg)

	// Initial cap should be 1
	if cap := caps.GetCurrentCap("cod"); cap != 1 {
		t.Errorf("expected initial cap 1, got %d", cap)
	}

	// Force ramp-up
	caps.ForceRampUp("cod")

	// Cap should be at max now
	if cap := caps.GetCurrentCap("cod"); cap != 5 {
		t.Errorf("expected cap 5 after force ramp-up, got %d", cap)
	}
}
