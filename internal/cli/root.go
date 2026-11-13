package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

var (
	cfgFile string
	cfg     *config.Config
	sshHost string

	// Global JSON output flag - inherited by all subcommands
	jsonOutput bool

	// Global color control flag - inherited by all subcommands
	noColor bool

	// Global redaction flags - inherited by all subcommands
	redactMode  string // --redact=MODE override
	allowSecret bool   // --allow-secret override

	// Audit command tracking
	auditCorrelationID string
	auditCommandPath   string
	auditCommandStart  time.Time

	// Build information - set by goreleaser via ldflags
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "unknown"
)

var robotStateStore *state.Store
var robotProcessExit error
var jsonArgumentRoot *cobra.Command

func recordRobotProcessExit(err error) {
	if err != nil && robotProcessExit == nil {
		robotProcessExit = err
	}
}

func failRobotCommand(err error, code, hint, command string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = robot.ErrCodeTimeout
		hint = "Retry the command after cancellation or increase its timeout"
	}
	recordRobotProcessExit(robot.EncodeErrorJSON(err, code, hint, command))
}

func classifyRobotContextInjectError(err error) (code, hint string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout, "Retry the command after cancellation or increase its timeout"
	}
	var dispatchErr *dispatch.Error
	if errors.As(err, &dispatchErr) && dispatchErr.Code == dispatch.ErrRedactionBlocked {
		return "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact"
	}
	if errors.Is(err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
		return robot.ErrCodeNotImplemented, agent.GrokPromptDeliveryCapabilityHint
	}
	return robot.ErrCodeInternalError, "Check tmux session health"
}

func recordLegacyRobotExit(code int) {
	recordRobotProcessExit(robot.ExitResultForCode(code, nil, true))
}

func robotInvocationFromArgs(args []string) (bool, string) {
	fallback := ""
	for _, arg := range args {
		if arg == "--" {
			break
		}
		flag := strings.SplitN(arg, "=", 2)[0]
		if strings.HasPrefix(flag, "--robot-") {
			command := strings.TrimPrefix(flag, "--")
			if fallback == "" {
				fallback = command
			}
			if isRobotGlobalModifier(command) {
				continue
			}
			return true, command
		}
	}
	return fallback != "", fallback
}

func jsonInvocationFromArgs(args []string) bool {
	jsonRequested := false
	formatJSONRequested := hasDefaultJSONOutputCommand(args)
	outputJSONRequested := false
	formatIsOutput := hasJSONOutputFormatCommand(args)
	shortFormatIsOutput := hasJSONShortOutputFormatCommand(args)
	outputIsFormat := hasJSONOutputValueCommand(args)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		normalized := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case normalized == "--json":
			jsonRequested = true
		case strings.HasPrefix(normalized, "--json="):
			value, err := strconv.ParseBool(strings.TrimSpace(strings.TrimPrefix(normalized, "--json=")))
			if err != nil {
				// Preserve explicit machine intent so Cobra parse failures are
				// returned as one JSON error document rather than human stderr.
				jsonRequested = true
				continue
			}
			jsonRequested = value
		case normalized == "--format":
			if formatIsOutput && i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				formatJSONRequested = strings.EqualFold(strings.TrimSpace(args[i+1]), "json")
				i++
			}
		case normalized == "-f":
			if shortFormatIsOutput && i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				formatJSONRequested = strings.EqualFold(strings.TrimSpace(args[i+1]), "json")
				i++
			}
		case normalized == "--output" || normalized == "-o":
			if outputIsFormat && i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				outputJSONRequested = strings.EqualFold(strings.TrimSpace(args[i+1]), "json")
				i++
			}
		default:
			if formatIsOutput && strings.HasPrefix(normalized, "--format=") {
				formatJSONRequested = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(normalized, "--format=")), "json")
			} else if shortFormatIsOutput && strings.HasPrefix(normalized, "-f") && !strings.HasPrefix(normalized, "--") {
				value := strings.TrimPrefix(normalized, "-f")
				value = strings.TrimPrefix(value, "=")
				if value != "" {
					formatJSONRequested = strings.EqualFold(strings.TrimSpace(value), "json")
				}
			} else if outputIsFormat && strings.HasPrefix(normalized, "--output=") {
				outputJSONRequested = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(normalized, "--output=")), "json")
			} else if outputIsFormat && strings.HasPrefix(normalized, "-o") && !strings.HasPrefix(normalized, "--") {
				value := strings.TrimPrefix(normalized, "-o")
				value = strings.TrimPrefix(value, "=")
				if value != "" {
					outputJSONRequested = strings.EqualFold(strings.TrimSpace(value), "json")
				}
			}
		}
	}
	return jsonRequested || formatJSONRequested || outputJSONRequested
}

var jsonOutputFormatCommandPaths = [][]string{
	{"analytics"},
	{"audit", "export"},
	{"ensemble", "cache", "clear"},
	{"ensemble", "cache", "stats"},
	{"ensemble", "clean-checkpoints"},
	{"ensemble", "compare"},
	{"ensemble", "estimate"},
	{"ensemble", "export-findings"},
	{"ensemble", "list"},
	{"ensemble", "presets"},
	{"ensemble", "provenance"},
	{"ensemble", "rerun-mode"},
	{"ensemble", "resume"},
	{"ensemble", "status"},
	{"ensemble", "stop"},
	{"ensemble", "suggest"},
	{"ensemble", "synthesize"},
	{"handoff"},
	{"metrics", "export"},
	{"modes", "explain"},
	{"modes", "list"},
	{"rebalance"},
	{"review-queue"},
	{"scrub"},
	{"summary"},
	{"work", "commit-ready"},
	{"work", "commit-readiness"},
	{"work", "graph"},
	{"work", "queue-dry"},
	{"work", "triage"},
}

var defaultJSONOutputCommandPaths = [][]string{
	{"audit", "export"},
	{"metrics", "export"},
	{"work", "graph"},
}

var jsonOutputValueCommandPaths = [][]string{
	{"worktree", "list"},
}

