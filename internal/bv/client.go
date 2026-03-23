// Package bv provides integration with the beads_viewer (bv) tool.
// client.go implements a client-oriented API for bv --robot-triage integration.
package bv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// ErrTimeout indicates bv command timed out
var ErrTimeout = errors.New("bv command timed out")

// ErrInvalidJSON indicates bv returned invalid JSON
var ErrInvalidJSON = errors.New("bv returned invalid JSON")

// DefaultClientCacheTTL is the default cache TTL for client instances
const DefaultClientCacheTTL = 30 * time.Second

// DefaultClientTimeout is the default timeout for bv commands
const DefaultClientTimeout = 10 * time.Second

// BVClient provides a client-oriented API for bv integration.
// It supports configurable caching, timeouts, and workspace paths.
type BVClient struct {
	// WorkspacePath is the project directory (defaults to current directory)
	WorkspacePath string

	// CacheTTL is how long to cache results (default 30s)
	CacheTTL time.Duration

	// Timeout is the max time for bv commands (default 10s)
	Timeout time.Duration

	// Internal cache
	mu             sync.RWMutex
	triageCache    *TriageResponse
	triageCacheDir string
	triageCacheAt  time.Time
}

// NewBVClient creates a new BVClient with default settings.
// WorkspacePath defaults to empty (uses current directory).
func NewBVClient() *BVClient {
	return &BVClient{
		CacheTTL: DefaultClientCacheTTL,
		Timeout:  DefaultClientTimeout,
	}
}

// NewBVClientWithOptions creates a new BVClient with custom settings.
func NewBVClientWithOptions(workspacePath string, cacheTTL, timeout time.Duration) *BVClient {
	if cacheTTL == 0 {
		cacheTTL = DefaultClientCacheTTL
	}
	if timeout == 0 {
		timeout = DefaultClientTimeout
	}
	return &BVClient{
		WorkspacePath: workspacePath,
		CacheTTL:      cacheTTL,
		Timeout:       timeout,
	}
}

// RecommendationOpts controls how recommendations are fetched.
type RecommendationOpts struct {
	// Limit is the max number of recommendations (default 20)
	Limit int

	// Strategy selects prioritization: "balanced", "speed", "quality", "dependency"
	Strategy string

	// FilterReady returns only actionable items (no blockers)
	FilterReady bool
}

// Recommendation is a task recommendation with graph metrics.
// This provides a flattened view of the triage data for easier consumption.
type Recommendation struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Priority      int      `json:"priority"`
	PageRank      float64  `json:"pagerank"`
	Betweenness   float64  `json:"betweenness"` // Critical path importance
	UnblocksCount int      `json:"unblocks_count"`
	UnblocksIDs   []string `json:"unblocks_ids,omitempty"`
	BlockedByIDs  []string `json:"blocked_by_ids,omitempty"`
	IsActionable  bool     `json:"is_actionable"` // True if no blockers
	Tags          []string `json:"tags,omitempty"`
	EstimatedSize string   `json:"estimated_size"` // "small", "medium", "large"
	Score         float64  `json:"score"`          // Composite score from bv
	Action        string   `json:"action"`         // Suggested action
	Reasons       []string `json:"reasons,omitempty"`
}

// BottleneckNode represents a high-betweenness node in the dependency graph.
type BottleneckNode struct {
	ID          string  `json:"id"`
	Betweenness float64 `json:"betweenness"`
}

// Insights contains graph analysis insights.
type Insights struct {
	Cycles      [][]string       `json:"cycles,omitempty"`
	Bottlenecks []BottleneckNode `json:"bottlenecks,omitempty"`
	ReadyCount  int              `json:"ready_count"`
	TotalCount  int              `json:"total_count"`
}

// IsAvailable checks if bv is installed and responsive.
func (c *BVClient) IsAvailable() bool {
	if !IsInstalled() {
		return false
	}

	// Quick health check - just verify we can run bv --version
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bv", "--version")
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run() == nil
}

