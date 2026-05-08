// Package fairness analyzes a sequence of completed scheduler/
// assignment dispatches for starvation and monopoly risks. It does
// not run the scheduler — callers feed in observed dispatch events
// (which lane, which agent type, which priority) and Detect returns
// a structured Report. Pure: no I/O, no global state.
//
// Two risks the package surfaces:
//
//   - Lane starvation: a lane with eligible work that received zero
//     dispatches over the observed window.
//   - Agent-type monopoly: a single agent type took an outsized
//     fraction of dispatches over the window, even though other
//     types had eligible work.
//
// Both checks are heuristic and deterministic. Thresholds default to
// values tuned for medium-scale swarm runs and can be tuned per call.
//
// See bd-fxj4f.8.
package fairness

import (
	"sort"
	"strings"
	"time"
)

// Severity classifies how seriously a Finding affects fairness.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Dispatch is one completed assignment the scheduler made. Fields
// are minimal so callers can map from any source — assignment
// audit, scheduler trace, or a synthetic harness.
type Dispatch struct {
	Lane      string    // logical work lane (label or category)
	AgentType string    // claude / cc / codex / cod / gemini / gmi / ...
	Priority  int       // 0=critical … 4=backlog (matches beads convention)
	At        time.Time // when the dispatch occurred
}

// LaneEligibility describes whether a lane had eligible work in the
// observed window. The detector uses this to distinguish "starved
// because no work existed" (not a finding) from "starved because the
// scheduler skipped it" (a finding).
type LaneEligibility struct {
	Lane            string
	HadEligibleWork bool
}

// Inputs is the full evidence Detect reduces.
type Inputs struct {
	Dispatches  []Dispatch
	Lanes       []LaneEligibility
	WindowStart time.Time
	WindowEnd   time.Time
	// MonopolyRatioWarn is the per-agent-type dispatch fraction that
	// triggers a warning. Sentinel handling:
	//   - zero      → use default (0.70, one type taking 70 %+)
	//   - negative  → disable the warning check entirely
	//   - positive  → use that fraction as the threshold
	//
	// Pass a small negative value like -1 to suppress the check; a
	// literal 0 is intentionally NOT the disable sentinel because
	// callers that leave the field unset get the recommended default
	// rather than accidentally turning off the detector.
	MonopolyRatioWarn float64
	// MonopolyRatioCritical is the per-agent-type dispatch fraction
	// that triggers a critical finding. Same sentinel handling as
	// MonopolyRatioWarn:
	//   - zero      → use default (0.90)
	//   - negative  → disable the critical check
	//   - positive  → use that fraction as the threshold
	//
	// Disabling the critical check while leaving warn enabled is
	// supported (the warning path still fires on its own threshold).
	MonopolyRatioCritical float64
	// Now lets tests pin the wall clock.
	Now time.Time
}

// LaneStats is one lane's per-window rollup, surfaced in the report
// alongside Findings so dashboards can render the full distribution.
type LaneStats struct {
	Lane        string `json:"lane"`
	Dispatches  int    `json:"dispatches"`
	Eligible    bool   `json:"eligible"`
	StarvedRisk bool   `json:"starved_risk"`
}

// AgentTypeStats is per-agent-type dispatch share over the window.
type AgentTypeStats struct {
	AgentType  string  `json:"agent_type"`
	Dispatches int     `json:"dispatches"`
	Share      float64 `json:"share"` // [0, 1]
}

