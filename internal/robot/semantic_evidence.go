package robot

import (
	"strings"
	"time"
)

// parseGitTokenActivity preserves positive attribution even if a partial or
// corrupt log cannot prove inactivity. Git's history walk is not guaranteed to
// be sorted by committer timestamp (for example, after clock skew or rebases).
func parseGitTokenActivity(raw []byte, token string, window time.Duration, now time.Time) gitTokenActivity {
	out := gitTokenActivity{available: true}
	cutoff := now.Add(-window)
	records := 0
	for _, record := range strings.Split(string(raw), "\x00") {
		if strings.TrimSpace(record) == "" {
			continue
		}
		records++
		date, body, ok := strings.Cut(record, "\x1f")
		if !ok {
			out.available = false
			continue
		}
		if !commitBodyHasTokenLine(body, token) {
			continue
		}
		out.anyTokenCommit = true
		ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(date))
		if err != nil || ts.After(now) {
			out.available = false
			continue
		}
		if out.lastCommitAt == nil || ts.After(*out.lastCommitAt) {
			t := ts
			out.lastCommitAt = &t
		}
		if !ts.Before(cutoff) {
			out.commitsInWindow++
		}
	}
	// --grep is only a prefilter. A full page of prefix-colliding siblings
	// can otherwise hide this pane's work and masquerade as complete zero.
	if records >= semanticGitLogCap {
		out.available = false
	}
	return out
}

// claimTimestampEvidence uses either transition timestamp as positive evidence,
// counting a bead at most once. Missing optional timestamps are normal; invalid
// or future values cannot establish inactivity. A valid recent timestamp does
// establish progress even when the other field is absent or unusable.
func claimTimestampEvidence(issue brListIssue, cutoff, now time.Time) (recent, available bool) {
	seenValid, invalid := false, false
	for _, stamp := range []string{issue.ClosedAt, issue.UpdatedAt} {
		stamp = strings.TrimSpace(stamp)
		if stamp == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || ts.After(now) {
			invalid = true
			continue
		}
		seenValid = true
		if !ts.Before(cutoff) {
			return true, true
		}
	}
	return false, seenValid && !invalid
}