var jsonShortOutputFormatCommandPaths = [][]string{
	{"ensemble", "cache", "clear"},
	{"ensemble", "cache", "stats"},
	{"ensemble", "clean-checkpoints"},
	{"ensemble", "compare"},
	{"ensemble", "estimate"},
	{"ensemble", "export-findings"},
	{"ensemble", "list"},
	{"ensemble", "presets"},
	{"ensemble", "provenance"},
	{"ensemble", "rerun-mode"},
	{"ensemble", "resume"},
	{"ensemble", "status"},
	{"ensemble", "stop"},
	{"ensemble", "suggest"},
	{"ensemble", "synthesize"},
	{"metrics", "export"},
	{"modes", "explain"},
	{"modes", "list"},
}

func hasJSONOutputFormatCommand(args []string) bool {
	for _, commandPath := range jsonOutputFormatCommandPaths {
		if argsMatchCommandPath(args, commandPath) {
			return true
		}
	}
	return false
}

func hasJSONShortOutputFormatCommand(args []string) bool {
	for _, commandPath := range jsonShortOutputFormatCommandPaths {
		if argsMatchCommandPath(args, commandPath) {
			return true
		}
	}
	return false
}

func hasDefaultJSONOutputCommand(args []string) bool {
	for _, commandPath := range defaultJSONOutputCommandPaths {
		if argsMatchCommandPath(args, commandPath) {
			return true
		}
	}
	return false
}

func hasJSONOutputValueCommand(args []string) bool {
	for _, commandPath := range jsonOutputValueCommandPaths {
		if argsMatchCommandPath(args, commandPath) {
			return true
		}
	}
	return false
}

func argsMatchCommandPath(args, commandPath []string) bool {
	_, ok := argsMatchCommandPathEnd(args, commandPath)
	return ok
}

func argsMatchCommandPathEnd(args, commandPath []string) (int, bool) {
	pathIndex := 0
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && argumentFlagConsumesValue(arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		if pathIndex >= len(commandPath) || arg != commandPath[pathIndex] {
			return -1, false
		}
		pathIndex++
		if pathIndex == len(commandPath) {
			return i, true
		}
	}
	return -1, false
}

func argumentFlagConsumesValue(arg string) bool {
	token := strings.SplitN(strings.TrimSpace(arg), "=", 2)[0]
	name := strings.TrimLeft(token, "-")
	shorthand := strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") && len(name) == 1
	if consumes, found := commandTreeFlagConsumesValue(jsonArgumentRoot, name, shorthand); found {
		return consumes
	}

	// Command-local format/output flags can occur between path components and
	// are not present in the root flag set used by the pre-execution detector.
	switch name {
	case "config", "f", "format", "o", "output", "redact", "ssh":
		return true
	default:
		return false
	}
}

func commandTreeFlagConsumesValue(cmd *cobra.Command, name string, shorthand bool) (consumes, found bool) {
	if cmd == nil {
		return false, false
	}
	for _, flags := range []*pflag.FlagSet{cmd.LocalNonPersistentFlags(), cmd.PersistentFlags()} {
		flag := flags.Lookup(name)
		if shorthand {
			flag = flags.ShorthandLookup(name)
		}
		if flag != nil {
			found = true
			if flag.NoOptDefVal == "" {
				consumes = true
			}
		}
	}
	for _, child := range cmd.Commands() {
		childConsumes, childFound := commandTreeFlagConsumesValue(child, name, shorthand)
		found = found || childFound
		consumes = consumes || childConsumes
	}
	return consumes, found
}

func machineJSONInvocation(cmd *cobra.Command) (bool, string) {
	if robotInvocation, robotCommand := robotInvocationFromArgs(os.Args[1:]); robotInvocation {
		return true, robotCommand
	}
	if !jsonOutput && !jsonInvocationFromArgs(os.Args[1:]) {
		return false, ""
	}
	command := "cli"
	if cmd != nil {
		path := strings.TrimSpace(cmd.CommandPath())
		rootName := "ntm"
		if root := cmd.Root(); root != nil && strings.TrimSpace(root.Name()) != "" {
			rootName = root.Name()
		}
		if path != "" && path != rootName {
			command = strings.TrimSpace(strings.TrimPrefix(path, rootName))
		}
	}
	return true, command
}

// encryptionStartupError reports a fatal encryption-at-rest startup failure.
// Machine invocations get a single JSON error document; humans get the error
// itself. Either way NTM refuses to run, because falling back to plaintext
// persistence would silently defeat the setting the operator enabled.
func encryptionStartupError(cmd *cobra.Command, err error, hint string) error {
	if machineInvocation, machineCommand := machineJSONInvocation(cmd); machineInvocation {
		return robot.EncodeErrorJSON(err, robot.ErrCodeInternalError, hint, machineCommand)
	}
	return fmt.Errorf("%w (%s)", err, hint)
}

func isRobotGlobalModifier(command string) bool {
	switch command {
	case "robot-format", "robot-output-format", "robot-verbosity", "robot-limit", "robot-offset":
		return true
	default:
		return false
	}
}

var errCLIInvalidInput = errors.New("invalid CLI input")

type cliInvalidInputError struct {
	err error
}

func (e *cliInvalidInputError) Error() string { return e.err.Error() }
func (e *cliInvalidInputError) Unwrap() error { return e.err }
func (e *cliInvalidInputError) Is(target error) bool {
	return target == errCLIInvalidInput || errors.Is(e.err, target)
}

func markCLIInvalidInput(err error) error {
	if err == nil || errors.Is(err, errCLIInvalidInput) {
		return err
	}
	return &cliInvalidInputError{err: err}
}

func classifyRobotExecuteError(err error) (string, string) {
	return classifyRobotExecuteErrorForCommand(err, rootCmd)
}

