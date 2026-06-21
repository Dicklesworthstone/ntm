// Package bv provides integration with the beads_viewer (bv) tool.
// triage.go implements the --robot-triage mega-command integration with caching.
package bv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

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

func acquireTriageRunLock(deadline time.Time, timeout time.Duration) (func(), error) {
	for {
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
		time.Sleep(sleep)
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
	return getTriageWithTimeout(dir, DefaultTimeout)
}

// GetTriageWithTimeout returns complete triage analysis with a caller-scoped
// command timeout. Cached results are still reused when valid.
func GetTriageWithTimeout(dir string, timeout time.Duration) (*TriageResponse, error) {
	return getTriageWithTimeout(dir, timeout)
}

func getTriageWithTimeout(dir string, timeout time.Duration) (*TriageResponse, error) {
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
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
	releaseRunLock, err := acquireTriageRunLock(deadline, timeout)
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
	output, err := runWithTimeout(normalizedDir, remaining, "--robot-triage")
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

// GetTriageNoCache returns fresh triage data, bypassing the cache
func GetTriageNoCache(dir string) (*TriageResponse, error) {
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return nil, err
	}

	output, err := run(normalizedDir, "--robot-triage")
	if err != nil {
		return nil, err
	}

	var resp TriageResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing triage: %w", err)
	}

	// Also update cache with fresh data
	triageCacheMu.Lock()
	triageCache = &resp
	triageCacheDir = normalizedDir
	triageCacheTime = time.Now()
	triageCacheMu.Unlock()

	return &resp, nil
}

// InvalidateTriageCache clears the triage cache.
// Call this when beads data changes (e.g., after bd sync).
func InvalidateTriageCache() {
	triageCacheMu.Lock()
	triageCache = nil
	triageCacheDir = ""
	triageCacheTTL = TriageCacheTTL // Reset to default
	triageCacheMu.Unlock()
}

// SetTriageCacheTTL allows configuring the cache TTL
func SetTriageCacheTTL(ttl time.Duration) {
	triageCacheMu.Lock()
	triageCacheTTL = ttl
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

// GetTriageTopPicks returns the top N picks from triage
func GetTriageTopPicks(dir string, n int) ([]TriageTopPick, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	picks := triage.Triage.QuickRef.TopPicks
	if len(picks) > n {
		picks = picks[:n]
	}
	return picks, nil
}

// GetActionableRecommendations returns recommendations sourced from the FULL
// dependency-aware actionable set (bv --robot-plan), ranked by triage scoring.
//
// bv --robot-triage is hardcoded to ≤10 recommendations (see beads_viewer
// triage.TopN), so GetTriageRecommendations can never surface more than 10
// candidates no matter what n is requested. On large or heavily-gated backlogs
// whose top-ranked rows are epics/gated/blocked, that ceiling silently starves
// the assigner: it reports the queue drained while dozens of beads below the
// top-10 cut are actually actionable (issue #197).
//
// This helper removes the ceiling without losing triage ranking:
//   - triage recommendations come first, in triage's scored order (they carry
//     the rich BlockedBy/Labels/Status/Score fields downstream filters need);
//   - every actionable plan item that triage did NOT surface is then appended
//     as a synthesized recommendation, so beads beyond the top-10 are still
//     dispatchable. Plan items are the dependency-aware actionable set, so they
//     carry no BlockedBy — synthesized recs pass the blocked-by filter and are
//     classified by status/labels/active-assignment like any other.
//
// n caps the merged result (≤0 means no cap). If the plan surface is
// unavailable, it degrades to the (capped) triage set so callers never regress
// below today's behavior; if triage itself fails, the error is returned.
func GetActionableRecommendations(dir string, n int) ([]TriageRecommendation, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	recs := make([]TriageRecommendation, 0, len(triage.Triage.Recommendations))
	seen := make(map[string]struct{}, len(triage.Triage.Recommendations))
	for _, rec := range triage.Triage.Recommendations {
		if _, dup := seen[rec.ID]; dup {
			continue
		}
		seen[rec.ID] = struct{}{}
		recs = append(recs, rec)
	}

	// Best-effort: pull the uncapped actionable plan and append anything triage
	// didn't already rank. A plan failure is non-fatal — fall back to triage.
	if plan, planErr := GetPlan(dir); planErr == nil && plan != nil {
		for _, track := range plan.Plan.Tracks {
			for _, item := range track.Items {
				if item.ID == "" {
					continue
				}
				if _, dup := seen[item.ID]; dup {
					continue
				}
				seen[item.ID] = struct{}{}
				recs = append(recs, TriageRecommendation{
					ID:          item.ID,
					Title:       item.Title,
					Status:      item.Status,
					Priority:    item.Priority,
					UnblocksIDs: item.Unblocks,
				})
			}
		}
	}

	if n > 0 && len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

// GetTriageRecommendations returns the top N recommendations
func GetTriageRecommendations(dir string, n int) ([]TriageRecommendation, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	recs := triage.Triage.Recommendations
	if len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

// GetQuickWins returns quick win recommendations (low effort, high impact)
func GetQuickWins(dir string, n int) ([]TriageRecommendation, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	wins := triage.Triage.QuickWins
	if len(wins) > n {
		wins = wins[:n]
	}
	return wins, nil
}

// GetBlockersToClear returns blockers that should be cleared first
func GetBlockersToClear(dir string, n int) ([]BlockerToClear, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	blockers := triage.Triage.BlockersToClear
	if len(blockers) > n {
		blockers = blockers[:n]
	}
	return blockers, nil
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

// GetProjectHealth returns the project health metrics from triage
func GetProjectHealth(dir string) (*ProjectHealth, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return nil, err
	}

	return triage.Triage.ProjectHealth, nil
}

// GetTriageDataHash returns the data hash for cache validation
func GetTriageDataHash(dir string) (string, error) {
	triage, err := GetTriage(dir)
	if err != nil {
		return "", err
	}
	return triage.DataHash, nil
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