// Finding is one detected fairness risk.
type Finding struct {
	Code        string   `json:"code"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// Report is the full assessment.
type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	WindowStart time.Time        `json:"window_start,omitempty"`
	WindowEnd   time.Time        `json:"window_end,omitempty"`
	Total       int              `json:"total_dispatches"`
	Lanes       []LaneStats      `json:"lanes,omitempty"`
	AgentTypes  []AgentTypeStats `json:"agent_types,omitempty"`
	Findings    []Finding        `json:"findings,omitempty"`
}

// Detect reduces inputs into a Report. Pure: no I/O.
func Detect(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	// Zero selects the documented default. Negative is the documented
	// "disable this check" sentinel and is passed through untouched
	// so detectMonopoly can short-circuit the corresponding gate
	// (bd-vfwnv).
	monopolyWarn := in.MonopolyRatioWarn
	if monopolyWarn == 0 {
		monopolyWarn = 0.70
	}
	monopolyCrit := in.MonopolyRatioCritical
	if monopolyCrit == 0 {
		monopolyCrit = 0.90
	}

	// Filter dispatches into the window if both bounds set.
	dispatches := filterWindow(in.Dispatches, in.WindowStart, in.WindowEnd)

	report := Report{
		GeneratedAt: now.UTC(),
		WindowStart: in.WindowStart,
		WindowEnd:   in.WindowEnd,
		Total:       len(dispatches),
	}

	report.Lanes = computeLaneStats(dispatches, in.Lanes)
	report.AgentTypes = computeAgentTypeStats(dispatches)
	report.Findings = append(report.Findings, detectStarvation(report.Lanes)...)
	report.Findings = append(report.Findings, detectMonopoly(report.AgentTypes, monopolyWarn, monopolyCrit)...)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		ri := severityRank(report.Findings[i].Severity)
		rj := severityRank(report.Findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})

	return report
}

func filterWindow(d []Dispatch, start, end time.Time) []Dispatch {
	if start.IsZero() && end.IsZero() {
		return d
	}
	out := make([]Dispatch, 0, len(d))
	for _, x := range d {
		if !start.IsZero() && x.At.Before(start) {
			continue
		}
		if !end.IsZero() && x.At.After(end) {
			continue
		}
		out = append(out, x)
	}
	return out
}

func computeLaneStats(dispatches []Dispatch, eligibility []LaneEligibility) []LaneStats {
	count := make(map[string]int)
	known := make(map[string]struct{})
	for _, d := range dispatches {
		l := strings.TrimSpace(d.Lane)
		if l == "" {
			continue
		}
		count[l]++
		known[l] = struct{}{}
	}
	eligibleSet := make(map[string]bool, len(eligibility))
	for _, e := range eligibility {
		l := strings.TrimSpace(e.Lane)
		if l == "" {
			continue
		}
		eligibleSet[l] = e.HadEligibleWork
		known[l] = struct{}{}
	}

	out := make([]LaneStats, 0, len(known))
	for l := range known {
		eligible, hadElig := eligibleSet[l]
		_ = hadElig
		stat := LaneStats{
			Lane:       l,
			Dispatches: count[l],
			Eligible:   eligible,
		}
		stat.StarvedRisk = stat.Eligible && stat.Dispatches == 0
		out = append(out, stat)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StarvedRisk != out[j].StarvedRisk {
			return out[i].StarvedRisk // true sorts first
		}
		if out[i].Dispatches != out[j].Dispatches {
			return out[i].Dispatches < out[j].Dispatches
		}
		return out[i].Lane < out[j].Lane
	})
	return out
}

func computeAgentTypeStats(dispatches []Dispatch) []AgentTypeStats {
	if len(dispatches) == 0 {
		return nil
	}
	count := make(map[string]int)
	total := 0
	for _, d := range dispatches {
		t := strings.TrimSpace(d.AgentType)
		if t == "" {
			continue
		}
		count[t]++
		total++
	}
	if total == 0 {
		return nil
	}
	out := make([]AgentTypeStats, 0, len(count))
	typedTotal := float64(total)
	for t, c := range count {
		share := float64(c) / typedTotal
		out = append(out, AgentTypeStats{
			AgentType:  t,
			Dispatches: c,
			Share:      round3(share),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dispatches != out[j].Dispatches {
			return out[i].Dispatches > out[j].Dispatches
		}
		return out[i].AgentType < out[j].AgentType
	})
	return out
}

func detectStarvation(lanes []LaneStats) []Finding {
	var starved []string
	for _, l := range lanes {
		if l.StarvedRisk {
			starved = append(starved, l.Lane)
		}
	}
	if len(starved) == 0 {
		return nil
	}
	sort.Strings(starved)
	return []Finding{{
		Code:        "lane_starvation",
		Severity:    SeverityCritical,
		Summary:     "lanes with eligible work received zero dispatches in the observed window",
		Remediation: "inspect scheduler priority + label-filter logic for these lanes; consider raising priority or relaxing filters",
		Evidence:    starved,
	}}
}

func detectMonopoly(stats []AgentTypeStats, warn, crit float64) []Finding {
	if len(stats) <= 1 {
		// One type or zero — monopoly is a degenerate concept.
		return nil
	}
	top := stats[0]

	// Negative thresholds are the documented "disable this check"
	// sentinel (bd-vfwnv). Each gate is independent: disabling the
	// warn check while leaving crit enabled still emits criticals,
	// and vice versa.
	warnEnabled := warn > 0
	critEnabled := crit > 0
	fireCritical := critEnabled && top.Share >= crit
	fireWarning := warnEnabled && top.Share >= warn

	if !fireCritical && !fireWarning {
		return nil
	}

	severity := SeverityWarning
	code := "agent_type_monopoly_warning"
	summary := "one agent type dominated dispatch in the observed window"
	if fireCritical {
		severity = SeverityCritical
		code = "agent_type_monopoly_critical"
		summary = "one agent type monopolized dispatch in the observed window"
	}
	evidence := []string{
		"top_type=" + top.AgentType,
		"share=" + formatRatio(top.Share),
	}
	for _, s := range stats[1:] {
		evidence = append(evidence, s.AgentType+"="+formatRatio(s.Share))
	}
	return []Finding{{
		Code:        code,
		Severity:    severity,
		Summary:     summary,
		Remediation: "inspect agent_type filters in dispatch; consider quotas if some types are intentionally excluded",
		Evidence:    evidence,
	}}
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

func formatRatio(v float64) string {
	pct := int(v*1000+0.5) / 10 // one decimal place
	dec := int(v*1000+0.5) % 10
	return itoa(pct) + "." + itoa(dec) + "%"
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
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
