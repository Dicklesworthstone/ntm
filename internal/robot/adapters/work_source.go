package adapters

import (
	"context"
	"errors"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// WorkVerificationPolicy permits ordinary local development by default. Strict
// source or program policies apply only when explicitly supplied by a caller.
type WorkVerificationPolicy struct {
	Source        worksource.Policy
	ProgramLabels []string
}

// WorkVerification distinguishes a verified preview from a tool's unchecked
// total. It is not proof of live Agent Mail reservations or an atomic claim.
type WorkVerification struct {
	Source        *worksource.Identity   `json:"source,omitempty"`
	Dirty         bool                   `json:"dirty,omitempty"`
	Excluded      []worksource.Exclusion `json:"excluded"`
	ReasonCode    string                 `json:"reason_code,omitempty"`
	ReportedReady int                    `json:"reported_ready"`
	CountScope    string                 `json:"count_scope"`
	Remediation   string                 `json:"remediation,omitempty"`
	Mismatch      *worksource.StaleError `json:"mismatch,omitempty"`
}

func (a *WorkCoordinationAdapter) collectVerifiedWork(ctx context.Context) (*WorkSection, error) {
	return collectWorkWithSource(ctx, a.config.ProjectDir, a.config.VerificationPolicy, a.collectWork)
}

// collectWorkWithSource brackets the existing collector with source checks.
// Missing exports preserve the existing DB-only mode but are never represented
// as JSONL-verified. An export appearing during that collection is a mismatch.
func collectWorkWithSource(ctx context.Context, project string, policy WorkVerificationPolicy, collect func(context.Context) (*WorkSection, error)) (*WorkSection, error) {
	if ctx == nil || collect == nil {
		err := errors.New("work verification requires a context and collector")
		return rejectWorkSource(nil, err), err
	}
	if err := ctx.Err(); err != nil {
		return rejectWorkSource(nil, err), err
	}
	if strings.TrimSpace(project) == "" {
		err := &worksource.StaleError{Reason: "explicit work project is required"}
		return rejectWorkSource(nil, err), err
	}
	identity, err := worksource.Capture(ctx, project)
	if err != nil {
		err = workSourceFailure(err)
		return rejectWorkSource(nil, err), err
	}
	strict := policy.Source.Expected != nil || policy.Source.RequiredRef != "" || policy.Source.RequireClean || len(policy.ProgramLabels) > 0
	if !identity.Bound() && !strict {
		work, err := collect(ctx)
		if err != nil {
			return rejectWorkSource(work, err), err
		}
		current, err := worksource.Capture(ctx, project)
		if err == nil {
			err = identity.Verify(current)
		}
		if err != nil {
			err = workSourceFailure(err)
			return rejectWorkSource(work, err), err
		}
		out := copyWorkForVerification(work)
		out.Verification = &WorkVerification{
			Excluded: []worksource.Exclusion{}, CountScope: "tool_reported_unverified",
			ReportedReady: reportedWorkReady(work),
			Remediation:   "No canonical JSONL export is available; tool results are not source-verified. Final claim and reservation checks remain required.",
		}
		return out, nil
	}
	if policy.Source.Expected == nil && identity.Bound() {
		policy.Source.Expected = &identity
	}
	source, err := worksource.Read(ctx, project, policy.Source)
	if err != nil {
		return rejectWorkSource(nil, err), err
	}
	work, err := collect(ctx)
	if err != nil {
		return rejectWorkSource(work, err), err
	}
	// Recheck strict policy as well as identity. Cleanliness and required refs
	// may change during collection without changing the captured HEAD/JSONL.
	policy.Source.Expected = &source.Identity
	current, err := worksource.Read(ctx, project, policy.Source)
	if err != nil {
		return rejectWorkSource(work, err), err
	}
	return filterVerifiedWork(work, current, worksource.EligibilityPolicy{
		GatedLabels:   bv.OperatorGatedLabelsForProject(project),
		ProgramLabels: policy.ProgramLabels,
	}), nil
}

func workSourceFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, worksource.ErrStale) {
		return err
	}
	return &worksource.StaleError{Reason: "cannot verify canonical work source", Cause: err}
}

