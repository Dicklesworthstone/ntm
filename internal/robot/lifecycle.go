// Package robot provides machine-readable output for AI agents.
// lifecycle.go implements --robot-exit-cli and --robot-kill-agent: agent
// process lifecycle inside a pane WITHOUT destroying the pane or its shell
// (ntm-2p5x). Before these verbs, operators hand-rolled the choreography the
// docs called "robust kill": tmux send-keys C-c; sleep 0.3; C-c, or pane_pid
// → pgrep -P → kill -9 — millisecond keystroke timing and process-tree
// surgery that belong inside the abstraction.
package robot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// LifecycleOptions configures --robot-exit-cli / --robot-kill-agent.
type LifecycleOptions struct {
	Session        string
	Panes          []string // pane selectors (N, W.P, %N); empty = all agent panes
	Relaunch       bool     // relaunch the agent CLI after exit/kill and verify
	RespawnShell   bool     // opt-in for --robot-exit-cli: replace exited direct-exec root agent with a fresh shell in the same cwd
	AllowPaneClose bool     // opt-in for --robot-exit-cli: permit a direct-exec root agent to exit and close its pane cleanly

	// ExpectedPane binds a compound caller's next action to the original
	// pane ID, root PID and agent type. Nil starts a new lifecycle operation.
	ExpectedPane *tmux.Pane
}

// LifecyclePaneResult is per-pane structured evidence for a lifecycle verb.
type LifecyclePaneResult struct {
	Pane           string `json:"pane"`
	Target         string `json:"target"`
	AgentType      string `json:"agent_type"`
	Status         string `json:"status,omitempty"`
	LineageMode    string `json:"lineage_mode,omitempty"`
	ShellPID       int    `json:"shell_pid,omitempty"`
	AgentPIDs      []int  `json:"agent_pids,omitempty"`
	Exited         bool   `json:"exited"`
	PaneClosed     bool   `json:"pane_closed,omitempty"`
	Killed         []int  `json:"killed,omitempty"`
	ShellPreserved bool   `json:"shell_preserved"`
	Relaunched     bool   `json:"relaunched,omitempty"`
	Detail         string `json:"detail,omitempty"`

	// VerificationFailed distinguishes "the pane is gone" from "tmux could not
	// be queried, so nothing was verified". Without it, shell_preserved:false
	// told the operator the verb had destroyed the pane — the one outcome both
	// verbs promise to avoid — when in fact the post-action lookup had simply
	// hit a transient error.
	VerificationFailed bool `json:"verification_failed,omitempty"`
}

// ExitCLIOutput is the structured output for --robot-exit-cli.
type ExitCLIOutput struct {
	RobotResponse
	Session string                `json:"session"`
	Results []LifecyclePaneResult `json:"results"`
}

// KillAgentOutput is the structured output for --robot-kill-agent.
type KillAgentOutput struct {
	RobotResponse
	Session string                `json:"session"`
	Results []LifecyclePaneResult `json:"results"`
}

// Graceful-exit choreography constants. Claude Code needs the second Ctrl+C
// within roughly 0.1-0.3s of the first to treat the pair as "exit" instead
// of "clear input"; other CLIs tolerate the same cadence.
const (
	lifecycleDoubleTapGap  = 200 * time.Millisecond
	lifecycleExitTimeout   = 6 * time.Second
	lifecyclePollInterval  = 250 * time.Millisecond
	lifecycleTermGrace     = 2 * time.Second
	lifecycleKillGrace     = 1 * time.Second
	lifecycleRelaunchBoot  = 10 * time.Second
	lifecycleChildPIDLimit = 16
)

var errRemoteAgentProcessOperations = errors.New("agent process inspection and control require local tmux; run this command directly on the tmux host without --ssh")

const remoteAgentProcessOperationsHint = "Remote pane process IDs and sessions cannot be inspected or controlled through local process operations."

// requireLocalAgentProcessOperations prevents remote pane PIDs, or a same-named
// local session, from being mistaken for local process inspection or control.
// Pane-preserving exits also need local root-process evidence; a foreground
// command cannot prove that a persistent parent shell exists.
func requireLocalAgentProcessOperations() error {
	if tmux.DefaultClient.Remote != "" {
		return errRemoteAgentProcessOperations
	}
	return nil
}

