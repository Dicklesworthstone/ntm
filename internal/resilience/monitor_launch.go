// monitor_launch.go — the ONE shared spawn-manifest + monitor-launch code path
// (WS0-G6 single-definition contract, bead bd-ws1-truth-safety-l5ddi.8).
//
// Both CLI spawn (internal/cli/spawn.go) and robot spawn
// (internal/robot/spawn.go) call StartSessionMonitor; neither constructs a
// SpawnManifest literal or re-derives the monitor process mechanics. The
// contracts lint (scripts/guards/contracts_lint.sh) enforces that
// resilience.SpawnManifest composite literals exist only inside
// internal/resilience/.
package resilience

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ErrInternalMonitorDisabled is returned by StartSessionMonitor when the
// internal monitor is disabled (test binary or NTM_DISABLE_INTERNAL_MONITOR).
// Callers treat this as "not started, not a failure".
var ErrInternalMonitorDisabled = errors.New("internal session monitor disabled")

// restartUnsupportedAgentTypes lists agent types excluded from the manifest
// because restart semantics are unproven for them. Grok Build restart remains
// unsupported until an authenticated Grok Build TUI lifecycle fixture proves
// the necessary semantics.
var restartUnsupportedAgentTypes = map[string]bool{
	"grok": true,
}

// ShouldStartInternalMonitor reports whether spawn should launch the detached
// internal monitor process.
//
// When invoked from package tests, os.Executable() points at a `*.test`
// binary. Spawning "internal-monitor" via that binary re-runs the entire test
// suite recursively (detached), which can quickly fork-bomb the machine, so
// the guard refuses under `go test` and when NTM_DISABLE_INTERNAL_MONITOR is
// set.
func ShouldStartInternalMonitor() bool {
	if flag.Lookup("test.v") != nil {
		return false
	}
	if os.Getenv("NTM_DISABLE_INTERNAL_MONITOR") != "" {
		return false
	}
	return true
}

// MonitorProcessPatternForExecutable builds the anchored pgrep/pkill regex for
// the internal monitor of a session, given the launching executable path. The
// pattern matches the binary name at a word boundary followed by the exact
// subcommand and session name, avoiding false positives from processes whose
// paths or arguments happen to contain "ntm".
func MonitorProcessPatternForExecutable(executablePath, session string) string {
	execName := strings.TrimSpace(filepath.Base(executablePath))
	if execName == "" {
		execName = "ntm"
	}
	// pgrep/pkill use POSIX ERE, which does not support Go's (?:...) groups.
	return `(^|[[:space:]])([^[:space:]]*/)?` + regexp.QuoteMeta(execName) + `[[:space:]]+internal-monitor[[:space:]]+` + regexp.QuoteMeta(session) + `([[:space:]]|$)`
}

// MonitorProcessPattern builds the monitor process pattern for the current
// executable.
func MonitorProcessPattern(session string) string {
	executablePath, err := os.Executable()
	if err != nil {
		executablePath = "ntm"
	}
	return MonitorProcessPatternForExecutable(executablePath, session)
}

// IsMonitorAlive checks whether the resilience monitor process is running for
// the given session by looking for the "internal-monitor <session>" process.
func IsMonitorAlive(session string) bool {
	err := exec.Command("pgrep", "-f", MonitorProcessPattern(session)).Run()
	return err == nil
}

// A pre-ownership-lock binary can still be resident during an upgrade. Do not
// duplicate it or claim that generation-scoped stop shut it down. A current
// owner that has already joined its loops and written a terminal receipt can
// briefly retain its PID while returning from main; that process is harmless.
func unownedMonitorAlive(ctx context.Context, session string) (bool, error) {
	output, err := exec.CommandContext(ctx, "pgrep", "-f", MonitorProcessPattern(session)).Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil // pgrep found no matching process.
		}
		return false, fmt.Errorf("inspect existing session monitor: %w", err)
	}
	status, _ := ReadSessionMonitorStatus(session)
	for _, raw := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 0 {
			continue
		}
		if status != nil && status.PID == pid && (status.State == "stopped" || status.State == "failed") {
			started, err := process.StartTime(pid)
			if err == nil && started.Equal(status.ProcessStarted) {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

func currentExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	exe = filepath.Clean(exe)
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("current executable path must be absolute: %q", exe)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("stat current executable: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("current executable path is a directory: %q", exe)
	}
	return exe, nil
}

// NewInternalMonitorCommand builds the detachable "ntm internal-monitor
// <session>" command for the current executable.
func NewInternalMonitorCommand(session string) (*exec.Cmd, error) {
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	exe, err := currentExecutablePath()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "internal-monitor", session), nil
}

