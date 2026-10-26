package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// ErrCircuitOpen is returned when the circuit breaker is open (tmux
// has failed too many times consecutively and we are in backoff).
var ErrCircuitOpen = errors.New("tmux circuit breaker open: too many consecutive failures, backing off")

// ErrPaneIdentityChanged means a pane no longer satisfies the process identity
// and ownership checked by a lifecycle operation. No guarded mutation ran.
var ErrPaneIdentityChanged = errors.New("pane process identity or ownership changed")

// CommandErrorKind is the stable category for a tmux command failure.
type CommandErrorKind string

const (
	CommandErrorNone              CommandErrorKind = ""
	CommandErrorTimeout           CommandErrorKind = "timeout"
	CommandErrorCanceled          CommandErrorKind = "canceled"
	CommandErrorCircuitOpen       CommandErrorKind = "circuit_open"
	CommandErrorBinaryUnavailable CommandErrorKind = "binary_unavailable"
	CommandErrorPermissionDenied  CommandErrorKind = "permission_denied"
	CommandErrorRemoteUnavailable CommandErrorKind = "remote_unavailable"
	CommandErrorSessionNotFound   CommandErrorKind = "session_not_found"
	CommandErrorPaneNotFound      CommandErrorKind = "pane_not_found"
	CommandErrorNoServer          CommandErrorKind = "no_server"
	CommandErrorMalformedOutput   CommandErrorKind = "malformed_output"
	CommandErrorCommandFailed     CommandErrorKind = "command_failed"
	CommandErrorUnknown           CommandErrorKind = "unknown"
)

// CommandErrorClass describes how callers should treat a tmux command error.
type CommandErrorClass struct {
	Kind           CommandErrorKind
	Infrastructure bool
	Retryable      bool
}

// CommandError is a failed tmux/ssh invocation that keeps the command's STDERR
// separate from its arguments.
//
// Classification used to substring-match the flattened error string, which
// included the argv — and for `send-keys` the argv contains the agent PROMPT.
// Agent prompts routinely discuss tooling failures, so a prompt containing
// "permission denied" made an unrelated non-zero exit classify as
// permission_denied/Infrastructure (charged to the circuit breaker, routed down
// the wrong retry path), and a prompt containing "no server running" turned a
// real infrastructure failure into a benign retryable no_server. Caller-supplied
// payload text must never be able to change how an error is classified, so the
// classifier reads Stderr and nothing else.
type CommandError struct {
	// Command is the binary that was executed (tmux or ssh).
	Command string
	// Args is the argv. It may contain caller payload and is for humans only.
	Args []string
	// Stderr is what the command actually reported. This is the only field
	// classification is allowed to read.
	Stderr string
	// Err is the underlying exec error (*exec.ExitError, *exec.Error, ...).
	Err error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", e.Command, strings.Join(e.Args, " "), e.Err, e.Stderr)
}

// Unwrap keeps errors.Is/As working against the underlying exec error, which
// ClassifyCommandError relies on for exit codes and binary-missing detection.
func (e *CommandError) Unwrap() error { return e.Err }

// Circuit breaker configuration.
const (
	// cbMaxFailures is the number of consecutive failures before the circuit
	// opens and starts rejecting calls immediately during the backoff window.
	cbMaxFailures = 5
	// cbBackoffDuration is how long the circuit stays open before allowing a
	// single probe call through (half-open state).
	cbBackoffDuration = 10 * time.Second
)

// Client handles tmux operations, optionally on a remote host.
// It includes a built-in circuit breaker that prevents hammering
// the tmux server when it is consistently failing.
type Client struct {
	Remote string // "user@host" or empty for local

	// captureBackpressure keeps the most recent capture attempt for each pane
	// so runtime overload snapshots are based on live tmux activity.
	captureBackpressure *captureBackpressureTracker

	// Circuit breaker state
	cbFailures  atomic.Int64 // consecutive failure count
	cbOpenUntil atomic.Int64 // unix-nano timestamp when circuit closes (0 = closed)
	cbProbing   atomic.Bool  // true when a half-open probe is in flight
}

// NewClient creates a new tmux client
func NewClient(remote string) *Client {
	return &Client{
		Remote:              remote,
		captureBackpressure: newCaptureBackpressureTracker(),
	}
}

// DefaultClient is the default local client
var DefaultClient = NewClient("")

