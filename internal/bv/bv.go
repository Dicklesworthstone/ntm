// Package bv provides integration with the beads_viewer (bv) tool.
// It executes bv robot mode commands and parses their JSON output.
package bv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
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
var noDBCache sync.Map
var runBDMutexes sync.Map

type workspaceBDGate struct {
	token chan struct{}
}

func newWorkspaceBDGate() *workspaceBDGate {
	gate := &workspaceBDGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (g *workspaceBDGate) Lock() {
	<-g.token
}

func (g *workspaceBDGate) LockContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workspace Beads lock context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		return nil
	}
}

func (g *workspaceBDGate) Unlock() {
	g.token <- struct{}{}
}

func getNoDBState(dir string) bool {
	v, ok := noDBCache.Load(dir)
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func setNoDBState(dir string, val bool) {
	noDBCache.Store(dir, val)
}

func workspaceBDMutex(dir string) *workspaceBDGate {
	if existing, ok := runBDMutexes.Load(dir); ok {
		if mu, ok := existing.(*workspaceBDGate); ok {
			return mu
		}
	}
	mu := newWorkspaceBDGate()
	actual, _ := runBDMutexes.LoadOrStore(dir, mu)
	if existing, ok := actual.(*workspaceBDGate); ok {
		return existing
	}
	return mu
}

// IsInstalled checks if bv is available in PATH
func IsInstalled() bool {
	_, err := exec.LookPath("bv")
	return err == nil
}

// run executes bv with given args and returns stdout.
// It includes retry logic for transient database locks.
func run(dir string, args ...string) (string, error) {
	return runWithTimeout(dir, CommandTimeout(), args...)
}

func runWithTimeout(dir string, timeout time.Duration, args ...string) (string, error) {
	return runWithContextTimeout(context.Background(), dir, timeout, args...)
}

func runWithContextTimeout(parent context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	if parent == nil {
		return "", errors.New("bv command context is required")
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	if !IsInstalled() {
		return "", ErrNotInstalled
	}
	if timeout <= 0 {
		timeout = CommandTimeout()
	}

	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return "", err
	}

	const maxAttempts = 3
	deadline := time.Now().Add(timeout)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
		}

		ctx, cancel := context.WithTimeout(parent, remaining)

		cmd := exec.CommandContext(ctx, "bv", args...)
		cmd.Dir = normalizedDir
		cmd.WaitDelay = time.Second // Prevent hanging on open pipes if child processes outlive context
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		cancel()

		if err == nil {
			return strings.TrimSpace(stdout.String()), nil
		}

		// Check for specific error conditions
		stderrStr := stderr.String()
		stdoutStr := stdout.String()

		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
		}

		if strings.Contains(stderrStr, "No baseline found") {
			return "", ErrNoBaseline
		}

		// Handle transient database locks (SQLite)
		if attempt < maxAttempts && (strings.Contains(stderrStr, "database is locked") ||
			strings.Contains(stdoutStr, "database is locked") ||
			strings.Contains(stderrStr, "database is busy")) {
			backoff := transientBeadsDBBackoff(attempt)
			if time.Until(deadline) <= backoff {
				return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
			}
			if err := waitForBeadsRetry(parent, backoff); err != nil {
				return "", err
			}
			continue
		}

		return "", fmt.Errorf("bv %s: %w: %s", strings.Join(args, " "), err, stderrStr)
	}

	return "", fmt.Errorf("bv %s: exceeded retry budget", strings.Join(args, " "))
}

// GetInsights returns graph analysis insights (bottlenecks, keystones, etc.)
func GetInsights(dir string) (*InsightsResponse, error) {
	return GetInsightsContext(context.Background(), dir)
}

// GetInsightsContext returns graph insights with caller cancellation.
func GetInsightsContext(ctx context.Context, dir string) (*InsightsResponse, error) {
	output, err := runWithContextTimeout(ctx, dir, CommandTimeout(), "--robot-insights")
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
	output, err := run(dir, "--robot-priority")
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
	return GetPlanContext(context.Background(), dir)
}

// GetPlanContext returns the parallel execution plan with caller cancellation.
func GetPlanContext(ctx context.Context, dir string) (*PlanResponse, error) {
	output, err := runWithContextTimeout(ctx, dir, CommandTimeout(), "--robot-plan")
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	if len(envelope.Plan) == 0 || string(envelope.Plan) == "null" {
		return nil, errors.New("parsing plan: missing plan object")
	}
	var planEnvelope struct {
		Tracks json.RawMessage `json:"tracks"`
	}
	if err := json.Unmarshal(envelope.Plan, &planEnvelope); err != nil {
		return nil, fmt.Errorf("parsing plan object: %w", err)
	}
	if len(planEnvelope.Tracks) == 0 || string(planEnvelope.Tracks) == "null" {
		return nil, errors.New("parsing plan: missing plan.tracks array")
	}
	var trackEnvelopes []struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(planEnvelope.Tracks, &trackEnvelopes); err != nil {
		return nil, fmt.Errorf("parsing plan.tracks: %w", err)
	}
	for i, track := range trackEnvelopes {
		if len(track.Items) == 0 || string(track.Items) == "null" {
			return nil, fmt.Errorf("parsing plan: missing plan.tracks[%d].items array", i)
		}
		var items []struct {
			Status   *string `json:"status"`
			Priority *int    `json:"priority"`
		}
		if err := json.Unmarshal(track.Items, &items); err != nil {
			return nil, fmt.Errorf("parsing plan.tracks[%d].items: %w", i, err)
		}
		for itemIndex, item := range items {
			if item.Status == nil || strings.TrimSpace(*item.Status) == "" {
				return nil, fmt.Errorf("parsing plan: missing or blank plan.tracks[%d].items[%d].status", i, itemIndex)
			}
			if item.Priority == nil {
				return nil, fmt.Errorf("parsing plan: missing plan.tracks[%d].items[%d].priority", i, itemIndex)
			}
			if *item.Priority < 0 || *item.Priority > 4 {
				return nil, fmt.Errorf("parsing plan: plan.tracks[%d].items[%d].priority %d is outside 0..4", i, itemIndex, *item.Priority)
			}
		}
	}

	var resp PlanResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	for trackIndex := range resp.Plan.Tracks {
		for itemIndex := range resp.Plan.Tracks[trackIndex].Items {
			item := &resp.Plan.Tracks[trackIndex].Items[itemIndex]
			item.Status = strings.TrimSpace(item.Status)
			if item.Status == "" {
				return nil, fmt.Errorf("parsing plan: missing or blank plan.tracks[%d].items[%d].status", trackIndex, itemIndex)
			}
		}
	}

	return &resp, nil
}

// GetRecipes returns available recipes
func GetRecipes(dir string) (*RecipesResponse, error) {
	output, err := run(dir, "--robot-recipes")
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

	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return DriftResult{
			Status:  DriftNoBaseline,
			Message: err.Error(),
		}
	}
	dir = normalizedDir

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

	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "bv", "--check-drift")
	cmd.WaitDelay = time.Second // Prevent hanging on open pipes
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

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
	if n < 0 {
		return nil, fmt.Errorf("bottleneck limit must not be negative: %d", n)
	}
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
	if n < 0 {
		return nil, fmt.Errorf("next-action limit must not be negative: %d", n)
	}
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

// graphPositionInsights is replaceable in package tests so the graph-position
// helpers are exercised against deterministic insights without launching bv.
var graphPositionInsights = GetInsights

// GetGraphPosition returns the full graph position context for an issue
func GetGraphPosition(dir, issueID string) (*GraphPosition, error) {
	insights, err := graphPositionInsights(dir)
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
	insights, err := graphPositionInsights(dir)
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

	// Get stats. `br stats --json` nests its counters under "summary"; a flat
	// struct unmarshalled without error and left every count at zero, so
	// health and recovery surfaces reported "0 ready / 0 blocked" for a
	// project with dozens of each — indistinguishable from a genuinely empty
	// backlog because the parse "succeeded".
	statsOutput, err := RunBd(dir, "stats", "--json")
	if err == nil {
		var stats struct {
			Summary struct {
				BlockedIssues int `json:"blocked_issues"`
				ReadyIssues   int `json:"ready_issues"`
			} `json:"summary"`
		}
		if unmarshalErr := json.Unmarshal([]byte(statsOutput), &stats); unmarshalErr != nil {
			return nil, fmt.Errorf("parse br stats output: %w", unmarshalErr)
		}
		ctx.BlockedCount = stats.Summary.BlockedIssues
		ctx.ReadyCount = stats.Summary.ReadyIssues
	}

	// Get in-progress tasks. `br list --json` returns the {"issues":[...]}
	// envelope, so a raw-array unmarshal always failed — and the failure was
	// swallowed, leaving the list permanently empty. UnmarshalBdList handles
	// both the envelope and the bare-array shape.
	inProgressOutput, err := RunBd(dir, "list", "--status=in_progress", "--json")
	if err == nil {
		type inProgressItem struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			DependencyCount int    `json:"dependency_count"`
		}
		inProgress, unmarshalErr := UnmarshalBdList[inProgressItem](inProgressOutput)
		if unmarshalErr != nil {
			return nil, fmt.Errorf("parse br in-progress list: %w", unmarshalErr)
		}
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

// HasLocalBeadsDB returns true when `dir` itself contains a .beads directory.
// Recovery callers use this to refuse to walk up into a parent repo's
// work-item database when the child has none of its own (#130). Generic
// list helpers (`GetInProgressList`, `GetRecentlyCompletedListContext`,
// `GetBlockedListContext`) deliberately do not gate on this — they preserve br's
// walk-up behavior so callers that *want* parent rows (alerts, status,
// triage) keep working from a child directory. Recovery and other
// trust-sensitive callers must pre-check.
//
// This deliberately does NOT use normalizeTriageDir / ResolveProjectDir,
// because those helpers walk UP the filesystem to find a beads/git root —
// which is exactly the behavior the recovery contract needs to defeat. We
// must consult the literal `dir` (after Abs+Clean only) to know whether the
// caller's working directory is its own beads workspace.
//
// An empty `dir` falls back to cwd. Any stat error is treated as "no local
// db" so we err on the side of an empty recovery list rather than surfacing
// parent rows.
func HasLocalBeadsDB(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return false
		}
		dir = cwd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(filepath.Clean(abs), ".beads"))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// RunBd executes br (beads_rust) with given args and returns stdout.
// If br reports a missing database and suggests `--no-db`, it retries once with `--no-db`
// and caches that preference for the remainder of the process.
func RunBd(dir string, args ...string) (string, error) {
	return runBdContext(context.Background(), dir, true, args...)
}