// GetRecommendations returns task recommendations from bv --robot-triage.
// Results are cached according to the client's CacheTTL.
func (c *BVClient) GetRecommendations(opts RecommendationOpts) ([]Recommendation, error) {
	// Set defaults
	if opts.Limit == 0 {
		opts.Limit = 20
	}

	// Get triage data (potentially cached)
	triage, err := c.getTriage()
	if err != nil {
		return nil, err
	}

	// Convert to Recommendation structs
	var recommendations []Recommendation
	for _, rec := range triage.Triage.Recommendations {
		r := c.convertRecommendation(rec)

		// Apply FilterReady if set
		if opts.FilterReady && !r.IsActionable {
			continue
		}

		recommendations = append(recommendations, r)

		// Apply limit
		if len(recommendations) >= opts.Limit {
			break
		}
	}

	return recommendations, nil
}

// GetInsights returns graph analysis insights.
func (c *BVClient) GetInsights() (*Insights, error) {
	// Try to get insights from bv -robot-insights
	workDir, err := c.workDir()
	if err != nil {
		return nil, err
	}

	insightsResp, err := GetInsights(workDir)
	if err != nil {
		// Fall back to triage data if insights fail
		triage, triageErr := c.getTriage()
		if triageErr != nil {
			// Return original error with context about fallback failure
			return nil, fmt.Errorf("%w (fallback also failed: %v)", err, triageErr)
		}

		// Build insights from triage data
		return c.buildInsightsFromTriage(triage), nil
	}

	// Build insights from insights response
	return c.buildInsightsFromResponse(insightsResp, workDir), nil
}

// GetQuickWins returns low-effort, high-impact recommendations.
func (c *BVClient) GetQuickWins(limit int) ([]Recommendation, error) {
	if limit == 0 {
		limit = 5
	}

	triage, err := c.getTriage()
	if err != nil {
		return nil, err
	}

	var recommendations []Recommendation
	for _, rec := range triage.Triage.QuickWins {
		r := c.convertRecommendation(rec)
		recommendations = append(recommendations, r)
		if len(recommendations) >= limit {
			break
		}
	}

	return recommendations, nil
}

// GetBlockersToClear returns blockers that unblock the most work.
func (c *BVClient) GetBlockersToClear(limit int) ([]Recommendation, error) {
	if limit == 0 {
		limit = 5
	}

	triage, err := c.getTriage()
	if err != nil {
		return nil, err
	}

	var recommendations []Recommendation
	for _, blocker := range triage.Triage.BlockersToClear {
		r := Recommendation{
			ID:            blocker.ID,
			Title:         blocker.Title,
			UnblocksCount: blocker.UnblocksCount,
			UnblocksIDs:   blocker.UnblocksIDs,
			IsActionable:  blocker.Actionable,
			BlockedByIDs:  blocker.BlockedBy,
			EstimatedSize: "medium", // Default for blockers
		}
		recommendations = append(recommendations, r)
		if len(recommendations) >= limit {
			break
		}
	}

	return recommendations, nil
}

// InvalidateCache clears the client's cache.
// Call this when beads data changes.
func (c *BVClient) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.triageCache = nil
}

// getTriage returns triage data, using cache if valid.
func (c *BVClient) getTriage() (*TriageResponse, error) {
	dir, err := c.workDir()
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	if c.triageCache != nil && c.triageCacheDir == dir && time.Since(c.triageCacheAt) < c.CacheTTL {
		cached := c.triageCache
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	// Fetch fresh data
	resp, err := c.fetchTriage(dir)
	if err != nil {
		return nil, err
	}

	// Update cache
	c.mu.Lock()
	c.triageCache = resp
	c.triageCacheDir = dir
	c.triageCacheAt = time.Now()
	c.mu.Unlock()

	return resp, nil
}

// fetchTriage executes bv --robot-triage and parses the response.
func (c *BVClient) fetchTriage(dir string) (*TriageResponse, error) {
	if !IsInstalled() {
		return nil, fmt.Errorf("%w: bv is not installed. Install it with: go install github.com/Dicklesworthstone/beads_viewer@latest", ErrNotInstalled)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bv", "--robot-triage")
	cmd.WaitDelay = 2 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w after %v", ErrTimeout, c.Timeout)
		}
		return nil, fmt.Errorf("bv --robot-triage failed: %w: %s", err, stderr.String())
	}

	// Validate and parse JSON
	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("%w: bv returned non-JSON output", ErrInvalidJSON)
	}

	var resp TriageResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("parsing triage response: %w", err)
	}

	return &resp, nil
}

