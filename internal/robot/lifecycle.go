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
	// ExpectedProcesses carries the preceding in-process operation's observed
	// lifetimes into an escalation. It cannot authorize a replacement agent.
	ExpectedProcesses *LifecycleProcessIdentity
}

// LifecyclePaneResult is per-pane structured evidence for a lifecycle verb.
type LifecyclePaneResult struct {
	Pane            string                    `json:"pane"`
	Target          string                    `json:"target"`
	AgentType       string                    `json:"agent_type"`
	Status          string                    `json:"status,omitempty"`
	LineageMode     string                    `json:"lineage_mode,omitempty"`
	ShellPID        int                       `json:"shell_pid,omitempty"`
	AgentPIDs       []int                     `json:"agent_pids,omitempty"`
	PaneState       string                    `json:"pane_state"`
	ProcessState    string                    `json:"process_state"`
	Exited          bool                      `json:"exited"`
	PaneClosed      bool                      `json:"pane_closed,omitempty"`
	Killed          []int                     `json:"killed,omitempty"`
	ShellPreserved  bool                      `json:"shell_preserved"`
	Relaunched      bool                      `json:"relaunched,omitempty"`
	Detail          string                    `json:"detail,omitempty"`
	Refused         bool                      `json:"refused,omitempty"`
	ProcessIdentity *LifecycleProcessIdentity `json:"-"`

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
	lifecycleDoubleTapGap     = 200 * time.Millisecond
	lifecycleExitTimeout      = 6 * time.Second
	lifecyclePollInterval     = 250 * time.Millisecond
	lifecycleTermGrace        = 2 * time.Second
	lifecycleKillGrace        = 1 * time.Second
	lifecycleRelaunchBoot     = 10 * time.Second
	lifecycleReconcileTimeout = 2 * time.Second
	lifecycleChildPIDLimit    = 16
)

// Pane presence and the original process's lifetime are independent evidence.
// A retained dead pane is present, but has no preserved interactive shell.
const (
	lifecyclePanePresent    = "present"
	lifecyclePaneMissing    = "missing"
	lifecyclePaneUnknown    = "unknown"
	lifecycleProcessRunning = "running"
	lifecycleProcessExited  = "exited"
	lifecycleProcessUnknown = "unknown"
)

// LifecycleProcessIdentity is an opaque continuation for compound callers.
// Its observations are not supplied by robot JSON or reconstructed from PIDs.
type LifecycleProcessIdentity struct {
	root   process.SnapshotProcess
	agents map[int]process.SnapshotProcess
}

func lifecycleProcessLive(p process.SnapshotProcess) bool {
	return p.PID > 0 && p.State != "" && !strings.ContainsAny(p.State[:1], "ZXx")
}

func sameLifecycleProcess(original, current process.SnapshotProcess) bool {
	return original.PID > 0 && original.PID == current.PID && original.Started != "" && original.Started == current.Started
}

// Use one complete, error-aware process table rather than treating failed
// child queries as an empty tree. Include helpers and stopped/background jobs.
func lifecycleDescendants(snapshot map[int]process.SnapshotProcess, root int) map[int]process.SnapshotProcess {
	children := make(map[int][]int)
	for pid, p := range snapshot {
		children[p.PPID] = append(children[p.PPID], pid)
	}
	result := make(map[int]process.SnapshotProcess)
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, pid := range children[parent] {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			queue = append(queue, pid)
			if p := snapshot[pid]; lifecycleProcessLive(p) {
				result[pid] = p
			}
		}
	}
	return result
}

func captureLifecycleProcesses(ctx context.Context, pane tmux.Pane, expected *LifecycleProcessIdentity) (*LifecycleProcessIdentity, error) {
	snapshot, err := process.SnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	root := snapshot[pane.PID]
	if !lifecycleProcessLive(root) || !tmux.PaneCommandIsShell(root.Command) {
		return nil, errors.New("original persistent shell could not be observed")
	}
	agents := lifecycleDescendants(snapshot, pane.PID)
	if expected != nil {
		if !sameLifecycleProcess(expected.root, root) {
			return nil, errors.New("shell lifetime changed since the preceding lifecycle action")
		}
		for pid := range agents {
			if !lifecycleProcessOwned(snapshot, pid, root.PID, expected.agents) {
				return nil, errors.New("agent lifetime changed since the preceding lifecycle action; replacement will not be signaled")
			}
		}
		// A captured helper may outlive and be reparented from its agent.
		// Its original lifetime is still part of a bound escalation.
		for pid, previous := range expected.agents {
			if p := snapshot[pid]; sameLifecycleProcess(previous, p) && lifecycleProcessLive(p) {
				agents[pid] = p
			}
		}
	}
	if len(agents) == 0 && !tmux.PaneCommandIsShell(pane.Command) {
		return nil, errors.New("foreground agent has no observable process beneath the shell")
	}
	return &LifecycleProcessIdentity{root: root, agents: agents}, nil
}

