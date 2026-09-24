// Package supervisor manages the lifecycle of long-running daemons (cm serve, bd daemon)
// that NTM spawns. It handles port allocation, health monitoring, and clean shutdown.
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// StateUnhealthy marks a daemon whose process is alive but whose health
	// probe has not passed within the startup health timeout (or has stopped
	// passing after it was running). A daemon can recover from unhealthy back
	// to running on the next successful probe. This state exists so a daemon
	// with a misconfigured or unreachable health endpoint is VISIBLY broken
	// instead of pinned in "starting" forever (bd-ws1-truth-safety-l5ddi.2).
	StateUnhealthy DaemonState = "unhealthy"
)

// DaemonSpec defines how to start and manage a daemon.
type DaemonSpec struct {
	Name           string   `json:"name"`             // Unique identifier: "cm", "bd", "am"
	Command        string   `json:"command"`          // Command to run: "cm", "bd"
	Args           []string `json:"args"`             // Arguments: ["serve", "--port", "8765"]
	HealthURL      string   `json:"health_url"`       // Health check URL: "http://127.0.0.1:8765/health/liveness" or "/health"
	HealthCmd      []string `json:"health_cmd"`       // Health check command: ["bd", "daemon", "--health"]
	HealthMCP      bool     `json:"health_mcp"`       // Health check is an MCP JSON-RPC round-trip at the daemon root (cm speaks MCP only; it has NO REST /health)
	PortFlag       string   `json:"port_flag"`        // Flag to specify port: "--port"
	DefaultPort    int      `json:"default_port"`     // Default port if none specified
	NoPortFallback bool     `json:"no_port_fallback"` // Refuse random-port fallback when DefaultPort is occupied
	WorkDir        string   `json:"work_dir"`         // Working directory for the daemon
	Env            []string `json:"env"`              // Additional environment variables
}

// ManagedDaemon represents a running daemon process.
type ManagedDaemon struct {
	Spec        DaemonSpec  `json:"spec"`
	State       DaemonState `json:"state"`
	PID         int         `json:"pid"`
	Port        int         `json:"port"`
	StartedAt   time.Time   `json:"started_at"`
	LastHealth  time.Time   `json:"last_health"`
	Restarts    int         `json:"restarts"`
	OwnerID     string      `json:"owner_id"` // Session ID that owns this daemon
	LastError   string      `json:"last_error,omitempty"`
	HealthMode  string      `json:"health_mode"` // process, http, mcp, command
	HealthError string      `json:"health_error,omitempty"`

	cmd          *exec.Cmd
	logFile      *os.File
	cancelFunc   context.CancelFunc
	done         chan struct{}
	ownership    *daemonOwnership
	healthCtx    context.Context
	healthCancel context.CancelFunc
	monitorDone  chan struct{}
	healthDir    string
	healthEnv    []string
	mu           sync.RWMutex
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

	healthInterval       time.Duration
	maxRestarts          int
	restartBackoffMax    time.Duration
	startupHealthTimeout time.Duration

	healthCtx  context.Context
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
	// StartupHealthTimeout bounds how long a daemon may sit in StateStarting
	// with failing health probes before it is marked StateUnhealthy.
	// Default: 30s.
	StartupHealthTimeout time.Duration
}

// DefaultMaxRestarts is the default restart budget applied when
// Config.MaxRestarts is zero. Exported so foreground callers (ntm memory
// serve) can report exhaustion, which is recorded as StateFailed with
// Restarts one greater than the configured budget.
const DefaultMaxRestarts = 5

