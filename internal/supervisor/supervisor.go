// Package supervisor manages the lifecycle of long-running daemons (cm serve, bd daemon)
// that NTM spawns. It handles port allocation, health monitoring, and clean shutdown.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DaemonState represents the current state of a managed daemon.
type DaemonState string

const (
	StateStarting   DaemonState = "starting"
	StateRunning    DaemonState = "running"
	StateStopping   DaemonState = "stopping"
	StateStopped    DaemonState = "stopped"
	StateFailed     DaemonState = "failed"
	StateRestarting DaemonState = "restarting"
)

// DaemonSpec defines how to start and manage a daemon.
type DaemonSpec struct {
	Name           string   `json:"name"`             // Unique identifier: "cm", "bd", "am"
	Command        string   `json:"command"`          // Command to run: "cm", "bd"
	Args           []string `json:"args"`             // Arguments: ["serve", "--port", "8765"]
	HealthURL      string   `json:"health_url"`       // Health check URL: "http://127.0.0.1:8765/health/liveness" or "/health"
	HealthCmd      []string `json:"health_cmd"`       // Health check command: ["bd", "daemon", "--health"]
	PortFlag       string   `json:"port_flag"`        // Flag to specify port: "--port"
	DefaultPort    int      `json:"default_port"`     // Default port if none specified
	NoPortFallback bool     `json:"no_port_fallback"` // Refuse random-port fallback when DefaultPort is occupied
	WorkDir        string   `json:"work_dir"`         // Working directory for the daemon
	Env            []string `json:"env"`              // Additional environment variables
}

// ManagedDaemon represents a running daemon process.
type ManagedDaemon struct {
	Spec       DaemonSpec  `json:"spec"`
	State      DaemonState `json:"state"`
	PID        int         `json:"pid"`
	Port       int         `json:"port"`
	StartedAt  time.Time   `json:"started_at"`
	LastHealth time.Time   `json:"last_health"`
	Restarts   int         `json:"restarts"`
	OwnerID    string      `json:"owner_id"` // Session ID that owns this daemon

	cmd        *exec.Cmd
	logFile    *os.File
	cancelFunc context.CancelFunc
	mu         sync.RWMutex
}

