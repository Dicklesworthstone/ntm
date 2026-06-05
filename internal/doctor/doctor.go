// Package doctor produces a structured readiness assessment for
// large-agent NTM work. It answers "is this host ready to spawn a
// 50-pane swarm?" with a single Score plus per-check findings the
// operator can act on.
//
// The package is pure: callers gather signals (tmux availability,
// session health, Agent Mail reachability, beads freshness, disk +
// memory pressure, rch hints) into plain views and pass them in.
// Doctor never shells out, never touches the network, and never
// mutates state.
//
// See bd-fxj4f.7.
package doctor

import (
	"sort"
	"time"
)

// Severity classifies how seriously a Finding should block large-
// agent work. Critical = block. Warning = proceed cautiously.
// Info = informational only.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// CheckStatus is the per-check rollup. UnknownCheck status means the
// caller could not gather data for that check; it never blocks
// readiness but does cap the overall Score below "perfect".
type CheckStatus string

const (
	CheckPass    CheckStatus = "pass"
	CheckWarn    CheckStatus = "warn"
	CheckFail    CheckStatus = "fail"
	CheckUnknown CheckStatus = "unknown"
)

// Finding is one line in the report. Code is stable so dashboards and
// agents can route on it; Remediation is a free-text hint.
type Finding struct {
	Code        string   `json:"code"`
	Check       string   `json:"check"` // "tmux" | "session" | "mail" | "beads" | "disk" | "memory" | "rch"
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// CheckResult is per-check status the report exposes alongside the
// aggregated findings, so a TUI can colour each tile independently
// of the overall score.
type CheckResult struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// TmuxView captures what doctor needs from the tmux probe.
type TmuxView struct {
	Available     bool
	BinaryVersion string
	ServerRunning bool
	ProbeError    string // non-empty means the probe itself failed
}

// SessionView is a per-session health roll-up the caller already has.
type SessionView struct {
	Name              string
	PaneCount         int
	UnresponsivePanes int
	StalePanes        int
}

// MailView is the Agent Mail reachability signal.
type MailView struct {
	Configured       bool   // true when the operator has Agent Mail wired up
	Reachable        bool   // ping succeeded
	LastError        string // last reachability error, if any
	StaleSnapshot    bool   // last snapshot is older than the freshness window
	StaleSnapshotAge time.Duration
}

// BeadsView is the beads-database freshness signal.
type BeadsView struct {
	HasLocalDB        bool
	IssuesJSONLExists bool
	NeedsFlush        bool
	LastSyncAge       time.Duration
}

// DiskView is the disk-pressure signal. UsedRatio in [0, 1].
type DiskView struct {
	Path          string
	UsedRatio     float64
	FreeBytes     uint64
	WarnRatio     float64 // default 0.85
	CriticalRatio float64 // default 0.95
}

// MemoryView is the host-memory pressure signal.
type MemoryView struct {
	UsedRatio     float64
	WarnRatio     float64
	CriticalRatio float64
}

// RchView is the remote-build-helper hint signal.
type RchView struct {
	Configured     bool
	WorkersHealthy int
	WorkersTotal   int
}

// Inputs is the full evidence the doctor reduces.
type Inputs struct {
	Tmux     TmuxView
	Sessions []SessionView
	Mail     MailView
	Beads    BeadsView
	Disk     DiskView
	Memory   MemoryView
	Rch      RchView

	// FreshnessWindow controls when a stale snapshot or beads sync
	// crosses into a Warn finding. Defaults to 1h.
	FreshnessWindow time.Duration

	// Now is overridable for tests.
	Now time.Time
}

// Report is the full readiness assessment.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Score in [0, 100]. 100 = every check passed; subtract 5 per
	// Warn, 25 per Fail, capped at 0. Unknown checks subtract 2 each
	// so a fully-degraded probe surface still produces a non-zero
	// score with the failures clearly visible.
	Score    int           `json:"score"`
	Status   CheckStatus   `json:"status"`
	Checks   []CheckResult `json:"checks"`
	Findings []Finding     `json:"findings,omitempty"`
	Notes    []string      `json:"notes,omitempty"`
}