// RunBdContext executes br with caller-controlled cancellation while retaining
// the workspace serialization and no-database retry policy used by RunBd.
func RunBdContext(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		return "", errors.New("br command context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return runBdContext(ctx, dir, true, args...)
}

// runBdContext executes br with the workspace serialization and retry policy
// used by RunBd. allowNoDB must be false for compare-and-set mutations such as
// claims because JSONL-only fallback cannot provide the SQLite transaction.
func runBdContext(parent context.Context, dir string, allowNoDB bool, args ...string) (string, error) {
	if parent == nil {
		return "", errors.New("br command context is required")
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	// Normalize dir to ensure consistent cache keys.
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return "", err
	}
	dir = normalizedDir
	args = append([]string(nil), args...)

	// br's SQLite-backed workspace can self-contend when a single ntm process
	// launches multiple br subprocesses against the same directory in parallel.
	mu := workspaceBDMutex(dir)
	if err := mu.LockContext(parent); err != nil {
		return "", err
	}
	defer mu.Unlock()

	// Check cache for this specific directory
	if allowNoDB && getNoDBState(dir) && !containsString(args, "--no-db") {
		args = append([]string{"--no-db"}, args...)
	}
	if !containsString(args, "--no-db") && !containsString(args, "--lock-timeout") {
		args = append([]string{"--lock-timeout", "5000"}, args...)
	}

	const maxAttempts = 6 // Canonical default: config.RetryConfig.DB.MaxAttempts
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(parent, DefaultTimeout)

		cmd := exec.CommandContext(ctx, "br", args...)
		cmd.WaitDelay = time.Second // Prevent hanging on open pipes
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		cancel()
		if err == nil {
			return strings.TrimSpace(stdout.String()), nil
		}
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("br timed out after %v", DefaultTimeout)
		}

		stdoutStr := stdout.String()
		stderrStr := stderr.String()
		diagnostics := stderrStr
		if strings.TrimSpace(diagnostics) == "" {
			diagnostics = stdoutStr
		}
		// If we haven't already forced no-db, check if we should
		if allowNoDB && !getNoDBState(dir) && !containsString(args, "--no-db") && isNoBeadsDBError(stderrStr, stdoutStr) {
			setNoDBState(dir, true)
			args = append([]string{"--no-db"}, stripFlagWithValue(args, "--lock-timeout")...)
			attempt = 0
			continue
		}
		if attempt < maxAttempts && isTransientBeadsDBError(stderrStr, stdoutStr) {
			if err := waitForBeadsRetry(parent, transientBeadsDBBackoff(attempt)); err != nil {
				return "", err
			}
			continue
		}
		return "", fmt.Errorf("br %s: %w: %s", strings.Join(args, " "), err, diagnostics)
	}

	return "", fmt.Errorf("br %s: exceeded retry budget", strings.Join(args, " "))
}

// beadsRetryWaitTestObserver lets in-package tests synchronize at the exact
// backoff boundary without exposing a public or process-global hook.
type beadsRetryWaitTestObserver interface {
	observeBeadsRetryWaitForTest(time.Duration)
}

