// Package robot provides machine-readable output for AI agents.
// health.go contains the --robot-health flag implementation.
package robot

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// HealthOutput provides a focused project health summary for AI agents
type HealthOutput struct {
	CheckedAt time.Time `json:"checked_at"`

	// System-level health
	System SystemHealthInfo `json:"system"`

	// Agent/session health matrix
	Sessions map[string]SessionHealthInfo `json:"sessions"`

	// Alerts for detected issues
	Alerts []string `json:"alerts"`

	// Project/beads health (existing functionality)
	BvAvailable       bool                  `json:"bv_available"`
	BdAvailable       bool                  `json:"bd_available"`
	Error             string                `json:"error,omitempty"`
	DriftStatus       string                `json:"drift_status,omitempty"`
	DriftMessage      string                `json:"drift_message,omitempty"`
	TopBottlenecks    []bv.NodeScore        `json:"top_bottlenecks,omitempty"`
	TopKeystones      []bv.NodeScore        `json:"top_keystones,omitempty"`
	ReadyCount        int                   `json:"ready_count"`
	InProgressCount   int                   `json:"in_progress_count"`
	BlockedCount      int                   `json:"blocked_count"`
	NextRecommended   []RecommendedAction   `json:"next_recommended,omitempty"`
	DependencyContext *bv.DependencyContext `json:"dependency_context,omitempty"`
}

// SystemHealthInfo contains system-level health metrics
type SystemHealthInfo struct {
	TmuxOK     bool    `json:"tmux_ok"`
	DiskFreeGB float64 `json:"disk_free_gb"`
	LoadAvg    float64 `json:"load_avg"`
}

// SessionHealthInfo contains health info for a single session
type SessionHealthInfo struct {
	Healthy bool                       `json:"healthy"`
	Agents  map[string]AgentHealthInfo `json:"agents"`
}

// AgentHealthInfo contains health metrics for a single agent
type AgentHealthInfo struct {
	Responsive      bool   `json:"responsive"`
	OutputRate      string `json:"output_rate"` // "high", "medium", "low", "none"
	LastActivitySec int    `json:"last_activity_sec"`
	Issue           string `json:"issue,omitempty"`
}

// RecommendedAction is a simplified priority recommendation
type RecommendedAction struct {
	IssueID  string `json:"issue_id"`
	Title    string `json:"title"`
	Reason   string `json:"reason"`
	Priority int    `json:"priority"`
}

// noOutputThreshold is the time in seconds after which an agent is considered unresponsive
const noOutputThreshold = 300 // 5 minutes

// PrintHealth outputs a focused project health summary for AI consumption
func PrintHealth() error {
	output := HealthOutput{
		CheckedAt:   time.Now().UTC(),
		BvAvailable: bv.IsInstalled(),
		BdAvailable: bv.IsBdInstalled(),
		Sessions:    make(map[string]SessionHealthInfo),
		Alerts:      []string{},
	}

	// Get system health
	output.System = getSystemHealth()

	// Get agent/session health matrix
	populateAgentHealth(&output)

	// Get drift status
	drift := bv.CheckDrift("")
	output.DriftStatus = drift.Status.String()
	output.DriftMessage = drift.Message

	// Get top bottlenecks (limit to 5)
	bottlenecks, err := bv.GetTopBottlenecks("", 5)
	if err == nil {
		output.TopBottlenecks = bottlenecks
	}

	// Get insights for keystones
	insights, err := bv.GetInsights("")
	if err == nil && insights != nil {
		keystones := insights.Keystones
		if len(keystones) > 5 {
			keystones = keystones[:5]
		}
		output.TopKeystones = keystones
	}

	// Get priority recommendations
	recommendations, err := bv.GetNextActions("", 5)
	if err == nil {
		for _, rec := range recommendations {
			var reason string
			if len(rec.Reasoning) > 0 {
				reason = rec.Reasoning[0]
			}
			output.NextRecommended = append(output.NextRecommended, RecommendedAction{
				IssueID:  rec.IssueID,
				Title:    rec.Title,
				Reason:   reason,
				Priority: rec.SuggestedPriority,
			})
		}
	}

	// Get dependency context (includes ready/in-progress/blocked counts)
	depCtx, err := bv.GetDependencyContext("", 5)
	if err == nil {
		output.DependencyContext = depCtx
		output.ReadyCount = depCtx.ReadyCount
		output.BlockedCount = depCtx.BlockedCount
		output.InProgressCount = len(depCtx.InProgressTasks)
	}

	return encodeJSON(output)
}

// getSystemHealth returns system-level health metrics
func getSystemHealth() SystemHealthInfo {
	info := SystemHealthInfo{
		TmuxOK: tmux.IsInstalled(),
	}

	// Get disk free space (platform-specific)
	info.DiskFreeGB = getDiskFreeGB()

	// Get load average (platform-specific)
	info.LoadAvg = getLoadAverage()

	return info
}

// getDiskFreeGB returns the free disk space in GB for the current directory
func getDiskFreeGB() float64 {
	switch runtime.GOOS {
	case "darwin", "linux":
		// Use df command
		cmd := exec.Command("df", "-k", ".")
		out, err := cmd.Output()
		if err != nil {
			return -1
		}
		lines := strings.Split(string(out), "\n")
		if len(lines) < 2 {
			return -1
		}
		// Parse the second line (data line)
		fields := strings.Fields(lines[1])
		if len(fields) < 4 {
			return -1
		}
		// Field 3 is available space in KB
		availKB, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			return -1
		}
		return availKB / (1024 * 1024) // Convert KB to GB
	default:
		return -1
	}
}

// getLoadAverage returns the 1-minute load average
func getLoadAverage() float64 {
	switch runtime.GOOS {
	case "darwin", "linux":
		// Use sysctl on macOS, /proc/loadavg on Linux
		if runtime.GOOS == "darwin" {
			cmd := exec.Command("sysctl", "-n", "vm.loadavg")
			out, err := cmd.Output()
			if err != nil {
				return -1
			}
			// Output format: "{ 1.23 2.34 3.45 }"
			s := strings.TrimSpace(string(out))
			s = strings.TrimPrefix(s, "{ ")
			s = strings.TrimSuffix(s, " }")
			fields := strings.Fields(s)
			if len(fields) < 1 {
				return -1
			}
			load, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				return -1
			}
			return load
		}
		// Linux: read from /proc/loadavg
		cmd := exec.Command("cat", "/proc/loadavg")
		out, err := cmd.Output()
		if err != nil {
			return -1
		}
		fields := strings.Fields(string(out))
		if len(fields) < 1 {
			return -1
		}
		load, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return -1
		}
		return load
	default:
		return -1
	}
}

// populateAgentHealth fills in the agent health matrix for all sessions
func populateAgentHealth(output *HealthOutput) {
	if !output.System.TmuxOK {
		output.Alerts = append(output.Alerts, "tmux not available")
		return
	}

	sessions, err := tmux.ListSessions()
	if err != nil {
		output.Alerts = append(output.Alerts, fmt.Sprintf("failed to list sessions: %v", err))
		return
	}

	for _, sess := range sessions {
		sessHealth := SessionHealthInfo{
			Healthy: true,
			Agents:  make(map[string]AgentHealthInfo),
		}

		panes, err := tmux.GetPanes(sess.Name)
		if err != nil {
			output.Alerts = append(output.Alerts, fmt.Sprintf("%s: failed to get panes: %v", sess.Name, err))
			sessHealth.Healthy = false
			output.Sessions[sess.Name] = sessHealth
			continue
		}

		for _, pane := range panes {
			paneKey := fmt.Sprintf("%d.%d", 0, pane.Index)
			agentHealth := getAgentHealth(sess.Name, pane)

			sessHealth.Agents[paneKey] = agentHealth

			// Check for issues and add to alerts
			if !agentHealth.Responsive {
				sessHealth.Healthy = false
				output.Alerts = append(output.Alerts, fmt.Sprintf("%s %s: %s", sess.Name, paneKey, agentHealth.Issue))
			}
		}

		output.Sessions[sess.Name] = sessHealth
	}
}

