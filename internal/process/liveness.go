// Package process provides PID-based process liveness checks.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gopsutil "github.com/shirou/gopsutil/v4/process"
)

var (
	procRootOverride   atomic.Pointer[string]
	defaultProcFSOnce  sync.Once
	defaultProcFSReady bool
	defaultBootTimeMu  sync.Once
	defaultBootTimeSec int64
	defaultBootTimeErr error
)

const linuxClockTicks = 100

// HasProcFS reports whether Linux /proc is mounted and authoritative for
// process inspection in the active environment.
func HasProcFS() bool {
	return procFSAvailable(procRoot())
}

// useProcFSSnapshot reports whether SnapshotContext/SnapshotPIDContext should
// read Linux /proc directly instead of spawning `ps`. When a test explicitly
// shadows `ps` on PATH (e.g. to simulate failed observations or synthetic
// process-group metadata) and has not set a custom procRootOverride, honor the
// shadowed `ps` binary.
func useProcFSSnapshot(root string) bool {
	if !procFSAvailable(root) {
		return false
	}
	if procRootOverride.Load() != nil {
		return true
	}
	psPath, err := exec.LookPath("ps")
	if err != nil {
		return false
	}
	return psPath == "/usr/bin/ps" || psPath == "/bin/ps"
}

func procRoot() string {
	if p := procRootOverride.Load(); p != nil {
		return *p
	}
	return "/proc"
}

// isProcGoneErr reports whether a /proc read failed because the target process
// exited (ENOENT before open, or ESRCH if the process disappeared mid-read).
func isProcGoneErr(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func procFSAvailable(root string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if root == "/proc" {
		defaultProcFSOnce.Do(func() {
			_, err := os.Stat("/proc/self/status")
			defaultProcFSReady = err == nil
		})
		return defaultProcFSReady
	}
	_, err := os.Stat(filepath.Join(root, "self", "status"))
	return err == nil
}

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
	root := procRoot()
	if useProcFSSnapshot(root) {
		if table, err := snapshotFromProcFS(ctx, root); err == nil {
			return table, nil
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("process snapshot: %w", err)
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, processSnapshotTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ps", "-ww", "-axo", "pid=,ppid=,pgid=,tpgid=,stat=,lstart=,ucomm=")
	DetachControllingTerminal(cmd)
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

// SnapshotPIDContext observes a single process lifetime without scanning the
// entire process table on Linux. Returns (proc, true, nil) when found,
// (zero, false, nil) when the process is definitively absent, or an error when
// observation fails.
func SnapshotPIDContext(ctx context.Context, pid int) (SnapshotProcess, bool, error) {
	if ctx == nil {
		return SnapshotProcess{}, false, errors.New("process snapshot requires a context")
	}
	if err := ctx.Err(); err != nil {
		return SnapshotProcess{}, false, err
	}
	if pid <= 0 {
		return SnapshotProcess{}, false, nil
	}
	root := procRoot()
	if useProcFSSnapshot(root) {
		btime, err := procBootTime(root)
		if err == nil {
			data, rerr := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
			if rerr != nil {
				if isProcGoneErr(rerr) {
					return SnapshotProcess{}, false, nil
				}
			} else if proc, ok := parseProcStatSnapshot(pid, data, btime); ok {
				return proc, true, nil
			}
		}
	}
	snapshot, err := SnapshotContext(ctx)
	if err != nil {
		return SnapshotProcess{}, false, err
	}
	proc, exists := snapshot[pid]
	return proc, exists, nil
}

func procBootTime(root string) (int64, error) {
	if root == "/proc" {
		defaultBootTimeMu.Do(func() {
			defaultBootTimeSec, defaultBootTimeErr = readProcBootTime("/proc")
		})
		return defaultBootTimeSec, defaultBootTimeErr
	}
	return readProcBootTime(root)
}

func readProcBootTime(root string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			sec, perr := strconv.ParseInt(fields[1], 10, 64)
			if perr == nil && sec > 0 {
				return sec, nil
			}
		}
		break
	}
	return 0, errors.New("proc/stat missing btime")
}

func parseProcStatSnapshot(pid int, data []byte, btime int64) (SnapshotProcess, bool) {
	text := string(data)
	start := strings.IndexByte(text, '(')
	end := strings.LastIndexByte(text, ')')
	if start < 0 || end <= start || end+2 > len(text) {
		return SnapshotProcess{}, false
	}
	comm := text[start+1 : end]
	fields := strings.Fields(text[end+2:])
	// fields[0]=state fields[1]=ppid fields[2]=pgrp fields[5]=tpgid fields[19]=starttime
	if len(fields) < 20 {
		return SnapshotProcess{}, false
	}
	state := fields[0]
	if len(state) == 0 || !strings.ContainsRune("RSDTtZXxIKWPUE", rune(state[0])) {
		return SnapshotProcess{}, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgid, err2 := strconv.Atoi(fields[2])
	sid, errSid := strconv.Atoi(fields[3])
	tpgid, err3 := strconv.Atoi(fields[5])
	startTicks, err4 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || errSid != nil || err3 != nil || err4 != nil || ppid < 0 || pgid < 0 || tpgid < -1 {
		return SnapshotProcess{}, false
	}
	if pid == sid {
		state += "s"
	}
	if tpgid > 0 && pgid == tpgid {
		state += "+"
	}
	sec := btime + int64(startTicks/linuxClockTicks)
	nsec := int64(startTicks%linuxClockTicks) * (int64(time.Second) / linuxClockTicks)
	started := time.Unix(sec, nsec).In(time.Local).Format("Mon Jan 2 15:04:05 2006")
	return SnapshotProcess{
		PID:     pid,
		PPID:    ppid,
		PGID:    pgid,
		TPGID:   tpgid,
		State:   state,
		Started: started,
		Command: comm,
	}, true
}

func snapshotFromProcFS(ctx context.Context, root string) (map[int]SnapshotProcess, error) {
	btime, err := procBootTime(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	result := make(map[int]SnapshotProcess, len(entries))
	for i, entry := range entries {
		if i&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) == 0 || name[0] < '1' || name[0] > '9' {
			continue
		}
		pid, perr := strconv.Atoi(name)
		if perr != nil || pid <= 0 {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(root, name, "stat"))
		if rerr != nil {
			continue
		}
		if proc, ok := parseProcStatSnapshot(pid, data, btime); ok {
			result[pid] = proc
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("procfs snapshot returned no processes")
	}
	return result, nil
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
	root := procRoot()
	if data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status")); err == nil {
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
	} else if isProcGoneErr(err) && procFSAvailable(root) {
		return false
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
	// task/<tid>/children file. When /proc is authoritative on Linux,
	// avoid spawning pgrep (which scans the entire process table).
	root := procRoot()
	taskDir := filepath.Join(root, strconv.Itoa(parentPID), "task")
	if entries, err := os.ReadDir(taskDir); err == nil {
		readAnyChildren := false
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(taskDir, entry.Name(), "children")
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			readAnyChildren = true
			for _, raw := range strings.Fields(string(data)) {
				if pid, perr := strconv.Atoi(raw); perr == nil {
					add(pid)
				}
			}
		}
		if readAnyChildren {
			return pids
		}
		if procFSAvailable(root) {
			if _, statErr := os.Stat(taskDir); isProcGoneErr(statErr) {
				return pids
			}
		}
	} else if isProcGoneErr(err) && procFSAvailable(root) {
		return nil
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

	// Fallback for systems without /proc or Linux kernels lacking CONFIG_PROC_CHILDREN.
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
	root := procRoot()
	if data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
		// /proc/.../cmdline is NUL-separated, with a trailing NUL.
		raw := strings.TrimRight(string(data), "\x00")
		if raw == "" {
			return nil
		}
		return strings.Split(raw, "\x00")
	} else if isProcGoneErr(err) && procFSAvailable(root) {
		return nil
	}
	// Fallback for non-Linux. `ps -o command=` returns a single
	// space-separated string with no easy way to recover argv0
	// boundaries inside arguments, but it's enough for substring
	// matching against agent binary names.
	cmd := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid))
	DetachControllingTerminal(cmd)
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
// Linux fast path reads `/proc/<pid>/task/*/children`; falls back
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

	root := procRoot()
	taskDir := filepath.Join(root, strconv.Itoa(parentPID), "task")
	if entries, err := os.ReadDir(taskDir); err == nil {
		seen := make(map[int]struct{})
		var out []int
		readAnyChildren := false
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(taskDir, entry.Name(), "children"))
			if rerr != nil {
				continue
			}
			readAnyChildren = true
			for _, p := range strings.Fields(string(data)) {
				pid, perr := strconv.Atoi(p)
				if perr != nil || pid <= 0 {
					continue
				}
				if _, dup := seen[pid]; dup {
					continue
				}
				seen[pid] = struct{}{}
				out = append(out, pid)
				if len(out) >= limit {
					return out
				}
			}
		}
		if readAnyChildren {
			return out
		}
		if procFSAvailable(root) {
			if _, statErr := os.Stat(taskDir); isProcGoneErr(statErr) {
				return out
			}
		}
	} else if isProcGoneErr(err) && procFSAvailable(root) {
		return nil
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
	tpgid, pgrp, _, ok = SessionTerminalForeground(pid)
	return tpgid, pgrp, ok
}