// resolveLifecycleTargets validates the session and resolves agent-pane
// targets shared by both lifecycle verbs.
func resolveLifecycleTargets(ctx context.Context, opts LifecycleOptions, verb string) ([]tmux.Pane, bool, *RobotResponse) {
	session := strings.TrimSpace(opts.Session)
	if session == "" {
		resp := NewErrorResponse(fmt.Errorf("session name required"), ErrCodeInvalidFlag,
			fmt.Sprintf("Use --robot-%s=SESSION; 'ntm list' shows available sessions", verb))
		return nil, false, &resp
	}
	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		resp := NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return nil, false, &resp
	}
	if !exists {
		resp := NewErrorResponse(fmt.Errorf("session '%s' not found", session), ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions")
		return nil, false, &resp
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		resp := NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return nil, false, &resp
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	var targets []tmux.Pane
	if len(opts.Panes) > 0 {
		targets, err = tmux.ResolvePaneSelectors(panes, opts.Panes, false)
		if err != nil {
			resp := NewErrorResponse(err, ErrCodeInvalidFlag,
				"Use --panes with N, W.P, or %N selectors; --robot-pane-address=SESSION lists them")
			return nil, false, &resp
		}
		targets = dedupePanesByID(targets)
	} else {
		for _, pane := range panes {
			if restartTargetIsAgent(restartPaneAgentType(pane)) {
				targets = append(targets, pane)
			}
		}
	}
	if len(targets) == 0 {
		resp := NewErrorResponse(fmt.Errorf("no agent panes matched"), ErrCodeInvalidFlag,
			"Use --panes to select agent panes; --robot-pane-address=SESSION lists them")
		return nil, false, &resp
	}
	// Both verbs manipulate the agent process; a user pane has no agent to
	// exit or kill, and typing Ctrl+C into it interrupts the operator.
	for _, pane := range targets {
		if !restartTargetIsAgent(restartPaneAgentType(pane)) {
			resp := NewErrorResponse(
				fmt.Errorf("pane %s is not an agent pane (type %s)", paneTargetKey(pane, multiWindow), restartPaneAgentType(pane)),
				ErrCodeInvalidFlag,
				"Restrict --panes to agent panes; --robot-pane-address=SESSION shows pane types",
			)
			return nil, false, &resp
		}
	}
	return tmux.SortPanesByTopology(targets), multiWindow, nil
}

// dedupePanesByID drops repeated panes when overlapping selectors (e.g.
// --panes=1,%37 naming the same pane) resolve to the same target, so a
// destructive verb never processes — or double-kills — a pane twice.
func dedupePanesByID(panes []tmux.Pane) []tmux.Pane {
	seen := make(map[string]struct{}, len(panes))
	deduped := panes[:0]
	for _, pane := range panes {
		if _, ok := seen[pane.ID]; ok {
			continue
		}
		seen[pane.ID] = struct{}{}
		deduped = append(deduped, pane)
	}
	return deduped
}

// paneLookup is the outcome of re-reading a pane after a lifecycle action.
type paneLookup int

const (
	// paneFound: the listing succeeded and the pane is still there.
	paneFound paneLookup = iota
	// paneAbsent: the listing SUCCEEDED and the pane was not in it. This is
	// the only result that proves the pane is gone.
	paneAbsent
	// paneLookupFailed: tmux could not be queried, so nothing is proven.
	paneLookupFailed
)

// lifecycleRefreshAttempts bounds the retry for a transient listing failure.
const lifecycleRefreshAttempts = 3

// refreshLifecyclePane re-reads a pane by stable ID.
//
// It distinguishes "absent from a successful listing" from "the listing
// failed". Collapsing the two meant a single tmux hiccup (busy server, EINTR)
// during the post-action refresh reported shell_preserved:false and
// success:false — telling the operator the verb had destroyed the pane, which
// is precisely what both verbs promise never to do. A failed listing is
// retried briefly, since the motivating case is transient.
func refreshLifecyclePane(ctx context.Context, session, paneID string) (tmux.Pane, paneLookup) {
	var lastErr error
	for attempt := 0; attempt < lifecycleRefreshAttempts; attempt++ {
		if attempt > 0 {
			if ctx.Err() != nil {
				break
			}
			time.Sleep(lifecyclePollInterval)
		}
		panes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			// A session that no longer exists is a DEFINITIVE answer, not a
			// transient failure: killing a session's last pane destroys the
			// session, so the listing legitimately errors and the pane really
			// is gone. Treating that as a lookup failure would report a
			// successful kill-pane as a failure.
			switch tmux.ClassifyCommandError(err).Kind {
			case tmux.CommandErrorSessionNotFound, tmux.CommandErrorNoServer:
				return tmux.Pane{}, paneAbsent
			}
			lastErr = err
			continue
		}
		for _, pane := range panes {
			if pane.ID == paneID {
				return pane, paneFound
			}
		}
		return tmux.Pane{}, paneAbsent
	}
	_ = lastErr
	return tmux.Pane{}, paneLookupFailed
}

// waitForBareShell polls until the pane's foreground process is a bare shell.
func waitForBareShell(ctx context.Context, session, paneID string, timeout time.Duration) (tmux.Pane, bool) {
	deadline := time.Now().Add(timeout)
	var last tmux.Pane
	for {
		pane, lookup := refreshLifecyclePane(ctx, session, paneID)
		switch lookup {
		case paneAbsent:
			// A successful listing without the pane: it really is gone.
			return last, false
		case paneLookupFailed:
			// Could not query tmux. Keep polling until the deadline rather
			// than aborting and reporting the agent as still running, which
			// escalated to a needless --robot-kill-agent.
			if time.Now().After(deadline) || ctx.Err() != nil {
				return last, false
			}
			time.Sleep(lifecyclePollInterval)
			continue
		}
		last = pane
		if pane.Dead {
			return pane, false
		}
		if pane.AgentCLIDead() {
			return pane, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return pane, false
		}
		time.Sleep(lifecyclePollInterval)
	}
}

// sendExitChoreography sends the graceful double Ctrl+C with the per-CLI
// timing window encapsulated. omp gets its own sequence: its Ctrl+C clears
// the draft rather than interrupting, so a double tap with text in the
// composer would never quit.
func sendExitChoreography(ctx context.Context, pane tmux.Pane) error {
	// Every key is fenced in the tmux server. If an earlier key already
	// returned to the shell, a later Ctrl+D (omp) must not close that shell,
	// and a delayed second interrupt must not reach a replacement process.
	guardedCtx, err := tmux.WithPaneMutationGuard(ctx, pane.ID, pane.PID)
	if err != nil {
		return err
	}
	if tmux.AgentType(restartPaneAgentType(pane)).Canonical() == tmux.AgentOmp {
		return tmux.SendOmpExitContext(guardedCtx, pane.ID)
	}
	if err := tmux.DefaultClient.RunSilentContext(guardedCtx, "send-keys", "-t", tmux.ExactTarget(pane.ID), "C-c"); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(lifecycleDoubleTapGap):
	}
	return tmux.DefaultClient.RunSilentContext(guardedCtx, "send-keys", "-t", tmux.ExactTarget(pane.ID), "C-c")
}