// Suggestions must come from the command that rejected a flag. A root-only
// robot option is not accepted by a subcommand, even if its name matches.
func classifyRobotExecuteErrorForCommand(err error, cmd *cobra.Command) (string, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout, "Retry the command after cancellation"
	}
	if errors.Is(err, errCLIInvalidInput) {
		return robot.ErrCodeInvalidFlag, "Fix the invalid command input or selected configuration"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, `for "--robot-status" flag`) && strings.Contains(message, "strconv.parsebool") {
		return robot.ErrCodeInvalidFlag, "--robot-status reports all sessions and takes no value; run 'ntm --robot-status' and select the requested session from the sessions array"
	}
	for _, fragment := range []string{
		"unknown flag",
		"unknown shorthand flag",
		// Cobra reports a stray positional argument (e.g. `--robot-send
		// --session X proj` parsing "proj" as a subcommand) as "unknown
		// command". That is deterministic caller input, not an internal
		// fault — "retry" would be actively wrong advice.
		"unknown command",
		"flag needs an argument",
		"invalid argument",
		"required flag",
		"requires at least",
		"requires at most",
		"accepts ",
	} {
		if strings.Contains(message, fragment) {
			hint := "Use 'ntm --robot-help' or 'ntm --robot-capabilities' to inspect valid flags"
			if unknown := unknownFlagFromError(err.Error()); unknown != "" {
				if curated, suggestion := flagErrorGuidance(unknown, executableFlagSets(cmd)...); curated != "" {
					hint = curated
				} else if suggestion != "" {
					hint = fmt.Sprintf("Did you mean --%s? Otherwise use 'ntm --robot-help' or 'ntm --robot-capabilities' to inspect valid flags", suggestion)
				}
			}
			return robot.ErrCodeInvalidFlag, hint
		}
	}
	return robot.ErrCodeInternalError, "Retry the command or inspect ntm diagnostics"
}

// executableFlagSets mirrors Cobra's accepted local/inherited flags without
// suggesting root-local robot flags to unrelated subcommands.
func executableFlagSets(cmd *cobra.Command) []*pflag.FlagSet {
	if cmd == nil {
		return nil
	}
	if cmd == rootCmd {
		return []*pflag.FlagSet{rootCmd.Flags(), rootCmd.PersistentFlags()}
	}
	return []*pflag.FlagSet{cmd.LocalNonPersistentFlags(), cmd.InheritedFlags(), cmd.PersistentFlags()}
}

// unknownFlagFromError extracts the offending flag name from a cobra/pflag
// "unknown flag: --foo" error message, or "" when the message has a
// different shape.
func unknownFlagFromError(errMsg string) string {
	const marker = "unknown flag: --"
	idx := strings.Index(errMsg, marker)
	if idx < 0 {
		return ""
	}
	unknown := strings.TrimSpace(errMsg[idx+len(marker):])
	if cut := strings.IndexAny(unknown, " \t\n"); cut >= 0 {
		unknown = unknown[:cut]
	}
	return unknown
}

// flagMisGuessHints maps flag spellings agents repeatedly guess (top
// mis-guesses in the cass-mined INVALID_FLAG dataset, 2026-08 fleet mining)
// to a hint naming the real spelling. Consulted when the guessed flag does
// not exist on the failing surface, before the generic edit-distance
// suggester, because the correct fix is usually not the nearest flag name
// (e.g. --session's fix is passing the session as the robot flag's value).
var flagMisGuessHints = map[string]string{
	"session":     "Pass the session as the robot flag value (e.g. --robot-send=SESSION) or as the command's positional argument (e.g. ntm send SESSION \"msg\")",
	"pane":        "Use --panes=N[,N...] to target panes (robot surfaces also accept singular --pane=N with --robot-send and --robot-history)",
	"project-dir": "Use --spawn-dir=PATH with --robot-spawn, or --project=PATH with ensemble/JFP robot commands",
	"timeout":     "--timeout is a robot-surface flag (e.g. ntm --robot-wait=SESSION --timeout=2m); this command does not accept it",
	"message":     "Use --msg (or --msg-file) for robot message payloads; 'ntm send' takes the prompt as a positional argument",
	"msg":         "Use --msg with robot surfaces (e.g. --robot-send=SESSION --msg='...'); 'ntm send' takes the prompt as a positional argument",
}

// curatedFlagHint returns the curated hint for an unknown flag, tolerating a
// single-edit typo of a curated key (--sesion, --messge). Without the typo
// tolerance those guesses fell through to the generic edit-distance flag
// suggester, which picks a red herring (--sesion's nearest flag on 'ntm send'
// is --json). Matching iterates keys in sorted order so a hypothetical
// distance-1 tie resolves deterministically. Callers must consult a real
// registered flag one edit away FIRST (see flagErrorGuidance): on a command
// that actually has --session, the fix for --sesion is the flag itself, not
// the curated redirection.
func curatedFlagHint(unknown string) (string, bool) {
	if hint, ok := flagMisGuessHints[unknown]; ok {
		return hint, true
	}
	keys := make([]string, 0, len(flagMisGuessHints))
	for key := range flagMisGuessHints {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if levenshteinDistance(unknown, key) == 1 {
			return flagMisGuessHints[key], true
		}
	}
	return "", false
}

// flagErrorGuidance resolves the best guidance for an unknown flag name, in
// precedence order:
//  1. exact curated hint (the guessed flag is a known fleet mis-guess);
//  2. a registered flag one edit away (a plain typo of a real flag on this
//     surface beats the curated redirection: ntm codex --sesion means the
//     command's own --session);
//  3. curated hint for a single-edit typo of a curated key;
//  4. the general edit-distance suggester.
//
// Exactly one of hint/suggestion is non-empty on a hit; both empty means no
// confident guidance exists.
func flagErrorGuidance(unknown string, flagSets ...*pflag.FlagSet) (hint, suggestion string) {
	if unknown == "" {
		return "", ""
	}
	if curated, ok := flagMisGuessHints[unknown]; ok {
		return curated, ""
	}
	nearest := nearestFlagName(unknown, flagSets...)
	if nearest != "" && levenshteinDistance(unknown, nearest) <= 1 {
		return "", nearest
	}
	if curated, ok := curatedFlagHint(unknown); ok {
		return curated, ""
	}
	return "", nearest
}

