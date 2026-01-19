// Package bv provides integration with the beads_viewer (bv) tool.
// It executes bv robot mode commands and parses their JSON output.
package bv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotInstalled indicates bv is not available
var ErrNotInstalled = errors.New("bv is not installed")

// ErrNoBaseline indicates no baseline exists for drift checking
var ErrNoBaseline = errors.New("no baseline found")

// DefaultTimeout is the default timeout for external command execution
const DefaultTimeout = 30 * time.Second

// noDBCache tracks which directories require --no-db flag.
// Key: directory path, Value: bool (true if --no-db is needed)
// We use a sync.Map for thread-safe concurrent access across sessions.
var noDBCache atomic.Value // Stores *sync.Map

func init() {
	noDBCache.Store(&sync.Map{})
}

func getNoDBState(dir string) bool {
	m := noDBCache.Load().(*sync.Map)
	v, ok := m.Load(dir)
	return ok && v.(bool)
}

func setNoDBState(dir string, val bool) {
	m := noDBCache.Load().(*sync.Map)
	m.Store(dir, val)
}

// IsInstalled checks if bv is available in PATH
func IsInstalled() bool {
	_, err := exec.LookPath("bv")
	return err == nil
}

// run executes bv with given args and returns stdout
func run(dir string, args ...string) (string, error) {
	if !IsInstalled() {
		return "", ErrNotInstalled
	}

	// Resolve empty dir to current working directory
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bv", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Check for specific error conditions
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "No baseline found") {
			return "", ErrNoBaseline
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("bv timed out after %v", DefaultTimeout)
		}
		return "", fmt.Errorf("bv %s: %w: %s", strings.Join(args, " "), err, stderrStr)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// GetInsights returns graph analysis insights (bottlenecks, keystones, etc.)
func GetInsights(dir string) (*InsightsResponse, error) {
	output, err := run(dir, "-robot-insights")
	if err != nil {
		return nil, err
	}

	var resp InsightsResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing insights: %w", err)
	}

	return &resp, nil
}

// GetPriority returns priority recommendations
func GetPriority(dir string) (*PriorityResponse, error) {
	output, err := run(dir, "-robot-priority")
	if err != nil {
		return nil, err
	}

	var resp PriorityResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing priority: %w", err)
	}

	return &resp, nil
}

// GetPlan returns a parallel execution plan
func GetPlan(dir string) (*PlanResponse, error) {
	output, err := run(dir, "-robot-plan")
	if err != nil {
		return nil, err
	}

	var resp PlanResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}

	return &resp, nil
}

// GetRecipes returns available recipes
func GetRecipes(dir string) (*RecipesResponse, error) {
	output, err := run(dir, "-robot-recipes")
	if err != nil {
		return nil, err
	}

	var resp RecipesResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing recipes: %w", err)
	}

	return &resp, nil
}

// CheckDrift checks project drift from baseline
// Returns DriftResult with status and message
func CheckDrift(dir string) DriftResult {
	if !IsInstalled() {
		return DriftResult{
			Status:  DriftNoBaseline,
			Message: "bv not installed",
		}
	}

	// Resolve empty dir to current working directory
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return DriftResult{
				Status:  DriftNoBaseline,
				Message: fmt.Sprintf("failed to get working directory: %v", err),
			}
		}
	}

	// Validate directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return DriftResult{
			Status:  DriftNoBaseline,
			Message: fmt.Sprintf("project directory does not exist: %s", dir),
		}
	}

	// Check if .beads directory exists
	beadsDir := filepath.Join(dir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DriftResult{
			Status:  DriftNoBaseline,
			Message: fmt.Sprintf("no .beads directory in %s", dir),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bv", "-check-drift")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Parse exit code
	if err == nil {
		return DriftResult{
			Status:  DriftOK,
			Message: strings.TrimSpace(stdout.String()),
		}
	}

	// Check for exit code
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		message := strings.TrimSpace(stdout.String())
		if message == "" {
			message = strings.TrimSpace(stderr.String())
		}

		if ctx.Err() == context.DeadlineExceeded {
			return DriftResult{
				Status:  DriftNoBaseline,
				Message: "timeout checking drift",
			}
		}

		switch code {
		case 1:
			// Could be critical drift or no baseline
			if strings.Contains(message, "No baseline") {
				return DriftResult{
					Status:  DriftNoBaseline,
					Message: message,
				}
			}
			return DriftResult{
				Status:  DriftCritical,
				Message: message,
			}
		case 2:
			return DriftResult{
				Status:  DriftWarning,
				Message: message,
			}
		default:
			return DriftResult{
				Status:  DriftStatus(code),
				Message: message,
			}
		}
	}

	return DriftResult{
		Status:  DriftNoBaseline,
		Message: err.Error(),
	}
}

