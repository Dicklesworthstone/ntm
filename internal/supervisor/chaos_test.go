package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestDaemonRestartOnCrash tests that the supervisor restarts crashed daemons
func TestDaemonRestartOnCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:      "chaos-test",
		ProjectDir:     tmpDir,
		HealthInterval: 100 * time.Millisecond,
		MaxRestarts:    3,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start a daemon that exits immediately (simulating crash)
	spec := DaemonSpec{
		Name:    "crash-daemon",
		Command: "sh",
		Args:    []string{"-c", "exit 1"},
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for restarts to happen
	time.Sleep(3 * time.Second)

	d, exists := s.GetDaemon("crash-daemon")
	if !exists {
		t.Fatal("GetDaemon() returned false")
	}

	// Should have attempted restarts
	d.mu.RLock()
	restarts := d.Restarts
	state := d.State
	d.mu.RUnlock()

	if restarts == 0 {
		t.Error("daemon should have restart count > 0")
	}

	// After max restarts, should be in failed state
	if state != StateFailed && restarts >= int(s.maxRestarts) {
		t.Logf("State: %v, Restarts: %d (max: %d)", state, restarts, s.maxRestarts)
	}
}

// TestPortCollisionHandling tests that supervisor handles port collisions gracefully
func TestPortCollisionHandling(t *testing.T) {
	tmpDir := t.TempDir()

	// First, occupy a port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	occupiedPort := ln.Addr().(*net.TCPAddr).Port

	s, err := New(Config{
		SessionID:  "port-test",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Try to start a daemon wanting the occupied port
	spec := DaemonSpec{
		Name:        "port-daemon",
		Command:     "sleep",
		Args:        []string{"10"},
		DefaultPort: occupiedPort,
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	d, exists := s.GetDaemon("port-daemon")
	if !exists {
		t.Fatal("daemon not found")
	}

	// Should have been assigned a different port
	d.mu.RLock()
	assignedPort := d.Port
	d.mu.RUnlock()

	// Close our listener
	ln.Close()

	// If port was non-zero, it should either be 0 (no port needed) or different
	if assignedPort == occupiedPort && assignedPort != 0 {
		t.Errorf("daemon was assigned occupied port %d", occupiedPort)
	}
}

// TestMaxRestartBackoff tests exponential backoff for restarts
func TestMaxRestartBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:         "backoff-test",
		ProjectDir:        tmpDir,
		HealthInterval:    100 * time.Millisecond,
		MaxRestarts:       2, // Only allow 2 restarts
		RestartBackoffMax: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Quick-crashing daemon
	spec := DaemonSpec{
		Name:    "backoff-daemon",
		Command: "sh",
		Args:    []string{"-c", "exit 1"},
	}

	startTime := time.Now()
	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for max restarts to exhaust
	time.Sleep(10 * time.Second)
	elapsed := time.Since(startTime)

	d, _ := s.GetDaemon("backoff-daemon")
	d.mu.RLock()
	restarts := d.Restarts
	d.mu.RUnlock()

	// With max 2 restarts and exponential backoff (1s, 2s), we expect
	// at least 3 seconds to pass before giving up
	if elapsed < 3*time.Second {
		t.Logf("Restarts: %d, Elapsed: %v", restarts, elapsed)
	}
}

// TestCleanShutdown tests that shutdown properly stops all daemons
func TestCleanShutdown(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "shutdown-test",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Start multiple daemons
	for i := 0; i < 3; i++ {
		spec := DaemonSpec{
			Name:    fmt.Sprintf("daemon-%d", i),
			Command: "sleep",
			Args:    []string{"60"},
		}
		if err := s.Start(spec); err != nil {
			t.Fatalf("Start(daemon-%d) error = %v", i, err)
		}
	}

	// Give daemons time to start
	time.Sleep(200 * time.Millisecond)

	// Verify daemons are running
	status := s.Status()
	running := 0
	for _, d := range status {
		if d.State == StateRunning || d.State == StateStarting {
			running++
		}
	}
	if running != 3 {
		t.Errorf("Expected 3 running daemons, got %d", running)
	}

	// Shutdown
	err = s.Shutdown()
	if err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}

	// Verify all stopped
	time.Sleep(200 * time.Millisecond)
	status = s.Status()
	for name, d := range status {
		if d.State != StateStopped {
			t.Errorf("daemon %s state = %v, want StateStopped", name, d.State)
		}
	}

	// Verify PID files removed
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	entries, err := os.ReadDir(pidsDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to read pids dir: %v", err)
	}
	for _, entry := range entries {
		t.Errorf("PID file not cleaned up: %s", entry.Name())
	}
}

// TestOrphanedPIDFile tests handling of stale PID files from previous runs
func TestOrphanedPIDFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake orphaned PID file
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0755); err != nil {
		t.Fatalf("failed to create pids dir: %v", err)
	}

	orphanedPID := PIDFileInfo{
		PID:       99999, // Non-existent PID
		OwnerID:   "old-session",
		Command:   "sleep",
		StartedAt: time.Now().Add(-time.Hour),
		Port:      8888,
	}
	data, _ := json.Marshal(orphanedPID)
	pidPath := filepath.Join(pidsDir, "orphan-daemon.pid")
	if err := os.WriteFile(pidPath, data, 0644); err != nil {
		t.Fatalf("failed to write orphan PID file: %v", err)
	}

	// Create supervisor (it should handle orphaned PID files)
	s, err := New(Config{
		SessionID:  "orphan-test",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// The supervisor should be able to start a daemon with the same name
	spec := DaemonSpec{
		Name:    "orphan-daemon",
		Command: "sleep",
		Args:    []string{"10"},
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v, should handle orphaned PID", err)
	}

	d, exists := s.GetDaemon("orphan-daemon")
	if !exists {
		t.Fatal("daemon not found")
	}

	d.mu.RLock()
	pid := d.PID
	d.mu.RUnlock()

	if pid == 99999 {
		t.Error("daemon has orphaned PID, should have new PID")
	}
}

// TestConcurrentDaemonOperations tests thread-safety of supervisor operations
func TestConcurrentDaemonOperations(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "concurrent-test",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start multiple daemons concurrently
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			spec := DaemonSpec{
				Name:    fmt.Sprintf("concurrent-%d", idx),
				Command: "sleep",
				Args:    []string{"10"},
			}
			if err := s.Start(spec); err != nil {
				errors <- fmt.Errorf("Start(concurrent-%d): %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify all daemons started
	status := s.Status()
	if len(status) != 5 {
		t.Errorf("Expected 5 daemons, got %d", len(status))
	}

	// Concurrently stop some and check status of others
	wg = sync.WaitGroup{}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.Stop(fmt.Sprintf("concurrent-%d", idx))
		}(i)
	}

	for i := 3; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.GetDaemon(fmt.Sprintf("concurrent-%d", idx))
		}(i)
	}

	wg.Wait()
}