// cbCheck returns ErrCircuitOpen if the circuit breaker is open and no
// probe should be attempted.  In half-open state it allows exactly one
// call through (the probe) and returns nil for that caller.
func (c *Client) cbCheck() error {
	openUntil := c.cbOpenUntil.Load()
	if openUntil == 0 {
		return nil // circuit closed
	}
	if time.Now().UnixNano() < openUntil {
		// Still in backoff window. Allow one probe through.
		if c.cbProbing.CompareAndSwap(false, true) {
			return nil // this caller is the half-open probe
		}
		return ErrCircuitOpen
	}

	// The backoff window retired. Transition to HALF-OPEN — admit exactly one
	// probe — rather than closing the circuit outright.
	//
	// Closing here meant the "one probe per window" property held only INSIDE
	// the window: against a still-broken tmux, every caller waiting at expiry
	// was admitted at once, so a 40-pane sweep failed fast for 10s and then
	// issued 40 execs in a burst before 5 failures re-opened the gate. Load
	// shedding was duty-cycled rather than effective.
	//
	// The CAS on the deadline is what picks the single winner. It also closes
	// the race that let a goroutine taking this branch Store(0) immediately
	// after another goroutine's cbRecordFailure had installed a fresh
	// deadline, silently disarming a just-opened breaker: the compare fails
	// there, so the stale observation cannot clobber the newer state.
	//
	// Only cbRecordSuccess closes the circuit now, which is what "the probe
	// proved tmux is healthy again" should mean.
	next := time.Now().Add(cbBackoffDuration).UnixNano()
	if c.cbOpenUntil.CompareAndSwap(openUntil, next) {
		c.cbProbing.Store(true)
		return nil // this caller is the half-open probe for the new window
	}

	// Another caller won the transition, so a new window is already open and
	// its probe is already designated. Reject rather than racing for the probe
	// slot: attempting the CAS here could land between the winner's successful
	// swap and its cbProbing.Store, admitting a SECOND probe for the same
	// window. The next call re-reads the state and takes the in-window path.
	return ErrCircuitOpen
}

// cbRecordSuccess resets the circuit breaker to a healthy state.
func (c *Client) cbRecordSuccess() {
	c.cbFailures.Store(0)
	c.cbOpenUntil.Store(0)
	c.cbProbing.Store(false)
}

// cbRecordFailure increments the consecutive failure count and opens
// the circuit once the threshold is reached.
func (c *Client) cbRecordFailure() {
	n := c.cbFailures.Add(1)
	if n >= int64(cbMaxFailures) {
		wasAlreadyOpen := c.cbOpenUntil.Load() != 0
		deadline := time.Now().Add(cbBackoffDuration).UnixNano()
		c.cbOpenUntil.Store(deadline)
		// Deliberately does NOT clear cbProbing. Clearing it here meant a
		// FAILED half-open probe immediately re-armed the gate, so the very
		// next caller won the CompareAndSwap in cbCheck and was admitted as a
		// fresh probe — and so on, forever. Against a fast-failing tmux
		// (missing binary, permission-denied socket) the breaker therefore
		// shed no load at all: a 40-pane status sweep issued 40 exec attempts
		// instead of failing fast after 5.
		//
		// The probe slot is re-granted only where a backoff window retires, by
		// the single caller that wins the deadline CAS in cbCheck, and cleared
		// outright on success (cbRecordSuccess). That is what "one probe per
		// window" requires.
		//
		// Log only on the transition from closed to open, not on
		// every subsequent failure or half-open probe failure.
		if !wasAlreadyOpen {
			slog.Warn("tmux circuit breaker opened",
				"consecutive_failures", n,
				"backoff", cbBackoffDuration.String())
		}
	}
}

var (
	tmuxBinaryOnce sync.Once
	tmuxBinaryPath string
)

// BinaryPath returns the resolved tmux binary path for local execution.
// An explicit override is evaluated on every call; only normal installation
// discovery is cached.
func BinaryPath() string {
	if override := strings.TrimSpace(os.Getenv("NTM_TMUX_BINARY")); override != "" {
		return override
	}
	tmuxBinaryOnce.Do(func() {
		tmuxBinaryPath = resolveInstalledTmuxBinaryPath()
	})
	if tmuxBinaryPath == "" {
		return "tmux"
	}
	return tmuxBinaryPath
}

func resolveInstalledTmuxBinaryPath() string {
	if path := findInstalledTmuxBinaryPath(); path != "" {
		return path
	}
	return "/usr/bin/tmux"
}