// GetTopBottlenecks returns the top N bottleneck issues
func GetTopBottlenecks(dir string, n int) ([]NodeScore, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return nil, err
	}

	bottlenecks := insights.Bottlenecks
	if len(bottlenecks) > n {
		bottlenecks = bottlenecks[:n]
	}

	return bottlenecks, nil
}

// GetNextActions returns recommended next actions based on priority analysis
func GetNextActions(dir string, n int) ([]PriorityRecommendation, error) {
	priority, err := GetPriority(dir)
	if err != nil {
		return nil, err
	}

	recommendations := priority.Recommendations
	if len(recommendations) > n {
		recommendations = recommendations[:n]
	}

	return recommendations, nil
}

// GetParallelTracks returns available parallel work tracks
func GetParallelTracks(dir string) ([]Track, error) {
	plan, err := GetPlan(dir)
	if err != nil {
		return nil, err
	}

	return plan.Plan.Tracks, nil
}

// IsBottleneck checks if an issue ID is in the bottleneck list
func IsBottleneck(dir, issueID string) (bool, float64, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return false, 0, err
	}

	for _, b := range insights.Bottlenecks {
		if b.ID == issueID {
			return true, b.Value, nil
		}
	}

	return false, 0, nil
}

// IsKeystone checks if an issue ID is in the keystone list
func IsKeystone(dir, issueID string) (bool, float64, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return false, 0, err
	}

	for _, k := range insights.Keystones {
		if k.ID == issueID {
			return true, k.Value, nil
		}
	}

	return false, 0, nil
}

// IsHub checks if an issue ID is in the hub list (HITS algorithm)
func IsHub(dir, issueID string) (bool, float64, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return false, 0, err
	}

	for _, h := range insights.Hubs {
		if h.ID == issueID {
			return true, h.Value, nil
		}
	}

	return false, 0, nil
}

// IsAuthority checks if an issue ID is in the authority list (HITS algorithm)
func IsAuthority(dir, issueID string) (bool, float64, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return false, 0, err
	}

	for _, a := range insights.Authorities {
		if a.ID == issueID {
			return true, a.Value, nil
		}
	}

	return false, 0, nil
}

// GraphPosition represents the position of an issue in the dependency graph
type GraphPosition struct {
	IssueID         string  `json:"issue_id"`
	IsBottleneck    bool    `json:"is_bottleneck"`
	BottleneckScore float64 `json:"bottleneck_score,omitempty"`
	IsKeystone      bool    `json:"is_keystone"`
	KeystoneScore   float64 `json:"keystone_score,omitempty"`
	IsHub           bool    `json:"is_hub"`
	HubScore        float64 `json:"hub_score,omitempty"`
	IsAuthority     bool    `json:"is_authority"`
	AuthorityScore  float64 `json:"authority_score,omitempty"`
	Summary         string  `json:"summary"` // Human-readable summary
}

