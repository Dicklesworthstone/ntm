//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const stepReaperMaxCapturedStdout = 1 << 20 // 1 MiB bounded stdout mirror for crash-only receipts

var (
	cgroupLeafSequence    atomic.Uint64
	commandContainmentMap sync.Map // map[*exec.Cmd]*linuxContainmentHandle
)

type linuxContainmentHandle struct {
	tier        string
	leafDir     string
	pidfd       int
	reaperChild bool
}

// StepReaperOutcome carries the terminal status of `ntm internal-step-reaper`.
type StepReaperOutcome struct {
	ExitCode int
	Signal   syscall.Signal
}

// ExitFromStepReaper terminates the current subreaper process with the exact
// exit code or fatal signal of its direct child once all descendants are reaped.
func ExitFromStepReaper(outcome StepReaperOutcome) {
	if outcome.Signal != 0 {
		signal.Reset(outcome.Signal)
		_ = unix.Kill(os.Getpid(), outcome.Signal)
		os.Exit(128 + int(outcome.Signal))
	}
	os.Exit(outcome.ExitCode)
}

func hasTier3FaultInjectionHooks(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return ctx.Value(commandGroupObservationTestHookKey{}) != nil ||
		ctx.Value(commandLeaderObservationTestHookKey{}) != nil
}

func previewContainmentTier(ctx context.Context) string {
	forced := strings.TrimSpace(os.Getenv(ContainmentTierEnv))
	if validContainmentTier(forced) {
		return forced
	}
	if hasTier3FaultInjectionHooks(ctx) {
		return ContainmentTierProcPGID
	}
	if parent, err := resolveWritableCgroupV2Parent(); err == nil && parent != "" {
		return ContainmentTierCgroupV2
	}
	if _, ok := resolveStepReaperBinary(false); ok {
		return ContainmentTierSubreaperPIDFD
	}
	return ContainmentTierProcPGID
}

func startCommandWithContainment(ctx context.Context, cmd *exec.Cmd, opts commandStartOptions) (commandContainmentMetadata, error) {
	if cmd == nil {
		return commandContainmentMetadata{}, errors.New("cannot start nil command")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		configureCommandProcessGroup(cmd)
	}

	forced := strings.TrimSpace(os.Getenv(ContainmentTierEnv))
	if forced != "" && !validContainmentTier(forced) {
		return commandContainmentMetadata{}, fmt.Errorf("invalid %s=%q", ContainmentTierEnv, forced)
	}
	if forced == "" && hasTier3FaultInjectionHooks(ctx) {
		forced = ContainmentTierProcPGID
	}

	// Tier 1: cgroup_v2 (CLONE_INTO_CGROUP + pidfd + cgroup.events inotify)
	if forced == "" || forced == ContainmentTierCgroupV2 {
		meta, started, err := tryStartCommandCgroupV2(cmd, opts, forced == ContainmentTierCgroupV2)
		if started || err != nil {
			return meta, err
		}
	}

	// Tier 2: subreaper_pidfd (PR_SET_CHILD_SUBREAPER wrapper + pidfd)
	if forced == "" || forced == ContainmentTierSubreaperPIDFD {
		meta, started, err := tryStartCommandSubreaperPIDFD(cmd, opts, forced == ContainmentTierSubreaperPIDFD)
		if started || err != nil {
			return meta, err
		}
	}

	// Tier 3: proc_pgid_fallback
	applyShellExitReceiptIfApplicable(cmd, opts)
	if err := cmd.Start(); err != nil {
		return commandContainmentMetadata{}, err
	}
	meta := commandContainmentMetadata{
		Tier:         ContainmentTierProcPGID,
		LeaderPID:    cmd.Process.Pid,
		ExitCodeFile: opts.ExitCodeFile,
	}
	if opts.RecordLaunchMetadata {
		if ticks, _, err := readProcessStartTimeTicks(cmd.Process.Pid); err == nil {
			meta.LeaderStartTimeTicks = ticks
		}
	}
	commandContainmentMap.Store(cmd, &linuxContainmentHandle{
		tier:  ContainmentTierProcPGID,
		pidfd: -1,
	})
	return meta, nil
}

func applyShellExitReceiptIfApplicable(cmd *exec.Cmd, opts commandStartOptions) {
	if opts.ExitCodeFile == "" || len(cmd.Args) != 3 {
		return
	}
	if cmd.Args[0] == "/bin/sh" && cmd.Args[1] == "-c" {
		cmd.Args[2] = wrapShellCommandForExitReceipt(cmd.Args[2], opts.ExitCodeFile, opts.StepID, opts.StepHash)
	}
}