// nearestFlagName returns the closest registered flag name across the given
// flag sets within a small edit distance, or "" when no confident match
// exists. Field evidence shows agents repeatedly guessing near-miss robot
// flags (--robot-state for --robot-status); each miss without a suggestion
// costs a discovery round-trip.
func nearestFlagName(unknown string, flagSets ...*pflag.FlagSet) string {
	if unknown == "" {
		return ""
	}
	best := ""
	bestDist := len(unknown)/3 + 2 // confidence bound scales with length
	bestPrefix := -1
	for _, fs := range flagSets {
		if fs == nil {
			continue
		}
		fs.VisitAll(func(f *pflag.Flag) {
			// Never suggest the flag that was just rejected: when it is
			// registered elsewhere (a root-only robot flag reaching a
			// subcommand) a distance-0 "Did you mean --x?" sends the caller
			// into a retry loop (#351).
			if f.Name == unknown {
				return
			}
			d := levenshteinDistance(unknown, f.Name)
			if d > bestDist {
				return
			}
			// Ties break toward the candidate sharing the longest prefix with
			// the guess: --robot-state should suggest --robot-status, not the
			// alphabetically earlier --robot-save at the same edit distance.
			p := commonPrefixLen(unknown, f.Name)
			if d < bestDist || p > bestPrefix {
				bestDist = d
				bestPrefix = p
				best = f.Name
			}
		})
	}
	return best
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// levenshteinDistance is a plain O(len(a)*len(b)) edit distance over bytes;
// flag names are short ASCII so this is cheap and allocation-light.
func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// silentExitError propagates a specific process exit code up through Execute()
// so cleanup/audit defers run cleanly without calling os.Exit inside internal/
// handlers or re-decorating already-printed output.
type silentExitError struct {
	code int
}

func newSilentExitError(code int) error {
	if code == 0 {
		return nil
	}
	return &silentExitError{code: code}
}

func (e *silentExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func (e *silentExitError) ExitCode() int {
	return e.code
}

func (e *silentExitError) RawExitCode() int {
	return e.code
}

// ExitCode maps an Execute error onto the public robot process contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var rawExit interface{ RawExitCode() int }
	if errors.As(err, &rawExit) {
		return rawExit.RawExitCode()
	}
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		return robot.NormalizeProcessExitCode(exitCoder.ExitCode())
	}
	return 1
}

// VersionInput is the kernel input for core.version.
type VersionInput struct {
	Short bool `json:"short,omitempty"`
}