func lifecycleProcessIDs(processes map[int]process.SnapshotProcess) []int {
	pids := make([]int, 0, len(processes))
	for pid := range processes {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

func observeLifecycleProcesses(snapshot map[int]process.SnapshotProcess, original map[int]process.SnapshotProcess) string {
	for pid, previous := range original {
		if current := snapshot[pid]; sameLifecycleProcess(previous, current) && lifecycleProcessLive(current) {
			return lifecycleProcessRunning
		}
	}
	return lifecycleProcessExited
}

type lifecycleObservation struct {
	pane               tmux.Pane
	snapshot           map[int]process.SnapshotProcess
	paneState          string
	processState       string
	shellPreserved     bool
	idle               bool
	verificationFailed bool
	detail             string
}

// Read process and pane evidence independently. A missing pane cannot prove
// its former process exited, and a failed ps/tmux read cannot prove absence.
func observeLifecycle(ctx context.Context, session string, pane tmux.Pane, original *LifecycleProcessIdentity) lifecycleObservation {
	observation := lifecycleObservation{paneState: lifecyclePaneUnknown, processState: lifecycleProcessUnknown}
	snapshot, processErr := process.SnapshotContext(ctx)
	observation.snapshot = snapshot
	if processErr == nil {
		observation.processState = observeLifecycleProcesses(snapshot, original.agents)
	} else {
		observation.verificationFailed = true
		observation.detail = "original process state could not be observed"
	}
	refreshed, lookup := refreshLifecyclePane(ctx, session, pane.ID)
	observation.pane = refreshed
	switch lookup {
	case paneLookupFailed:
		observation.verificationFailed = true
		observation.detail = joinLifecycleDetail(observation.detail, "pane observation failed; preservation is unverified")
		return observation
	case paneAbsent:
		observation.paneState = lifecyclePaneMissing
		observation.detail = joinLifecycleDetail(observation.detail, "pane is gone; no further input or signals are permitted")
		return observation
	}
	observation.paneState = lifecyclePanePresent
	if !lifecyclePaneIdentityMatches(pane, refreshed) {
		observation.detail = joinLifecycleDetail(observation.detail, "original pane is dead or its identity changed")
		return observation
	}
	if processErr != nil {
		return observation
	}
	root := snapshot[pane.PID]
	if !sameLifecycleProcess(original.root, root) || !lifecycleProcessLive(root) || !tmux.PaneCommandIsShell(root.Command) {
		observation.detail = "original persistent shell no longer exists"
		return observation
	}
	if err := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, refreshed); err != nil {
		observation.verificationFailed = true
		observation.detail = joinLifecycleDetail(observation.detail, "persistent shell could not be verified: "+err.Error())
		return observation
	}
	observation.shellPreserved = true
	observation.idle = tmux.PaneCommandIsShell(refreshed.Command) && len(lifecycleDescendants(snapshot, pane.PID)) == 0 &&
		root.PGID > 0 && root.PGID == root.TPGID
	return observation
}

func applyLifecycleObservation(result *LifecyclePaneResult, observation lifecycleObservation) {
	result.PaneState = observation.paneState
	// Once the original lifetime is confirmed ended it cannot return. A later
	// relaunch/readback failure must not turn that evidence into "still active".
	if result.ProcessState != lifecycleProcessExited {
		result.ProcessState = observation.processState
	}
	result.Exited = result.ProcessState == lifecycleProcessExited
	result.ShellPreserved = observation.shellPreserved
	result.VerificationFailed = observation.verificationFailed
	result.Detail = joinLifecycleDetail(result.Detail, observation.detail)
}

// Wait only for observations. Cancellation stops all mutations; a separate,
// bounded read-only context may subsequently reconcile a partially sent exit.
func waitForLifecycleExit(ctx context.Context, session string, pane tmux.Pane, original *LifecycleProcessIdentity, timeout time.Duration) lifecycleObservation {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var observation lifecycleObservation
	confirmedExit := false
	for {
		// A composite observation needs several bounded reads. Do not start
		// one in the final polling interval only to invalidate a completed
		// observation with our own settling deadline. Genuine read failures
		// still replace the evidence with unknown state below.
		if deadline, ok := ctx.Deadline(); ok && observation.paneState != "" && time.Until(deadline) <= lifecyclePollInterval {
			return observation
		}
		observation = observeLifecycle(ctx, session, pane, original)
		confirmedExit = confirmedExit || observation.processState == lifecycleProcessExited
		if confirmedExit {
			observation.processState = lifecycleProcessExited
		}
		if observation.paneState == lifecyclePaneMissing ||
			(observation.paneState == lifecyclePanePresent && !observation.shellPreserved && !observation.verificationFailed) ||
			(observation.processState == lifecycleProcessExited && observation.idle) {
			return observation
		}
		select {
		case <-ctx.Done():
			return observation
		case <-time.After(lifecyclePollInterval):
		}
	}
}

// A replacement foreground job must never receive a delayed second key or a
// retry. New helper processes are eligible only beneath an original live
// process, and every member of the foreground group must have that lineage.
func lifecycleOriginalForeground(observation lifecycleObservation, original *LifecycleProcessIdentity) bool {
	root := observation.snapshot[original.root.PID]
	if root.PGID <= 0 || root.TPGID <= 0 || root.PGID == root.TPGID {
		return false
	}
	descendants := lifecycleDescendants(observation.snapshot, root.PID)
	for pid, p := range observation.snapshot {
		if p.PGID == root.TPGID && lifecycleProcessLive(p) {
			if _, owned := descendants[pid]; !owned {
				return false
			}
		}
	}
	found := false
	for pid, p := range descendants {
		if p.PGID != root.TPGID {
			continue
		}
		if !lifecycleProcessOwned(observation.snapshot, pid, root.PID, original.agents) {
			return false
		}
		found = true
	}
	return found
}

func lifecycleHasForegroundDescendant(observation lifecycleObservation) bool {
	root := observation.snapshot[observation.pane.PID]
	if root.PGID <= 0 || root.TPGID <= 0 || root.PGID == root.TPGID {
		return false
	}
	for _, p := range lifecycleDescendants(observation.snapshot, root.PID) {
		if p.PGID == root.TPGID {
			return true
		}
	}
	return false
}

func lifecycleProcessOwned(snapshot map[int]process.SnapshotProcess, pid, root int, original map[int]process.SnapshotProcess) bool {
	seen := make(map[int]bool)
	for pid > 0 && pid != root && !seen[pid] {
		seen[pid] = true
		current := snapshot[pid]
		if previous, ok := original[pid]; ok && sameLifecycleProcess(previous, current) && lifecycleProcessLive(current) {
			return true
		}
		pid = current.PPID
	}
	return false
}

func checkLifecycleInput(ctx context.Context, session string, pane tmux.Pane, original *LifecycleProcessIdentity) error {
	observation := observeLifecycle(ctx, session, pane, original)
	if observation.verificationFailed || !observation.shellPreserved || observation.processState != lifecycleProcessRunning ||
		!lifecycleOriginalForeground(observation, original) {
		return errors.New("exit input withheld: the original live agent no longer has verified foreground ownership")
	}
	return ctx.Err()
}

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
			select {
			case <-ctx.Done():
				return tmux.Pane{}, paneLookupFailed
			case <-time.After(lifecyclePollInterval):
			}
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

// sendExitChoreography sends the graceful double Ctrl+C with the per-CLI
// timing window encapsulated. omp gets its own sequence: its Ctrl+C clears
// the draft rather than interrupting, so a double tap with text in the
// composer would never quit.
func sendExitChoreography(ctx context.Context, pane tmux.Pane, beforeInput func(context.Context) error) error {
	// Every key is fenced in the tmux server. If an earlier key already
	// returned to the shell, a later Ctrl+D (omp) must not close that shell,
	// and a delayed second interrupt must not reach a replacement process.
	guardedCtx, err := tmux.WithPaneMutationGuard(ctx, pane.ID, pane.PID, beforeInput)
	if err != nil {
		return err
	}
	if tmux.AgentType(restartPaneAgentType(pane)).Canonical() == tmux.AgentOmp {
		return tmux.SendOmpExitContext(guardedCtx, pane.ID)
	}
	if err := tmux.DefaultClient.RunSilentContext(guardedCtx, "send-keys", "-t", tmux.ExactTarget(pane.ID), "C-c"); err != nil {
		return err
	}
	secondDeadline := time.Now().Add(300 * time.Millisecond)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(lifecycleDoubleTapGap):
	}
	// The ownership check counts toward the double-interrupt window. A slow
	// observation must stop the sequence, not send a late key to another job.
	secondCtx, cancel := context.WithDeadline(guardedCtx, secondDeadline)
	defer cancel()
	return tmux.DefaultClient.RunSilentContext(secondCtx, "send-keys", "-t", tmux.ExactTarget(pane.ID), "C-c")
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
	result.PaneState = lifecyclePaneUnknown
	if lookup == paneFound {
		result.PaneState = lifecyclePanePresent
	} else if lookup == paneAbsent {
		result.PaneState = lifecyclePaneMissing
	}
	result.VerificationFailed = lookup == paneLookupFailed
	if lookup != paneFound || !lifecyclePaneIdentityMatches(original, refreshed) {
		result.Detail = joinLifecycleDetail(result.Detail, "lifecycle action withheld: original live agent pane identity could not be verified")
		return refreshed, false
	}
	if err := tmux.DefaultClient.ValidatePanePersistentShellContext(ctx, refreshed); err != nil {
		result.VerificationFailed = true
		result.Detail = joinLifecycleDetail(result.Detail, "lifecycle action withheld: "+err.Error())
		return refreshed, false
	}
	result.ShellPreserved = true
	return refreshed, true
}