func resolveWritableCgroupV2Parent() (string, error) {
	override := strings.TrimSpace(os.Getenv(CgroupRootEnv))
	if override != "" {
		switch strings.ToLower(override) {
		case "none", "disabled", "false", "0":
			return "", errors.New("cgroup v2 containment disabled via NTM_CGROUP_ROOT")
		}
		var st unix.Statfs_t
		if err := unix.Statfs(override, &st); err != nil {
			return "", fmt.Errorf("statfs NTM_CGROUP_ROOT %q: %w", override, err)
		}
		if int64(st.Type) != int64(unix.CGROUP2_SUPER_MAGIC) {
			return "", fmt.Errorf("NTM_CGROUP_ROOT %q is not a cgroup2 filesystem", override)
		}
		if err := verifyDelegatedCgroupDomain(override); err != nil {
			return "", err
		}
		return override, nil
	}

	var rootSt unix.Statfs_t
	if err := unix.Statfs("/sys/fs/cgroup", &rootSt); err != nil {
		return "", err
	}
	if int64(rootSt.Type) != int64(unix.CGROUP2_SUPER_MAGIC) {
		return "", errors.New("/sys/fs/cgroup is not unified cgroup v2")
	}

	selfCg, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	relPath := ""
	for _, line := range strings.Split(string(selfCg), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "0::") {
			relPath = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if relPath == "" {
		return "", errors.New("unified cgroup v2 hierarchy entry missing from /proc/self/cgroup")
	}
	candidate := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(relPath, "/"))
	if err := verifyDelegatedCgroupDomain(candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func verifyDelegatedCgroupDomain(dir string) error {
	if err := unix.Access(dir, unix.W_OK|unix.X_OK); err != nil {
		return err
	}
	// If parent has enabled controllers in cgroup.subtree_control while also
	// holding internal processes, cgroup v2's no-internal-processes rule rejects
	// spawning into a child leaf with EBUSY. Check before touching exec.Cmd.
	if raw, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")); err == nil {
		if strings.TrimSpace(string(raw)) != "" {
			if procs, pErr := os.ReadFile(filepath.Join(dir, "cgroup.procs")); pErr == nil && strings.TrimSpace(string(procs)) != "" {
				return errors.New("parent cgroup has active subtree_control with internal processes")
			}
		}
	}
	return nil
}

func tryStartCommandCgroupV2(cmd *exec.Cmd, opts commandStartOptions, strict bool) (commandContainmentMetadata, bool, error) {
	parentDir, err := resolveWritableCgroupV2Parent()
	if err != nil {
		if strict {
			return commandContainmentMetadata{}, false, fmt.Errorf("cgroup_v2 containment unavailable: %w", err)
		}
		return commandContainmentMetadata{}, false, nil
	}

	leafName := fmt.Sprintf("ntm-step-%d-%d", os.Getpid(), cgroupLeafSequence.Add(1))
	leafDir := filepath.Join(parentDir, leafName)
	if err := os.Mkdir(leafDir, 0o755); err != nil {
		if strict {
			return commandContainmentMetadata{}, false, fmt.Errorf("create cgroup_v2 leaf %q: %w", leafDir, err)
		}
		return commandContainmentMetadata{}, false, nil
	}
	if err := unix.Access(filepath.Join(leafDir, "cgroup.procs"), unix.W_OK); err != nil {
		_ = os.Remove(leafDir)
		if strict {
			return commandContainmentMetadata{}, false, fmt.Errorf("cgroup_v2 leaf %q is not writable: %w", leafDir, err)
		}
		return commandContainmentMetadata{}, false, nil
	}
	if cgType, err := os.ReadFile(filepath.Join(leafDir, "cgroup.type")); err == nil {
		if strings.Contains(strings.TrimSpace(string(cgType)), "invalid") {
			_ = os.Remove(leafDir)
			if strict {
				return commandContainmentMetadata{}, false, fmt.Errorf("cgroup_v2 leaf %q has invalid domain type", leafDir)
			}
			return commandContainmentMetadata{}, false, nil
		}
	}

	dirFD, err := unix.Open(leafDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(leafDir)
		if strict {
			return commandContainmentMetadata{}, false, fmt.Errorf("open cgroup_v2 leaf FD %q: %w", leafDir, err)
		}
		return commandContainmentMetadata{}, false, nil
	}

	applyShellExitReceiptIfApplicable(cmd, opts)
	origAttr := *cmd.SysProcAttr
	attrWithCgroup := origAttr
	attrWithCgroup.UseCgroupFD = true
	attrWithCgroup.CgroupFD = dirFD
	cmd.SysProcAttr = &attrWithCgroup

	startErr := cmd.Start()
	_ = unix.Close(dirFD)
	cmd.SysProcAttr.CgroupFD = 0
	if startErr != nil {
		cmd.SysProcAttr = &origAttr
		_ = os.Remove(leafDir)
		return commandContainmentMetadata{}, false, startErr
	}

	pidfd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		pidfd = -1
	}

	meta := commandContainmentMetadata{
		Tier:         ContainmentTierCgroupV2,
		LeaderPID:    cmd.Process.Pid,
		CgroupPath:   leafDir,
		ExitCodeFile: opts.ExitCodeFile,
	}
	if opts.RecordLaunchMetadata {
		if ticks, _, err := readProcessStartTimeTicks(cmd.Process.Pid); err == nil {
			meta.LeaderStartTimeTicks = ticks
		}
	}
	commandContainmentMap.Store(cmd, &linuxContainmentHandle{
		tier:    ContainmentTierCgroupV2,
		leafDir: leafDir,
		pidfd:   pidfd,
	})
	return meta, true, nil
}

