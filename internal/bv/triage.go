// Package bv provides integration with the beads_viewer (bv) tool.
// triage.go implements the --robot-triage mega-command integration with caching.
package bv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ErrActionableLabelsUnverified marks a fail-closed planning error: bv plan
// items cannot authorize automated assignment until their live Beads labels
// have been verified.
var ErrActionableLabelsUnverified = errors.New("actionable bead labels could not be verified")

// ErrActionablePlanUnverified marks a fail-closed planning error: automated
// assignment requires a complete dependency-aware bv plan and cannot fall
// back to permissive triage recommendations when that plan is unavailable or
// structurally invalid.
var ErrActionablePlanUnverified = errors.New("actionable bv plan could not be verified")

// TriageCacheTTL is the default cache TTL for triage results
const TriageCacheTTL = 30 * time.Second

var (
	triageCache     *TriageResponse
	triageCacheDir  string
	triageCacheTime time.Time
	triageCacheTTL  = TriageCacheTTL
	triageCacheMu   sync.RWMutex
	triageRunMu     sync.Mutex
)

func acquireTriageRunLock(ctx context.Context, deadline time.Time, timeout time.Duration) (func(), error) {
	if ctx == nil {
		return nil, fmt.Errorf("triage context is required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("bv timed out after %v", timeout)
		}
		if triageRunMu.TryLock() {
			return triageRunMu.Unlock, nil
		}
		sleep := 10 * time.Millisecond
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func normalizeTriageDir(dir string) (string, error) {
	if dir == "" {
		dir = util.ResolveProjectDir("")
		if dir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("getting working directory: %w", err)
			}
			dir = cwd
		}
	}

	resolvedDir := util.ResolveProjectDir(dir)
	absDir, err := filepath.Abs(resolvedDir)
	if err != nil {
		return "", fmt.Errorf("resolving triage directory: %w", err)
	}
	return absDir, nil
}

// GetTriage returns the complete triage analysis from bv --robot-triage.
// Results are cached for TriageCacheTTL (default 30 seconds).
func GetTriage(dir string) (*TriageResponse, error) {
	return getTriageContext(context.Background(), dir, CommandTimeout())
}

// GetTriageContext returns cached or fresh triage while honoring caller
// cancellation during runner serialization, subprocess execution, and retry.
func GetTriageContext(ctx context.Context, dir string) (*TriageResponse, error) {
	if ctx == nil {
		return nil, fmt.Errorf("triage context is required")
	}
	return getTriageContext(ctx, dir, CommandTimeout())
}

// GetTriageWithTimeout returns complete triage analysis with a caller-scoped
// command timeout. Cached results are still reused when valid.
func GetTriageWithTimeout(dir string, timeout time.Duration) (*TriageResponse, error) {
	return getTriageContext(context.Background(), dir, timeout)
}

func getTriageContext(ctx context.Context, dir string, timeout time.Duration) (*TriageResponse, error) {
	if ctx == nil {
		return nil, fmt.Errorf("triage context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = CommandTimeout()
	}
	deadline := time.Now().Add(timeout)

	triageCacheMu.RLock()
	// Return cached result if still valid and for the same directory
	if triageCache != nil && triageCacheDir == normalizedDir && time.Since(triageCacheTime) < triageCacheTTL {
		cached := triageCache
		triageCacheMu.RUnlock()
		return cached, nil
	}
	triageCacheMu.RUnlock()

	// Ensure only one runner fetches triage concurrently
	releaseRunLock, err := acquireTriageRunLock(ctx, deadline, timeout)
	if err != nil {
		return nil, err
	}
	defer releaseRunLock()

	// Double-check cache after acquiring run lock
	triageCacheMu.RLock()
	if triageCache != nil && triageCacheDir == normalizedDir && time.Since(triageCacheTime) < triageCacheTTL {
		cached := triageCache
		triageCacheMu.RUnlock()
		return cached, nil
	}
	triageCacheMu.RUnlock()

	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, fmt.Errorf("bv timed out after %v", timeout)
	}
	output, err := runWithContextTimeout(ctx, normalizedDir, remaining, "--robot-triage")
	if err != nil {
		return nil, err
	}

	var resp TriageResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing triage: %w", err)
	}

	// Update cache
	triageCacheMu.Lock()
	triageCache = &resp
	triageCacheDir = normalizedDir
	triageCacheTime = time.Now()
	triageCacheMu.Unlock()

	return &resp, nil
}

