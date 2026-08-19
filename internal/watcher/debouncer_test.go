package watcher

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestNewDebouncer(t *testing.T) {
	t.Run("default duration", func(t *testing.T) {
		d := NewDebouncer(0)
		if d.Duration() != DefaultDebounceDuration {
			t.Errorf("Duration() = %v, want %v", d.Duration(), DefaultDebounceDuration)
		}
	})

	t.Run("custom duration", func(t *testing.T) {
		duration := 500 * time.Millisecond
		d := NewDebouncer(duration)
		if d.Duration() != duration {
			t.Errorf("Duration() = %v, want %v", d.Duration(), duration)
		}
	})
}

func TestDebouncerTrigger(t *testing.T) {
	t.Run("single trigger", func(t *testing.T) {
		var callCount atomic.Int32
		d := NewDebouncer(50 * time.Millisecond)

		d.Trigger(func() {
			callCount.Add(1)
		})

		// Wait for debounce
		time.Sleep(100 * time.Millisecond)

		if got := callCount.Load(); got != 1 {
			t.Errorf("callback called %d times, want 1", got)
		}
	})

	t.Run("multiple rapid triggers", func(t *testing.T) {
		var callCount atomic.Int32
		d := NewDebouncer(100 * time.Millisecond)

		// Trigger multiple times rapidly
		for i := 0; i < 5; i++ {
			d.Trigger(func() {
				callCount.Add(1)
			})
			time.Sleep(10 * time.Millisecond)
		}

		// Wait for debounce
		time.Sleep(150 * time.Millisecond)

		// Should only have been called once due to debouncing
		if got := callCount.Load(); got != 1 {
			t.Errorf("callback called %d times, want 1", got)
		}
	})

	t.Run("triggers with delay between", func(t *testing.T) {
		var callCount atomic.Int32
		d := NewDebouncer(50 * time.Millisecond)

		d.Trigger(func() {
			callCount.Add(1)
		})

		// Wait for first to complete
		time.Sleep(100 * time.Millisecond)

		d.Trigger(func() {
			callCount.Add(1)
		})

		// Wait for second to complete
		time.Sleep(100 * time.Millisecond)

		// Should have been called twice (once per non-overlapping trigger)
		if got := callCount.Load(); got != 2 {
			t.Errorf("callback called %d times, want 2", got)
		}
	})
}

func TestDebouncerCancel(t *testing.T) {
	var callCount atomic.Int32
	d := NewDebouncer(100 * time.Millisecond)

	d.Trigger(func() {
		callCount.Add(1)
	})

	// Cancel before debounce expires
	time.Sleep(20 * time.Millisecond)
	d.Cancel()

	// Wait longer than debounce would have taken
	time.Sleep(150 * time.Millisecond)

	if got := callCount.Load(); got != 0 {
		t.Errorf("callback called %d times after Cancel(), want 0", got)
	}
}

func TestDebouncerCancelNilTimer(t *testing.T) {
	d := NewDebouncer(50 * time.Millisecond)
	// Cancel without any trigger should not panic
	d.Cancel()
}

func TestDefaultDebounceDuration(t *testing.T) {
	if DefaultDebounceDuration != 250*time.Millisecond {
		t.Errorf("DefaultDebounceDuration = %v, want 250ms", DefaultDebounceDuration)
	}
}