// Evaluate runs every check and assembles a Report. Pure: never
// touches the filesystem, network, or any global state.
func Evaluate(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	freshness := in.FreshnessWindow
	if freshness == 0 {
		freshness = 1 * time.Hour
	}

	report := Report{
		GeneratedAt: now.UTC(),
		Notes:       []string{"advisory only; doctor never mutates state"},
	}

	for _, fn := range []func() (CheckResult, []Finding){
		func() (CheckResult, []Finding) { return checkTmux(in.Tmux) },
		func() (CheckResult, []Finding) { return checkSessions(in.Sessions) },
		func() (CheckResult, []Finding) { return checkMail(in.Mail, freshness) },
		func() (CheckResult, []Finding) { return checkBeads(in.Beads, freshness) },
		func() (CheckResult, []Finding) { return checkDisk(in.Disk) },
		func() (CheckResult, []Finding) { return checkMemory(in.Memory) },
		func() (CheckResult, []Finding) { return checkRch(in.Rch) },
	} {
		res, findings := fn()
		report.Checks = append(report.Checks, res)
		report.Findings = append(report.Findings, findings...)
	}

	report.Score = computeScore(report.Checks)
	report.Status = rollupStatus(report.Checks)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		ri := severityRank(report.Findings[i].Severity)
		rj := severityRank(report.Findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if report.Findings[i].Check != report.Findings[j].Check {
			return report.Findings[i].Check < report.Findings[j].Check
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})

	return report
}

func checkTmux(v TmuxView) (CheckResult, []Finding) {
	if v.ProbeError != "" {
		return CheckResult{Name: "tmux", Status: CheckUnknown, Detail: v.ProbeError},
			[]Finding{{
				Code:        "tmux.probe_failed",
				Check:       "tmux",
				Severity:    SeverityWarning,
				Summary:     "tmux probe failed; readiness cannot be confirmed",
				Remediation: "run `tmux -V` directly; install or fix tmux on PATH",
				Evidence:    []string{"probe_error=" + v.ProbeError},
			}}
	}
	if !v.Available {
		return CheckResult{Name: "tmux", Status: CheckFail},
			[]Finding{{
				Code:        "tmux.unavailable",
				Check:       "tmux",
				Severity:    SeverityCritical,
				Summary:     "tmux is not on PATH; large-agent work requires tmux",
				Remediation: "install tmux 3.0+ via the system package manager",
			}}
	}
	if !v.ServerRunning {
		return CheckResult{Name: "tmux", Status: CheckWarn, Detail: "no server running"},
			[]Finding{{
				Code:        "tmux.no_server",
				Check:       "tmux",
				Severity:    SeverityWarning,
				Summary:     "no tmux server is running yet; first spawn will start one",
				Remediation: "ok to ignore if you intend to start a server; otherwise `tmux new-session -d`",
			}}
	}
	return CheckResult{Name: "tmux", Status: CheckPass, Detail: v.BinaryVersion}, nil
}

func checkSessions(sessions []SessionView) (CheckResult, []Finding) {
	if len(sessions) == 0 {
		return CheckResult{Name: "session", Status: CheckPass, Detail: "no sessions yet"}, nil
	}
	var findings []Finding
	worst := CheckPass
	for _, s := range sessions {
		if s.UnresponsivePanes > 0 {
			worst = worseStatus(worst, CheckWarn)
			findings = append(findings, Finding{
				Code:        "session.unresponsive_panes",
				Check:       "session",
				Severity:    SeverityWarning,
				Summary:     "session has unresponsive panes",
				Remediation: "inspect with `ntm --robot-tail=" + s.Name + "` and restart wedged agents",
				Evidence: []string{
					"session=" + s.Name,
					"unresponsive=" + intoa(s.UnresponsivePanes),
				},
			})
		}
		if s.StalePanes > s.PaneCount/2 && s.PaneCount > 0 {
			worst = worseStatus(worst, CheckWarn)
			findings = append(findings, Finding{
				Code:        "session.majority_stale",
				Check:       "session",
				Severity:    SeverityWarning,
				Summary:     "majority of panes in this session are stale",
				Remediation: "consider `ntm work queue-dry` or restart the session",
				Evidence: []string{
					"session=" + s.Name,
					"stale=" + intoa(s.StalePanes) + "/" + intoa(s.PaneCount),
				},
			})
		}
	}
	return CheckResult{Name: "session", Status: worst, Detail: ""}, findings
}