// getAgentHealth calculates health metrics for a single agent pane
func getAgentHealth(session string, pane tmux.Pane) AgentHealthInfo {
	health := AgentHealthInfo{
		Responsive:      true,
		OutputRate:      "unknown",
		LastActivitySec: -1,
	}

	// Get pane activity time
	activityTime, err := tmux.GetPaneActivity(pane.ID)
	if err == nil {
		health.LastActivitySec = int(time.Since(activityTime).Seconds())

		// Check if unresponsive (no output for threshold time)
		if health.LastActivitySec > noOutputThreshold {
			health.Responsive = false
			health.Issue = fmt.Sprintf("no_output_%dm", noOutputThreshold/60)
		}
	}

	// Calculate output rate from recent activity
	health.OutputRate = calculateOutputRate(health.LastActivitySec)

	// Capture recent output to detect error states
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	captured, err := tmux.CapturePaneOutputContext(ctx, pane.ID, 20)
	cancel()
	if err == nil {
		// Get agent type from pane info
		agentType := agentTypeString(pane.Type)
		if agentType == "unknown" {
			agentType = detectAgentType(pane.Title)
		}
		shortAgentType := translateAgentTypeForStatus(agentType)
		state := determineState(captured, shortAgentType)

		if state == "error" {
			health.Responsive = false
			health.Issue = "error_state_detected"
		}
	}

	return health
}

// calculateOutputRate determines output rate based on last activity time
func calculateOutputRate(lastActivitySec int) string {
	if lastActivitySec < 0 {
		return "unknown"
	}
	switch {
	case lastActivitySec <= 1:
		return "high" // >1 line/sec equivalent
	case lastActivitySec <= 10:
		return "medium"
	case lastActivitySec <= 60:
		return "low" // <1 line/min equivalent
	default:
		return "none"
	}
}

// =============================================================================
// Agent Health States and Activity Detection Integration
// =============================================================================
//
// Note: HealthState enum is defined in routing.go with values:
// - HealthHealthy, HealthDegraded, HealthUnhealthy, HealthRateLimited

// HealthCheck contains the result of a comprehensive health check
type HealthCheck struct {
	PaneID       string              `json:"pane_id"`
	AgentType    string              `json:"agent_type"`
	HealthState  HealthState         `json:"health_state"`
	ProcessCheck *ProcessCheckResult `json:"process_check"`
	StallCheck   *StallCheckResult   `json:"stall_check"`
	ErrorCheck   *ErrorCheckResult   `json:"error_check"`
	Confidence   float64             `json:"confidence"`
	Reason       string              `json:"reason"`
	CheckedAt    time.Time           `json:"checked_at"`
}

// ProcessCheckResult contains the result of process-level health check
type ProcessCheckResult struct {
	Running    bool   `json:"running"`
	Crashed    bool   `json:"crashed"`
	ExitStatus string `json:"exit_status,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// StallCheckResult contains the result of stall detection using activity detection
type StallCheckResult struct {
	Stalled       bool    `json:"stalled"`
	ActivityState string  `json:"activity_state"` // from StateClassifier
	Velocity      float64 `json:"velocity"`       // chars/sec
	IdleSeconds   int     `json:"idle_seconds"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason,omitempty"`
}

// ErrorCheckResult contains the result of error pattern detection
type ErrorCheckResult struct {
	HasErrors   bool     `json:"has_errors"`
	RateLimited bool     `json:"rate_limited"`
	Patterns    []string `json:"patterns,omitempty"`
	WaitSeconds int      `json:"wait_seconds,omitempty"` // suggested wait time for rate limit
	Reason      string   `json:"reason,omitempty"`
}

// Error patterns for detailed detection (literal string patterns for strings.Contains)
var healthErrorPatterns = []struct {
	Pattern string
	Type    string
}{
	// Rate limit patterns
	{"rate limit", "rate_limit"},
	{"ratelimit", "rate_limit"},
	{"rate-limit", "rate_limit"},
	{"429", "rate_limit"},
	{"too many requests", "rate_limit"},
	{"quota exceeded", "rate_limit"},
	// Auth error patterns
	{"authentication failed", "auth_error"},
	{"authentication error", "auth_error"},
	{"401", "auth_error"},
	{"unauthorized", "auth_error"},
	// Crash patterns
	{"panic:", "crash"},
	{"fatal error", "crash"},
	{"segmentation fault", "crash"},
	{"stack trace", "crash"},
	// Network error patterns
	{"connection refused", "network_error"},
	{"connection reset", "network_error"},
	{"connection timeout", "network_error"},
	{"network error", "network_error"},
	{"network unreachable", "network_error"},
}

// CheckAgentHealthWithActivity performs a comprehensive health check using activity detection
func CheckAgentHealthWithActivity(paneID string, agentType string) (*HealthCheck, error) {
	check := &HealthCheck{
		PaneID:      paneID,
		AgentType:   agentType,
		HealthState: HealthHealthy,
		Confidence:  1.0,
		CheckedAt:   time.Now().UTC(),
	}

	// 1. Process check - is the agent still running or crashed?
	check.ProcessCheck = checkProcess(paneID)

	// 2. Stall check - use activity detection for stall detection
	check.StallCheck = checkStallWithActivity(paneID, agentType)

	// 3. Error check - detect error patterns
	check.ErrorCheck = checkErrors(paneID)

	// Calculate overall health state
	check.HealthState, check.Reason = calculateHealthState(check)

	// Calculate confidence based on checks
	check.Confidence = calculateHealthConfidence(check)

	return check, nil
}

// checkProcess checks if the agent process is running or crashed
func checkProcess(paneID string) *ProcessCheckResult {
	result := &ProcessCheckResult{
		Running: true,
		Crashed: false,
	}

	// Capture pane output to check for exit indicators
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	output, err := tmux.CapturePaneOutputContext(ctx, paneID, 30)
	cancel()
	if err != nil {
		result.Reason = "failed to capture pane output"
		return result
	}

	output = stripANSI(output)
	outputLower := strings.ToLower(output)

	// Check for exit indicators
	exitPatterns := []string{
		"exit status", "exited with", "process exited",
		"connection closed", "session ended", "terminated",
	}

	for _, pattern := range exitPatterns {
		if strings.Contains(outputLower, pattern) {
			// Check if it's really a crash (shell prompt at end)
			lines := splitLines(output)
			if len(lines) > 0 {
				lastLine := strings.TrimSpace(lines[len(lines)-1])
				// If the last line looks like a shell prompt, agent may have crashed
				if lastLine == "$" || lastLine == "bash$" || lastLine == "zsh$" ||
					strings.HasSuffix(lastLine, "$") && !strings.Contains(lastLine, ">") {
					result.Running = false
					result.Crashed = true
					result.ExitStatus = pattern
					result.Reason = "detected shell prompt - agent may have crashed"
					return result
				}
			}
		}
	}

	// Check for explicit exit messages
	if strings.Contains(outputLower, "exited with code") || strings.Contains(outputLower, "exit code:") {
		result.Running = false
		result.Crashed = true
		result.Reason = "exit code detected"
		return result
	}

	// Check for bare shell prompt at end of output (agent crashed to shell)
	lines := splitLines(output)
	if len(lines) > 0 {
		lastLine := strings.TrimSpace(lines[len(lines)-1])
		// Detect common shell prompts: "$", "bash$", "zsh%", "user@host$", etc.
		// Be conservative - only match if line is very short (prompt-like)
		if len(lastLine) < 50 {
			if lastLine == "$" || lastLine == "%" ||
				strings.HasSuffix(lastLine, " $") || strings.HasSuffix(lastLine, " %") ||
				strings.HasSuffix(lastLine, "$ ") || strings.HasSuffix(lastLine, "% ") {
				result.Running = false
				result.Crashed = true
				result.ExitStatus = "shell prompt"
				result.Reason = "detected shell prompt - agent may have crashed"
			}
		}
	}

	return result
}

