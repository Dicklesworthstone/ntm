// Package process provides PID-based process liveness checks.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	gopsutil "github.com/shirou/gopsutil/v4/process"
)

// SnapshotProcess is one process lifetime observed by SnapshotContext. Started
// is ps's locale-independent start timestamp; Command is the kernel process
// name, not argv[0] (which the process may replace).
type SnapshotProcess struct {
	PID     int
	PPID    int
	PGID    int
	TPGID   int
	State   string
	Started string
	Command string
}

const (
	processSnapshotTimeout     = 5 * time.Second
	processSnapshotOutputLimit = 16 << 20
)

// SnapshotContext observes the local process table without collapsing a failed
// observation into an empty table. Only a successful, complete snapshot can
// establish that a previously observed process is absent. Zombies remain in
// the result so callers can distinguish termination from a missing observation.
// No command arguments or command stderr are included in the result or errors.
func SnapshotContext(ctx context.Context) (map[int]SnapshotProcess, error) {
	if ctx == nil {
		return nil, errors.New("process snapshot requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("process snapshots are unsupported on %s", runtime.GOOS)
	}
	probeCtx, cancel := context.WithTimeout(ctx, processSnapshotTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ps", "-ww", "-axo", "pid=,ppid=,pgid=,tpgid=,stat=,lstart=,ucomm=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = 100 * time.Millisecond
	var output processSnapshotBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		if probeCtx.Err() != nil {
			return nil, fmt.Errorf("process snapshot: %w", probeCtx.Err())
		}
		return nil, fmt.Errorf("process snapshot command failed: %w", err)
	}
	if err := probeCtx.Err(); err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	if output.exceeded {
		return nil, errors.New("process snapshot exceeds the output limit")
	}
	return parseProcessSnapshot(output.Bytes())
}

// Keep even a malfunctioning ps bounded without retaining private stderr.
type processSnapshotBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *processSnapshotBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *processSnapshotBuffer) Len() int      { return b.buffer.Len() }

func (b *processSnapshotBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := processSnapshotOutputLimit - b.Len()
	if n > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.buffer.Write(data)
	return n, nil
}

func parseProcessSnapshot(output []byte) (map[int]SnapshotProcess, error) {
	if len(output) == 0 || len(output) > processSnapshotOutputLimit || output[len(output)-1] != '\n' || bytes.IndexByte(output, 0) >= 0 {
		return nil, errors.New("process snapshot is empty, oversized or incomplete")
	}
	rows := strings.Split(string(output[:len(output)-1]), "\n")
	result := make(map[int]SnapshotProcess, len(rows))
	for i, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < 11 {
			return nil, fmt.Errorf("process snapshot row %d is incomplete", i+1)
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		pgid, pgidErr := strconv.Atoi(fields[2])
		tpgid, tpgidErr := strconv.Atoi(fields[3])
		if pidErr != nil || ppidErr != nil || pgidErr != nil || tpgidErr != nil || pid <= 0 || ppid < 0 || pgid < 0 || tpgid < -1 {
			return nil, fmt.Errorf("process snapshot row %d has invalid process identifiers", i+1)
		}
		if _, exists := result[pid]; exists {
			return nil, fmt.Errorf("process snapshot row %d repeats a process identifier", i+1)
		}
		state := fields[4]
		if !strings.ContainsRune("RSDTtZXxIKWPUE", rune(state[0])) || strings.IndexFunc(state, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '<' || r == '>' || r == '+')
		}) >= 0 {
			return nil, fmt.Errorf("process snapshot row %d has an invalid process state", i+1)
		}
		started := strings.Join(fields[5:10], " ")
		if _, err := time.Parse("Mon Jan 2 15:04:05 2006", started); err != nil {
			return nil, fmt.Errorf("process snapshot row %d has an invalid start time", i+1)
		}
		result[pid] = SnapshotProcess{
			PID: pid, PPID: ppid, PGID: pgid, TPGID: tpgid,
			State: state, Started: started, Command: strings.Join(fields[10:], " "),
		}
	}
	return result, nil
}