var rootCmd = &cobra.Command{
	Use: "ntm",
	// Version enables cobra's built-in --version flag. Field evidence from
	// fleet health probes shows agents reach for `ntm --version` first and
	// treat "unknown flag" as a broken install; `ntm version` remains the
	// detailed surface.
	Version: Version,
	Short:   "Named Tmux Manager - orchestrate AI coding agents in tmux sessions",
	Long: `NTM (Named Tmux Manager) helps you create and manage tmux sessions
with multiple AI coding agents (Claude, Codex, Antigravity, Grok Build, and legacy Gemini) in separate panes.

Quick Start:
  ntm spawn myproject --cc=2 --cod=2    # Create session with 4 agents
  ntm attach myproject                   # Attach to session
  ntm palette                            # Open command palette (TUI)
  ntm send myproject --all "fix bugs"   # Broadcast prompt to all agents

Shell Integration:
  Add to your .zshrc:  eval "$(ntm shell zsh)"
  Add to your .bashrc: eval "$(ntm shell bash)"`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Configure remote client if requested
		if sshHost != "" {
			tmux.DefaultClient = tmux.NewClient(sshHost)
		}

		if robotContextInject != "" && tmux.DefaultClient.Remote != "" {
			return robot.EncodeErrorJSON(
				ntmctx.ErrRemoteContextPreparation,
				robot.ErrCodeNotImplemented,
				"Run context preparation on the target host, or use 'ntm context inject SESSION --pack ID' for a stored artifact",
				"robot-context-inject",
			)
		}

		// Agent plugins are recognised agent types everywhere (status, robot
		// surfaces, send targeting, deps), not only at spawn/add (ntm#260).
		// Cheap: a directory scan of a handful of TOML files, nil when absent.
		registerAgentPluginTypes(pluginAgentsDirForArgs(os.Args[1:]))

		// Handle --no-color flag by setting environment variable
		// This integrates with the existing theme.NoColorEnabled() system
		if noColor {
			os.Setenv("NTM_NO_COLOR", "1")
		}
		theme.ApplyLipGlossDefaults(theme.Current())

		// Phase 1: Critical startup (always runs, minimal overhead)
		startup.BeginPhase1()
		EnableProfilingIfRequested()
		startup.EndPhase1()

		// Check if this command can skip config loading (Phase 1 only)
		// This includes subcommands AND robot flags that don't need config
		if canSkipConfigLoading(cmd.Name()) {
			startCommandAudit(cmd, args)
			return nil
		}

		// Phase 2: Deferred initialization (config loading)
		startup.BeginPhase2()
		defer startup.EndPhase2()

		// Preserve whether the user explicitly selected a config. Runtime safety
		// checks use NTM_CONFIG as that signal, so propagating an implicit default
		// path would incorrectly make a missing default config fatal.
		configureStartupConfigPath()

		// Load config lazily - only commands that need it will trigger loading
		if needsConfigLoading(cmd.Name()) {
			endProfile := ProfileConfigLoad()
			var err error
			cfg, err = startup.GetConfig()
			endProfile()
			if err != nil {
				if machineInvocation, machineCommand := machineJSONInvocation(cmd); machineInvocation {
					return robot.EncodeErrorJSON(
						fmt.Errorf("config load failed: %w", err),
						robot.ErrCodeInvalidFlag,
						"Fix the selected TOML configuration or pass --config with a valid file",
						machineCommand,
					)
				}
				// Config loading failed for a genuine reason (e.g. an invalid
				// global config); an invalid project overlay no longer reaches
				// here — it is skipped with its own warning inside LoadMerged.
				// Warn loudly so the user sees the real cause instead of
				// silently reverting to built-in defaults (issue #162).
				//
				// Exception (bd-config-migrate-warning-wall-151x2): when the
				// failure is removed/deprecated keys, the full multi-key
				// disposition wall printed on EVERY invocation (~30 lines per
				// terminal pane via shell integration). Collapse it to one
				// actionable line; the full detail remains in robot JSON
				// envelopes, `ntm doctor`, and `ntm config migrate --dry-run`.
				var deadErr *config.DeadKeyLoadError
				if errors.As(err, &deadErr) && deadErr.DeadKeyCount() > 0 {
					fmt.Fprintf(os.Stderr, "ntm: config has %d removed key(s) — run 'ntm config migrate' to clean them (backup kept); details: ntm doctor\n", deadErr.DeadKeyCount())
				} else {
					fmt.Fprintf(os.Stderr, "ntm: warning: config load failed (%v); using built-in defaults\n", err)
				}
				cfg = config.Default()
			}
			// Record the configured theme process-wide so every theme.Current()
			// caller (dashboard panels and overlays included) honors it;
			// an explicit NTM_THEME still takes precedence inside Current.
			if cfg != nil {
				theme.SetConfigured(cfg.Theme)
			}
			theme.ApplyLipGlossDefaults(theme.Current())

			// Thread [retry] and [rotation.thresholds] policies into their
			// consuming packages (WS6-wire, bd-ws6-config-truth-ienmd.1).
			applyConfiguredPolicies(cfg)

			// Apply redaction flag overrides. Robot invocations must fail with
			// one machine-readable envelope; the human CLI retains its historical
			// warn-and-ignore behavior for an invalid override.
			if err := applyRedactionFlagOverrides(cfg); err != nil {
				if machineInvocation, machineCommand := machineJSONInvocation(cmd); machineInvocation {
					return robot.EncodeErrorJSON(
						err,
						robot.ErrCodeInvalidFlag,
						"Use --redact=off, --redact=warn, --redact=redact, or --redact=block",
						machineCommand,
					)
				}
				fmt.Fprintf(os.Stderr, "Warning: %v, ignoring\n", err)
			}

			// Ensure persisted prompt history + event logs never store raw secrets/PII when redaction is enabled.
			// (bd-3sl0s)
			if cfg != nil {
				// Install the merged operator-gate label vocabulary so every
				// automated assignment surface honors project-specific gating
				// labels (#223).
				bv.ConfigureOperatorGatedLabels(cfg.Assign.OperatorGatedLabels)
				// ...and the [assign.work_source] policy dispatch and work
				// snapshots apply (GH #283).
				bv.ConfigureWorkSourcePolicy(cfg.Assign.WorkSource.Policy())

				// Bound every bv subprocess with the configured timeout
				// ([integrations.bv] timeout_seconds; NTM_BV_TIMEOUT wins, GH#253).
				bv.ConfigureCommandTimeout(cfg.Integrations.BV.TimeoutSeconds)

				// The lookup lets every ntm process honor the privacy
				// settings `ntm spawn --privacy` recorded on a session.
				privacy.SetDefaultManager(privacy.New(cfg.Privacy).WithSessionLookup(tmuxSessionPrivacy))

				redactCfg := cfg.Redaction.ToRedactionLibConfig()
				history.SetRedactionConfig(&redactCfg)
				events.SetRedactionConfig(&redactCfg)
				audit.SetRedactionConfig(&redactCfg)
				session.SetRedactionConfig(&redactCfg)
				checkpoint.SetRedactionConfig(&redactCfg)
			}

			// Wire encryption into history + event log persistence (bd-3ld77).
			// Key resolution failures are fatal for every invocation: continuing
			// would persist prompt history and event logs in plaintext even though
			// the operator asked for encryption at rest
			// (docs/ENCRYPTION_SPEC.md, "Failure Modes").
			if cfg != nil && cfg.Encryption.Enabled {
				keyCfg := encryption.KeyConfig{
					KeySource:   cfg.Encryption.KeySource,
					KeyEnv:      cfg.Encryption.KeyEnv,
					KeyFile:     cfg.Encryption.KeyFile,
					KeyCommand:  cfg.Encryption.KeyCommand,
					KeyFormat:   cfg.Encryption.KeyFormat,
					ActiveKeyID: cfg.Encryption.ActiveKeyID,
					Keyring:     cfg.Encryption.Keyring,
				}
				encKey, err := encryption.ResolveKey(keyCfg)
				if err != nil {
					return encryptionStartupError(
						cmd,
						fmt.Errorf("encryption key resolution failed: %w", err),
						"Configure a valid encryption key source, or set [encryption] enabled = false to persist artifacts unencrypted",
					)
				}
				allKeys, err := encryption.ResolveKeyring(keyCfg)
				if err != nil {
					return encryptionStartupError(
						cmd,
						fmt.Errorf("encryption keyring resolution failed: %w", err),
						"Configure a valid encryption keyring, or set [encryption] enabled = false to persist artifacts unencrypted",
					)
				}
				history.SetEncryptionConfig(&history.EncryptionConfig{
					Enabled:     true,
					EncryptKey:  encKey,
					DecryptKeys: allKeys,
				})
				events.SetEncryptionConfig(&events.EncryptionConfig{
					Enabled:     true,
					EncryptKey:  encKey,
					DecryptKeys: allKeys,
				})
				checkpoint.SetEncryptionConfig(&checkpoint.EncryptionConfig{
					Enabled:     true,
					EncryptKey:  encKey,
					DecryptKeys: allKeys,
				})
			} else {
				history.SetEncryptionConfig(nil)
				events.SetEncryptionConfig(nil)
				checkpoint.SetEncryptionConfig(nil)
			}

			// Run automatic temp file cleanup if enabled
			MaybeRunStartupCleanup(
				cfg.Cleanup.AutoCleanOnStartup,
				cfg.Cleanup.MaxAgeHours,
				cfg.Cleanup.Verbose,
			)
		}

		if shouldInitializeRobotPersistence(cmd) {
			if err := initializeRobotPersistence(cmd.Context(), robotPersistenceRefreshesProjection(cmd)); err != nil {
				return err
			}
		}
		startCommandAudit(cmd, args)
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Profiling is a separate human-readable document. Appending it to a
		// machine response would make the stdout stream impossible to decode.
		if machineInvocation, _ := machineJSONInvocation(cmd); !machineInvocation {
			PrintProfilingIfEnabled()
			recordInteractiveUsage(cmd)
		}
	},
	Run: runRootCommand,
}