func isGoTestBinary(exe string) bool {
	base := filepath.Base(exe)
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func resolveStepReaperBinary(strict bool) (string, bool) {
	if override := strings.TrimSpace(os.Getenv(StepReaperBinEnv)); override != "" {
		return override, true
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "", false
	}
	if isGoTestBinary(exe) && !strict && os.Getenv(StepReaperTestAllowEnv) != "1" {
		return "", false
	}
	return exe, true
}

func tryStartCommandSubreaperPIDFD(cmd *exec.Cmd, opts commandStartOptions, strict bool) (commandContainmentMetadata, bool, error) {
	reaperBin, ok := resolveStepReaperBinary(strict)
	if !ok {
		if strict {
			return commandContainmentMetadata{}, false, errors.New("subreaper_pidfd executable unavailable")
		}
		return commandContainmentMetadata{}, false, nil
	}

	targetPath := cmd.Path
	targetArgs := append([]string(nil), cmd.Args...)
	if len(targetArgs) == 0 {
		targetArgs = []string{targetPath}
	}

	reaperArgs := []string{reaperBin, StepReaperSubcommand}
	if opts.ExitCodeFile != "" {
		reaperArgs = append(reaperArgs, "--exit-code-file="+opts.ExitCodeFile)
	}
	if opts.StepID != "" {
		reaperArgs = append(reaperArgs, "--step-id="+opts.StepID)
	}
	if opts.StepHash != "" {
		reaperArgs = append(reaperArgs, "--step-hash="+opts.StepHash)
	}
	if targetArgs[0] != targetPath {
		reaperArgs = append(reaperArgs, "--argv0="+targetArgs[0])
	}
	reaperArgs = append(reaperArgs, "--", targetPath)
	if len(targetArgs) > 1 {
		reaperArgs = append(reaperArgs, targetArgs[1:]...)
	}

	cmd.Path = reaperBin
	cmd.Args = reaperArgs
	if err := cmd.Start(); err != nil {
		cmd.Path = targetPath
		cmd.Args = targetArgs
		return commandContainmentMetadata{}, false, err
	}

	pidfd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		pidfd = -1
	}

	meta := commandContainmentMetadata{
		Tier:         ContainmentTierSubreaperPIDFD,
		LeaderPID:    cmd.Process.Pid,
		ExitCodeFile: opts.ExitCodeFile,
	}
	if opts.RecordLaunchMetadata {
		if ticks, _, err := readProcessStartTimeTicks(cmd.Process.Pid); err == nil {
			meta.LeaderStartTimeTicks = ticks
		}
	}
	commandContainmentMap.Store(cmd, &linuxContainmentHandle{
		tier:        ContainmentTierSubreaperPIDFD,
		pidfd:       pidfd,
		reaperChild: true,
	})
	return meta, true, nil
}

func takeCommandContainmentHandle(cmd *exec.Cmd) (*linuxContainmentHandle, bool) {
	raw, ok := commandContainmentMap.LoadAndDelete(cmd)
	if !ok {
		return nil, false
	}
	handle, ok := raw.(*linuxContainmentHandle)
	return handle, ok && handle != nil
}

func waitCommandWithContainmentHandle(ctx context.Context, cmd *exec.Cmd, handle *linuxContainmentHandle) commandCleanupResult {
	switch handle.tier {
	case ContainmentTierCgroupV2:
		return waitCommandWithCgroupV2(ctx, cmd, handle)
	case ContainmentTierSubreaperPIDFD:
		return waitCommandWithSubreaperPIDFD(ctx, cmd, handle)
	default:
		if handle.pidfd >= 0 {
			_ = unix.Close(handle.pidfd)
		}
		return waitCommandWithProcPGID(ctx, cmd)
	}
}

func closeContainmentPIDFD(handle *linuxContainmentHandle) {
	if handle != nil && handle.pidfd >= 0 {
		_ = unix.Close(handle.pidfd)
		handle.pidfd = -1
	}
}

func leaderExitedFast(pidfd int, pid int) bool {
	if pidfd >= 0 {
		pfds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfds, 0)
		if err == nil {
			return n > 0 && pfds[0].Revents&unix.POLLIN != 0
		}
	}
	return observeCommandExit(pid, syscall.WNOHANG) == nil
}

func reapContainedLeader(cmd *exec.Cmd, leaderExitedBeforeSignal bool, result commandCleanupResult) commandCleanupResult {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitBudget := commandPipeDrainTimeout + cmd.WaitDelay
	timer := time.NewTimer(waitBudget)
	defer timer.Stop()
	select {
	case err := <-done:
		result.Err = errors.Join(result.Err, err)
		if exitErr, ok := err.(*exec.ExitError); ok && (leaderExitedBeforeSignal || result.leaderExitedBeforeSignal) {
			result.ExitBeforeSignalErr = exitErr
		}
		if cmd.ProcessState == nil {
			result.Settled = false
		}
	case <-timer.C:
		result.Err = errors.Join(result.Err, errors.New("command leader did not finish waiting after cleanup; execution remains unresolved"))
		result.Settled = false
	}
	return result
}

