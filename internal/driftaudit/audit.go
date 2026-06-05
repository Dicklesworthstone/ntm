// Package driftaudit compares the set of robot-mode commands and
// flags advertised across NTM's parallel surfaces — capabilities
// JSON, --robot-help text, README/docs snippets where practical, and
// the contract test corpus — and reports any name that exists in
// one surface but not the others.
//
// The audit is pure: callers extract the sets from each surface
// (the live binary's --robot-capabilities / --robot-help, the docs
// markdown, and the contract testdata), then pass plain views in.
// Drift Compare returns a structured report whose top-level shape
// is byte-stable so a regression test can pin it.
//
// See bd-fxj4f.10.
package driftaudit

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Surface names a place where a robot command/flag is documented.
// Stable strings — consumers may route on them.
type Surface string

const (
	SurfaceCapabilities Surface = "capabilities"
	SurfaceHelp         Surface = "help"
	SurfaceDocs         Surface = "docs"
	SurfaceContract     Surface = "contract"
)

// Severity classifies a drift finding. A name missing from
// capabilities is Critical (an agent has no way to discover it); a
// name missing from docs is Warning (humans miss it); a name missing
// from contract is Info (test coverage gap).
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// SurfaceSet is the set of canonical robot names a surface advertises.
// Names should be the canonical command/flag form (e.g.
// "robot-status"). Whitespace and case are normalized by Compare.
type SurfaceSet struct {
	// Surface identifies which surface this set was extracted from.
	// When set, Compare validates it matches the slot the caller
	// passed the SurfaceSet into (Inputs.Capabilities must hold a set
	// labeled SurfaceCapabilities, and so on). A mismatch produces a
	// mislabeled_set finding so a wiring bug — e.g. routing the help
	// extractor's output into the Capabilities slot — is surfaced
	// instead of silently misclassified. Leave empty to opt out of
	// the check (the slot then trusts itself).
	Surface Surface
	// Names is the list of canonical names. Order does not matter;
	// duplicates are folded.
	Names []string
}

// Inputs is the full evidence Compare reduces.
type Inputs struct {
	Capabilities SurfaceSet
	Help         SurfaceSet
	Docs         SurfaceSet
	Contract     SurfaceSet
	// IgnoredNames lists names the audit should skip (e.g.
	// deliberately-internal commands not yet promoted to docs).
	IgnoredNames []string
	// Now is overridable for tests.
	Now time.Time
}

// Finding is one drift row.
type Finding struct {
	Name     string    `json:"name"`
	Severity Severity  `json:"severity"`
	Present  []Surface `json:"present"`
	Missing  []Surface `json:"missing"`
	Summary  string    `json:"summary"`
}

// Report is the full drift assessment.
type Report struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Total       int             `json:"total"`
	Drift       int             `json:"drift"`
	Findings    []Finding       `json:"findings,omitempty"`
	Surfaces    []SurfaceTotals `json:"surfaces"`
}

// SurfaceTotals is the per-surface count rolled up so dashboards can
// summarize without re-traversing Findings.
type SurfaceTotals struct {
	Surface Surface `json:"surface"`
	Count   int     `json:"count"`
}

// Compare produces a Report. For each canonical name across all
// surfaces, if a name appears in some surfaces but not others a
// Finding is emitted with severity:
//   - critical: missing from capabilities (agents cannot discover).
//   - warning: missing from help OR docs (human-visible gap).
//   - info: missing from contract (test coverage gap).
//
// When a name is missing from multiple surfaces the highest severity
// applies.
//
// Names listed in IgnoredNames are skipped entirely.
func Compare(in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	caps := normalizeSet(in.Capabilities.Names)
	help := normalizeSet(in.Help.Names)
	docs := normalizeSet(in.Docs.Names)
	cont := normalizeSet(in.Contract.Names)
	ignored := normalizeSet(in.IgnoredNames)

	// Validate SurfaceSet.Surface labels against the slot they were
	// passed into. Empty Surface opts out (the slot trusts itself).
	// A mismatch is a wiring bug: the data still flows through the
	// slot's normal classification, but the mislabeled_set finding
	// announces the contract violation so the operator can fix the
	// caller (bd-i5da4).
	mislabelFindings := validateSurfaceLabels(in)

	all := make(map[string]struct{})
	for n := range caps {
		all[n] = struct{}{}
	}
	for n := range help {
		all[n] = struct{}{}
	}
	for n := range docs {
		all[n] = struct{}{}
	}
	for n := range cont {
		all[n] = struct{}{}
	}
	for n := range ignored {
		delete(all, n)
	}

	allNames := make([]string, 0, len(all))
	for n := range all {
		allNames = append(allNames, n)
	}
	sort.Strings(allNames)

	report := Report{
		GeneratedAt: now.UTC(),
		Total:       len(allNames),
		Surfaces: []SurfaceTotals{
			{Surface: SurfaceCapabilities, Count: len(caps)},
			{Surface: SurfaceContract, Count: len(cont)},
			{Surface: SurfaceDocs, Count: len(docs)},
			{Surface: SurfaceHelp, Count: len(help)},
		},
	}

	for _, name := range allNames {
		f := classify(name, caps, help, docs, cont)
		if f == nil {
			continue
		}
		report.Findings = append(report.Findings, *f)
	}
	report.Findings = append(report.Findings, mislabelFindings...)
	report.Drift = len(report.Findings)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		ri := severityRank(report.Findings[i].Severity)
		rj := severityRank(report.Findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return report.Findings[i].Name < report.Findings[j].Name
	})

	return report
}

