// Package parity compares the same session/project state as rendered
// through robot JSON and TUI-facing view models. The first slice
// avoids brittle terminal screenshots: it compares stable view-model
// fields (session names, pane indices, agent types, statuses, counts)
// extracted from each surface and reports per-field drift.
//
// The harness is pure: callers extract two SnapshotView slices — one
// from the robot JSON envelope, one from the TUI's render-time view
// models — and Compare returns a structured Report. The two surfaces
// are expected to converge on the same shape; any drift indicates a
// rendering bug, a stale field, or a contract violation that would
// otherwise reach an operator as a confusing UI inconsistency.
//
// See bd-fxj4f.13.
package parity

import (
	"sort"
	"strings"
	"time"
)

// Severity classifies the impact of a parity drift.
type Severity string

const (
	// SeverityCritical: a session, pane, or agent visible on one
	// surface is missing from the other. Operators see different
	// state depending on which surface they look at.
	SeverityCritical Severity = "critical"

	// SeverityWarning: both surfaces agree on identity but disagree
	// on a stable field (status, agent type, counts). Operators
	// still see the same set of things, just different details.
	SeverityWarning Severity = "warning"

	// SeverityInfo: a field is present on one surface and absent on
	// the other but is not critical (e.g. a TUI-only annotation).
	SeverityInfo Severity = "info"
)

// SnapshotView is the parity-focused subset of one entity (session,
// pane, or agent) on either surface. The shape is intentionally
// narrow: only fields BOTH surfaces should agree on. Callers map
// from robot.RobotResponse and tui.ViewModel into this type.
type SnapshotView struct {
	// Kind groups views by entity type so Compare matches sessions
	// against sessions, panes against panes, etc.
	Kind string // "session" | "pane" | "agent"
	// ID is the canonical key for matching across surfaces.
	ID string
	// Name is a free-form label used in diagnostics.
	Name string
	// Status is the operator-visible status string. Empty means
	// "not present" on this surface.
	Status string
	// AgentType is the per-pane/per-agent type identifier.
	AgentType string
	// PaneIndex (panes only) is the integer pane number.
	PaneIndex int
	// Count is a generic counter for fields like "panes" on a
	// session view; semantics depend on Kind.
	Count int
	// Extra holds additional simple key=value fields the parity
	// harness should compare. Keys not in both surfaces' Extra are
	// reported as info-severity drift.
	Extra map[string]string
}

// Inputs is the full evidence Compare reduces.
type Inputs struct {
	Robot []SnapshotView // canonical robot-mode JSON view
	TUI   []SnapshotView // TUI render-time view models
	// IgnoredFields lets the caller suppress known-divergent fields
	// (e.g. a TUI-only "highlighted" boolean). Names match the
	// Drift.Field values emitted by Compare.
	IgnoredFields []string
	// Now is overridable for tests.
	Now time.Time
}