func waitForBeadsRetry(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("beads retry context is required")
	}
	if delay <= 0 {
		return ctx.Err()
	}
	if observer, ok := ctx.(beadsRetryWaitTestObserver); ok {
		observer.observeBeadsRetryWaitForTest(delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// beadsDBCorruptionSignatures are the SQLite corruption signatures that br
// itself recognizes and auto-repairs by rebuilding the database from the
// authoritative JSONL (beads_rust routes them through
// retry_mutation_with_jsonl_recovery). NTM's guarded CAS-claim paths bypass
// br's CLI wrapper with direct SQL, so they must trigger the same recovery
// themselves instead of stalling the self-feeder on every scan (#227).
var beadsDBCorruptionSignatures = []string{
	"database disk image is malformed",
	"malformed database schema",
	"file is not a database",
	"database corruption",
}

// isBeadsDBCorruptionError reports whether err carries a recognized SQLite
// corruption signature anywhere in its chain's message.
func isBeadsDBCorruptionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, signature := range beadsDBCorruptionSignatures {
		if strings.Contains(message, signature) {
			return true
		}
	}
	return false
}

// recoverCorruptBeadsDatabase runs br's supported JSONL-authoritative repair
// (`br sync --import-only --rebuild`), which recreates the SQLite index from
// JSONL and removes orphaned DB entries. br >= 0.2.19 requires an explicit
// sync mode flag; --rebuild alone fails validation.
func recoverCorruptBeadsDatabase(ctx context.Context, normalizedDir string) error {
	_, err := runBdContext(ctx, normalizedDir, false, "sync", "--import-only", "--rebuild", "--json")
	return err
}

// withBeadsCorruptionRecovery executes op and, when it fails with a
// recognized corruption signature, rebuilds the Beads database from JSONL
// once and retries op exactly once. Any other error, and any error from the
// bounded retry itself, is returned unchanged so callers keep their existing
// failure semantics. This mirrors br's own recovery for direct CLI mutations
// and keeps a transient index corruption from becoming a persistent
// assignment-factory stall (#227).
func withBeadsCorruptionRecovery[T any](ctx context.Context, normalizedDir string, op func() (T, error)) (T, error) {
	result, err := op()
	if err == nil || !isBeadsDBCorruptionError(err) {
		return result, err
	}
	slog.Warn("beads database corruption detected during guarded mutation; rebuilding from JSONL",
		"dir", normalizedDir, "error", err)
	if rebuildErr := recoverCorruptBeadsDatabase(ctx, normalizedDir); rebuildErr != nil {
		var zero T
		return zero, fmt.Errorf(
			"beads database corruption detected (%v); rebuild-from-JSONL recovery failed: %w", err, rebuildErr)
	}
	slog.Info("beads database rebuilt from JSONL after corruption; retrying guarded mutation once",
		"dir", normalizedDir)
	return op()
}

var ErrBeadAlreadyClaimed = errors.New("bead is already claimed")

// ErrBeadTerminal means a guarded terminal-generation claim observed a closed
// or tombstoned issue and refused to let br's generic --claim path reopen it.
var ErrBeadTerminal = errors.New("bead is terminal")

// ErrBeadAssignmentIneligible means the final assignment claim transaction
// observed a work item that was not open and free of automation gates.
var ErrBeadAssignmentIneligible = errors.New("bead is not eligible for automated assignment")

// defaultOperatorGatedLabels is the built-in vocabulary of labels that mark a
// bead as requiring a human or operator decision before automated assignment.
// Projects with their own gating vocabulary extend (never replace) this set via
// `[assign] operator_gated_labels` in config.toml (#223); the merged set is
// installed process-wide through ConfigureOperatorGatedLabels.
var defaultOperatorGatedLabels = []string{
	"blocked-on-ivan",
	"operator-gated",
	"operator-action",
	"needs-operator",
	"human-gated",
	"human-input",
	"business-input",
	"blocked-on-operator",
	"packet-tracked",
}

var (
	operatorGatedMu     sync.RWMutex
	operatorGatedLabels = buildOperatorGatedSet(nil)
	// projectOperatorGatedLabels keeps assignment policy isolated by the
	// authoritative Beads workspace. Automated assignment commands register
	// the strictly loaded merged policy before planning or claiming, so two
	// projects in the same process cannot overwrite one another's gates.
	projectOperatorGatedLabels sync.Map // map[string]map[string]struct{}
)

// buildOperatorGatedSet merges the built-in defaults with normalized
// (lowercased, trimmed) extra labels. Empty entries are dropped.
func buildOperatorGatedSet(extra []string) map[string]struct{} {
	set := make(map[string]struct{}, len(defaultOperatorGatedLabels)+len(extra))
	for _, label := range defaultOperatorGatedLabels {
		set[label] = struct{}{}
	}
	for _, label := range extra {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	return set
}

func canonicalOperatorGatedLabels(extra []string) []string {
	set := buildOperatorGatedSet(extra)
	labels := make([]string, 0, len(set))
	for label := range set {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// ConfigureOperatorGatedLabels installs the process-wide operator-gate
// vocabulary: the built-in defaults merged with the configured extra labels
// (`[assign] operator_gated_labels`). Extras extend the defaults — they can
// never un-gate a built-in label. Matching is case-insensitive.
func ConfigureOperatorGatedLabels(extra []string) {
	set := buildOperatorGatedSet(extra)
	operatorGatedMu.Lock()
	operatorGatedLabels = set
	operatorGatedMu.Unlock()
}

// ConfigureProjectOperatorGatedLabels installs the operator-gate vocabulary
// for one authoritative project. The stored set is immutable after publication
// and may be replaced atomically when that same project's configuration is
// reloaded.
func ConfigureProjectOperatorGatedLabels(projectDir string, extra []string) error {
	key, err := operatorGateProjectKey(projectDir)
	if err != nil {
		return err
	}
	projectOperatorGatedLabels.Store(key, buildOperatorGatedSet(extra))
	return nil
}

func operatorGateProjectKey(projectDir string) (string, error) {
	resolved, err := normalizeTriageDir(strings.TrimSpace(projectDir))
	if err != nil {
		return "", fmt.Errorf("resolve operator-gate project directory %q: %w", projectDir, err)
	}
	return filepath.Clean(resolved), nil
}

func operatorGatedLabelsForProject(projectDir string) map[string]struct{} {
	if key, err := operatorGateProjectKey(projectDir); err == nil {
		if stored, ok := projectOperatorGatedLabels.Load(key); ok {
			if labels, valid := stored.(map[string]struct{}); valid {
				return labels
			}
		}
	}
	operatorGatedMu.RLock()
	labels := make(map[string]struct{}, len(operatorGatedLabels))
	for label := range operatorGatedLabels {
		labels[label] = struct{}{}
	}
	operatorGatedMu.RUnlock()
	return labels
}

// OperatorGatedLabelsForProject returns the canonical gate vocabulary for one
// authoritative project. It falls back to the compatibility process policy
// only when the project has not yet registered a strict policy snapshot.
func OperatorGatedLabelsForProject(projectDir string) []string {
	set := operatorGatedLabelsForProject(projectDir)
	labels := make([]string, 0, len(set))
	for label := range set {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// IsOperatorGatedLabelForProject reports whether label blocks automated
// assignment under the exact authoritative project's configured policy.
func IsOperatorGatedLabelForProject(projectDir, label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	_, gated := operatorGatedLabelsForProject(projectDir)[normalized]
	return gated
}

// IsContainerBeadType reports whether an issue type is a container/grouping
// node rather than a unit of implementation work. Containers are never
// auto-dispatched into worker panes (GH#242 / ntm-e165).
//
// This lives in bv, next to the operator-label gate, because it is the same
// kind of rule — part of the assignment authorization boundary — and it has to
// mean the same thing on every path that can dispatch work. It previously
// existed only in the CLI classifier, so `ntm coordinator run` happily claimed
// and mailed open epics that `ntm assign` refuses. Unlike the label gate, this
// exclusion is intrinsic to the issue type and no label can lift it.
func IsContainerBeadType(issueType string) bool {
	return strings.EqualFold(strings.TrimSpace(issueType), "epic")
}

// IsOperatorGatedLabelInPolicy evaluates a label against the built-in gates
// plus one immutable configured extras snapshot.
func IsOperatorGatedLabelInPolicy(label string, extra []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	_, gated := buildOperatorGatedSet(extra)[normalized]
	return gated
}

// OperatorGatedLabels returns the canonical normalized labels that require a
// human or operator decision before automated assignment.
func OperatorGatedLabels() []string {
	operatorGatedMu.RLock()
	labels := make([]string, 0, len(operatorGatedLabels))
	for label := range operatorGatedLabels {
		labels = append(labels, label)
	}
	operatorGatedMu.RUnlock()
	sort.Strings(labels)
	return labels
}

// AssignmentEligibilityError reports the exact tracker preconditions that
// rejected an automated assignment at the claim transaction boundary.
type AssignmentEligibilityError struct {
	BeadID             string
	Status             string
	UnresolvedBlockers []string
	OperatorLabels     []string
	Deferred           bool
	Pinned             bool
	Ephemeral          bool
	Template           bool
	Wisp               bool
}

func (e *AssignmentEligibilityError) Error() string {
	if e == nil {
		return ErrBeadAssignmentIneligible.Error()
	}
	reasons := make([]string, 0, 3)
	if status := strings.TrimSpace(e.Status); !strings.EqualFold(status, "open") {
		reasons = append(reasons, fmt.Sprintf("status is %q", status))
	}
	if len(e.UnresolvedBlockers) > 0 {
		reasons = append(reasons, "unresolved blockers: "+strings.Join(e.UnresolvedBlockers, ", "))
	}
	if len(e.OperatorLabels) > 0 {
		reasons = append(reasons, "operator-gated labels: "+strings.Join(e.OperatorLabels, ", "))
	}
	if e.Deferred {
		reasons = append(reasons, "work is deferred")
	}
	if e.Pinned {
		reasons = append(reasons, "work is pinned")
	}
	if e.Ephemeral {
		reasons = append(reasons, "work is ephemeral")
	}
	if e.Template {
		reasons = append(reasons, "work is a template")
	}
	if e.Wisp {
		reasons = append(reasons, "work is a wisp")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "assignment eligibility changed")
	}
	return fmt.Sprintf("%s: %s: %s", ErrBeadAssignmentIneligible, strings.TrimSpace(e.BeadID), strings.Join(reasons, "; "))
}

func (e *AssignmentEligibilityError) Unwrap() error {
	return ErrBeadAssignmentIneligible
}

// BeadClaimResult is the validated output of br's atomic claim operation.
type BeadClaimResult struct {
	ID        string
	Title     string
	Actor     string
	Status    string
	ClaimedAt time.Time
}

// ClaimBeadForAssignment atomically checks every authoritative automation gate
// and claims the bead in the same SQLite transaction. Unlike ClaimBead, it is
// intentionally limited to assignment paths and never delegates to br's
// generic --claim behavior.
func ClaimBeadForAssignment(ctx context.Context, dir, beadID, actor string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	return ClaimBeadForAssignmentWithOperatorGatedLabels(
		ctx, dir, beadID, actor, OperatorGatedLabelsForProject(dir),
	)
}

// ClaimBeadForAssignmentWithOperatorGatedLabels performs the final guarded
// claim using an immutable policy snapshot captured by the authoritative
// command. Passing the vocabulary explicitly prevents a concurrent project
// from changing the gates between read-only authorization and this CAS.
func ClaimBeadForAssignmentWithOperatorGatedLabels(ctx context.Context, dir, beadID, actor string, gatedLabels []string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	beadID = strings.TrimSpace(beadID)
	actor = strings.TrimSpace(actor)
	if beadID == "" {
		return BeadClaimResult{}, errors.New("bead ID is required")
	}
	if actor == "" {
		return BeadClaimResult{}, errors.New("claim actor is required")
	}
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return BeadClaimResult{}, err
	}
	return withBeadsCorruptionRecovery(ctx, normalizedDir, func() (BeadClaimResult, error) {
		return claimBeadForAssignmentAttempt(ctx, normalizedDir, beadID, actor, gatedLabels)
	})
}

// claimBeadForAssignmentAttempt is a single guarded assignment-claim attempt;
// ClaimBeadForAssignmentWithOperatorGatedLabels wraps it with the bounded
// corruption rebuild-and-retry (#227).
func claimBeadForAssignmentAttempt(ctx context.Context, normalizedDir, beadID, actor string, gatedLabels []string) (BeadClaimResult, error) {
	infoOutput, err := runBdContext(ctx, normalizedDir, false, "info", "--json", "--no-auto-import", "--no-auto-flush")
	if err != nil {
		return BeadClaimResult{}, fmt.Errorf("resolve Beads database for assignment claim: %w", err)
	}
	var info beadsWorkspaceInfo
	if err := json.Unmarshal([]byte(infoOutput), &info); err != nil {
		return BeadClaimResult{}, fmt.Errorf("parse Beads workspace info for assignment claim: %w", err)
	}
	databasePath := strings.TrimSpace(info.DatabasePath)
	if databasePath == "" {
		return BeadClaimResult{}, errors.New("assignment claim requires a SQLite Beads database")
	}

	mu := workspaceBDMutex(normalizedDir)
	if err := mu.LockContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	result, changed, claimErr := claimBeadForAssignmentTransaction(
		ctx, databasePath, beadID, actor, canonicalOperatorGatedLabels(gatedLabels),
	)
	mu.Unlock()
	if claimErr != nil {
		return BeadClaimResult{}, claimErr
	}
	if !changed {
		return result, nil
	}
	if _, err := runBdContext(ctx, normalizedDir, false, "sync", "--flush-only", "--json", "--no-auto-import"); err != nil {
		return BeadClaimResult{}, fmt.Errorf("flush assignment Beads claim: %w", err)
	}
	if err := mu.LockContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	hashErr := repairGuardedClaimContentHash(ctx, databasePath, beadID, actor)
	mu.Unlock()
	if hashErr != nil {
		return BeadClaimResult{}, hashErr
	}
	return result, nil
}

// ReleaseBeadClaim clears the exact claim owned by actor. It is intentionally
// compare-and-set: a claim now owned by a different actor is left untouched.
// Open and in-progress issues become open; closed and tombstoned issues keep
// their terminal status while only the matching actor is cleared.
func ReleaseBeadClaim(ctx context.Context, dir, beadID, actor string) (bool, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return false, err
	}
	beadID = strings.TrimSpace(beadID)
	actor = strings.TrimSpace(actor)
	if beadID == "" {
		return false, errors.New("bead ID is required")
	}
	if actor == "" {
		return false, errors.New("claim actor is required")
	}
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return false, err
	}
	return withBeadsCorruptionRecovery(ctx, normalizedDir, func() (bool, error) {
		return releaseBeadClaimAttempt(ctx, normalizedDir, beadID, actor)
	})
}

// releaseBeadClaimAttempt is a single guarded claim-release attempt;
// ReleaseBeadClaim wraps it with the bounded corruption rebuild-and-retry
// (#227).
func releaseBeadClaimAttempt(ctx context.Context, normalizedDir, beadID, actor string) (bool, error) {
	infoOutput, err := runBdContext(ctx, normalizedDir, false, "info", "--json", "--no-auto-import", "--no-auto-flush")
	if err != nil {
		return false, fmt.Errorf("resolve Beads database for claim release: %w", err)
	}
	var info beadsWorkspaceInfo
	if err := json.Unmarshal([]byte(infoOutput), &info); err != nil {
		return false, fmt.Errorf("parse Beads workspace info for claim release: %w", err)
	}
	databasePath := strings.TrimSpace(info.DatabasePath)
	if databasePath == "" {
		return false, errors.New("claim release requires a SQLite Beads database")
	}

	mu := workspaceBDMutex(normalizedDir)
	if err := mu.LockContext(ctx); err != nil {
		return false, err
	}
	releaseResult, releaseErr := releaseBeadClaimTransaction(ctx, databasePath, beadID, actor)
	mu.Unlock()
	if releaseErr != nil || !releaseResult.NeedsFinalization {
		return releaseResult.Released, releaseErr
	}
	if _, err := runBdContext(ctx, normalizedDir, false, "sync", "--flush-only", "--json", "--no-auto-import"); err != nil {
		return false, fmt.Errorf("flush released Beads claim: %w", err)
	}
	if err := mu.LockContext(ctx); err != nil {
		return false, err
	}
	hashErr := repairReleasedClaimContentHash(ctx, databasePath, beadID, releaseResult.Status)
	mu.Unlock()
	if hashErr != nil {
		return false, hashErr
	}
	return releaseResult.Released, nil
}

// ClaimBead performs the generic cross-process Beads claim. Automated NTM
// assignment paths use ClaimBeadForAssignment so readiness policy is enforced
// in the same transaction as the claim. This generic path deliberately
// disables RunBd's --no-db fallback.
func ClaimBead(ctx context.Context, dir, beadID, actor string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	beadID = strings.TrimSpace(beadID)
	actor = strings.TrimSpace(actor)
	if beadID == "" {
		return BeadClaimResult{}, errors.New("bead ID is required")
	}
	if actor == "" {
		return BeadClaimResult{}, errors.New("claim actor is required")
	}
	if assignmentstore.NonTerminalClaimGuardRequired(ctx) {
		return claimBeadNonTerminal(ctx, dir, beadID, actor)
	}

	output, err := runBdContext(ctx, dir, false, beadClaimArgs(beadID, actor)...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already assigned to") {
			return BeadClaimResult{}, fmt.Errorf("%w: %v", ErrBeadAlreadyClaimed, err)
		}
		return BeadClaimResult{}, err
	}
	result, err := parseBeadClaimOutput(output)
	if err != nil {
		return BeadClaimResult{}, err
	}
	if result.ID != beadID {
		return BeadClaimResult{}, fmt.Errorf("atomic claim returned bead %q, want %q", result.ID, beadID)
	}
	result.Actor = actor
	result.ClaimedAt = time.Now().UTC()
	return result, nil
}

type beadsWorkspaceInfo struct {
	DatabasePath string `json:"database_path"`
}

type guardedClaimIssue struct {
	ContentHash sql.NullString
	Title       string
	Status      string
	Assignee    sql.NullString
	UpdatedAt   string
	Deferred    bool
	Pinned      bool
	Ephemeral   bool
	Template    bool
}

// claimBeadNonTerminal is the compare-and-set used only when a prior terminal
// NTM assignment is starting a new generation. br 0.2.x's generic --claim
// operation also reopens closed rows, so the ordinary CLI primitive cannot
// enforce this precondition. This transaction mirrors the mutation invariants
// maintained by Beads: audit events, dirty/export-hash tracking, and blocked
// cache invalidation are committed with the guarded issue update. The installed
// br then exports the dirty row and supplies its version-specific canonical
// content hash, avoiding a second independent hash implementation in NTM.
func claimBeadNonTerminal(ctx context.Context, dir, beadID, actor string) (BeadClaimResult, error) {
	return claimBeadNonTerminalMode(ctx, dir, beadID, actor, time.Time{}, OperatorGatedLabelsForProject(dir))
}

// ClaimStaleBeadForAssignment atomically adopts an unowned in-progress bead.
// It never reopens terminal work and remains idempotent for the actor that won
// the first compare-and-set, making it suitable for bulk stale-work recovery.
func ClaimStaleBeadForAssignment(ctx context.Context, dir, beadID, actor string, expectedUpdatedAt time.Time) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	return ClaimStaleBeadForAssignmentWithOperatorGatedLabels(
		ctx, dir, beadID, actor, expectedUpdatedAt, OperatorGatedLabelsForProject(dir),
	)
}