// GetGraphPosition returns the full graph position context for an issue
func GetGraphPosition(dir, issueID string) (*GraphPosition, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return nil, err
	}

	pos := &GraphPosition{
		IssueID: issueID,
	}

	// Check bottleneck status
	for _, b := range insights.Bottlenecks {
		if b.ID == issueID {
			pos.IsBottleneck = true
			pos.BottleneckScore = b.Value
			break
		}
	}

	// Check keystone status
	for _, k := range insights.Keystones {
		if k.ID == issueID {
			pos.IsKeystone = true
			pos.KeystoneScore = k.Value
			break
		}
	}

	// Check hub status
	for _, h := range insights.Hubs {
		if h.ID == issueID {
			pos.IsHub = true
			pos.HubScore = h.Value
			break
		}
	}

	// Check authority status
	for _, a := range insights.Authorities {
		if a.ID == issueID {
			pos.IsAuthority = true
			pos.AuthorityScore = a.Value
			break
		}
	}

	// Generate summary
	pos.Summary = generatePositionSummary(pos)

	return pos, nil
}

// generatePositionSummary creates a human-readable summary of graph position
func generatePositionSummary(pos *GraphPosition) string {
	var parts []string

	if pos.IsBottleneck {
		parts = append(parts, "bottleneck (blocks many paths)")
	}
	if pos.IsKeystone {
		parts = append(parts, "keystone (high centrality)")
	}
	if pos.IsHub {
		parts = append(parts, "hub (links to many authorities)")
	}
	if pos.IsAuthority {
		parts = append(parts, "authority (linked by many hubs)")
	}

	if len(parts) == 0 {
		return "regular node"
	}

	return strings.Join(parts, ", ")
}

// GetGraphPositionsBatch returns graph positions for multiple issues efficiently
func GetGraphPositionsBatch(dir string, issueIDs []string) (map[string]*GraphPosition, error) {
	insights, err := GetInsights(dir)
	if err != nil {
		return nil, err
	}

	// Build lookup maps for O(1) access
	bottleneckMap := make(map[string]float64)
	for _, b := range insights.Bottlenecks {
		bottleneckMap[b.ID] = b.Value
	}

	keystoneMap := make(map[string]float64)
	for _, k := range insights.Keystones {
		keystoneMap[k.ID] = k.Value
	}

	hubMap := make(map[string]float64)
	for _, h := range insights.Hubs {
		hubMap[h.ID] = h.Value
	}

	authorityMap := make(map[string]float64)
	for _, a := range insights.Authorities {
		authorityMap[a.ID] = a.Value
	}

	// Build positions for requested issues
	result := make(map[string]*GraphPosition)
	for _, id := range issueIDs {
		pos := &GraphPosition{IssueID: id}

		if score, ok := bottleneckMap[id]; ok {
			pos.IsBottleneck = true
			pos.BottleneckScore = score
		}
		if score, ok := keystoneMap[id]; ok {
			pos.IsKeystone = true
			pos.KeystoneScore = score
		}
		if score, ok := hubMap[id]; ok {
			pos.IsHub = true
			pos.HubScore = score
		}
		if score, ok := authorityMap[id]; ok {
			pos.IsAuthority = true
			pos.AuthorityScore = score
		}

		pos.Summary = generatePositionSummary(pos)
		result[id] = pos
	}

	return result, nil
}

// HealthSummary returns a brief project health summary
type HealthSummary struct {
	DriftStatus     DriftStatus
	DriftMessage    string
	TopBottleneck   string
	BottleneckCount int
}

// GetHealthSummary returns a quick project health check
func GetHealthSummary(dir string) (*HealthSummary, error) {
	summary := &HealthSummary{}

	// Check drift
	drift := CheckDrift(dir)
	summary.DriftStatus = drift.Status
	summary.DriftMessage = drift.Message

	// Get bottlenecks
	bottlenecks, err := GetTopBottlenecks(dir, 5)
	if err != nil {
		// Non-fatal, just skip bottleneck info
		return summary, nil
	}

	summary.BottleneckCount = len(bottlenecks)
	if len(bottlenecks) > 0 {
		summary.TopBottleneck = bottlenecks[0].ID
	}

	return summary, nil
}

// BlockerInfo represents an issue that is blocked and what blocks it
type BlockerInfo struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	BlockedBy    []string `json:"blocked_by"`
	IsInProgress bool     `json:"is_in_progress"`
}

// InProgressInfo represents an in-progress issue with its dependencies
type InProgressInfo struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	DependencyCount  int      `json:"dependency_count"`
	OpenDependencies []string `json:"open_dependencies,omitempty"`
}