func waitCommandWithCgroupV2(ctx context.Context, cmd *exec.Cmd, handle *linuxContainmentHandle) (result commandCleanupResult) {
	defer func() { verifyCommandOutputSettlement(cmd, &result) }()
	defer closeContainmentPIDFD(handle)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}

	leaderExitedBeforeSignal := leaderExitedFast(handle.pidfd, cmd.Process.Pid)
	waitStart := time.Now()
	waitErr := waitCgroupPopulatedZero(ctx, handle.leafDir)
	if waitErr == nil && ctx.Err() == nil {
		settleLatencyUS := time.Since(waitStart).Microseconds()
		result = reapContainedLeader(cmd, true, commandCleanupResult{Settled: true})
		removeCgroupLeaf(handle.leafDir)
		slog.Debug("command containment settled",
			"containment_tier", ContainmentTierCgroupV2,
			"settlement_latency_us", settleLatencyUS,
			"reaped_descendants", 0,
		)
		return result
	}

	settleStart := time.Now()
	if ctx.Err() == nil {
		// Inotify/cgroup observation failed unexpectedly without caller cancellation.
		closeContainmentPIDFD(handle)
		return reapContainedLeader(cmd, leaderExitedBeforeSignal, cleanupCommandAfterObservationFailure(ctx, cmd, waitErr))
	}

	leaderExitedBeforeSignal = leaderExitedBeforeSignal || leaderExitedFast(handle.pidfd, cmd.Process.Pid)
	result = commandCleanupResult{
		Cancelled:                true,
		SignalSent:               "SIGTERM",
		leaderExitedBeforeSignal: leaderExitedBeforeSignal,
	}

	termSignaled, termErr := signalCgroupAndProcessGroup(cmd, handle.leafDir, syscall.SIGTERM)
	if termErr != nil && !errors.Is(termErr, os.ErrProcessDone) {
		result.Err = errors.Join(result.Err, fmt.Errorf("terminate cgroup command group: %w", termErr))
	}

	graceCtx, graceCancel := context.WithTimeout(context.WithoutCancel(ctx), commandCancelGracePeriod)
	graceErr := waitCgroupPopulatedZero(graceCtx, handle.leafDir)
	graceCancel()
	if graceErr != nil && !errors.Is(graceErr, context.DeadlineExceeded) {
		result.Err = errors.Join(result.Err, fmt.Errorf("observe cgroup during SIGTERM grace period: %w", graceErr))
	}

	result.SignalSent = "SIGTERM,SIGKILL"
	killSignaled, killErr := forceKillCgroupLeaf(cmd, handle.leafDir)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}

	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
	quietErr := waitCgroupPopulatedZero(settleCtx, handle.leafDir)
	settleCancel()

	if killErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("kill cgroup command group: %w", killErr))
	}
	if quietErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("cgroup command group did not settle: %w", quietErr))
	}
	result.Err = errors.Join(result.Err, ctx.Err())
	result.Settled = killErr == nil && quietErr == nil

	result = reapContainedLeader(cmd, leaderExitedBeforeSignal, result)
	if result.Settled {
		removeCgroupLeaf(handle.leafDir)
	}
	slog.Debug("command containment settled",
		"containment_tier", ContainmentTierCgroupV2,
		"settlement_latency_us", time.Since(settleStart).Microseconds(),
		"reaped_descendants", termSignaled+killSignaled,
	)
	return result
}