// PIDFileInfo stores information written to PID files.
type PIDFileInfo struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	OwnerID   string    `json:"owner_id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
}

// Supervisor manages the lifecycle of multiple daemons for an NTM session.
type Supervisor struct {
	sessionID  string
	projectDir string
	ntmDir     string // .ntm directory path

	daemons     map[string]*ManagedDaemon
	mu          sync.RWMutex
	lifecycleMu sync.Mutex
	stopped     bool

	healthInterval    time.Duration
	maxRestarts       int
	restartBackoffMax time.Duration

	shutdownCh chan struct{}
	cancel     func()
}

// Config holds supervisor configuration.
type Config struct {
	SessionID         string
	ProjectDir        string
	HealthInterval    time.Duration // Default: 5s
	MaxRestarts       int           // Default: 5
	RestartBackoffMax time.Duration // Default: 60s
}

// New creates a new Supervisor for the given session.
func New(cfg Config) (*Supervisor, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("session ID required")
	}
	if cfg.ProjectDir == "" {
		return nil, fmt.Errorf("project directory required")
	}

	ntmDir := filepath.Join(cfg.ProjectDir, ".ntm")

	// MinHealthInterval is the minimum allowed health check interval to prevent ticker panics.
	// time.NewTicker requires a positive duration.
	const MinHealthInterval = 100 * time.Millisecond

	// Set defaults and validate intervals to prevent ticker panics
	if cfg.HealthInterval < MinHealthInterval {
		cfg.HealthInterval = 5 * time.Second
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 5
	}
	if cfg.RestartBackoffMax == 0 {
		cfg.RestartBackoffMax = 60 * time.Second
	}

	// Ensure directories exist
	dirs := []string{
		filepath.Join(ntmDir, "pids"),
		filepath.Join(ntmDir, "logs"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	shutdownCh := make(chan struct{})
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			close(shutdownCh)
		})
	}

	return &Supervisor{
		sessionID:         cfg.SessionID,
		projectDir:        cfg.ProjectDir,
		ntmDir:            ntmDir,
		daemons:           make(map[string]*ManagedDaemon),
		healthInterval:    cfg.HealthInterval,
		maxRestarts:       cfg.MaxRestarts,
		restartBackoffMax: cfg.RestartBackoffMax,
		shutdownCh:        shutdownCh,
		cancel:            cancel,
	}, nil
}

// Start starts a daemon with the given spec.
func (s *Supervisor) Start(spec DaemonSpec) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	if s.stopped {
		return fmt.Errorf("supervisor is shut down")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already running or starting
	// Note: StateRestarting is allowed because handleDaemonFailure sets it before calling Start()
	if d, exists := s.daemons[spec.Name]; exists {
		if d.State == StateRunning || d.State == StateStarting {
			return fmt.Errorf("daemon %s already running (state: %s)", spec.Name, d.State)
		}
	}

	// Find available port
	port := spec.DefaultPort
	if !isPortAvailable(port) {
		if spec.NoPortFallback {
			return fmt.Errorf("daemon %s default port %d is already in use; not starting a second instance on a random port", spec.Name, spec.DefaultPort)
		}
		var err error
		port, err = findAvailablePort()
		if err != nil {
			return fmt.Errorf("find available port: %w", err)
		}
	}

	// Build args with port
	args := make([]string, len(spec.Args))
	copy(args, spec.Args)
	if spec.PortFlag != "" {
		args = append(args, spec.PortFlag, fmt.Sprintf("%d", port))
	}

	// Open log file
	logPath := s.logPath(spec.Name)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Create command
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, spec.Command, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = spec.WorkDir
	if cmd.Dir == "" {
		cmd.Dir = s.projectDir
	}
	cmd.Env = append(os.Environ(), spec.Env...)

	// Set process group for clean shutdown (platform-specific)
	setSysProcAttr(cmd)

	// Start the process
	if err := cmd.Start(); err != nil {
		logFile.Close()
		cancel()
		return fmt.Errorf("start daemon %s: %w", spec.Name, err)
	}

	// Preserve restart count from existing daemon if this is a restart
	var restartCount int
	if existing, exists := s.daemons[spec.Name]; exists && existing.State == StateRestarting {
		restartCount = existing.Restarts
	}

	daemon := &ManagedDaemon{
		Spec:       copyDaemonSpec(spec),
		State:      StateStarting,
		PID:        cmd.Process.Pid,
		Port:       port,
		StartedAt:  time.Now(),
		Restarts:   restartCount,
		OwnerID:    s.sessionID,
		cmd:        cmd,
		logFile:    logFile,
		cancelFunc: cancel,
	}

	// Update health URL with actual port, preserving the original path
	if spec.HealthURL != "" {
		if u, err := url.Parse(spec.HealthURL); err == nil {
			daemon.Spec.HealthURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, u.Path)
		}
	}

	s.daemons[spec.Name] = daemon

	// Write PID file
	if err := s.writePIDFile(daemon); err != nil {
		// Non-fatal, log it
		fmt.Fprintf(logFile, "[supervisor] warning: failed to write PID file: %v\n", err)
	}

	// Start health monitor in background
	go s.monitorDaemon(daemon)

	// Wait for daemon to exit in background
	go s.waitForExit(daemon)

	return nil
}

// Stop stops a daemon gracefully.
func (s *Supervisor) Stop(name string) error {
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

// StopAll stops all daemons owned by this session.
func (s *Supervisor) StopAll() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.stopAllOwnedDaemons()
}

// Shutdown stops all owned daemons and cancels the supervisor context.
func (s *Supervisor) Shutdown() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	if s.stopped {
		return nil
	}
	s.stopped = true
	s.cancel()
	return s.stopAllOwnedDaemons()
}

func (s *Supervisor) stopAllOwnedDaemons() error {
	s.mu.Lock()
	daemons := make([]*ManagedDaemon, 0, len(s.daemons))
	for _, d := range s.daemons {
		if d.OwnerID == s.sessionID {
			daemons = append(daemons, d)
		}
	}
	s.mu.Unlock()

	var errs []error
	for _, d := range daemons {
		if err := s.stopDaemon(d); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", d.Spec.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping daemons: %v", errs)
	}
	return nil
}

// Status returns the status of all managed daemons.
// Returns deep copies to prevent data races with slice fields.
func (s *Supervisor) Status() map[string]*ManagedDaemon {
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

// GetDaemon returns a snapshot of a daemon by name.
func (s *Supervisor) GetDaemon(name string) (*ManagedDaemon, bool) {
	s.mu.RLock()
	d, ok := s.daemons[name]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	return snapshotDaemonLocked(d), true
}

func snapshotDaemonLocked(d *ManagedDaemon) *ManagedDaemon {
	return &ManagedDaemon{
		Spec:       copyDaemonSpec(d.Spec),
		State:      d.State,
		PID:        d.PID,
		Port:       d.Port,
		StartedAt:  d.StartedAt,
		LastHealth: d.LastHealth,
		Restarts:   d.Restarts,
		OwnerID:    d.OwnerID,
	}
}

func copyDaemonSpec(spec DaemonSpec) DaemonSpec {
	spec.Args = copyStringSlice(spec.Args)
	spec.HealthCmd = copyStringSlice(spec.HealthCmd)
	spec.Env = copyStringSlice(spec.Env)
	return spec
}

func copyStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

// stopDaemon stops a single daemon.
func (s *Supervisor) stopDaemon(d *ManagedDaemon) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check ownership
	if d.OwnerID != s.sessionID {
		return fmt.Errorf("daemon %s owned by different session: %s", d.Spec.Name, d.OwnerID)
	}

	if d.State == StateStopped || d.State == StateStopping {
		return nil
	}

	d.State = StateStopping

	// Cancel context to signal shutdown
	if d.cancelFunc != nil {
		d.cancelFunc()
	}

	// Gracefully terminate process (platform-specific)
	if d.cmd != nil && d.cmd.Process != nil {
		terminateProcess(d.cmd.Process)
	}

	// Clean up PID file
	s.removePIDFile(d.Spec.Name)

	// Close log file
	if d.logFile != nil {
		d.logFile.Close()
	}

	d.State = StateStopped
	return nil
}

// monitorDaemon runs health checks for a daemon.
func (s *Supervisor) monitorDaemon(d *ManagedDaemon) {
	ticker := time.NewTicker(s.healthInterval)
	defer ticker.Stop()

	// Wait a bit for daemon to start
	time.Sleep(2 * time.Second)

	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
			d.mu.RLock()
			state := d.State
			healthURL := d.Spec.HealthURL
			healthCmd := d.Spec.HealthCmd
			d.mu.RUnlock()

			if state != StateRunning && state != StateStarting {
				return
			}

			if healthURL == "" && len(healthCmd) == 0 {
				// No health check configured, just check if process is running
				d.mu.RLock()
				cmd := d.cmd
				d.mu.RUnlock()
				if cmd != nil && cmd.ProcessState != nil && cmd.ProcessState.Exited() {
					s.handleDaemonFailure(d)
				}
				continue
			}

			// Perform health check
			var healthy bool
			if healthURL != "" {
				healthy = s.checkHealthHTTP(healthURL)
			} else if len(healthCmd) > 0 {
				healthy = s.checkHealthCmd(healthCmd)
			}

			d.mu.Lock()
			if healthy {
				d.LastHealth = time.Now()
				if d.State == StateStarting {
					d.State = StateRunning
				}
			}
			d.mu.Unlock()
		}
	}
}

// waitForExit waits for the daemon process to exit.
func (s *Supervisor) waitForExit(d *ManagedDaemon) {
	if d.cmd == nil {
		return
	}

	err := d.cmd.Wait()

	d.mu.Lock()
	state := d.State
	logFile := d.logFile
	d.mu.Unlock()

	// Only handle as failure if we didn't stop it ourselves
	if state != StateStopping && state != StateStopped {
		if err != nil && logFile != nil {
			fmt.Fprintf(logFile, "[supervisor] daemon exited with error: %v\n", err)
		}
		s.handleDaemonFailure(d)
	}
}

// handleDaemonFailure handles a daemon crash and potentially restarts it.
func (s *Supervisor) handleDaemonFailure(d *ManagedDaemon) {
	d.mu.Lock()
	// Avoid double-handling the same failure (e.g. monitor + wait goroutines).
	// If a daemon is already in a terminal/recovery state, a second failure signal is noise.
	if d.State == StateStopping || d.State == StateStopped || d.State == StateFailed || d.State == StateRestarting {
		d.mu.Unlock()
		return
	}
	d.Restarts++
	restarts := d.Restarts
	d.State = StateFailed
	logFile := d.logFile
	d.mu.Unlock()

	// Check if we should restart
	if restarts > s.maxRestarts {
		if logFile != nil {
			fmt.Fprintf(logFile, "[supervisor] max restarts (%d) exceeded, not restarting\n", s.maxRestarts)
		}
		return
	}

	// Calculate backoff
	backoff := time.Duration(1<<uint(restarts-1)) * time.Second
	if backoff > s.restartBackoffMax {
		backoff = s.restartBackoffMax
	}

	if logFile != nil {
		fmt.Fprintf(logFile, "[supervisor] restarting in %v (attempt %d/%d)\n", backoff, restarts, s.maxRestarts)
	}

	select {
	case <-s.shutdownCh:
		return
	case <-time.After(backoff):
	}

	d.mu.Lock()
	// Don't restart if daemon was explicitly stopped
	if d.State == StateStopping || d.State == StateStopped {
		d.mu.Unlock()
		return
	}
	d.State = StateRestarting
	d.mu.Unlock()

	// Restart the daemon
	if err := s.Start(d.Spec); err != nil {
		d.mu.RLock()
		logFile = d.logFile
		d.mu.RUnlock()
		if logFile != nil {
			fmt.Fprintf(logFile, "[supervisor] restart failed: %v\n", err)
		}
	}
}

// healthCheckClient is a shared HTTP client for health checks with appropriate timeouts.
// Using a dedicated client (vs http.DefaultClient) ensures connection timeouts are respected.
var healthCheckClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
	},
}

// checkHealthHTTP performs an HTTP health check.
func (s *Supervisor) checkHealthHTTP(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	resp, err := healthCheckClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // Drain body

	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// checkHealthCmd performs a command-based health check.
func (s *Supervisor) checkHealthCmd(cmdArgs []string) bool {
	if len(cmdArgs) == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = s.projectDir // Execute in project dir context

	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// writePIDFile writes the PID file for a daemon.
func (s *Supervisor) writePIDFile(d *ManagedDaemon) error {
	path := s.pidPath(d.Spec.Name)

	info := PIDFileInfo{
		PID:       d.PID,
		Port:      d.Port,
		OwnerID:   d.OwnerID,
		Command:   d.Spec.Command,
		StartedAt: d.StartedAt,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// removePIDFile removes the PID file for a daemon.
func (s *Supervisor) removePIDFile(name string) {
	os.Remove(s.pidPath(name))
}

// pidPath returns the path to a daemon's PID file.
func (s *Supervisor) pidPath(name string) string {
	return filepath.Join(s.ntmDir, "pids", fmt.Sprintf("%s-%s.pid", name, s.sessionID))
}

// logPath returns the path to a daemon's log file.
func (s *Supervisor) logPath(name string) string {
	return filepath.Join(s.ntmDir, "logs", fmt.Sprintf("%s-%s.log", name, s.sessionID))
}

// isPortAvailable checks if a port is available for binding.
func isPortAvailable(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// findAvailablePort finds an available port starting from a random high port.
func findAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// DefaultSpecs returns the default daemon specs for NTM.
func DefaultSpecs() []DaemonSpec {
	return []DaemonSpec{
		{
			Name:        "cm",
			Command:     "cm",
			Args:        []string{"serve"},
			HealthURL:   "http://127.0.0.1:8200/health",
			PortFlag:    "--port",
			DefaultPort: 8200,
		},
		{
			// This spec is used only when `[agent_mail].supervisor_enabled = true`.
			// Agent Mail is externally managed by default so ntm does not
			// compete with a user-owned `am` process on port 8765.
			//
			// `am` >= 0.2 split the original `serve` subcommand into
			// `serve-http` (HTTP transport, what ntm needs) and
			// `serve-stdio`. Older binaries supporting bare `serve` are
			// pre-0.2.0 and not on any current install; ntm pins to the
			// `am` from the Dicklesworthstone tap, which has been 0.2+
			// for the duration of this code path's life. See ntm#137.
			//
			// `--no-tui` keeps the supervised process headless (no
			// curses surface that would conflict with ntm's tmux panes).
			//
			// `--host 127.0.0.1` locks the bind to loopback so the
			// supervised daemon is not exposed beyond the local box.
			// Without this, `--no-auth` would expose the API to anyone
			// on the network.
			//
			// `--no-auth` matches the canonical args the launchd /
			// systemd installers `am service install` produce. This is
			// safe for single-user workstations (the bind is loopback)
			// but is a security trade-off on shared multi-user hosts.
			// Those operators should keep the default external ownership
			// and run `am serve-http` themselves with an auth-token
			// configuration.
			Name:           "am",
			Command:        "am",
			Args:           []string{"serve-http", "--host", "127.0.0.1", "--no-tui", "--no-auth"},
			HealthURL:      "http://127.0.0.1:8765/health/liveness",
			PortFlag:       "--port",
			DefaultPort:    8765,
			NoPortFallback: true,
		},
	}
}