// prepareLifecycleRelaunch validates the entire batch before any quit or kill.
// It shares the restart path's saved-command, dependency, and account checks.
func prepareLifecycleRelaunch(ctx context.Context, session string, panes []tmux.Pane, multiWindow bool) (restartLaunchPlan, error) {
	for _, pane := range panes {
		if pane.PID <= 0 || pane.Dead || pane.IsServicePane() {
			return restartLaunchPlan{}, fmt.Errorf("pane %s is not a live agent pane with a known shell PID", pane.ID)
		}
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return restartLaunchPlan{}, fmt.Errorf("load relaunch configuration: %w", err)
	}
	return prepareRestartLaunchPlan(ctx, session, panes, multiWindow, cfg, restartLaunchOverride{}, restartPaneDeps(nil), nil)
}

func lifecyclePaneIdentityMatches(original, refreshed tmux.Pane) bool {
	return original.ID == refreshed.ID && original.PID > 0 && original.PID == refreshed.PID &&
		original.Type.Canonical() == refreshed.Type.Canonical() && !refreshed.IsServicePane() &&
		!refreshed.Dead && refreshed.RotationToken == ""
}

// validateLifecycleExpectedPane prevents a fallback from silently rebinding
// an earlier result's physical pane ID to a replacement process or provider.
func validateLifecycleExpectedPane(opts LifecycleOptions, panes []tmux.Pane) error {
	if opts.ExpectedPane == nil {
		return nil
	}
	if len(panes) != 1 || !lifecyclePaneIdentityMatches(*opts.ExpectedPane, panes[0]) {
		return fmt.Errorf("pane %s identity changed since the preceding lifecycle action; refusing to target a replacement", opts.ExpectedPane.ID)
	}
	return nil
}

// validateLifecycleShells admits the whole batch before any pane is changed.
// A direct-exec root is not a shell, even if tmux labels the pane as an agent.
func validateLifecycleShells(ctx context.Context, panes []tmux.Pane) error {
	for _, pane := range panes {
		if pane.RotationToken != "" {
			return fmt.Errorf("pane %s has an unresolved context rotation; lifecycle action withheld", pane.ID)
		}
		if err := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, pane); err != nil {
			return err
		}
	}
	return nil
}

// verifyLifecycleShell repeats admission immediately before an action. A pane
// may have been respawned or its shell may have exec'd an agent since preflight.
func verifyLifecycleShell(ctx context.Context, session string, original tmux.Pane, result *LifecyclePaneResult) (tmux.Pane, bool) {
	refreshed, lookup := refreshLifecyclePane(ctx, session, original.ID)
	result.ShellPreserved = false
	result.VerificationFailed = lookup == paneLookupFailed
	if lookup != paneFound || !lifecyclePaneIdentityMatches(original, refreshed) {
		result.Detail = joinLifecycleDetail(result.Detail, "lifecycle action withheld: original live agent pane identity could not be verified")
		return refreshed, false
	}
	if err := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, refreshed); err != nil {
		result.Detail = joinLifecycleDetail(result.Detail, "lifecycle action withheld: "+err.Error())
		return refreshed, false
	}
	result.ShellPreserved = true
	return refreshed, true
}

// verifyLifecycleRelaunchShell requires fresh evidence that the original pane
// shell is idle. A stale exit result must not allow typing into another agent.
func verifyLifecycleRelaunchShell(ctx context.Context, session string, pane tmux.Pane, result *LifecyclePaneResult) bool {
	refreshed, verified := verifyLifecycleShell(ctx, session, pane, result)
	if !verified {
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: original persistent shell could not be verified")
		return false
	}
	if !refreshed.AgentCLIDead() {
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: pane is no longer at an idle shell")
		return false
	}
	return true
}

// relaunchAgentCLI replays the preflighted command only after observing the
// original pane back at a shell, recording that command before delivery.
func relaunchAgentCLI(ctx context.Context, session string, pane tmux.Pane, plan restartLaunchPlan, result *LifecyclePaneResult) {
	launchCmd, ok := plan.Commands[result.Pane]
	spec, hasSpec := plan.Specs[result.Pane]
	if !ok || !hasSpec || strings.TrimSpace(launchCmd) == "" {
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: preflighted launch specification is unavailable")
		return
	}
	if !verifyLifecycleRelaunchShell(ctx, session, pane, result) {
		return
	}
	if replay := plan.Replay[result.Pane]; replay != nil {
		var err error
		launchCmd, err = replay.Prepare(ctx, plan.Directories[result.Pane], session, plan.Indices[result.Pane])
		if err == nil {
			launchCmd, err = tmux.BuildPaneCommand(plan.Directories[result.Pane], launchCmd)
		}
		if err != nil {
			result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("prepare saved relaunch failed: %v", err))
			return
		}
		if !verifyLifecycleRelaunchShell(ctx, session, pane, result) {
			return
		}
	}
	if err := tmux.DefaultClient.SetPaneLaunchSpecIfCurrentContext(ctx, pane.ID, pane.PID, spec); err != nil {
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("record relaunch specification failed: %v", err))
		return
	}
	if err := tmux.DefaultClient.SendKeysForAgentIfCurrentContext(ctx, pane.ID, launchCmd, true, tmux.AgentUser, pane.PID, true); err != nil {
		// A tmux command error can contain the command's private arguments.
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch command delivery failed")
		return
	}
	deadline := time.Now().Add(lifecycleRelaunchBoot)
	for {
		refreshed, lookup := refreshLifecyclePane(ctx, session, pane.ID)
		if lookup == paneFound && !lifecyclePaneIdentityMatches(pane, refreshed) {
			result.ShellPreserved = pane.PID > 0 && refreshed.PID == pane.PID && !refreshed.Dead
			result.Detail = joinLifecycleDetail(result.Detail, "relaunch could not be confirmed: agent pane identity changed")
			return
		}
		if lookup == paneFound && strings.TrimSpace(refreshed.Command) != "" && !refreshed.AgentCLIDead() {
			result.Relaunched = true
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			result.Detail = joinLifecycleDetail(result.Detail,
				fmt.Sprintf("relaunch sent but the agent CLI did not appear within %s", lifecycleRelaunchBoot))
			return
		}
		time.Sleep(lifecyclePollInterval)
	}
}