// RootCommand exposes the real cobra command tree for the docs-example
// conformance test (WS0-G3, bd-ws0-guards-klz98.4), which resolves every
// fenced `ntm ...` doc example against it. Read-only: callers must not
// mutate the tree or execute commands through it.
//
// NOTE(deadcode): flagged unreachable from ./cmd/ntm because its only
// consumer is tests/docs_conformance_test.go — an active docs CI gate.
// Deliberately retained; see G1 dead-code lane report.
func RootCommand() *cobra.Command {
	return rootCmd
}

func Execute() error {
	defer closeRobotPersistence()
	defer persistEmittedBusEvents(events.DefaultEmitter(), events.DefaultBus)
	robotProcessExit = nil
	robotInvocation, robotCommand := robotInvocationFromArgs(os.Args[1:])
	machineInvocation := robotInvocation || jsonInvocationFromArgs(os.Args[1:])
	if machineInvocation {
		restoreDiagnostics := suppressRobotDiagnostics(rootCmd)
		defer restoreDiagnostics()
	}
	// Install a signal-cancellable context at the root so handlers using
	// `cmd.Context()` (diagnose, swarm, openapi, support_bundle, …) see
	// cancellation when the user presses Ctrl-C. Without this, `cmd.Context()`
	// is a background context that never cancels, making any context-aware
	// API plumbed through cobra effectively non-cancellable. Commands with
	// their own `signal.Notify`-based shutdown loops (serve, watch, monitor,
	// send, …) continue to receive the OS signal directly — Go delivers it
	// to every registered consumer — so adding a root-level consumer does
	// not interfere with their existing shutdown logic.
	err := executeRootWithSignals(rootCmd.ExecuteContext)
	if err == nil && robotProcessExit != nil {
		err = robotProcessExit
	}
	logCommandAuditEnd(err)
	_ = audit.CloseAll()
	if err != nil {
		var processExit *robot.ProcessExitError
		if errors.As(err, &processExit) {
			if processExit.JSONWritten() {
				return err
			}
			fmt.Fprintln(os.Stderr, "Error:", err)
			return err
		}
		// errJSONFailure means the command already wrote its failure envelope
		// to stdout; the only remaining job is to exit non-zero. Don't decorate
		// stderr — the JSON is the contract.
		if errors.Is(err, errJSONFailure) {
			return err
		}
		var silentExit *silentExitError
		if errors.As(err, &silentExit) {
			return err
		}
		if machineInvocation || jsonOutput {
			command := robotCommand
			if command == "" {
				_, command = machineJSONInvocation(rootCmd)
			}
			failingCommand := rootCmd
			if candidate, _, findErr := rootCmd.Find(os.Args[1:]); findErr == nil && candidate != nil {
				failingCommand = candidate
			}
			code, hint := classifyRobotExecuteErrorForCommand(err, failingCommand)
			return robot.EncodeErrorJSON(err, code, hint, command)
		}
		// If not in JSON mode, print the error to stderr
		// (SilenceErrors is set to true to handle JSON mode properly)
		if !jsonOutput {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return err
	}
	return nil
}

func executeRootWithSignals(run func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	commandDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-signals:
			// Reset process-wide handling before publishing cancellation. Reset
			// waits for the current notification fanout, so command-local Notify
			// channels still receive the first signal while a second signal uses
			// the OS default and force-terminates a context-ignoring command.
			signal.Reset(os.Interrupt, syscall.SIGTERM)
			cancel()
		case <-commandDone:
		}
	}()

	err := run(ctx)
	close(commandDone)
	signal.Stop(signals)
	cancel()
	<-watcherDone
	return err
}

func suppressRobotDiagnostics(cmd *cobra.Command) func() {
	previousSlog := slog.Default()
	previousLogWriter := log.Writer()
	previousCommandErr := cmd.ErrOrStderr()
	deprecatedFlags := make(map[*pflag.Flag]string)
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if flag.Deprecated != "" {
			deprecatedFlags[flag] = flag.Deprecated
			flag.Deprecated = ""
		}
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	log.SetOutput(io.Discard)
	cmd.SetErr(io.Discard)
	return func() {
		for flag, message := range deprecatedFlags {
			flag.Deprecated = message
		}
		slog.SetDefault(previousSlog)
		log.SetOutput(previousLogWriter)
		cmd.SetErr(previousCommandErr)
	}
}

func shouldInitializeRobotPersistence(cmd *cobra.Command) bool {
	if cmd != nil && cmd.Name() == "serve" {
		return false
	}
	// `ntm activity` is not a --robot- command, but it needs the runtime store
	// to keep pane state age stable across one-shot invocations (ntm#301).
	if cmd != nil && cmd.Name() == activityCommandName {
		return true
	}

	for _, arg := range os.Args[1:] {
		if arg == "--schema" {
			return true
		}
		if strings.HasPrefix(arg, "--robot-") {
			command := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
			if isRobotGlobalModifier(command) {
				continue
			}
			if !robotFlagSkipsPersistence(arg) {
				return true
			}
		}
	}

	return false
}