func filterVerifiedWork(work *WorkSection, source *worksource.Snapshot, policy worksource.EligibilityPolicy) *WorkSection {
	if source == nil || !source.Identity.Bound() {
		return rejectWorkSource(work, &worksource.StaleError{Reason: "canonical work source is missing"})
	}
	out := copyWorkForVerification(work)
	ids := make([]string, 0, len(out.Ready))
	for _, item := range out.Ready {
		ids = append(ids, item.ID)
	}
	verdict := source.Filter(ids, policy)
	allowed := make(map[string]bool, len(verdict.EligibleIDs))
	for _, id := range verdict.EligibleIDs {
		allowed[id] = true
	}
	ready := make([]WorkItem, 0, len(allowed))
	for _, item := range out.Ready {
		item.ID = strings.TrimSpace(item.ID)
		if allowed[item.ID] {
			// Canonical eligibility proved no unresolved blocker; do not retain
			// a stale triage blocker list on a verified ready row.
			item.BlockedBy = nil
			ready = append(ready, item)
			delete(allowed, item.ID)
		}
	}
	out.Ready = ready
	identity := source.Identity
	out.Verification = &WorkVerification{
		Source: &identity, Dirty: source.Dirty, Excluded: verdict.Excluded,
		ReasonCode: verdict.ReasonCode, CountScope: "verified_preview",
		ReportedReady: reportedWorkReady(work),
	}
	if out.Summary != nil {
		out.Summary.Ready = len(out.Ready)
	}
	if out.Triage != nil {
		out.Triage.ReadyCount = len(out.Ready)
		if top := out.Triage.TopRecommendation; top != nil {
			visible := false
			for _, item := range out.Ready {
				if item.ID == strings.TrimSpace(top.ID) {
					visible = true
					break
				}
			}
			if !visible {
				out.Triage.TopRecommendation = nil
			}
		}
	}
	if len(out.Ready) == 0 {
		out.Verification.Remediation = "No collected candidate passed canonical eligibility checks; inspect exclusions or collect a larger preview."
	}
	return out
}

// Optional triage enrichment must not swallow a source mismatch and present
// the same collection as a healthy snapshot.
func isStaleWorkSourceError(err error) bool {
	return errors.Is(err, worksource.ErrStale)
}

func rejectWorkSource(work *WorkSection, err error) *WorkSection {
	out := copyWorkForVerification(work)
	out.Verification = &WorkVerification{
		CountScope: "unverified", ReportedReady: reportedWorkReady(work),
		Excluded:    []worksource.Exclusion{},
		Remediation: "Recollect work from the intended local checkout; do not repair or dispatch from the stale projection.",
	}
	if isStaleWorkSourceError(err) {
		out.Verification.ReasonCode = worksource.StaleCode
	}
	for _, item := range out.Ready {
		out.Verification.Excluded = append(out.Verification.Excluded, worksource.Exclusion{ID: item.ID, Reasons: []string{"source_unverified"}})
	}
	out.Ready = []WorkItem{}
	if out.Summary != nil {
		out.Summary.Ready = 0
	}
	if out.Triage != nil {
		out.Triage.ReadyCount = 0
		out.Triage.TopRecommendation = nil
	}
	out.Available = false
	if err != nil {
		out.Reason = err.Error()
		errors.As(err, &out.Verification.Mismatch)
	}
	return out
}

func reportedWorkReady(work *WorkSection) int {
	if work == nil {
		return 0
	}
	if work.Verification != nil {
		return work.Verification.ReportedReady
	}
	if work.Summary != nil {
		return work.Summary.Ready
	}
	return len(work.Ready)
}

func copyWorkForVerification(work *WorkSection) *WorkSection {
	if work == nil {
		return NewWorkSection()
	}
	out := *work
	if work.Summary != nil {
		summary := *work.Summary
		out.Summary = &summary
	}
	if work.Triage != nil {
		triage := *work.Triage
		out.Triage = &triage
	}
	return &out
}
