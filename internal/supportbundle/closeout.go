package supportbundle

import (
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot/assurance"
	"github.com/Dicklesworthstone/ntm/internal/swarmslo"
)

// CloseoutSchemaVersion is the version of the closeout bundle JSON
// shape. Bump on backward-incompatible changes; additive fields keep
// the same version.
const CloseoutSchemaVersion = 1

// VerificationOutcome is a closed enum for command outcomes the
// closeout bundle records. The package never *runs* commands or
// guesses success — these values are supplied by the caller from
// observed evidence.
type VerificationOutcome string

const (
	OutcomePassed  VerificationOutcome = "passed"
	OutcomeFailed  VerificationOutcome = "failed"
	OutcomeSkipped VerificationOutcome = "skipped"
	OutcomeUnknown VerificationOutcome = "unknown"
)

// ResidualRiskSeverity grades a risk for dashboard rollup.
type ResidualRiskSeverity string

const (
	RiskSeverityHigh   ResidualRiskSeverity = "high"
	RiskSeverityMedium ResidualRiskSeverity = "medium"
	RiskSeverityLow    ResidualRiskSeverity = "low"
)

// RunMeta is the high-level identity of the run being closed out.
type RunMeta struct {
	SwarmName string    `json:"swarm_name,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// CommitEntry is one committed change. Hash should be the short or
// long SHA the caller saw in `git log`.
type CommitEntry struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author,omitempty"`
}

// BeadsDelta records the bead state change across the run.
type BeadsDelta struct {
	Opened    []string `json:"opened,omitempty"`
	Closed    []string `json:"closed,omitempty"`
	StillOpen []string `json:"still_open,omitempty"`
}

// VerificationEntry records one command the operator (or harness)
// observed running. Outcome is whatever the caller saw — the closeout
// bundle does not infer success.
type VerificationEntry struct {
	Command  string              `json:"command"`
	Outcome  VerificationOutcome `json:"outcome"`
	Notes    string              `json:"notes,omitempty"`
	Duration time.Duration       `json:"duration,omitempty"`
}

// ReservationSnapshot is one outstanding reservation at closeout time.
type ReservationSnapshot struct {
	PathPattern string    `json:"path_pattern"`
	AgentName   string    `json:"agent_name"`
	Exclusive   bool      `json:"exclusive"`
	AcquiredAt  time.Time `json:"acquired_at"`
}

// MailSnapshot rolls up the operator's outstanding mail at closeout.
type MailSnapshot struct {
	UnackedUrgent int `json:"unacked_urgent"`
	PendingAck    int `json:"pending_ack"`
}

// QueueState rolls up the bv ready / in-progress queue at closeout.
type QueueState struct {
	Ready      int  `json:"ready"`
	InProgress int  `json:"in_progress"`
	Blocked    int  `json:"blocked"`
	QueueDry   bool `json:"queue_dry"`
}

// ResidualRisk is one outstanding concern derived from inputs. Codes
// are stable so consumers can route on them.
type ResidualRisk struct {
	Code        string               `json:"code"`
	Severity    ResidualRiskSeverity `json:"severity"`
	Description string               `json:"description"`
	Evidence    []string             `json:"evidence,omitempty"`
}

// CloseoutInputs is the full set of evidence BuildCloseout reduces.
type CloseoutInputs struct {
	Run               RunMeta
	Commits           []CommitEntry
	Beads             BeadsDelta
	Verifications     []VerificationEntry
	Reservations      []ReservationSnapshot
	Mail              MailSnapshot
	Queue             QueueState
	DegradedProviders []string
	Notes             []string
	Now               time.Time
}