// buildSpawnManifest constructs the SpawnManifest for a spawn request. It is
// the single place manifest construction happens (the contracts lint rejects
// SpawnManifest literals outside internal/resilience/).
func buildSpawnManifest(req SpawnMonitorRequest) *SpawnManifest {
	manifest := &SpawnManifest{
		Session:         req.Session,
		ProjectDir:      req.ProjectDir,
		AutoRestart:     req.AutoRestart,
		ConfigPath:      req.ConfigPath,
		SessionIdentity: req.SessionIdentity,
	}
	if req.AccountRotation != nil {
		options := *req.AccountRotation
		options.Providers = append([]string(nil), options.Providers...)
		manifest.AccountRotation = &options
	}
	for _, agentConfig := range req.Agents {
		if restartUnsupportedAgentTypes[agentConfig.Type] {
			// Restart remains unsupported for these types until lifecycle
			// fixtures prove the necessary semantics.
			continue
		}
		agentConfig.LaunchBinding = CloneLaunchBinding(agentConfig.LaunchBinding)
		if agentConfig.LaunchBinding == nil {
			agentConfig.LaunchBinding = CaptureLaunchBinding(agentConfig.Type)
		}
		manifest.Agents = append(manifest.Agents, agentConfig)
	}
	return manifest
}

// SpawnMonitorRequest describes a freshly spawned session for the shared
// manifest + monitor path. Callers pass ALL launched agents;
// restart-unsupported types are filtered here so the filtering rule has a
// single definition.
type SpawnMonitorRequest struct {
	Session         string
	SessionIdentity string
	ProjectDir      string
	AutoRestart     bool
	Agents          []AgentConfig
	ConfigPath      string
	AccountRotation *RotationMonitorOptions
}

// SpawnMonitorResult reports what the shared path did.
type SpawnMonitorResult struct {
	Manifest       *SpawnManifest `json:"manifest,omitempty"`
	MonitorStarted bool           `json:"monitor_started"`
	MonitorPID     int            `json:"monitor_pid,omitempty"`
	Generation     string         `json:"generation,omitempty"`
}