// checkStallWithActivity uses the StateClassifier for stall detection
func checkStallWithActivity(paneID string, agentType string) *StallCheckResult {
	result := &StallCheckResult{
		Stalled:    false,
		Confidence: 0.5,
	}

	// Create a classifier for this pane
	classifier := NewStateClassifier(paneID, &ClassifierConfig{
		AgentType:      agentType,
		StallThreshold: DefaultStallThreshold,
	})

	// Classify the current state
	activity, err := classifier.Classify()
	if err != nil {
		result.Reason = "failed to classify activity: " + err.Error()
		return result
	}

	// Extract activity state
	result.ActivityState = string(activity.State)
	result.Velocity = activity.Velocity
	result.Confidence = activity.Confidence

	// Calculate idle time from StateSince for idle-like states
	if !activity.StateSince.IsZero() {
		result.IdleSeconds = int(time.Since(activity.StateSince).Seconds())
	}

	// Check for stall conditions
	switch activity.State {
	case StateStalled:
		result.Stalled = true
		result.Reason = "agent stalled - no output for extended period"
	case StateError:
		result.Stalled = true
		result.Reason = "agent in error state"
	case StateUnknown:
		// Unknown might indicate a stall if velocity is 0
		if activity.Velocity == 0 && result.IdleSeconds > int(DefaultStallThreshold.Seconds()) {
			result.Stalled = true
			result.Reason = "unknown state with no output"
		}
	}

	return result
}

// checkErrors detects error patterns in pane output
func checkErrors(paneID string) *ErrorCheckResult {
	result := &ErrorCheckResult{
		HasErrors:   false,
		RateLimited: false,
		Patterns:    []string{},
	}

	// Capture pane output
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	output, err := tmux.CapturePaneOutputContext(ctx, paneID, 50)
	cancel()
	if err != nil {
		result.Reason = "failed to capture pane output"
		return result
	}

	output = stripANSI(output)
	outputLower := strings.ToLower(output)

	// Check for error patterns
	seenPatterns := make(map[string]bool)
	for _, ep := range healthErrorPatterns {
		if strings.Contains(outputLower, strings.ToLower(ep.Pattern)) {
			if !seenPatterns[ep.Type] {
				result.Patterns = append(result.Patterns, ep.Type)
				seenPatterns[ep.Type] = true

				if ep.Type == "rate_limit" {
					result.RateLimited = true
					result.WaitSeconds = parseRateLimitWait(output)
				}

				if ep.Type == "crash" || ep.Type == "auth_error" || ep.Type == "network_error" {
					result.HasErrors = true
				}
			}
		}
	}

	if result.RateLimited {
		result.HasErrors = true
		result.Reason = "rate limit detected"
	} else if len(result.Patterns) > 0 {
		result.Reason = fmt.Sprintf("detected: %v", result.Patterns)
	}

	return result
}

// parseRateLimitWait extracts wait time from rate limit messages
func parseRateLimitWait(output string) int {
	outputLower := strings.ToLower(output)

	// Look for common wait time indicators and extract the number
	// Patterns: "wait 60 seconds", "retry in 30s", "try again in 60s", "60 second cooldown"
	indicators := []string{
		"wait ", "retry in ", "retry after ",
		"try again in ", " second", " sec", "cooldown", "delay",
	}

	// Find any indicator and look for nearby numbers
	for _, ind := range indicators {
		idx := strings.Index(outputLower, ind)
		if idx < 0 {
			continue
		}

		// Search around the indicator for a number (before or after)
		searchStart := idx - 10
		if searchStart < 0 {
			searchStart = 0
		}
		searchEnd := idx + len(ind) + 10
		if searchEnd > len(outputLower) {
			searchEnd = len(outputLower)
		}

		region := outputLower[searchStart:searchEnd]

		// Extract numbers from the region
		var num int
		for i := 0; i < len(region); i++ {
			if region[i] >= '0' && region[i] <= '9' {
				fmt.Sscanf(region[i:], "%d", &num)
				if num > 0 && num <= 3600 { // Reasonable wait time (1 sec to 1 hour)
					return num
				}
				// Skip past this number
				for i < len(region) && region[i] >= '0' && region[i] <= '9' {
					i++
				}
			}
		}
	}
	return 0
}

// calculateHealthState determines the overall health state from all checks
func calculateHealthState(check *HealthCheck) (HealthState, string) {
	// Priority order: unhealthy > rate_limited > degraded > healthy

	// Check for crash (unhealthy)
	if check.ProcessCheck != nil && check.ProcessCheck.Crashed {
		return HealthUnhealthy, "agent crashed"
	}

	// Check for error state (unhealthy)
	if check.ErrorCheck != nil && check.ErrorCheck.HasErrors && !check.ErrorCheck.RateLimited {
		return HealthUnhealthy, "error detected: " + check.ErrorCheck.Reason
	}

	// Check for rate limit
	if check.ErrorCheck != nil && check.ErrorCheck.RateLimited {
		return HealthRateLimited, "rate limit detected"
	}

	// Check for stall (degraded)
	if check.StallCheck != nil && check.StallCheck.Stalled {
		return HealthDegraded, "agent stalled: " + check.StallCheck.Reason
	}

	// Check for low velocity (degraded)
	if check.StallCheck != nil && check.StallCheck.IdleSeconds > 300 { // 5 minutes
		return HealthDegraded, "agent idle for extended period"
	}

	return HealthHealthy, "all checks passed"
}

// calculateHealthConfidence determines confidence in the health assessment
func calculateHealthConfidence(check *HealthCheck) float64 {
	confidence := 1.0

	// Lower confidence if stall check has low confidence
	if check.StallCheck != nil && check.StallCheck.Confidence < 0.7 {
		confidence *= check.StallCheck.Confidence
	}

	// Lower confidence if we couldn't perform all checks
	if check.ProcessCheck == nil || check.StallCheck == nil || check.ErrorCheck == nil {
		confidence *= 0.8
	}

	return confidence
}

// =============================================================================
// Session Health API (--robot-health=SESSION)
// =============================================================================

// SessionHealthOutput is the response format for --robot-health=SESSION
type SessionHealthOutput struct {
	Success   bool                 `json:"success"`
	Session   string               `json:"session"`
	CheckedAt time.Time            `json:"checked_at"`
	Agents    []SessionAgentHealth `json:"agents"`
	Summary   SessionHealthSummary `json:"summary"`
	Error     string               `json:"error,omitempty"`
}

// SessionAgentHealth contains health metrics for a single agent pane
type SessionAgentHealth struct {
	Pane             int     `json:"pane"`
	AgentType        string  `json:"agent_type"`
	Health           string  `json:"health"`             // healthy, degraded, unhealthy, rate_limited
	IdleSinceSeconds int     `json:"idle_since_seconds"` // seconds since last pane activity
	Restarts         int     `json:"restarts"`
	LastError        string  `json:"last_error,omitempty"`
	RateLimitCount   int     `json:"rate_limit_count"`
	BackoffRemaining int     `json:"backoff_remaining"` // seconds until ready
	Confidence       float64 `json:"confidence"`
}