func findInstalledTmuxBinaryPath() string {
	if path := findStandardTmuxBinaryPath(); path != "" {
		return path
	}
	if path, err := exec.LookPath("tmux"); err == nil && path != "" {
		return path
	}
	return ""
}

func findStandardTmuxBinaryPath() string {
	candidates := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
		"/bin/tmux",
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, fmt.Sprintf("%s/.local/bin/tmux", home))
	}

	for _, path := range candidates {
		if binaryExists(path) {
			return path
		}
	}
	return ""
}

func binaryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DefaultCommandTimeout is the maximum time a tmux command may run before
// being killed.  This prevents indefinite hangs when the tmux server is
// overloaded (e.g. during parallel tests) or a pane/session is wedged.
const DefaultCommandTimeout = 30 * time.Second

// Run executes a tmux command with a default timeout.
func (c *Client) Run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
	defer cancel()
	return c.RunContext(ctx, args...)
}

// RunContext executes a tmux command with cancellation support.
// It checks the circuit breaker before executing and records the
// result (success or failure) to update circuit state.
func (c *Client) RunContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Circuit breaker: reject early if tmux has been consistently failing.
	if err := c.cbCheck(); err != nil {
		return "", err
	}

	var out string
	var err error
	if c.Remote == "" {
		out, err = runLocalContext(ctx, args...)
	} else {
		// Remote execution via ssh
		remoteCmd := buildRemoteShellCommand("tmux", args...)
		// Use "--" to prevent Remote from being parsed as an ssh option.
		out, err = runSSHContext(ctx, "--", c.Remote, remoteCmd)
	}

	if err != nil && ClassifyCommandError(err).Infrastructure {
		c.cbRecordFailure()
	} else {
		// Both success (err==nil) and application-level errors (tmux ran
		// but returned non-zero) prove tmux is responsive.  Reset the
		// consecutive infrastructure failure counter.
		c.cbRecordSuccess()
	}
	return out, err
}

// ClassifyCommandError returns the stable class for a tmux command failure.
// It keeps caller decisions about retry and circuit-breaker accounting aligned.
func ClassifyCommandError(err error) CommandErrorClass {
	if err == nil {
		return CommandErrorClass{Kind: CommandErrorNone}
	}

	// Match on STDERR alone when we have it. The flattened error string also
	// contains the argv, and for send-keys the argv is the agent's prompt —
	// letting caller payload steer classification. See CommandError.
	msg := strings.ToLower(err.Error())
	var cmdErr *CommandError
	if errors.As(err, &cmdErr) {
		msg = strings.ToLower(cmdErr.Stderr)
	}

	if errors.Is(err, ErrCircuitOpen) {
		return CommandErrorClass{Kind: CommandErrorCircuitOpen, Infrastructure: true, Retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.Canceled) {
			// Deliberately NOT Infrastructure, for the same reason as
			// "no server running" below: Infrastructure feeds only
			// circuit-breaker accounting, and a cancellation is CALLER-initiated
			// — an operator Ctrl+C, a per-tick context in a TUI poll loop, a
			// shutting-down request. It is not evidence that tmux is sick.
			// Counting it meant five cancelled calls opened the breaker for
			// every caller in the process. A timeout is different and stays
			// Infrastructure: that IS evidence of a wedged server.
			return CommandErrorClass{Kind: CommandErrorCanceled}
		}
		return CommandErrorClass{Kind: CommandErrorTimeout, Infrastructure: true, Retryable: true}
	}
	if strings.Contains(msg, "can't find pane") || strings.Contains(msg, "can't find window") {
		return CommandErrorClass{Kind: CommandErrorPaneNotFound}
	}
	if strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no such session") ||
		strings.Contains(msg, "session not found") {
		return CommandErrorClass{Kind: CommandErrorSessionNotFound}
	}
	if strings.Contains(msg, "permission denied") {
		return CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true}
	}
	if strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "error connecting to") ||
		strings.Contains(msg, "no sessions") {
		// Deliberately NOT Infrastructure. Infrastructure is consumed by
		// exactly one thing — circuit-breaker accounting — and "no server
		// running" is an instant, definitive answer meaning "there are no
		// tmux sessions", not evidence that tmux is sick. tmux returns it
		// without connecting to anything, so retrying costs nothing and there
		// is no load to shed. Counting it conflated "tmux is unhealthy" with
		// "tmux has nothing running": on a machine with no server, the sixth
		// consecutive session query stopped reporting SESSION_NOT_FOUND and
		// started reporting an INTERNAL_ERROR circuit-open instead — a
		// correct, actionable answer degraded into a confusing one.
		// Retryable stays true: a server may be started later.
		return CommandErrorClass{Kind: CommandErrorNoServer, Retryable: true}
	}
	if strings.Contains(msg, "unexpected session format") ||
		strings.Contains(msg, "malformed tmux output") ||
		strings.Contains(msg, "malformed output") {
		return CommandErrorClass{Kind: CommandErrorMalformedOutput, Infrastructure: true, Retryable: true}
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		switch {
		case errors.Is(execErr.Err, exec.ErrNotFound):
			return CommandErrorClass{Kind: CommandErrorBinaryUnavailable, Infrastructure: true}
		case errors.Is(execErr.Err, os.ErrPermission):
			return CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true}
		default:
			return CommandErrorClass{Kind: CommandErrorBinaryUnavailable, Infrastructure: true}
		}
	}

	if exitCode, ok := commandExitCode(err); ok {
		if exitCode == 255 {
			return CommandErrorClass{Kind: CommandErrorRemoteUnavailable, Infrastructure: true, Retryable: true}
		}
		return CommandErrorClass{Kind: CommandErrorCommandFailed}
	}

	return CommandErrorClass{Kind: CommandErrorUnknown, Infrastructure: true, Retryable: true}
}