// New creates a new Supervisor for the given session.
func New(cfg Config) (*Supervisor, error) {
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("session ID required")
	}
	if cfg.ProjectDir == "" {
		return nil, fmt.Errorf("project directory required")
	}

	if err := validateDaemonComponent(cfg.SessionID, "session ID"); err != nil {
		return nil, err
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
		cfg.MaxRestarts = DefaultMaxRestarts
	}
	if cfg.RestartBackoffMax <= 0 {
		cfg.RestartBackoffMax = 60 * time.Second
	}
	if cfg.StartupHealthTimeout <= 0 {
		cfg.StartupHealthTimeout = 30 * time.Second
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

	healthCtx, cancelHealth := context.WithCancel(context.Background())
	shutdownCh := make(chan struct{})
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			cancelHealth()
			close(shutdownCh)
		})
	}

	return &Supervisor{
		sessionID:            cfg.SessionID,
		projectDir:           cfg.ProjectDir,
		ntmDir:               ntmDir,
		daemons:              make(map[string]*ManagedDaemon),
		healthInterval:       cfg.HealthInterval,
		maxRestarts:          cfg.MaxRestarts,
		restartBackoffMax:    cfg.RestartBackoffMax,
		startupHealthTimeout: cfg.StartupHealthTimeout,
		healthCtx:            healthCtx,
		shutdownCh:           shutdownCh,
		cancel:               cancel,
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

	// Only an explicitly stopped daemon or an exhausted recovery may be
	// replaced by a public start. Unhealthy processes are still alive, and a
	// pending restart owns this name until recovery completes or is stopped.
	if d, exists := s.daemons[spec.Name]; exists {
		d.mu.RLock()
		state := d.State
		d.mu.RUnlock()
		if state != StateStopped && state != StateFailed {
			return fmt.Errorf("daemon %s already managed (state: %s)", spec.Name, state)
		}
	}

	if err := validateDaemonComponent(spec.Name, "daemon name"); err != nil {
		return err
	}
	owner, err := acquireDaemonOwnership(s.ntmDir, spec.Name, s.sessionID)
	if err != nil {
		return err
	}
	if err := s.startDaemonLocked(spec, 0, owner); err != nil {
		// Failed preflight/exec must not leak the fence. If a child did
		// start, startDaemonLocked has stopped and reaped that child first.
		return errors.Join(err, owner.finish())
	}
	return nil
}