// ClaimStaleBeadForAssignmentWithOperatorGatedLabels adopts stale work under
// the exact immutable project policy captured during assignment admission.
func ClaimStaleBeadForAssignmentWithOperatorGatedLabels(ctx context.Context, dir, beadID, actor string, expectedUpdatedAt time.Time, gatedLabels []string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	beadID = strings.TrimSpace(beadID)
	actor = strings.TrimSpace(actor)
	if beadID == "" {
		return BeadClaimResult{}, errors.New("bead ID is required")
	}
	if actor == "" {
		return BeadClaimResult{}, errors.New("claim actor is required")
	}
	if expectedUpdatedAt.IsZero() {
		return BeadClaimResult{}, errors.New("expected stale update time is required")
	}
	return claimBeadNonTerminalMode(ctx, dir, beadID, actor, expectedUpdatedAt, gatedLabels)
}

func requireBeadsMutationContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("beads mutation context is required")
	}
	return ctx.Err()
}

func claimBeadNonTerminalMode(ctx context.Context, dir, beadID, actor string, expectedStaleUpdatedAt time.Time, gatedLabels []string) (BeadClaimResult, error) {
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return BeadClaimResult{}, err
	}
	return withBeadsCorruptionRecovery(ctx, normalizedDir, func() (BeadClaimResult, error) {
		return claimBeadNonTerminalAttempt(ctx, normalizedDir, beadID, actor, expectedStaleUpdatedAt, gatedLabels)
	})
}

// claimBeadNonTerminalAttempt is a single guarded non-terminal claim attempt;
// claimBeadNonTerminalMode wraps it with the bounded corruption
// rebuild-and-retry (#227).
func claimBeadNonTerminalAttempt(ctx context.Context, normalizedDir, beadID, actor string, expectedStaleUpdatedAt time.Time, gatedLabels []string) (BeadClaimResult, error) {
	infoOutput, err := runBdContext(ctx, normalizedDir, false, "info", "--json", "--no-auto-import", "--no-auto-flush")
	if err != nil {
		return BeadClaimResult{}, fmt.Errorf("resolve Beads database for guarded claim: %w", err)
	}
	var info beadsWorkspaceInfo
	if err := json.Unmarshal([]byte(infoOutput), &info); err != nil {
		return BeadClaimResult{}, fmt.Errorf("parse Beads workspace info for guarded claim: %w", err)
	}
	databasePath := strings.TrimSpace(info.DatabasePath)
	if databasePath == "" {
		return BeadClaimResult{}, errors.New("guarded claim requires a SQLite Beads database")
	}

	mu := workspaceBDMutex(normalizedDir)
	if err := mu.LockContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	result, changed, claimErr := claimBeadNonTerminalTransaction(ctx, databasePath, beadID, actor, expectedStaleUpdatedAt, canonicalOperatorGatedLabels(gatedLabels))
	mu.Unlock()
	if claimErr != nil {
		return BeadClaimResult{}, claimErr
	}
	if !changed {
		return result, nil
	}
	if _, err := runBdContext(ctx, normalizedDir, false, "sync", "--flush-only", "--json", "--no-auto-import"); err != nil {
		return BeadClaimResult{}, fmt.Errorf("flush guarded Beads claim: %w", err)
	}
	if err := mu.LockContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	hashErr := repairGuardedClaimContentHash(ctx, databasePath, beadID, actor)
	mu.Unlock()
	if hashErr != nil {
		return BeadClaimResult{}, hashErr
	}
	return result, nil
}

func claimBeadForAssignmentTransaction(ctx context.Context, databasePath, beadID, actor string, gatedLabels []string) (BeadClaimResult, bool, error) {
	dsn := sqliteutil.ImmediateTransactionFileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)")
	database, err := sql.Open(sqliteutil.DriverName, dsn)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("open Beads database for assignment claim: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("begin assignment Beads claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	issue, err := loadGuardedClaimIssue(ctx, tx, beadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BeadClaimResult{}, false, fmt.Errorf("bead %s not found", beadID)
		}
		return BeadClaimResult{}, false, err
	}
	status := strings.ToLower(strings.TrimSpace(issue.Status))
	currentAssignee := strings.TrimSpace(issue.Assignee.String)
	if issue.Assignee.Valid && currentAssignee != "" && currentAssignee != actor {
		return BeadClaimResult{}, false, fmt.Errorf("%w: issue %s already assigned to %s", ErrBeadAlreadyClaimed, beadID, currentAssignee)
	}
	if status == "in_progress" && currentAssignee != actor {
		return BeadClaimResult{}, false, fmt.Errorf("%w: issue %s is already in progress", ErrBeadAlreadyClaimed, beadID)
	}

	blockers, err := loadUnresolvedAssignmentBlockers(ctx, tx, beadID)
	if err != nil {
		return BeadClaimResult{}, false, err
	}
	operatorLabels, err := loadAssignmentOperatorLabels(ctx, tx, beadID, gatedLabels)
	if err != nil {
		return BeadClaimResult{}, false, err
	}
	wisp := strings.Contains(strings.ToLower(beadID), "-wisp-")
	sameActorInProgress := status == "in_progress" && currentAssignee == actor
	if (!sameActorInProgress && status != "open") || len(blockers) > 0 || len(operatorLabels) > 0 || issue.Deferred || issue.Pinned || issue.Ephemeral || issue.Template || wisp {
		return BeadClaimResult{}, false, &AssignmentEligibilityError{
			BeadID: beadID, Status: issue.Status,
			UnresolvedBlockers: blockers, OperatorLabels: operatorLabels,
			Deferred: issue.Deferred, Pinned: issue.Pinned, Ephemeral: issue.Ephemeral,
			Template: issue.Template, Wisp: wisp,
		}
	}
	if sameActorInProgress {
		if err := tx.Commit(); err != nil {
			return BeadClaimResult{}, false, fmt.Errorf("commit idempotent assignment Beads claim: %w", err)
		}
		committed = true
		return BeadClaimResult{ID: beadID, Title: issue.Title, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, !issue.ContentHash.Valid, nil
	}

	claimedAt := time.Now().UTC()
	update, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET status = 'in_progress', assignee = ?, updated_at = ?, content_hash = NULL
		WHERE id = ?
		  AND LOWER(TRIM(status)) = 'open'
		  AND (assignee IS NULL OR TRIM(assignee) = '' OR assignee = ?)
		  AND (defer_until IS NULL OR (datetime(defer_until) IS NOT NULL AND datetime(defer_until) <= datetime('now')))
		  AND (pinned = 0 OR pinned IS NULL)
		  AND (ephemeral = 0 OR ephemeral IS NULL)
		  AND (is_template = 0 OR is_template IS NULL)
		  AND id NOT LIKE '%-wisp-%'`,
		actor, claimedAt.Format(time.RFC3339Nano), beadID, actor)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("compare-and-set assignment Beads claim: %w", err)
	}
	updatedRows, err := update.RowsAffected()
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("inspect assignment Beads claim result: %w", err)
	}
	if updatedRows != 1 {
		return BeadClaimResult{}, false, fmt.Errorf("%w: assignment claim precondition changed for %s", ErrBeadAlreadyClaimed, beadID)
	}
	if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", actor, issue.Status, "in_progress", claimedAt); err != nil {
		return BeadClaimResult{}, false, err
	}
	if currentAssignee != actor {
		if err := insertGuardedClaimEvent(ctx, tx, beadID, "assignee_changed", actor, currentAssignee, actor, claimedAt); err != nil {
			return BeadClaimResult{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM dirty_issues WHERE issue_id = ?", beadID); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("refresh assignment claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO dirty_issues (issue_id, marked_at) VALUES (?, ?)", beadID, claimedAt.Format(time.RFC3339Nano)); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("record assignment claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM export_hashes WHERE issue_id = ?", beadID); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("invalidate assignment claim export hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE key = 'blocked_cache_state'"); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("invalidate assignment claim blocked cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata (key, value) VALUES ('blocked_cache_state', 'stale')"); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("record assignment claim blocked cache state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("commit assignment Beads claim: %w", err)
	}
	committed = true
	return BeadClaimResult{ID: beadID, Title: issue.Title, Actor: actor, Status: "in_progress", ClaimedAt: claimedAt}, true, nil
}

func loadUnresolvedAssignmentBlockers(ctx context.Context, tx *sql.Tx, beadID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT d.depends_on_id
		FROM dependencies d
		LEFT JOIN issues blocker ON blocker.id = d.depends_on_id
		WHERE d.issue_id = ?
		  AND LOWER(TRIM(d.type)) IN ('blocks', 'conditional-blocks', 'waits-for')
		  AND (blocker.id IS NULL OR LOWER(TRIM(blocker.status)) NOT IN ('closed', 'tombstone'))
		  AND (blocker.id IS NULL OR blocker.is_template IS NULL OR blocker.is_template = 0)
		ORDER BY d.depends_on_id`, beadID)
	if err != nil {
		return nil, fmt.Errorf("inspect assignment blockers for %s: %w", beadID, err)
	}
	defer rows.Close()

	blockers := make([]string, 0)
	for rows.Next() {
		var blockerID string
		if err := rows.Scan(&blockerID); err != nil {
			return nil, fmt.Errorf("read assignment blocker for %s: %w", beadID, err)
		}
		blockerID = strings.TrimSpace(blockerID)
		if blockerID == "" {
			return nil, fmt.Errorf("assignment blocker for %s has no bead ID", beadID)
		}
		blockers = append(blockers, blockerID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignment blockers for %s: %w", beadID, err)
	}
	return blockers, nil
}