// CloseoutBundle is the JSON-first artifact a swarm or long agent
// run emits at end-of-run. It is read-only data — no commands are
// executed by this package.
type CloseoutBundle struct {
	SchemaVersion     int                   `json:"schema_version"`
	GeneratedAt       time.Time             `json:"generated_at"`
	Run               RunMeta               `json:"run"`
	Commits           []CommitEntry         `json:"commits,omitempty"`
	Beads             BeadsDelta            `json:"beads"`
	Verifications     []VerificationEntry   `json:"verifications,omitempty"`
	Reservations      []ReservationSnapshot `json:"active_reservations,omitempty"`
	Mail              MailSnapshot          `json:"mail"`
	Queue             QueueState            `json:"queue"`
	DegradedProviders []string              `json:"degraded_providers,omitempty"`
	ResidualRisks     []ResidualRisk        `json:"residual_risks,omitempty"`
	Counts            CloseoutCounts        `json:"counts"`
	Notes             []string              `json:"notes,omitempty"`
}

// CloseoutCounts is a small rollup so a dashboard can summarize a
// bundle without re-traversing every field.
type CloseoutCounts struct {
	Commits             int `json:"commits"`
	BeadsOpened         int `json:"beads_opened"`
	BeadsClosed         int `json:"beads_closed"`
	BeadsStillOpen      int `json:"beads_still_open"`
	Verifications       int `json:"verifications"`
	VerificationsPassed int `json:"verifications_passed"`
	VerificationsFailed int `json:"verifications_failed"`
	ActiveReservations  int `json:"active_reservations"`
	ResidualRisks       int `json:"residual_risks"`
}

// CloseoutProofSchemaVersion is the JSON contract for the composed
// operator proof bundle.
const CloseoutProofSchemaVersion = "ntm.closeout_proof.v1"

// ProofState is the state of a proof section or the overall bundle.
type ProofState string

const (
	ProofStatePresent  ProofState = "present"
	ProofStateMissing  ProofState = "missing"
	ProofStateStale    ProofState = "stale"
	ProofStateDegraded ProofState = "degraded"
	ProofStateFailing  ProofState = "failing"
)

// CloseoutProofInputs are already-collected evidence for a closeout
// proof bundle. The reducer is pure: callers gather live Beads, Agent
// Mail, pressure, SLO, git, and support-bundle facts first.
type CloseoutProofInputs struct {
	ProjectDir            string
	Closeout              CloseoutBundle
	Assurance             assurance.Digest
	Pressure              pressure.RobotPressure
	SLO                   swarmslo.RecommendationSummary
	SupportBundlePath     string
	ArtifactPaths         []string
	MissingSources        []string
	TrackerNeedsFlush     bool
	DirtyWorktree         bool
	StaleReservationAfter time.Duration
	Now                   time.Time
}

// ProofSection is one source's contribution to the closeout proof.
type ProofSection struct {
	Name          string     `json:"name"`
	State         ProofState `json:"state"`
	Summary       string     `json:"summary"`
	ReasonCodes   []string   `json:"reason_codes,omitempty"`
	Evidence      []string   `json:"evidence,omitempty"`
	Commands      []string   `json:"commands,omitempty"`
	ArtifactPaths []string   `json:"artifact_paths,omitempty"`
}

// ProofLogRow is a structured log projection. It carries the fields
// operators and robot consumers need without reinterpreting sections.
type ProofLogRow struct {
	GeneratedAt    time.Time  `json:"generated_at"`
	ProjectDir     string     `json:"project_dir,omitempty"`
	Section        string     `json:"section"`
	QueueState     string     `json:"queue_state"`
	ProofState     ProofState `json:"proof_state"`
	SectionState   ProofState `json:"section_state"`
	MissingSources []string   `json:"missing_sources"`
	ArtifactPath   string     `json:"artifact_path,omitempty"`
	ReasonCodes    []string   `json:"reason_codes,omitempty"`
}

// CloseoutProofBundle composes the closeout bundle with operator
// assurance, pressure, SLO, and support-bundle evidence.
type CloseoutProofBundle struct {
	SchemaVersion   string         `json:"schema_version"`
	GeneratedAt     time.Time      `json:"generated_at"`
	ProjectDir      string         `json:"project_dir,omitempty"`
	ProofState      ProofState     `json:"proof_state"`
	SafeToStandDown bool           `json:"safe_to_stand_down"`
	Sections        []ProofSection `json:"sections"`
	Closeout        CloseoutBundle `json:"closeout"`
	MissingSources  []string       `json:"missing_sources,omitempty"`
	ArtifactPaths   []string       `json:"artifact_paths,omitempty"`
	LogRows         []ProofLogRow  `json:"log_rows"`
}