// SessionHealthSummary contains aggregate health counts
type SessionHealthSummary struct {
	Total       int `json:"total"`
	Healthy     int `json:"healthy"`
	Degraded    int `json:"degraded"`
	Unhealthy   int `json:"unhealthy"`
	RateLimited int `json:"rate_limited"`
}

// PrintSessionHealth outputs per-agent health for a specific session
func PrintSessionHealth(session string) error {
	output := SessionHealthOutput{
		Success:   true,
		Session:   session,
		CheckedAt: time.Now().UTC(),
		Agents:    []SessionAgentHealth{},
		Summary:   SessionHealthSummary{},
	}

	// Check if session exists
	if !tmux.SessionExists(session) {
		output.Success = false
		output.Error = fmt.Sprintf("session '%s' not found", session)
		return encodeJSON(output)
	}

	// Get panes in the session
	panes, err := tmux.GetPanes(session)
	if err != nil {
		output.Success = false
		output.Error = fmt.Sprintf("failed to get panes: %v", err)
		return encodeJSON(output)
	}

	// Check health for each pane
	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" || agentType == "unknown" {
			continue // Skip non-agent panes
		}

		agentHealth := SessionAgentHealth{
			Pane:      pane.Index,
			AgentType: agentType,
			Health:    "healthy",
		}

		// Get activity time - how long since last pane activity
		activityTime, err := tmux.GetPaneActivity(pane.ID)
		if err == nil {
			agentHealth.IdleSinceSeconds = int(time.Since(activityTime).Seconds())
		}

		// Perform comprehensive health check
		check, err := CheckAgentHealthWithActivity(pane.ID, agentType)
		if err == nil {
			agentHealth.Confidence = check.Confidence

			// Map health state to string
			switch check.HealthState {
			case HealthHealthy:
				agentHealth.Health = "healthy"
			case HealthDegraded:
				agentHealth.Health = "degraded"
			case HealthUnhealthy:
				agentHealth.Health = "unhealthy"
				agentHealth.LastError = check.Reason
			case HealthRateLimited:
				agentHealth.Health = "rate_limited"
				agentHealth.RateLimitCount = 1
				if check.ErrorCheck != nil && check.ErrorCheck.WaitSeconds > 0 {
					agentHealth.BackoffRemaining = check.ErrorCheck.WaitSeconds
				}
			}

			// Track errors
			if check.ProcessCheck != nil && check.ProcessCheck.Crashed {
				agentHealth.Restarts++ // Track as a restart indicator
				agentHealth.LastError = check.ProcessCheck.Reason
			}
		}

		output.Agents = append(output.Agents, agentHealth)

		// Update summary
		output.Summary.Total++
		switch agentHealth.Health {
		case "healthy":
			output.Summary.Healthy++
		case "degraded":
			output.Summary.Degraded++
		case "unhealthy":
			output.Summary.Unhealthy++
		case "rate_limited":
			output.Summary.RateLimited++
		}
	}

	return encodeJSON(output)
}

// detectAgentTypeFromPane determines the agent type from pane information
func detectAgentTypeFromPane(pane tmux.Pane) string {
	switch pane.Type {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

// =============================================================================
// Health State Tracking (ntm-v5if)
// =============================================================================
//
// HealthTracker maintains health state history for agents over time.
// It tracks state transitions, restarts, errors, and rate limits in memory.

// HealthStateTransition records a state change
type HealthStateTransition struct {
	From      HealthState `json:"from"`
	To        HealthState `json:"to"`
	Reason    string      `json:"reason"`
	Timestamp time.Time   `json:"timestamp"`
}

// AgentErrorInfo captures details about an error
type AgentErrorInfo struct {
	Type      string    `json:"type"` // e.g., "rate_limit", "crash", "auth_error"
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// AgentHealthMetrics holds all tracked metrics for a single agent
type AgentHealthMetrics struct {
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"`
	SessionID string `json:"session_id"`

	// Current state
	CurrentState HealthState `json:"current_state"`
	StateReason  string      `json:"state_reason"`

	// State transition history (most recent first)
	Transitions []HealthStateTransition `json:"transitions"`

	// Uptime tracking
	StartTime       time.Time `json:"start_time"`        // When agent was first tracked
	LastRestartTime time.Time `json:"last_restart_time"` // Time of last restart
	TotalRestarts   int       `json:"total_restarts"`    // Total restarts ever

	// Restart tracking per window
	RestartTimestamps []time.Time `json:"-"` // Internal: for counting restarts in windows

	// Error tracking
	LastError   *AgentErrorInfo `json:"last_error,omitempty"`
	TotalErrors int             `json:"total_errors"`

	// Rate limit tracking
	RateLimitCount      int       `json:"rate_limit_count"`       // Total rate limits hit
	RateLimitWindowHits int       `json:"rate_limit_window_hits"` // Hits in current window
	RateLimitWindowEnd  time.Time `json:"rate_limit_window_end"`  // When window expires

	// Consecutive failure tracking
	ConsecutiveFailures int `json:"consecutive_failures"`

	// Last check time
	LastCheckTime time.Time `json:"last_check_time"`
}

// HealthTracker manages health state for all agents in a session
type HealthTracker struct {
	mu      sync.RWMutex
	agents  map[string]*AgentHealthMetrics // keyed by pane ID
	session string

	// Configuration
	maxTransitions  int           // Max state transitions to keep in history
	restartWindow   time.Duration // Window for counting restarts (e.g., 1 hour)
	rateLimitWindow time.Duration // Window for counting rate limits
}

// HealthTrackerConfig holds configuration for HealthTracker
type HealthTrackerConfig struct {
	MaxTransitions  int           // Default: 50
	RestartWindow   time.Duration // Default: 1 hour
	RateLimitWindow time.Duration // Default: 1 hour
}

// DefaultHealthTrackerConfig returns sensible defaults
func DefaultHealthTrackerConfig() HealthTrackerConfig {
	return HealthTrackerConfig{
		MaxTransitions:  50,
		RestartWindow:   time.Hour,
		RateLimitWindow: time.Hour,
	}
}

// NewHealthTracker creates a new health tracker for a session
func NewHealthTracker(session string, config *HealthTrackerConfig) *HealthTracker {
	cfg := DefaultHealthTrackerConfig()
	if config != nil {
		if config.MaxTransitions > 0 {
			cfg.MaxTransitions = config.MaxTransitions
		}
		if config.RestartWindow > 0 {
			cfg.RestartWindow = config.RestartWindow
		}
		if config.RateLimitWindow > 0 {
			cfg.RateLimitWindow = config.RateLimitWindow
		}
	}

	return &HealthTracker{
		agents:          make(map[string]*AgentHealthMetrics),
		session:         session,
		maxTransitions:  cfg.MaxTransitions,
		restartWindow:   cfg.RestartWindow,
		rateLimitWindow: cfg.RateLimitWindow,
	}
}

// RecordState records a health state for an agent
func (ht *HealthTracker) RecordState(paneID string, agentType string, state HealthState, reason string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	now := time.Now()
	metrics := ht.getOrCreateMetrics(paneID, agentType)

	// Record state transition if changed
	if metrics.CurrentState != state {
		transition := HealthStateTransition{
			From:      metrics.CurrentState,
			To:        state,
			Reason:    reason,
			Timestamp: now,
		}

		// Prepend to transitions (most recent first)
		metrics.Transitions = append([]HealthStateTransition{transition}, metrics.Transitions...)

		// Trim to max size
		if len(metrics.Transitions) > ht.maxTransitions {
			metrics.Transitions = metrics.Transitions[:ht.maxTransitions]
		}

		// Reset consecutive failures on healthy transition
		if state == HealthHealthy {
			metrics.ConsecutiveFailures = 0
		}
	}

	// Update current state
	metrics.CurrentState = state
	metrics.StateReason = reason
	metrics.LastCheckTime = now

	// Track failures
	if state == HealthUnhealthy || state == HealthRateLimited {
		metrics.ConsecutiveFailures++
	}

	// Track rate limits specifically
	if state == HealthRateLimited {
		ht.recordRateLimitInternal(metrics, now)
	}
}

// RecordError records an error for an agent
func (ht *HealthTracker) RecordError(paneID string, errType string, message string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return // Agent not yet tracked
	}

	metrics.LastError = &AgentErrorInfo{
		Type:      errType,
		Message:   message,
		Timestamp: time.Now(),
	}
	metrics.TotalErrors++
}

// RecordRestart records a restart for an agent
func (ht *HealthTracker) RecordRestart(paneID string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	now := time.Now()
	metrics, ok := ht.agents[paneID]
	if !ok {
		return // Agent not yet tracked
	}

	metrics.TotalRestarts++
	metrics.LastRestartTime = now
	metrics.RestartTimestamps = append(metrics.RestartTimestamps, now)

	// Clean old timestamps outside the window
	metrics.RestartTimestamps = ht.filterTimestampsInWindow(metrics.RestartTimestamps, ht.restartWindow)
}

// recordRateLimitInternal records a rate limit hit (must hold lock)
func (ht *HealthTracker) recordRateLimitInternal(metrics *AgentHealthMetrics, now time.Time) {
	metrics.RateLimitCount++

	// Check if we're in a tracking window
	if now.After(metrics.RateLimitWindowEnd) {
		// Start new window
		metrics.RateLimitWindowHits = 1
		metrics.RateLimitWindowEnd = now.Add(ht.rateLimitWindow)
	} else {
		// Increment in current window
		metrics.RateLimitWindowHits++
	}
}

// GetHealth returns current health metrics for an agent
func (ht *HealthTracker) GetHealth(paneID string) (*AgentHealthMetrics, bool) {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return nil, false
	}

	// Return a copy to avoid race conditions
	metricsCopy := *metrics
	metricsCopy.Transitions = append([]HealthStateTransition(nil), metrics.Transitions...)
	metricsCopy.RestartTimestamps = append([]time.Time(nil), metrics.RestartTimestamps...)
	if metrics.LastError != nil {
		errCopy := *metrics.LastError
		metricsCopy.LastError = &errCopy
	}
	return &metricsCopy, true
}

// GetAllHealth returns health metrics for all tracked agents
func (ht *HealthTracker) GetAllHealth() map[string]*AgentHealthMetrics {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	result := make(map[string]*AgentHealthMetrics, len(ht.agents))
	for k, v := range ht.agents {
		metricsCopy := *v
		metricsCopy.Transitions = append([]HealthStateTransition(nil), v.Transitions...)
		metricsCopy.RestartTimestamps = append([]time.Time(nil), v.RestartTimestamps...)
		if v.LastError != nil {
			errCopy := *v.LastError
			metricsCopy.LastError = &errCopy
		}
		result[k] = &metricsCopy
	}
	return result
}

// GetUptime returns the uptime since last restart for an agent
func (ht *HealthTracker) GetUptime(paneID string) time.Duration {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return 0
	}

	// If never restarted, uptime is since start
	if metrics.LastRestartTime.IsZero() {
		return time.Since(metrics.StartTime)
	}
	return time.Since(metrics.LastRestartTime)
}

