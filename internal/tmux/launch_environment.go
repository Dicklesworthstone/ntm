package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxLaunchEnvironmentKeys  = 256
	maxLaunchEnvironmentValue = 64 * 1024
	maxLaunchEnvironmentBytes = 1 << 20
	maxRecoveryCommandBytes   = 1 << 20
	launchEnvironmentTimeout  = 10 * time.Second
)

// ValidateLaunchEnvironment checks explicit recovery inputs without exposing
// their values. The portable shell transport accepts single-line UTF-8 values,
// including an explicitly empty value. Shell interpreter controls are excluded:
// they could enable tracing or change how the private launch script is read.
func ValidateLaunchEnvironment(environment map[string]string) error {
	if len(environment) > maxLaunchEnvironmentKeys {
		return errors.New("recovery environment has too many keys")
	}
	total := 0
	for name, value := range environment {
		if !validLaunchEnvironmentName(name) {
			return errors.New("recovery environment has an invalid key")
		}
		if name == "BASH" || strings.HasPrefix(name, "BASH_") || strings.HasPrefix(name, "KSH_") || strings.HasPrefix(name, "ZSH_") {
			return fmt.Errorf("recovery environment key %s is reserved by the shell or pane transport", name)
		}
		switch name {
		case "ENV", "SHELLOPTS", "BASHOPTS", "PS0", "PS1", "PS2", "PS3", "PS4", "PROMPT_COMMAND", "IFS",
			"UID", "EUID", "PPID", "BASHPID", "GROUPS", "DIRSTACK", "FUNCNAME", "PIPESTATUS",
			"RANDOM", "SRANDOM", "SECONDS", "LINENO", "HISTCMD", "HISTSIZE", "HISTFILESIZE",
			"OPTIND", "OPTERR", "TMOUT", "MAILCHECK", "COLUMNS", "LINES", "SHLVL", "EPOCHSECONDS", "EPOCHREALTIME",
			"_", "TMUX", "TMUX_PANE":
			return fmt.Errorf("recovery environment key %s is reserved by the shell or pane transport", name)
		}
		if len(value) > maxLaunchEnvironmentValue {
			return fmt.Errorf("recovery environment value for %s exceeds the size limit", name)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("recovery environment value for %s is not valid UTF-8", name)
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("recovery environment value for %s contains control characters", name)
			}
		}
		total += len(name) + len(value) + 2
		if total > maxLaunchEnvironmentBytes {
			return errors.New("recovery environment exceeds the size limit")
		}
	}
	return nil
}

// LaunchEnvironment is a one-use, in-memory environment handoff to a local pane.
// The filesystem contains only an empty FIFO in a private temporary directory.
// Values never enter tmux commands, pane input, process arguments, or regular
// files. A caller must Close the handoff, including when only doing a dry run.
type LaunchEnvironment struct {
	mu          sync.Mutex
	directory   string
	pipe        string
	environment map[string]string
	writer      *os.File
	cancel      context.CancelFunc
	used        bool
	closed      bool
}

// String and GoString keep incidental diagnostic formatting from revealing the
// private values. All fields are unexported, so JSON encoding is also value-free.
func (*LaunchEnvironment) String() string   { return "LaunchEnvironment{redacted}" }
func (*LaunchEnvironment) GoString() string { return "LaunchEnvironment{redacted}" }

// NewLaunchEnvironment performs the filesystem and platform preflight before a
// pane is mutated. Remote panes require recovery on the remote host; a local
// FIFO must never be mistaken for a remote credential source.
func NewLaunchEnvironment(ctx context.Context, environment map[string]string) (*LaunchEnvironment, error) {
	if ctx == nil {
		return nil, errors.New("recovery environment requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateLaunchEnvironment(environment); err != nil {
		return nil, err
	}
	if DefaultClient.Remote != "" {
		return nil, errors.New("explicit environment recovery requires local tmux; run recovery on the remote host")
	}
	info, err := os.Stat("/bin/sh")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("explicit environment recovery requires an executable /bin/sh")
	}
	base, err := filepath.Abs(os.TempDir())
	if err != nil {
		return nil, errors.New("cannot resolve private recovery environment directory")
	}
	for _, r := range base {
		if unicode.IsControl(r) {
			return nil, errors.New("private recovery environment directory contains control characters")
		}
	}
	directory, err := os.MkdirTemp(base, "ntm-recovery-env-*")
	if err != nil {
		return nil, errors.New("cannot create private recovery environment directory")
	}
	launch := &LaunchEnvironment{
		directory: directory,
		pipe: filepath.Join(directory, "launch"),
		environment: make(map[string]string, len(environment)),
	}
	if err := createFIFO(launch.pipe); err != nil {
		_ = launch.Close()
		return nil, errors.New("cannot create private recovery environment pipe")
	}
	for name, value := range environment {
		launch.environment[name] = value
	}
	if err := ctx.Err(); err != nil {
		_ = launch.Close()
		return nil, err
	}
	return launch, nil
}

