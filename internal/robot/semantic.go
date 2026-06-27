// Package robot — semantic.go implements the OPTIONAL, opt-in semantic-progress
// signal for --robot-is-working / --robot-activity (#199).
//
// Motivation
// ----------
// The default is-working signal is derived entirely from tmux scrollback
// (CapturePaneOutput → agent.Parser + the IsLiveBusy THINKING override). That
// answers "is the terminal painting?", not "is work advancing?": a wedged pane
// that keeps animating a spinner is indistinguishable from a working one.
//
// This file adds a GROUND-TRUTH signal — git commits (and, secondarily, bead
// claims) attributed BY TOKEN to a specific pane — so an orchestrator can
// confirm real forward motion. The attribution key is a dispatch-time pane
// work-token (`NTM-Pane: <session>/<window>.<pane>`) that ntm injects into the
// marching orders at send/assign time; commits carry it as a trailer. Because
// the token is per-pane, a sibling pane's commits in the SAME shared repo are
// never miscredited (the false-attribution footgun of a cwd-level git scrape).
//
// SAFETY (the whole point — a false "wedged" verdict can kill a working agent):
//   - This signal is ADVISORY ONLY. buildSemanticProgress takes is_working's
//     velocity verdict as an INPUT and never returns it: it can never flip
//     is_working from true to false, and never emits a kill/reassign action.
//   - It degrades safely: when a pane was never stamped (older sessions, manual
//     sends, feature-off-at-dispatch) there is no token commit/label, so
//     source == "none" and nothing is inferred. Absence of a token never reads
//     as "wedged".
//   - All git/br reads are bounded (context timeout + row cap) and ONLY run
//     under --semantic. The default poll path makes zero new subprocess calls.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// defaultSemanticWindow is the conservative fallback look-back window used when
// neither --semantic-window nor [robot.semantic].window_minutes is set. Long
// enough that a legitimately-slow pane that simply hasn't committed recently is
// not flagged as a suspected wedge.
const defaultSemanticWindow = 30 * time.Minute

// semanticReadTimeout bounds each git/br subprocess so a hung repo can never
// stall a --semantic poll.
const semanticReadTimeout = 5 * time.Second

// semanticGitLogCap bounds how many token-bearing commits git returns. The
// token is pane-unique, so a pane realistically produces tens of commits; the
// cap exists only to keep the read O(1) even on a pathological history.
const semanticGitLogCap = 500

// SemanticProgress is the OPTIONAL, additive per-pane ground-truth signal. It is
// attached to PaneWorkStatus.SemanticProgress only under --semantic and omitted
// entirely otherwise (json:"...,omitempty" on the pointer field).
type SemanticProgress struct {
	// Source is "token" when this pane was demonstrably stamped (a commit
	// carrying its NTM-Pane trailer exists, or a bead carries its pane label),
	// or "none" when no token attribution is available (degrade-safe default).
	Source string `json:"source"`
	// Token is the canonical NTM-Pane work-token that commit messages are
	// grepped for. Always populated so consumers can see what was attributed.
	Token string `json:"token"`
	// CommitsInWindow counts commits carrying this pane's token within the
	// look-back window (attributed BY TOKEN, never by repo/cwd).
	CommitsInWindow int `json:"commits_in_window"`
	// ClaimsInWindow counts beads carrying this pane's label whose status
	// changed within the window. See the limitation note on gatherClaimActivity:
	// beads_rust exposes no per-transition ledger, so this is a conservative
	// proxy (status-updated-within-window), not an exact claim/close count.
	ClaimsInWindow int `json:"claims_in_window"`
	// LastCommitAt is the RFC3339 committer time of the most recent
	// token-attributed commit (any age), or null when none exists. A value
	// older than the window is the positive "stale" tell.
	LastCommitAt *string `json:"last_commit_at"`
	// WindowSeconds echoes the window used so a consumer can interpret the
	// counts without guessing the default.
	WindowSeconds int `json:"window_seconds"`
	// SuspectedWedge is an ADVISORY indicator string set only when there is
	// sustained terminal velocity AND the pane was stamped AND no
	// token-attributed forward motion landed within the window. It is purely
	// informational: it NEVER flips is_working and NEVER recommends a restart.
	SuspectedWedge string `json:"suspected_wedge,omitempty"`
}