func joinLifecycleDetail(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "; " + extra
}

// classifyExitCLILineage inspects a pane's root PID and process arguments.
// Returns ("shell_backed", rootCmd, nil) when a persistent interactive shell
// hosts the pane, ("direct_exec", rootCmd, nil) when a live root command exists
// without an underlying interactive shell, or an admission error when the pane
// is dead, service-tagged, quarantined, or unreadable.
func classifyExitCLILineage(ctx context.Context, pane tmux.Pane) (string, string, error) {
	if pane.RotationToken != "" {
		return "", "", fmt.Errorf("pane %s has an unresolved context rotation; lifecycle action withheld", pane.ID)
	}
	shellErr := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, pane)
	if shellErr == nil {
		argv := process.GetCmdlineContext(ctx, pane.PID)
		rootCmd := strings.TrimSpace(pane.Command)
		if len(argv) > 0 {
			rootCmd = filepath.Base(strings.TrimSpace(argv[0]))
		}
		return "shell_backed", rootCmd, nil
	}
	if pane.PID <= 0 || pane.Dead || pane.IsServicePane() {
		return "", "", shellErr
	}
	probeCtx, cancel := context.WithTimeout(ctx, tmux.DefaultCommandTimeout)
	defer cancel()
	argv := process.GetCmdlineContext(probeCtx, pane.PID)
	if err := probeCtx.Err(); err != nil {
		return "", "", fmt.Errorf("verify pane %s root process: %w", pane.ID, err)
	}
	if len(argv) == 0 {
		return "", "", shellErr
	}
	rootCmd := filepath.Base(strings.TrimSpace(argv[0]))
	return "direct_exec", rootCmd, nil
}

func validateExitCLITargets(ctx context.Context, opts LifecycleOptions, targets []tmux.Pane) (map[string]string, map[string]string, error) {
	lineages := make(map[string]string, len(targets))
	rootCmds := make(map[string]string, len(targets))
	for _, pane := range targets {
		lineage, rootCmd, err := classifyExitCLILineage(ctx, pane)
		if err != nil {
			return nil, nil, err
		}
		if lineage == "direct_exec" && !opts.RespawnShell && !opts.AllowPaneClose {
			return nil, nil, fmt.Errorf("DIRECT_EXEC_NO_SHELL: pane %s has no persistent interactive shell (root=%s); exiting its root process would destroy the pane", pane.ID, rootCmd)
		}
		lineages[pane.ID] = lineage
		rootCmds[pane.ID] = rootCmd
	}
	if opts.RespawnShell {
		for _, lineage := range lineages {
			if lineage == "direct_exec" {
				if err := tmux.DefaultClient.PreflightRestartShellContext(ctx); err != nil {
					return nil, nil, err
				}
				break
			}
		}
	}
	return lineages, rootCmds, nil
}