// BuildCloseout reduces inputs into a CloseoutBundle. Pure: no I/O,
// never infers verification success — Outcome must be supplied.
//
// Residual risks are derived deterministically from inputs:
//   - verification_failed [HIGH]: any Verification.Outcome == failed
//   - verification_inconclusive [MEDIUM]: any Outcome == unknown / skipped
//   - active_reservations_outstanding [MEDIUM]: at least one reservation
//   - unacked_urgent_mail [HIGH]: Mail.UnackedUrgent > 0
//   - pending_ack_mail [LOW]: Mail.PendingAck > 0 (and no urgent)
//   - beads_still_open [MEDIUM]: any bead in StillOpen
//   - providers_degraded [MEDIUM]: any DegradedProviders entry
func BuildCloseout(in CloseoutInputs) CloseoutBundle {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	bundle := CloseoutBundle{
		SchemaVersion:     CloseoutSchemaVersion,
		GeneratedAt:       now.UTC(),
		Run:               in.Run,
		Commits:           append([]CommitEntry(nil), in.Commits...),
		Beads:             dedupedBeadsDelta(in.Beads),
		Verifications:     append([]VerificationEntry(nil), in.Verifications...),
		Reservations:      append([]ReservationSnapshot(nil), in.Reservations...),
		Mail:              in.Mail,
		Queue:             in.Queue,
		DegradedProviders: uniqueSortedCloseout(in.DegradedProviders),
		Notes:             append([]string(nil), in.Notes...),
	}

	// Residual risks first so the count below reflects them. Reversing
	// these two lines silently always reports counts.residual_risks=0
	// regardless of how many entries computeResidualRisks produced
	// (bd-55myk).
	bundle.ResidualRisks = computeResidualRisks(bundle)
	bundle.Counts = computeCloseoutCounts(bundle)

	// Sort emitted lists deterministically so the bundle is byte-stable.
	sort.SliceStable(bundle.Commits, func(i, j int) bool {
		return bundle.Commits[i].Hash < bundle.Commits[j].Hash
	})
	sort.SliceStable(bundle.Reservations, func(i, j int) bool {
		if bundle.Reservations[i].PathPattern != bundle.Reservations[j].PathPattern {
			return bundle.Reservations[i].PathPattern < bundle.Reservations[j].PathPattern
		}
		return bundle.Reservations[i].AgentName < bundle.Reservations[j].AgentName
	})
	// Verifications keep insertion order — operators care about the
	// observed sequence — but the count fields are already aggregated.

	return bundle
}

// BuildCloseoutProofBundle reduces closeout evidence into one
// operator-facing proof artifact. It never gathers evidence itself.
func BuildCloseoutProofBundle(in CloseoutProofInputs) CloseoutProofBundle {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	staleReservationAfter := in.StaleReservationAfter
	if staleReservationAfter <= 0 {
		staleReservationAfter = 30 * time.Minute
	}

	missing := canonicalMissingSources(in.MissingSources)
	artifacts := proofArtifactPaths(in.SupportBundlePath, in.ArtifactPaths)
	out := CloseoutProofBundle{
		SchemaVersion:  CloseoutProofSchemaVersion,
		GeneratedAt:    now.UTC(),
		ProjectDir:     strings.TrimSpace(in.ProjectDir),
		Closeout:       in.Closeout,
		MissingSources: missing.sorted(),
		ArtifactPaths:  artifacts,
	}

	out.Sections = []ProofSection{
		buildQueueProofSection(in.Closeout.Queue, missing),
		buildMailProofSection(in.Closeout.Mail, missing),
		buildReservationProofSection(in.Closeout.Reservations, now, staleReservationAfter, missing),
		buildPressureProofSection(in.Pressure, missing),
		buildSLOProofSection(in.SLO, missing),
		buildAssuranceProofSection(in.Assurance, missing),
		buildGitProofSection(in.Closeout, in.TrackerNeedsFlush, in.DirtyWorktree, missing),
		buildSupportBundleProofSection(artifacts, missing),
	}
	out.ProofState = rollupProofState(out.Sections)
	out.SafeToStandDown = out.ProofState == ProofStatePresent
	out.LogRows = buildProofLogRows(out)
	return out
}

