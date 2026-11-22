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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const stepReaperMaxCapturedStdout = 1 << 20 // 1 MiB bounded stdout mirror for crash-only receipts
const stepReaperSettlementMarker = "ntm-step-settled-v1"
const stepReaperMaxSettlementBytes = 64

var (
	cgroupLeafSequence    atomic.Uint64
	commandContainmentMap sync.Map // map[*exec.Cmd]*linuxContainmentHandle
)

type linuxContainmentHandle struct {
	tier        string
	leafDir     string
	pidfd       int
	reaperChild bool
	settlement  *os.File
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
		// Go retains special handlers even after signal.Reset (including
		// ignoring SIGPIPE). Exec resets caught dispositions to native defaults
		// while preserving this wrapper's PID, pidfd, and start identity.
		// Catch an inherited Ignore disposition first, then let a native shell
		// restore its own defaults and deliver the exact fatal signal.
		runtime.LockOSThread()
		signal.Notify(make(chan os.Signal, 1), outcome.Signal)
		if err := unix.PthreadSigmask(unix.SIG_SETMASK, &unix.Sigset_t{}, nil); err == nil {
			number := strconv.Itoa(int(outcome.Signal))
			exitCode := strconv.Itoa(128 + int(outcome.Signal))
			command := "ulimit -c 0 || exit " + exitCode + "; trap - " + number + "; kill -" + number + " \"$$\"; exit " + exitCode
			_ = syscall.Exec("/bin/sh", []string{"sh", "-c", command}, nil)
		}
		// If native mirroring fails, the parent rejects this ordinary exit as
		// inconsistent with the proof instead of silently changing the outcome.
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

	settlementRead, settlementWrite, err := os.Pipe()
	if err != nil {
		if strict {
			return commandContainmentMetadata{}, false, fmt.Errorf("create subreaper settlement pipe: %w", err)
		}
		return commandContainmentMetadata{}, false, nil
	}
	originalExtraFiles := cmd.ExtraFiles
	settlementFD := 3 + len(originalExtraFiles)
	cmd.ExtraFiles = append(append([]*os.File(nil), originalExtraFiles...), settlementWrite)

	targetPath := cmd.Path
	targetArgs := append([]string(nil), cmd.Args...)
	if len(targetArgs) == 0 {
		targetArgs = []string{targetPath}
	}

	reaperArgs := []string{reaperBin, StepReaperSubcommand, "--settlement-fd=" + strconv.Itoa(settlementFD)}
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
	startErr := cmd.Start()
	cmd.ExtraFiles = originalExtraFiles
	_ = settlementWrite.Close()
	if startErr != nil {
		_ = settlementRead.Close()
		cmd.Path = targetPath
		cmd.Args = targetArgs
		return commandContainmentMetadata{}, false, startErr
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
		settlement:  settlementRead,
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
	settleStart := time.Now()
	defer func() {
		verifySubreaperSettlement(cmd, handle, &result)
		verifyCommandOutputSettlement(cmd, &result)
		message := "command containment unresolved"
		if result.Settled {
			message = "command containment settled"
		}
		slog.Debug(message,
			"containment_tier", ContainmentTierSubreaperPIDFD,
			"settlement_latency_us", time.Since(settleStart).Microseconds(),
			"reaped_descendants", 0,
		)
		if handle.settlement != nil {
			_ = handle.settlement.Close()
		}
	}()
	defer closeContainmentPIDFD(handle)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}

	leaderExitedBeforeSignal := leaderExitedFast(handle.pidfd, cmd.Process.Pid)
	waitErr := waitPIDFDOrLeaderExit(ctx, handle.pidfd, cmd.Process)
	if waitErr == nil && ctx.Err() == nil {
		result = reapContainedLeader(cmd, true, commandCleanupResult{Settled: true})
		return result
	}

	settleStart = time.Now()
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
	return result
}

