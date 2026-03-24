package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{
				SessionID:  "test-session",
				ProjectDir: tmpDir,
			},
			wantErr: false,
		},
		{
			name: "missing session ID",
			cfg: Config{
				ProjectDir: tmpDir,
			},
			wantErr: true,
		},
		{
			name: "missing project dir",
			cfg: Config{
				SessionID: "test-session",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && s == nil {
				t.Error("New() returned nil supervisor without error")
			}
			if s != nil {
				s.Shutdown()
			}
		})
	}
}

func TestSupervisorCreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Check that .ntm/pids and .ntm/logs exist
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	logsDir := filepath.Join(tmpDir, ".ntm", "logs")

	if _, err := os.Stat(pidsDir); os.IsNotExist(err) {
		t.Errorf("pids directory not created: %v", err)
	}
	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		t.Errorf("logs directory not created: %v", err)
	}
}

func TestPortAllocation(t *testing.T) {
	// Test isPortAvailable
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// Port should not be available while listener is active
	if isPortAvailable(port) {
		t.Error("isPortAvailable() returned true for occupied port")
	}

	ln.Close()

	// Port should be available after listener is closed
	if !isPortAvailable(port) {
		t.Error("isPortAvailable() returned false for free port")
	}
}

func TestFindAvailablePort(t *testing.T) {
	port, err := findAvailablePort()
	if err != nil {
		t.Fatalf("findAvailablePort() error = %v", err)
	}

	if port < 1024 || port > 65535 {
		t.Errorf("findAvailablePort() returned invalid port: %d", port)
	}

	// The port should be available
	if !isPortAvailable(port) {
		t.Error("findAvailablePort() returned unavailable port")
	}
}

func TestStartStopDaemon(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:      "test-session",
		ProjectDir:     tmpDir,
		HealthInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start a simple daemon (use 'sleep' as a mock daemon)
	spec := DaemonSpec{
		Name:        "test-daemon",
		Command:     "sleep",
		Args:        []string{"10"},
		DefaultPort: 0, // No port needed for sleep
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Check daemon is tracked
	d, exists := s.GetDaemon("test-daemon")
	if !exists {
		t.Fatal("GetDaemon() returned false for started daemon")
	}

	if d.PID <= 0 {
		t.Error("daemon has invalid PID")
	}

	if d.State != StateStarting && d.State != StateRunning {
		t.Errorf("daemon state = %v, want StateStarting or StateRunning", d.State)
	}

	// Stop the daemon
	err = s.Stop("test-daemon")
	if err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	// Give it time to stop
	time.Sleep(100 * time.Millisecond)

	d, exists = s.GetDaemon("test-daemon")
	if !exists {
		t.Fatal("GetDaemon() returned false after stop")
	}

	if d.State != StateStopped {
		t.Errorf("daemon state = %v, want StateStopped", d.State)
	}
}

func TestStartDuplicateDaemon(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	spec := DaemonSpec{
		Name:    "test-daemon",
		Command: "sleep",
		Args:    []string{"10"},
	}

	err = s.Start(spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for daemon to start
	time.Sleep(100 * time.Millisecond)

	// Try to start again
	err = s.Start(spec)
	if err == nil {
		t.Error("Start() should return error for duplicate daemon")
	}
}

func TestStopNonExistent(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	err = s.Stop("nonexistent")
	if err == nil {
		t.Error("Stop() should return error for nonexistent daemon")
	}
}

func TestStatus(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start two daemons
	spec1 := DaemonSpec{Name: "daemon1", Command: "sleep", Args: []string{"10"}}
	spec2 := DaemonSpec{Name: "daemon2", Command: "sleep", Args: []string{"10"}}

	if err := s.Start(spec1); err != nil {
		t.Fatalf("Start(daemon1) error = %v", err)
	}
	if err := s.Start(spec2); err != nil {
		t.Fatalf("Start(daemon2) error = %v", err)
	}

	status := s.Status()
	if len(status) != 2 {
		t.Errorf("Status() returned %d daemons, want 2", len(status))
	}

	if _, ok := status["daemon1"]; !ok {
		t.Error("Status() missing daemon1")
	}
	if _, ok := status["daemon2"]; !ok {
		t.Error("Status() missing daemon2")
	}
}

func TestPIDFile(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	spec := DaemonSpec{
		Name:    "pid-test",
		Command: "sleep",
		Args:    []string{"10"},
	}

	if err := s.Start(spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for daemon to start
	time.Sleep(100 * time.Millisecond)

	// Check PID file exists
	pidPath := s.pidPath("pid-test")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("failed to read PID file: %v", err)
	}

	var info PIDFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("failed to parse PID file: %v", err)
	}

	if info.PID <= 0 {
		t.Error("PID file has invalid PID")
	}
	if info.OwnerID != "test-session" {
		t.Errorf("PID file OwnerID = %q, want %q", info.OwnerID, "test-session")
	}

	// Stop daemon
	if err := s.Stop("pid-test"); err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file not removed after stop")
	}
}