func restorePaneRemainOnExit(ctx context.Context, paneID, previous string) {
	restoreCtx := ctx
	if restoreCtx == nil || restoreCtx.Err() != nil {
		var cancel context.CancelFunc
		restoreCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	trimmed := strings.TrimSpace(previous)
	if trimmed == "" {
		_ = tmux.DefaultClient.RunSilentContext(restoreCtx, "set-option", "-p", "-u", "-t", tmux.ExactTarget(paneID), "remain-on-exit")
		return
	}
	_ = tmux.DefaultClient.RunSilentContext(restoreCtx, "set-option", "-p", "-t", tmux.ExactTarget(paneID), "remain-on-exit", trimmed)
}

func waitForDirectExecExit(ctx context.Context, session, paneID string, rootPID int, timeout time.Duration) (tmux.Pane, paneLookup, bool) {
	deadline := time.Now().Add(timeout)
	for {
		pane, lookup := refreshLifecyclePane(ctx, session, paneID)
		switch lookup {
		case paneAbsent:
			return pane, lookup, true
		case paneFound:
			if pane.Dead || (rootPID > 0 && pane.PID == rootPID && !process.IsAlive(rootPID)) {
				return pane, lookup, true
			}
			if pane.PID != rootPID {
				return pane, lookup, false
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return pane, lookup, false
		}
		time.Sleep(lifecyclePollInterval)
	}
}

func exitDirectExecPane(ctx context.Context, opts LifecycleOptions, original tmux.Pane, result *LifecyclePaneResult) (tmux.Pane, bool) {
	refreshed, lookup := refreshLifecyclePane(ctx, opts.Session, original.ID)
	result.ShellPreserved = false
	result.VerificationFailed = lookup == paneLookupFailed
	if lookup != paneFound || !lifecyclePaneIdentityMatches(original, refreshed) {
		result.Status = "withheld"
		result.Detail = joinLifecycleDetail(result.Detail, "lifecycle action withheld: original live agent pane identity could not be verified")
		return refreshed, false
	}

	if opts.AllowPaneClose {
		var exitErr error
		var exited bool
		var obs tmux.Pane
		var obsLookup paneLookup
		for attempt := 0; attempt < 2 && !exited; attempt++ {
			if attempt > 0 {
				refreshed, obsLookup = refreshLifecyclePane(ctx, opts.Session, original.ID)
				if obsLookup == paneAbsent || (obsLookup == paneFound && refreshed.Dead) {
					exited = true
					break
				}
				if obsLookup != paneFound || !lifecyclePaneIdentityMatches(original, refreshed) {
					break
				}
			}
			exitErr = sendExitChoreography(ctx, refreshed)
			obs, obsLookup, exited = waitForDirectExecExit(ctx, opts.Session, original.ID, original.PID, lifecycleExitTimeout)
			if errors.Is(exitErr, tmux.ErrPaneIdentityChanged) && (obsLookup == paneAbsent || (obsLookup == paneFound && obs.Dead)) {
				exitErr = nil
				exited = true
			}
			if exitErr != nil && !exited {
				break
			}
		}
		if exited && (obsLookup == paneAbsent || (obsLookup == paneFound && obs.Dead)) {
			result.Exited = true
			result.PaneClosed = true
			result.Status = "pane_closed"
			result.VerificationFailed = false
			result.Detail = "direct-exec agent exited and pane closed as permitted by --allow-pane-close"
			return obs, false
		}
		result.VerificationFailed = obsLookup == paneLookupFailed
		result.Status = "failed"
		if exitErr != nil {
			result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("exit keystrokes stopped: %v", exitErr))
		} else {
			result.Detail = joinLifecycleDetail(result.Detail, "direct-exec agent did not exit within timeout")
		}
		return obs, false
	}

	// --respawn-shell: retain pane on exit, gracefully exit direct-exec root,
	// then replace with a fresh interactive shell in the exact working directory.
	workingDir, dirErr := restartPaneWorkingDirectory(ctx, original.ID)
	if dirErr != nil {
		result.Status = "failed"
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("cannot capture pane working directory before respawn: %v", dirErr))
		return refreshed, false
	}
	prevRemain, _ := tmux.DefaultClient.RunContext(ctx, "show-options", "-p", "-v", "-t", tmux.ExactTarget(original.ID), "remain-on-exit")
	if err := tmux.DefaultClient.RunSilentContext(ctx, "set-option", "-p", "-t", tmux.ExactTarget(original.ID), "remain-on-exit", "on"); err != nil {
		result.Status = "failed"
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("cannot enable remain-on-exit before direct-exec exit: %v", err))
		return refreshed, false
	}
	defer restorePaneRemainOnExit(ctx, original.ID, prevRemain)

	var exitErr error
	var exited bool
	var obs tmux.Pane
	var obsLookup paneLookup
	for attempt := 0; attempt < 2 && !exited; attempt++ {
		if attempt > 0 {
			refreshed, obsLookup = refreshLifecyclePane(ctx, opts.Session, original.ID)
			if obsLookup == paneFound && refreshed.Dead {
				obs = refreshed
				exited = true
				break
			}
			if obsLookup != paneFound || !lifecyclePaneIdentityMatches(original, refreshed) {
				break
			}
		}
		exitErr = sendExitChoreography(ctx, refreshed)
		obs, obsLookup, exited = waitForDirectExecExit(ctx, opts.Session, original.ID, original.PID, lifecycleExitTimeout)
		if errors.Is(exitErr, tmux.ErrPaneIdentityChanged) && obsLookup == paneFound && obs.Dead {
			exitErr = nil
			exited = true
		}
		if exitErr != nil && !exited {
			break
		}
	}
	if !exited {
		result.VerificationFailed = obsLookup == paneLookupFailed
		result.Status = "failed"
		if exitErr != nil {
			result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("exit keystrokes stopped: %v", exitErr))
		} else {
			result.Detail = joinLifecycleDetail(result.Detail, "direct-exec agent still running after graceful exit attempts; inspect before killing")
		}
		return obs, false
	}

	if err := tmux.DefaultClient.RespawnPaneIfCurrentContext(ctx, original.ID, original.PID, true, workingDir, true); err != nil {
		result.Status = "failed"
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("respawn shell failed: %v", err))
		return obs, false
	}
	shellPane, shellReady := waitForBareShell(ctx, opts.Session, original.ID, lifecycleExitTimeout)
	if !shellReady || tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, shellPane) != nil {
		result.Status = "failed"
		result.Detail = joinLifecycleDetail(result.Detail, "respawned shell could not be verified as an idle persistent shell")
		return shellPane, false
	}
	result.Exited = true
	result.ShellPreserved = true
	result.ShellPID = shellPane.PID
	result.Status = "respawned_shell"
	result.Detail = "direct-exec agent exited and replaced with a fresh interactive shell"
	return shellPane, true
}