// waitCgroupPopulatedZero watches `<leafDir>/cgroup.events` with inotify
// (IN_MODIFY) and wakes immediately via poll(2) + eventfd(2) on either
// `populated 0` or ctx.Done(), performing zero timer polling and zero
// /proc/<pid>/stat opens.
func waitCgroupPopulatedZero(ctx context.Context, leafDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	eventsPath := filepath.Join(leafDir, "cgroup.events")
	ifd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("init inotify for cgroup.events: %w", err)
	}
	defer unix.Close(ifd)

	if _, err := unix.InotifyAddWatch(ifd, eventsPath, unix.IN_MODIFY); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return fmt.Errorf("watch %q: %w", eventsPath, err)
	}

	wfd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return fmt.Errorf("init wakeup eventfd: %w", err)
	}
	stopWake := make(chan struct{})
	wakeDone := make(chan struct{})
	if ctx.Done() != nil {
		go func() {
			defer close(wakeDone)
			select {
			case <-ctx.Done():
				var one [8]byte
				one[0] = 1
				_, _ = unix.Write(wfd, one[:])
			case <-stopWake:
			}
		}()
	} else {
		close(wakeDone)
	}
	defer func() {
		close(stopWake)
		<-wakeDone
		_ = unix.Close(wfd)
	}()

	// Re-check cgroup.events immediately after registering the inotify watch
	// so a fast-exiting child/grandchild never misses a transition edge.
	populated, err := readCgroupPopulatedFile(eventsPath)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !populated {
		return nil
	}

	pfds := []unix.PollFd{
		{Fd: int32(ifd), Events: unix.POLLIN},
		{Fd: int32(wfd), Events: unix.POLLIN},
	}
	var scratch [256]byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, pollErr := unix.Poll(pfds, -1)
		if pollErr != nil {
			if errors.Is(pollErr, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("poll cgroup.events inotify: %w", pollErr)
		}
		if pfds[0].Revents&unix.POLLIN != 0 {
			for {
				if _, rErr := unix.Read(ifd, scratch[:]); rErr != nil {
					break
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		populated, err = readCgroupPopulatedFile(eventsPath)
		if err != nil {
			return err
		}
		if !populated {
			return nil
		}
	}
}

func readCgroupPopulatedFile(eventsPath string) (bool, error) {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, err
	}
	return parseCgroupPopulated(data)
}

func readCgroupProcs(leafDir string) ([]int, error) {
	data, err := os.ReadFile(filepath.Join(leafDir, "cgroup.procs"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 1 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func signalPIDViaPIDFD(pid int, sig unix.Signal) error {
	pfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if errors.Is(err, syscall.ENOSYS) {
			kErr := unix.Kill(pid, sig)
			if errors.Is(kErr, syscall.ESRCH) {
				return nil
			}
			return kErr
		}
		return err
	}
	defer unix.Close(pfd)
	sErr := unix.PidfdSendSignal(pfd, sig, nil, 0)
	if errors.Is(sErr, syscall.ESRCH) {
		return nil
	}
	return sErr
}

// signalCgroupAndProcessGroup sends sig to the command's process group first,
// then signals any escaped processes (e.g. setsid/setpgid) recorded in
// `<leafDir>/cgroup.procs` without double-signaling existing PGID members.
func signalCgroupAndProcessGroup(cmd *exec.Cmd, leafDir string, sig syscall.Signal) (int, error) {
	pgid := cmd.Process.Pid
	groupErr := signalCommandProcessGroup(cmd, sig)
	if errors.Is(groupErr, os.ErrProcessDone) {
		groupErr = nil
	}

	pids, readErr := readCgroupProcs(leafDir)
	if readErr != nil {
		return 0, errors.Join(groupErr, readErr)
	}
	escapedSignaled := 0
	var sigErrs error
	for _, pid := range pids {
		if groupErr == nil {
			if memberPGID, err := syscall.Getpgid(pid); err == nil && memberPGID == pgid {
				continue
			}
		}
		if err := signalPIDViaPIDFD(pid, unix.Signal(sig)); err != nil {
			sigErrs = errors.Join(sigErrs, err)
		} else {
			escapedSignaled++
		}
	}
	return escapedSignaled, errors.Join(groupErr, sigErrs)
}

func forceKillCgroupLeaf(cmd *exec.Cmd, leafDir string) (int, error) {
	killPath := filepath.Join(leafDir, "cgroup.kill")
	if err := os.WriteFile(killPath, []byte("1"), 0o200); err == nil {
		groupErr := cancelCommandProcessGroup(cmd)
		if errors.Is(groupErr, os.ErrProcessDone) {
			groupErr = nil
		}
		return 0, groupErr
	} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		// Pre-5.14 fallback: iterate cgroup.procs with pidfd SIGKILL until empty.
		totalKilled := 0
		var lastErr error
		for pass := 0; pass < 4; pass++ {
			pids, rErr := readCgroupProcs(leafDir)
			if rErr != nil {
				lastErr = rErr
				break
			}
			if len(pids) == 0 {
				break
			}
			for _, pid := range pids {
				if kErr := signalPIDViaPIDFD(pid, unix.SIGKILL); kErr != nil {
					lastErr = errors.Join(lastErr, kErr)
				} else {
					totalKilled++
				}
			}
		}
		groupErr := cancelCommandProcessGroup(cmd)
		if errors.Is(groupErr, os.ErrProcessDone) {
			groupErr = nil
		}
		return totalKilled, errors.Join(groupErr, lastErr)
	} else {
		return 0, err
	}
}

func removeCgroupLeaf(leafDir string) {
	if leafDir == "" {
		return
	}
	for attempt := 0; attempt < 25; attempt++ {
		err := os.Remove(leafDir)
		if err == nil || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return
		}
		if !errors.Is(err, syscall.EBUSY) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func waitPIDFDOrLeaderExit(ctx context.Context, pidfd int, process *os.Process) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pidfd < 0 {
		return waitCommandLeaderExit(ctx, process)
	}

	wfd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return waitCommandLeaderExit(ctx, process)
	}
	stopWake := make(chan struct{})
	wakeDone := make(chan struct{})
	if ctx.Done() != nil {
		go func() {
			defer close(wakeDone)
			select {
			case <-ctx.Done():
				var one [8]byte
				one[0] = 1
				_, _ = unix.Write(wfd, one[:])
			case <-stopWake:
			}
		}()
	} else {
		close(wakeDone)
	}
	defer func() {
		close(stopWake)
		<-wakeDone
		_ = unix.Close(wfd)
	}()

	pfds := []unix.PollFd{
		{Fd: int32(pidfd), Events: unix.POLLIN},
		{Fd: int32(wfd), Events: unix.POLLIN},
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, pollErr := unix.Poll(pfds, -1)
		if pollErr != nil {
			if errors.Is(pollErr, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("poll subreaper pidfd: %w", pollErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if pfds[0].Revents&unix.POLLIN != 0 {
			return nil
		}
	}
}

func waitCommandWithSubreaperPIDFD(ctx context.Context, cmd *exec.Cmd, handle *linuxContainmentHandle) (result commandCleanupResult) {
	defer func() { verifyCommandOutputSettlement(cmd, &result) }()
	defer closeContainmentPIDFD(handle)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}

	leaderExitedBeforeSignal := leaderExitedFast(handle.pidfd, cmd.Process.Pid)
	waitStart := time.Now()
	waitErr := waitPIDFDOrLeaderExit(ctx, handle.pidfd, cmd.Process)
	if waitErr == nil && ctx.Err() == nil {
		settleLatencyUS := time.Since(waitStart).Microseconds()
		result = reapContainedLeader(cmd, true, commandCleanupResult{Settled: true})
		slog.Debug("command containment settled",
			"containment_tier", ContainmentTierSubreaperPIDFD,
			"settlement_latency_us", settleLatencyUS,
			"reaped_descendants", 0,
		)
		return result
	}

	settleStart := time.Now()
	if ctx.Err() == nil {
		closeContainmentPIDFD(handle)
		return reapContainedLeader(cmd, leaderExitedBeforeSignal, cleanupCommandAfterObservationFailure(ctx, cmd, waitErr))
	}

	leaderExitedBeforeSignal = leaderExitedBeforeSignal || leaderExitedFast(handle.pidfd, cmd.Process.Pid)
	result = commandCleanupResult{
		Cancelled:                true,
		SignalSent:               "SIGTERM",
		leaderExitedBeforeSignal: leaderExitedBeforeSignal,
	}

	// Send SIGTERM to the subreaper so it forwards SIGTERM to its child group
	// AND any reparented setsid() grandchildren while staying alive until ECHILD.
	var termErr error
	if handle.pidfd >= 0 {
		termErr = unix.PidfdSendSignal(handle.pidfd, unix.SIGTERM, nil, 0)
		if errors.Is(termErr, syscall.ESRCH) {
			termErr = nil
		}
	} else {
		termErr = signalCommandProcessGroup(cmd, syscall.SIGTERM)
		if errors.Is(termErr, os.ErrProcessDone) {
			termErr = nil
		}
	}
	if termErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("terminate subreaper group: %w", termErr))
	}

	graceCtx, graceCancel := context.WithTimeout(context.WithoutCancel(ctx), commandCancelGracePeriod)
	graceErr := waitPIDFDOrLeaderExit(graceCtx, handle.pidfd, cmd.Process)
	graceCancel()
	if graceErr != nil && !errors.Is(graceErr, context.DeadlineExceeded) {
		result.Err = errors.Join(result.Err, fmt.Errorf("observe subreaper during SIGTERM grace period: %w", graceErr))
	}

	result.SignalSent = "SIGTERM,SIGKILL"
	var killErr error
	if !leaderExitedFast(handle.pidfd, cmd.Process.Pid) {
		// Instruct the subreaper via SIGUSR1 to SIGKILL its entire descendant
		// tree and reap until ECHILD BEFORE killing the subreaper itself, so
		// reparented setsid() grandchildren can never escape to PID 1.
		if handle.pidfd >= 0 {
			uErr := unix.PidfdSendSignal(handle.pidfd, unix.SIGUSR1, nil, 0)
			if errors.Is(uErr, syscall.ESRCH) {
				uErr = nil
			}
			killErr = uErr
		}
		drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
		_ = waitPIDFDOrLeaderExit(drainCtx, handle.pidfd, cmd.Process)
		drainCancel()

		if !leaderExitedFast(handle.pidfd, cmd.Process.Pid) {
			fErr := cancelCommandProcessGroup(cmd)
			if errors.Is(fErr, os.ErrProcessDone) {
				fErr = nil
			}
			killErr = errors.Join(killErr, fErr)
		}
	}

	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), commandPipeDrainTimeout)
	quietErr := waitPIDFDOrLeaderExit(settleCtx, handle.pidfd, cmd.Process)
	settleCancel()

	if killErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("kill subreaper command group: %w", killErr))
	}
	if quietErr != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("subreaper did not settle: %w", quietErr))
	}
	result.Err = errors.Join(result.Err, ctx.Err())
	result.Settled = killErr == nil && quietErr == nil

	result = reapContainedLeader(cmd, leaderExitedBeforeSignal, result)
	slog.Debug("command containment settled",
		"containment_tier", ContainmentTierSubreaperPIDFD,
		"settlement_latency_us", time.Since(settleStart).Microseconds(),
		"reaped_descendants", 0,
	)
	return result
}

