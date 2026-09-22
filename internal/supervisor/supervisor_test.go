package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	err = supervisorStopForTest(s, "test-daemon")
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

func TestStopDaemonDeliversGracefulTermination(t *testing.T) {
	tmpDir := t.TempDir()
	markerPath := filepath.Join(tmpDir, "terminated")
	readyPath := filepath.Join(tmpDir, "ready")

	s, err := New(Config{
		SessionID:  "graceful-stop",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// A CommandContext cancellation kills the shell before its TERM trap can
	// run. Stop must instead deliver the platform graceful signal first.
	if err := s.Start(DaemonSpec{
		Name:    "graceful-daemon",
		Command: "sh",
		Args: []string{
			"-c",
			`trap 'echo terminated > "$1"; exit 0' TERM; echo ready > "$2"; while :; do :; done`,
			"sh",
			markerPath,
			readyPath,
		},
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("daemon did not install TERM trap: %v", err)
	}

	if err := supervisorStopForTest(s, "graceful-daemon"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("TERM trap marker missing: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "terminated" {
		t.Fatalf("TERM trap marker = %q, want %q", got, "terminated")
	}
}

func TestExitedDaemonClosesSupersededLogFile(t *testing.T) {
	tmpDir := t.TempDir()
	s, err := New(Config{
		SessionID:   "crash-log-close",
		ProjectDir:  tmpDir,
		MaxRestarts: 1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	if err := s.Start(DaemonSpec{
		Name:    "crashing-daemon",
		Command: "sh",
		Args:    []string{"-c", "exit 1"},
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	s.mu.RLock()
	first := s.daemons["crashing-daemon"]
	s.mu.RUnlock()
	if first == nil {
		t.Fatal("initial daemon was not registered")
	}

	// The first crash waits one second before installing its replacement. The
	// first ManagedDaemon must then release the log descriptor it no longer owns.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		first.mu.RLock()
		closed := first.logFile == nil
		first.mu.RUnlock()
		if closed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("exited daemon retained its superseded log file")
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

func TestStartRejectsLiveAndRecoveringGenerations(t *testing.T) {
	s, err := New(Config{SessionID: "start-admission", ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	spec := DaemonSpec{Name: "daemon", Command: "sleep", Args: []string{"60"}}
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	original := s.daemons[spec.Name]
	s.mu.RUnlock()
	defer func() {
		original.mu.Lock()
		original.State = StateStarting
		original.mu.Unlock()
		_ = s.stopDaemon(original)
	}()

	for _, state := range []DaemonState{StateStarting, StateRunning, StateUnhealthy, StateStopping, StateRestarting} {
		t.Run(string(state), func(t *testing.T) {
			original.mu.Lock()
			original.State = state
			original.mu.Unlock()
			if err := s.Start(spec); err == nil {
				t.Fatalf("Start replaced a %s daemon", state)
			}
			current, _ := s.GetDaemon(spec.Name)
			if current.PID != original.PID {
				t.Fatalf("Start changed managed PID from %d to %d", original.PID, current.PID)
			}
		})
	}

	// Health transitions race with external Start calls in a real supervisor.
	// Both states reject the duplicate, and admission must read them under mu.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 250 {
			original.mu.Lock()
			original.State = StateStarting
			original.mu.Unlock()
			original.mu.Lock()
			original.State = StateRunning
			original.mu.Unlock()
		}
	}()
	for range 250 {
		if err := s.Start(spec); err == nil {
			t.Error("Start replaced a live daemon during a health transition")
		}
	}
	wg.Wait()
}

func restartTestSpec(t *testing.T) DaemonSpec {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("restart fault injection requires unix executable permissions")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sleepPath)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "daemon")
	if err := os.WriteFile(command, data, 0755); err != nil {
		t.Fatal(err)
	}
	return DaemonSpec{Name: "daemon", Command: command, Args: []string{"60"}}
}

func waitForRestartCount(t *testing.T, s *Supervisor, name string, count int) *ManagedDaemon {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := s.GetDaemon(name); ok && d.Restarts >= count {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon %s never reached restart attempt %d", name, count)
	return nil
}

func TestRestartRetriesLaunchFailuresAndRecovers(t *testing.T) {
	for _, fault := range []string{"missing binary", "permission denied", "occupied port"} {
		t.Run(fault, func(t *testing.T) {
			spec := restartTestSpec(t)
			port, err := findAvailablePort()
			if err != nil {
				t.Fatal(err)
			}
			spec.DefaultPort = port
			spec.NoPortFallback = true
			s, err := New(Config{
				SessionID: "retry-launch", ProjectDir: t.TempDir(),
				MaxRestarts: 5, RestartBackoffMax: 150 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Shutdown()
			if err := s.Start(spec); err != nil {
				t.Fatal(err)
			}
			s.mu.RLock()
			first := s.daemons[spec.Name]
			s.mu.RUnlock()

			var restore func() error
			switch fault {
			case "missing binary":
				if err := os.Rename(spec.Command, spec.Command+".unavailable"); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Rename(spec.Command+".unavailable", spec.Command) }
			case "permission denied":
				if err := os.Chmod(spec.Command, 0644); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Chmod(spec.Command, 0755) }
			case "occupied port":
				ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				restore = ln.Close
			}
			if err := first.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}

			// The first process has exited, and the first relaunch has failed.
			// Recovery must advance its budget instead of pinning forever at 1.
			pending := waitForRestartCount(t, s, spec.Name, 2)
			if pending.State != StateRestarting || pending.PID != first.PID {
				t.Fatalf("expected original generation awaiting retry; got %+v", pending)
			}
			if err := s.Start(spec); err == nil {
				t.Fatal("public Start bypassed recovery ownership")
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}

			deadline := time.Now().Add(3 * time.Second)
			var recovered *ManagedDaemon
			for time.Now().Before(deadline) {
				d, _ := s.GetDaemon(spec.Name)
				if d.PID != first.PID {
					recovered = d
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if recovered == nil || recovered.Restarts < 2 || recovered.Port != port {
				t.Fatalf("daemon did not recover on its requested port with preserved budget: %+v", recovered)
			}
			pidData, err := os.ReadFile(s.pidPath(spec.Name))
			if err != nil {
				t.Fatal(err)
			}
			var info PIDFileInfo
			if err := json.Unmarshal(pidData, &info); err != nil || info.PID != recovered.PID {
				t.Fatalf("recovery did not replace PID file: %s (error: %v)", pidData, err)
			}
			logData, err := os.ReadFile(s.logPath(spec.Name))
			if err != nil || !strings.Contains(string(logData), "restart failed:") {
				t.Fatalf("launch failure missing from recovery log: %s (error: %v)", logData, err)
			}
		})
	}
}

func TestRestartLaunchFailuresExhaustBudgetAndAllowFreshStart(t *testing.T) {
	spec := restartTestSpec(t)
	s, err := New(Config{
		SessionID: "retry-exhaustion", ProjectDir: t.TempDir(),
		MaxRestarts: 2, RestartBackoffMax: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	first := s.daemons[spec.Name]
	s.mu.RUnlock()
	if err := os.Chmod(spec.Command, 0644); err != nil {
		t.Fatal(err)
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	failed := waitForDaemonState(t, s, spec.Name, StateFailed, 3*time.Second)
	if failed.Restarts != 3 || failed.PID != first.PID {
		t.Fatalf("exhausted generation = %+v; want original PID and 3 failures", failed)
	}
	logData, err := os.ReadFile(s.logPath(spec.Name))
	if err != nil || strings.Count(string(logData), "restart failed:") != 2 || !strings.Contains(string(logData), "max restarts (2) exceeded") {
		t.Fatalf("log must explain both failed attempts and exhaustion: %s (error: %v)", logData, err)
	}

	if err := os.Chmod(spec.Command, 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(spec); err != nil {
		t.Fatalf("fresh Start after exhaustion: %v", err)
	}
	fresh, _ := s.GetDaemon(spec.Name)
	if fresh.PID == first.PID || fresh.Restarts != 0 {
		t.Fatalf("fresh Start did not reset generation and budget: %+v", fresh)
	}
	// Delayed callbacks from the old generation cannot affect the replacement,
	// even if their old local state claims that recovery is still in progress.
	first.mu.Lock()
	first.State = StateRunning
	first.mu.Unlock()
	s.handleDaemonFailure(first)
	first.mu.Lock()
	first.State = StateRestarting
	first.mu.Unlock()
	if attempted, err := s.restartDaemon(first); attempted || err != nil {
		t.Fatalf("stale generation attempted restart: attempted=%v error=%v", attempted, err)
	}
	current, _ := s.GetDaemon(spec.Name)
	if current.PID != fresh.PID || current.Restarts != 0 {
		t.Fatalf("stale callback replaced fresh daemon: %+v", current)
	}
}

func TestStopDuringBackoffCancelsOldGeneration(t *testing.T) {
	spec := restartTestSpec(t)
	s, err := New(Config{
		SessionID: "cancel-recovery", ProjectDir: t.TempDir(),
		RestartBackoffMax: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	first := s.daemons[spec.Name]
	s.mu.RUnlock()
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitForRestartCount(t, s, spec.Name, 1)
	if err := supervisorStopForTest(s, spec.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(spec); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.GetDaemon(spec.Name)
	time.Sleep(2 * s.restartBackoffMax)
	current, _ := s.GetDaemon(spec.Name)
	if current.PID != fresh.PID || current.Restarts != 0 {
		t.Fatalf("cancelled recovery replaced fresh daemon: %+v", current)
	}
}

func TestRestartBackoffSaturates(t *testing.T) {
	for _, tt := range []struct {
		attempt int
		cap     time.Duration
		want    time.Duration
	}{
		{1, 60 * time.Second, time.Second},
		{3, 60 * time.Second, 4 * time.Second},
		{64, 60 * time.Second, 60 * time.Second},
		{1000, time.Duration(1<<63 - 1), time.Duration(1<<63 - 1)},
		{1, 50 * time.Millisecond, 50 * time.Millisecond},
	} {
		s := &Supervisor{restartBackoffMax: tt.cap}
		if got := s.restartBackoff(tt.attempt); got != tt.want {
			t.Errorf("attempt %d with cap %v: got %v, want %v", tt.attempt, tt.cap, got, tt.want)
		}
	}
}

func TestGetDaemonReturnsSnapshot(t *testing.T) {
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
		Name:      "snapshot-daemon",
		Command:   "sleep",
		Args:      []string{"10"},
		HealthCmd: []string{"echo", "ok"},
		Env:       []string{"SNAPSHOT_ORIGINAL=1"},
	}
	if err := s.Start(spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	spec.Args[0] = "mutated-input"
	spec.HealthCmd[0] = "mutated-input"
	spec.Env[0] = "mutated-input"

	first, exists := s.GetDaemon("snapshot-daemon")
	if !exists {
		t.Fatal("GetDaemon() returned false")
	}
	if first.Spec.Args[0] != "10" {
		t.Fatalf("GetDaemon() saw mutated input Args = %q, want %q", first.Spec.Args[0], "10")
	}
	if first.Spec.HealthCmd[0] != "echo" {
		t.Fatalf("GetDaemon() saw mutated input HealthCmd = %q, want %q", first.Spec.HealthCmd[0], "echo")
	}
	if first.Spec.Env[0] != "SNAPSHOT_ORIGINAL=1" {
		t.Fatalf("GetDaemon() saw mutated input Env = %q, want %q", first.Spec.Env[0], "SNAPSHOT_ORIGINAL=1")
	}

	first.State = StateFailed
	first.OwnerID = "mutated-owner"
	first.Spec.Args[0] = "mutated-snapshot"
	first.Spec.HealthCmd[0] = "mutated-snapshot"
	first.Spec.Env[0] = "mutated-snapshot"

	second, exists := s.GetDaemon("snapshot-daemon")
	if !exists {
		t.Fatal("GetDaemon() returned false after snapshot mutation")
	}
	if second.State == StateFailed {
		t.Fatal("mutating GetDaemon() snapshot changed daemon state")
	}
	if second.OwnerID != "test-session" {
		t.Fatalf("mutating GetDaemon() snapshot changed owner = %q", second.OwnerID)
	}
	if second.Spec.Args[0] != "10" {
		t.Fatalf("mutating GetDaemon() snapshot changed Args = %q", second.Spec.Args[0])
	}
	if second.Spec.HealthCmd[0] != "echo" {
		t.Fatalf("mutating GetDaemon() snapshot changed HealthCmd = %q", second.Spec.HealthCmd[0])
	}
	if second.Spec.Env[0] != "SNAPSHOT_ORIGINAL=1" {
		t.Fatalf("mutating GetDaemon() snapshot changed Env = %q", second.Spec.Env[0])
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
	if err := supervisorStopForTest(s, "pid-test"); err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file not removed after stop")
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

// Regression for ntm#137. `am` >= 0.2 split the `serve` subcommand
// into `serve-http`/`serve-stdio`; the older bare `serve` form makes
// the supervisor retry-storm with `unrecognized subcommand 'serve'`
// and then give up after 5 attempts, taking out the multi-agent
// pane's coordination transport. Pin the canonical args so a future
// refactor can't silently revert to `serve`.
func TestDefaultSpecsAgentMailUsesServeHTTP(t *testing.T) {
	specs := DefaultSpecs()
	var amSpec *DaemonSpec
	for i := range specs {
		if specs[i].Name == "am" {
			amSpec = &specs[i]
			break
		}
	}
	if amSpec == nil {
		t.Fatal("DefaultSpecs() missing 'am' daemon")
	}
	if len(amSpec.Args) == 0 {
		t.Fatal("'am' daemon spec has no args")
	}
	if amSpec.Args[0] != "serve-http" {
		t.Fatalf(
			"'am' daemon must invoke `serve-http` (not %q) — "+
				"`am serve` was removed in mcp-agent-mail >= 0.2 (ntm#137)",
			amSpec.Args[0],
		)
	}
	// `--no-tui` is non-negotiable: ntm runs the daemon in the
	// background; an interactive TUI surface would race the
	// pane-managing supervisor.
	hasNoTUI := false
	for _, a := range amSpec.Args {
		if a == "--no-tui" {
			hasNoTUI = true
			break
		}
	}
	if !hasNoTUI {
		t.Fatalf("'am' daemon args must include --no-tui; got %v", amSpec.Args)
	}
	if !amSpec.NoPortFallback {
		t.Fatal("'am' daemon must refuse random-port fallback when 8765 is already occupied")
	}
}

func TestStartNoPortFallbackRefusesOccupiedPort(t *testing.T) {
	tmpDir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve port: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	// A real binary: Start() now fails fast on missing binaries BEFORE the
	// port check, and this test is about the occupied-port refusal.
	err = s.Start(DaemonSpec{
		Name:           "am",
		Command:        "sleep",
		Args:           []string{"10"},
		DefaultPort:    port,
		NoPortFallback: true,
	})
	if err == nil {
		t.Fatal("expected occupied-port error")
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "random port") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.GetDaemon("am"); ok {
		t.Fatal("daemon should not be registered after refusing occupied default port")
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
	defer ln.Close()
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
		SessionID:   "test-session",
		ProjectDir:  tmpDir,
		MaxRestarts: -1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Shutdown()

	d := &ManagedDaemon{
		Spec: DaemonSpec{
			Name:    "test-daemon",
			Command: "true",
		},
		State:    StateRunning,
		Restarts: 0,
		OwnerID:  "test-session",
	}
	s.daemons[d.Spec.Name] = d

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

func TestShutdownPreventsFutureStarts(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	err = s.Start(DaemonSpec{
		Name:    "after-shutdown",
		Command: "sleep",
		Args:    []string{"1"},
	})
	if err == nil {
		t.Fatal("Start() after Shutdown should return error")
	}
	if _, exists := s.GetDaemon("after-shutdown"); exists {
		t.Fatal("Start() after Shutdown installed a daemon")
	}
}

func TestStartWaitsForConcurrentShutdown(t *testing.T) {
	tmpDir := t.TempDir()

	s, err := New(Config{
		SessionID:  "test-session",
		ProjectDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	blocked := &ManagedDaemon{
		Spec:    DaemonSpec{Name: "existing"},
		State:   StateRunning,
		OwnerID: "test-session",
	}
	blocked.mu.Lock()
	var releaseOnce sync.Once
	releaseBlocked := func() {
		releaseOnce.Do(func() {
			blocked.mu.Unlock()
		})
	}
	t.Cleanup(releaseBlocked)

	s.mu.Lock()
	s.daemons["existing"] = blocked
	s.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		_ = s.Shutdown()
		close(stopped)
	}()

	// Wait for Shutdown to acquire lifecycleMu and call s.cancel().
	// Shutdown closes shutdownCh before entering stopAllOwnedDaemons,
	// which blocks on blocked.mu — so lifecycleMu is still held.
	select {
	case <-s.shutdownCh:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not acquire lifecycleMu")
	}

	startAttempt := make(chan error, 1)
	go func() {
		startAttempt <- s.Start(DaemonSpec{
			Name:    "late-daemon",
			Command: "sleep",
			Args:    []string{"1"},
		})
	}()

	select {
	case err := <-startAttempt:
		t.Fatalf("Start returned before Shutdown completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseBlocked()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}

	select {
	case err := <-startAttempt:
		if err == nil {
			t.Fatal("Start after concurrent Shutdown should return error")
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not unblock after Shutdown")
	}

	if _, exists := s.GetDaemon("late-daemon"); exists {
		t.Fatal("concurrent Start installed a daemon after Shutdown")
	}
}

// supervisorStatusForTest snapshots all managed daemons.
func supervisorStatusForTest(s *Supervisor) map[string]*ManagedDaemon {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*ManagedDaemon, len(s.daemons))
	for name, d := range s.daemons {
		d.mu.RLock()
		result[name] = snapshotDaemonLocked(d)
		d.mu.RUnlock()
	}
	return result
}

// supervisorStopForTest stops one daemon by name.
func supervisorStopForTest(s *Supervisor, name string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	daemon, exists := s.daemons[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("daemon %s not found", name)
	}
	s.mu.Unlock()

	return s.stopDaemon(daemon)
}

// supervisorStopAllForTest stops all daemons owned by this session.
func supervisorStopAllForTest(s *Supervisor) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.stopAllOwnedDaemons()
}