// GetExitCLI performs the graceful agent-CLI exit choreography on each target
// pane, verifying via the pane's foreground command instead of assuming.
func GetExitCLI(ctx context.Context, opts LifecycleOptions) (*ExitCLIOutput, error) {
	output := &ExitCLIOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Results:       []LifecyclePaneResult{},
	}
	if err := requireLocalAgentProcessOperations(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, remoteAgentProcessOperationsHint)
		return output, nil
	}
	if opts.RespawnShell && opts.AllowPaneClose {
		output.RobotResponse = NewErrorResponse(
			errors.New("--respawn-shell and --allow-pane-close are mutually exclusive"),
			ErrCodeInvalidFlag,
			"Choose either --respawn-shell (preserve pane with fresh shell) or --allow-pane-close (permit pane termination)",
		)
		return output, nil
	}
	if opts.AllowPaneClose && opts.Relaunch {
		output.RobotResponse = NewErrorResponse(
			errors.New("--allow-pane-close cannot be combined with --relaunch"),
			ErrCodeInvalidFlag,
			"Use --respawn-shell --relaunch to replace a direct-exec agent and relaunch inside a persistent shell",
		)
		return output, nil
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, opts, "exit-cli")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	if err := validateLifecycleExpectedPane(opts, targets); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Inspect the current pane before starting a new lifecycle operation; no agent was changed")
		return output, nil
	}
	lineages, rootCmds, err := validateExitCLITargets(ctx, opts, targets)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Select agents running under a persistent interactive shell, or pass --respawn-shell to replace a direct-exec agent with a fresh shell in the same cwd (or --allow-pane-close to permit pane termination); no exit keys were sent")
		return output, nil
	}
	var launchPlan restartLaunchPlan
	if opts.Relaunch {
		var err error
		launchPlan, err = prepareLifecycleRelaunch(ctx, opts.Session, targets, multiWindow)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Fix the saved launch specification or its dependencies before retrying; no agent was exited")
			return output, nil
		}
	}
	exitPolicy := "refuse"
	if opts.RespawnShell {
		exitPolicy = "respawn_shell"
	} else if opts.AllowPaneClose {
		exitPolicy = "allow_close"
	}
	for _, pane := range targets {
		started := time.Now()
		lineage := lineages[pane.ID]
		rootCmd := rootCmds[pane.ID]
		childPIDs := collectProcessTree(pane.PID, lifecycleChildPIDLimit, 2)
		result := LifecyclePaneResult{
			Pane:        paneTargetKey(pane, multiWindow),
			Target:      pane.ID,
			AgentType:   restartPaneAgentType(pane),
			LineageMode: lineage,
			ShellPID:    pane.PID,
		}
		relaunchTarget := pane
		if lineage == "direct_exec" {
			var canRelaunch bool
			relaunchTarget, canRelaunch = exitDirectExecPane(ctx, opts, pane, &result)
			if opts.Relaunch && canRelaunch && result.Exited && result.ShellPreserved {
				relaunchAgentCLI(ctx, opts.Session, relaunchTarget, launchPlan, &result)
			}
			slog.Info("[robot.exit-cli] completed pane lifecycle",
				"session", opts.Session,
				"pane_id", pane.ID,
				"pane_pid", pane.PID,
				"root_command", rootCmd,
				"child_pids", childPIDs,
				"lineage_mode", lineage,
				"exit_policy", exitPolicy,
				"status", result.Status,
				"elapsed_ms", time.Since(started).Milliseconds(),
			)
			output.Results = append(output.Results, result)
			continue
		}
		current, verified := verifyLifecycleShell(ctx, opts.Session, pane, &result)
		if !verified {
			result.Status = "withheld"
			output.Results = append(output.Results, result)
			continue
		}
		if current.AgentCLIDead() {
			result.Exited = true
			result.Status = "exited"
			result.Detail = "agent CLI already exited; pane is at a bare shell"
		} else {
			exited := false
			var exitErr error
			for attempt := 0; attempt < 2 && !exited; attempt++ {
				if attempt > 0 {
					current, verified = verifyLifecycleShell(ctx, opts.Session, pane, &result)
					if !verified {
						break
					}
					if current.AgentCLIDead() {
						break
					}
				}
				if exitErr = sendExitChoreography(ctx, current); exitErr != nil {
					break
				}
				_, exited = waitForBareShell(ctx, opts.Session, pane.ID, lifecycleExitTimeout)
			}
			refreshed, lookup := refreshLifecyclePane(ctx, opts.Session, pane.ID)
			result.ShellPreserved = false
			result.VerificationFailed = lookup == paneLookupFailed
			switch lookup {
			case paneLookupFailed:
				result.Detail = joinLifecycleDetail(result.Detail,
					"could not re-read the pane after exit; shell_preserved is unverified, not false")
			case paneAbsent:
				result.Detail = joinLifecycleDetail(result.Detail,
					"pane is gone; no further exit or kill attempt was made")
			case paneFound:
				if refreshed.Dead {
					result.Detail = joinLifecycleDetail(result.Detail,
						"pane process exited and tmux retained a dead pane; no live shell remains")
				} else if !lifecyclePaneIdentityMatches(pane, refreshed) {
					result.Detail = joinLifecycleDetail(result.Detail,
						"pane identity changed during exit; no action was taken against the replacement")
				} else if err := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, refreshed); err != nil {
					result.Detail = joinLifecycleDetail(result.Detail, err.Error())
				} else {
					result.ShellPreserved = true
					result.Exited = refreshed.AgentCLIDead()
					if !result.Exited && exitErr == nil {
						result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf(
							"agent CLI still in foreground (%s) after graceful exit attempts; inspect the pane before using --robot-kill-agent", refreshed.Command))
					}
				}
			}
			// A guarded follow-up key can be refused because the previous key
			// already exited the agent. Fresh shell evidence is success, not an
			// error that tells the caller to escalate an already-ended process.
			if exitErr != nil && !result.Exited {
				result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("exit keystrokes stopped: %v", exitErr))
			}
			if result.Exited && result.ShellPreserved {
				result.Status = "exited"
			} else {
				result.Status = "failed"
			}
		}
		if opts.Relaunch && result.Exited && result.ShellPreserved {
			relaunchAgentCLI(ctx, opts.Session, relaunchTarget, launchPlan, &result)
		}
		slog.Info("[robot.exit-cli] completed pane lifecycle",
			"session", opts.Session,
			"pane_id", pane.ID,
			"pane_pid", pane.PID,
			"root_command", rootCmd,
			"child_pids", childPIDs,
			"lineage_mode", lineage,
			"exit_policy", exitPolicy,
			"status", result.Status,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		output.Results = append(output.Results, result)
	}
	for _, result := range output.Results {
		paneOK := result.Exited && (result.ShellPreserved || (opts.AllowPaneClose && result.PaneClosed))
		if !paneOK || (opts.Relaunch && !result.Relaunched) {
			output.Success = false
			break
		}
	}
	return output, nil
}

