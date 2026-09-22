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
	Source             *worksource.Identity   `json:"source,omitempty"`
	Dirty              bool                   `json:"dirty,omitempty"`
	Excluded           []worksource.Exclusion `json:"excluded"`
	ReasonCode         string                 `json:"reason_code,omitempty"`
	ReportedReady      int                    `json:"reported_ready"`
	CountScope         string                 `json:"count_scope"`
	CandidatesObserved int                    `json:"candidates_observed,omitempty"`
	VerifiedReady      *int                   `json:"verified_ready,omitempty"`
	PreviewLimit       int                    `json:"preview_limit,omitempty"`
	PreviewTruncated   bool                   `json:"preview_truncated,omitempty"`
	Remediation        string                 `json:"remediation,omitempty"`
	Mismatch           *worksource.StaleError `json:"mismatch,omitempty"`
}

func (a *WorkCoordinationAdapter) collectVerifiedWork(ctx context.Context) (*WorkSection, error) {
	work, err := collectWorkWithSource(ctx, a.config.ProjectDir, a.config.VerificationPolicy, func(ctx context.Context) (*WorkSection, error) {
		work, err := a.collectWork(ctx)
		if err != nil || work == nil || !work.Available {
			return work, err
		}
		// Summary previews are display-limited, sometimes twice (br's default
		// and WorkItemLimit). Use one explicit full ready response as candidate
		// membership. Never interpret a failed read as a successfully empty set.
		candidates, err := bv.GetReadyCandidatesContext(ctx, a.config.ProjectDir)
		if err != nil {
			return work, err
		}
		return workWithReadyCandidates(work, candidates), nil
	})
	if err != nil || work == nil || !work.Available {
		return work, err
	}
	return limitVerifiedWorkPreview(work, a.config.WorkItemLimit), nil
}

// workWithReadyCandidates preserves optional ranking enrichment, but membership
// and visible identity/title/priority come from the complete direct ready read.
// It does not mutate the summary's limited preview or its backing array.
func workWithReadyCandidates(work *WorkSection, candidates []bv.BeadPreview) *WorkSection {
	out := copyWorkForVerification(work)
	previous := make(map[string]WorkItem, len(out.Ready))
	for _, item := range out.Ready {
		previous[strings.TrimSpace(item.ID)] = item
	}
	out.Ready = make([]WorkItem, 0, len(candidates))
	for _, preview := range candidates {
		normalized := workItemFromPreview(preview, bv.TriageRecommendation{})
		item := previous[preview.ID]
		item.ID = normalized.ID
		item.Title = normalized.Title
		item.TitleDisclosure = normalized.TitleDisclosure
		item.Priority = normalized.Priority
		out.Ready = append(out.Ready, item)
	}
	return out
}

// limitVerifiedWorkPreview runs AFTER canonical eligibility and mutex-batch
// selection. Summary.Ready stays the verified count, not the number displayed.
// This prevents a gated top-N prefix from masquerading as a drained backlog.
func limitVerifiedWorkPreview(work *WorkSection, limit int) *WorkSection {
	out := copyWorkForVerification(work)
	if limit <= 0 {
		limit = defaultWorkItemLimit
	}
	if out.Verification != nil {
		verification := *out.Verification
		out.Verification = &verification
		verification.PreviewLimit = limit
		verification.PreviewTruncated = len(out.Ready) > limit
		if verification.CountScope == "verified_preview" {
			total := len(out.Ready)
			verification.VerifiedReady = &total
			verification.CandidatesObserved = total + len(verification.Excluded)
			verification.CountScope = "verified_candidates"
			if total == 0 {
				verification.Remediation = "No direct tracker ready candidate passed canonical eligibility checks; inspect exclusions and tracker readiness."
			}
		}
	}
	if len(out.Ready) > limit {
		out.Ready = append([]WorkItem{}, out.Ready[:limit]...)
	}
	keepVisibleWorkRecommendation(out)
	return out
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
	}
	keepVisibleWorkRecommendation(out)
	if len(out.Ready) == 0 {
		out.Verification.Remediation = "No collected candidate passed canonical eligibility checks; inspect exclusions or collect a larger preview."
	}
	return out
}

func keepVisibleWorkRecommendation(work *WorkSection) {
	if work.Triage == nil || work.Triage.TopRecommendation == nil {
		return
	}
	id := strings.TrimSpace(work.Triage.TopRecommendation.ID)
	for _, item := range work.Ready {
		if item.ID == id {
			return
		}
	}
	work.Triage.TopRecommendation = nil
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
	} else if errors.Is(err, bv.ErrReadyCandidatesIncomplete) {
		out.Verification.ReasonCode = bv.ErrReadyCandidatesIncomplete.Error()
		out.Verification.Remediation = "Inspect the tracker ready response or its result limit, then recollect; an incomplete ready list cannot prove the queue is empty."
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