// DependencyContext contains dependency information for recovery prompts
type DependencyContext struct {
	InProgressTasks []InProgressInfo `json:"in_progress_tasks"`
	BlockedCount    int              `json:"blocked_count"`
	ReadyCount      int              `json:"ready_count"`
	TopBlockers     []BlockerInfo    `json:"top_blockers,omitempty"`
}

// GetDependencyContext returns dependency/blocker context from bd
func GetDependencyContext(dir string, n int) (*DependencyContext, error) {
	ctx := &DependencyContext{}

	// Get stats
	statsOutput, err := RunBd(dir, "stats", "--json")
	if err == nil {
		var stats struct {
			BlockedIssues int `json:"blocked_issues"`
			ReadyIssues   int `json:"ready_issues"`
		}
		if json.Unmarshal([]byte(statsOutput), &stats) == nil {
			ctx.BlockedCount = stats.BlockedIssues
			ctx.ReadyCount = stats.ReadyIssues
		}
	}

	// Get in-progress tasks
	inProgressOutput, err := RunBd(dir, "list", "--status=in_progress", "--json")
	if err == nil {
		var inProgress []struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			DependencyCount int    `json:"dependency_count"`
		}
		if json.Unmarshal([]byte(inProgressOutput), &inProgress) == nil {
			for _, task := range inProgress {
				if len(ctx.InProgressTasks) >= n {
					break
				}
				ctx.InProgressTasks = append(ctx.InProgressTasks, InProgressInfo{
					ID:              task.ID,
					Title:           task.Title,
					DependencyCount: task.DependencyCount,
				})
			}
		}
	}

	// Get blocked tasks (what is blocking progress)
	blockedOutput, err := RunBd(dir, "blocked", "--json")
	if err == nil {
		var blocked []struct {
			ID             string   `json:"id"`
			Title          string   `json:"title"`
			BlockedByCount int      `json:"blocked_by_count"`
			BlockedBy      []string `json:"blocked_by"`
		}
		if json.Unmarshal([]byte(blockedOutput), &blocked) == nil {
			for _, task := range blocked {
				if len(ctx.TopBlockers) >= n {
					break
				}
				ctx.TopBlockers = append(ctx.TopBlockers, BlockerInfo{
					ID:        task.ID,
					Title:     task.Title,
					BlockedBy: task.BlockedBy,
				})
			}
		}
	}

	return ctx, nil
}

// RunBd executes bd with given args and returns stdout.
// If bd reports a missing database and suggests `--no-db`, it retries once with `--no-db`
// and caches that preference for the remainder of the process.
func RunBd(dir string, args ...string) (string, error) {
	// Resolve empty dir to current working directory first to ensure consistent cache keys
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	// Check cache for this specific directory
	if getNoDBState(dir) && !containsString(args, "--no-db") {
		args = append([]string{"--no-db"}, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("bd timed out after %v", DefaultTimeout)
		}

		stderrStr := stderr.String()
		// If we haven't already forced no-db, check if we should
		if !getNoDBState(dir) && !containsString(args, "--no-db") && isNoBeadsDBError(stderrStr) {
			setNoDBState(dir, true)
			return RunBd(dir, append([]string{"--no-db"}, args...)...)
		}
		return "", fmt.Errorf("bd %s: %w: %s", strings.Join(args, " "), err, stderrStr)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func isNoBeadsDBError(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no beads database found") || strings.Contains(s, "use 'bd --no-db'")
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// IsBdInstalled checks if bd is available in PATH
func IsBdInstalled() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}

// GetBeadsSummary attempts to get bead statistics from bd command
func GetBeadsSummary(dir string, limit int) *BeadsSummary {
	result := &BeadsSummary{}

	// Resolve empty dir to current working directory
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			result.Available = false
			result.Reason = fmt.Sprintf("failed to get working directory: %v", err)
			return result
		}
	}

	// Validate directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		result.Available = false
		result.Reason = fmt.Sprintf("project directory does not exist: %s", dir)
		return result
	}

	// Check if .beads directory exists
	beadsDir := filepath.Join(dir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		result.Available = false
		result.Reason = fmt.Sprintf("no .beads/ directory in %s", dir)
		return result
	}

	result.Project = dir

	// Try to run bd stats --json to get summary
	statsOutput, err := RunBd(dir, "stats", "--json")
	if err != nil {
		result.Available = false
		result.Reason = fmt.Sprintf("bd stats failed: %v", err)
		return result
	}

	// Parse the JSON output
	var stats struct {
		TotalIssues      int `json:"total_issues"`
		OpenIssues       int `json:"open_issues"`
		InProgressIssues int `json:"in_progress_issues"`
		BlockedIssues    int `json:"blocked_issues"`
		ReadyIssues      int `json:"ready_issues"`
		ClosedIssues     int `json:"closed_issues"`
	}
	if err := json.Unmarshal([]byte(statsOutput), &stats); err != nil {
		result.Available = false
		result.Reason = fmt.Sprintf("parse stats failed: %v", err)
		return result
	}

	result.Available = true
	result.Total = stats.TotalIssues
	result.Open = stats.OpenIssues
	result.InProgress = stats.InProgressIssues
	result.Blocked = stats.BlockedIssues
	result.Ready = stats.ReadyIssues
	result.Closed = stats.ClosedIssues

	// Get ready preview (top N ready issues sorted by priority)
	result.ReadyPreview = GetReadyPreview(dir, limit)

	// Get in-progress list
	result.InProgressList = GetInProgressList(dir, limit)

	return result
}