// InvalidateTriageCache clears the triage cache.
// Call this when beads data changes (e.g., after br sync --flush-only).
func InvalidateTriageCache() {
	triageCacheMu.Lock()
	triageCache = nil
	triageCacheDir = ""
	triageCacheTTL = TriageCacheTTL // Reset to default
	triageCacheMu.Unlock()
}

// GetTriageQuickRef returns just the quick reference portion of triage
func GetTriageQuickRef(dir string) (*TriageQuickRef, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}
	return &triage.Triage.QuickRef, nil
}

// GetActionableRecommendationsContext returns recommendations sourced from the FULL
// dependency-aware actionable set (bv --robot-plan), ranked by triage scoring.
//
// bv --robot-triage is hardcoded to ≤10 recommendations (see beads_viewer
// triage.TopN), so triage-derived recommendations can never surface more than 10
// candidates no matter what n is requested. On large or heavily-gated backlogs
// whose top-ranked rows are epics/gated/blocked, that ceiling silently starves
// the assigner: it reports the queue drained while dozens of beads below the
// top-10 cut are actually actionable (issue #197).
//
// This helper removes the ceiling without losing triage ranking. The plan is
// the authoritative membership boundary, while triage contributes ordering and
// scoring only for open/ready IDs also present in that plan. Every assignable
// plan item is enriched with live labels from `br ready` plus `br list --status
// open`, including IDs that also appeared in triage. Non-open plan rows are not
// assignment candidates; stale-recovery callers verify those rows through a
// separate guarded tracker path. This prevents stale or omitted triage labels
// from bypassing operator gates without letting in-progress rows poison the
// open-work authorization set.
//
// n caps the merged result (≤0 means no cap). If the plan surface is
// unavailable, malformed, structurally incomplete, or contains an assignable
// item whose labels cannot be verified, the call fails closed rather than
// authorizing raw triage candidates.
//
// It honors caller cancellation across triage, plan, and label enrichment.
func GetActionableRecommendationsContext(ctx context.Context, dir string, n int) ([]TriageRecommendation, error) {
	if ctx == nil {
		return nil, fmt.Errorf("actionable recommendations context is required")
	}
	triage, err := GetTriageContext(ctx, dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: load bv --robot-triage: %w", ErrActionablePlanUnverified, err)
	}

	plan, planErr := GetPlanContext(ctx, dir)
	if planErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: load bv --robot-plan: %w", ErrActionablePlanUnverified, planErr)
	}
	if plan == nil {
		return nil, fmt.Errorf("%w: bv --robot-plan returned no plan", ErrActionablePlanUnverified)
	}

	planItems := make([]PlanItem, 0)
	planByID := make(map[string]PlanItem)
	for trackIndex, track := range plan.Plan.Tracks {
		for itemIndex, item := range track.Items {
			item.ID = strings.TrimSpace(item.ID)
			if item.ID == "" {
				return nil, fmt.Errorf("%w: bv --robot-plan track %d item %d has an empty id", ErrActionablePlanUnverified, trackIndex, itemIndex)
			}
			switch strings.ToLower(strings.TrimSpace(item.Status)) {
			case "open", "ready":
				// Eligible for live-label authorization below.
			default:
				continue
			}
			if _, duplicate := planByID[item.ID]; duplicate {
				continue
			}
			planByID[item.ID] = item
			planItems = append(planItems, item)
		}
	}
	if len(planItems) == 0 {
		return []TriageRecommendation{}, nil
	}

	labelsByID, typesByID, labelsErr := readyBeadLabelsContext(ctx, dir)
	if labelsErr != nil {
		return nil, classifyActionableLabelsError(ctx, labelsErr)
	}

	// bv --robot-plan is a ranked snapshot; Beads owns lifecycle state. A plan
	// row that has vanished from both open-only label surfaces is either stale
	// (closed/blocked/in_progress/deferred since the snapshot — satisfied or
	// ineligible, skip it) or genuinely unaccounted for (fail closed). Resolve
	// the distinction once against the complete tracker surface; the normal
	// all-verified path never pays for the extra br call.
	var missingFromOpen []string
	for _, item := range planItems {
		if _, verified := labelsByID[item.ID]; !verified {
			missingFromOpen = append(missingFromOpen, item.ID)
		}
	}
	if len(missingFromOpen) > 0 {
		statusesByID, statusErr := allBeadStatusesContext(ctx, dir)
		if statusErr != nil {
			return nil, classifyActionableLabelsError(ctx, statusErr)
		}
		stale := make(map[string]struct{}, len(missingFromOpen))
		for _, id := range missingFromOpen {
			status, tracked := statusesByID[id]
			switch {
			case !tracked:
				return nil, fmt.Errorf("%w: actionable plan item %q was absent from br ready, br list --status open, and br list --all", ErrActionableLabelsUnverified, id)
			case status == "open":
				// Live-open yet invisible on both open surfaces: the label
				// sources are inconsistent, not merely behind the plan. Keep
				// failing closed rather than dispatch unverifiable work.
				return nil, fmt.Errorf("%w: plan item %q is still open in br list --all but was absent from both br ready and br list --status open", ErrActionableLabelsUnverified, id)
			default:
				stale[id] = struct{}{}
			}
		}
		filtered := planItems[:0]
		for _, item := range planItems {
			if _, drop := stale[item.ID]; drop {
				delete(planByID, item.ID)
				continue
			}
			filtered = append(filtered, item)
		}
		planItems = filtered
	}

	for _, item := range planItems {
		// Type evidence follows the same coverage invariant as labels: without
		// a verified issue type the classifier cannot prove a plan item is not
		// a container (epic), so assignment must stop rather than dispatch it.
		if strings.TrimSpace(typesByID[item.ID]) == "" {
			return nil, fmt.Errorf("%w: open actionable plan item %q has no issue type in br ready or br list --status open", ErrActionableLabelsUnverified, item.ID)
		}
	}

	// Preserve triage score ordering only for IDs authorized by the plan. Live
	// labels and plan state always replace their potentially stale triage copies.
	recs := make([]TriageRecommendation, 0, len(planItems))
	seen := make(map[string]struct{}, len(planItems))
	for _, rec := range triage.Triage.Recommendations {
		id := strings.TrimSpace(rec.ID)
		item, authorized := planByID[id]
		if !authorized {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		rec.ID = id
		if strings.TrimSpace(rec.Title) == "" {
			rec.Title = item.Title
		}
		rec.Status = item.Status
		rec.Priority = item.Priority
		rec.Type = typesByID[id]
		rec.Labels = append([]string(nil), labelsByID[id]...)
		rec.BlockedBy = nil
		rec.UnblocksIDs = append([]string(nil), item.Unblocks...)
		seen[id] = struct{}{}
		recs = append(recs, rec)
	}
	for _, item := range planItems {
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		recs = append(recs, TriageRecommendation{
			ID:          item.ID,
			Title:       item.Title,
			Type:        typesByID[item.ID],
			Status:      item.Status,
			Priority:    item.Priority,
			Labels:      append([]string(nil), labelsByID[item.ID]...),
			UnblocksIDs: append([]string(nil), item.Unblocks...),
		})
	}

	if n > 0 && len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

func classifyActionableLabelsError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrActionableLabelsUnverified, err)
}