// StartTime returns the process's start time. Used as the pane-age source
// (tmux has no per-pane creation-time format variable): a pane's shell PID is
// created at pane creation and replaced on respawn, so its process start time
// is exactly the "how old is this pane's current incarnation" answer
// age-based replacement policies need (ntm-qvpm).
func StartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("invalid pid %d", pid)
	}
	proc, err := gopsutil.NewProcess(int32(pid))
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d: %w", pid, err)
	}
	createMs, err := proc.CreateTime()
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d create time: %w", pid, err)
	}
	if createMs <= 0 {
		return time.Time{}, fmt.Errorf("process %d reported non-positive create time", pid)
	}
	return time.UnixMilli(createMs).UTC(), nil
}

// IsAlive checks whether a process with the given PID is still running.
// It uses /proc on Linux for an efficient, non-racy check and falls back
// to kill(pid, 0) plus a `ps` zombie-state probe on other platforms.
//
// Zombies are deliberately treated as not-alive: a zombie process is
// terminated but unreaped, and from any practical liveness standpoint
// (e.g. the memory-daemon supervisor) it is gone. Without this, the
// kill(pid, 0) fallback used on macOS would happily report a zombie as
// alive — that's the macOS-only failure in
// TestCheckMemoryDaemon_ZombiePIDFileIsCleanedUp.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	// Windows: os.Process.Signal only implements os.Kill — any other signal
	// (including the POSIX signal-0 probe below) returns EWINDOWS, so the
	// fallback path would report every live process as dead. That silently
	// broke every Windows consumer of IsAlive (e.g. cm daemon PID-file
	// discovery, which would always skip a live daemon). gopsutil implements
	// existence via the process snapshot API, and Windows has no zombie
	// state to filter.
	if runtime.GOOS == "windows" {
		proc, err := gopsutil.NewProcess(int32(pid))
		if err != nil {
			return false
		}
		running, err := proc.IsRunning()
		return err == nil && running
	}

	// Fast path: inspect /proc state directly on Linux. Zombie and dead
	// processes still have a /proc entry until reaped, but they are not alive.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "State:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				state := fields[1]
				return state != "Z" && state != "X" && state != "x"
			}
			break
		}
		return true
	}

	// Native snapshot (darwin): one sysctl lookup answers both existence and
	// zombie state, replacing the kill(0)+`ps` probe below (bd-4479y).
	if alive, ok := nativeIsAlive(pid); ok {
		return alive
	}

	// Fallback: signal 0 check (works on all POSIX systems).
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}

	// Signal 0 succeeded — but on macOS that includes zombies, which
	// /proc would have rejected on Linux. Cross-check the ps state so
	// the two platforms agree on what "alive" means.
	if cmd := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)); cmd != nil {
		if out, perr := cmd.Output(); perr == nil {
			state := strings.TrimSpace(string(out))
			if state != "" {
				short := string(state[0])
				if short == "Z" || short == "X" || short == "x" {
					return false
				}
			}
		}
	}
	return true
}

// HasChildAlive returns true if the given shell PID has at least one
// living child process. This is useful for detecting whether an agent
// launched inside a tmux shell pane is still running.
//
// We enumerate ALL children of the parent and return true on the first
// living one. The original implementation returned only GetChildPID's
// first-child answer and then checked its liveness; under heavy
// parallel-test load, that first child could be a zombie of an
// unrelated parallel test even though plenty of healthy children
// remained, which flapped TestDetectProcessStatus_PIDWithChildren and
// TestDetectProcessStatus_PIDBasedCurrentProcess on busy runners.
func HasChildAlive(shellPID int) bool {
	if shellPID <= 0 {
		return false
	}
	// Native single-syscall path (darwin): the portable fallback below costs
	// one pgrep plus one ps per child, which robot status pays once per pane,
	// serially (bd-4479y).
	if alive, ok := nativeHasChildAlive(shellPID); ok {
		return alive
	}
	for _, child := range getChildPIDs(shellPID) {
		if IsAlive(child) {
			return true
		}
	}
	return false
}

// GetChildPID returns the first child PID of the given parent, or 0 if
// no child is found. It reads /proc on Linux and falls back to pgrep.
// Kept for backward compatibility — callers that need
// "is at least one child alive" should use HasChildAlive directly.
func GetChildPID(parentPID int) int {
	if parentPID <= 0 {
		return 0
	}
	children := getChildPIDs(parentPID)
	if len(children) == 0 {
		return 0
	}
	return children[0]
}