// PaneAddr is the topology-aware pane address used to build the work-token. It
// mirrors the addressing --robot-is-working already reports (#172): window +
// pane index, so the token a pane is stamped with matches what is-working
// greps for.
type PaneAddr struct {
	Session string
	Window  int
	Pane    int
}

// PaneWorkToken returns the canonical commit-trailer work-token for a pane:
//
//	NTM-Pane: <session>/<window>.<pane>
//
// This is the single source of truth shared by the stamping path (send/assign)
// and the reading path (--semantic), so the two can never drift.
func PaneWorkToken(session string, window, pane int) string {
	return fmt.Sprintf("NTM-Pane: %s/%d.%d", session, window, pane)
}

// PaneBeadLabel returns the bead-label encoding of the same pane identity. Bead
// labels must be simple tokens, so the session is sanitized to [A-Za-z0-9_-].
// The commit trailer (PaneWorkToken) and this label are two encodings of the
// same pane work-token; both are derived from the same (session, window, pane)
// so the stamp and the read always agree.
func PaneBeadLabel(session string, window, pane int) string {
	return fmt.Sprintf("ntm-pane-%s-%d-%d", sanitizeLabelComponent(session), window, pane)
}

func sanitizeLabelComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// gitTokenActivity is the raw git-derived attribution for a pane.
type gitTokenActivity struct {
	commitsInWindow int
	anyTokenCommit  bool
	lastCommitAt    *time.Time
}

// claimActivity is the raw bead-derived attribution for a pane.
type claimActivity struct {
	claimsInWindow int
	anyLabeledBead bool
}

// PaneSemanticProgress orchestrates the bounded git/br reads for one pane and
// folds them into a SemanticProgress. It is called ONLY under --semantic.
//
// velocityPositive is the existing scrollback/velocity verdict (status.IsWorking
// AFTER the IsLiveBusy override). It is consumed as an input to decide whether
// to surface the advisory wedge tell — it is never modified here, so the caller
// cannot have is_working flipped by this function.
//
// repoDir is the pane's pane_current_path; "" (tmux lookup failed) degrades to
// source "none" with zero counts.
func PaneSemanticProgress(addr PaneAddr, repoDir string, window time.Duration, velocityPositive bool, now time.Time) *SemanticProgress {
	if window <= 0 {
		window = defaultSemanticWindow
	}
	token := PaneWorkToken(addr.Session, addr.Window, addr.Pane)
	label := PaneBeadLabel(addr.Session, addr.Window, addr.Pane)

	git := gatherGitTokenActivity(repoDir, token, window, now)
	claims := gatherClaimActivity(repoDir, label, window, now)

	sp := buildSemanticProgress(token, window, velocityPositive, git, claims, now)
	return &sp
}

// buildSemanticProgress is the PURE core (no I/O). Keeping it pure makes the
// safety invariants directly testable: it receives velocityPositive but only
// reads it to gate the advisory string, and it returns a SemanticProgress that
// carries NO is_working field — so it is structurally impossible for this
// signal to flip is_working true→false.
func buildSemanticProgress(token string, window time.Duration, velocityPositive bool, git gitTokenActivity, claims claimActivity, now time.Time) SemanticProgress {
	source := "none"
	if git.anyTokenCommit || claims.anyLabeledBead {
		source = "token"
	}

	var lastCommitStr *string
	if git.lastCommitAt != nil {
		s := git.lastCommitAt.UTC().Format(time.RFC3339)
		lastCommitStr = &s
	}

	sp := SemanticProgress{
		Source:          source,
		Token:           token,
		CommitsInWindow: git.commitsInWindow,
		ClaimsInWindow:  claims.claimsInWindow,
		LastCommitAt:    lastCommitStr,
		WindowSeconds:   int(window / time.Second),
	}

	// Advisory wedge tell — ADVISORY ONLY. Fires only when the pane is BOTH
	// velocity-positive (terminal still painting) AND demonstrably stamped
	// (source == "token") AND shows no token-attributed forward motion in the
	// window (no commits, no claims, and the last token commit is stale or
	// absent). It sets an informational string and nothing else; is_working is
	// untouched, and there is deliberately no kill/reassign recommendation.
	if velocityPositive && source == "token" {
		stale := git.lastCommitAt == nil || now.Sub(*git.lastCommitAt) > window
		if stale && git.commitsInWindow == 0 && claims.claimsInWindow == 0 {
			sp.SuspectedWedge = "sustained terminal velocity but no token-attributed commit or bead claim within the window; verify forward progress before any restart"
		}
	}

	return sp
}