func loadAssignmentOperatorLabels(ctx context.Context, tx *sql.Tx, beadID string, gatedLabels []string) ([]string, error) {
	normalizedSet := make(map[string]struct{}, len(gatedLabels))
	for _, rawLabel := range gatedLabels {
		label := strings.ToLower(strings.TrimSpace(rawLabel))
		if label != "" {
			normalizedSet[label] = struct{}{}
		}
	}
	if len(normalizedSet) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT LOWER(TRIM(label))
		FROM labels
		WHERE issue_id = ?
		ORDER BY LOWER(TRIM(label))`, beadID)
	if err != nil {
		return nil, fmt.Errorf("inspect assignment operator labels for %s: %w", beadID, err)
	}
	defer rows.Close()

	labels := make([]string, 0)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("read assignment operator label for %s: %w", beadID, err)
		}
		if _, gated := normalizedSet[label]; gated {
			labels = append(labels, label)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignment operator labels for %s: %w", beadID, err)
	}
	return labels, nil
}

func claimBeadNonTerminalTransaction(ctx context.Context, databasePath, beadID, actor string, expectedStaleUpdatedAt time.Time, gatedLabels []string) (BeadClaimResult, bool, error) {
	dsn := sqliteutil.ImmediateTransactionFileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)")
	database, err := sql.Open(sqliteutil.DriverName, dsn)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("open Beads database for guarded claim: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("begin guarded Beads claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	issue, err := loadGuardedClaimIssue(ctx, tx, beadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BeadClaimResult{}, false, fmt.Errorf("bead %s not found", beadID)
		}
		return BeadClaimResult{}, false, err
	}
	status := strings.ToLower(strings.TrimSpace(issue.Status))
	requireUnownedInProgress := !expectedStaleUpdatedAt.IsZero()
	if requireUnownedInProgress && status != "in_progress" {
		return BeadClaimResult{}, false, &AssignmentEligibilityError{BeadID: beadID, Status: issue.Status}
	}
	if status != "open" && status != "in_progress" {
		return BeadClaimResult{}, false, errors.Join(ErrBeadAlreadyClaimed, fmt.Errorf("%w: %s has status %s", ErrBeadTerminal, beadID, issue.Status))
	}
	currentAssignee := strings.TrimSpace(issue.Assignee.String)
	if issue.Assignee.Valid && currentAssignee != "" && currentAssignee != actor {
		return BeadClaimResult{}, false, fmt.Errorf("%w: issue %s already assigned to %s", ErrBeadAlreadyClaimed, beadID, currentAssignee)
	}
	blockers, err := loadUnresolvedAssignmentBlockers(ctx, tx, beadID)
	if err != nil {
		return BeadClaimResult{}, false, err
	}
	operatorLabels, err := loadAssignmentOperatorLabels(ctx, tx, beadID, gatedLabels)
	if err != nil {
		return BeadClaimResult{}, false, err
	}
	wisp := strings.Contains(strings.ToLower(beadID), "-wisp-")
	if len(blockers) > 0 || len(operatorLabels) > 0 || issue.Deferred || issue.Pinned || issue.Ephemeral || issue.Template || wisp {
		return BeadClaimResult{}, false, &AssignmentEligibilityError{
			BeadID: beadID, Status: issue.Status,
			UnresolvedBlockers: blockers, OperatorLabels: operatorLabels,
			Deferred: issue.Deferred, Pinned: issue.Pinned, Ephemeral: issue.Ephemeral,
			Template: issue.Template, Wisp: wisp,
		}
	}
	if status == "in_progress" && currentAssignee == actor {
		if err := tx.Commit(); err != nil {
			return BeadClaimResult{}, false, fmt.Errorf("commit idempotent guarded Beads claim: %w", err)
		}
		committed = true
		return BeadClaimResult{ID: beadID, Title: issue.Title, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, !issue.ContentHash.Valid, nil
	}
	if requireUnownedInProgress {
		observedUpdatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(issue.UpdatedAt))
		if err != nil {
			return BeadClaimResult{}, false, fmt.Errorf("parse stale bead %s update time %q: %w", beadID, issue.UpdatedAt, err)
		}
		if !observedUpdatedAt.Equal(expectedStaleUpdatedAt) {
			return BeadClaimResult{}, false, fmt.Errorf("%w: stale bead %s changed after planning", ErrBeadAlreadyClaimed, beadID)
		}
		if observedUpdatedAt.After(time.Now().UTC().Add(-24 * time.Hour)) {
			return BeadClaimResult{}, false, fmt.Errorf("%w: bead %s is not stale enough to adopt", ErrBeadAssignmentIneligible, beadID)
		}
	}

	claimedAt := time.Now().UTC()
	updateSQL := `
		UPDATE issues
		SET status = 'in_progress', assignee = ?, updated_at = ?, content_hash = NULL
		WHERE id = ?
		  AND status IN ('open', 'in_progress')
		  AND (assignee IS NULL OR TRIM(assignee) = '' OR assignee = ?)`
	updateArgs := []any{actor, claimedAt.Format(time.RFC3339Nano), beadID, actor}
	if requireUnownedInProgress {
		updateSQL += " AND updated_at = ?"
		updateArgs = append(updateArgs, issue.UpdatedAt)
	}
	update, err := tx.ExecContext(ctx, updateSQL, updateArgs...)
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("compare-and-set guarded Beads claim: %w", err)
	}
	updatedRows, err := update.RowsAffected()
	if err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("inspect guarded Beads claim result: %w", err)
	}
	if updatedRows != 1 {
		return BeadClaimResult{}, false, fmt.Errorf("%w: guarded claim precondition changed for %s", ErrBeadAlreadyClaimed, beadID)
	}
	if status != "in_progress" {
		if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", actor, issue.Status, "in_progress", claimedAt); err != nil {
			return BeadClaimResult{}, false, err
		}
	}
	if currentAssignee != actor {
		if err := insertGuardedClaimEvent(ctx, tx, beadID, "assignee_changed", actor, currentAssignee, actor, claimedAt); err != nil {
			return BeadClaimResult{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM dirty_issues WHERE issue_id = ?", beadID); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("refresh guarded claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO dirty_issues (issue_id, marked_at) VALUES (?, ?)", beadID, claimedAt.Format(time.RFC3339Nano)); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("record guarded claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM export_hashes WHERE issue_id = ?", beadID); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("invalidate guarded claim export hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE key = 'blocked_cache_state'"); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("invalidate guarded claim blocked cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata (key, value) VALUES ('blocked_cache_state', 'stale')"); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("record guarded claim blocked cache state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BeadClaimResult{}, false, fmt.Errorf("commit guarded Beads claim: %w", err)
	}
	committed = true
	return BeadClaimResult{ID: beadID, Title: issue.Title, Actor: actor, Status: "in_progress", ClaimedAt: claimedAt}, true, nil
}

type releaseBeadClaimResult struct {
	Released          bool
	NeedsFinalization bool
	Status            string
}

func releaseBeadClaimTransaction(ctx context.Context, databasePath, beadID, actor string) (releaseBeadClaimResult, error) {
	dsn := sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)")
	database, err := sql.Open(sqliteutil.DriverName, dsn)
	if err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("open Beads database for claim release: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("begin Beads claim release: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	issue, err := loadGuardedClaimIssue(ctx, tx, beadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return releaseBeadClaimResult{}, fmt.Errorf("bead %s not found", beadID)
		}
		return releaseBeadClaimResult{}, err
	}
	status := strings.ToLower(strings.TrimSpace(issue.Status))
	currentAssignee := strings.TrimSpace(issue.Assignee.String)
	result := releaseBeadClaimResult{Status: status}
	if currentAssignee == "" {
		var dirty int
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM dirty_issues WHERE issue_id = ?)", beadID).Scan(&dirty); err != nil {
			return releaseBeadClaimResult{}, fmt.Errorf("inspect pending Beads claim release finalization: %w", err)
		}
		result.NeedsFinalization = dirty != 0 || !issue.ContentHash.Valid
		if err := tx.Commit(); err != nil {
			return releaseBeadClaimResult{}, fmt.Errorf("commit no-op Beads claim release: %w", err)
		}
		committed = true
		return result, nil
	}
	if currentAssignee != actor {
		if err := tx.Commit(); err != nil {
			return releaseBeadClaimResult{}, fmt.Errorf("commit different-owner Beads claim release: %w", err)
		}
		committed = true
		return result, nil
	}
	targetStatus := status
	switch status {
	case "open", "in_progress":
		targetStatus = "open"
	case "closed", "tombstone":
		// Preserve terminal status while clearing the exact NTM claim actor.
	default:
		if err := tx.Commit(); err != nil {
			return releaseBeadClaimResult{}, fmt.Errorf("commit unsupported-status Beads claim release: %w", err)
		}
		committed = true
		return result, nil
	}

	releasedAt := time.Now().UTC()
	update, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = ?, assignee = NULL, updated_at = ?, content_hash = NULL
			WHERE id = ? AND status = ? AND assignee = ?`,
		targetStatus, releasedAt.Format(time.RFC3339Nano), beadID, status, actor)
	if err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("compare-and-set Beads claim release: %w", err)
	}
	updatedRows, err := update.RowsAffected()
	if err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("inspect Beads claim release result: %w", err)
	}
	if updatedRows != 1 {
		return releaseBeadClaimResult{}, fmt.Errorf("beads claim release precondition changed for %s", beadID)
	}
	if targetStatus != status {
		if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", actor, issue.Status, targetStatus, releasedAt); err != nil {
			return releaseBeadClaimResult{}, err
		}
	}
	if err := insertGuardedClaimEvent(ctx, tx, beadID, "assignee_changed", actor, currentAssignee, "", releasedAt); err != nil {
		return releaseBeadClaimResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM dirty_issues WHERE issue_id = ?", beadID); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("refresh released claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO dirty_issues (issue_id, marked_at) VALUES (?, ?)", beadID, releasedAt.Format(time.RFC3339Nano)); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("record released claim dirty marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM export_hashes WHERE issue_id = ?", beadID); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("invalidate released claim export hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE key = 'blocked_cache_state'"); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("invalidate released claim blocked cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata (key, value) VALUES ('blocked_cache_state', 'stale')"); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("record released claim blocked cache state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return releaseBeadClaimResult{}, fmt.Errorf("commit Beads claim release: %w", err)
	}
	committed = true
	return releaseBeadClaimResult{Released: true, NeedsFinalization: true, Status: targetStatus}, nil
}

func loadGuardedClaimIssue(ctx context.Context, tx *sql.Tx, beadID string) (guardedClaimIssue, error) {
	var issue guardedClaimIssue
	// updated_at is CAST to TEXT deliberately. The column's decltype is
	// DATETIME, and the SQLite driver silently parses such columns into
	// time.Time, which database/sql then re-renders as RFC3339Nano when
	// scanning into a string: br stores "…+00:00" but the scan yields "…Z".
	// The guarded-claim CAS below binds this value back into
	// `AND updated_at = ?`, so the reformatted string matched ZERO rows and
	// every stale-bead adoption failed with a bogus
	// "guarded claim precondition changed". An expression has no decltype, so
	// the CAST delivers the stored bytes unchanged.
	err := tx.QueryRowContext(ctx, `
		SELECT content_hash, title, status, assignee, CAST(updated_at AS TEXT),
		       CASE WHEN defer_until IS NOT NULL AND (datetime(defer_until) IS NULL OR datetime(defer_until) > datetime('now')) THEN 1 ELSE 0 END,
		       CASE WHEN COALESCE(pinned, 0) != 0 THEN 1 ELSE 0 END,
		       CASE WHEN COALESCE(ephemeral, 0) != 0 THEN 1 ELSE 0 END,
		       CASE WHEN COALESCE(is_template, 0) != 0 THEN 1 ELSE 0 END
		FROM issues WHERE id = ?`, beadID).Scan(
		&issue.ContentHash, &issue.Title, &issue.Status, &issue.Assignee, &issue.UpdatedAt,
		&issue.Deferred, &issue.Pinned, &issue.Ephemeral, &issue.Template,
	)
	if err != nil {
		return guardedClaimIssue{}, err
	}
	return issue, nil
}