// getChildPIDs returns every direct child PID of parentPID. It uses
// /proc on Linux (walking every task/<tid>/children entry to catch
// children adopted by non-main threads, which Go's runtime can do
// when ForkExec runs on a non-main goroutine) and falls back to pgrep
// on systems without /proc.
func getChildPIDs(parentPID int) []int {
	if parentPID <= 0 {
		return nil
	}

	seen := make(map[int]struct{})
	var pids []int
	add := func(pid int) {
		if pid <= 0 {
			return
		}
		if _, ok := seen[pid]; ok {
			return
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}

	// /proc walk: every thread of the parent can be the recorded
	// fork() caller in Linux's accounting, so read every
	// task/<tid>/children file.
	taskDir := fmt.Sprintf("/proc/%d/task", parentPID)
	if entries, err := os.ReadDir(taskDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := fmt.Sprintf("%s/%s/children", taskDir, entry.Name())
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			for _, raw := range strings.Fields(string(data)) {
				if pid, perr := strconv.Atoi(raw); perr == nil {
					add(pid)
				}
			}
		}
	}

	// Native snapshot (darwin): one sysctl call replaces the pgrep spawn.
	if len(pids) == 0 {
		if native, ok := nativeChildPIDs(parentPID); ok {
			for _, pid := range native {
				add(pid)
			}
			return pids
		}
	}

	// Fallback / supplement: pgrep also picks up children where the
	// parent's /proc layout is absent (macOS) or where /proc lists
	// have raced behind the kernel.
	if cmd := exec.Command("pgrep", "-P", strconv.Itoa(parentPID)); cmd != nil {
		if out, err := cmd.Output(); err == nil {
			for _, raw := range strings.Fields(string(out)) {
				if pid, perr := strconv.Atoi(raw); perr == nil {
					add(pid)
				}
			}
		}
	}

	return pids
}

// IsChildAlive is an alias for HasChildAlive for backward compatibility.
var IsChildAlive = HasChildAlive

// GetCmdline returns the full argv of the process with the given PID,
// or an empty slice if it can't be read. On Linux this uses
// `/proc/<pid>/cmdline`; on other systems it falls back to `ps -o
// command=`.
//
// This is useful for distinguishing wrapper processes (`bun`, `node`,
// `npx`, `python`) from the agent binary they launched: the wrapper
// shows up as the process's basename, but the agent it's running is
// usually visible in argv (e.g. `bun /home/.../codex ...`).
func GetCmdline(pid int) []string {
	return GetCmdlineContext(context.Background(), pid)
}

// GetCmdlineContext reads argv while allowing callers to stop the external
// ps fallback. Native /proc reads remain the inexpensive first choice.
func GetCmdlineContext(ctx context.Context, pid int) []string {
	if ctx == nil || ctx.Err() != nil || pid <= 0 {
		return nil
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		// /proc/.../cmdline is NUL-separated, with a trailing NUL.
		raw := strings.TrimRight(string(data), "\x00")
		if raw == "" {
			return nil
		}
		return strings.Split(raw, "\x00")
	}
	// Fallback for non-Linux. `ps -o command=` returns a single
	// space-separated string with no easy way to recover argv0
	// boundaries inside arguments, but it's enough for substring
	// matching against agent binary names.
	cmd := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid))
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil
	}
	return strings.Fields(line)
}

// GetChildPIDs returns up to `limit` direct children of `parentPID`.
// Linux fast path reads `/proc/<pid>/task/<pid>/children`; falls back
// to `pgrep -P`. Returns nil on failure.
func GetChildPIDs(parentPID int, limit int) []int {
	return GetChildPIDsContext(context.Background(), parentPID, limit)
}