// Drift is one row in the report describing a parity gap.
type Drift struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Field    string   `json:"field"`
	Severity Severity `json:"severity"`
	Robot    string   `json:"robot,omitempty"`
	TUI      string   `json:"tui,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// Report is the full parity assessment.
type Report struct {
	GeneratedAt    time.Time `json:"generated_at"`
	RobotCount     int       `json:"robot_count"`
	TUICount       int       `json:"tui_count"`
	Drifts         []Drift   `json:"drifts,omitempty"`
	Counts         Counts    `json:"counts"`
	Summary        string    `json:"summary"`
}

// Counts breaks the drift list down by severity for dashboard rollup.
type Counts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// Compare reduces Inputs into a Report. Pure: no I/O.
func Compare(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	ignored := make(map[string]struct{}, len(in.IgnoredFields))
	for _, f := range in.IgnoredFields {
		ignored[strings.TrimSpace(f)] = struct{}{}
	}

	report := Report{
		GeneratedAt: now.UTC(),
		RobotCount:  len(in.Robot),
		TUICount:    len(in.TUI),
	}

	robotMap := indexByKindID(in.Robot)
	tuiMap := indexByKindID(in.TUI)

	allKeys := make(map[string]struct{})
	for k := range robotMap {
		allKeys[k] = struct{}{}
	}
	for k := range tuiMap {
		allKeys[k] = struct{}{}
	}
	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		r, hasR := robotMap[k]
		u, hasU := tuiMap[k]
		switch {
		case hasR && !hasU:
			report.Drifts = append(report.Drifts, Drift{
				Kind: r.Kind, ID: r.ID, Field: "presence",
				Severity: SeverityCritical,
				Robot:    "present", TUI: "missing",
				Detail: r.Name + " visible on robot but not TUI",
			})
		case hasU && !hasR:
			report.Drifts = append(report.Drifts, Drift{
				Kind: u.Kind, ID: u.ID, Field: "presence",
				Severity: SeverityCritical,
				Robot:    "missing", TUI: "present",
				Detail: u.Name + " visible on TUI but not robot",
			})
		default:
			report.Drifts = append(report.Drifts, compareViews(r, u, ignored)...)
		}
	}

	sort.SliceStable(report.Drifts, func(i, j int) bool {
		ri := severityRank(report.Drifts[i].Severity)
		rj := severityRank(report.Drifts[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if report.Drifts[i].Kind != report.Drifts[j].Kind {
			return report.Drifts[i].Kind < report.Drifts[j].Kind
		}
		if report.Drifts[i].ID != report.Drifts[j].ID {
			return report.Drifts[i].ID < report.Drifts[j].ID
		}
		return report.Drifts[i].Field < report.Drifts[j].Field
	})

	for _, d := range report.Drifts {
		switch d.Severity {
		case SeverityCritical:
			report.Counts.Critical++
		case SeverityWarning:
			report.Counts.Warning++
		case SeverityInfo:
			report.Counts.Info++
		}
	}
	report.Summary = composeSummary(report)
	return report
}

func indexByKindID(views []SnapshotView) map[string]SnapshotView {
	out := make(map[string]SnapshotView, len(views))
	for _, v := range views {
		k := strings.TrimSpace(v.Kind) + ":" + strings.TrimSpace(v.ID)
		if k == ":" {
			continue
		}
		out[k] = v
	}
	return out
}

// compareViews returns drifts for one pair of present-on-both views.
func compareViews(r, u SnapshotView, ignored map[string]struct{}) []Drift {
	var drifts []Drift
	add := func(field, robot, tui, detail string, sev Severity) {
		if _, skip := ignored[field]; skip {
			return
		}
		drifts = append(drifts, Drift{
			Kind: r.Kind, ID: r.ID, Field: field,
			Severity: sev, Robot: robot, TUI: tui, Detail: detail,
		})
	}
	if r.Status != u.Status {
		add("status", r.Status, u.Status, "", SeverityWarning)
	}
	if r.AgentType != u.AgentType {
		add("agent_type", r.AgentType, u.AgentType, "", SeverityWarning)
	}
	if r.Kind == "pane" && r.PaneIndex != u.PaneIndex {
		add("pane_index", itoa(r.PaneIndex), itoa(u.PaneIndex), "", SeverityWarning)
	}
	if r.Count != u.Count {
		add("count", itoa(r.Count), itoa(u.Count), "", SeverityWarning)
	}
	if r.Name != u.Name {
		add("name", r.Name, u.Name, "label-only difference", SeverityInfo)
	}
	// Extra: surface every key whose value differs or is missing on
	// one side. Same-value keys produce no drift.
	for k, rv := range r.Extra {
		uv, ok := u.Extra[k]
		if !ok {
			add("extra."+k, rv, "", "key missing on TUI", SeverityInfo)
			continue
		}
		if rv != uv {
			add("extra."+k, rv, uv, "value differs", SeverityWarning)
		}
	}
	for k, uv := range u.Extra {
		if _, ok := r.Extra[k]; ok {
			continue
		}
		add("extra."+k, "", uv, "key missing on robot", SeverityInfo)
	}
	return drifts
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

func composeSummary(r Report) string {
	parts := []string{
		"robot=" + itoa(r.RobotCount),
		"tui=" + itoa(r.TUICount),
		"drifts=" + itoa(len(r.Drifts)),
	}
	if r.Counts.Critical > 0 {
		parts = append(parts, "critical="+itoa(r.Counts.Critical))
	}
	if r.Counts.Warning > 0 {
		parts = append(parts, "warning="+itoa(r.Counts.Warning))
	}
	if r.Counts.Info > 0 {
		parts = append(parts, "info="+itoa(r.Counts.Info))
	}
	return strings.Join(parts, " ")
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