// workDir returns the normalized workspace directory, defaulting to current dir.
func (c *BVClient) workDir() (string, error) {
	return normalizeTriageDir(c.WorkspacePath)
}

// convertRecommendation converts a TriageRecommendation to our Recommendation format.
func (c *BVClient) convertRecommendation(rec TriageRecommendation) Recommendation {
	r := Recommendation{
		ID:            rec.ID,
		Title:         rec.Title,
		Priority:      rec.Priority,
		UnblocksCount: len(rec.UnblocksIDs),
		UnblocksIDs:   rec.UnblocksIDs,
		BlockedByIDs:  rec.BlockedBy,
		IsActionable:  len(rec.BlockedBy) == 0,
		Tags:          rec.Labels,
		Score:         rec.Score,
		Action:        rec.Action,
		Reasons:       rec.Reasons,
	}

	// Extract PageRank and Betweenness from breakdown
	if rec.Breakdown != nil {
		r.PageRank = rec.Breakdown.Pagerank
		r.Betweenness = rec.Breakdown.Betweenness
	}

	// Estimate size based on heuristics
	r.EstimatedSize = c.estimateSize(rec)

	return r
}

// estimateSize estimates task size based on heuristics.
func (c *BVClient) estimateSize(rec TriageRecommendation) string {
	// Size heuristics:
	// - "small": leaf node (unblocks nothing), low betweenness
	// - "large": epic type, high betweenness, or unblocks many items
	// - "medium": everything else

	if rec.Type == "epic" {
		return "large"
	}

	unblockCount := len(rec.UnblocksIDs)

	if rec.Breakdown != nil {
		// High betweenness (>0.1) suggests critical path = larger
		if rec.Breakdown.Betweenness > 0.1 {
			return "large"
		}
	}

	if unblockCount > 3 {
		return "large"
	}

	if unblockCount == 0 && rec.Breakdown != nil && rec.Breakdown.Betweenness < 0.05 {
		return "small"
	}

	return "medium"
}

// buildInsightsFromResponse builds Insights from an InsightsResponse.
func (c *BVClient) buildInsightsFromResponse(resp *InsightsResponse, workDir string) *Insights {
	insights := &Insights{}

	// Extract cycles
	for _, cycle := range resp.Cycles {
		insights.Cycles = append(insights.Cycles, cycle.Nodes)
	}

	// Extract bottlenecks
	for _, b := range resp.Bottlenecks {
		insights.Bottlenecks = append(insights.Bottlenecks, BottleneckNode{
			ID:          b.ID,
			Betweenness: b.Value,
		})
	}

	// Populate counts using GetBeadsSummary
	if workDir != "" {
		summary := GetBeadsSummary(workDir, 1)
		if summary != nil && summary.Available {
			insights.ReadyCount = summary.Ready
			insights.TotalCount = summary.Total
		}
	}

	return insights
}

// buildInsightsFromTriage builds Insights from triage data as fallback.
func (c *BVClient) buildInsightsFromTriage(triage *TriageResponse) *Insights {
	insights := &Insights{}

	if triage.Triage.ProjectHealth != nil {
		// Extract counts from project health
		if counts, ok := triage.Triage.ProjectHealth.StatusDistribution["total"]; ok {
			insights.TotalCount = counts
		}

		// Check graph metrics for cycles
		if triage.Triage.ProjectHealth.GraphMetrics != nil {
			if triage.Triage.ProjectHealth.GraphMetrics.CycleCount > 0 {
				// Note: actual cycle nodes not available in triage, just count
				insights.Cycles = make([][]string, triage.Triage.ProjectHealth.GraphMetrics.CycleCount)
			}
		}
	}

	// Get ready count from quick_ref
	insights.ReadyCount = triage.Triage.QuickRef.ActionableCount

	// Build bottlenecks from recommendations with high betweenness
	for _, rec := range triage.Triage.Recommendations {
		if rec.Breakdown != nil && rec.Breakdown.Betweenness > 0.05 {
			insights.Bottlenecks = append(insights.Bottlenecks, BottleneckNode{
				ID:          rec.ID,
				Betweenness: rec.Breakdown.Betweenness,
			})
		}
	}

	return insights
}