// FormatCloseoutProofMarkdown renders a concise deterministic handoff
// view. The JSON bundle remains the source of truth.
func FormatCloseoutProofMarkdown(b CloseoutProofBundle) string {
	var out strings.Builder
	out.WriteString("# Closeout Proof\n\n")
	out.WriteString("- state: ")
	out.WriteString(string(b.ProofState))
	out.WriteString("\n")
	out.WriteString("- safe_to_stand_down: ")
	if b.SafeToStandDown {
		out.WriteString("true\n")
	} else {
		out.WriteString("false\n")
	}
	if b.ProjectDir != "" {
		out.WriteString("- project_dir: ")
		out.WriteString(markdownCell(b.ProjectDir))
		out.WriteString("\n")
	}
	if len(b.MissingSources) > 0 {
		out.WriteString("- missing_sources: ")
		out.WriteString(strings.Join(b.MissingSources, ","))
		out.WriteString("\n")
	}
	if len(b.ArtifactPaths) > 0 {
		out.WriteString("- artifact_path: ")
		out.WriteString(markdownCell(b.ArtifactPaths[0]))
		out.WriteString("\n")
	}
	out.WriteString("\n| section | state | reasons | summary |\n")
	out.WriteString("| --- | --- | --- | --- |\n")
	for _, s := range b.Sections {
		out.WriteString("| ")
		out.WriteString(markdownCell(s.Name))
		out.WriteString(" | ")
		out.WriteString(string(s.State))
		out.WriteString(" | ")
		out.WriteString(markdownCell(strings.Join(s.ReasonCodes, ",")))
		out.WriteString(" | ")
		out.WriteString(markdownCell(s.Summary))
		out.WriteString(" |\n")
	}
	return out.String()
}

type missingSourceSet map[string]struct{}

func canonicalMissingSources(in []string) missingSourceSet {
	set := make(missingSourceSet, len(in))
	for _, raw := range in {
		canonical := canonicalMissingSource(raw)
		if canonical == "" {
			continue
		}
		set[canonical] = struct{}{}
	}
	return set
}

func canonicalMissingSource(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "":
		return ""
	case "agentmail", "agent_mail", "mail":
		return "agent_mail"
	case "beads", "br", "queue":
		return "queue"
	case "agentmail_reservations", "reservations":
		return "reservations"
	case "pressure":
		return "pressure"
	case "slo", "swarmslo":
		return "slo"
	case "assurance", "digest", "contract":
		return "assurance"
	case "git", "tracker", "worktree":
		return "git"
	case "supportbundle", "support_bundle", "support-bundle":
		return "support_bundle"
	default:
		return strings.TrimSpace(strings.ToLower(raw))
	}
}

func (set missingSourceSet) has(name string) bool {
	_, ok := set[name]
	return ok
}

func (set missingSourceSet) sorted() []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func buildQueueProofSection(q QueueState, missing missingSourceSet) ProofSection {
	if missing.has("queue") {
		return missingProofSection("queue", "beads queue evidence was not loaded", "missing.queue")
	}
	evidence := []string{
		"ready=" + itoa(q.Ready),
		"in_progress=" + itoa(q.InProgress),
		"blocked=" + itoa(q.Blocked),
	}
	if q.Ready > 0 || q.InProgress > 0 || q.Blocked > 0 || !q.QueueDry {
		reasons := make([]string, 0, 3)
		if q.Ready > 0 {
			reasons = append(reasons, string(assurance.ReasonQuiescenceReadyWork))
		}
		if q.InProgress > 0 {
			reasons = append(reasons, string(assurance.ReasonQuiescenceInProgressWork))
		}
		if q.Blocked > 0 {
			reasons = append(reasons, string(assurance.ReasonQuiescencePendingWork))
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "queue.not_dry")
		}
		return ProofSection{
			Name:        "queue",
			State:       ProofStateFailing,
			Summary:     "queue still has operator-visible work",
			ReasonCodes: reasons,
			Evidence:    evidence,
		}
	}
	return ProofSection{
		Name:        "queue",
		State:       ProofStatePresent,
		Summary:     "queue is dry",
		ReasonCodes: []string{string(assurance.ReasonQuiescenceQueueDry)},
		Evidence:    evidence,
	}
}