type resilientStdoutTee struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	limit   int
	forward io.Writer
}

func (t *resilientStdoutTee) Write(p []byte) (int, error) {
	if t.forward != nil {
		_, _ = t.forward.Write(p)
	}
	t.mu.Lock()
	if remaining := t.limit - t.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			_, _ = t.buf.Write(p)
		} else {
			_, _ = t.buf.Write(p[:remaining])
		}
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *resilientStdoutTee) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// readSubreaperDirectChildren reads `/proc/self/task/*/children` to discover
// every process currently parented to this subreaper (including reparented
// double-forked / setsid() grandchildren) with zero `/proc/<pid>/stat` opens.
func readSubreaperDirectChildren() []int {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil
	}
	seen := make(map[int]struct{})
	var pids []int
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 1 || pid == os.Getpid() {
				continue
			}
			if _, dup := seen[pid]; !dup {
				seen[pid] = struct{}{}
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func signalSubreaperTree(directChildPID int, sig syscall.Signal) {
	if directChildPID > 1 {
		_ = unix.Kill(-directChildPID, sig)
		_ = signalPIDViaPIDFD(directChildPID, unix.Signal(sig))
	}
	for _, pid := range readSubreaperDirectChildren() {
		_ = unix.Kill(-pid, sig)
		_ = signalPIDViaPIDFD(pid, unix.Signal(sig))
	}
}

// RunStepReaper implements `ntm internal-step-reaper`:
//  1. Marks the current process as PR_SET_CHILD_SUBREAPER so every orphaned or
//     setsid() descendant reparents here instead of escaping to PID 1.
//  2. Spawns the target command in its own process group, forwarding SIGTERM /
//     SIGINT / SIGHUP and escalating to SIGKILL across all reparented children
//     upon SIGUSR1.
//  3. Waits for the direct child AND loops wait4(-1) until ECHILD so the
//     subreaper exits only after every descendant has terminated.
//  4. Optionally writes an atomic StepExitReceipt before returning the direct
//     child's exact exit status or fatal signal.
func RunStepReaper(args []string) StepReaperOutcome {
	var (
		exitCodeFile string
		stepID       string
		stepHash     string
		customArgv0  string
		target       []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			target = args[i+1:]
			break
		}
		switch {
		case strings.HasPrefix(arg, "--exit-code-file="):
			exitCodeFile = strings.TrimPrefix(arg, "--exit-code-file=")
		case strings.HasPrefix(arg, "--step-id="):
			stepID = strings.TrimPrefix(arg, "--step-id=")
		case strings.HasPrefix(arg, "--step-hash="):
			stepHash = strings.TrimPrefix(arg, "--step-hash=")
		case strings.HasPrefix(arg, "--argv0="):
			customArgv0 = strings.TrimPrefix(arg, "--argv0=")
		default:
			target = args[i:]
			i = len(args)
		}
	}
	if len(target) == 0 {
		return StepReaperOutcome{ExitCode: 2}
	}

	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: PR_SET_CHILD_SUBREAPER failed: %v\n", err)
		return StepReaperOutcome{ExitCode: 125}
	}

	signal.Ignore(syscall.SIGPIPE)
	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	selfStartTicks, _, _ := readProcessStartTimeTicks(os.Getpid())

	argv0 := target[0]
	if customArgv0 != "" {
		argv0 = customArgv0
	}

	child := &exec.Cmd{
		Path:   target[0],
		Args:   append([]string{argv0}, target[1:]...),
		Stdin:  os.Stdin,
		Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{
			Setpgid: true,
		},
	}
	if !strings.Contains(child.Path, "/") {
		if resolved, err := exec.LookPath(child.Path); err == nil {
			child.Path = resolved
		}
	}

	var (
		stdoutPipeR *os.File
		stdoutPipeW *os.File
		stdoutTee   *resilientStdoutTee
		stdoutDone  chan struct{}
	)
	if exitCodeFile != "" {
		pr, pw, err := os.Pipe()
		if err == nil {
			stdoutPipeR, stdoutPipeW = pr, pw
			stdoutTee = &resilientStdoutTee{
				limit:   stepReaperMaxCapturedStdout,
				forward: os.Stdout,
			}
			stdoutDone = make(chan struct{})
			child.Stdout = pw
			go func() {
				defer close(stdoutDone)
				_, _ = io.Copy(stdoutTee, pr)
				_ = pr.Close()
			}()
		} else {
			child.Stdout = os.Stdout
		}
	} else {
		child.Stdout = os.Stdout
	}

	if err := child.Start(); err != nil {
		if stdoutPipeW != nil {
			_ = stdoutPipeW.Close()
		}
		if stdoutPipeR != nil {
			_ = stdoutPipeR.Close()
		}
		_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: start %q: %v\n", target[0], err)
		return StepReaperOutcome{ExitCode: 127}
	}
	if stdoutPipeW != nil {
		_ = stdoutPipeW.Close()
	}

	directPID := child.Process.Pid
	_ = child.Process.Release()

	var (
		stateMu         sync.Mutex
		cancelRequested bool
		forceKillActive bool
		reapDone        = make(chan struct{})
	)

	go func() {
		for {
			select {
			case <-reapDone:
				return
			case rawSig := <-sigCh:
				sysSig, ok := rawSig.(syscall.Signal)
				if !ok {
					continue
				}
				stateMu.Lock()
				if sysSig == syscall.SIGUSR1 {
					forceKillActive = true
					signalSubreaperTree(directPID, syscall.SIGKILL)
				} else {
					cancelRequested = true
					if forceKillActive {
						signalSubreaperTree(directPID, syscall.SIGKILL)
					} else {
						signalSubreaperTree(directPID, sysSig)
					}
				}
				stateMu.Unlock()
			}
		}
	}()

	var (
		directObserved  bool
		directExitCode  int
		directSignal    syscall.Signal
		reapedProcesses int
	)

	for {
		var ws unix.WaitStatus
		wpid, err := unix.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.ECHILD) {
				break
			}
			break
		}
		if wpid <= 0 {
			continue
		}
		if wpid == directPID && !directObserved {
			directObserved = true
			if ws.Signaled() {
				directSignal = ws.Signal()
				directExitCode = 128 + int(directSignal)
			} else {
				directExitCode = ws.ExitStatus()
			}
		} else {
			reapedProcesses++
		}

		// Whenever a child exits while cancellation is active, any grandchild it
		// spawned (including setsid() escapees) has now been reparented to us.
		// Signal the newly reparented children immediately.
		stateMu.Lock()
		killing := forceKillActive
		cancelling := cancelRequested
		stateMu.Unlock()
		if killing {
			signalSubreaperTree(directPID, syscall.SIGKILL)
		} else if cancelling {
			signalSubreaperTree(directPID, syscall.SIGTERM)
		}
	}
	close(reapDone)

	if stdoutDone != nil {
		<-stdoutDone
	}

	if exitCodeFile != "" && directObserved && directSignal == 0 {
		captured := ""
		if stdoutTee != nil {
			captured = stdoutTee.String()
		}
		_ = writeStepExitReceiptAtomic(exitCodeFile, StepExitReceipt{
			Version:              stepExitReceiptVersion,
			StepID:               stepID,
			StepHash:             stepHash,
			LeaderPID:            os.Getpid(),
			LeaderStartTimeTicks: selfStartTicks,
			ExitCode:             directExitCode,
			Output:               captured,
			OutputCaptured:       stdoutTee != nil,
			FinishedAt:           time.Now().UTC(),
		})
	}

	slog.Debug("step subreaper finished",
		"containment_tier", ContainmentTierSubreaperPIDFD,
		"reaped_descendants", reapedProcesses,
		"exit_code", directExitCode,
	)
	if !directObserved {
		return StepReaperOutcome{ExitCode: 1}
	}
	return StepReaperOutcome{
		ExitCode: directExitCode,
		Signal:   directSignal,
	}
}

