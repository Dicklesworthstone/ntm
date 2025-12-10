// Package bv provides integration with the beads_viewer (bv) tool.
// It executes bv robot mode commands and parses their JSON output.
package bv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotInstalled indicates bv is not available
var ErrNotInstalled = errors.New("bv is not installed")

// ErrNoBaseline indicates no baseline exists for drift checking
var ErrNoBaseline = errors.New("no baseline found")

// IsInstalled checks if bv is available in PATH
func IsInstalled() bool {
	_, err := exec.LookPath("bv")
	return err == nil
}

// WorkDir is the working directory for bv commands.
// If empty, uses current directory.
var WorkDir string

// run executes bv with given args and returns stdout
func run(args ...string) (string, error) {
	if !IsInstalled() {
		return "", ErrNotInstalled
	}

	cmd := exec.Command("bv", args...)
	if WorkDir != "" {
		cmd.Dir = WorkDir
	}
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
		return "", fmt.Errorf("bv %s: %w: %s", strings.Join(args, " "), err, stderrStr)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// GetInsights returns graph analysis insights (bottlenecks, keystones, etc.)
func GetInsights() (*InsightsResponse, error) {
	output, err := run("-robot-insights")
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
func GetPriority() (*PriorityResponse, error) {
	output, err := run("-robot-priority")
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
func GetPlan() (*PlanResponse, error) {
	output, err := run("-robot-plan")
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
func GetRecipes() (*RecipesResponse, error) {
	output, err := run("-robot-recipes")
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
func CheckDrift() DriftResult {
	if !IsInstalled() {
		return DriftResult{
			Status:  DriftNoBaseline,
			Message: "bv not installed",
		}
	}

	cmd := exec.Command("bv", "-check-drift")
	if WorkDir != "" {
		cmd.Dir = WorkDir
	}
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
func GetTopBottlenecks(n int) ([]NodeScore, error) {
	insights, err := GetInsights()
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
func GetNextActions(n int) ([]PriorityRecommendation, error) {
	priority, err := GetPriority()
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
func GetParallelTracks() ([]Track, error) {
	plan, err := GetPlan()
	if err != nil {
		return nil, err
	}

	return plan.Plan.Tracks, nil
}

// IsBottleneck checks if an issue ID is in the bottleneck list
func IsBottleneck(issueID string) (bool, float64, error) {
	insights, err := GetInsights()
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
func IsKeystone(issueID string) (bool, float64, error) {
	insights, err := GetInsights()
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
func IsHub(issueID string) (bool, float64, error) {
	insights, err := GetInsights()
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
func IsAuthority(issueID string) (bool, float64, error) {
	insights, err := GetInsights()
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
func GetGraphPosition(issueID string) (*GraphPosition, error) {
	insights, err := GetInsights()
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
func GetGraphPositionsBatch(issueIDs []string) (map[string]*GraphPosition, error) {
	insights, err := GetInsights()
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
	DriftStatus   DriftStatus
	DriftMessage  string
	TopBottleneck string
	BottleneckCount int
}

// GetHealthSummary returns a quick project health check
func GetHealthSummary() (*HealthSummary, error) {
	summary := &HealthSummary{}

	// Check drift
	drift := CheckDrift()
	summary.DriftStatus = drift.Status
	summary.DriftMessage = drift.Message

	// Get bottlenecks
	bottlenecks, err := GetTopBottlenecks(5)
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
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	BlockedBy   []string `json:"blocked_by"`
	IsInProgress bool    `json:"is_in_progress"`
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
	InProgressTasks  []InProgressInfo `json:"in_progress_tasks"`
	BlockedCount     int              `json:"blocked_count"`
	ReadyCount       int              `json:"ready_count"`
	TopBlockers      []BlockerInfo    `json:"top_blockers,omitempty"`
}

// GetDependencyContext returns dependency/blocker context from bd
func GetDependencyContext(n int) (*DependencyContext, error) {
	ctx := &DependencyContext{}

	// Get stats
	statsOutput, err := runBd("stats", "--json")
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
	inProgressOutput, err := runBd("list", "--status=in_progress", "--json")
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
	blockedOutput, err := runBd("blocked", "--json")
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

// runBd executes bd with given args and returns stdout
func runBd(args ...string) (string, error) {
	cmd := exec.Command("bd", args...)
	if WorkDir != "" {
		cmd.Dir = WorkDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("bd %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// IsBdInstalled checks if bd is available in PATH
func IsBdInstalled() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}