func insertGuardedClaimEvent(ctx context.Context, tx *sql.Tx, beadID, eventType, actor, oldValue, newValue string, createdAt time.Time) error {
	nullableOld := any(oldValue)
	if strings.TrimSpace(oldValue) == "" {
		nullableOld = nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events
			(issue_id, event_type, actor, old_value, new_value, comment, created_at, agent_name, harness, model)
		VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
		beadID, eventType, actor, nullableOld, newValue, createdAt.Format(time.RFC3339Nano),
		nullableEnvironment("BR_AGENT_NAME"), nullableEnvironment("BR_HARNESS"), nullableEnvironment("BR_MODEL"))
	if err != nil {
		return fmt.Errorf("record guarded claim %s event: %w", eventType, err)
	}
	return nil
}

func nullableEnvironment(name string) any {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil
	}
	return value
}

// computeBeadsContentHash replicates beads_rust's util::content_hash: SHA-256
// over stable-ordered fields, each encoded as an 8-byte little-endian length
// prefix followed by raw UTF-8 bytes, including the reserved empty
// slots. It must stay byte-compatible with beads_rust/src/util/hash.rs so
// NTM-side finalization produces the same hash br would have recorded.
func computeBeadsContentHash(title, description, design, acceptanceCriteria, notes,
	status, priority, issueType, assignee, owner, createdBy, externalRef, sourceSystem string,
	pinned, isTemplate bool) string {
	hasher := sha256.New()
	writeField := func(value string) {
		var prefix [8]byte
		binary.LittleEndian.PutUint64(prefix[:], uint64(len(value)))
		hasher.Write(prefix[:])
		hasher.Write([]byte(value))
	}
	writeFlag := func(value bool, label string) {
		if value {
			writeField(label)
		} else {
			writeField("")
		}
	}
	writeField(title)
	writeField(description)
	writeField(design)
	writeField(acceptanceCriteria)
	writeField(notes)
	writeField(status)
	writeField(priority)
	writeField(issueType)
	writeField(assignee)
	writeField(owner)
	writeField(createdBy)
	writeField(externalRef)
	writeField(sourceSystem)
	writeFlag(pinned, "pinned")
	writeFlag(isTemplate, "template")
	// Reserved empty slots (quality_score, crystallizes, await_type,
	// await_id, timeout, holder, hook_bead, role_bead, agent_state, role_type,
	// rig, mol_type, work_type, event_kind, actor, target, payload).
	writeField("")
	writeFlag(false, "crystallizes")
	writeField("")
	writeField("")
	writeField("0")
	for i := 0; i < 12; i++ {
		writeField("")
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// finalizeClaimContentHashLocally computes the issue's content hash from its
// current row and finalizes it (setting issues.content_hash and clearing the
// dirty marker) when the row still matches the expected finalization state.
// This is the fallback for br >= 0.2.19, whose fsqlite engine can silently
// lose its DB-side export bookkeeping (export_hashes insert, dirty_issues
// clear) when another process holds the database open, even though the JSONL
// export itself succeeded and `sync --flush-only` exited 0. The caller must
// only invoke this immediately after a successful flush so the row state is
// the exported state. Returns true when finalization was applied.
func finalizeClaimContentHashLocally(ctx context.Context, database *sql.DB, beadID, stateWhere string, stateArgs ...any) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin local claim hash finalization: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var title, description, design, acceptanceCriteria, notes, status, issueType string
	var priority int
	var assignee, owner, createdBy, externalRef, sourceSystem sql.NullString
	var pinned, isTemplate bool
	query := `SELECT title, description, design, acceptance_criteria, notes, status, priority, issue_type,
			assignee, owner, created_by, external_ref, source_system, pinned, is_template
		FROM issues WHERE id = ? AND content_hash IS NULL AND ` + stateWhere
	args := append([]any{beadID}, stateArgs...)
	if err := tx.QueryRowContext(ctx, query, args...).Scan(
		&title, &description, &design, &acceptanceCriteria, &notes, &status, &priority, &issueType,
		&assignee, &owner, &createdBy, &externalRef, &sourceSystem, &pinned, &isTemplate); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load issue for local claim hash finalization: %w", err)
	}
	contentHash := computeBeadsContentHash(title, description, design, acceptanceCriteria, notes,
		status, strconv.Itoa(priority), issueType,
		assignee.String, owner.String, createdBy.String, externalRef.String, sourceSystem.String,
		pinned, isTemplate)
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET content_hash = ? WHERE id = ? AND content_hash IS NULL", contentHash, beadID); err != nil {
		return false, fmt.Errorf("apply local claim content hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM dirty_issues WHERE issue_id = ?", beadID); err != nil {
		return false, fmt.Errorf("clear dirty marker after local claim hash finalization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit local claim hash finalization: %w", err)
	}
	committed = true
	return true, nil
}