func buildMailProofSection(mail MailSnapshot, missing missingSourceSet) ProofSection {
	if missing.has("agent_mail") {
		return missingProofSection("mail", "Agent Mail evidence was not loaded", "missing.agent_mail")
	}
	evidence := []string{
		"unacked_urgent=" + itoa(mail.UnackedUrgent),
		"pending_ack=" + itoa(mail.PendingAck),
	}
	switch {
	case mail.UnackedUrgent > 0:
		return ProofSection{
			Name:        "mail",
			State:       ProofStateFailing,
			Summary:     "urgent ack-required mail remains",
			ReasonCodes: []string{string(assurance.ReasonQuiescenceUrgentMail)},
			Evidence:    evidence,
		}
	case mail.PendingAck > 0:
		return ProofSection{
			Name:        "mail",
			State:       ProofStateDegraded,
			Summary:     "mail awaits acknowledgement",
			ReasonCodes: []string{string(assurance.ReasonQuiescencePendingAckMail)},
			Evidence:    evidence,
		}
	default:
		return ProofSection{
			Name:        "mail",
			State:       ProofStatePresent,
			Summary:     "no pending mail acknowledgements",
			ReasonCodes: []string{"mail.clear"},
			Evidence:    evidence,
		}
	}
}

func buildReservationProofSection(reservations []ReservationSnapshot, now time.Time, staleAfter time.Duration, missing missingSourceSet) ProofSection {
	if missing.has("reservations") {
		return missingProofSection("reservations", "reservation evidence was not loaded", "missing.reservations")
	}
	if len(reservations) == 0 {
		return ProofSection{
			Name:        "reservations",
			State:       ProofStatePresent,
			Summary:     "no active reservations",
			ReasonCodes: []string{"reservations.clear"},
		}
	}
	evidence := make([]string, 0, len(reservations))
	stale := false
	for _, r := range reservations {
		age := now.Sub(r.AcquiredAt)
		if !r.AcquiredAt.IsZero() && age >= staleAfter {
			stale = true
		}
		evidence = append(evidence, r.PathPattern+" by "+r.AgentName+" age="+age.Round(time.Second).String())
	}
	sort.Strings(evidence)
	if stale {
		return ProofSection{
			Name:        "reservations",
			State:       ProofStateStale,
			Summary:     "active reservations include stale holds",
			ReasonCodes: []string{string(assurance.ReasonReservationOverdue)},
			Evidence:    evidence,
		}
	}
	return ProofSection{
		Name:        "reservations",
		State:       ProofStateDegraded,
		Summary:     "active reservations remain",
		ReasonCodes: []string{string(assurance.ReasonReservationPathConflict)},
		Evidence:    evidence,
	}
}

func buildPressureProofSection(snapshot pressure.RobotPressure, missing missingSourceSet) ProofSection {
	if missing.has("pressure") || strings.TrimSpace(snapshot.Overall) == "" {
		return missingProofSection("pressure", "pressure evidence was not loaded", "missing.pressure")
	}
	evidence := []string{"overall=" + snapshot.Overall}
	if len(snapshot.Limiting) > 0 {
		evidence = append(evidence, "limiting="+strings.Join(snapshot.Limiting, ","))
	}
	switch snapshot.Overall {
	case "critical":
		return ProofSection{
			Name:        "pressure",
			State:       ProofStateFailing,
			Summary:     "pressure is critical",
			ReasonCodes: []string{"digest.source_degraded.pressure", "pressure.critical"},
			Evidence:    evidence,
		}
	case "high", "elevated":
		return ProofSection{
			Name:        "pressure",
			State:       ProofStateDegraded,
			Summary:     "pressure is " + snapshot.Overall,
			ReasonCodes: []string{"digest.source_degraded.pressure", "pressure." + snapshot.Overall},
			Evidence:    evidence,
		}
	default:
		return ProofSection{
			Name:        "pressure",
			State:       ProofStatePresent,
			Summary:     "pressure is within closeout budget",
			ReasonCodes: []string{"pressure.ok"},
			Evidence:    evidence,
		}
	}
}

