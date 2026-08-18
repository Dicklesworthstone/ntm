package cli

// guards_check.go — the REAL pre-commit reservation check behind the guard
// hook (bd-ws1-truth-safety-l5ddi.1). The installed fallback hook execs
// `ntm guards check --staged`, which queries Agent Mail for active exclusive
// file reservations overlapping the staged paths.
//
// Failure posture (per the bead, rev 3):
//   - Conflict            -> exit non-zero naming the holder + reservation.
//   - Agent Mail down     -> FAIL OPEN with a visible WARN *and* a
//     degraded-event row in the state DB (surfaced by `ntm doctor`,
//     `ntm guards status`, and the dashboard doctor endpoint), because a WARN
//     in commit scrollback is unobserved by construction.
//   - NTM_GUARD_STRICT=1  -> opt-in fail-closed: Agent Mail down blocks the
//     commit, naming the strict setting.
//
// A hard timeout (guardCheckTimeout) bounds the Agent Mail query so the hook
// can never hang a commit.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// guardCheckTimeout bounds the Agent Mail reservation query from the
// pre-commit hook. Two seconds, per the bead: the hook must never hang a
// commit waiting on a daemon.
const guardCheckTimeout = 2 * time.Second

// guardStrictEnv is the opt-in fail-closed switch: when set to a truthy value
// the guard blocks commits whenever the reservation check cannot complete.
const guardStrictEnv = "NTM_GUARD_STRICT"

// guardSelfAgentEnv optionally names the committing agent so its own
// exclusive reservations do not block its commit.
const guardSelfAgentEnv = "NTM_GUARD_AGENT"

// Degraded-event reasons (bd-2c0yh.2). Transport failures — the daemon is
// down, unreachable, or timing out — are a different fact from a HEALTHY
// Agent Mail answering with an application error (e.g. "project not found"
// for a repo never registered). Blurring them under one reason made the
// doctor light permanently red for unregistered repos.
const (
	guardReasonUnreachable = "agent-mail-unreachable"
	guardReasonAppError    = "agent-mail-error"
)

// guardDegradedRetention bounds how long degraded events are kept: recording
// a new event prunes rows older than this, so the ledger is self-clearing
// and can never become an all-time monotone counter (bd-2c0yh.2).
const guardDegradedRetention = 30 * 24 * time.Hour

// guardDegradedDoctorWindow is the reporting window for `ntm doctor`:
// only degraded runs inside this window turn the check yellow, so a stale
// incident stops warning once the window passes instead of forever.
const guardDegradedDoctorWindow = 7 * 24 * time.Hour

// guardTransportError reports whether the reservation-check failure was a
// transport-level failure (server unreachable / request timed out) rather
// than an application-level answer from a healthy server.
func guardTransportError(err error) bool {
	return agentmail.IsServerUnavailable(err) ||
		agentmail.IsTimeout(err) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}