// GetChildPIDsContext reads the bounded child list with cancellation for the
// pgrep fallback, so optional pane inspection cannot outlive its operation.
func GetChildPIDsContext(ctx context.Context, parentPID int, limit int) []int {
	if ctx == nil || ctx.Err() != nil || parentPID <= 0 {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}

	collect := func(parts []string) []int {
		var out []int
		for _, p := range parts {
			pid, err := strconv.Atoi(p)
			if err != nil || pid <= 0 {
				continue
			}
			out = append(out, pid)
			if len(out) >= limit {
				return out
			}
		}
		return out
	}

	taskPath := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	if data, err := os.ReadFile(taskPath); err == nil {
		return collect(strings.Fields(string(data)))
	}
	if native, ok := nativeChildPIDs(parentPID); ok {
		var out []int
		for _, pid := range native {
			if pid <= 0 {
				continue
			}
			out = append(out, pid)
			if len(out) >= limit {
				break
			}
		}
		return out
	}
	cmd := exec.CommandContext(ctx, "pgrep", "-P", strconv.Itoa(parentPID))
	cmd.WaitDelay = 100 * time.Millisecond
	if out, err := cmd.Output(); err == nil {
		return collect(strings.Fields(string(out)))
	}
	return nil
}

// processStateNames maps single-character /proc state codes to human names.
var processStateNames = map[string]string{
	"R": "running",
	"S": "sleeping",
	"D": "disk sleep",
	"Z": "zombie",
	"T": "stopped",
	"t": "tracing stop",
	"X": "dead",
	"x": "dead",
	"K": "wakekill",
	"W": "waking",
	"P": "parked",
	"I": "idle",
}

// GetProcessState reads the process state.
// It tries /proc/<pid>/status on Linux and falls back to ps on macOS/Linux.
// Returns the single-character state code (R, S, D, Z, T, etc.),
// a human-readable name, and any error.
// TerminalForeground reports which process group currently owns the
// controlling terminal of pid (tpgid) and pid's own process group (pgrp),
// read from /proc/<pid>/stat. A shell whose pgrp equals tpgid is itself in
// the foreground (idle, or running a builtin / same-group rc children); a job
// it launched with job control owns the terminal under a different group.
// ok is false when the fields cannot be read (non-Linux, process gone).
func TerminalForeground(pid int) (tpgid, pgrp int, ok bool) {
	if pid <= 0 {
		return 0, 0, false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	// "pid (comm) state ppid pgrp session tty_nr tpgid ..." — comm may contain
	// spaces and parentheses, so split after the LAST ')'.
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 > len(text) {
		return 0, 0, false
	}
	fields := strings.Fields(text[end+2:])
	// fields[0]=state fields[1]=ppid fields[2]=pgrp fields[3]=session fields[4]=tty_nr fields[5]=tpgid
	if len(fields) < 6 {
		return 0, 0, false
	}
	pg, err1 := strconv.Atoi(fields[2])
	tp, err2 := strconv.Atoi(fields[5])
	if err1 != nil || err2 != nil || pg <= 0 {
		return 0, 0, false
	}
	return tp, pg, true
}

func GetProcessState(pid int) (string, string, error) {
	if pid <= 0 {
		return "", "", fmt.Errorf("invalid pid: %d", pid)
	}

	// Try /proc first (Linux)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "State:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					state := fields[1]
					name := processStateNames[state]
					if name == "" {
						name = "unknown"
					}
					return state, name, nil
				}
			}
		}
	}

	// Native snapshot (darwin): one sysctl lookup replaces the ps spawn,
	// which robot status pays once per pane, serially (bd-4479y).
	if state, ok := nativeProcessState(pid); ok {
		name := processStateNames[state]
		if name == "" && state == "I" {
			name = "idle"
		}
		if name == "" {
			name = "unknown"
		}
		return state, name, nil
	}

	// Fallback to ps (macOS and Linux)
	// 'state' column provides the process state
	cmd := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err == nil {
		state := strings.TrimSpace(string(out))
		if state != "" {
			// ps state can have multiple chars (e.g., S+ on macOS), we take the first
			shortState := string(state[0])
			name := processStateNames[shortState]
			if name == "" {
				// macOS specific states
				switch shortState {
				case "I":
					name = "idle"
				case "U":
					name = "uninterruptible"
				default:
					name = "unknown"
				}
			}
			return shortState, name, nil
		}
	}

	return "", "", fmt.Errorf("failed to get process state for pid %d", pid)
}