func buildSLOProofSection(slo swarmslo.RecommendationSummary, missing missingSourceSet) ProofSection {
	if missing.has("slo") || slo.SchemaVersion == "" {
		return missingProofSection("slo", "SLO evidence was not loaded", "missing.slo")
	}
	reasons := make([]string, 0, len(slo.Recommendations))
	for _, r := range slo.Recommendations {
		for _, code := range r.ReasonCodes {
			reasons = append(reasons, string(code))
		}
	}
	reasons = uniqueSortedCloseout(reasons)
	evidence := []string{"recommendations=" + itoa(len(slo.Recommendations))}
	if len(slo.Warnings) > 0 {
		evidence = append(evidence, "warnings="+strings.Join(slo.Warnings, ","))
	}
	if !slo.Healthy {
		return ProofSection{
			Name:        "slo",
			State:       ProofStateDegraded,
			Summary:     "SLO recommendations require operator attention",
			ReasonCodes: reasons,
			Evidence:    evidence,
		}
	}
	if len(reasons) == 0 {
		reasons = []string{"slo.healthy"}
	}
	return ProofSection{
		Name:        "slo",
		State:       ProofStatePresent,
		Summary:     "SLOs are healthy",
		ReasonCodes: reasons,
		Evidence:    evidence,
	}
}

func buildAssuranceProofSection(digest assurance.Digest, missing missingSourceSet) ProofSection {
	if missing.has("assurance") || digest.Status == "" {
		return missingProofSection("assurance", "assurance digest was not loaded", "missing.assurance")
	}
	reasons := make([]string, 0, len(digest.ReasonCodes))
	for _, c := range digest.ReasonCodes {
		reasons = append(reasons, string(c))
	}
	reasons = uniqueSortedCloseout(reasons)
	evidence := []string{"status=" + string(digest.Status)}
	switch digest.Status {
	case assurance.DigestStatusUnsafe:
		return ProofSection{
			Name:        "assurance",
			State:       ProofStateFailing,
			Summary:     digest.Summary,
			ReasonCodes: reasons,
			Evidence:    evidence,
		}
	case assurance.DigestStatusDegraded:
		return ProofSection{
			Name:        "assurance",
			State:       ProofStateDegraded,
			Summary:     digest.Summary,
			ReasonCodes: reasons,
			Evidence:    evidence,
		}
	default:
		if len(reasons) == 0 {
			reasons = []string{"assurance.healthy"}
		}
		return ProofSection{
			Name:        "assurance",
			State:       ProofStatePresent,
			Summary:     digest.Summary,
			ReasonCodes: reasons,
			Evidence:    evidence,
		}
	}
}

func buildGitProofSection(closeout CloseoutBundle, trackerNeedsFlush, dirtyWorktree bool, missing missingSourceSet) ProofSection {
	if missing.has("git") {
		return missingProofSection("git", "git or tracker evidence was not loaded", "missing.git")
	}
	reasons := []string{}
	evidence := []string{"commits=" + itoa(len(closeout.Commits))}
	state := ProofStatePresent
	summary := "git and tracker evidence is clean"
	if dirtyWorktree {
		state = ProofStateFailing
		summary = "dirty worktree remains"
		reasons = append(reasons, string(assurance.ReasonCloseoutDirtyWorktree))
	}
	if trackerNeedsFlush {
		state = ProofStateFailing
		summary = "tracker sync is dirty"
		reasons = append(reasons, string(assurance.ReasonQuiescenceTrackerDirty))
	}
	if len(closeout.Commits) == 0 && state == ProofStatePresent {
		state = ProofStateMissing
		summary = "recent git commit evidence is missing"
		reasons = append(reasons, string(assurance.ReasonCloseoutNoBeadReference))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "git.clean")
	}
	return ProofSection{
		Name:        "git",
		State:       state,
		Summary:     summary,
		ReasonCodes: uniqueSortedCloseout(reasons),
		Evidence:    evidence,
	}
}