// GetRestartsInWindow returns the number of restarts in the configured window
func (ht *HealthTracker) GetRestartsInWindow(paneID string) int {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return 0
	}

	// Filter to timestamps in window
	filtered := ht.filterTimestampsInWindow(metrics.RestartTimestamps, ht.restartWindow)
	return len(filtered)
}

// GetRateLimitsInWindow returns rate limit hits in current window
func (ht *HealthTracker) GetRateLimitsInWindow(paneID string) int {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return 0
	}

	// Check if window has expired
	if time.Now().After(metrics.RateLimitWindowEnd) {
		return 0
	}
	return metrics.RateLimitWindowHits
}

// GetTransitionHistory returns state transition history for an agent
func (ht *HealthTracker) GetTransitionHistory(paneID string, limit int) []HealthStateTransition {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	metrics, ok := ht.agents[paneID]
	if !ok {
		return nil
	}

	if limit <= 0 || limit > len(metrics.Transitions) {
		limit = len(metrics.Transitions)
	}

	result := make([]HealthStateTransition, limit)
	copy(result, metrics.Transitions[:limit])
	return result
}

// ClearAgent removes tracking for an agent (e.g., when pane is destroyed)
func (ht *HealthTracker) ClearAgent(paneID string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	delete(ht.agents, paneID)
}

// Reset clears all tracked state
func (ht *HealthTracker) Reset() {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.agents = make(map[string]*AgentHealthMetrics)
}

// getOrCreateMetrics gets or creates metrics for an agent (must hold lock)
func (ht *HealthTracker) getOrCreateMetrics(paneID string, agentType string) *AgentHealthMetrics {
	metrics, ok := ht.agents[paneID]
	if !ok {
		now := time.Now()
		metrics = &AgentHealthMetrics{
			PaneID:       paneID,
			AgentType:    agentType,
			SessionID:    ht.session,
			CurrentState: HealthHealthy,
			StartTime:    now,
			Transitions:  []HealthStateTransition{},
		}
		ht.agents[paneID] = metrics
	}
	return metrics
}

// filterTimestampsInWindow filters timestamps to those within the window
func (ht *HealthTracker) filterTimestampsInWindow(timestamps []time.Time, window time.Duration) []time.Time {
	cutoff := time.Now().Add(-window)
	result := make([]time.Time, 0, len(timestamps))
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			result = append(result, ts)
		}
	}
	return result
}

// =============================================================================
// Rate Limit Backoff (ntm-1ir2)
// =============================================================================
//
// Implements exponential backoff for rate-limited agents:
// - Base: 30 seconds
// - Multiplier: 2^n (where n = consecutive rate limits)
// - Max: 5 minutes
// - Clears after backoff expires and agent is healthy

const (
	// BackoffBase is the initial backoff duration
	BackoffBase = 30 * time.Second
	// BackoffMax is the maximum backoff duration
	BackoffMax = 5 * time.Minute
	// BackoffMultiplier is the exponential multiplier
	BackoffMultiplier = 2
)

// RateLimitBackoff tracks backoff state for a single agent
type RateLimitBackoff struct {
	PaneID          string    `json:"pane_id"`
	BackoffCount    int       `json:"backoff_count"` // n in 30s * 2^n
	BackoffEndsAt   time.Time `json:"backoff_ends_at"`
	LastRateLimitAt time.Time `json:"last_rate_limit_at"`
	TotalRateLimits int       `json:"total_rate_limits"` // lifetime count
}

// BackoffManager manages rate limit backoffs for all agents in a session
type BackoffManager struct {
	mu       sync.RWMutex
	backoffs map[string]*RateLimitBackoff // keyed by pane ID
	session  string
}

// NewBackoffManager creates a new backoff manager for a session
func NewBackoffManager(session string) *BackoffManager {
	return &BackoffManager{
		backoffs: make(map[string]*RateLimitBackoff),
		session:  session,
	}
}

// RecordRateLimit records a rate limit hit and starts/extends backoff
func (bm *BackoffManager) RecordRateLimit(paneID string) time.Duration {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	now := time.Now()
	backoff, exists := bm.backoffs[paneID]

	if !exists {
		backoff = &RateLimitBackoff{
			PaneID:          paneID,
			BackoffCount:    0,
			TotalRateLimits: 0,
		}
		bm.backoffs[paneID] = backoff
	}

	// If already in backoff, increment the count (escalate)
	if now.Before(backoff.BackoffEndsAt) {
		backoff.BackoffCount++
	} else {
		// First rate limit or backoff expired - start fresh
		backoff.BackoffCount = 0
	}

	backoff.LastRateLimitAt = now
	backoff.TotalRateLimits++

	// Calculate backoff duration: 30s * 2^n, capped at 5 minutes
	duration := calculateBackoffDuration(backoff.BackoffCount)
	backoff.BackoffEndsAt = now.Add(duration)

	return duration
}