// readyBeadLabels returns a map of bead ID -> labels for the actionable work
// set, sourced from `br ready --json` merged with `br list --json --status
// open` (non-empty ready entries win; the open list fills absent/empty gaps).
//
// It exists to restore label fidelity on plan-sourced recommendations:
// bv --robot-plan omits labels, yet the assignment classifier
// (classifyTriageRecForAssignment) gates operator-gated beads by label.
//
// `br ready` alone is NOT sufficient: it excludes epic-type beads, while
// bv --robot-plan includes them as actionable. An operator-gated epic surfaced
// below triage's top-10 cut would therefore arrive with empty labels and slip
// through the operator gate (#224). Merging the full open list restores epic
// labels; non-empty ready entries stay authoritative for beads present in both.
//
// A br error or parse failure on either source is fatal: an incomplete label
// map would silently bypass the operator gate.

// readyBeadLabelsContext also returns each bead's issue type (map ID ->
// issue_type) from the same br rows, so plan-sourced recommendations recover
// type evidence exactly like label evidence: bv --robot-plan omits both, yet
// the assignment classifier must exclude container types (epic) from
// auto-dispatch (GH#242 / ntm-e165).
func readyBeadLabelsContext(ctx context.Context, dir string) (map[string][]string, map[string]string, error) {
	labels := make(map[string][]string)
	issueTypes := make(map[string]string)
	// Order matters: `br ready` first so its non-empty labels win, then the full
	// open list (which includes epics) fills every absent or empty label gap. A
	// large explicit --limit keeps each map complete on br builds whose default
	// limit is finite (older builds treat --limit 0 as zero rows rather than
	// "unlimited").
	for _, args := range [][]string{
		{"ready", "--json", "--limit", "100000"},
		{"list", "--json", "--status", "open", "--limit", "100000"},
	} {
		output, err := RunBdContext(ctx, dir, args...)
		if err != nil {
			return nil, nil, fmt.Errorf("read bead labels (br %s): %w", strings.Join(args, " "), err)
		}
		items, err := UnmarshalBdList[struct {
			ID        string          `json:"id"`
			IssueType string          `json:"issue_type"`
			Labels    json.RawMessage `json:"labels"`
		}](output)
		if err != nil {
			return nil, nil, fmt.Errorf("parse bead labels (br %s): %w", strings.Join(args, " "), err)
		}
		for _, it := range items {
			it.ID = strings.TrimSpace(it.ID)
			if it.ID == "" {
				continue
			}
			var itemLabels []string
			if len(it.Labels) > 0 {
				if strings.TrimSpace(string(it.Labels)) == "null" {
					return nil, nil, fmt.Errorf("parse bead labels (br %s): bead %q has a null or blank labels container", strings.Join(args, " "), it.ID)
				}
				if err := json.Unmarshal(it.Labels, &itemLabels); err != nil {
					return nil, nil, fmt.Errorf("parse bead labels (br %s): bead %q labels: %w", strings.Join(args, " "), it.ID, err)
				}
			}
			for labelIndex := range itemLabels {
				itemLabels[labelIndex] = strings.TrimSpace(itemLabels[labelIndex])
				if itemLabels[labelIndex] == "" {
					return nil, nil, fmt.Errorf("parse bead labels (br %s): bead %q has a null or blank label at index %d", strings.Join(args, " "), it.ID, labelIndex)
				}
			}
			if issueType := strings.TrimSpace(it.IssueType); issueType != "" {
				if _, seen := issueTypes[it.ID]; !seen {
					issueTypes[it.ID] = issueType
				}
			}
			existing, seen := labels[it.ID]
			if seen && (len(existing) > 0 || len(itemLabels) == 0) {
				continue
			}
			// Preserve empty rows as proof that the tracker covered this ID, but
			// let a later non-empty source fill that label gap. This matters when
			// `br ready` omits labels that `br list --status open` still reports.
			labels[it.ID] = append([]string(nil), itemLabels...)
		}
	}
	return labels, issueTypes, nil
}