// verifyLifecycleRelaunchShell requires fresh evidence that the original pane
// shell is idle. A stale exit result must not allow typing into another agent.
func verifyLifecycleRelaunchShell(ctx context.Context, session string, pane tmux.Pane, original *LifecycleProcessIdentity, result *LifecyclePaneResult) bool {
	observation := observeLifecycle(ctx, session, pane, original)
	applyLifecycleObservation(result, observation)
	if ctx.Err() != nil || observation.verificationFailed || !observation.shellPreserved || !observation.idle ||
		observation.processState != lifecycleProcessExited {
		result.Refused = true
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: original persistent shell is not verified idle")
		return false
	}
	return true
}

// relaunchAgentCLI replays the preflighted command only after observing the
// original pane back at a shell, recording that command before delivery.
func relaunchAgentCLI(ctx context.Context, session string, pane tmux.Pane, original *LifecycleProcessIdentity, plan restartLaunchPlan, result *LifecyclePaneResult) {
	launchCmd, ok := plan.Commands[result.Pane]
	spec, hasSpec := plan.Specs[result.Pane]
	if !ok || !hasSpec || strings.TrimSpace(launchCmd) == "" {
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: preflighted launch specification is unavailable")
		return
	}
	if !verifyLifecycleRelaunchShell(ctx, session, pane, original, result) {
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
		if !verifyLifecycleRelaunchShell(ctx, session, pane, original, result) {
			return
		}
	}
	// Both a lost send response and a pane disappearing during boot require
	// fresh readback; never retain a pre-send shell_preserved:true by default.
	defer func() {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleReconcileTimeout)
		defer cancel()
		observation := observeLifecycle(readCtx, session, pane, original)
		applyLifecycleObservation(result, observation)
		if !observation.shellPreserved || observation.verificationFailed || !lifecycleHasForegroundDescendant(observation) {
			result.Relaunched = false
		}
	}()
	if err := tmux.DefaultClient.SetPaneLaunchSpecIfCurrentContext(ctx, pane.ID, pane.PID, spec); err != nil {
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("record relaunch specification failed: %v", err))
		return
	}
	if err := tmux.DefaultClient.SendKeysForAgentIfCurrentContext(ctx, pane.ID, launchCmd, true, tmux.AgentUser, pane.PID, true); err != nil {
		// A tmux command error can contain the command's private arguments.
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch command delivery failed")
		return
	}
	bootCtx, cancel := context.WithTimeout(ctx, lifecycleRelaunchBoot)
	defer cancel()
	for {
		observation := observeLifecycle(bootCtx, session, pane, original)
		applyLifecycleObservation(result, observation)
		if observation.paneState == lifecyclePaneMissing ||
			(observation.paneState == lifecyclePanePresent && !observation.shellPreserved && !observation.verificationFailed) {
			result.Detail = joinLifecycleDetail(result.Detail, "relaunch could not be confirmed in the original live pane")
			return
		}
		if observation.shellPreserved && !observation.verificationFailed &&
			strings.TrimSpace(observation.pane.Command) != "" && lifecycleHasForegroundDescendant(observation) {
			result.Relaunched = true
			return
		}
		select {
		case <-bootCtx.Done():
			result.Detail = joinLifecycleDetail(result.Detail,
				fmt.Sprintf("relaunch sent but the agent CLI did not appear within %s", lifecycleRelaunchBoot))
			return
		case <-time.After(lifecyclePollInterval):
		}
	}
}

