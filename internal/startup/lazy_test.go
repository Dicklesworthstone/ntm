package startup

import (
	"errors"
	"sync"
	"testing"
)

func TestLazyInitialization(t *testing.T) {
	Reset()

	initCalled := 0
	lazy := NewLazy[string]("test_lazy", func() (string, error) {
		initCalled++
		return "test_value", nil
	})

	// Value shouldn't be initialized yet
	if lazy.IsInitialized() {
		t.Error("Should not be initialized before Get()")
	}
	if initCalled != 0 {
		t.Error("Init should not have been called yet")
	}

	// First Get should initialize
	val, err := lazy.Get()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if val != "test_value" {
		t.Errorf("Expected 'test_value', got '%s'", val)
	}
	if initCalled != 1 {
		t.Errorf("Init should have been called once, called %d times", initCalled)
	}

	// Second Get should not re-initialize
	val2, err := lazy.Get()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if val2 != "test_value" {
		t.Errorf("Expected 'test_value', got '%s'", val2)
	}
	if initCalled != 1 {
		t.Errorf("Init should still be 1, got %d", initCalled)
	}
}

func TestLazyInitializationError(t *testing.T) {
	Reset()

	expectedErr := errors.New("init failed")
	lazy := NewLazy[string]("test_lazy_err", func() (string, error) {
		return "", expectedErr
	})

	val, err := lazy.Get()
	if err != expectedErr {
		t.Errorf("Expected error %v, got %v", expectedErr, err)
	}
	if val != "" {
		t.Errorf("Expected empty string on error, got '%s'", val)
	}
}

func TestLazyConcurrency(t *testing.T) {
	Reset()

	initCalled := 0
	var mu sync.Mutex
	lazy := NewLazy[int]("test_concurrent", func() (int, error) {
		mu.Lock()
		initCalled++
		mu.Unlock()
		return 100, nil
	})

	// Launch multiple goroutines to call Get() simultaneously
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := lazy.Get()
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if val != 100 {
				t.Errorf("Expected 100, got %d", val)
			}
		}()
	}
	wg.Wait()

	// Init should only be called once despite concurrent access
	if initCalled != 1 {
		t.Errorf("Init should have been called exactly once, called %d times", initCalled)
	}
}
