// Package commitlint evaluates pre-commit / pre-handoff readiness for
// an agent operating in a coordinated swarm. Inputs are gathered from
// the existing Agent Mail, Beads, git, and queue-dry adapters by the
// caller and fed in here as plain views — the lint itself does no I/O
// and never mutates state, so it is safe to call from any context and
// trivially testable.
//
// See bd-3v1gs.5: the operator wants one preflight that says "yes,
// commit/hand off" or "no, here's why" with a structured findings
// list a wrapper can render or feed to another tool.
package commitlint

import (
	"path"
	"sort"
	"strings"
	"time"
)

// Severity classifies how seriously a Finding should block the
// caller. Critical findings make SafeToCommit=false; warnings and
// infos do not.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Finding is one line in the readiness report.
type Finding struct {
	Code        string   `json:"code"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// ReservationView is the subset of agentmail.FileReservation fields
// the lint uses. Callers convert from agentmail.FileReservation to
// avoid coupling this package to the mail client.
type ReservationView struct {
	ID          int
	PathPattern string
	AgentName   string
	Exclusive   bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// InboxView is the subset of agentmail.InboxMessage the lint uses.
type InboxView struct {
	ID          int
	Subject     string
	From        string
	Importance  string // "urgent", "high", "normal"
	AckRequired bool
	ReadAt      *time.Time // nil means unread/unacked
}

// SyncView captures the beads JSONL/DB sync status — derived from the
// queue-dry diagnostic so the lint stays consistent with that path.
type SyncView struct {
	HasLocalBeadsDB bool
	NeedsFlush      bool   // beads.db newer than issues.jsonl by more than the lag threshold
	Status          string // free-text status from evaluateQueueDrySync
}

// Inputs is the full set of evidence the lint evaluates.
type Inputs struct {
	// AgentName is the current operator's agent identity (per Agent
	// Mail). Used to attribute reservations and urgent mail.
	AgentName string

	// TouchedPaths is the list of repo-relative paths the operator
	// has changed (modified, created, or staged).
	TouchedPaths []string

	// Reservations is every active file reservation across the
	// project — not filtered to AgentName, because the lint has to
	// detect foreign-held locks on touched paths.
	Reservations []ReservationView

	// Inbox contains the unacked messages addressed to AgentName.
	// The lint scans for urgent + ack-required entries.
	Inbox []InboxView

	// Sync is the beads sync status. NeedsFlush=true is a critical
	// finding because committing on top of an out-of-date JSONL
	// produces a confusing diff for downstream agents.
	Sync SyncView

	// StaleReservationThreshold marks a reservation as stale if its
	// CreatedAt is older than (Now - threshold). Default 24h via
	// DefaultStaleReservationThreshold; pass zero to skip the check.
	StaleReservationThreshold time.Duration

	// Now lets tests pin the wall clock. Defaults to time.Now() when
	// zero.
	Now time.Time
}

// DefaultStaleReservationThreshold matches the bv ready/queue-dry
// default for "stale in_progress" so operators see the same window
// in both diagnostics.
const DefaultStaleReservationThreshold = 24 * time.Hour

// Summary aggregates findings for the JSON envelope.
type Summary struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// Report is the full readiness assessment.
type Report struct {
	GeneratedAt  time.Time `json:"generated_at"`
	AgentName    string    `json:"agent_name,omitempty"`
	SafeToCommit bool      `json:"safe_to_commit"`
	Findings     []Finding `json:"findings"`
	Summary      Summary   `json:"summary"`
	Notes        []string  `json:"notes,omitempty"`
}

// Evaluate runs the lint over inputs and returns a Report. SafeToCommit
// is true iff there are zero critical findings. The lint is pure: it
// never reads files, calls the network, or mutates anything.
func Evaluate(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	staleThreshold := in.StaleReservationThreshold
	if staleThreshold == 0 {
		staleThreshold = DefaultStaleReservationThreshold
	}

	report := Report{
		GeneratedAt:  now.UTC(),
		AgentName:    strings.TrimSpace(in.AgentName),
		Findings:     []Finding{},
		SafeToCommit: true,
		Notes:        []string{"advisory only; lint does not mutate reservations or files"},
	}

	report.Findings = append(report.Findings, evalSyncFlush(in.Sync)...)
	report.Findings = append(report.Findings, evalUrgentMail(in.Inbox)...)
	report.Findings = append(report.Findings, evalReservations(in, now, staleThreshold)...)
	report.Findings = append(report.Findings, evalDirtyTreeExplained(in)...)

	// Stable order: severity descending, then code ascending.
	sort.SliceStable(report.Findings, func(i, j int) bool {
		si := severityRank(report.Findings[i].Severity)
		sj := severityRank(report.Findings[j].Severity)
		if si != sj {
			return si > sj
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})

	for _, f := range report.Findings {
		switch f.Severity {
		case SeverityCritical:
			report.Summary.Critical++
			report.SafeToCommit = false
		case SeverityWarning:
			report.Summary.Warning++
		case SeverityInfo:
			report.Summary.Info++
		}
	}

	return report
}

func evalSyncFlush(s SyncView) []Finding {
	if !s.HasLocalBeadsDB {
		return nil
	}
	if !s.NeedsFlush {
		return nil
	}
	return []Finding{{
		Code:        "stale_beads_export",
		Severity:    SeverityCritical,
		Summary:     "beads.db is newer than issues.jsonl; commit would ship a stale tracker export",
		Remediation: "br sync --flush-only && git add .beads/issues.jsonl",
		Evidence:    []string{"sync_status=" + s.Status},
	}}
}

func evalUrgentMail(inbox []InboxView) []Finding {
	if len(inbox) == 0 {
		return nil
	}
	var unacked []InboxView
	for _, m := range inbox {
		if !m.AckRequired {
			continue
		}
		if m.ReadAt != nil {
			continue
		}
		if !strings.EqualFold(m.Importance, "urgent") {
			continue
		}
		unacked = append(unacked, m)
	}
	if len(unacked) == 0 {
		return nil
	}
	evidence := make([]string, 0, len(unacked))
	for _, m := range unacked {
		evidence = append(evidence, formatInboxLine(m))
	}
	return []Finding{{
		Code:        "urgent_unacked_mail",
		Severity:    SeverityCritical,
		Summary:     "ack-required urgent mail is still unread; commit may proceed past a coordination signal",
		Remediation: "review the listed messages and acknowledge_message before committing",
		Evidence:    evidence,
	}}
}

func formatInboxLine(m InboxView) string {
	parts := []string{
		"id=" + intToString(m.ID),
		"from=" + strings.TrimSpace(m.From),
	}
	if subj := strings.TrimSpace(m.Subject); subj != "" {
		parts = append(parts, "subject="+subj)
	}
	return strings.Join(parts, " ")
}

func evalReservations(in Inputs, now time.Time, staleThreshold time.Duration) []Finding {
	var findings []Finding

	if len(in.TouchedPaths) == 0 {
		return findings
	}

	type match struct {
		path        string
		reservation ReservationView
	}
	var foreignMatches []match
	var ownStaleMatches []match
	var unreservedPaths []string
	agent := strings.TrimSpace(in.AgentName)

	for _, raw := range in.TouchedPaths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		ownerMatched := false
		foreignMatched := false
		var staleHit *ReservationView
		for i := range in.Reservations {
			r := in.Reservations[i]
			if !pathMatchesReservation(p, r.PathPattern) {
				continue
			}
			if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
				continue
			}
			isOwn := agent != "" && strings.EqualFold(r.AgentName, agent)
			switch {
			case isOwn:
				ownerMatched = true
				if staleThreshold > 0 && !r.CreatedAt.IsZero() && now.Sub(r.CreatedAt) > staleThreshold {
					rr := r
					staleHit = &rr
				}
			case r.Exclusive:
				foreignMatched = true
				foreignMatches = append(foreignMatches, match{path: p, reservation: r})
			}
		}
		switch {
		case foreignMatched:
			// already recorded above
		case !ownerMatched && agent != "":
			unreservedPaths = append(unreservedPaths, p)
		}
		if staleHit != nil {
			ownStaleMatches = append(ownStaleMatches, match{path: p, reservation: *staleHit})
		}
	}

	if len(foreignMatches) > 0 {
		evidence := make([]string, 0, len(foreignMatches))
		for _, m := range foreignMatches {
			evidence = append(evidence, formatForeignReservation(m.path, m.reservation))
		}
		findings = append(findings, Finding{
			Code:        "foreign_reservation",
			Severity:    SeverityCritical,
			Summary:     "touched files are covered by another agent's exclusive reservation",
			Remediation: "negotiate handoff with the holding agent or wait for expiry; do not commit over their lock",
			Evidence:    evidence,
		})
	}
	if len(unreservedPaths) > 0 {
		findings = append(findings, Finding{
			Code:        "missing_reservation",
			Severity:    SeverityWarning,
			Summary:     "touched files have no matching reservation under your agent name",
			Remediation: "file_reservation_paths(...) before committing, or document why no reservation was needed",
			Evidence:    uniqueEvidence(unreservedPaths),
		})
	}
	if len(ownStaleMatches) > 0 {
		evidence := make([]string, 0, len(ownStaleMatches))
		for _, m := range ownStaleMatches {
			age := now.Sub(m.reservation.CreatedAt).Round(time.Hour)
			evidence = append(evidence, "path="+m.path+" reservation_age="+age.String())
		}
		findings = append(findings, Finding{
			Code:        "stale_own_reservation",
			Severity:    SeverityWarning,
			Summary:     "your reservation on a touched path is older than the stale threshold",
			Remediation: "extend the reservation TTL or release+re-acquire to refresh the audit trail",
			Evidence:    evidence,
		})
	}

	return findings
}

func evalDirtyTreeExplained(in Inputs) []Finding {
	if len(in.TouchedPaths) == 0 {
		return nil
	}
	if strings.TrimSpace(in.AgentName) != "" {
		return nil
	}
	return []Finding{{
		Code:        "dirty_tree_no_identity",
		Severity:    SeverityInfo,
		Summary:     "touched paths recorded but no AgentName provided; reservation checks were skipped",
		Remediation: "set AGENT_NAME so reservation attribution can run",
	}}
}

// pathMatchesReservation returns true when `p` matches the
// reservation's PathPattern. We support exact match and a single
// trailing `/**` wildcard (the convention used by Agent Mail
// reservations); anything else falls back to filepath.Match for
// generic glob support.
func pathMatchesReservation(p, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if p == pattern {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if prefix == "" {
			return true
		}
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		// One-segment match.
		if !strings.HasPrefix(p, prefix+"/") {
			return false
		}
		rest := strings.TrimPrefix(p, prefix+"/")
		return rest != "" && !strings.Contains(rest, "/")
	}
	if matched, err := path.Match(pattern, p); err == nil && matched {
		return true
	}
	return false
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

func uniqueEvidence(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func formatForeignReservation(p string, r ReservationView) string {
	return "path=" + p + " holder=" + r.AgentName + " pattern=" + r.PathPattern
}

// intToString avoids pulling strconv just for the inbox formatter.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