func newGuardsCheckCmd() *cobra.Command {
	var staged bool
	var projectKey string

	cmd := &cobra.Command{
		Use:    "check",
		Short:  "Check staged files against Agent Mail file reservations",
		Hidden: true, // plumbing for the installed pre-commit hook
		Long: `Check files against active Agent Mail file reservations.

This is the command the NTM pre-commit guard hook runs. With --staged it
lists the files staged in the current git repository and fails when any of
them is covered by an active exclusive reservation held by another agent.

If Agent Mail is unreachable the check fails open (commit allowed) with a
visible WARN and a degraded-event row surfaced by 'ntm doctor'. Set
NTM_GUARD_STRICT=1 to fail closed instead.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !staged {
				return fmt.Errorf("guards check currently requires --staged")
			}
			return runGuardsCheckStaged(cmd.OutOrStdout(), cmd.ErrOrStderr(), projectKey)
		},
	}

	cmd.Flags().BoolVar(&staged, "staged", false, "Check the files staged for commit")
	cmd.Flags().StringVarP(&projectKey, "project-key", "p", "", "Agent Mail project key (defaults to the repository root)")

	return cmd
}

func runGuardsCheckStaged(stdout, stderr io.Writer, projectKey string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	repoPath, err := findGitRoot(cwd)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	if projectKey == "" {
		projectKey = repoPath
	}

	stagedPaths, err := stagedFiles(repoPath)
	if err != nil {
		return fmt.Errorf("listing staged files: %w", err)
	}
	if len(stagedPaths) == 0 {
		fmt.Fprintln(stdout, "[ntm-guard] Pre-commit check passed (no staged files)")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), guardCheckTimeout)
	defer cancel()

	client := agentmail.NewClient()
	conflicts, checkErr := client.CheckStagedReservations(ctx, projectKey, os.Getenv(guardSelfAgentEnv), stagedPaths)
	if checkErr != nil {
		if guardStrictMode() {
			fmt.Fprintf(stderr, "[ntm-guard] BLOCKED: Agent Mail reservation check failed and %s=1 (fail-closed): %v\n", guardStrictEnv, checkErr)
			return fmt.Errorf("commit blocked: Agent Mail unreachable and %s=1 requires a completed reservation check", guardStrictEnv)
		}
		// Fail open, but VISIBLY: WARN now, and a degraded-event row that
		// `ntm doctor` / the dashboard surface later. Transport failures and
		// application errors from a healthy server are recorded under
		// distinct reasons (bd-2c0yh.2) so doctor can tell "daemon down at
		// commit time" from "repo not registered with Agent Mail".
		reason := guardReasonAppError
		if guardTransportError(checkErr) {
			reason = guardReasonUnreachable
			fmt.Fprintf(stderr, "[ntm-guard] WARN: Agent Mail unreachable (%v) — allowing commit WITHOUT reservation check (degraded mode)\n", checkErr)
		} else {
			fmt.Fprintf(stderr, "[ntm-guard] WARN: Agent Mail answered with an error (%v) — allowing commit WITHOUT reservation check (degraded mode)\n", checkErr)
		}
		fmt.Fprintf(stderr, "[ntm-guard] WARN: degraded runs are recorded; see 'ntm doctor'. Set %s=1 to fail closed.\n", guardStrictEnv)
		recordGuardDegradedEvent(stderr, repoPath, projectKey, reason, checkErr)
		return nil
	}

	if len(conflicts) > 0 {
		for _, c := range conflicts {
			fmt.Fprintf(stderr, "[ntm-guard] CONFLICT: %s is reserved by %s (reservation #%d, pattern %q, expires %s)\n",
				c.Path, c.Holder, c.ReservationID, c.PathPattern, c.ExpiresTS.Format(time.RFC3339))
		}
		fmt.Fprintf(stderr, "[ntm-guard] Resolve with the holder or release the reservation (ntm mail), then retry the commit.\n")
		return fmt.Errorf("commit blocked: %d staged file(s) conflict with active Agent Mail file reservations", len(conflicts))
	}

	fmt.Fprintf(stdout, "[ntm-guard] Pre-commit check passed (%d staged file(s), no reservation conflicts)\n", len(stagedPaths))
	return nil
}

// guardStrictMode reports whether the opt-in fail-closed switch is set.
func guardStrictMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(guardStrictEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// stagedFiles lists the paths staged for commit, relative to the repo root.
// --no-renames decomposes staged renames into A+D so the OLD path of a
// `git mv` still hits the reservation predicate (with rename detection,
// --name-only emits only the destination and a reserved source path would
// escape the guard entirely). Output is NUL-delimited; entries are used
// verbatim (no trimming) so legal names with leading/trailing whitespace
// still match their reservations — only the terminating empty element is
// dropped.
func stagedFiles(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "diff", "--cached", "--name-only", "--no-renames", "--diff-filter=ACDM", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// recordGuardDegradedEvent appends a fail-open row to the state DB. Best
// effort: a bookkeeping failure must not block the commit, but it must not be
// silent either.
func recordGuardDegradedEvent(stderr io.Writer, repoPath, projectKey, reason string, cause error) {
	store, err := state.Open("")
	if err != nil {
		fmt.Fprintf(stderr, "[ntm-guard] WARN: could not record degraded event (state DB: %v)\n", err)
		return
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(); err != nil {
		fmt.Fprintf(stderr, "[ntm-guard] WARN: could not record degraded event (migrate: %v)\n", err)
		return
	}

	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	if err := store.RecordGuardDegradedEvent(&state.GuardDegradedEvent{
		RepoPath:   repoPath,
		ProjectKey: projectKey,
		Reason:     reason,
		Detail:     detail,
	}); err != nil {
		fmt.Fprintf(stderr, "[ntm-guard] WARN: could not record degraded event: %v\n", err)
	}
	// Self-limiting ledger (bd-2c0yh.2): drop rows past retention so the
	// ledger never becomes an all-time counter. Best effort; a prune failure
	// must not block the commit.
	_, _ = store.PruneGuardDegradedEvents(time.Now().UTC().Add(-guardDegradedRetention))
}

// guardDegradationCheck is the `ntm doctor` surface for degraded guard runs
// (fail-open events recorded by the hook while Agent Mail was unreachable).
func guardDegradationCheck() ConfigCheck {
	check := ConfigCheck{Name: "guard-hook degraded runs", Valid: true, Status: "ok"}

	store, err := state.Open("")
	if err != nil {
		check.Status = "warning"
		check.Message = fmt.Sprintf("state DB unavailable: %v", err)
		return check
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(); err != nil {
		check.Status = "warning"
		check.Message = fmt.Sprintf("state DB migrate failed: %v", err)
		return check
	}

	// Windowed, not all-time (bd-2c0yh.2): an incident from months ago must
	// not keep the light red forever — users learn to ignore a lamp that
	// cannot clear.
	since := time.Now().UTC().Add(-guardDegradedDoctorWindow)
	stats, err := store.GuardDegradedEventStats(since)
	if err != nil {
		check.Status = "warning"
		check.Message = fmt.Sprintf("could not read guard degraded events: %v", err)
		return check
	}
	if stats.Count > 0 {
		transport, app := 0, 0
		if recent, listErr := store.ListGuardDegradedEvents(200); listErr == nil {
			for _, ev := range recent {
				if ev.CreatedAt.Before(since) {
					continue
				}
				if ev.Reason == guardReasonUnreachable {
					transport++
				} else {
					app++
				}
			}
		}
		check.Valid = false
		check.Status = "warning"
		check.Message = fmt.Sprintf("guard hook ran degraded %d time(s) in the last 7 days (first %s; %d with Agent Mail unreachable, %d with Agent Mail errors; commits were allowed unchecked)",
			stats.Count, stats.FirstAt.Local().Format(time.RFC3339), transport, app)
		return check
	}
	check.Message = "no degraded (unchecked) guard runs recorded in the last 7 days"
	return check
}

// guardHookPathCheck is the `ntm doctor` surface for the one fail-open path
// that can never record a ledger row (bd-2c0yh.4): the installed pre-commit
// hook execs `ntm guards check --staged`, but when `ntm` itself is not on
// PATH the hook prints a WARN into commit scrollback and exits 0 — no check,
// no degraded event, nothing for doctor to count. Detect that combination
// here, where `ntm` is definitionally runnable.
func guardHookPathCheck() ConfigCheck {
	check := ConfigCheck{Name: "guard-hook ntm on PATH", Valid: true, Status: "ok"}

	cwd, err := os.Getwd()
	if err != nil {
		check.Message = "could not determine current directory"
		return check
	}
	repoPath, err := findGitRoot(cwd)
	if err != nil {
		check.Message = "not in a git repository (guard hook not applicable)"
		return check
	}
	hookPath, err := findGitHookPath(repoPath, "pre-commit")
	if err != nil || !fileExists(hookPath) {
		check.Message = "no pre-commit guard hook installed here"
		return check
	}
	content, err := os.ReadFile(hookPath)
	if err != nil || !strings.Contains(string(content), "ntm-precommit-guard") {
		check.Message = "pre-commit hook is not an NTM guard"
		return check
	}
	if _, err := exec.LookPath("ntm"); err != nil {
		check.Valid = false
		check.Status = "warning"
		check.Message = "guard hook is installed but 'ntm' is not on PATH: the hook silently skips the reservation check on every commit (and cannot record a degraded event); add ntm to PATH or set NTM_GUARD_STRICT=1"
		return check
	}
	check.Message = "guard hook installed and 'ntm' resolves on PATH"
	return check
}