// PreflightSessionMonitor validates a resident launch without writing files or
// starting subprocesses. Physical pane IDs are not required until launch.
func PreflightSessionMonitor(req SpawnMonitorRequest) error {
	if err := preflightLocalMonitorClient(); err != nil {
		return err
	}
	if !ShouldStartInternalMonitor() {
		return ErrInternalMonitorDisabled
	}
	if err := monitorPlatformSupported(); err != nil {
		return err
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return err
	}
	if !filepath.IsAbs(req.ProjectDir) {
		return errors.New("session monitor project directory must be absolute")
	}
	info, err := os.Stat(req.ProjectDir)
	if err != nil {
		return fmt.Errorf("session monitor project directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("session monitor project path is not a directory")
	}
	if req.ConfigPath != "" && !filepath.IsAbs(req.ConfigPath) {
		return errors.New("session monitor config path must be absolute")
	}
	_, err = currentExecutablePath()
	return err
}

func preflightLocalMonitorClient() error {
	if tmux.DefaultClient.Remote != "" {
		return errors.New("session monitoring requires a local tmux client; remote residents are unsupported")
	}
	return nil
}

// StartSessionMonitor is the single manifest and resident-launch path. It
// refuses a live owner instead of replacing it, and reports success only after
// the child has acquired ownership, initialized and acknowledged authorization.
func StartSessionMonitor(ctx context.Context, req SpawnMonitorRequest) (*SpawnMonitorResult, error) {
	if err := PreflightSessionMonitor(req); err != nil {
		return nil, err
	}
	return startSessionMonitor(ctx, req, NewInternalMonitorCommand)
}

func startSessionMonitor(ctx context.Context, req SpawnMonitorRequest, command func(string) (*exec.Cmd, error)) (*SpawnMonitorResult, error) {
	if err := preflightLocalMonitorClient(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, monitorStartupTimeout)
	defer cancel()
	prefix, err := prepareMonitorPrefix(req.Session)
	if err != nil {
		return nil, err
	}
	launchLock, err := waitMonitorLock(ctx, prefix+".launch.lock")
	if err != nil {
		return nil, err
	}
	defer launchLock.Close()
	owner, err := tryMonitorLock(prefix + ".owner.lock")
	if err != nil {
		return nil, err
	}
	_ = owner.Close()
	legacyAlive, err := unownedMonitorAlive(ctx, req.Session)
	if err != nil {
		return nil, err
	}
	if legacyAlive {
		return nil, fmt.Errorf("%w: an older monitor has no generation control; stop it before launching another", ErrSessionMonitorOwned)
	}
	manifest := buildSpawnManifest(req)
	manifest.MonitorGeneration, err = newMonitorGeneration()
	if err != nil {
		return nil, err
	}
	if err := SaveManifest(manifest); err != nil {
		return nil, fmt.Errorf("saving resilience manifest: %w", err)
	}
	result := &SpawnMonitorResult{Manifest: manifest, Generation: manifest.MonitorGeneration}
	cmd, err := command(req.Session)
	if err != nil {
		return result, fmt.Errorf("preparing session monitor: %w", err)
	}
	if cmd == nil {
		return result, errors.New("session monitor command builder returned nil")
	}
	if req.ConfigPath != "" {
		cmd.Args = append(cmd.Args, "--config", req.ConfigPath)
	}
	cmd.Dir = req.ProjectDir
	baseEnv := cmd.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = make([]string, 0, len(baseEnv)+1)
	for _, entry := range baseEnv {
		if !strings.HasPrefix(entry, monitorGenerationEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, monitorGenerationEnv+"="+manifest.MonitorGeneration)
	if err := os.MkdirAll(LogDir(), 0700); err != nil {
		return result, err
	}
	logPath := filepath.Join(LogDir(), req.Session+"-monitor.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return result, fmt.Errorf("open session monitor log: %w", err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	input, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	defer input.Close()
	setDetachedProcess(cmd)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("starting session monitor: %w", err)
	}
	result.MonitorPID = cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	committed, authorized, childExited := false, false, false
	defer func() {
		if committed || childExited {
			return
		}
		_ = input.Close()
		// Let the owning child join loops and run daemon cleanup first. The
		// request cannot stop a different generation; Kill is only a bounded
		// fallback against this exact process handle when startup is wedged.
		_ = writeMonitorJSON(prefix+".stop.json", monitorStopRequest{Generation: manifest.MonitorGeneration})
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			if err := cmd.Process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
				// Wait has no output pipes to drain: both streams are files.
				// Reap this exact child before returning its failed startup.
				<-exited
			}
		}
	}()
	ticker := time.NewTicker(monitorControlPoll)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("session monitor startup interrupted: %w", err)
		}
		status, readErr := ReadSessionMonitorStatus(req.Session)
		if readErr == nil && status.Generation == manifest.MonitorGeneration && status.PID == cmd.Process.Pid {
			if status.State == "failed" {
				return result, fmt.Errorf("session monitor initialization failed: %s (log: %s)", status.Error, logPath)
			}
			if !authorized && status.State == "ready" && status.Alive {
				if _, err := io.WriteString(input, manifest.MonitorGeneration+"\n"); err != nil {
					return result, fmt.Errorf("authorize session monitor: %w", err)
				}
				authorized = true
			}
			if authorized && status.Healthy {
				committed, result.MonitorStarted = true, true
				return result, nil
			}
		}
		select {
		case err := <-exited:
			childExited = true
			if final, readErr := ReadSessionMonitorStatus(req.Session); readErr == nil &&
				final.Generation == manifest.MonitorGeneration && final.PID == cmd.Process.Pid && final.Error != "" {
				return result, fmt.Errorf("session monitor initialization failed: %s (log: %s)", final.Error, logPath)
			}
			return result, fmt.Errorf("session monitor exited before startup acknowledgment: %v (log: %s)", err, logPath)
		case <-ctx.Done():
			return result, fmt.Errorf("session monitor startup timed out: %w (log: %s)", ctx.Err(), logPath)
		case <-ticker.C:
		}
	}
}
