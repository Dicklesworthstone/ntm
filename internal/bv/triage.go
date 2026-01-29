// Package bv provides integration with the beads_viewer (bv) tool.
// triage.go implements the -robot-triage mega-command integration with caching.
package bv

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// TriageCacheTTL is the default cache TTL for triage results
const TriageCacheTTL = 30 * time.Second

var (
	triageCache     *TriageResponse
	triageCacheDir  string
	triageCacheTime time.Time
	triageCacheTTL  = TriageCacheTTL
	triageCacheMu   sync.Mutex
)

// GetTriage returns the complete triage analysis from bv -robot-triage.
// Results are cached for TriageCacheTTL (default 30 seconds).
func GetTriage(dir string) (*TriageResponse, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting working directory: %w", err)
		}
		dir = cwd
	}

	triageCacheMu.Lock()
	defer triageCacheMu.Unlock()

	// Return cached result if still valid and for the same directory
	if triageCache != nil && triageCacheDir == dir && time.Since(triageCacheTime) < triageCacheTTL {
		return triageCache, nil
	}

	output, err := run(dir, "-robot-triage")
	if err != nil {
		return nil, err
	}

	var resp TriageResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing triage: %w", err)
	}

	// Update cache
	triageCache = &resp
	triageCacheDir = dir
	triageCacheTime = time.Now()

	return &resp, nil
}

// GetTriageNoCache returns fresh triage data, bypassing the cache
func GetTriageNoCache(dir string) (*TriageResponse, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting working directory: %w", err)
		}
		dir = cwd
	}

	output, err := run(dir, "-robot-triage")
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
	triageCacheDir = dir
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
	triageCacheMu.Lock()
	defer triageCacheMu.Unlock()
	return triageCache != nil && time.Since(triageCacheTime) < triageCacheTTL
}

// GetCacheAge returns how long the cache has been in place
func GetCacheAge() time.Duration {
	triageCacheMu.Lock()
	defer triageCacheMu.Unlock()
	if triageCache == nil {
		return 0
	}
	return time.Since(triageCacheTime)
}