// commandExitCode returns the process exit code carried by err, including when
// the exec.ExitError is wrapped with command and stderr context.
func commandExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	return exitErr.ExitCode(), true
}

// ShellQuote returns a POSIX-shell-safe single-quoted string.
//
// This is required for ssh remote commands because OpenSSH transmits a single
// command string to the remote shell (not an argv vector).
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}

	// Close-quote, escape single quote, reopen: ' -> '\''.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildRemoteShellCommand(command string, args ...string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, ShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func runLocalContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	binary := BinaryPath()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", &CommandError{
			Command: binary,
			Args:    append([]string(nil), args...),
			Stderr:  stderr.String(),
			Err:     err,
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runSSHContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Inject /bin/sh -c to ensure consistent shell behavior for the remote command.
	// The args passed here are already built by buildRemoteShellCommand, which
	// produces a single string like "tmux 'arg1' 'arg2'".
	// We want: ssh host /bin/sh -c "tmux 'arg1' 'arg2'"
	//
	// args[0] is flags like "-t"
	// args[1] is "--"
	// args[2] is remote host
	// args[3] is the command string

	if len(args) > 0 {
		commandIndex := len(args) - 1
		originalCommand := args[commandIndex]
		args[commandIndex] = fmt.Sprintf("/bin/sh -c %s", ShellQuote(originalCommand))
	}

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", &CommandError{
			Command: "ssh",
			Args:    append([]string(nil), args...),
			Stderr:  stderr.String(),
			Err:     err,
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunSilent executes a tmux command ignoring output
func (c *Client) RunSilent(args ...string) error {
	_, err := c.Run(args...)
	return err
}

// RunSilentContext executes a tmux command with cancellation support, ignoring stdout.
func (c *Client) RunSilentContext(ctx context.Context, args ...string) error {
	if guard := paneMutationGuardFromContext(ctx); guard != nil && isPaneInputCommand(args) {
		target := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--" {
				break
			}
			if args[i] == "-t" {
				if target != "" || i+1 >= len(args) {
					return fmt.Errorf("guarded pane input requires one exact target: %w", ErrPaneIdentityChanged)
				}
				i++
				target = args[i]
			}
		}
		if target != guard.target {
			return fmt.Errorf("guarded pane input cannot target another or implicit pane: %w", ErrPaneIdentityChanged)
		}
		return c.runPaneMutationIfCurrentContext(ctx, guard.target, guard.pid, paneMutationAgent, args...)
	}
	_, err := c.RunContext(ctx, args...)
	return err
}

type paneMutationGuardContextKey struct{}

type paneMutationGuard struct {
	target string
	pid    int
}