func classify(name string, caps, help, docs, cont map[string]struct{}) *Finding {
	var present, missing []Surface
	if _, ok := caps[name]; ok {
		present = append(present, SurfaceCapabilities)
	} else {
		missing = append(missing, SurfaceCapabilities)
	}
	if _, ok := help[name]; ok {
		present = append(present, SurfaceHelp)
	} else {
		missing = append(missing, SurfaceHelp)
	}
	if _, ok := docs[name]; ok {
		present = append(present, SurfaceDocs)
	} else {
		missing = append(missing, SurfaceDocs)
	}
	if _, ok := cont[name]; ok {
		present = append(present, SurfaceContract)
	} else {
		missing = append(missing, SurfaceContract)
	}
	if len(missing) == 0 {
		return nil // present everywhere
	}
	severity := highestSeverity(missing)
	sort.Slice(present, func(i, j int) bool { return present[i] < present[j] })
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return &Finding{
		Name:     name,
		Severity: severity,
		Present:  present,
		Missing:  missing,
		Summary:  buildSummary(name, missing),
	}
}

// validateSurfaceLabels reports each Inputs slot whose SurfaceSet.Surface
// label disagrees with the slot it was passed into. Empty Surface fields
// are silently accepted — the field is opt-in so existing callers that
// don't set it remain valid.
//
// Each mismatch produces a Finding with Severity=Critical and a
// _mislabeled_set:<slot> Name so the existing severity-then-name sort
// surfaces the wiring bug at the top of the report. The Present /
// Missing fields carry the slot's expected vs. observed label so a
// dashboard can render "this set claimed to be Help but landed in
// Capabilities".
func validateSurfaceLabels(in Inputs) []Finding {
	type slotCheck struct {
		slot     Surface
		observed Surface
	}
	checks := []slotCheck{
		{SurfaceCapabilities, in.Capabilities.Surface},
		{SurfaceHelp, in.Help.Surface},
		{SurfaceDocs, in.Docs.Surface},
		{SurfaceContract, in.Contract.Surface},
	}
	var findings []Finding
	for _, c := range checks {
		if c.observed == "" || c.observed == c.slot {
			continue
		}
		findings = append(findings, Finding{
			Name:     "_mislabeled_set:" + string(c.slot),
			Severity: SeverityCritical,
			Present:  []Surface{c.observed},
			Missing:  []Surface{c.slot},
			Summary: "set passed into the " + string(c.slot) +
				" slot self-identifies as " + string(c.observed) +
				"; data still flows through this slot, but the wiring is wrong",
		})
	}
	return findings
}

// highestSeverity returns the severity of the worst-missing surface.
func highestSeverity(missing []Surface) Severity {
	for _, s := range missing {
		if s == SurfaceCapabilities {
			return SeverityCritical
		}
	}
	for _, s := range missing {
		if s == SurfaceHelp || s == SurfaceDocs {
			return SeverityWarning
		}
	}
	return SeverityInfo
}

func buildSummary(name string, missing []Surface) string {
	parts := make([]string, len(missing))
	for i, s := range missing {
		parts[i] = string(s)
	}
	return name + " missing from: " + strings.Join(parts, ", ")
}

// normalizeSet folds Names into a lower-case, trimmed set so two
// surfaces using slightly different capitalization or whitespace do
// not show as drift.
func normalizeSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, raw := range names {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		s = strings.TrimPrefix(s, "--")
		s = strings.ToLower(s)
		out[s] = struct{}{}
	}
	return out
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

// Pin returns a stable JSON snapshot of the report — useful for
// regression-pinning in tests or for storing alongside the test
// fixtures.
func (r Report) Pin() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