func buildSupportBundleProofSection(artifactPaths []string, missing missingSourceSet) ProofSection {
	if missing.has("support_bundle") {
		return missingProofSection("support_bundle", "support bundle evidence was not loaded", "missing.support_bundle")
	}
	if len(artifactPaths) == 0 {
		return missingProofSection("support_bundle", "support bundle artifact path is missing", "missing.support_bundle")
	}
	return ProofSection{
		Name:          "support_bundle",
		State:         ProofStatePresent,
		Summary:       "support bundle artifact path recorded",
		ReasonCodes:   []string{"support_bundle.present"},
		ArtifactPaths: append([]string(nil), artifactPaths...),
	}
}

func missingProofSection(name, summary, reason string) ProofSection {
	return ProofSection{
		Name:        name,
		State:       ProofStateMissing,
		Summary:     summary,
		ReasonCodes: []string{reason},
	}
}

func rollupProofState(sections []ProofSection) ProofState {
	state := ProofStatePresent
	for _, s := range sections {
		if proofStateRank(s.State) > proofStateRank(state) {
			state = s.State
		}
	}
	return state
}

func proofStateRank(state ProofState) int {
	switch state {
	case ProofStateFailing:
		return 5
	case ProofStateDegraded:
		return 4
	case ProofStateStale:
		return 3
	case ProofStateMissing:
		return 2
	case ProofStatePresent:
		return 1
	default:
		return 0
	}
}

func buildProofLogRows(bundle CloseoutProofBundle) []ProofLogRow {
	rows := make([]ProofLogRow, 0, len(bundle.Sections))
	queueState := "work_remaining"
	if bundle.Closeout.Queue.QueueDry {
		queueState = "queue_dry"
	}
	artifactPath := ""
	if len(bundle.ArtifactPaths) > 0 {
		artifactPath = bundle.ArtifactPaths[0]
	}
	for _, section := range bundle.Sections {
		missingSources := append([]string{}, bundle.MissingSources...)
		rows = append(rows, ProofLogRow{
			GeneratedAt:    bundle.GeneratedAt,
			ProjectDir:     bundle.ProjectDir,
			Section:        section.Name,
			QueueState:     queueState,
			ProofState:     bundle.ProofState,
			SectionState:   section.State,
			MissingSources: missingSources,
			ArtifactPath:   artifactPath,
			ReasonCodes:    append([]string(nil), section.ReasonCodes...),
		})
	}
	return rows
}

func proofArtifactPaths(supportBundlePath string, extra []string) []string {
	paths := make([]string, 0, 1+len(extra))
	if strings.TrimSpace(supportBundlePath) != "" {
		paths = append(paths, supportBundlePath)
	}
	paths = append(paths, extra...)
	return uniqueSortedCloseout(paths)
}

func markdownCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

func dedupedBeadsDelta(b BeadsDelta) BeadsDelta {
	return BeadsDelta{
		Opened:    uniqueSortedCloseout(b.Opened),
		Closed:    uniqueSortedCloseout(b.Closed),
		StillOpen: uniqueSortedCloseout(b.StillOpen),
	}
}

func computeCloseoutCounts(b CloseoutBundle) CloseoutCounts {
	c := CloseoutCounts{
		Commits:            len(b.Commits),
		BeadsOpened:        len(b.Beads.Opened),
		BeadsClosed:        len(b.Beads.Closed),
		BeadsStillOpen:     len(b.Beads.StillOpen),
		Verifications:      len(b.Verifications),
		ActiveReservations: len(b.Reservations),
		ResidualRisks:      len(b.ResidualRisks),
	}
	for _, v := range b.Verifications {
		switch v.Outcome {
		case OutcomePassed:
			c.VerificationsPassed++
		case OutcomeFailed:
			c.VerificationsFailed++
		}
	}
	return c
}

