//go:build !linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StepReaperOutcome carries the terminal status of `ntm internal-step-reaper`.
type StepReaperOutcome struct {
	ExitCode int
}

// ExitFromStepReaper terminates the current process with the subreaper outcome.
func ExitFromStepReaper(outcome StepReaperOutcome) {
	os.Exit(outcome.ExitCode)
}

func previewContainmentTier(_ context.Context) string {
	forced := strings.TrimSpace(os.Getenv(ContainmentTierEnv))
	if validContainmentTier(forced) {
		return forced
	}
	return ContainmentTierProcPGID
}

func startCommandWithContainment(_ context.Context, cmd *exec.Cmd, opts commandStartOptions) (commandContainmentMetadata, error) {
	if cmd == nil {
		return commandContainmentMetadata{}, errors.New("cannot start nil command")
	}
	configureCommandProcessGroup(cmd)
	if opts.ExitCodeFile != "" && len(cmd.Args) == 3 && cmd.Args[0] == "/bin/sh" && cmd.Args[1] == "-c" {
		cmd.Args[2] = wrapShellCommandForExitReceipt(cmd.Args[2], opts.ExitCodeFile, opts.StepID, opts.StepHash)
	}
	if err := cmd.Start(); err != nil {
		return commandContainmentMetadata{}, err
	}
	return commandContainmentMetadata{
		Tier:         ContainmentTierProcPGID,
		LeaderPID:    cmd.Process.Pid,
		ExitCodeFile: opts.ExitCodeFile,
	}, nil
}

// RunStepReaper runs the target command directly on non-Linux platforms where
// PR_SET_CHILD_SUBREAPER is unavailable.
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
	argv0 := target[0]
	if customArgv0 != "" {
		argv0 = customArgv0
	}
	child := exec.Command(target[0], target[1:]...)
	child.Args[0] = argv0
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	err := child.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			_, _ = fmt.Fprintf(os.Stderr, "ntm internal-step-reaper: %v\n", err)
			return StepReaperOutcome{ExitCode: 127}
		}
	}
	if exitCodeFile != "" && exitCode >= 0 {
		_ = writeStepExitReceiptAtomic(exitCodeFile, StepExitReceipt{
			Version:   stepExitReceiptVersion,
			StepID:    stepID,
			StepHash:  stepHash,
			LeaderPID: os.Getpid(),
			ExitCode:  exitCode,
		})
	}
	return StepReaperOutcome{ExitCode: exitCode}
}

func inspectLiveLeaderIdentity(_ int, _ uint64) (alive bool, pidReused bool, err error) {
	return false, false, nil
}

func inspectCgroupPathPopulated(cgroupPath string) (populated bool, exists bool, err error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.events"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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