// allBeadStatusesContext reads Beads' complete lifecycle surface (br list
// --all) as an id→status map. It backs the stale-plan-row resolution above
// and is only invoked when a plan item has already vanished from both
// open-only label sources, so the common path stays at two br calls.
func allBeadStatusesContext(ctx context.Context, dir string) (map[string]string, error) {
	args := []string{"list", "--json", "--all", "--limit", "100000"}
	output, err := RunBdContext(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("read bead lifecycle (br %s): %w", strings.Join(args, " "), err)
	}
	items, err := UnmarshalBdList[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](output)
	if err != nil {
		return nil, fmt.Errorf("parse bead lifecycle (br %s): %w", strings.Join(args, " "), err)
	}
	statuses := make(map[string]string, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "" {
			return nil, fmt.Errorf("parse bead lifecycle (br %s): bead %q has a blank status", strings.Join(args, " "), id)
		}
		statuses[id] = status
	}
	return statuses, nil
}

// GetNextRecommendation returns the single top recommendation.
// This is equivalent to bv -robot-next but uses cached triage data.
func GetNextRecommendation(dir string) (*TriageRecommendation, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	if len(triage.Triage.Recommendations) == 0 {
		return nil, nil
	}

	return &triage.Triage.Recommendations[0], nil
}

// IsCacheValid checks if the cache is still valid
func IsCacheValid() bool {
	triageCacheMu.RLock()
	defer triageCacheMu.RUnlock()
	return triageCache != nil && time.Since(triageCacheTime) < triageCacheTTL
}

// GetCacheAge returns how long the cache has been in place
func GetCacheAge() time.Duration {
	triageCacheMu.RLock()
	defer triageCacheMu.RUnlock()
	if triageCache == nil {
		return 0
	}
	return time.Since(triageCacheTime)
}