func TestStopAll(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start multiple daemons
	for i := 0; i < 3; i++ {
		spec := DaemonSpec{
			Name:    fmt.Sprintf("daemon%d", i),
			Command: "sleep",
			Args:    []string{"10"},
		}
		if err := s.Start(spec); err != nil {
			t.Fatalf("Start(daemon%d) error = %v", i, err)
		}
	}

	// StopAll
	if err := s.StopAll(); err != nil {
		t.Errorf("StopAll() error = %v", err)
	}

	// All should be stopped
	time.Sleep(100 * time.Millisecond)
	status := s.Status()
	for name, d := range status {
		if d.State != StateStopped {
			t.Errorf("daemon %s state = %v, want StateStopped", name, d.State)
		}
	}
}

func TestDefaultSpecs(t *testing.T) {
	specs := DefaultSpecs()
	if len(specs) == 0 {
		t.Error("DefaultSpecs() returned empty slice")
	}

	// Check we have the expected daemons
	names := make(map[string]bool)
	for _, s := range specs {
		names[s.Name] = true
	}

	if !names["cm"] {
		t.Error("DefaultSpecs() missing 'cm' daemon")
	}
	if !names["am"] {
		t.Error("DefaultSpecs() missing 'am' daemon")
	}
}

// TestHealthCheck tests the HTTP health check functionality
func TestHealthCheck(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Start a test HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/health/liveness", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	go server.Serve(ln)
	defer server.Shutdown(context.Background())

	// Test checkHealthHTTP
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health/liveness", port)
	if !s.checkHealthHTTP(healthURL) {
		t.Error("checkHealthHTTP() returned false for healthy endpoint")
	}

	// Test with invalid URL
	if s.checkHealthHTTP("http://127.0.0.1:99999/health/liveness") {
		t.Error("checkHealthHTTP() returned true for invalid endpoint")
	}
}

// TestHealthCheckCmd tests the command-based health check functionality
func TestHealthCheckCmd(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Test with successful command
	if !s.checkHealthCmd([]string{"echo", "ok"}) {
		t.Error("checkHealthCmd() returned false for successful command")
	}

	// Test with failed command
	if s.checkHealthCmd([]string{"false"}) {
		t.Error("checkHealthCmd() returned true for failed command")
	}
}

func TestHandleDaemonFailure_IdempotentWhenAlreadyFailed(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// Force ctx done so handleDaemonFailure exits quickly (no time.After backoff).
	s.cancel()

	d := &ManagedDaemon{
		Spec: DaemonSpec{
			Name:    "test-daemon",
			Command: "true",
		},
		State:    StateRunning,
		Restarts: 0,
		OwnerID:  "test-session",
	}

	s.handleDaemonFailure(d)
	if d.Restarts != 1 {
		t.Fatalf("Restarts = %d, want 1", d.Restarts)
	}
	if d.State != StateFailed {
		t.Fatalf("State = %v, want %v", d.State, StateFailed)
	}

	// A second failure signal should be ignored.
	s.handleDaemonFailure(d)
	if d.Restarts != 1 {
		t.Fatalf("Restarts after 2nd call = %d, want 1", d.Restarts)
	}
}