// WithPaneMutationGuard fences shared agent delivery helpers without changing
// their protocols. Composer cleanup, ordinary sends, and verification/rescue
// keys all retain the same pane process and live-agent requirement. The scope
// covers pane input only; buffer loading and read-only observation are unchanged.
func WithPaneMutationGuard(ctx context.Context, target string, expectedPID int) (context.Context, error) {
	if err := validatePhysicalPaneTarget(ctx, target); err != nil {
		return nil, err
	}
	if expectedPID <= 0 {
		return nil, errors.New("guarded pane input requires a positive expected PID")
	}
	if existing := paneMutationGuardFromContext(ctx); existing != nil && (existing.target != target || existing.pid != expectedPID) {
		return nil, errors.New("guarded pane input cannot replace an existing pane binding")
	}
	return context.WithValue(ctx, paneMutationGuardContextKey{}, &paneMutationGuard{target: target, pid: expectedPID}), nil
}

func paneMutationGuardFromContext(ctx context.Context) *paneMutationGuard {
	if ctx == nil {
		return nil
	}
	guard, _ := ctx.Value(paneMutationGuardContextKey{}).(*paneMutationGuard)
	return guard
}

func isPaneInputCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "send-keys" || args[0] == "paste-buffer")
}

// IsInstalled checks if tmux is available on the target host
func (c *Client) IsInstalled() bool {
	if c.Remote == "" {
		return binaryExists(BinaryPath())
	}
	// Check remote
	err := c.RunSilent("-V")
	return err == nil
}

// RespawnPane respawns a pane, optionally killing the current process (-k)
func (c *Client) RespawnPane(target string, kill bool) error {
	return c.RespawnPaneContext(context.Background(), target, kill)
}

// RespawnPaneContext respawns a pane with cancellation support
func (c *Client) RespawnPaneContext(ctx context.Context, target string, kill bool) error {
	args := []string{"respawn-pane", "-t", ExactTarget(target)}
	if kill {
		args = append(args, "-k")
	}
	return c.RunSilentContext(ctx, args...)
}

// PreflightRestartShellContext checks the fixed shell used for a two-stage
// agent restart. It performs no pane mutation and belongs in dry-run preflight
// as well as immediately before respawn.
func (c *Client) PreflightRestartShellContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("restart shell preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Remote != "" {
		out, err := c.RunContext(ctx, "if-shell", "/bin/sh -p -c 'test -f /bin/sh && test -x /bin/sh'",
			"display-message -p ntm_restart_shell_ready", "display-message -p ntm_restart_shell_unavailable")
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("cannot verify remote restart shell")
		}
		if out != "ntm_restart_shell_ready" {
			return errors.New("agent restart requires an executable /bin/sh")
		}
		return nil
	}
	info, err := os.Stat("/bin/sh")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("agent restart requires an executable /bin/sh")
	}
	return nil
}

// RespawnPaneIfCurrentContext replaces only the process that the caller
// preflighted. The comparison and respawn share tmux's command queue, so another
// client cannot replace the pane between a separate PID read and this mutation.
// Agent callers request freshShell to prevent tmux from rerunning a stored
// direct-provider command before the saved launch can be delivered. Other panes
// retain ordinary respawn behavior. An observed working directory is preserved.
func (c *Client) RespawnPaneIfCurrentContext(ctx context.Context, target string, expectedPID int, kill bool, workingDir string, freshShell bool) error {
	if err := validatePhysicalPaneTarget(ctx, target); err != nil {
		return err
	}
	if expectedPID <= 0 {
		return errors.New("guarded pane mutation requires a positive expected PID")
	}
	if workingDir != "" {
		if !filepath.IsAbs(workingDir) {
			return errors.New("guarded respawn requires an absolute working directory")
		}
		for _, r := range workingDir {
			if unicode.IsControl(r) {
				return errors.New("guarded respawn working directory contains control characters")
			}
		}
	}
	if freshShell {
		if err := c.PreflightRestartShellContext(ctx); err != nil {
			return err
		}
	}
	args := []string{"respawn-pane", "-t", ExactTarget(target)}
	if kill {
		args = append(args, "-k")
	}
	if workingDir != "" {
		args = append(args, "-c", workingDir)
	}
	if freshShell {
		// Multiple argv bypass tmux's stored-command and default-shell -c
		// paths. -p ignores inherited startup/tracing controls; -i keeps the
		// replacement shell interactive for the saved launch and later input.
		args = append(args, "/bin/sh", "-p", "-i")
	}
	return c.runPaneMutationIfCurrentContext(ctx, target, expectedPID, paneMutationAny, args...)
}

type paneMutationMode int