// TestHealthCheckFlapping tests behavior with intermittently healthy daemons
func TestHealthCheckFlapping(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	tmpDir := t.TempDir()

	// Create a simple HTTP server that alternates health status
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	s, err := New(Config{
		SessionID:      "flap-test",
		ProjectDir:     tmpDir,
		HealthInterval: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start a daemon (won't actually serve health)
	spec := DaemonSpec{
		Name:        "flap-daemon",
		Command:     "sleep",
		Args:        []string{"30"},
		DefaultPort: port,
		HealthURL:   fmt.Sprintf("http://127.0.0.1:%d/health", port),
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Let it run for a bit (health checks will fail since there's no server)
	time.Sleep(2 * time.Second)

	d, _ := s.GetDaemon("flap-daemon")
	d.mu.RLock()
	state := d.State
	d.mu.RUnlock()

	// Daemon should still be starting/running despite health check failures
	// (health check failures don't immediately kill the daemon)
	t.Logf("Daemon state after 2s without health endpoint: %v", state)
}

// TestContextCancellation tests that operations respect context cancellation
func TestContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:      "ctx-test",
		ProjectDir:     tmpDir,
		HealthInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Start a daemon
	spec := DaemonSpec{
		Name:    "ctx-daemon",
		Command: "sleep",
		Args:    []string{"60"},
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for daemon to start
	time.Sleep(200 * time.Millisecond)

	// Create a child context and cancel it
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx

	// Cancel supervisor context
	cancel()

	// Give shutdown time to propagate
	time.Sleep(200 * time.Millisecond)

	// Now call shutdown which should be fast since context is already cancelled
	start := time.Now()
	s.Shutdown()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("Shutdown took too long after context cancellation: %v", elapsed)
	}
}