// GetKillAgent SIGTERM-then-SIGKILLs the agent process tree under each target
// pane's shell while preserving the pane and the shell itself.
func GetKillAgent(ctx context.Context, opts LifecycleOptions) (*KillAgentOutput, error) {
	output := &KillAgentOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Results:       []LifecyclePaneResult{},
	}
	if err := requireLocalAgentProcessOperations(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, remoteAgentProcessOperationsHint)
		return output, nil
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, opts, "kill-agent")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	if err := validateLifecycleExpectedPane(opts, targets); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Inspect the current pane before starting a new lifecycle operation; no agent was changed")
		return output, nil
	}
	if err := validateLifecycleShells(ctx, targets); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Select agents running under a persistent interactive shell; no process was signaled")
		return output, nil
	}
	var launchPlan restartLaunchPlan
	if opts.Relaunch {
		var err error
		launchPlan, err = prepareLifecycleRelaunch(ctx, opts.Session, targets, multiWindow)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Fix the saved launch specification or its dependencies before retrying; no agent was killed")
			return output, nil
		}
	}
	for _, pane := range targets {
		result := LifecyclePaneResult{
			Pane:      paneTargetKey(pane, multiWindow),
			Target:    pane.ID,
			AgentType: restartPaneAgentType(pane),
			ShellPID:  pane.PID,
		}
		if _, verified := verifyLifecycleShell(ctx, opts.Session, pane, &result); !verified {
			output.Results = append(output.Results, result)
			continue
		}
		agentPIDs := collectProcessTree(pane.PID, lifecycleChildPIDLimit, 5)
		sort.Ints(agentPIDs)
		result.AgentPIDs = agentPIDs
		if len(agentPIDs) == 0 {
			result.Exited = true
			result.ShellPreserved = true
			result.Detail = "no agent process found under the pane shell; nothing to kill"
		} else {
			result.Killed = killProcessesGracefully(ctx, agentPIDs)
			var survivors []int
			for _, pid := range agentPIDs {
				if process.IsAlive(pid) {
					survivors = append(survivors, pid)
				}
			}
			result.Exited = len(survivors) == 0
			if len(survivors) > 0 {
				result.Detail = joinLifecycleDetail(result.Detail,
					fmt.Sprintf("processes survived SIGKILL: %v", survivors))
			}
			refreshed, lookup := refreshLifecyclePane(ctx, opts.Session, pane.ID)
			result.ShellPreserved = lookup == paneFound && lifecyclePaneIdentityMatches(pane, refreshed) &&
				tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, refreshed) == nil
			switch {
			case lookup == paneLookupFailed:
				result.VerificationFailed = true
				result.Detail = joinLifecycleDetail(result.Detail,
					"could not re-read the pane after kill; shell_preserved is unverified, not false")
			case lookup == paneFound && refreshed.PID != pane.PID:
				result.Detail = joinLifecycleDetail(result.Detail,
					fmt.Sprintf("pane shell PID changed from %d to %d", pane.PID, refreshed.PID))
			}
		}
		if opts.Relaunch && result.Exited && result.ShellPreserved {
			relaunchAgentCLI(ctx, opts.Session, pane, launchPlan, &result)
		}
		output.Results = append(output.Results, result)
	}
	for _, result := range output.Results {
		if !result.Exited || !result.ShellPreserved || (opts.Relaunch && !result.Relaunched) {
			output.Success = false
			break
		}
	}
	return output, nil
}

// collectProcessTree gathers the shell's descendant PIDs breadth-first
// (excluding the shell itself) so a kill covers helper subprocesses the
// agent spawned, not just the CLI root. Depth- and count-limited.
func collectProcessTree(rootPID, perNodeLimit, maxDepth int) []int {
	if rootPID <= 0 {
		return nil
	}
	var tree []int
	seen := map[int]bool{rootPID: true}
	frontier := []int{rootPID}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []int
		for _, pid := range frontier {
			for _, child := range process.GetChildPIDs(pid, perNodeLimit) {
				if child <= 0 || seen[child] {
					continue
				}
				seen[child] = true
				tree = append(tree, child)
				next = append(next, child)
			}
		}
		frontier = next
	}
	return tree
}

// killProcessesGracefully SIGTERMs each PID, waits for exit, and escalates to
// SIGKILL for survivors. Returns the PIDs that actually died.
func killProcessesGracefully(ctx context.Context, pids []int) []int {
	for _, pid := range pids {
		signalTerm(pid)
	}
	waitForProcessExit(ctx, pids, lifecycleTermGrace)
	escalated := false
	for _, pid := range pids {
		if process.IsAlive(pid) {
			signalKill(pid)
			escalated = true
		}
	}
	if escalated {
		waitForProcessExit(ctx, pids, lifecycleKillGrace)
	}
	killed := make([]int, 0, len(pids))
	for _, pid := range pids {
		if !process.IsAlive(pid) {
			killed = append(killed, pid)
		}
	}
	return killed
}