func joinLifecycleDetail(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "; " + extra
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

func waitForBareShell(ctx context.Context, session, paneID string, timeout time.Duration) (tmux.Pane, bool) {
	deadline := time.Now().Add(timeout)
	var last tmux.Pane
	for {
		pane, lookup := refreshLifecyclePane(ctx, session, paneID)
		switch lookup {
		case paneAbsent:
			return last, false
		case paneLookupFailed:
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

func directExecExitInput(ctx context.Context, session string, original, current tmux.Pane) error {
	latest, lookup := refreshLifecyclePane(ctx, session, original.ID)
	if lookup != paneFound || !lifecyclePaneIdentityMatches(original, latest) || latest.PID != current.PID {
		return fmt.Errorf("pane %s identity changed before exit input", original.ID)
	}
	return nil
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
			exitErr = sendExitChoreography(ctx, refreshed, func(checkCtx context.Context) error {
				return directExecExitInput(checkCtx, opts.Session, original, refreshed)
			})
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
		exitErr = sendExitChoreography(ctx, refreshed, func(checkCtx context.Context) error {
			return directExecExitInput(checkCtx, opts.Session, original, refreshed)
		})
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

// GetExitCLI exits a shell-backed agent only after the original process
// lifetime is observed, and handles a direct-exec root separately so a
// missing persistent shell is not destroyed unless the caller opted in.
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
		var childPIDs []int
		if snap, snapErr := process.SnapshotContext(ctx); snapErr == nil {
			childPIDs = lifecycleProcessIDs(lifecycleDescendants(snap, pane.PID))
		}
		result := LifecyclePaneResult{
			Pane:         paneTargetKey(pane, multiWindow),
			Target:       pane.ID,
			AgentType:    restartPaneAgentType(pane),
			Status:       "",
			LineageMode:  lineage,
			ShellPID:     pane.PID,
			PaneState:    lifecyclePaneUnknown,
			ProcessState: lifecycleProcessUnknown,
		}
		relaunchTarget := pane
		if lineage == "direct_exec" {
			var canRelaunch bool
			relaunchTarget, canRelaunch = exitDirectExecPane(ctx, opts, pane, &result)
			if result.PaneClosed {
				result.PaneState = lifecyclePaneMissing
				result.ProcessState = lifecycleProcessExited
			} else if result.ShellPreserved {
				result.PaneState = lifecyclePanePresent
				if result.Exited {
					result.ProcessState = lifecycleProcessExited
				}
			}
			if opts.Relaunch && canRelaunch && result.Exited && result.ShellPreserved && !result.Refused && ctx.Err() == nil {
				shellIdentity, err := captureLifecycleProcesses(ctx, relaunchTarget, nil)
				if err != nil {
					result.Refused = true
					result.Detail = joinLifecycleDetail(result.Detail, "relaunch withheld: "+err.Error())
				} else {
					result.ProcessIdentity = shellIdentity
					relaunchAgentCLI(ctx, opts.Session, relaunchTarget, shellIdentity, launchPlan, &result)
				}
			}
		} else {
			current, verified := verifyLifecycleShell(ctx, opts.Session, pane, &result)
			if !verified {
				result.Status = "withheld"
				result.Refused = true
				output.Results = append(output.Results, result)
				continue
			}
			original, err := captureLifecycleProcesses(ctx, current, opts.ExpectedProcesses)
			if err != nil {
				result.Refused = true
				result.Status = "withheld"
				result.VerificationFailed = true
				result.Detail = joinLifecycleDetail(result.Detail, "exit withheld before input: "+err.Error())
				output.Results = append(output.Results, result)
				continue
			}
			result.AgentPIDs = lifecycleProcessIDs(original.agents)
			result.ProcessIdentity = original
			observation := observeLifecycle(ctx, opts.Session, pane, original)
			var exitErr error
			for attempt := 0; attempt < 2 && observation.processState == lifecycleProcessRunning &&
				observation.shellPreserved && !observation.verificationFailed && ctx.Err() == nil; attempt++ {
				exitErr = sendExitChoreography(ctx, pane, func(checkCtx context.Context) error {
					return checkLifecycleInput(checkCtx, opts.Session, pane, original)
				})
				if exitErr != nil {
					break
				}
				observation = waitForLifecycleExit(ctx, opts.Session, pane, original, lifecycleExitTimeout)
			}
			if observation.processState == lifecycleProcessExited {
				result.ProcessState = lifecycleProcessExited
			}
			if exitErr != nil || ctx.Err() != nil || observation.verificationFailed {
				// A failed/canceled send may have delivered its first key. This
				// context is strictly read-only and must never reach a relaunch.
				readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleReconcileTimeout)
				observation = waitForLifecycleExit(readCtx, opts.Session, pane, original, lifecycleReconcileTimeout)
				cancel()
			}
			applyLifecycleObservation(&result, observation)
			result.Refused = !observation.shellPreserved || observation.verificationFailed ||
				(result.Exited && !observation.idle) || (exitErr != nil && !result.Exited) || ctx.Err() != nil
			if result.Exited && observation.idle {
				// Cancellation prevents further mutation, but does not invalidate
				// a confirmed completed exit or imply that the caller should kill.
				result.Refused = false
				result.Status = "exited"
				if len(original.agents) == 0 {
					result.Detail = joinLifecycleDetail(result.Detail, "agent CLI already exited; pane is at a bare shell")
				}
			} else if result.Exited && result.ShellPreserved {
				result.Status = "exited"
				result.Detail = joinLifecycleDetail(result.Detail, "original process exited, but the shell is not idle; further input withheld")
			} else if result.ProcessState == lifecycleProcessRunning {
				result.Status = "failed"
				result.Detail = joinLifecycleDetail(result.Detail, "original agent process remains active")
			}
			if exitErr != nil && !result.Exited {
				result.Status = "failed"
				result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("exit keystrokes stopped: %v", exitErr))
			}
			if opts.Relaunch && result.Exited && result.ShellPreserved && !result.Refused && ctx.Err() == nil {
				relaunchAgentCLI(ctx, opts.Session, pane, original, launchPlan, &result)
			}
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
		if !result.Exited || result.Refused || (opts.Relaunch && !result.Relaunched) ||
			(!result.ShellPreserved && !(opts.AllowPaneClose && result.PaneClosed)) {
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
			Pane:         paneTargetKey(pane, multiWindow),
			Target:       pane.ID,
			AgentType:    restartPaneAgentType(pane),
			ShellPID:     pane.PID,
			PaneState:    lifecyclePaneUnknown,
			ProcessState: lifecycleProcessUnknown,
		}
		current, verified := verifyLifecycleShell(ctx, opts.Session, pane, &result)
		if !verified {
			result.Refused = true
			output.Results = append(output.Results, result)
			continue
		}
		original, err := captureLifecycleProcesses(ctx, current, opts.ExpectedProcesses)
		if err != nil {
			result.Refused = true
			result.VerificationFailed = true
			result.Detail = joinLifecycleDetail(result.Detail, "kill withheld before signals: "+err.Error())
			output.Results = append(output.Results, result)
			continue
		}
		result.AgentPIDs = lifecycleProcessIDs(original.agents)
		result.ProcessIdentity = original
		result.Killed, err = killProcessesGracefully(ctx, original.agents, func(checkCtx context.Context) error {
			observation := observeLifecycle(checkCtx, opts.Session, pane, original)
			if observation.verificationFailed || !observation.shellPreserved {
				return errors.New("kill withheld: original persistent shell is unverified")
			}
			// Originally captured helpers may be reparented when TERM stops
			// their parent. Lifetime checks still bind each later PID signal;
			// stopped/background jobs do not require foreground ownership.
			return checkCtx.Err()
		})
		if len(original.agents) > 0 && len(result.Killed) == len(original.agents) {
			result.ProcessState = lifecycleProcessExited
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleReconcileTimeout)
		observation := waitForLifecycleExit(readCtx, opts.Session, pane, original, lifecycleReconcileTimeout)
		cancel()
		applyLifecycleObservation(&result, observation)
		result.Refused = err != nil || observation.verificationFailed || !observation.shellPreserved ||
			(result.Exited && !observation.idle) || ctx.Err() != nil
		if err != nil {
			result.Detail = joinLifecycleDetail(result.Detail, "process signals stopped: "+err.Error())
		}
		if result.Exited && observation.idle {
			result.Refused = false
			if len(original.agents) == 0 {
				result.Detail = joinLifecycleDetail(result.Detail, "no agent process under the verified idle shell; nothing to kill")
			}
		} else if result.ProcessState == lifecycleProcessRunning {
			result.Detail = joinLifecycleDetail(result.Detail, "original agent process remains active")
		}
		if opts.Relaunch && result.Exited && result.ShellPreserved && !result.Refused && ctx.Err() == nil {
			relaunchAgentCLI(ctx, opts.Session, pane, original, launchPlan, &result)
		}
		output.Results = append(output.Results, result)
	}
	for _, result := range output.Results {
		if !result.Exited || !result.ShellPreserved || result.Refused || (opts.Relaunch && !result.Relaunched) {
			output.Success = false
			break
		}
	}
	return output, nil
}

// killProcessesGracefully SIGTERMs each PID, waits for exit, and escalates to
// SIGKILL only after a complete grace period and positive survivor evidence.
// Failed observations or cancellation stop signals, including escalation.
func killProcessesGracefully(ctx context.Context, original map[int]process.SnapshotProcess, beforeSignal func(context.Context) error) (killed []int, signalErr error) {
	confirmedExited := make(map[int]bool)
	rememberExits := func(snapshot map[int]process.SnapshotProcess) {
		for pid, previous := range original {
			if p := snapshot[pid]; !sameLifecycleProcess(previous, p) || !lifecycleProcessLive(p) {
				confirmedExited[pid] = true
			}
		}
	}
	defer func() {
		if len(original) == 0 {
			return
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleReconcileTimeout)
		defer cancel()
		snapshot, err := process.SnapshotContext(readCtx)
		if err != nil {
			signalErr = errors.Join(signalErr, err)
		} else {
			rememberExits(snapshot)
		}
		for _, pid := range lifecycleProcessIDs(original) {
			if confirmedExited[pid] {
				killed = append(killed, pid)
			}
		}
	}()
	for phase, grace := range []time.Duration{lifecycleTermGrace, lifecycleKillGrace} {
		for _, pid := range lifecycleProcessIDs(original) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if beforeSignal != nil {
				if err := beforeSignal(ctx); err != nil {
					return nil, err
				}
			}
			snapshot, err := process.SnapshotContext(ctx)
			if err != nil {
				return nil, err
			}
			rememberExits(snapshot)
			if p := snapshot[pid]; !sameLifecycleProcess(original[pid], p) || !lifecycleProcessLive(p) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if phase == 0 {
				signalTerm(pid)
			} else {
				signalKill(pid)
			}
		}
		snapshot, err := waitForProcessExit(ctx, original, grace)
		if err != nil {
			return nil, err
		}
		rememberExits(snapshot)
		if observeLifecycleProcesses(snapshot, original) == lifecycleProcessExited {
			return nil, nil
		}
	}
	return nil, nil
}

func waitForProcessExit(ctx context.Context, original map[int]process.SnapshotProcess, timeout time.Duration) (map[int]process.SnapshotProcess, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := process.SnapshotContext(ctx)
		if err != nil {
			return nil, err
		}
		if observeLifecycleProcesses(snapshot, original) == lifecycleProcessExited || !time.Now().Before(deadline) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(100*time.Millisecond, time.Until(deadline))):
		}
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
		output.Hint = "Inspect pane_state and process_state; unknown or refused outcomes require fresh observation before any retry or escalation"
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
		output.Hint = "Inspect pane_state and process_state; verify the original pane and process before choosing further recovery"
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot kill-agent failed")
}