func computeResidualRisks(b CloseoutBundle) []ResidualRisk {
	var risks []ResidualRisk

	// Verification failures (HIGH) — the most serious signal.
	failedCmds := []string{}
	inconclusiveCmds := []string{}
	for _, v := range b.Verifications {
		switch v.Outcome {
		case OutcomeFailed:
			failedCmds = append(failedCmds, v.Command)
		case OutcomeUnknown, OutcomeSkipped:
			inconclusiveCmds = append(inconclusiveCmds, v.Command+":"+string(v.Outcome))
		}
	}
	if len(failedCmds) > 0 {
		sort.Strings(failedCmds)
		risks = append(risks, ResidualRisk{
			Code:        "verification_failed",
			Severity:    RiskSeverityHigh,
			Description: "one or more recorded verifications failed; the run shipped with known regressions",
			Evidence:    failedCmds,
		})
	}
	if len(inconclusiveCmds) > 0 {
		sort.Strings(inconclusiveCmds)
		risks = append(risks, ResidualRisk{
			Code:        "verification_inconclusive",
			Severity:    RiskSeverityMedium,
			Description: "one or more verifications were skipped or returned an unknown outcome; success is not proven",
			Evidence:    inconclusiveCmds,
		})
	}

	// bd-056vx: emit each mail-attention residual risk independently.
	// Pre-fix the LOW pending_ack_mail risk was suppressed by an
	// else-if whenever HIGH unacked_urgent_mail fired — but the LOW
	// risk's description explicitly names itself "non-urgent mail",
	// so the two signals are orthogonal and both must surface when
	// both input counts are non-zero. Mirror of bd-zy2c1 fix in
	// internal/robot/assurance/quiescence.go.
	if b.Mail.UnackedUrgent > 0 {
		risks = append(risks, ResidualRisk{
			Code:        "unacked_urgent_mail",
			Severity:    RiskSeverityHigh,
			Description: "ack-required urgent mail was outstanding at closeout",
			Evidence:    []string{"unacked_urgent=" + itoa(b.Mail.UnackedUrgent)},
		})
	}
	if b.Mail.PendingAck > 0 {
		risks = append(risks, ResidualRisk{
			Code:        "pending_ack_mail",
			Severity:    RiskSeverityLow,
			Description: "non-urgent mail awaits acknowledgement",
			Evidence:    []string{"pending_ack=" + itoa(b.Mail.PendingAck)},
		})
	}

	if len(b.Reservations) > 0 {
		evidence := make([]string, 0, len(b.Reservations))
		for _, r := range b.Reservations {
			evidence = append(evidence, r.PathPattern+" by "+r.AgentName)
		}
		sort.Strings(evidence)
		risks = append(risks, ResidualRisk{
			Code:        "active_reservations_outstanding",
			Severity:    RiskSeverityMedium,
			Description: "file reservations were still held at closeout; downstream agents may collide",
			Evidence:    evidence,
		})
	}

	if len(b.Beads.StillOpen) > 0 {
		risks = append(risks, ResidualRisk{
			Code:        "beads_still_open",
			Severity:    RiskSeverityMedium,
			Description: "beads opened during the run remain open at closeout",
			Evidence:    append([]string(nil), b.Beads.StillOpen...),
		})
	}

	if len(b.DegradedProviders) > 0 {
		risks = append(risks, ResidualRisk{
			Code:        "providers_degraded",
			Severity:    RiskSeverityMedium,
			Description: "one or more coordination providers were degraded during the run; evidence may be incomplete",
			Evidence:    append([]string(nil), b.DegradedProviders...),
		})
	}

	// Sort: high severity first, then code asc, for stable JSON.
	sort.SliceStable(risks, func(i, j int) bool {
		ri := riskRank(risks[i].Severity)
		rj := riskRank(risks[j].Severity)
		if ri < rj || ri > rj {
			return ri > rj
		}
		return risks[i].Code < risks[j].Code
	})
	return risks
}

func riskRank(s ResidualRiskSeverity) int {
	switch s {
	case RiskSeverityHigh:
		return 3
	case RiskSeverityMedium:
		return 2
	case RiskSeverityLow:
		return 1
	default:
		return 0
	}
}

func uniqueSortedCloseout(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
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
