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
	"bufio"
	"bytes"
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
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
const guardDegradedDoctorWindow = robot.GuardDegradedDoctorWindow

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
	var push bool
	var projectKey string

	cmd := &cobra.Command{
		Use:    "check",
		Short:  "Check staged or pushed files against Agent Mail file reservations",
		Hidden: true, // plumbing for the installed pre-commit / pre-push hooks
		Long: `Check files against active Agent Mail file reservations.

This is the command the NTM pre-commit and pre-push guard hooks run:
  - With --staged, it lists the files staged in the current git repository.
  - With --push, it reads git pre-push ref lines from stdin and checks files
    touched by commits being pushed (falling back to unpushed commits on HEAD
    when stdin is empty).

If any checked file is covered by an active exclusive reservation held by
another agent, the check exits non-zero.

If Agent Mail is unreachable the check fails open with a visible WARN and a
degraded-event row surfaced by 'ntm doctor'. Set NTM_GUARD_STRICT=1 to fail
closed instead.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if staged && push {
				return fmt.Errorf("guards check accepts only one of --staged or --push")
			}
			if !staged && !push {
				return fmt.Errorf("guards check requires --staged or --push")
			}
			if push {
				return runGuardsCheckPush(cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin(), projectKey)
			}
			return runGuardsCheckStaged(cmd.OutOrStdout(), cmd.ErrOrStderr(), projectKey)
		},
	}

	cmd.Flags().BoolVar(&staged, "staged", false, "Check the files staged for commit")
	cmd.Flags().BoolVar(&push, "push", false, "Check files modified by commits being pushed")
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
	return checkReservedPaths(stdout, stderr, repoPath, projectKey, "Pre-commit", "commit", "staged", stagedPaths)
}

func runGuardsCheckPush(stdout, stderr io.Writer, stdin io.Reader, projectKey string) error {
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

	paths, err := pushedFiles(repoPath, stdin)
	if err != nil {
		return fmt.Errorf("listing pushed files: %w", err)
	}
	return checkReservedPaths(stdout, stderr, repoPath, projectKey, "Pre-push", "push", "pushed", paths)
}

func checkReservedPaths(stdout, stderr io.Writer, repoPath, projectKey, phaseLabel, actionVerb, scopeAdj string, paths []string) error {
	if len(paths) == 0 {
		fmt.Fprintf(stdout, "[ntm-guard] %s check passed (no %s files)\n", phaseLabel, scopeAdj)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), guardCheckTimeout)
	defer cancel()

	client := agentmail.NewClient()
	conflicts, checkErr := client.CheckStagedReservations(ctx, projectKey, os.Getenv(guardSelfAgentEnv), paths)
	if checkErr != nil {
		if guardStrictMode() {
			fmt.Fprintf(stderr, "[ntm-guard] BLOCKED: Agent Mail reservation check failed and %s=1 (fail-closed): %v\n", guardStrictEnv, checkErr)
			return fmt.Errorf("%s blocked: Agent Mail unreachable and %s=1 requires a completed reservation check", actionVerb, guardStrictEnv)
		}
		// Fail open, but VISIBLY: WARN now, and a degraded-event row that
		// `ntm doctor` / the dashboard surface later. Transport failures and
		// application errors from a healthy server are recorded under
		// distinct reasons (bd-2c0yh.2) so doctor can tell "daemon down at
		// commit time" from "repo not registered with Agent Mail".
		reason := guardReasonAppError
		if guardTransportError(checkErr) {
			reason = guardReasonUnreachable
			fmt.Fprintf(stderr, "[ntm-guard] WARN: Agent Mail unreachable (%v) — allowing %s WITHOUT reservation check (degraded mode)\n", checkErr, actionVerb)
		} else {
			fmt.Fprintf(stderr, "[ntm-guard] WARN: Agent Mail answered with an error (%v) — allowing %s WITHOUT reservation check (degraded mode)\n", checkErr, actionVerb)
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
		fmt.Fprintf(stderr, "[ntm-guard] Resolve with the holder or release the reservation (ntm mail), then retry the %s.\n", actionVerb)
		return fmt.Errorf("%s blocked: %d %s file(s) conflict with active Agent Mail file reservations", actionVerb, len(conflicts), scopeAdj)
	}

	fmt.Fprintf(stdout, "[ntm-guard] %s check passed (%d %s file(s), no reservation conflicts)\n", phaseLabel, len(paths), scopeAdj)
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

func isZeroGitOID(oid string) bool {
	if oid == "" {
		return true
	}
	for i := 0; i < len(oid); i++ {
		if oid[i] != '0' {
			return false
		}
	}
	return true
}

// pushedFiles collects the unique repo-relative paths modified by commits being
// pushed, reading git's standard pre-push stdin format:
//
//	<local ref> <local oid> <remote ref> <remote oid>
//
// If stdin is nil or contains no ref lines, it falls back to commits reachable
// from HEAD that are not yet on any remote-tracking ref.
func pushedFiles(repoPath string, stdin io.Reader) ([]string, error) {
	seen := make(map[string]struct{})
	var paths []string
	addPaths := func(items []string) {
		for _, p := range items {
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			paths = append(paths, p)
		}
	}

	sawRefLine := false
	if stdin != nil {
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 {
				continue
			}
			sawRefLine = true
			localOID := fields[1]
			remoteOID := fields[3]
			if !isValidGitOID(localOID) || !isValidGitOID(remoteOID) {
				return nil, fmt.Errorf("invalid git object ID in pre-push ref line: local=%q remote=%q", localOID, remoteOID)
			}
			if isZeroGitOID(localOID) {
				// Remote branch/tag deletion: no local commit contents pushed.
				continue
			}
			if !isZeroGitOID(remoteOID) {
				diffPaths, err := gitDiffRangePaths(repoPath, remoteOID, localOID)
				if err == nil {
					addPaths(diffPaths)
					continue
				}
			}
			unpushedPaths, err := pushedFilesForUnpushedCommits(repoPath, localOID)
			if err != nil {
				return nil, err
			}
			addPaths(unpushedPaths)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading pre-push stdin: %w", err)
		}
	}

	if !sawRefLine {
		// Fallback when invoked directly without pre-push stdin: check commits
		// ahead of remotes on HEAD, if HEAD exists.
		if err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "HEAD").Run(); err != nil {
			return nil, nil
		}
		unpushedPaths, err := pushedFilesForUnpushedCommits(repoPath, "HEAD")
		if err != nil {
			return nil, err
		}
		addPaths(unpushedPaths)
	}

	return paths, nil
}

func isValidGitOID(oid string) bool {
	if len(oid) < 40 || len(oid) > 64 {
		return false
	}
	for i := 0; i < len(oid); i++ {
		c := oid[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func gitDiffRangePaths(repoPath, baseOID, headOID string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "diff", "--name-only", "--no-renames", "--diff-filter=ACDM", "-z", baseOID+".."+headOID, "--")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s..%s: %w", baseOID, headOID, err)
	}
	return parseNulSeparatedPaths(out), nil
}

func pushedFilesForUnpushedCommits(repoPath, localRef string) ([]string, error) {
	revCmd := exec.Command("git", "-C", repoPath, "rev-list", "--max-count=500", localRef, "--not", "--remotes")
	revOut, err := revCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-list %s --not --remotes: %w", localRef, err)
	}
	commits := strings.TrimSpace(string(revOut))
	if commits == "" {
		return nil, nil
	}

	diffCmd := exec.Command("git", "-C", repoPath, "diff-tree", "-m", "--stdin", "--no-commit-id", "--name-only", "-r", "--root", "--no-renames", "--diff-filter=ACDM", "-z")
	diffCmd.Stdin = bytes.NewBufferString(commits + "\n")
	diffOut, err := diffCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree for unpushed commits: %w", err)
	}
	return parseNulSeparatedPaths(diffOut), nil
}

func parseNulSeparatedPaths(out []byte) []string {
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
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