// A ready pidfd proves only that the wrapper exited. The private pipe proves
// that this particular wrapper reaped all descendants and finished its output
// copies before exiting, including when the payload's true outcome is a fatal
// signal. The payload never inherits the write descriptor. Missing, malformed,
// or unbounded evidence must not turn a killed wrapper into replay permission.
// The recorded outcome must also match the wrapper's actual wait status: a
// wrapper killed after publishing proof must not turn payload success into a
// failed command that can be retried automatically.
func verifySubreaperSettlement(cmd *exec.Cmd, handle *linuxContainmentHandle, result *commandCleanupResult) {
	if !result.Settled {
		return
	}
	reject := func(err error) {
		result.Settled = false
		result.Err = errors.Join(result.Err, errors.New("subreaper exited without verified descendant settlement; inspect surviving command work before replay"), err)
	}
	if handle.settlement == nil {
		reject(nil)
		return
	}
	if err := handle.settlement.SetReadDeadline(time.Now().Add(commandPipeDrainTimeout)); err != nil {
		reject(err)
		return
	}
	proof, err := io.ReadAll(io.LimitReader(handle.settlement, stepReaperMaxSettlementBytes+1))
	if err != nil {
		reject(err)
		return
	}
	var exitCode, outcomeSignal int
	_, err = fmt.Sscanf(string(proof), stepReaperSettlementMarker+" %d %d\n", &exitCode, &outcomeSignal)
	if err != nil || exitCode < 0 || exitCode > 255 || outcomeSignal < 0 ||
		(outcomeSignal != 0 && exitCode != 128+outcomeSignal) ||
		string(proof) != fmt.Sprintf("%s %d %d\n", stepReaperSettlementMarker, exitCode, outcomeSignal) {
		reject(errors.New("invalid subreaper settlement record"))
		return
	}
	if cmd.ProcessState == nil {
		reject(errors.New("subreaper terminal status unavailable"))
		return
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || (outcomeSignal == 0 && (!status.Exited() || status.ExitStatus() != exitCode)) ||
		(outcomeSignal != 0 && (!status.Signaled() || int(status.Signal()) != outcomeSignal)) {
		reject(fmt.Errorf("subreaper terminal status does not match completed payload outcome: status=%v exit_code=%d signal=%d", status, exitCode, outcomeSignal))
	}
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
// Kernels without task/children support use PPid from /proc/<pid>/status.
// The caller keeps reaping locked throughout discovery and signaling, so any
// selected child remains waitable and its PID cannot be reused in between.
func readSubreaperDirectChildren() []int {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return readSubreaperChildrenFromProcStatus("/proc", os.Getpid())
	}
	seen := make(map[int]struct{})
	var pids []int
	incomplete := len(tasks) == 0
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if err != nil {
			incomplete = true
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 1 || pid == os.Getpid() {
				incomplete = true
				continue
			}
			if _, dup := seen[pid]; !dup {
				seen[pid] = struct{}{}
				pids = append(pids, pid)
			}
		}
	}
	if incomplete {
		for _, pid := range readSubreaperChildrenFromProcStatus("/proc", os.Getpid()) {
			if _, ok := seen[pid]; !ok {
				seen[pid] = struct{}{}
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func readSubreaperChildrenFromProcStatus(procRoot string, parentPID int) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		slog.Debug("subreaper child observation failed", "source", "proc_status_fallback", "error", err)
		return nil
	}
	var pids []int
	statusReads := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == parentPID {
			continue
		}
		statusReads++
		data, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "status"))
		if err != nil {
			continue // Exited or inaccessible processes cannot prove ownership.
		}
		for _, line := range strings.Split(string(data), "\n") {
			value, ok := strings.CutPrefix(line, "PPid:")
			if !ok {
				continue
			}
			ppid, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && ppid == parentPID {
				pids = append(pids, pid)
			}
			break
		}
	}
	slog.Debug("subreaper child observation",
		"source", "proc_status_fallback",
		"status_files_read", statusReads,
		"children", len(pids),
	)
	return pids
}

// The caller holds the reaper's state lock, which also guards nonblocking
// wait4. Every enumerated PID remains our unreaped child throughout signaling;
// a saved direct-child PID must never be used after that child has been reaped.
func signalSubreaperTree(sig syscall.Signal) {
	for _, pid := range readSubreaperDirectChildren() {
		// procfs can describe an ancestor PID namespace. Only a native wait
		// can prove that this numeric PID belongs to our own unreaped child.
		// The shared reaping lock keeps that identity pinned until signaling.
		if !ownsUnreapedSubreaperChild(pid) {
			continue
		}
		_ = unix.Kill(-pid, sig)
		_ = signalPIDViaPIDFD(pid, unix.Signal(sig))
	}
}