// parseProcStatStartTimeTicks extracts field #22 (starttime in clock ticks
// since boot) and field #3 (process state character) from a Linux
// /proc/<pid>/stat buffer, handling arbitrary comm names containing spaces or
// parentheses.
func parseProcStatStartTimeTicks(data []byte, expectedPID int) (uint64, string, error) {
	text := string(data)
	openIdx := strings.IndexByte(text, '(')
	closeIdx := strings.LastIndexByte(text, ')')
	if openIdx < 1 || closeIdx <= openIdx || closeIdx+1 >= len(text) {
		return 0, "", fmt.Errorf("inspect process %d: malformed /proc stat row", expectedPID)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(text[:openIdx]), "%d", &pid); err != nil || pid <= 0 || (expectedPID > 0 && pid != expectedPID) {
		return 0, "", fmt.Errorf("inspect process %d: mismatched PID in /proc stat", expectedPID)
	}
	fields := strings.Fields(text[closeIdx+1:])
	// After `(comm)`, fields[0] is field #3 (state) and fields[19] is field #22
	// (starttime). Require at least 20 fields after comm.
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, "", fmt.Errorf("inspect process %d: truncated /proc stat row", expectedPID)
	}
	var ticks uint64
	if _, err := fmt.Sscanf(fields[19], "%d", &ticks); err != nil || ticks == 0 {
		return 0, "", fmt.Errorf("inspect process %d: invalid starttime ticks %q", expectedPID, fields[19])
	}
	return ticks, fields[0], nil
}

func readProcessStartTimeTicks(pid int) (uint64, string, error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid PID %d", pid)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	return parseProcStatStartTimeTicks(data, pid)
}

func inspectLiveLeaderIdentity(pid int, expectedStartTicks uint64) (alive bool, pidReused bool, err error) {
	ticks, stateChar, err := readProcessStartTimeTicks(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return false, false, nil
		}
		return false, false, err
	}
	if ticks != expectedStartTicks {
		return false, true, nil
	}
	if stateChar == "Z" || stateChar == "X" {
		return false, false, nil
	}
	return true, false, nil
}

func inspectCgroupPathPopulated(cgroupPath string) (populated bool, exists bool, err error) {
	eventsPath := filepath.Join(cgroupPath, "cgroup.events")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
			return false, false, nil
		}
		return false, false, err
	}
	pop, parseErr := parseCgroupPopulated(data)
	if parseErr != nil {
		return false, true, parseErr
	}
	return pop, true, nil
}