// calculateBackoffDuration calculates the backoff duration for a given count
func calculateBackoffDuration(count int) time.Duration {
	// 30s * 2^n
	duration := BackoffBase
	for i := 0; i < count; i++ {
		duration *= BackoffMultiplier
		if duration > BackoffMax {
			return BackoffMax
		}
	}
	return duration
}

// IsInBackoff returns true if the agent is currently in backoff
func (bm *BackoffManager) IsInBackoff(paneID string) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	backoff, exists := bm.backoffs[paneID]
	if !exists {
		return false
	}

	return time.Now().Before(backoff.BackoffEndsAt)
}

// GetBackoffRemaining returns the time remaining in backoff (0 if not in backoff)
func (bm *BackoffManager) GetBackoffRemaining(paneID string) time.Duration {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	backoff, exists := bm.backoffs[paneID]
	if !exists {
		return 0
	}

	remaining := time.Until(backoff.BackoffEndsAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetBackoff returns the backoff state for an agent (nil if none)
func (bm *BackoffManager) GetBackoff(paneID string) *RateLimitBackoff {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	backoff, exists := bm.backoffs[paneID]
	if !exists {
		return nil
	}

	// Return a copy
	backoffCopy := *backoff
	return &backoffCopy
}

// ClearBackoff clears the backoff state for an agent (e.g., after recovery)
func (bm *BackoffManager) ClearBackoff(paneID string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	delete(bm.backoffs, paneID)
}

// ClearExpiredBackoffs removes all expired backoffs
func (bm *BackoffManager) ClearExpiredBackoffs() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	now := time.Now()
	cleared := 0

	for paneID, backoff := range bm.backoffs {
		if now.After(backoff.BackoffEndsAt) {
			delete(bm.backoffs, paneID)
			cleared++
		}
	}

	return cleared
}

// GetAllBackoffs returns all active backoffs
func (bm *BackoffManager) GetAllBackoffs() map[string]*RateLimitBackoff {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	now := time.Now()
	result := make(map[string]*RateLimitBackoff)

	for paneID, backoff := range bm.backoffs {
		if now.Before(backoff.BackoffEndsAt) {
			backoffCopy := *backoff
			result[paneID] = &backoffCopy
		}
	}

	return result
}

// GetRateLimitCount returns the total rate limit count for an agent
func (bm *BackoffManager) GetRateLimitCount(paneID string) int {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	backoff, exists := bm.backoffs[paneID]
	if !exists {
		return 0
	}

	return backoff.TotalRateLimits
}

// =============================================================================
// Global Backoff Manager Registry
// =============================================================================

var (
	backoffManagerRegistry   = make(map[string]*BackoffManager)
	backoffManagerRegistryMu sync.RWMutex
)

// GetBackoffManager returns the backoff manager for a session, creating if needed
func GetBackoffManager(session string) *BackoffManager {
	backoffManagerRegistryMu.RLock()
	manager, ok := backoffManagerRegistry[session]
	backoffManagerRegistryMu.RUnlock()

	if ok {
		return manager
	}

	backoffManagerRegistryMu.Lock()
	defer backoffManagerRegistryMu.Unlock()

	// Double-check after acquiring write lock
	if manager, ok = backoffManagerRegistry[session]; ok {
		return manager
	}

	manager = NewBackoffManager(session)
	backoffManagerRegistry[session] = manager
	return manager
}

// ClearBackoffManager removes the backoff manager for a session
func ClearBackoffManager(session string) {
	backoffManagerRegistryMu.Lock()
	defer backoffManagerRegistryMu.Unlock()
	delete(backoffManagerRegistry, session)
}

// =============================================================================
// Integration: Check Rate Limit Before Send
// =============================================================================

// CheckSendAllowed checks if sending to an agent is allowed (not in backoff)
// Returns: allowed, backoffRemaining, rateLimitCount
func CheckSendAllowed(session, paneID string) (bool, time.Duration, int) {
	manager := GetBackoffManager(session)

	remaining := manager.GetBackoffRemaining(paneID)
	count := manager.GetRateLimitCount(paneID)

	return remaining == 0, remaining, count
}

// RecordAgentRateLimit records a rate limit for an agent and returns backoff duration
func RecordAgentRateLimit(session, paneID string) time.Duration {
	manager := GetBackoffManager(session)
	return manager.RecordRateLimit(paneID)
}

// ClearAgentBackoff clears backoff state for an agent after recovery
func ClearAgentBackoff(session, paneID string) {
	manager := GetBackoffManager(session)
	manager.ClearBackoff(paneID)
}

// =============================================================================
// Global Health Tracker Registry
// =============================================================================
//
// For convenience, we maintain a registry of trackers per session

var (
	healthTrackerRegistry   = make(map[string]*HealthTracker)
	healthTrackerRegistryMu sync.RWMutex
)

// GetHealthTracker returns the health tracker for a session, creating if needed
func GetHealthTracker(session string) *HealthTracker {
	healthTrackerRegistryMu.RLock()
	tracker, ok := healthTrackerRegistry[session]
	healthTrackerRegistryMu.RUnlock()

	if ok {
		return tracker
	}

	healthTrackerRegistryMu.Lock()
	defer healthTrackerRegistryMu.Unlock()

	// Double-check after acquiring write lock
	if tracker, ok = healthTrackerRegistry[session]; ok {
		return tracker
	}

	tracker = NewHealthTracker(session, nil)
	healthTrackerRegistry[session] = tracker
	return tracker
}

// ClearHealthTracker removes the tracker for a session
func ClearHealthTracker(session string) {
	healthTrackerRegistryMu.Lock()
	defer healthTrackerRegistryMu.Unlock()
	delete(healthTrackerRegistry, session)
}

// =============================================================================
// Automatic Restart Manager (ntm-ebvm)
// =============================================================================
//
// Implements automatic restart for unhealthy agents with:
// - Soft restart: Ctrl+C interrupt, wait for idle prompt
// - Hard restart: Aggressive restart with agent re-launch
// - Exponential backoff between restarts
// - Max restart limit per hour
// - Context loss notification

// RestartConfig configures the restart behavior
type RestartConfig struct {
	Enabled            bool          `toml:"enabled"`
	MaxRestartsPerHour int           `toml:"max_restarts"`         // Max restarts per hour (default: 3)
	BackoffBase        time.Duration `toml:"backoff_base"`         // Base backoff (default: 30s)
	BackoffMax         time.Duration `toml:"backoff_max"`          // Max backoff (default: 5m)
	SoftRestartTimeout time.Duration `toml:"soft_restart_timeout"` // Timeout for soft restart (default: 10s)
	NotifyContextLoss  bool          `toml:"notify_on_context_loss"`
}

// DefaultRestartConfig returns sensible defaults
func DefaultRestartConfig() RestartConfig {
	return RestartConfig{
		Enabled:            true,
		MaxRestartsPerHour: 3,
		BackoffBase:        30 * time.Second,
		BackoffMax:         5 * time.Minute,
		SoftRestartTimeout: 10 * time.Second,
		NotifyContextLoss:  true,
	}
}

// RestartType indicates the type of restart performed
type RestartType string

const (
	RestartSoft RestartType = "soft"
	RestartHard RestartType = "hard"
	RestartNone RestartType = "none"
)

// RestartResult contains the result of a restart attempt
type RestartResult struct {
	Success        bool          `json:"success"`
	Type           RestartType   `json:"type"`
	PaneID         string        `json:"pane_id"`
	AgentType      string        `json:"agent_type"`
	BackoffApplied time.Duration `json:"backoff_applied"`
	ContextLost    bool          `json:"context_lost"`
	Reason         string        `json:"reason"`
	AttemptedAt    time.Time     `json:"attempted_at"`
}

// RestartManager manages automatic restarts for agents
type RestartManager struct {
	mu           sync.Mutex
	session      string
	config       RestartConfig
	restartTimes map[string][]time.Time // pane ID -> restart timestamps
	alerter      AlerterInterface       // For sending alerts
}

// AlerterInterface defines the alerting capability needed by RestartManager
type AlerterInterface interface {
	Send(ctx context.Context, alert *Alert) error
}

// NewRestartManager creates a new restart manager
func NewRestartManager(session string, config *RestartConfig, alerter AlerterInterface) *RestartManager {
	cfg := DefaultRestartConfig()
	if config != nil {
		if config.MaxRestartsPerHour > 0 {
			cfg.MaxRestartsPerHour = config.MaxRestartsPerHour
		}
		if config.BackoffBase > 0 {
			cfg.BackoffBase = config.BackoffBase
		}
		if config.BackoffMax > 0 {
			cfg.BackoffMax = config.BackoffMax
		}
		if config.SoftRestartTimeout > 0 {
			cfg.SoftRestartTimeout = config.SoftRestartTimeout
		}
		cfg.Enabled = config.Enabled
		cfg.NotifyContextLoss = config.NotifyContextLoss
	}

	return &RestartManager{
		session:      session,
		config:       cfg,
		restartTimes: make(map[string][]time.Time),
		alerter:      alerter,
	}
}

// getRestartsInLastHour returns the number of restarts in the last hour
func (rm *RestartManager) getRestartsInLastHour(paneID string) int {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	cutoff := time.Now().Add(-time.Hour)
	timestamps, ok := rm.restartTimes[paneID]
	if !ok {
		return 0
	}

	// Filter to last hour
	var recent []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			recent = append(recent, ts)
		}
	}
	rm.restartTimes[paneID] = recent
	return len(recent)
}