func checkMail(v MailView, freshness time.Duration) (CheckResult, []Finding) {
	if !v.Configured {
		return CheckResult{Name: "mail", Status: CheckPass, Detail: "not configured (skipped)"}, nil
	}
	if !v.Reachable {
		return CheckResult{Name: "mail", Status: CheckFail, Detail: "unreachable"},
			[]Finding{{
				Code:        "mail.unreachable",
				Check:       "mail",
				Severity:    SeverityCritical,
				Summary:     "Agent Mail is configured but unreachable; coordination will silently degrade",
				Remediation: "start the mcp-agent-mail server or set AGENT_MAIL_DISABLED=1 to skip",
				Evidence:    []string{"last_error=" + v.LastError},
			}}
	}
	if v.StaleSnapshot && v.StaleSnapshotAge > freshness {
		return CheckResult{Name: "mail", Status: CheckWarn, Detail: "stale snapshot"},
			[]Finding{{
				Code:        "mail.stale_snapshot",
				Check:       "mail",
				Severity:    SeverityWarning,
				Summary:     "Agent Mail snapshot is older than the freshness window",
				Remediation: "refresh inbox/reservations before claiming new work",
				Evidence:    []string{"age=" + v.StaleSnapshotAge.Round(time.Second).String()},
			}}
	}
	return CheckResult{Name: "mail", Status: CheckPass}, nil
}

func checkBeads(v BeadsView, freshness time.Duration) (CheckResult, []Finding) {
	if !v.HasLocalDB {
		return CheckResult{Name: "beads", Status: CheckPass, Detail: "no local DB (skipped)"}, nil
	}
	if !v.IssuesJSONLExists {
		return CheckResult{Name: "beads", Status: CheckFail, Detail: "issues.jsonl missing"},
			[]Finding{{
				Code:        "beads.jsonl_missing",
				Check:       "beads",
				Severity:    SeverityCritical,
				Summary:     "beads.db exists but issues.jsonl is missing; commits cannot capture state",
				Remediation: "br sync --flush-only && git add .beads/issues.jsonl",
			}}
	}
	if v.NeedsFlush {
		return CheckResult{Name: "beads", Status: CheckWarn, Detail: "needs flush"},
			[]Finding{{
				Code:        "beads.needs_flush",
				Check:       "beads",
				Severity:    SeverityWarning,
				Summary:     "beads.db is newer than issues.jsonl; export not yet synced",
				Remediation: "br sync --flush-only && git add .beads/issues.jsonl",
				Evidence:    []string{"sync_age=" + v.LastSyncAge.Round(time.Second).String()},
			}}
	}
	if v.LastSyncAge > freshness {
		return CheckResult{Name: "beads", Status: CheckWarn, Detail: "stale sync"},
			[]Finding{{
				Code:        "beads.stale_sync",
				Check:       "beads",
				Severity:    SeverityWarning,
				Summary:     "beads sync is older than the freshness window",
				Remediation: "br sync --flush-only to refresh issues.jsonl",
				Evidence:    []string{"sync_age=" + v.LastSyncAge.Round(time.Second).String()},
			}}
	}
	return CheckResult{Name: "beads", Status: CheckPass}, nil
}

func checkDisk(v DiskView) (CheckResult, []Finding) {
	warn := v.WarnRatio
	if warn == 0 {
		warn = 0.85
	}
	crit := v.CriticalRatio
	if crit == 0 {
		crit = 0.95
	}
	if v.UsedRatio <= 0 {
		return CheckResult{Name: "disk", Status: CheckUnknown, Detail: "no probe data"},
			[]Finding{{
				Code:     "disk.no_probe",
				Check:    "disk",
				Severity: SeverityInfo,
				Summary:  "disk usage probe returned zero; check skipped",
			}}
	}
	if v.UsedRatio >= crit {
		return CheckResult{Name: "disk", Status: CheckFail},
			[]Finding{{
				Code:        "disk.critical",
				Check:       "disk",
				Severity:    SeverityCritical,
				Summary:     "disk usage above critical threshold; large-agent work may stall",
				Remediation: "free space on " + v.Path + " before spawning more panes",
				Evidence:    []string{floatPercentEvidence("used_ratio", v.UsedRatio)},
			}}
	}
	if v.UsedRatio >= warn {
		return CheckResult{Name: "disk", Status: CheckWarn},
			[]Finding{{
				Code:        "disk.elevated",
				Check:       "disk",
				Severity:    SeverityWarning,
				Summary:     "disk usage is elevated",
				Remediation: "monitor " + v.Path + "; consider trimming caches",
				Evidence:    []string{floatPercentEvidence("used_ratio", v.UsedRatio)},
			}}
	}
	return CheckResult{Name: "disk", Status: CheckPass}, nil
}

