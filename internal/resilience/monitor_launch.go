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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	return `(?:^|[[:space:]])(?:[^[:space:]]*/)?` + regexp.QuoteMeta(execName) + `[[:space:]]+internal-monitor[[:space:]]+` + regexp.QuoteMeta(session) + `(?:[[:space:]]|$)`
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

// KillExistingMonitorProcess terminates any running internal monitor for the
// session.
func KillExistingMonitorProcess(session string) error {
	if err := tmux.ValidateSessionName(session); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
	}
	pattern := MonitorProcessPattern(session)
	if strings.TrimSpace(pattern) == "" {
		return errors.New("empty monitor process pattern")
	}
	return exec.Command("pkill", "-f", pattern).Run()
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
		Session:     req.Session,
		ProjectDir:  req.ProjectDir,
		AutoRestart: req.AutoRestart,
	}
	for _, agent := range req.Agents {
		if restartUnsupportedAgentTypes[agent.Type] {
			// Restart remains unsupported for these types until lifecycle
			// fixtures prove the necessary semantics.
			continue
		}
		manifest.Agents = append(manifest.Agents, agent)
	}
	return manifest
}

// SpawnMonitorRequest describes a freshly spawned session for the shared
// manifest + monitor path. Callers pass ALL launched agents;
// restart-unsupported types are filtered here so the filtering rule has a
// single definition.
type SpawnMonitorRequest struct {
	Session     string
	ProjectDir  string
	AutoRestart bool
	Agents      []AgentConfig
}

// SpawnMonitorResult reports what the shared path did.
type SpawnMonitorResult struct {
	Manifest       *SpawnManifest
	MonitorStarted bool
	MonitorPID     int
}

// StartSessionMonitor is the single manifest-writing + monitor-launching code
// path shared by CLI spawn and robot spawn. It:
//  1. builds and persists the SpawnManifest (filtering restart-unsupported
//     agent types),
//  2. replaces any already-running monitor for the session, and
//  3. launches the detached internal monitor with its log wired to LogDir().
//
// The call is best-effort from the caller's perspective: a non-nil error must
// never fail the spawn itself. When the monitor is disabled (test binary or
// NTM_DISABLE_INTERNAL_MONITOR) it returns ErrInternalMonitorDisabled without
// writing anything. A canceled context is reported via the wrapped context
// error so callers can distinguish cancellation from monitor failure.
func StartSessionMonitor(ctx context.Context, req SpawnMonitorRequest) (*SpawnMonitorResult, error) {
	if !ShouldStartInternalMonitor() {
		return nil, ErrInternalMonitorDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}

	manifest := buildSpawnManifest(req)
	if err := SaveManifest(manifest); err != nil {
		return nil, fmt.Errorf("saving resilience manifest: %w", err)
	}
	result := &SpawnMonitorResult{Manifest: manifest}

	// Replace an already-running monitor for this session.
	if IsMonitorAlive(req.Session) {
		if err := KillExistingMonitorProcess(req.Session); err == nil {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return result, fmt.Errorf("session monitor replacement canceled: %w", ctx.Err())
			}
		}
		// A failed kill is non-fatal: proceed and let the new monitor start.
	}

	cmd, err := NewInternalMonitorCommand(req.Session)
	if err != nil {
		return result, fmt.Errorf("preparing session monitor: %w", err)
	}

	// Wire monitor output to the session log.
	var logFile *os.File
	logDir := LogDir()
	if err := os.MkdirAll(logDir, 0755); err == nil {
		logPath := filepath.Join(logDir, fmt.Sprintf("%s-monitor.log", req.Session))
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			logFile = f
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}

	// Detach from the terminal so the monitor survives when the spawner exits.
	setDetachedProcess(cmd)
	startErr := cmd.Start()
	if logFile != nil {
		// The child holds its own descriptor after Start; close the parent's.
		_ = logFile.Close()
	}
	if startErr != nil {
		return result, fmt.Errorf("starting session monitor: %w", startErr)
	}
	result.MonitorStarted = true
	result.MonitorPID = cmd.Process.Pid
	return result, nil
}