func ownsUnreapedSubreaperChild(pid int) bool {
	if pid <= 1 {
		return false
	}
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		// A running owned child returns nil with empty siginfo. ECHILD or
		// another observation error must never authorize signaling its PID.
		return err == nil
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
func RunStepReaper(args []string) (outcome StepReaperOutcome) {
	var (
		exitCodeFile string
		stepID       string
		stepHash     string
		customArgv0  string
		settlementFD = -1
		target       []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			target = args[i+1:]
			break
		}
		switch {
		case strings.HasPrefix(arg, "--settlement-fd="):
			fd, err := strconv.Atoi(strings.TrimPrefix(arg, "--settlement-fd="))
			if err != nil || fd < 3 || settlementFD >= 0 {
				_, _ = fmt.Fprintln(os.Stderr, "ntm internal-step-reaper: invalid settlement descriptor")
				return StepReaperOutcome{ExitCode: 2}
			}
			settlementFD = fd
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
	var settlement *os.File
	settlementProven := false
	if settlementFD >= 3 {
		// ExtraFiles deliberately clears CLOEXEC when starting this wrapper.
		// Restore it BEFORE launching the payload so no child or grandchild
		// can publish a false marker or keep the proof pipe alive after us.
		unix.CloseOnExec(settlementFD)
		settlement = os.NewFile(uintptr(settlementFD), "ntm-step-settlement")
		if settlement == nil {
			return StepReaperOutcome{ExitCode: 125}
		}
		defer func() {
			if settlementProven {
				if _, err := fmt.Fprintf(settlement, "%s %d %d\n", stepReaperSettlementMarker, outcome.ExitCode, outcome.Signal); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: publish settlement: %v\n", err)
				}
			}
			_ = settlement.Close()
		}()
	}

	if len(target) == 0 {
		settlementProven = true // No payload has been started.
		return StepReaperOutcome{ExitCode: 2}
	}

	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: PR_SET_CHILD_SUBREAPER failed: %v\n", err)
		settlementProven = true // Containment setup failed before payload launch.
		return StepReaperOutcome{ExitCode: 125}
	}

	// Catch broken forwarding pipes without making the payload inherit an
	// ignored SIGPIPE disposition. Exec restores caught signals to default.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
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
		stdoutErr   error
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
				_, stdoutErr = io.Copy(stdoutTee, pr)
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
		if stdoutDone != nil {
			<-stdoutDone
		}
		_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: start %q: %v\n", target[0], err)
		settlementProven = true // exec.Start failed without a running payload.
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
		signalDone      = make(chan struct{})
	)

	go func() {
		defer close(signalDone)
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
					signalSubreaperTree(syscall.SIGKILL)
				} else {
					cancelRequested = true
					if forceKillActive {
						signalSubreaperTree(syscall.SIGKILL)
					} else {
						signalSubreaperTree(sysSig)
					}
				}
				stateMu.Unlock()
			}
		}
	}()

	var (
		directObserved    bool
		directExitCode    int
		directSignal      syscall.Signal
		reapedProcesses   int
		descendantsReaped bool
	)

	for {
		// Wait for an exited child without releasing its PID. Reaping and
		// signaling are then serialized under stateMu, while this blocking
		// observation leaves the cancellation goroutine free to send signals.
		var info unix.Siginfo
		if err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.ECHILD) {
				descendantsReaped = true
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: observe children: %v\n", err)
			}
			break
		}
		stateMu.Lock()
		var ws unix.WaitStatus
		wpid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if err != nil {
			stateMu.Unlock()
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: reap children: %v\n", err)
			break
		}
		if wpid <= 0 {
			stateMu.Unlock()
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
		killing := forceKillActive
		cancelling := cancelRequested
		if killing {
			signalSubreaperTree(syscall.SIGKILL)
		} else if cancelling {
			signalSubreaperTree(syscall.SIGTERM)
		}
		stateMu.Unlock()
	}
	close(reapDone)
	<-signalDone

	if !descendantsReaped || !directObserved {
		// An unexpected wait failure leaves descendant lifetime unknown.
		// Stop the output copy without waiting for those unknown writers,
		// and return without a receipt or settlement acknowledgement.
		if stdoutPipeR != nil {
			_ = stdoutPipeR.Close()
		}
		if stdoutDone != nil {
			<-stdoutDone
		}
		return StepReaperOutcome{ExitCode: 125}
	}

	if stdoutDone != nil {
		<-stdoutDone
		if stdoutErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: capture stdout: %v\n", stdoutErr)
			return StepReaperOutcome{ExitCode: 125}
		}
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

	settlementProven = true

	slog.Debug("step subreaper finished",
		"containment_tier", ContainmentTierSubreaperPIDFD,
		"reaped_descendants", reapedProcesses,
		"exit_code", directExitCode,
	)
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