func repairGuardedClaimContentHash(ctx context.Context, databasePath, beadID, actor string) error {
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		return fmt.Errorf("open Beads database to finalize guarded claim hash: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	result, err := database.ExecContext(ctx, `
		UPDATE issues
		SET content_hash = (SELECT content_hash FROM export_hashes WHERE issue_id = ?)
		WHERE id = ? AND status = 'in_progress' AND assignee = ? AND content_hash IS NULL
		  AND EXISTS (SELECT 1 FROM export_hashes WHERE issue_id = ?)`,
		beadID, beadID, actor, beadID)
	if err != nil {
		return fmt.Errorf("finalize guarded claim content hash: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect guarded claim content hash finalization: %w", err)
	}
	if updated == 0 {
		var status string
		var assignee, contentHash sql.NullString
		if err := database.QueryRowContext(ctx, "SELECT status, assignee, content_hash FROM issues WHERE id = ?", beadID).Scan(&status, &assignee, &contentHash); err != nil {
			return fmt.Errorf("verify guarded claim content hash finalization: %w", err)
		}
		if status == "in_progress" && strings.TrimSpace(assignee.String) == actor && !contentHash.Valid {
			finalized, finalizeErr := finalizeClaimContentHashLocally(ctx, database, beadID,
				"status = 'in_progress' AND assignee = ?", actor)
			if finalizeErr != nil {
				return finalizeErr
			}
			if !finalized {
				return errors.New("beads export did not produce a content hash for guarded claim")
			}
		}
	}
	return nil
}

func repairReleasedClaimContentHash(ctx context.Context, databasePath, beadID, expectedStatus string) error {
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		return fmt.Errorf("open Beads database to finalize released claim hash: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	result, err := database.ExecContext(ctx, `
		UPDATE issues
		SET content_hash = (SELECT content_hash FROM export_hashes WHERE issue_id = ?)
			WHERE id = ? AND status = ? AND (assignee IS NULL OR TRIM(assignee) = '') AND content_hash IS NULL
			  AND EXISTS (SELECT 1 FROM export_hashes WHERE issue_id = ?)`,
		beadID, beadID, expectedStatus, beadID)
	if err != nil {
		return fmt.Errorf("finalize released claim content hash: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect released claim content hash finalization: %w", err)
	}
	if updated == 0 {
		var actualStatus string
		var assignee, contentHash sql.NullString
		if err := database.QueryRowContext(ctx, "SELECT status, assignee, content_hash FROM issues WHERE id = ?", beadID).Scan(&actualStatus, &assignee, &contentHash); err != nil {
			return fmt.Errorf("verify released claim content hash finalization: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(actualStatus), strings.TrimSpace(expectedStatus)) && strings.TrimSpace(assignee.String) == "" && !contentHash.Valid {
			finalized, finalizeErr := finalizeClaimContentHashLocally(ctx, database, beadID,
				"status = ? AND (assignee IS NULL OR TRIM(assignee) = '')", expectedStatus)
			if finalizeErr != nil {
				return finalizeErr
			}
			if !finalized {
				return errors.New("beads export did not produce a content hash for released claim")
			}
		}
	}
	return nil
}

func beadClaimArgs(beadID, actor string) []string {
	return []string{"update", beadID, "--claim", "--actor", actor, "--json"}
}

func parseBeadClaimOutput(output string) (BeadClaimResult, error) {
	var rows []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &rows); err != nil {
		return BeadClaimResult{}, fmt.Errorf("parse atomic claim output: %w", err)
	}
	if len(rows) != 1 {
		return BeadClaimResult{}, fmt.Errorf("atomic claim returned %d rows, want 1", len(rows))
	}
	if strings.TrimSpace(rows[0].ID) == "" {
		return BeadClaimResult{}, errors.New("atomic claim response is missing id")
	}
	if !strings.EqualFold(strings.TrimSpace(rows[0].Status), "in_progress") {
		return BeadClaimResult{}, fmt.Errorf("atomic claim status is %q, want in_progress", rows[0].Status)
	}
	return BeadClaimResult{ID: rows[0].ID, Title: rows[0].Title, Status: "in_progress"}, nil
}

type brListEnvelope[T any] struct {
	Issues []T `json:"issues"`
	Beads  []T `json:"beads"`
	Items  []T `json:"items"`
}

// UnmarshalBdList parses list-style br JSON that may be either a raw array or
// an envelope object such as {"issues":[...]}.
func UnmarshalBdList[T any](output string) ([]T, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "null" {
		return []T{}, nil
	}

	var items []T
	if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
		if items == nil {
			return []T{}, nil
		}
		return items, nil
	}

	var wrapped brListEnvelope[T]
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		switch {
		case len(wrapped.Issues) > 0:
			return wrapped.Issues, nil
		case len(wrapped.Beads) > 0:
			return wrapped.Beads, nil
		case len(wrapped.Items) > 0:
			return wrapped.Items, nil
		}
		// An envelope whose list key is present but EMPTY means "no results",
		// not "not an envelope". Falling through to the single-object branch
		// below unmarshalled the envelope itself into T — unknown fields are
		// ignored, so it always succeeded — and returned one phantom
		// zero-valued item. `br list --status=X --json` answers
		// {"issues":[],"total":0,...} for an empty result, so every caller
		// with no matching beads got one blank bead: the alert generator
		// emitted a stale-bead alert for ID "" (zero UpdatedAt is always
		// older than any threshold) and the REST beads endpoint returned the
		// envelope as a bead.
		if isEmptyBrListEnvelope(trimmed) {
			return []T{}, nil
		}
	}

	var single T
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
		// Prove the payload actually populated T before treating it as one
		// item. json.Unmarshal ignores unknown fields, so decoding ANY JSON
		// object into T succeeds and yields a zero value — which is how an
		// unrecognized envelope became a single phantom bead with ID "".
		// isEmptyBrListEnvelope above closes that for the three known list
		// keys; this closes the whole class, including a future or renamed
		// envelope key and an error object emitted on stdout with exit 0.
		var zero T
		if !reflect.DeepEqual(single, zero) {
			return []T{single}, nil
		}
		return []T{}, nil
	}

	return nil, fmt.Errorf("parse br list output: %s", trimmed)
}

// isEmptyBrListEnvelope reports whether the payload is a br list envelope
// whose list key is present but empty (or null) — i.e. a genuine "no results"
// answer rather than some other JSON object.
func isEmptyBrListEnvelope(payload string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return false
	}
	for _, key := range []string{"issues", "beads", "items"} {
		raw, ok := probe[key]
		if !ok {
			continue
		}
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			continue
		}
		return len(list) == 0
	}
	return false
}

func isNoBeadsDBError(streams ...string) bool {
	s := strings.ToLower(strings.Join(streams, "\n"))
	return strings.Contains(s, "no beads database found") || strings.Contains(s, "use 'br --no-db'")
}

func isTransientBeadsDBError(streams ...string) bool {
	s := strings.ToLower(strings.Join(streams, "\n"))
	return strings.Contains(s, "database is busy") || strings.Contains(s, "database is locked")
}

func transientBeadsDBBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := 50 * time.Millisecond
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= 800*time.Millisecond {
			return 800 * time.Millisecond
		}
	}
	return backoff
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func stripFlagWithValue(args []string, flag string) []string {
	if len(args) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			filtered = append(filtered, args[i])
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return filtered
}

// GetBeadStatus returns the current status for a bead ID using br show --json.
func GetBeadStatus(dir, beadID string) (string, error) {
	return GetBeadStatusContext(context.Background(), dir, beadID)
}

// GetBeadStatusContext returns exact bead status with caller cancellation.
func GetBeadStatusContext(ctx context.Context, dir, beadID string) (string, error) {
	if strings.TrimSpace(beadID) == "" {
		return "", errors.New("bead ID is required")
	}

	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return "", err
	}
	return parseBeadStatusOutput(output)
}

// BeadAssignmentDetails is the exact work-item state assignment gates need.
// Unlike triage output, br show is keyed by the requested ID and is not capped
// or ranking-dependent.
type BeadAssignmentDetails struct {
	ID                   string
	Title                string
	IssueType            string
	Status               string
	Priority             int
	Assignee             string
	DeferUntil           *time.Time
	Pinned               bool
	Ephemeral            bool
	Template             bool
	Wisp                 bool
	Labels               []string
	BlockedBy            []string
	BlockingDependencies []BeadDependencyState
}

// BeadAssignmentAuthorization defines the tracker ownership states an atomic
// assignment generation may accept. Automation gates are never relaxed by
// these state allowances.
type BeadAssignmentAuthorization struct {
	BeadID                    string
	ExpectedAssignee          string
	AllowUnassignedOpen       bool
	AllowOwnedOpen            bool
	AllowUnassignedInProgress bool
	AllowOwnedInProgress      bool
}

// ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels validates against
// the immutable vocabulary captured by the caller at strict policy admission.
// The same slice can then be passed to the guarded claim, keeping both sides of
// the atomic boundary on an identical policy snapshot.
func ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details *BeadAssignmentDetails, authorization BeadAssignmentAuthorization, gatedLabels []string) error {
	set := buildOperatorGatedSet(gatedLabels)
	return validateBeadAssignmentAuthorization(details, authorization, func(label string) bool {
		_, gated := set[strings.ToLower(strings.TrimSpace(label))]
		return gated
	})
}

func validateBeadAssignmentAuthorization(details *BeadAssignmentDetails, authorization BeadAssignmentAuthorization, operatorGated func(string) bool) error {
	beadID := strings.TrimSpace(authorization.BeadID)
	if beadID == "" {
		return fmt.Errorf("%w: requested bead ID is empty", ErrBeadAssignmentIneligible)
	}
	if details == nil {
		return fmt.Errorf("%w: live details for %s are missing", ErrBeadAssignmentIneligible, beadID)
	}
	liveID := strings.TrimSpace(details.ID)
	if liveID != beadID {
		return fmt.Errorf("%w: live details returned bead %q, want %q", ErrBeadAssignmentIneligible, liveID, beadID)
	}

	blockerSet := make(map[string]struct{}, len(details.BlockedBy)+len(details.BlockingDependencies))
	for _, blockerID := range details.BlockedBy {
		if blockerID = strings.TrimSpace(blockerID); blockerID != "" {
			blockerSet[blockerID] = struct{}{}
		}
	}
	for _, dependency := range details.BlockingDependencies {
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "closed" || status == "tombstone" {
			continue
		}
		if blockerID := strings.TrimSpace(dependency.ID); blockerID != "" {
			blockerSet[blockerID] = struct{}{}
		}
	}
	blockers := make([]string, 0, len(blockerSet))
	for blockerID := range blockerSet {
		blockers = append(blockers, blockerID)
	}
	sort.Strings(blockers)

	operatorLabels := make([]string, 0, len(details.Labels))
	for _, label := range details.Labels {
		if operatorGated(label) {
			operatorLabels = append(operatorLabels, strings.TrimSpace(label))
		}
	}
	sort.Strings(operatorLabels)
	deferred := details.DeferUntil != nil && details.DeferUntil.After(time.Now().UTC())
	wisp := details.Wisp || strings.Contains(strings.ToLower(liveID), "-wisp-")
	if len(blockers) > 0 || len(operatorLabels) > 0 || deferred || details.Pinned || details.Ephemeral || details.Template || wisp {
		return &AssignmentEligibilityError{
			BeadID: liveID, Status: details.Status,
			UnresolvedBlockers: blockers, OperatorLabels: operatorLabels,
			Deferred: deferred, Pinned: details.Pinned, Ephemeral: details.Ephemeral,
			Template: details.Template, Wisp: wisp,
		}
	}

	status := strings.ToLower(strings.TrimSpace(details.Status))
	status = strings.ReplaceAll(status, "-", "_")
	status = strings.ReplaceAll(status, " ", "_")
	assignee := strings.TrimSpace(details.Assignee)
	expectedAssignee := strings.TrimSpace(authorization.ExpectedAssignee)
	switch status {
	case "open":
		if assignee == "" && authorization.AllowUnassignedOpen {
			return nil
		}
		if expectedAssignee != "" && assignee == expectedAssignee && authorization.AllowOwnedOpen {
			return nil
		}
	case "in_progress":
		if assignee == "" && authorization.AllowUnassignedInProgress {
			return nil
		}
		if expectedAssignee != "" && assignee == expectedAssignee && authorization.AllowOwnedInProgress {
			return nil
		}
	}
	return fmt.Errorf("%w: bead %s live status %q and assignee %q do not match the authorized generation state for %q", ErrBeadAssignmentIneligible, liveID, details.Status, details.Assignee, expectedAssignee)
}

type beadShowDependency struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	DependencyType string `json:"dependency_type"`
}

type beadShowAssignmentRow struct {
	ID           string               `json:"id"`
	Title        string               `json:"title"`
	IssueType    string               `json:"issue_type"`
	Status       string               `json:"status"`
	Priority     int                  `json:"priority"`
	Assignee     string               `json:"assignee"`
	DeferUntil   *time.Time           `json:"defer_until"`
	Pinned       bool                 `json:"pinned"`
	Ephemeral    bool                 `json:"ephemeral"`
	Template     bool                 `json:"is_template"`
	Labels       []string             `json:"labels"`
	Dependencies []beadShowDependency `json:"dependencies"`
	Dependents   []beadShowDependency `json:"dependents"`
}

// BeadDependencyState is one blocking dependency exactly as reported by
// br show. Terminal dependencies are retained so callers can prove that a
// previously blocked bead became unblocked instead of mistaking filtered data
// for an empty dependency set.
type BeadDependencyState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// BeadDependentState is one local work item blocked by the inspected bead.
// External dependency endpoints are intentionally omitted because they cannot
// be validated or assigned through the local Beads database.
type BeadDependentState struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

// IsBlockingDependencyType reports whether a Beads relationship participates
// in readiness. Keep this set aligned with the guarded assignment claim query.
func IsBlockingDependencyType(dependencyType string) bool {
	switch strings.ToLower(strings.TrimSpace(dependencyType)) {
	case "blocks", "conditional-blocks", "waits-for":
		return true
	default:
		return false
	}
}

// GetBeadAssignmentDetailsContext returns exact title, status, assignee,
// labels, and unresolved blocking dependencies for one bead using
// br show --json, with caller cancellation.
func GetBeadAssignmentDetailsContext(ctx context.Context, dir, beadID string) (*BeadAssignmentDetails, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return nil, err
	}
	details, err := parseBeadAssignmentDetailsOutput(output)
	if err != nil {
		return nil, err
	}
	if details.ID != beadID {
		return nil, fmt.Errorf("br show returned bead %q, want %q", details.ID, beadID)
	}
	return details, nil
}

// GetBeadBlockingDependentsContext returns every local work item whose
// readiness depends on beadID. The source is one uncapped br show response,
// rather than a ranked triage list, so no dependent is dropped by a limit.
func GetBeadBlockingDependentsContext(ctx context.Context, dir, beadID string) ([]BeadDependentState, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return nil, err
	}
	row, err := parseBeadShowAssignmentRow(output)
	if err != nil {
		return nil, err
	}
	if row.ID != beadID {
		return nil, fmt.Errorf("br show returned bead %q, want %q", row.ID, beadID)
	}
	return blockingDependentStates(row.Dependents)
}

func parseBeadAssignmentDetailsOutput(output string) (*BeadAssignmentDetails, error) {
	row, err := parseBeadShowAssignmentRow(output)
	if err != nil {
		return nil, err
	}

	dependencyStates, err := blockingDependencyStates(row.Dependencies)
	if err != nil {
		return nil, err
	}
	blockedBy := make([]string, 0, len(dependencyStates))
	for _, dependency := range dependencyStates {
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "closed" || status == "tombstone" {
			continue
		}
		blockedBy = append(blockedBy, dependency.ID)
	}

	labels := make([]string, 0, len(row.Labels))
	seenLabels := make(map[string]struct{}, len(row.Labels))
	for _, rawLabel := range row.Labels {
		label := strings.TrimSpace(rawLabel)
		if label == "" {
			continue
		}
		if _, duplicate := seenLabels[label]; duplicate {
			continue
		}
		seenLabels[label] = struct{}{}
		labels = append(labels, label)
	}
	sort.Strings(labels)

	return &BeadAssignmentDetails{
		ID: row.ID, Title: row.Title, IssueType: row.IssueType, Status: row.Status, Priority: row.Priority,
		Assignee: row.Assignee, DeferUntil: row.DeferUntil, Pinned: row.Pinned, Ephemeral: row.Ephemeral,
		Template: row.Template, Wisp: strings.Contains(strings.ToLower(row.ID), "-wisp-"), Labels: labels,
		BlockedBy: blockedBy, BlockingDependencies: dependencyStates,
	}, nil
}

func parseBeadShowAssignmentRow(output string) (beadShowAssignmentRow, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return beadShowAssignmentRow{}, errors.New("empty bead output")
	}

	var row beadShowAssignmentRow
	var rows []beadShowAssignmentRow
	if err := json.Unmarshal([]byte(trimmed), &rows); err == nil {
		if len(rows) != 1 {
			return beadShowAssignmentRow{}, fmt.Errorf("br show returned %d beads, want exactly 1", len(rows))
		}
		row = rows[0]
	} else if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
		return beadShowAssignmentRow{}, fmt.Errorf("parse bead assignment details: %w", err)
	}

	row.ID = strings.TrimSpace(row.ID)
	row.Title = strings.TrimSpace(row.Title)
	row.IssueType = strings.TrimSpace(row.IssueType)
	row.Status = strings.TrimSpace(row.Status)
	row.Assignee = strings.TrimSpace(row.Assignee)
	if row.ID == "" {
		return beadShowAssignmentRow{}, errors.New("bead ID field not found in br show response")
	}
	if row.Status == "" {
		return beadShowAssignmentRow{}, errors.New("bead status field not found in br show response")
	}
	return row, nil
}

func blockingDependencyStates(dependencies []beadShowDependency) ([]BeadDependencyState, error) {
	statesByID := make(map[string]string, len(dependencies))
	for _, dependency := range dependencies {
		if !IsBlockingDependencyType(dependency.DependencyType) {
			continue
		}
		dependencyID := strings.TrimSpace(dependency.ID)
		if dependencyID == "" {
			return nil, errors.New("blocking dependency has no bead ID")
		}
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "" {
			return nil, fmt.Errorf("blocking dependency %s has no status", dependencyID)
		}
		if existing, duplicate := statesByID[dependencyID]; duplicate && existing != status {
			return nil, fmt.Errorf("blocking dependency %s has conflicting statuses %q and %q", dependencyID, existing, status)
		}
		statesByID[dependencyID] = status
	}
	states := make([]BeadDependencyState, 0, len(statesByID))
	for dependencyID, status := range statesByID {
		states = append(states, BeadDependencyState{ID: dependencyID, Status: status})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func blockingDependentStates(dependents []beadShowDependency) ([]BeadDependentState, error) {
	statesByID := make(map[string]BeadDependentState, len(dependents))
	for _, dependent := range dependents {
		if !IsBlockingDependencyType(dependent.DependencyType) {
			continue
		}
		dependentID := strings.TrimSpace(dependent.ID)
		if strings.HasPrefix(strings.ToLower(dependentID), "external:") {
			continue
		}
		if dependentID == "" {
			return nil, errors.New("blocking dependent has no bead ID")
		}
		status := strings.ToLower(strings.TrimSpace(dependent.Status))
		if status == "" {
			return nil, fmt.Errorf("blocking dependent %s has no status", dependentID)
		}
		state := BeadDependentState{
			ID: dependentID, Title: strings.TrimSpace(dependent.Title), Status: status, Priority: dependent.Priority,
		}
		if existing, duplicate := statesByID[dependentID]; duplicate && existing != state {
			return nil, fmt.Errorf("blocking dependent %s has conflicting rows", dependentID)
		}
		statesByID[dependentID] = state
	}
	states := make([]BeadDependentState, 0, len(statesByID))
	for _, state := range statesByID {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func parseBeadStatusOutput(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("empty bead output")
	}

	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		if len(arr) == 0 {
			return "", errors.New("empty bead response array")
		}
		if status, ok := extractStatusField(arr[0]); ok {
			return status, nil
		}
		return "", errors.New("status field not found in bead response")
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return "", fmt.Errorf("parse bead status: %w", err)
	}
	if status, ok := extractStatusField(obj); ok {
		return status, nil
	}
	return "", errors.New("status field not found in bead response")
}

func extractStatusField(payload map[string]interface{}) (string, bool) {
	raw, ok := payload["status"]
	if !ok {
		return "", false
	}
	status, ok := raw.(string)
	if !ok {
		return "", false
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "", false
	}
	return status, true
}

// IsBdInstalled checks if br is available in PATH (legacy name).
func IsBdInstalled() bool {
	_, err := exec.LookPath("br")
	return err == nil
}

// GetBeadsSummary attempts to get bead statistics from the br command.
func GetBeadsSummary(dir string, limit int) *BeadsSummary {
	result, _ := GetBeadsSummaryContext(context.Background(), dir, limit)
	return result
}

// GetBeadsSummaryContext gets bead statistics while honoring cancellation
// across every br subprocess used to assemble the summary.
func GetBeadsSummaryContext(ctx context.Context, dir string, limit int) (*BeadsSummary, error) {
	result := &BeadsSummary{}
	if ctx == nil {
		return result, errors.New("beads summary context is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		result.Available = false
		result.Reason = err.Error()
		return result, nil
	}
	dir = normalizedDir

	// Validate directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		result.Available = false
		result.Reason = fmt.Sprintf("project directory does not exist: %s", dir)
		return result, nil
	}

	// Check if .beads directory exists
	beadsDir := filepath.Join(dir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		result.Available = false
		result.Reason = fmt.Sprintf("no .beads/ directory in %s", dir)
		return result, nil
	}

	if !IsBdInstalled() {
		result.Available = false
		result.Reason = "br not installed"
		return result, nil
	}

	result.Project = dir

	// Try to run br stats --json to get summary
	statsOutput, err := RunBdContext(ctx, dir, "stats", "--json")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		result.Available = false
		result.Reason = fmt.Sprintf("br stats failed: %v", err)
		return result, nil
	}

	// br stats --json nests counters under summary. Decoding this into a flat
	// struct succeeds while leaving every counter at zero, which makes a real
	// backlog indistinguishable from an empty one to snapshot and queue-dry
	// consumers. Require the envelope explicitly so a future schema drift also
	// degrades the source instead of publishing false zero counts.
	var statsEnvelope struct {
		Summary *struct {
			TotalIssues      int `json:"total_issues"`
			OpenIssues       int `json:"open_issues"`
			InProgressIssues int `json:"in_progress_issues"`
			BlockedIssues    int `json:"blocked_issues"`
			ReadyIssues      int `json:"ready_issues"`
			ClosedIssues     int `json:"closed_issues"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(statsOutput), &statsEnvelope); err != nil {
		result.Available = false
		result.Reason = fmt.Sprintf("parse stats failed: %v", err)
		return result, nil
	}
	if statsEnvelope.Summary == nil {
		result.Available = false
		result.Reason = "parse stats failed: missing summary object"
		return result, nil
	}

	result.Available = true
	result.Total = statsEnvelope.Summary.TotalIssues
	result.Open = statsEnvelope.Summary.OpenIssues
	result.InProgress = statsEnvelope.Summary.InProgressIssues
	result.Blocked = statsEnvelope.Summary.BlockedIssues
	result.Ready = statsEnvelope.Summary.ReadyIssues
	result.Closed = statsEnvelope.Summary.ClosedIssues

	// Get ready preview (top N ready issues sorted by priority)
	ready, err := GetReadyPreviewContext(ctx, dir, limit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
	} else {
		result.ReadyPreview = ready
	}

	// Get in-progress list
	inProgress, err := GetInProgressListContext(ctx, dir, limit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
	} else {
		result.InProgressList = inProgress
	}

	return result, nil
}

