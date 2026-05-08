// Package identityhygiene reports on Agent Mail identity records and
// contact links that refer to panes no longer present in the active
// tmux session, project keys NTM no longer knows about, or registered
// agents whose pane has died.
//
// The report is dry-run only: this package never deletes a file,
// never releases a reservation, and never mutates state. A future
// cleanup helper must take an explicit DryRun=false flag and live in
// a separate function.
//
// See bd-3v1gs.6.
package identityhygiene

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Severity classifies how seriously a Finding should be surfaced.
// Identity hygiene is advisory, not blocking — the worst it can emit
// is a Warning. Operators decide whether to clean up.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Finding is one line in the hygiene report.
type Finding struct {
	Code        string   `json:"code"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// IdentityRecord describes one on-disk identity file or contact link
// that the hygiene check should evaluate. The caller enumerates these
// from the canonical and legacy paths (see agentmail.ResolveIdentity)
// and passes them in as plain views.
type IdentityRecord struct {
	// Path is the absolute filesystem path of the identity record.
	// Used in evidence strings so operators can locate the file.
	Path string

	// AgentName is the name stored in the identity file.
	AgentName string

	// ProjectHash is the project namespace hash carved out of the
	// path (e.g., the 12-char prefix of sha1(project_key)). Empty
	// when the format does not include a project hash.
	ProjectHash string

	// PaneID is the raw pane ID associated with the record. Used to
	// match against LivePanes.
	PaneID string

	// LinkedAgent is the contact-link target when the record is a
	// contact link rather than a primary identity file. Empty for
	// identity files. Used by the dead_contact_link check.
	LinkedAgent string

	// ModifiedAt is the file's mtime, used to suppress findings on
	// records younger than StaleAfter.
	ModifiedAt time.Time
}

// LivePane is one tmux pane currently visible to NTM.
type LivePane struct {
	// ID is the raw tmux pane id (e.g., "%17").
	ID string
	// Session is the tmux session name.
	Session string
}

// RegisteredAgent captures the subset of agentmail.Agent fields we
// inspect: a stale registration is one whose LastActiveAt is older
// than StaleAfter and whose PaneID (parsed from the agent's task
// description or pane fields) doesn't match any LivePane.
type RegisteredAgent struct {
	Name         string
	PaneID       string
	LastActiveAt time.Time
}

// Inputs is the full set of evidence the hygiene check evaluates.
type Inputs struct {
	// Identities is every identity record + contact link the caller
	// could enumerate (canonical + legacy paths).
	Identities []IdentityRecord

	// LivePanes is the set of panes currently visible to NTM via tmux.
	LivePanes []LivePane

	// RegisteredAgents is the agentmail Agent registry view.
	RegisteredAgents []RegisteredAgent

	// KnownProjectKeys is the set of project keys NTM currently
	// recognizes. The hygiene check derives the same 12-char SHA-1
	// prefix the canonical path uses and flags any IdentityRecord
	// whose ProjectHash does not appear in this set.
	KnownProjectKeys []string

	// StaleAfter is the minimum age a record must reach before a
	// stale_identity / dead_pane finding fires. Records younger than
	// StaleAfter are considered "fresh" — they may be in transition.
	// Zero defaults to 1h.
	StaleAfter time.Duration

	// Now lets tests pin the clock. Defaults to time.Now() when zero.
	Now time.Time
}

// DefaultStaleAfter matches the bv ready / queue-dry default-ish
// freshness window for an idle pane.
const DefaultStaleAfter = 1 * time.Hour

// Summary aggregates findings for the JSON envelope.
type Summary struct {
	Warning int `json:"warning"`
	Info    int `json:"info"`
}

// Report is the full hygiene assessment.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	DryRun      bool      `json:"dry_run"`
	Findings    []Finding `json:"findings"`
	Summary     Summary   `json:"summary"`
	Notes       []string  `json:"notes,omitempty"`
}

// Evaluate runs the hygiene check over inputs and returns a Report.
// Pure: never reads files, never mutates state. DryRun is always
// true on the returned report — the caller cannot opt out of dry-run
// from this entry point.
func Evaluate(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	staleAfter := in.StaleAfter
	if staleAfter == 0 {
		staleAfter = DefaultStaleAfter
	}

	report := Report{
		GeneratedAt: now.UTC(),
		DryRun:      true,
		Findings:    []Finding{},
		Notes:       []string{"dry-run only; identity files are never deleted by this report"},
	}

	livePaneSet := make(map[string]struct{}, len(in.LivePanes))
	for _, p := range in.LivePanes {
		if id := strings.TrimSpace(p.ID); id != "" {
			livePaneSet[id] = struct{}{}
		}
	}
	knownHashSet := make(map[string]struct{}, len(in.KnownProjectKeys))
	for _, k := range in.KnownProjectKeys {
		if h := projectHash(k); h != "" {
			knownHashSet[h] = struct{}{}
		}
	}
	registeredByName := make(map[string]RegisteredAgent, len(in.RegisteredAgents))
	for _, a := range in.RegisteredAgents {
		if name := strings.TrimSpace(a.Name); name != "" {
			registeredByName[strings.ToLower(name)] = a
		}
	}

	report.Findings = append(report.Findings, evalStaleIdentities(in.Identities, livePaneSet, staleAfter, now)...)
	report.Findings = append(report.Findings, evalUnknownProjects(in.Identities, knownHashSet)...)
	report.Findings = append(report.Findings, evalDeadPanes(in.RegisteredAgents, livePaneSet, staleAfter, now)...)
	report.Findings = append(report.Findings, evalDeadContactLinks(in.Identities, registeredByName)...)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		ri := severityRank(report.Findings[i].Severity)
		rj := severityRank(report.Findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})

	for _, f := range report.Findings {
		switch f.Severity {
		case SeverityWarning:
			report.Summary.Warning++
		case SeverityInfo:
			report.Summary.Info++
		}
	}

	return report
}

func evalStaleIdentities(records []IdentityRecord, livePanes map[string]struct{}, staleAfter time.Duration, now time.Time) []Finding {
	var dead []string
	for _, r := range records {
		// Contact links don't have a pane to check; handled by evalDeadContactLinks.
		if r.LinkedAgent != "" {
			continue
		}
		paneID := strings.TrimSpace(r.PaneID)
		if paneID == "" {
			continue
		}
		if _, alive := livePanes[paneID]; alive {
			continue
		}
		if !r.ModifiedAt.IsZero() && now.Sub(r.ModifiedAt) < staleAfter {
			continue
		}
		dead = append(dead, formatRecordEvidence(r))
	}
	if len(dead) == 0 {
		return nil
	}
	sort.Strings(dead)
	return []Finding{{
		Code:        "stale_identity",
		Severity:    SeverityWarning,
		Summary:     "identity files reference panes no longer present in the active tmux session",
		Remediation: "review the listed records; if the pane is gone for good, delete the file with `rm` after operator confirmation",
		Evidence:    dead,
	}}
}

func evalUnknownProjects(records []IdentityRecord, knownHashes map[string]struct{}) []Finding {
	if len(knownHashes) == 0 {
		return nil
	}
	var unknown []string
	for _, r := range records {
		hash := strings.TrimSpace(strings.ToLower(r.ProjectHash))
		if hash == "" {
			continue
		}
		if _, known := knownHashes[hash]; known {
			continue
		}
		unknown = append(unknown, formatRecordEvidence(r))
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return []Finding{{
		Code:        "unknown_project",
		Severity:    SeverityWarning,
		Summary:     "identity records exist under project hashes NTM no longer recognizes",
		Remediation: "confirm the project is gone for good before removing; legacy projects can be re-registered if recovery is needed",
		Evidence:    unknown,
	}}
}

func evalDeadPanes(agents []RegisteredAgent, livePanes map[string]struct{}, staleAfter time.Duration, now time.Time) []Finding {
	var dead []string
	for _, a := range agents {
		paneID := strings.TrimSpace(a.PaneID)
		if paneID == "" {
			continue
		}
		if _, alive := livePanes[paneID]; alive {
			continue
		}
		if !a.LastActiveAt.IsZero() && now.Sub(a.LastActiveAt) < staleAfter {
			continue
		}
		dead = append(dead, "agent="+a.Name+" pane="+paneID)
	}
	if len(dead) == 0 {
		return nil
	}
	sort.Strings(dead)
	return []Finding{{
		Code:        "dead_pane",
		Severity:    SeverityWarning,
		Summary:     "registered Agent Mail agents reference panes that are no longer alive",
		Remediation: "operator may unregister these agents via Agent Mail tools after verifying the pane is truly gone",
		Evidence:    dead,
	}}
}

func evalDeadContactLinks(records []IdentityRecord, registered map[string]RegisteredAgent) []Finding {
	if len(registered) == 0 {
		return nil
	}
	var dead []string
	for _, r := range records {
		linked := strings.TrimSpace(r.LinkedAgent)
		if linked == "" {
			continue
		}
		if _, ok := registered[strings.ToLower(linked)]; ok {
			continue
		}
		dead = append(dead, "path="+r.Path+" linked_agent="+linked)
	}
	if len(dead) == 0 {
		return nil
	}
	sort.Strings(dead)
	return []Finding{{
		Code:        "dead_contact_link",
		Severity:    SeverityInfo,
		Summary:     "contact links point at agents that are no longer registered",
		Remediation: "review and remove only after confirming the linked agent has not simply rotated names",
		Evidence:    dead,
	}}
}

// projectHash returns the same 12-char SHA-1 prefix the canonical
// agentmail identity path uses. Re-derived locally so this package
// does not need to import internal/agentmail.
func projectHash(projectKey string) string {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return ""
	}
	sum := sha1.Sum([]byte(projectKey)) // nolint:gosec // path namespace, not cryptographic
	full := hex.EncodeToString(sum[:])
	if len(full) < 12 {
		return full
	}
	return full[:12]
}

// ProjectHash exposes the canonical project-hash derivation so callers
// gathering IdentityRecord lists from disk can produce comparable
// values without re-implementing the algorithm.
func ProjectHash(projectKey string) string {
	return projectHash(projectKey)
}

func formatRecordEvidence(r IdentityRecord) string {
	parts := []string{"path=" + r.Path}
	if name := strings.TrimSpace(r.AgentName); name != "" {
		parts = append(parts, "agent="+name)
	}
	if pane := strings.TrimSpace(r.PaneID); pane != "" {
		parts = append(parts, "pane="+pane)
	}
	if hash := strings.TrimSpace(r.ProjectHash); hash != "" {
		parts = append(parts, "project_hash="+hash)
	}
	return strings.Join(parts, " ")
}

func severityRank(s Severity) int {
	switch s {
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}
