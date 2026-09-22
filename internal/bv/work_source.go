package bv

import (
	"context"
	"errors"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

var ErrNoClaimableWork = errors.New(worksource.NoClaimableCode)

// WorkEligibilityError retains canonical exclusion reasons instead of reporting
// a healthy, empty queue when a tool supplied only ineligible candidates.
type WorkEligibilityError struct {
	Source     worksource.Identity    `json:"source"`
	Exclusions []worksource.Exclusion `json:"exclusions"`
}

func (e *WorkEligibilityError) Error() string {
	return worksource.NoClaimableCode + ": collected work candidates failed canonical source eligibility checks"
}
func (e *WorkEligibilityError) Unwrap() error { return ErrNoClaimableWork }

// GetActionableRecommendationsContext adds canonical eligibility to the
// existing source-bound plan/lifecycle reconciliation. Keep DB-only workspaces
// operational without claiming that their candidates were JSONL-verified.
func GetActionableRecommendationsContext(ctx context.Context, dir string, n int) ([]TriageRecommendation, error) {
	return actionableWithWorkSource(ctx, dir, n, getActionableRecommendationsFromToolsContext)
}

func actionableWithWorkSource(ctx context.Context, dir string, n int, collect func(context.Context, string, int) ([]TriageRecommendation, error)) ([]TriageRecommendation, error) {
	if ctx == nil {
		return nil, errors.New("actionable recommendations context is required")
	}
	dir, err := normalizeTriageDir(dir)
	if err != nil {
		return nil, err
	}
	identity, err := captureTriageSource(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !identity.Bound() {
		candidates, err := collect(ctx, dir, n)
		if err != nil {
			return nil, err
		}
		current, err := captureTriageSource(ctx, dir)
		if err != nil {
			return nil, err
		}
		if err := identity.Verify(current); err != nil {
			return nil, err
		}
		return candidates, nil
	}
	source, err := worksource.Read(ctx, dir, worksource.Policy{Expected: &identity})
	if err != nil {
		return nil, err
	}
	// Apply the caller's limit AFTER exclusion, or blocked top-ranked rows can
	// hide eligible work below the cutoff and make the assigner report dry.
	candidates, err := collect(ctx, dir, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	eligibility := source.Filter(ids, worksource.EligibilityPolicy{GatedLabels: OperatorGatedLabelsForProject(dir)})
	if err := worksource.Validate(ctx, source.Identity); err != nil {
		return nil, err
	}
	if len(candidates) > 0 && len(eligibility.EligibleIDs) == 0 {
		return nil, &WorkEligibilityError{Source: source.Identity, Exclusions: eligibility.Excluded}
	}
	allowed := make(map[string]bool, len(eligibility.EligibleIDs))
	for _, id := range eligibility.EligibleIDs {
		allowed[id] = true
	}
	result := make([]TriageRecommendation, 0, len(eligibility.EligibleIDs))
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if !allowed[id] {
			continue
		}
		delete(allowed, id)
		candidate.ID = id
		result = append(result, candidate)
		if n > 0 && len(result) >= n {
			break
		}
	}
	return result, nil
}