// GetReadyPreviewContext returns top N ready beads sorted by priority and
// preserves command, parse, and cancellation failures so callers can
// distinguish checked-empty from unknown.
func GetReadyPreviewContext(ctx context.Context, dir string, limit int) ([]BeadPreview, error) {
	previews := make([]BeadPreview, 0)
	output, err := RunBdContext(ctx, dir, "ready", "--json")
	if err != nil {
		return nil, fmt.Errorf("list ready beads: %w", err)
	}

	var issues []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Priority int    `json:"priority"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Priority int    `json:"priority"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse ready beads: %w", err)
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

	return previews, nil
}

// GetInProgressList returns in-progress beads with assignees.
//
// br walks the filesystem upward to find a workspace root, so this can
// return rows from a parent repo when the caller's directory has no local
// .beads/. Callers that need a strict "this directory only" contract
// (recovery context, anywhere parent-row bleed would be incorrect) should
// gate via [`HasLocalBeadsDB`] before calling this and refuse to surface
// the result if it returns false. See #130.
func GetInProgressList(dir string, limit int) []BeadInProgress {
	items, _ := GetInProgressListContext(context.Background(), dir, limit)
	return items
}

// GetInProgressListContext returns in-progress beads without erasing command,
// parse, or cancellation failures. A limit of zero requests the full list.
func GetInProgressListContext(ctx context.Context, dir string, limit int) ([]BeadInProgress, error) {
	if limit < 0 {
		return nil, fmt.Errorf("in-progress limit must not be negative: %d", limit)
	}
	items := make([]BeadInProgress, 0)
	output, err := RunBdContext(ctx, dir, "list", "--status=in_progress", "--json", "--limit", strconv.Itoa(limit))
	if err != nil {
		return nil, fmt.Errorf("list in-progress beads: %w", err)
	}

	var issues []struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Assignee  string    `json:"assignee"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Assignee  string    `json:"assignee"`
		UpdatedAt time.Time `json:"updated_at"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse in-progress beads: %w", err)
	}

	// A zero limit is br's documented unlimited mode. Positive limits are also
	// forwarded to br so the subprocess does not serialize rows the caller will
	// immediately discard.
	for i, issue := range issues {
		if limit > 0 && i >= limit {
			break
		}
		items = append(items, BeadInProgress{
			ID:        issue.ID,
			Title:     issue.Title,
			Assignee:  issue.Assignee,
			UpdatedAt: issue.UpdatedAt,
		})
	}

	return items, nil
}

// GetRecentlyCompletedListContext returns recently completed beads (beads with
// status=closed, ordered by completion time descending) without erasing
// command, parse, or cancellation failures.
//
// Like [`GetInProgressList`] this will walk up to a parent .beads/ when the
// directory has none of its own; callers that need a strict per-directory
// view should pre-check [`HasLocalBeadsDB`] (#130).
func GetRecentlyCompletedListContext(ctx context.Context, dir string, limit int) ([]BeadPreview, error) {
	items := make([]BeadPreview, 0)

	output, err := RunBdContext(ctx, dir, "list", "--status=closed", "--json")
	if err != nil {
		return nil, fmt.Errorf("list recently completed beads: %w", err)
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse recently completed beads: %w", err)
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

	return items, nil
}

// GetBlockedListContext returns blocked beads (beads that are blocked by
// dependencies) without erasing command, parse, or cancellation failures.
//
// Like [`GetInProgressList`] this will walk up to a parent .beads/ when the
// directory has none of its own; callers that need a strict per-directory
// view should pre-check [`HasLocalBeadsDB`] (#130).
func GetBlockedListContext(ctx context.Context, dir string, limit int) ([]BeadPreview, error) {
	items := make([]BeadPreview, 0)
	output, err := RunBdContext(ctx, dir, "blocked", "--json")
	if err != nil {
		return nil, fmt.Errorf("list blocked beads: %w", err)
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if issues, err = UnmarshalBdList[struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}](output); err != nil {
		return nil, fmt.Errorf("parse blocked beads: %w", err)
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

	return items, nil
}