// Send launches a preflighted command with the explicit environment. Only the
// interpreter and FIFO pathname are typed into the pane. /bin/sh -p ignores
// inherited shell startup and tracing controls; the script also disables tracing
// before reading any value. A complete compound command must be read before any
// export or launch executes, so a truncated/canceled write cannot launch a
// partially constructed command. Reading the script by pathname preserves the
// agent's terminal stdin (feeding the script over stdin would detach its TUI).
func (launch *LaunchEnvironment) Send(ctx context.Context, target, command string, enter bool, agentType AgentType) error {
	if !enter {
		return errors.New("recovery environment handoff requires an immediate launch")
	}
	return launch.send(ctx, target, command, agentType, DefaultClient.SendKeysForAgentContext)
}

func (launch *LaunchEnvironment) send(ctx context.Context, target, command string, agentType AgentType, send func(context.Context, string, string, bool, AgentType) error) error {
	if launch == nil {
		return errors.New("recovery environment handoff is missing")
	}
	if err := validatePhysicalPaneTarget(ctx, target); err != nil {
		return err
	}
	if send == nil {
		return errors.New("recovery environment launch transport is missing")
	}
	if strings.TrimSpace(command) == "" || len(command) > maxRecoveryCommandBytes {
		return errors.New("recovery launch command is empty or exceeds the size limit")
	}
	if _, err := SanitizePaneCommand(command); err != nil {
		return errors.New("recovery launch command contains control characters")
	}
	launch.mu.Lock()
	if launch.closed || launch.used {
		launch.mu.Unlock()
		return errors.New("recovery environment handoff has already been used or closed")
	}
	launch.used = true
	ctx, cancel := context.WithTimeout(ctx, launchEnvironmentTimeout)
	launch.cancel = cancel
	var script strings.Builder
	script.WriteString("set +x +v\n{\n")
	names := make([]string, 0, len(launch.environment))
	for name := range launch.environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		script.WriteString("export ")
		script.WriteString(name)
		script.WriteByte('=')
		script.WriteString(ShellQuote(launch.environment[name]))
		// Some shells warn using the value when assigning locale or other
		// special variables. Never send assignment diagnostics to the pane.
		script.WriteString(" 2>/dev/null || exit 1\n")
	}
	script.WriteString(command)
	script.WriteString("\n}\n")
	pipe := launch.pipe
	launch.mu.Unlock()
	defer cancel()
	defer launch.Close()
	if err := send(ctx, target, "/bin/sh -p "+ShellQuote(pipe), true, agentType); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot deliver recovery environment launcher (%s)", ClassifyCommandError(err).Kind)
	}
	writer, err := openLaunchEnvironmentWriter(ctx, pipe)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot connect recovery environment pipe")
	}
	launch.mu.Lock()
	if launch.closed {
		launch.mu.Unlock()
		_ = writer.Close()
		return errors.New("recovery environment handoff was closed")
	}
	launch.writer = writer
	launch.mu.Unlock()
	stopClose := context.AfterFunc(ctx, func() { _ = writer.Close() })
	_, err = io.WriteString(writer, script.String())
	stopClose()
	_ = writer.Close()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot deliver complete recovery environment")
	}
	return ctx.Err()
}

// Close cancels an in-flight handoff, releases any blocked shell reader with
// EOF, and removes only the FIFO and its private directory. It is
// safe to call more than once or concurrently with Send.
func (launch *LaunchEnvironment) Close() error {
	if launch == nil {
		return nil
	}
	launch.mu.Lock()
	if launch.closed {
		launch.mu.Unlock()
		return nil
	}
	launch.closed = true
	launch.environment = nil
	cancel, writer := launch.cancel, launch.writer
	pipe, directory := launch.pipe, launch.directory
	launch.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if writer != nil {
		_ = writer.Close()
	}
	// Holding a nonblocking read/write descriptor while unlinking closes the
	// open/unlink race: existing readers receive EOF and later readers get
	// ENOENT instead of waiting forever for a writer that has gone away.
	abort, _ := openLaunchEnvironmentAbort(pipe)
	removeErr := os.Remove(pipe)
	if abort != nil {
		_ = abort.Close()
	}
	dirErr := os.Remove(directory)
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) || dirErr != nil && !errors.Is(dirErr, os.ErrNotExist) {
		return errors.New("cannot remove private recovery environment pipe")
	}
	return nil
}