// restartDaemon admits a replacement only while the failed generation still
// owns its name and recovery state. A stopped or superseded generation must
// never launch over a newer process. The bool reports whether a launch was
// attempted; false means recovery was cancelled, not a retryable launch error.
func (s *Supervisor) restartDaemon(previous *ManagedDaemon) (bool, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	if s.stopped {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.daemons[previous.Spec.Name] != previous {
		return false, nil
	}

	previous.mu.RLock()
	state := previous.State
	restarts := previous.Restarts
	previous.mu.RUnlock()
	if state != StateRestarting {
		return false, nil
	}

	return true, s.startDaemonLocked(previous.Spec, restarts, previous.ownership)
}

// startDaemonLocked launches an admitted generation. The caller holds both
// lifecycleMu and mu, so shutdown and other launches cannot replace its owner
// between admission and registration.
func (s *Supervisor) startDaemonLocked(spec DaemonSpec, restartCount int, owner *daemonOwnership) error {
	if owner == nil {
		return fmt.Errorf("%w: daemon %s has no ownership fence", ErrDaemonRecoveryRequired, spec.Name)
	}
	// Fail fast and loud when the daemon binary does not exist. Without this
	// check a missing binary would only surface as a launch-retry loop in the
	// log file — silence is the sin (bd-ws1-truth-safety-l5ddi.2).
	if !filepath.IsAbs(spec.Command) {
		if _, err := exec.LookPath(spec.Command); err != nil {
			return fmt.Errorf("daemon %s: binary %q not found in PATH: %w", spec.Name, spec.Command, err)
		}
	} else if _, err := os.Stat(spec.Command); err != nil {
		return fmt.Errorf("daemon %s: binary %q not found: %w", spec.Name, spec.Command, err)
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
	// Freeze the effective launch scope before exec. Later parent cwd/env
	// changes must not point a health command at a different store.
	launchDir, err := filepath.Abs(cmd.Dir)
	if err != nil {
		_ = logFile.Close()
		cancel()
		return fmt.Errorf("resolve daemon working directory: %w", err)
	}
	cmd.Dir = launchDir
	cmd.Env = append(os.Environ(), spec.Env...)

	// Set process group for clean shutdown (platform-specific)
	setSysProcAttr(cmd)

	// Journal the launch boundary before exec; an unlocked fence after a
	// supervisor crash is not proof that no daemon was launched.
	if err := owner.prepare(); err != nil {
		_ = logFile.Close()
		cancel()
		return err
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		logFile.Close()
		cancel()
		return errors.Join(fmt.Errorf("start daemon %s: %w", spec.Name, err), owner.startFailed())
	}

	healthParent := s.healthCtx
	if healthParent == nil {
		healthParent = context.Background()
	}
	healthCtx, cancelHealth := context.WithCancel(healthParent)
	daemon := &ManagedDaemon{
		Spec:         copyDaemonSpec(spec),
		State:        StateStarting,
		PID:          cmd.Process.Pid,
		Port:         port,
		StartedAt:    time.Now(),
		Restarts:     restartCount,
		OwnerID:      s.sessionID,
		cmd:          cmd,
		logFile:      logFile,
		cancelFunc:   cancel,
		done:         make(chan struct{}),
		ownership:    owner,
		healthCtx:    healthCtx,
		healthCancel: cancelHealth,
		monitorDone:  make(chan struct{}),
		HealthMode:   daemonHealthMode(spec),
		healthDir:    launchDir,
		healthEnv:    append([]string(nil), cmd.Env...),
	}

	// Update health URL with actual port, preserving the original path
	if spec.HealthURL != "" {
		if u, err := url.Parse(spec.HealthURL); err == nil {
			daemon.Spec.HealthURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, u.Path)
		}
	}
	// MCP daemons (cm) are probed with a JSON-RPC round-trip at the root;
	// there is no REST health path to preserve.
	if spec.HealthMCP {
		daemon.Spec.HealthURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	}

	s.daemons[spec.Name] = daemon
	if err := owner.started(daemon.PID); err != nil {
		// Do not launch a replacement after a successful exec whose PID
		// could not be checkpointed. Stop only this known child and reap it.
		daemon.LastError = err.Error()
		daemon.State = StateStopping
		terminateProcess(cmd.Process)
		// No exit/restart worker exists yet. Wait exactly once here.
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			forceKillProcess(cmd.Process)
			<-waited
		}
		cancel()
		cancelHealth()
		close(daemon.monitorDone)
		close(daemon.done)
		_ = logFile.Close()
		daemon.logFile = nil
		daemon.State = StateFailed
		return errors.Join(err, owner.finish())
	}

	// Log the launch so every attempt (first start and each restart) is
	// diagnosable from the log alone.
	fmt.Fprintf(logFile, "[supervisor] launched daemon %s (pid=%d port=%d cmd=%s state=%s)\n",
		spec.Name, daemon.PID, port, spec.Command, daemon.State)

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
		return fmt.Errorf("errors stopping daemons: %w", errors.Join(errs...))
	}
	return nil
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
		Spec:        copyDaemonSpec(d.Spec),
		State:       d.State,
		PID:         d.PID,
		Port:        d.Port,
		StartedAt:   d.StartedAt,
		LastHealth:  d.LastHealth,
		Restarts:    d.Restarts,
		OwnerID:     d.OwnerID,
		LastError:   d.LastError,
		HealthMode:  d.HealthMode,
		HealthError: d.HealthError,
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

	// Check ownership
	if d.OwnerID != s.sessionID {
		d.mu.Unlock()
		return fmt.Errorf("daemon %s owned by different session: %s", d.Spec.Name, d.OwnerID)
	}

	if d.State == StateStopped || d.State == StateStopping {
		d.mu.Unlock()
		return nil
	}

	d.State = StateStopping
	cmd := d.cmd
	done := d.done
	cancelHealth := d.healthCancel
	monitorDone := d.monitorDone
	d.mu.Unlock()
	if cancelHealth != nil {
		cancelHealth()
	}

	// Do not cancel the CommandContext here: its default cancellation handler
	// calls Process.Kill, which races (and usually wins) against the graceful
	// platform-specific termination signal below. waitForExit cancels the
	// context after Wait has reaped the process.
	alreadyExited := false
	if done != nil {
		select {
		case <-done:
			alreadyExited = true
		default:
		}
	}
	if !alreadyExited && cmd != nil && cmd.Process != nil {
		terminateProcess(cmd.Process)
		if done != nil {
			const gracefulShutdownTimeout = 2 * time.Second
			select {
			case <-done:
			case <-time.After(gracefulShutdownTimeout):
				forceKillProcess(cmd.Process)
				<-done
			}
		}
	}

	if monitorDone != nil {
		<-monitorDone
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// The process is reaped before its log is closed, so its final graceful
	// shutdown output is retained and no live child writes to a closed file.
	if d.logFile != nil {
		_ = d.logFile.Close()
		d.logFile = nil
	}

	err := d.ownership.finishWithCleanup(func() { s.removePIDFile(d.Spec.Name) })
	if err != nil {
		d.LastError = err.Error()
	}
	d.State = StateStopped
	return err
}