func robotFlagSkipsPersistence(arg string) bool {
	if i := strings.IndexByte(arg, '='); i >= 0 {
		arg = arg[:i]
	}
	switch arg {
	case "--robot-overlay":
		return true
	default:
		return false
	}
}

// activityCommandName is the cobra name of the `ntm activity` command.
const activityCommandName = "activity"

// robotPersistenceRefreshesProjection reports whether opening the runtime store
// should also refresh the normalized bv/br projection. `ntm activity` only
// needs the store's watermarks; the projection refresh is a bv/br round trip
// with a 10s budget that would tax a command whose whole point is a fast pane
// snapshot. The same holds for robot surfaces that never read the projection
// (robotSurfacesWithoutProjection).
func robotPersistenceRefreshesProjection(cmd *cobra.Command) bool {
	if cmd == nil {
		return true
	}
	if cmd.Name() == activityCommandName {
		return false
	}
	surfaces, exempt := 0, 0
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.HasPrefix(f.Name, "robot-") || isRobotGlobalModifier(f.Name) {
			return
		}
		surfaces++
		if robotSurfacesWithoutProjection[f.Name] {
			exempt++
		}
	})
	return surfaces == 0 || exempt < surfaces
}

// robotSurfacesWithoutProjection are robot surfaces that never read the
// normalized projection, so they skip its refresh, which on a busy host cost
// 2.5-10s and ~60 subprocesses per call. Evidence, from an RTA call graph over
// static calls and interface invokes starting at each Print function: the
// static-metadata and tool-bridge surfaces (cass, jfp, ms, xf, slb, rch, caut,
// ru, dcg, rano, giil, Agent Mail) never reach currentProjectionStore; send,
// interrupt, ack, tail, is-working and send-receipt reach the store only for
// send-operation records and attention-event appends (the send guard hook
// included). Lifecycle verbs inspect their targets after host admission and
// must not observe remote panes through a startup refresh before that check.
// An invocation that also names any other robot surface still refreshes.
var robotSurfacesWithoutProjection = map[string]bool{
	"robot-send": true, "robot-interrupt": true, "robot-ack": true, "robot-tail": true,
	"robot-is-working": true, "robot-send-receipt": true,
	"robot-action-status": true, "robot-wait-request": true, "robot-cancel-request": true,
	"robot-exit-cli": true, "robot-kill-agent": true, "robot-smart-restart": true,
	"robot-capabilities": true, "robot-help": true, "robot-docs": true, "robot-schema": true,
	"robot-palette": true, "robot-recipes": true, "robot-ensemble-modes": true, "robot-ensemble-presets": true,
	"robot-default-prompts": true, "robot-profile-list": true, "robot-profile-show": true,
	"robot-cass-search": true, "robot-cass-context": true, "robot-cass-insights": true, "robot-cass-status": true,
	"robot-ms-search": true, "robot-ms-show": true, "robot-xf-search": true, "robot-xf-status": true,
	"robot-jfp-status": true, "robot-jfp-list": true, "robot-jfp-search": true, "robot-jfp-show": true,
	"robot-jfp-suggest": true, "robot-jfp-export": true, "robot-jfp-update": true,
	"robot-jfp-categories": true, "robot-jfp-tags": true, "robot-jfp-bundles": true,
	"robot-slb-pending": true, "robot-slb-approve": true, "robot-slb-deny": true,
	"robot-rch-status": true, "robot-rch-workers": true, "robot-tools": true, "robot-acfs-status": true,
	"robot-proxy-status": true, "robot-quota-status": true, "robot-quota-check": true,
	"robot-account-status": true, "robot-accounts-list": true, "robot-switch-account": true,
	"robot-ru-sync": true, "robot-dcg-status": true, "robot-dcg-check": true, "robot-guard": true,
	"robot-safety-simulate": true, "robot-policy-decisions": true, "robot-ubs-scan": true,
	"robot-mail": true, "robot-mail-check": true, "robot-rano-stats": true, "robot-giil-fetch": true,
	"robot-kill-pane": true, "robot-restart-pane": true,
}