// recordRestart records a restart attempt
func (rm *RestartManager) recordRestart(paneID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.restartTimes[paneID] = append(rm.restartTimes[paneID], time.Now())
}

// calculateBackoff calculates the backoff duration based on recent restarts
func (rm *RestartManager) calculateBackoff(restartCount int) time.Duration {
	if restartCount == 0 {
		return 0
	}

	// Exponential backoff: base * 2^(restarts-1)
	backoff := rm.config.BackoffBase
	for i := 1; i < restartCount; i++ {
		backoff *= 2
		if backoff > rm.config.BackoffMax {
			return rm.config.BackoffMax
		}
	}
	return backoff
}

// TryRestart attempts to restart an unhealthy agent
// Returns the result of the restart attempt
func (rm *RestartManager) TryRestart(ctx context.Context, paneID, agentType string, healthState HealthState) *RestartResult {
	result := &RestartResult{
		PaneID:      paneID,
		AgentType:   agentType,
		AttemptedAt: time.Now(),
	}

	// Check if restarts are enabled
	if !rm.config.Enabled {
		result.Type = RestartNone
		result.Reason = "restarts disabled"
		return result
	}

	// Check max restarts limit
	restartsInHour := rm.getRestartsInLastHour(paneID)
	if restartsInHour >= rm.config.MaxRestartsPerHour {
		result.Type = RestartNone
		result.Reason = fmt.Sprintf("max restarts exceeded (%d/%d per hour)", restartsInHour, rm.config.MaxRestartsPerHour)

		// Send alert about max restarts
		if rm.alerter != nil {
			rm.alerter.Send(ctx, &Alert{
				Type:      AlertMaxRestarts,
				Session:   rm.session,
				PaneID:    paneID,
				AgentType: agentType,
				Message:   fmt.Sprintf("Max restarts exceeded for agent %s (%d restarts in last hour)", agentType, restartsInHour),
				Timestamp: time.Now(),
			})
		}
		return result
	}

	// Calculate and apply backoff
	backoff := rm.calculateBackoff(restartsInHour)
	result.BackoffApplied = backoff
	if backoff > 0 {
		select {
		case <-ctx.Done():
			result.Type = RestartNone
			result.Reason = "context cancelled during backoff"
			return result
		case <-time.After(backoff):
			// Backoff complete
		}
	}

	// Try soft restart first
	softResult := rm.trySoftRestart(ctx, paneID, agentType)
	if softResult.Success {
		rm.recordRestart(paneID)
		return softResult
	}

	// Fall back to hard restart
	hardResult := rm.tryHardRestart(ctx, paneID, agentType)
	if hardResult.Success {
		rm.recordRestart(paneID)

		// Notify about context loss
		if rm.config.NotifyContextLoss && rm.alerter != nil {
			rm.alerter.Send(ctx, &Alert{
				Type:        AlertRestart,
				Session:     rm.session,
				PaneID:      paneID,
				AgentType:   agentType,
				Message:     fmt.Sprintf("Agent %s restarted (hard), context lost", agentType),
				ContextLoss: true,
				Timestamp:   time.Now(),
			})
		}

		// Also record in health tracker
		tracker := GetHealthTracker(rm.session)
		tracker.RecordRestart(paneID)

		return hardResult
	}

	// Both failed
	result.Type = RestartNone
	result.Reason = "both soft and hard restart failed"

	// Alert about failed restart
	if rm.alerter != nil {
		rm.alerter.Send(ctx, &Alert{
			Type:      AlertRestartFailed,
			Session:   rm.session,
			PaneID:    paneID,
			AgentType: agentType,
			Message:   fmt.Sprintf("Failed to restart agent %s after soft and hard restart attempts", agentType),
			Timestamp: time.Now(),
		})
	}

	return result
}

// trySoftRestart attempts a soft restart (Ctrl+C interrupt)
func (rm *RestartManager) trySoftRestart(ctx context.Context, paneID, agentType string) *RestartResult {
	result := &RestartResult{
		PaneID:      paneID,
		AgentType:   agentType,
		Type:        RestartSoft,
		AttemptedAt: time.Now(),
	}

	// Send Ctrl+C (target format: session:pane)
	target := fmt.Sprintf("%s:%s", rm.session, paneID)
	if err := tmux.SendInterrupt(target); err != nil {
		result.Reason = fmt.Sprintf("failed to send Ctrl+C: %v", err)
		return result
	}

	// Wait for idle prompt with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, rm.config.SoftRestartTimeout)
	defer cancel()

	// Poll for idle state
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			result.Reason = "timeout waiting for idle prompt"
			return result
		case <-ticker.C:
			// Check if agent is now idle
			output, err := tmux.CapturePaneOutputContext(timeoutCtx, target, 5)
			if err != nil {
				continue
			}

			if isAgentIdlePrompt(output, agentType) {
				result.Success = true
				result.Reason = "soft restart successful"
				return result
			}
		}
	}
}