// monitorDaemon runs health checks for one process generation. A cancelled or
// retired generation cannot publish a delayed health result or keep probing
// after the ownership fence is handed to another controller.
func (s *Supervisor) monitorDaemon(d *ManagedDaemon) {
	if d.monitorDone != nil {
		defer close(d.monitorDone)
	}
	ctx := d.healthCtx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(s.healthInterval)
	defer ticker.Stop()
	startupTimer := time.NewTimer(2 * time.Second)
	defer startupTimer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-s.shutdownCh:
		return
	case <-d.done:
		return
	case <-startupTimer.C:
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.shutdownCh:
			return
		case <-d.done:
			return
		case <-ticker.C:
			d.mu.RLock()
			state, spec := d.State, copyDaemonSpec(d.Spec)
			d.mu.RUnlock()
			if state != StateRunning && state != StateStarting && state != StateUnhealthy {
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, daemonHealthTimeout)
			err := probeDaemonHealthInScope(probeCtx, spec, d.healthDir, d.healthEnv)
			cancel()
			d.mu.Lock()
			if ctx.Err() != nil || (d.State != StateRunning && d.State != StateStarting && d.State != StateUnhealthy) {
				d.mu.Unlock()
				return
			}
			select {
			case <-d.done:
				d.mu.Unlock()
				return
			default:
			}
			d.HealthMode = daemonHealthMode(spec)
			if err == nil {
				d.HealthError = ""
				// No configured probe means process liveness, not a fabricated
				// successful protocol check. Expose the distinction explicitly.
				if d.HealthMode != "process" {
					d.LastHealth = time.Now()
				}
				if d.State == StateStarting || d.State == StateUnhealthy {
					prev := d.State
					d.State = StateRunning
					if d.logFile != nil {
						evidence := "health probe passed"
						if d.HealthMode == "process" {
							evidence = "process liveness observed"
						}
						fmt.Fprintf(d.logFile, "[supervisor] daemon %s %s (mode=%s); state %s -> %s\n", d.Spec.Name, evidence, d.HealthMode, prev, StateRunning)
					}
				}
			} else {
				d.HealthError = err.Error()
				sinceOK := time.Since(d.StartedAt)
				if d.State == StateRunning {
					sinceOK = time.Since(d.LastHealth)
				}
				if (d.State == StateStarting || d.State == StateRunning) && sinceOK > s.startupHealthTimeout {
					prev := d.State
					d.State = StateUnhealthy
					if d.logFile != nil {
						fmt.Fprintf(d.logFile, "[supervisor] daemon %s health probe failing for %v (timeout %v); state %s -> %s\n", d.Spec.Name, sinceOK.Round(time.Millisecond), s.startupHealthTimeout, prev, StateUnhealthy)
					}
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
	if d.cancelFunc != nil {
		d.cancelFunc()
	}
	if d.healthCancel != nil {
		d.healthCancel()
	}
	if d.done != nil {
		close(d.done)
	}
	if d.monitorDone != nil {
		<-d.monitorDone
	}

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

		// A restart creates a replacement ManagedDaemon with its own log
		// descriptor. The exited daemon must release its descriptor after the
		// failure record and restart decision have been written.
		d.mu.Lock()
		if d.logFile == logFile && d.logFile != nil {
			_ = d.logFile.Close()
			d.logFile = nil
		}
		d.mu.Unlock()
	}
}

// handleDaemonFailure handles a daemon crash and potentially restarts it.
func (s *Supervisor) handleDaemonFailure(d *ManagedDaemon) {
	s.mu.RLock()
	if s.daemons[d.Spec.Name] != d {
		s.mu.RUnlock()
		return
	}
	d.mu.Lock()
	// Avoid double-handling the same failure after repeated notifications.
	// If a daemon is already in a terminal/recovery state, a second failure signal is noise.
	if d.State == StateStopping || d.State == StateStopped || d.State == StateFailed || d.State == StateRestarting {
		d.mu.Unlock()
		s.mu.RUnlock()
		return
	}
	d.State = StateRestarting
	d.mu.Unlock()
	s.mu.RUnlock()

	for {
		d.mu.Lock()
		if d.State != StateRestarting {
			d.mu.Unlock()
			return
		}
		d.Restarts++
		restarts := d.Restarts
		if restarts > s.maxRestarts {
			if err := d.ownership.finishWithCleanup(func() { s.removePIDFile(d.Spec.Name) }); err != nil {
				d.LastError = err.Error()
			}
			d.State = StateFailed
			if d.logFile != nil {
				fmt.Fprintf(d.logFile, "[supervisor] max restarts (%d) exceeded, not restarting\n", s.maxRestarts)
			}
			d.mu.Unlock()
			return
		}

		backoff := s.restartBackoff(restarts)
		if d.logFile != nil {
			fmt.Fprintf(d.logFile, "[supervisor] restarting in %v (attempt %d/%d)\n", backoff, restarts, s.maxRestarts)
		}
		d.mu.Unlock()

		timer := time.NewTimer(backoff)
		select {
		case <-s.shutdownCh:
			timer.Stop()
			return
		case <-timer.C:
		}

		attempted, err := s.restartDaemon(d)
		if !attempted || err == nil {
			return
		}

		if errors.Is(err, ErrDaemonRecoveryRequired) {
			d.mu.Lock()
			d.LastError = errors.Join(err, d.ownership.finish()).Error()
			if d.State == StateRestarting {
				d.State = StateFailed
			}
			d.mu.Unlock()
			return
		}

		// Port collisions, binary upgrades and launch errors all consume the
		// same bounded restart budget as a new process that immediately exits.
		// Retain this generation and its log while the next attempt is pending.
		d.mu.Lock()
		if d.logFile != nil {
			fmt.Fprintf(d.logFile, "[supervisor] restart failed: %v\n", err)
		}
		d.mu.Unlock()
	}
}

// restartBackoff doubles the delay until the configured cap, without shifts
// or duration multiplication overflowing when a large budget is configured.
func (s *Supervisor) restartBackoff(attempt int) time.Duration {
	backoff := time.Second
	for i := 1; i < attempt && backoff < s.restartBackoffMax; i++ {
		if backoff > s.restartBackoffMax/2 {
			return s.restartBackoffMax
		}
		backoff *= 2
	}
	if backoff > s.restartBackoffMax {
		return s.restartBackoffMax
	}
	return backoff
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

const daemonHealthTimeout = 3 * time.Second
const daemonHealthWaitDelay = 2 * time.Second

// Compatibility helpers use the same bounded probes as the live monitor.
func (s *Supervisor) checkHealthHTTP(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), daemonHealthTimeout)
	defer cancel()
	_, err := daemonHealthResponse(ctx, http.MethodGet, url, nil, false)
	return err == nil
}

func (s *Supervisor) checkHealthMCP(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), daemonHealthTimeout)
	defer cancel()
	return probeDaemonMCP(ctx, url) == nil
}

func (s *Supervisor) checkHealthCmd(args []string) bool {
	if len(args) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonHealthTimeout)
	defer cancel()
	return probeDaemonHealth(ctx, DaemonSpec{HealthCmd: args}, s.projectDir) == nil
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
			// cm 0.2.x speaks MCP JSON-RPC at its root and has NO REST
			// /health endpoint, so its probe is the MCP round-trip
			// (HealthMCP), never a GET HealthURL — a REST probe here can
			// never pass and pins the daemon in "starting" forever
			// (bd-ws1-truth-safety-l5ddi.2).
			Name:        "cm",
			Command:     "cm",
			Args:        []string{"serve"},
			HealthMCP:   true,
			PortFlag:    "--port",
			DefaultPort: 8200,
			// A busy default port almost always means ANOTHER cm is already
			// serving this store. Falling back to a random port would silently
			// start a SECOND cm against the same database — on first start and
			// again on every supervised restart — so refuse instead and tell
			// the operator which port is contested (bd-2c0yh.1).
			NoPortFallback: true,
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
