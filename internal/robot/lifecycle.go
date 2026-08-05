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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// LifecycleOptions configures --robot-exit-cli / --robot-kill-agent.
type LifecycleOptions struct {
	Session  string
	Panes    []string // pane selectors (N, W.P, %N); empty = all agent panes
	Relaunch bool     // relaunch the agent CLI after exit/kill and verify
}

// LifecyclePaneResult is per-pane structured evidence for a lifecycle verb.
type LifecyclePaneResult struct {
	Pane           string `json:"pane"`
	Target         string `json:"target"`
	AgentType      string `json:"agent_type"`
	ShellPID       int    `json:"shell_pid,omitempty"`
	AgentPIDs      []int  `json:"agent_pids,omitempty"`
	Exited         bool   `json:"exited"`
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
// timing window encapsulated.
func sendExitChoreography(ctx context.Context, paneID string) error {
	if err := tmux.DefaultClient.SendInterrupt(paneID); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(lifecycleDoubleTapGap):
	}
	return tmux.DefaultClient.SendInterrupt(paneID)
}

// relaunchAgentCLI sends the configured launch command into the pane's shell
// and verifies the foreground process leaves the bare shell.
func relaunchAgentCLI(ctx context.Context, cfg *config.Config, session string, pane tmux.Pane, result *LifecyclePaneResult) {
	resolvedType := restartPaneAgentType(pane)
	launchCmd := restartAgentLaunchCommand(cfg, resolvedType, pane.Variant)
	if strings.TrimSpace(launchCmd) == "" {
		result.Detail = joinLifecycleDetail(result.Detail, "relaunch skipped: no launch command for agent type "+resolvedType)
		return
	}
	if err := tmux.SendKeysContext(ctx, pane.ID, launchCmd, true); err != nil {
		result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("relaunch send failed: %v", err))
		return
	}
	deadline := time.Now().Add(lifecycleRelaunchBoot)
	for {
		refreshed, lookup := refreshLifecyclePane(ctx, session, pane.ID)
		if lookup == paneFound && !refreshed.AgentCLIDead() {
			result.Relaunched = true
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			result.Detail = joinLifecycleDetail(result.Detail,
				fmt.Sprintf("relaunch sent (%s) but the agent CLI did not appear within %s", launchCmd, lifecycleRelaunchBoot))
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

// GetExitCLI performs the graceful agent-CLI exit choreography on each target
// pane, verifying via the pane's foreground command instead of assuming.
func GetExitCLI(ctx context.Context, opts LifecycleOptions) (*ExitCLIOutput, error) {
	output := &ExitCLIOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Results:       []LifecyclePaneResult{},
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, opts, "exit-cli")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	var cfg *config.Config
	if opts.Relaunch {
		cfg, _ = config.Load(config.DefaultPath())
	}
	for _, pane := range targets {
		result := LifecyclePaneResult{
			Pane:      paneTargetKey(pane, multiWindow),
			Target:    pane.ID,
			AgentType: restartPaneAgentType(pane),
			ShellPID:  pane.PID,
		}
		if pane.AgentCLIDead() {
			result.Exited = true
			result.ShellPreserved = true
			result.Detail = "agent CLI already exited; pane is at a bare shell"
		} else {
			exited := false
			for attempt := 0; attempt < 2 && !exited; attempt++ {
				if err := sendExitChoreography(ctx, pane.ID); err != nil {
					result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf("exit keystrokes failed: %v", err))
					break
				}
				_, exited = waitForBareShell(ctx, opts.Session, pane.ID, lifecycleExitTimeout)
			}
			result.Exited = exited
			refreshed, lookup := refreshLifecyclePane(ctx, opts.Session, pane.ID)
			result.ShellPreserved = lookup == paneFound
			switch lookup {
			case paneLookupFailed:
				// Nothing was proven either way; say so instead of implying
				// the pane was destroyed.
				result.VerificationFailed = true
				result.Detail = joinLifecycleDetail(result.Detail,
					"could not re-read the pane after exit; shell_preserved is unverified, not false")
			case paneFound:
				if !exited {
					result.Detail = joinLifecycleDetail(result.Detail, fmt.Sprintf(
						"agent CLI still in foreground (%s) after two graceful exit attempts; escalate with --robot-kill-agent", refreshed.Command))
				}
			}
		}
		if opts.Relaunch && result.Exited && result.ShellPreserved {
			relaunchAgentCLI(ctx, cfg, opts.Session, pane, &result)
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

// GetKillAgent SIGTERM-then-SIGKILLs the agent process tree under each target
// pane's shell while preserving the pane and the shell itself.
func GetKillAgent(ctx context.Context, opts LifecycleOptions) (*KillAgentOutput, error) {
	output := &KillAgentOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Results:       []LifecyclePaneResult{},
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, opts, "kill-agent")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	var cfg *config.Config
	if opts.Relaunch {
		cfg, _ = config.Load(config.DefaultPath())
	}
	for _, pane := range targets {
		result := LifecyclePaneResult{
			Pane:      paneTargetKey(pane, multiWindow),
			Target:    pane.ID,
			AgentType: restartPaneAgentType(pane),
			ShellPID:  pane.PID,
		}
		if pane.PID <= 0 {
			result.Detail = "pane shell PID unavailable; cannot locate the agent process tree"
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
			result.ShellPreserved = lookup == paneFound && refreshed.PID == pane.PID
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
			relaunchAgentCLI(ctx, cfg, opts.Session, pane, &result)
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