// tryHardRestart attempts a hard restart (aggressive restart with re-launch)
func (rm *RestartManager) tryHardRestart(ctx context.Context, paneID, agentType string) *RestartResult {
	result := &RestartResult{
		PaneID:      paneID,
		AgentType:   agentType,
		Type:        RestartHard,
		ContextLost: true,
		AttemptedAt: time.Now(),
	}

	// Send Ctrl+C multiple times with delay
	target := fmt.Sprintf("%s:%s", rm.session, paneID)
	for i := 0; i < 3; i++ {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			result.Reason = "context cancelled during hard restart"
			return result
		default:
		}

		if err := tmux.SendInterrupt(target); err != nil {
			continue
		}

		// Sleep with context awareness
		select {
		case <-ctx.Done():
			result.Reason = "context cancelled during hard restart"
			return result
		case <-time.After(time.Second):
		}
	}

	// Check if we got a shell prompt (with context check)
	select {
	case <-ctx.Done():
		result.Reason = "context cancelled during hard restart"
		return result
	case <-time.After(500 * time.Millisecond):
	}
	output, _ := tmux.CapturePaneOutputContext(ctx, target, 5)

	// If still not at prompt, try Ctrl+D
	if !isShellPrompt(output) {
		tmux.SendEOF(target)
		select {
		case <-ctx.Done():
			result.Reason = "context cancelled during hard restart"
			return result
		case <-time.After(time.Second):
		}
	}

	// Re-launch the agent
	agentCmd := getAgentCommand(agentType)
	if agentCmd == "" {
		result.Reason = fmt.Sprintf("unknown agent type: %s", agentType)
		return result
	}

	// Send the agent command
	if err := tmux.SendKeys(target, agentCmd, true); err != nil {
		result.Reason = fmt.Sprintf("failed to launch agent: %v", err)
		return result
	}

	// Wait for agent to start (with context check)
	select {
	case <-ctx.Done():
		result.Reason = "context cancelled during hard restart"
		return result
	case <-time.After(2 * time.Second):
	}

	// Verify agent started
	output, captureErr := tmux.CapturePaneOutputContext(ctx, target, 10)
	if captureErr != nil {
		result.Reason = fmt.Sprintf("failed to capture output: %v", captureErr)
		return result
	}

	if isAgentRunning(output, agentType) {
		result.Success = true
		result.Reason = "hard restart successful"
		return result
	}

	result.Reason = "agent did not start after relaunch"
	return result
}

// isAgentIdlePrompt checks if the output indicates an idle agent prompt
func isAgentIdlePrompt(output, agentType string) bool {
	output = strings.TrimSpace(output)
	lines := splitLines(output)
	if len(lines) == 0 {
		return false
	}
	lastLine := strings.TrimSpace(lines[len(lines)-1])

	// Agent-specific idle patterns
	switch agentType {
	case "claude", "cc":
		// Claude Code shows ">" prompt when idle
		return strings.HasSuffix(lastLine, ">") || strings.Contains(output, "What would you like to do?")
	case "codex", "cod":
		// Codex shows ">" or "$" when idle
		return strings.HasSuffix(lastLine, ">") || strings.HasSuffix(lastLine, "$")
	case "gemini", "gmi":
		// Gemini shows ">" when idle
		return strings.HasSuffix(lastLine, ">")
	default:
		// Generic check for common prompt patterns
		return strings.HasSuffix(lastLine, ">") || strings.HasSuffix(lastLine, "$") || strings.HasSuffix(lastLine, "%")
	}
}

// isShellPrompt checks if the output shows a shell prompt
func isShellPrompt(output string) bool {
	output = strings.TrimSpace(output)
	lines := splitLines(output)
	if len(lines) == 0 {
		return false
	}
	lastLine := strings.TrimSpace(lines[len(lines)-1])

	// Short line ending with shell prompt characters
	return len(lastLine) < 100 && (strings.HasSuffix(lastLine, "$") ||
		strings.HasSuffix(lastLine, "%") ||
		strings.HasSuffix(lastLine, "#"))
}

// isAgentRunning checks if the agent appears to be running
func isAgentRunning(output, agentType string) bool {
	output = strings.TrimSpace(output)
	if len(output) == 0 {
		return false
	}

	outputLower := strings.ToLower(output)
	lines := splitLines(output)
	lastLine := ""
	if len(lines) > 0 {
		lastLine = strings.TrimSpace(lines[len(lines)-1])
	}

	// Check for startup indicators - be more specific than just ">"
	switch agentType {
	case "claude", "cc":
		// Claude shows "claude" in startup or idle prompt ending with ">"
		return strings.Contains(outputLower, "claude") ||
			strings.Contains(outputLower, "what would you like") ||
			(len(lastLine) < 50 && strings.HasSuffix(lastLine, ">"))
	case "codex", "cod":
		// Codex shows "codex" in startup or prompt
		return strings.Contains(outputLower, "codex") ||
			(len(lastLine) < 50 && strings.HasSuffix(lastLine, ">"))
	case "gemini", "gmi":
		// Gemini shows "gemini" in startup or prompt
		return strings.Contains(outputLower, "gemini") ||
			(len(lastLine) < 50 && strings.HasSuffix(lastLine, ">"))
	default:
		// For unknown types, require a short prompt-like line
		return len(lastLine) < 50 && (strings.HasSuffix(lastLine, ">") ||
			strings.HasSuffix(lastLine, "$") ||
			strings.HasSuffix(lastLine, "%"))
	}
}

// getAgentCommand returns the command to launch an agent
func getAgentCommand(agentType string) string {
	switch agentType {
	case "claude", "cc":
		return "claude"
	case "codex", "cod":
		return "codex"
	case "gemini", "gmi":
		return "gemini"
	default:
		return ""
	}
}

// =============================================================================
// Global Restart Manager Registry
// =============================================================================

var (
	restartManagerRegistry   = make(map[string]*RestartManager)
	restartManagerRegistryMu sync.RWMutex
)

// GetRestartManager returns the restart manager for a session.
// If a manager already exists and alerter is non-nil, the alerter is updated.
func GetRestartManager(session string, alerter AlerterInterface) *RestartManager {
	restartManagerRegistryMu.RLock()
	manager, ok := restartManagerRegistry[session]
	restartManagerRegistryMu.RUnlock()

	if ok {
		// Update alerter if provided (allows late binding)
		if alerter != nil {
			manager.mu.Lock()
			manager.alerter = alerter
			manager.mu.Unlock()
		}
		return manager
	}

	restartManagerRegistryMu.Lock()
	defer restartManagerRegistryMu.Unlock()

	// Double-check
	if manager, ok = restartManagerRegistry[session]; ok {
		// Update alerter if provided
		if alerter != nil {
			manager.mu.Lock()
			manager.alerter = alerter
			manager.mu.Unlock()
		}
		return manager
	}

	manager = NewRestartManager(session, nil, alerter)
	restartManagerRegistry[session] = manager
	return manager
}

// ClearRestartManager removes the restart manager for a session
func ClearRestartManager(session string) {
	restartManagerRegistryMu.Lock()
	defer restartManagerRegistryMu.Unlock()
	delete(restartManagerRegistry, session)
}

// =============================================================================
// Integration: Auto-Restart Unhealthy Agents
// =============================================================================

// AutoRestartUnhealthyAgent checks agent health and restarts if needed
func AutoRestartUnhealthyAgent(ctx context.Context, session, paneID, agentType string, alerter AlerterInterface) *RestartResult {
	// Check current health (paneID can be just the pane or full session:pane target)
	check, err := CheckAgentHealthWithActivity(paneID, agentType)
	if err != nil {
		return &RestartResult{
			PaneID:    paneID,
			AgentType: agentType,
			Type:      RestartNone,
			Reason:    fmt.Sprintf("health check failed: %v", err),
		}
	}

	// Only restart if unhealthy
	if check.HealthState != HealthUnhealthy && check.HealthState != HealthRateLimited {
		return &RestartResult{
			PaneID:    paneID,
			AgentType: agentType,
			Type:      RestartNone,
			Reason:    fmt.Sprintf("agent is %s, no restart needed", check.HealthState),
		}
	}

	// Get restart manager and attempt restart
	manager := GetRestartManager(session, alerter)
	return manager.TryRestart(ctx, paneID, agentType, check.HealthState)
}