// GetReadyPreview returns top N ready beads sorted by priority
func GetReadyPreview(dir string, limit int) []BeadPreview {
	var previews []BeadPreview

	output, err := RunBd(dir, "ready", "--json")
	if err != nil {
		return previews
	}

	var issues []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Priority int    `json:"priority"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return previews
	}

	// Take up to limit items
	for i, issue := range issues {
		if i >= limit {
			break
		}
		previews = append(previews, BeadPreview{
			ID:       issue.ID,
			Title:    issue.Title,
			Priority: fmt.Sprintf("P%d", issue.Priority),
		})
	}

	return previews
}

// GetInProgressList returns in-progress beads with assignees
func GetInProgressList(dir string, limit int) []BeadInProgress {
	var items []BeadInProgress

	output, err := RunBd(dir, "list", "--status=in_progress", "--json")
	if err != nil {
		return items
	}

	var issues []struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Assignee  string    `json:"assignee"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return items
	}

	// Take up to limit items
	for i, issue := range issues {
		if i >= limit {
			break
		}
		items = append(items, BeadInProgress{
			ID:        issue.ID,
			Title:     issue.Title,
			Assignee:  issue.Assignee,
			UpdatedAt: issue.UpdatedAt,
		})
	}

	return items
}

// GetRecentlyCompletedList returns recently completed beads.
// These are beads with status=done, ordered by completion time descending.
func GetRecentlyCompletedList(dir string, limit int) []BeadPreview {
	var items []BeadPreview

	output, err := RunBd(dir, "list", "--status=done", "--json")
	if err != nil {
		return items
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return items
	}

	// Take up to limit items
	for i, issue := range issues {
		if i >= limit {
			break
		}
		items = append(items, BeadPreview{
			ID:    issue.ID,
			Title: issue.Title,
		})
	}

	return items
}

// GetBlockedList returns blocked beads (beads that are blocked by dependencies).
func GetBlockedList(dir string, limit int) []BeadPreview {
	var items []BeadPreview

	output, err := RunBd(dir, "blocked", "--json")
	if err != nil {
		return items
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return items
	}

	// Take up to limit items
	for i, issue := range issues {
		if i >= limit {
			break
		}
		items = append(items, BeadPreview{
			ID:    issue.ID,
			Title: issue.Title,
		})
	}

	return items
}

// RunRaw executes bv with given args and returns the raw output.
// This is useful for commands where the caller wants to parse or display
// the output directly rather than using typed wrappers.
func RunRaw(dir string, args ...string) (string, error) {
	return run(dir, args...)
}