func checkMemory(v MemoryView) (CheckResult, []Finding) {
	warn := v.WarnRatio
	if warn == 0 {
		warn = 0.80
	}
	crit := v.CriticalRatio
	if crit == 0 {
		crit = 0.92
	}
	if v.UsedRatio <= 0 {
		// bd-a7ky6: mirror disk/rch's no_probe Info finding so a TUI or
		// agent iterating Report.Findings sees memory's probe failure
		// surface the same way disk's does, instead of being silently
		// observable only via CheckResult.Status.
		return CheckResult{Name: "memory", Status: CheckUnknown, Detail: "no probe data"},
			[]Finding{{
				Code:     "memory.no_probe",
				Check:    "memory",
				Severity: SeverityInfo,
				Summary:  "memory usage probe returned zero; check skipped",
			}}
	}
	if v.UsedRatio >= crit {
		return CheckResult{Name: "memory", Status: CheckFail},
			[]Finding{{
				Code:        "memory.critical",
				Check:       "memory",
				Severity:    SeverityCritical,
				Summary:     "host memory usage above critical threshold",
				Remediation: "shut down idle panes or wait before launching a large swarm",
				Evidence:    []string{floatPercentEvidence("used_ratio", v.UsedRatio)},
			}}
	}
	if v.UsedRatio >= warn {
		return CheckResult{Name: "memory", Status: CheckWarn},
			[]Finding{{
				Code:        "memory.elevated",
				Check:       "memory",
				Severity:    SeverityWarning,
				Summary:     "host memory usage is elevated",
				Remediation: "consider rch offload for compilation-heavy work",
				Evidence:    []string{floatPercentEvidence("used_ratio", v.UsedRatio)},
			}}
	}
	return CheckResult{Name: "memory", Status: CheckPass}, nil
}

func checkRch(v RchView) (CheckResult, []Finding) {
	if !v.Configured {
		return CheckResult{Name: "rch", Status: CheckPass, Detail: "not configured (skipped)"}, nil
	}
	if v.WorkersTotal == 0 {
		return CheckResult{Name: "rch", Status: CheckUnknown, Detail: "no worker info"},
			[]Finding{{
				Code:     "rch.no_workers_known",
				Check:    "rch",
				Severity: SeverityInfo,
				Summary:  "rch is configured but worker count is unknown",
			}}
	}
	if v.WorkersHealthy == 0 {
		return CheckResult{Name: "rch", Status: CheckFail},
			[]Finding{{
				Code:        "rch.no_healthy_workers",
				Check:       "rch",
				Severity:    SeverityCritical,
				Summary:     "rch is configured but zero workers are healthy; offloads will fail",
				Remediation: "rch workers probe --all; restart unhealthy workers",
				Evidence:    []string{"healthy=0/" + intoa(v.WorkersTotal)},
			}}
	}
	if v.WorkersHealthy < v.WorkersTotal {
		return CheckResult{Name: "rch", Status: CheckWarn},
			[]Finding{{
				Code:        "rch.degraded",
				Check:       "rch",
				Severity:    SeverityWarning,
				Summary:     "some rch workers are unhealthy; throughput will be reduced",
				Remediation: "rch workers probe --all to identify the bad workers",
				Evidence:    []string{"healthy=" + intoa(v.WorkersHealthy) + "/" + intoa(v.WorkersTotal)},
			}}
	}
	return CheckResult{Name: "rch", Status: CheckPass}, nil
}

// computeScore returns the readiness score in [0, 100]. Each Pass
// counts full; Warn -5; Fail -25; Unknown -2.
func computeScore(checks []CheckResult) int {
	score := 100
	for _, c := range checks {
		switch c.Status {
		case CheckWarn:
			score -= 5
		case CheckFail:
			score -= 25
		case CheckUnknown:
			score -= 2
		}
	}
	if score < 0 {
		score = 0
	}
	return score
}

// rollupStatus returns the worst per-check status as the overall.
func rollupStatus(checks []CheckResult) CheckStatus {
	worst := CheckPass
	for _, c := range checks {
		worst = worseStatus(worst, c.Status)
	}
	return worst
}

func worseStatus(a, b CheckStatus) CheckStatus {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

func statusRank(s CheckStatus) int {
	switch s {
	case CheckFail:
		return 4
	case CheckWarn:
		return 3
	case CheckUnknown:
		return 2
	case CheckPass:
		return 1
	default:
		return 0
	}
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

func floatPercentEvidence(key string, ratio float64) string {
	pct := int(ratio * 100)
	return key + "=" + intoa(pct) + "%"
}

func intoa(n int) string {
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