// SessionTerminalForeground returns tpgid, pgrp, and session ID (sid) from
// /proc/<pid>/stat. A tmux pane's root process is the session leader of its
// PTY (sid == pid); when sid != pid, any controlling terminal was inherited
// from an outer session rather than owned by pid.
func SessionTerminalForeground(pid int) (tpgid, pgrp, sid int, ok bool) {
	if pid <= 0 {
		return 0, 0, 0, false
	}
	data, err := os.ReadFile(filepath.Join(procRoot(), strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, 0, false
	}
	// "pid (comm) state ppid pgrp session tty_nr tpgid ..." — comm may contain
	// spaces and parentheses, so split after the LAST ')'.
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 > len(text) {
		return 0, 0, 0, false
	}
	fields := strings.Fields(text[end+2:])
	// fields[0]=state fields[1]=ppid fields[2]=pgrp fields[3]=session fields[4]=tty_nr fields[5]=tpgid
	if len(fields) < 6 {
		return 0, 0, 0, false
	}
	pg, err1 := strconv.Atoi(fields[2])
	s, err2 := strconv.Atoi(fields[3])
	tp, err3 := strconv.Atoi(fields[5])
	if err1 != nil || err2 != nil || err3 != nil || pg <= 0 {
		return 0, 0, 0, false
	}
	return tp, pg, s, true
}

func GetProcessState(pid int) (string, string, error) {
	if pid <= 0 {
		return "", "", fmt.Errorf("invalid pid: %d", pid)
	}

	// Try /proc first (Linux)
	root := procRoot()
	if data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status")); err == nil {
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
	} else if isProcGoneErr(err) && procFSAvailable(root) {
		return "", "", fmt.Errorf("failed to get process state for pid %d", pid)
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