func initializeRobotPersistence(ctx context.Context, refreshProjection bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if robotStateStore != nil {
		return nil
	}

	store, err := state.Open("")
	if err != nil {
		return fmt.Errorf("open robot state store: %w", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		return fmt.Errorf("migrate robot state store: %w", err)
	}

	robot.SetAttentionFeed(robot.NewAttentionFeed(
		robot.DefaultAttentionFeedConfig(),
		robot.WithAttentionStore(store),
	))
	robot.SetProjectionStore(store)
	robotStateStore = store
	collectStateGarbageOccasionally(store, state.DefaultPath()+".gc", robotStateGCInterval, time.Now())

	if !refreshProjection {
		return nil
	}

	// Assignment commands perform their own strict policy validation before
	// consulting bv/br. Keep the persistence store available to the command, but
	// do not let this optional refresh cross that command-specific safety boundary.
	// Explicitly selected invalid config paths have the same ordering requirement.
	if robotAssignmentCommandRequiresPolicyPreflight() || !selectedConfigAllowsProjectionRefresh() {
		return nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = robot.RefreshNormalizedProjection(refreshCtx, store, "", "")
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// robotStateGCInterval bounds how often CLI robot processes run the full
// state-store GC. ntm serve collects hourly as well (serveStateMaintenance);
// this covers hosts that never run serve (bd-7dhqw).
const robotStateGCInterval = time.Hour

// collectStateGarbageOccasionally runs Store.RunGC when the previous pass,
// recorded as the mtime of a stamp file next to the state DB, is older than
// interval or never happened. The stamp is claimed before collecting so a
// burst of concurrent robot commands collects once. Failures only log: GC is
// maintenance and never fails the command.
func collectStateGarbageOccasionally(store *state.Store, stamp string, interval time.Duration, now time.Time) bool {
	if info, err := os.Stat(stamp); err == nil && now.Sub(info.ModTime()) < interval {
		return false
	}
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		slog.Debug("state GC stamp not writable; skipping", "path", stamp, "err", err)
		return false
	}
	if err := os.Chtimes(stamp, now, now); err != nil {
		slog.Debug("state GC stamp time not set", "path", stamp, "err", err)
	}
	if _, err := store.RunGC(state.RuntimeGCConfig{}); err != nil {
		slog.Warn("state store GC failed", "err", err)
	}
	return true
}

// openDurableAttentionFeed gives a long-lived process (the internal session
// monitor, `ntm coordinator run`) the store-backed attention feed that robot
// commands get in initializeRobotPersistence, and forwards this process's
// event-bus traffic into it (bd-viwo4). Without it, actuation records
// published here went to an in-memory feed nobody else could read, and agent
// crashed/restarted/rate-limited and session-ended events reached webhooks
// only, never --robot-attention or --robot-snapshot --since. The returned
// func unsubscribes, stops the feed and closes the store.
func openDurableAttentionFeed() (func(), error) {
	feed, closeFeed, err := openStoreAttentionFeed()
	if err != nil {
		return nil, err
	}
	unsubscribe := feed.SubscribeEventBus(events.DefaultBus)
	busEventsPersistedLive.Store(true)
	return func() {
		drainEvents()
		unsubscribe()
		closeFeed()
	}, nil
}

// drainEvents waits, bounded, until the events this process emitted are on the
// bus and the handlers they started have returned. A process unsubscribing its
// durable feed calls it first: the monitor's session-ended event is emitted
// right before it returns, and would otherwise be lost in the async emitter.
func drainEvents() {
	events.DefaultEmitter().Flush(emittedEventsFlushTimeout)
	events.DefaultBus.WaitHandlers(emittedEventsFlushTimeout)
}

// openStoreAttentionFeed installs a store-backed attention feed as the global
// feed. The returned func stops it and closes the store.
func openStoreAttentionFeed() (*robot.AttentionFeed, func(), error) {
	store, err := state.Open("")
	if err != nil {
		return nil, nil, fmt.Errorf("open state store for the attention feed: %w", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("migrate state store for the attention feed: %w", err)
	}
	feed := robot.NewAttentionFeed(robot.DefaultAttentionFeedConfig(), robot.WithAttentionStore(store))
	robot.SetAttentionFeed(feed)
	return feed, func() {
		robot.SetAttentionFeed(nil)
		store.Close()
	}, nil
}

// busEventsPersistedLive marks a process whose durable feed is subscribed to
// the event bus (internal monitor, coordinator run, serve): its events are
// already stored, so persistEmittedBusEvents must not replay them.
var busEventsPersistedLive atomic.Bool

// emittedEventsFlushTimeout bounds how long a command waits at exit for its
// queued events to reach the bus.
const emittedEventsFlushTimeout = 2 * time.Second

// persistEmittedBusEvents stores the webhook events a short-lived command
// emitted (spawn, add, kill, assignment status) in the durable attention feed
// before the process exits. The emitter publishes asynchronously, so without
// this they died with the process and never reached --robot-events or
// --robot-wait (bd-viwo4). Events come from the bus's in-process history,
// oldest first, after the emitter's queue drains.
func persistEmittedBusEvents(emitter *events.EventEmitter, bus *events.EventBus) {
	if busEventsPersistedLive.Load() {
		return
	}
	if !emitter.Flush(emittedEventsFlushTimeout) {
		slog.Default().Debug("event emitter did not drain before exit; persisting what reached the bus")
	}
	history := bus.History(0)
	var emitted []events.WebhookEvent
	for i := len(history) - 1; i >= 0; i-- {
		if event, ok := history[i].(events.WebhookEvent); ok {
			emitted = append(emitted, event)
		}
	}
	if len(emitted) == 0 {
		return
	}
	// A robot command's global feed is already store-backed
	// (initializeRobotPersistence); any other command opens one for this.
	feed := robot.PeekAttentionFeed()
	if robotStateStore == nil || feed == nil {
		storeFeed, closeFeed, err := openStoreAttentionFeed()
		if err != nil {
			slog.Default().Debug("emitted events not persisted", "events", len(emitted), "error", err)
			return
		}
		defer closeFeed()
		feed = storeFeed
	}
	for _, event := range emitted {
		feed.PublishBusEvent(event)
	}
}

func closeRobotPersistence() {
	if robotStateStore == nil {
		return
	}
	_ = robotStateStore.Close()
	robotStateStore = nil
	robot.SetProjectionStore(nil)
}

func startCommandAudit(cmd *cobra.Command, args []string) {
	if cmd == nil || !auditCommandStart.IsZero() {
		return
	}
	auditCorrelationID = audit.NewCorrelationID()
	auditCommandPath = cmd.CommandPath()
	auditCommandStart = time.Now()

	cwd, _ := os.Getwd()
	payload := map[string]interface{}{
		"phase":          "start",
		"command":        auditCommandPath,
		"command_name":   cmd.Name(),
		"args_preview":   commandArgsPreview(args),
		"args_count":     len(args),
		"cwd":            cwd,
		"correlation_id": auditCorrelationID,
	}
	_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, auditCommandPath, payload, nil)
}

func logCommandAuditEnd(err error) {
	if auditCommandStart.IsZero() {
		return
	}
	duration := time.Since(auditCommandStart)
	payload := map[string]interface{}{
		"phase":          "finish",
		"command":        auditCommandPath,
		"success":        err == nil,
		"duration_ms":    duration.Milliseconds(),
		"correlation_id": auditCorrelationID,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, auditCommandPath, payload, nil)
}

func commandArgsPreview(args []string) string {
	if len(args) == 0 {
		return ""
	}
	const maxLen = 200
	preview := strings.Join(args, " ")
	if len(preview) > maxLen {
		return preview[:maxLen] + "..."
	}
	return preview
}

// goVersion returns the current Go runtime version.
func goVersion() string {
	return runtime.Version()
}

// goPlatform returns the OS/ARCH string.
func goPlatform() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}

func init() {
	initRootFlagsAndSubcommands()
}

func init() {
	registerCoreVersionKernel()
}