func waitForProcessExit(ctx context.Context, pids []int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		anyAlive := false
		for _, pid := range pids {
			if process.IsAlive(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive || time.Now().After(deadline) || ctx.Err() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// KillPaneOptions configures --robot-kill-pane (ntm-34jt): remove specific
// panes from a session, completing the lifecycle trio (exit-cli exits the
// CLI, kill-agent kills the process, kill-pane removes the pane itself).
type KillPaneOptions struct {
	Session string
	Panes   []string // required: explicit selectors; no default-all for a destructive verb
	Force   bool     // allow removing the user pane
}

// RemovedPane records the identity of a pane that was removed.
type RemovedPane struct {
	Pane      string `json:"pane"`
	Target    string `json:"target"`
	Title     string `json:"title,omitempty"`
	AgentType string `json:"agent_type"`
}

// KillPaneOutput is the structured output for --robot-kill-pane.
type KillPaneOutput struct {
	RobotResponse
	Session        string        `json:"session"`
	Removed        []RemovedPane `json:"removed"`
	Failed         []RemovedPane `json:"failed,omitempty"`
	RemainingPanes int           `json:"remaining_panes"`
}

// GetKillPane removes explicitly selected panes, leaving the session and
// sibling panes untouched (recovery-ladder Rung 6).
func GetKillPane(ctx context.Context, opts KillPaneOptions) (*KillPaneOutput, error) {
	output := &KillPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Removed:       []RemovedPane{},
	}
	session := strings.TrimSpace(opts.Session)
	if session == "" {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("session name required"), ErrCodeInvalidFlag,
			"Use --robot-kill-pane=SESSION --panes=SELECTOR")
		return output, nil
	}
	if len(opts.Panes) == 0 {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("--panes is required"), ErrCodeInvalidFlag,
			"Pane removal is destructive; select targets explicitly with --panes=N, W.P, or %N")
		return output, nil
	}
	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return output, nil
	}
	if !exists {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("session '%s' not found", session), ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions")
		return output, nil
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return output, nil
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	targets, err := tmux.ResolvePaneSelectors(panes, opts.Panes, false)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Use --panes with N, W.P, or %N selectors; --robot-pane-address=SESSION lists them")
		return output, nil
	}
	targets = dedupePanesByID(targets)
	if len(targets) >= len(panes) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("selection removes every pane (%d of %d)", len(targets), len(panes)),
			ErrCodeInvalidFlag,
			"Use 'ntm kill SESSION' to remove the whole session",
		)
		return output, nil
	}
	for _, pane := range targets {
		if restartPaneAgentType(pane) == "user" && !opts.Force {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("pane %s is the user pane; refusing without --force", paneTargetKey(pane, multiWindow)),
				ErrCodeInvalidFlag,
				"Add --force to remove the user pane deliberately",
			)
			return output, nil
		}
	}
	for _, pane := range tmux.SortPanesByTopology(targets) {
		identity := RemovedPane{
			Pane:      paneTargetKey(pane, multiWindow),
			Target:    pane.ID,
			Title:     pane.Title,
			AgentType: restartPaneAgentType(pane),
		}
		if err := tmux.KillPaneContext(ctx, pane.ID); err != nil {
			output.Failed = append(output.Failed, identity)
			output.Success = false
			continue
		}
		// Only a SUCCESSFUL listing that still contains the pane proves the
		// kill failed. A failed listing proves nothing, and treating it as
		// "still there" reported a successful kill-pane as a failure.
		if _, lookup := refreshLifecyclePane(ctx, session, pane.ID); lookup != paneAbsent {
			output.Failed = append(output.Failed, identity)
			output.Success = false
			continue
		}
		output.Removed = append(output.Removed, identity)
	}
	if remaining, err := tmux.GetPanesContext(ctx, session); err == nil {
		output.RemainingPanes = len(remaining)
	}
	if !output.Success && output.Error == "" {
		output.Error = "one or more panes could not be removed"
		output.ErrorCode = ErrCodeInternalError
		output.Hint = "Inspect failed entries; --robot-pane-address=SESSION shows the surviving topology"
	}
	return output, nil
}

// PrintKillPane removes selected panes and prints structured output.
func PrintKillPane(ctx context.Context, opts KillPaneOptions) error {
	output, err := GetKillPane(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot kill-pane failed")
}

// PrintExitCLI runs the graceful exit verb and prints structured output.
func PrintExitCLI(ctx context.Context, opts LifecycleOptions) error {
	output, err := GetExitCLI(ctx, opts)
	if err != nil {
		return err
	}
	if !output.Success && output.Error == "" {
		output.Error = "one or more panes did not complete the requested lifecycle transition"
		output.ErrorCode = ErrCodeInternalError
		output.Hint = "Inspect per-pane results; escalate stuck panes with --robot-kill-agent"
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot exit-cli failed")
}

// PrintKillAgent runs the hard-kill verb and prints structured output.
func PrintKillAgent(ctx context.Context, opts LifecycleOptions) error {
	output, err := GetKillAgent(ctx, opts)
	if err != nil {
		return err
	}
	if !output.Success && output.Error == "" {
		output.Error = "one or more panes did not complete the requested lifecycle transition"
		output.ErrorCode = ErrCodeInternalError
		output.Hint = "Inspect per-pane results; --robot-restart-pane replaces the pane if the shell is wedged"
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot kill-agent failed")
}