const (
	paneMutationAny paneMutationMode = iota
	paneMutationShell
	paneMutationAgent
)

// runPaneMutationIfCurrentContext evaluates its guard inside the tmux server,
// immediately before inserting the mutation in the same client command queue.
// It never uses a shell to interpret the command. The shell-only variant is for
// publishing and typing a launch into the replacement shell; a foreground agent
// that starts after the caller's observation must not receive those keystrokes.
func (c *Client) runPaneMutationIfCurrentContext(ctx context.Context, target string, expectedPID int, mode paneMutationMode, args ...string) error {
	if err := validatePhysicalPaneTarget(ctx, target); err != nil {
		return err
	}
	if expectedPID <= 0 {
		return errors.New("guarded pane mutation requires a positive expected PID")
	}
	if len(args) == 0 {
		return errors.New("guarded pane mutation requires a command")
	}
	if guard := paneMutationGuardFromContext(ctx); guard != nil && isPaneInputCommand(args) {
		if target != guard.target || expectedPID != guard.pid || mode == paneMutationShell {
			return fmt.Errorf("guarded pane input cannot override its agent binding: %w", ErrPaneIdentityChanged)
		}
		mode = paneMutationAgent
	}
	// Adoption deliberately outranks old service tags. Observe the recorded
	// type using the same parser as topology, then pin that exact option in
	// the queued predicate before allowing a retained service tag. This also
	// supports registered plugin types without duplicating their registry.
	recorded, err := c.RunContext(ctx, "display-message", "-p", "-t", target, "[#{"+PaneAgentTypeOption+"}]")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot observe guarded pane ownership")
	}
	if len(recorded) < 2 || len(recorded) > 258 || recorded[0] != '[' || recorded[len(recorded)-1] != ']' {
		return errors.New("guarded pane ownership metadata is malformed")
	}
	recorded = recorded[1 : len(recorded)-1]
	for _, r := range recorded {
		if unicode.IsControl(r) || strings.ContainsRune("#{},", r) {
			return errors.New("guarded pane ownership metadata cannot be compared safely")
		}
	}
	condition := fmt.Sprintf("#{==:#{pane_pid},%d}", expectedPID)
	condition = "#{&&:" + condition + ",#{==:#{" + PaneAgentTypeOption + "}," + recorded + "}}"
	options := []string{PaneOptionRotationToken}
	if ParsePaneAgentTypeOption(recorded) == AgentUnknown {
		options = append(options, PaneOptionNTMService, PaneOptionACFSService)
	}
	for _, option := range options {
		condition = "#{&&:" + condition + ",#{==:#{" + option + "},}}"
	}
	if mode != paneMutationAny {
		// Keep the permitted command names aligned with RunsBareShell. tmux
		// supplies the basename, even when the shell was invoked by full path.
		shells := make([]string, 0, len(bareShellCommands))
		for name := range bareShellCommands {
			shells = append(shells, name)
		}
		sort.Strings(shells)
		shellCondition := "0"
		for _, name := range shells {
			shellCondition = "#{||:" + shellCondition + ",#{==:#{pane_current_command}," + name + "}}"
		}
		if mode == paneMutationAgent {
			// A provider exiting to its parent shell does not change pane_pid.
			// Do not turn a delayed prompt or rescue Enter into shell input.
			shellCondition = "#{&&:#{!=:#{pane_current_command},},#{==:" + shellCondition + ",0}}"
		}
		condition = "#{&&:" + condition + ",#{&&:#{==:#{pane_dead},0}," + shellCondition + "}}"
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = ShellQuote(arg)
	}
	out, err := c.RunContext(ctx, "if-shell", "-F", "-t", target, condition,
		strings.Join(quoted, " "), "display-message -p ntm_pane_identity_changed")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// CommandError includes the entire conditional command in argv. Saved
		// launch text and metadata must not escape through diagnostics.
		return fmt.Errorf("cannot perform guarded pane mutation (%s)", ClassifyCommandError(err).Kind)
	}
	if out != "" {
		return fmt.Errorf("pane %s: %w", target, ErrPaneIdentityChanged)
	}
	return nil
}

// RespawnPaneContext respawns a pane with cancellation support (default client)
func RespawnPaneContext(ctx context.Context, target string, kill bool) error {
	return DefaultClient.RespawnPaneContext(ctx, target, kill)
}

// ApplyTiledLayout applies tiled layout to all windows