// gatherGitTokenActivity runs a single bounded `git log --grep` keyed by the
// pane token across all refs. It attributes commits BY TOKEN, so a sibling
// pane's commits in the same shared repo are never counted. Any error (not a
// git repo, git missing, timeout) degrades to "no activity" — never a wedge.
func gatherGitTokenActivity(repoDir, token string, window time.Duration, now time.Time) gitTokenActivity {
	var out gitTokenActivity
	if strings.TrimSpace(repoDir) == "" {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), semanticReadTimeout)
	defer cancel()

	// -F: treat the token as a literal (it contains '.', '/'), not a regex.
	// --all: attribute the pane's commits regardless of which branch they land
	// on. -n caps the scan. %cI is the strict-ISO committer date.
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir,
		"log", "--all", "-F", "--grep="+token,
		fmt.Sprintf("-n%d", semanticGitLogCap), "--format=%cI")
	raw, err := cmd.Output()
	if err != nil {
		return out
	}

	cutoff := now.Add(-window)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, line)
		if perr != nil {
			continue
		}
		out.anyTokenCommit = true
		// Lines are newest-first; record the first (most recent) as last commit.
		if out.lastCommitAt == nil {
			t := ts
			out.lastCommitAt = &t
		}
		if !ts.Before(cutoff) {
			out.commitsInWindow++
		}
	}
	return out
}

// gatherClaimActivity is a bounded, best-effort bead read keyed by the pane
// label. It is the SECONDARY ("and/or") signal per the design.
//
// LIMITATION (documented deliberately): beads_rust exposes no per-pane→bead
// binding and no per-window status-transition ledger. The only cleanly
// available signal is `br list --label <pane-label>` plus the issue's
// updated_at/closed_at. We therefore count beads carrying this pane's label
// whose status changed within the window as a CONSERVATIVE PROXY for
// claim/close transitions — it can under- or over-count relative to a true
// transition ledger (e.g. a label re-applied for an unrelated edit bumps
// updated_at). It is never used to flip is_working; at worst it raises
// confidence or suppresses the advisory wedge tell. Any error (br missing, no
// .beads workspace, timeout) degrades to zero — never a wedge.
func gatherClaimActivity(dir, label string, window time.Duration, now time.Time) claimActivity {
	var out claimActivity
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(label) == "" {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), semanticReadTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "br", "list", "--label", label, "--include-closed", "--json")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	return countClaimsInWindow(raw, window, now)
}

// brListResponse is the subset of `br list --json` we parse.
type brListResponse struct {
	Issues []struct {
		Status    string `json:"status"`
		UpdatedAt string `json:"updated_at"`
		ClosedAt  string `json:"closed_at"`
	} `json:"issues"`
}

// countClaimsInWindow is the PURE parser over `br list --json` output, separated
// for direct fixture testing. It counts labeled beads whose status changed
// (updated_at or closed_at) within the window.
func countClaimsInWindow(raw []byte, window time.Duration, now time.Time) claimActivity {
	var out claimActivity
	var resp brListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return out
	}
	out.anyLabeledBead = len(resp.Issues) > 0
	cutoff := now.Add(-window)
	for _, issue := range resp.Issues {
		if withinWindow(issue.ClosedAt, cutoff) || withinWindow(issue.UpdatedAt, cutoff) {
			out.claimsInWindow++
		}
	}
	return out
}

func withinWindow(ts string, cutoff time.Time) bool {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// br emits RFC3339 with sub-second precision; RFC3339Nano covers it.
		parsed, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return false
		}
	}
	return !parsed.Before(cutoff)
}
