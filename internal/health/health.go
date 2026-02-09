// Package health provides agent health checking and status detection.
package health

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Status represents the overall health status of an agent
type Status string

const (
	StatusOK      Status = "ok"      // Agent is healthy and working
	StatusWarning Status = "warning" // Agent may have issues (stale, near rate limit)
	StatusError   Status = "error"   // Agent has errors (crashed, rate limited)
	StatusUnknown Status = "unknown" // Cannot determine status
)

// ActivityLevel represents how active the agent is
type ActivityLevel string

const (
	ActivityActive  ActivityLevel = "active"  // Content changed recently (< 30s)
	ActivityIdle    ActivityLevel = "idle"    // No change, but process running (prompt visible)
	ActivityStale   ActivityLevel = "stale"   // No change for extended period (> 5m)
	ActivityUnknown ActivityLevel = "unknown" // Cannot determine
)

// ProcessStatus represents the process state
type ProcessStatus string

const (
	ProcessRunning ProcessStatus = "running" // Process is alive
	ProcessExited  ProcessStatus = "exited"  // Process has exited
	ProcessUnknown ProcessStatus = "unknown" // Cannot determine
)

// Issue represents a detected problem
type Issue struct {
	Type    string `json:"type"`    // error_type identifier
	Message string `json:"message"` // Human-readable description
}

// ProgressStage represents what phase of work an agent is in
type ProgressStage string

const (
	StageStarting  ProgressStage = "starting"  // Agent beginning work
	StageWorking   ProgressStage = "working"   // Agent actively working
	StageFinishing ProgressStage = "finishing" // Agent wrapping up
	StageStuck     ProgressStage = "stuck"     // Agent blocked or erroring
	StageIdle      ProgressStage = "idle"      // Agent waiting for input
	StageUnknown   ProgressStage = "unknown"   // Cannot determine
)

// Progress represents the detected work progress of an agent
type Progress struct {
	Stage          ProgressStage `json:"stage"`             // Current progress stage
	Confidence     float64       `json:"confidence"`        // 0.0-1.0 confidence in detection
	Indicators     []string      `json:"indicators"`        // What patterns were detected
	TimeInStageSec int           `json:"time_in_stage_sec"` // Seconds in current stage
	StageChangedAt *time.Time    `json:"stage_changed_at"`  // When stage last changed
}

// AgentHealth contains health information for a single agent
type AgentHealth struct {
	Pane          int           `json:"pane"`           // Pane index
	PaneID        string        `json:"pane_id"`        // Full pane ID
	AgentType     string        `json:"agent_type"`     // claude, codex, gemini, user, unknown
	Status        Status        `json:"status"`         // Overall health status
	ProcessStatus ProcessStatus `json:"process_status"` // Process running state
	Activity      ActivityLevel `json:"activity"`       // Activity level
	LastActivity  *time.Time    `json:"last_activity"`  // Last activity timestamp
	IdleSeconds   int           `json:"idle_seconds"`   // Seconds since last activity
	Issues        []Issue       `json:"issues"`         // Detected issues
	RateLimited   bool          `json:"rate_limited"`   // True if agent hit rate limit
	WaitSeconds   int           `json:"wait_seconds"`   // Suggested wait time (if rate limited)
	Progress      *Progress     `json:"progress"`       // Detected work progress
	ShellPID      int           `json:"shell_pid"`      // Shell PID from tmux pane
}

// SessionHealth contains health information for an entire session
type SessionHealth struct {
	Session       string        `json:"session"`        // Session name
	CheckedAt     time.Time     `json:"checked_at"`     // When check was performed
	Agents        []AgentHealth `json:"agents"`         // Per-agent health
	Summary       HealthSummary `json:"summary"`        // Aggregate summary
	OverallStatus Status        `json:"overall_status"` // Worst status among all agents
}

// HealthSummary provides aggregate statistics
type HealthSummary struct {
	Total   int `json:"total"`   // Total agents
	Healthy int `json:"healthy"` // Agents with OK status
	Warning int `json:"warning"` // Agents with warning status
	Error   int `json:"error"`   // Agents with error status
	Unknown int `json:"unknown"` // Agents with unknown status
}

// CheckSession performs health checks on all agents in a session
func CheckSession(ctx context.Context, session string) (*SessionHealth, error) {
	if !tmux.SessionExists(session) {
		return nil, &SessionNotFoundError{Session: session}
	}

	panesWithActivity, err := tmux.GetPanesWithActivityContext(ctx, session)
	if err != nil {
		return nil, err
	}

	health := &SessionHealth{
		Session:       session,
		CheckedAt:     time.Now().UTC(),
		Agents:        make([]AgentHealth, 0, len(panesWithActivity)),
		Summary:       HealthSummary{},
		OverallStatus: StatusOK,
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // Limit concurrent tmux checks to 8

	for _, pa := range panesWithActivity {
		wg.Add(1)
		go func(pa tmux.PaneActivity) {
			defer wg.Done()

			select {
			case sem <- struct{}{}: // Acquire token
			case <-ctx.Done():
				return
			}
			agentHealth := checkAgent(ctx, pa)
			<-sem // Release token

			mu.Lock()
			defer mu.Unlock()

			health.Agents = append(health.Agents, agentHealth)

			// Update summary
			health.Summary.Total++
			switch agentHealth.Status {
			case StatusOK:
				health.Summary.Healthy++
			case StatusWarning:
				health.Summary.Warning++
			case StatusError:
				health.Summary.Error++
			default:
				health.Summary.Unknown++
			}

			// Update overall status (worst wins)
			if statusSeverity(agentHealth.Status) > statusSeverity(health.OverallStatus) {
				health.OverallStatus = agentHealth.Status
			}
		}(pa)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return health, nil
}

// checkAgent performs health checks on a single agent pane
func checkAgent(ctx context.Context, pa tmux.PaneActivity) AgentHealth {
	agent := AgentHealth{
		Pane:          pa.Pane.Index,
		PaneID:        pa.Pane.ID,
		AgentType:     string(pa.Pane.Type),
		Status:        StatusUnknown,
		ProcessStatus: ProcessUnknown,
		Activity:      ActivityUnknown,
		Issues:        []Issue{},
	}

	// Set last activity
	if !pa.LastActivity.IsZero() {
		agent.LastActivity = &pa.LastActivity
		agent.IdleSeconds = int(time.Since(pa.LastActivity).Seconds())
	}

	// Capture pane output for analysis
	output, err := tmux.CapturePaneOutputContext(ctx, pa.Pane.ID, 50)
	if err != nil {
		agent.ProcessStatus = ProcessUnknown
		agent.Status = StatusUnknown
		return agent
	}

	// Check for error patterns
	issues := detectErrors(output)

	// Check for rate limit and parse wait time (including exit code detection)
	detection := ratelimit.DetectRateLimit(output)
	if detection.RateLimited {
		if !hasRateLimitIssue(issues) {
			issues = append(issues, Issue{Type: "rate_limit", Message: "Rate limit detected"})
		}
		agent.RateLimited = true
		agent.WaitSeconds = detection.WaitSeconds
	} else if hasRateLimitIssue(issues) {
		agent.RateLimited = true
		agent.WaitSeconds = ratelimit.ParseWaitSeconds(output)
	}

	agent.Issues = issues

	// Determine activity level
	agent.Activity = detectActivity(output, pa.LastActivity, string(pa.Pane.Type))

	// Determine process status using PID-based check (preferred) with text fallback
	agent.ProcessStatus = detectProcessStatus(output, pa.Pane.Command, pa.Pane.PID)

	// Detect work progress
	agent.Progress = detectProgress(output, agent.Activity, agent.Issues)

	// Calculate overall status
	agent.Status = calculateStatus(agent)

	return agent
}

// detectErrors scans output for error patterns
func detectErrors(output string) []Issue {
	var issues []Issue
	errorTypes := status.DetectAllErrorsInOutput(output)

	for _, et := range errorTypes {
		var typeStr string
		var message string

		switch et {
		case status.ErrorRateLimit:
			typeStr = "rate_limit"
			message = "Rate limit detected"
		case status.ErrorCrash:
			typeStr = "crash"
			message = "Agent crashed"
		case status.ErrorAuth:
			typeStr = "auth_error"
			message = "Authentication error"
		case status.ErrorConnection:
			typeStr = "network_error"
			message = "Network error"
		case status.ErrorGeneric:
			typeStr = "error"
			message = "Error detected"
		default:
			continue
		}

		issues = append(issues, Issue{
			Type:    typeStr,
			Message: message,
		})
	}

	return issues
}

// parseWaitTime extracts the suggested wait time in seconds from rate limit messages
// Returns 0 if no wait time is found
func parseWaitTime(output string) int {
	return ratelimit.ParseWaitSeconds(output)
}

// hasRateLimitIssue checks if any issue indicates a rate limit
func hasRateLimitIssue(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Type == "rate_limit" {
			return true
		}
	}
	return false
}

// progressPatterns define indicators for each progress stage
var progressPatterns = map[ProgressStage][]struct {
	Pattern   *regexp.Regexp
	Indicator string
	Weight    float64 // How much this contributes to confidence
}{
	StageStarting: {
		{regexp.MustCompile(`(?i)^(Let me|I'll|I will|Starting|Beginning)`), "planning_phrase", 0.3},
		{regexp.MustCompile(`(?i)(read|reading|search|searching|looking|exploring)`), "exploration", 0.2},
		{regexp.MustCompile(`(?i)(understand|analyzing|reviewing|checking)`), "analysis", 0.2},
		{regexp.MustCompile(`(?i)(plan|approach|strategy)`), "planning", 0.3},
	},
	StageWorking: {
		{regexp.MustCompile(`(?i)(edit|editing|writing|creating|implementing)`), "file_edits", 0.4},
		{regexp.MustCompile("```"), "code_blocks", 0.3},
		{regexp.MustCompile(`(?i)(running|testing|building|compiling)`), "execution", 0.3},
		{regexp.MustCompile(`(?i)(adding|removing|changing|updating|modifying)`), "modifications", 0.3},
		{regexp.MustCompile(`(?i)(function|class|struct|type|const|var)`), "code_content", 0.2},
	},
	StageFinishing: {
		{regexp.MustCompile(`(?i)(done|complete|finished|completed|success)`), "completion_phrase", 0.4},
		{regexp.MustCompile(`(?i)(summary|in summary|to summarize)`), "summary", 0.3},
		{regexp.MustCompile(`(?i)(commit|committed|push|pushed)`), "git_actions", 0.4},
		{regexp.MustCompile(`(?i)(all tests pass|tests passed|build succeeded)`), "success_report", 0.4},
		{regexp.MustCompile(`(?i)(ready for|you can now|feel free to)`), "handoff", 0.3},
	},
	StageStuck: {
		{regexp.MustCompile(`(?i)(error|failed|cannot|unable|problem)`), "error_phrase", 0.4},
		{regexp.MustCompile(`(?i)(help|question|clarify|unclear|confused)`), "needs_help", 0.3},
		{regexp.MustCompile(`(?i)(stuck|blocked|waiting|need more)`), "blocked_phrase", 0.4},
		{regexp.MustCompile(`(?i)(retry|retrying|trying again)`), "retry_attempt", 0.3},
		{regexp.MustCompile(`(?i)\?$`), "question_mark", 0.2},
	},
}

// detectProgress analyzes pane output to determine work progress stage
func detectProgress(output string, activity ActivityLevel, issues []Issue) *Progress {
	progress := &Progress{
		Stage:      StageUnknown,
		Confidence: 0.0,
		Indicators: []string{},
	}

	// If agent is idle at prompt, it's in idle stage
	if activity == ActivityIdle {
		progress.Stage = StageIdle
		progress.Confidence = 0.9
		progress.Indicators = []string{"idle_prompt"}
		return progress
	}

	// Check for stuck indicators first (errors, rate limits)
	if hasRateLimitIssue(issues) || hasErrorIssue(issues) {
		progress.Stage = StageStuck
		progress.Confidence = 0.8
		progress.Indicators = []string{"error_detected"}
		return progress
	}

	// Score each stage based on pattern matches
	stageScores := make(map[ProgressStage]float64)
	stageIndicators := make(map[ProgressStage][]string)

	// Only analyze the last portion of output (most recent context)
	recentOutput := output
	if len(output) > 2000 {
		recentOutput = output[len(output)-2000:]
	}

	for stage, patterns := range progressPatterns {
		for _, p := range patterns {
			if p.Pattern.MatchString(recentOutput) {
				stageScores[stage] += p.Weight
				stageIndicators[stage] = append(stageIndicators[stage], p.Indicator)
			}
		}
	}

	// Find stage with highest score
	var bestStage ProgressStage = StageUnknown
	var bestScore float64 = 0.0

	for stage, score := range stageScores {
		if score > bestScore {
			bestScore = score
			bestStage = stage
		}
	}

	// Set progress based on best match
	if bestScore > 0.2 { // Minimum threshold for detection
		progress.Stage = bestStage
		progress.Confidence = normalizeConfidence(bestScore)
		progress.Indicators = dedupeIndicators(stageIndicators[bestStage])
	}

	return progress
}

// hasErrorIssue checks if any issue indicates an error (crash, auth, network)
func hasErrorIssue(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Type == "crash" || issue.Type == "auth_error" || issue.Type == "network_error" {
			return true
		}
	}
	return false
}

// normalizeConfidence converts raw score to 0.0-1.0 range
func normalizeConfidence(score float64) float64 {
	// Scores above 1.0 get capped, lower scores are proportional
	if score >= 1.0 {
		return 0.95
	}
	if score >= 0.7 {
		return 0.85
	}
	if score >= 0.5 {
		return 0.75
	}
	if score >= 0.3 {
		return 0.60
	}
	return 0.50
}

// dedupeIndicators removes duplicate indicators
func dedupeIndicators(indicators []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, ind := range indicators {
		if !seen[ind] {
			seen[ind] = true
			result = append(result, ind)
		}
	}
	return result
}

// detectActivity determines the activity level of an agent
func detectActivity(output string, lastActivity time.Time, agentType string) ActivityLevel {
	// Check last activity timestamp
	// If lastActivity is zero (not set), we can't determine idle time reliably
	var idleTime time.Duration
	if lastActivity.IsZero() {
		// No activity timestamp available - will rely on prompt detection
		idleTime = 0
	} else {
		idleTime = time.Since(lastActivity)
	}

	// Check for idle prompt using status package
	hasIdlePrompt := status.DetectIdleFromOutput(output, agentType)

	// Determine activity level
	// If we don't have reliable timing (idleTime == 0 from zero lastActivity),
	// use prompt detection as the primary signal
	if hasIdlePrompt {
		return ActivityIdle
	}

	if idleTime == 0 {
		return ActivityUnknown
	}

	if idleTime < 5*time.Minute {
		return ActivityActive
	}
	// Stale if > 5 minutes with no activity
	return ActivityStale
}

// detectProcessStatus determines if the agent process is running.
//
// When shellPID > 0 it uses PID-based liveness as the primary signal:
// the shell's child process (the agent) is checked via /proc. This avoids
// false positives from AI agents that routinely print strings like
// "exit status" or "connection closed" in their normal output.
//
// Text-pattern matching is only used as a fallback when no PID is available
// (e.g. in tests or when tmux doesn't report a PID).
func detectProcessStatus(output string, command string, shellPID int) ProcessStatus {
	// Primary: PID-based liveness check (reliable, no false positives).
	if shellPID > 0 {
		if process.HasChildAlive(shellPID) {
			return ProcessRunning
		}
		// Shell has no living child -- the agent process has exited.
		return ProcessExited
	}

	// Fallback: text-based detection when PID is unavailable.
	exitPatterns := []string{
		"exit status", "exited with", "process exited",
		"connection closed", "session ended",
	}

	outputLower := strings.ToLower(output)
	for _, pattern := range exitPatterns {
		if strings.Contains(outputLower, pattern) {
			return ProcessExited
		}
	}

	// If command is empty or shell-like, assume running
	if command == "" || strings.Contains(command, "bash") || strings.Contains(command, "zsh") || strings.Contains(command, "sh") {
		return ProcessRunning
	}

	// Default to running if no exit indicators
	return ProcessRunning
}

// calculateStatus determines overall status from all factors
func calculateStatus(agent AgentHealth) Status {
	// Error status if any critical issues
	for _, issue := range agent.Issues {
		if issue.Type == "crash" || issue.Type == "auth_error" {
			return StatusError
		}
	}

	// Error if process exited
	if agent.ProcessStatus == ProcessExited {
		return StatusError
	}

	// Warning if rate limited
	for _, issue := range agent.Issues {
		if issue.Type == "rate_limit" || issue.Type == "network_error" {
			return StatusWarning
		}
	}

	// Warning if stale
	if agent.Activity == ActivityStale {
		return StatusWarning
	}

	// OK if we got here
	if agent.ProcessStatus == ProcessRunning || agent.Activity == ActivityActive || agent.Activity == ActivityIdle {
		return StatusOK
	}

	return StatusUnknown
}

// statusSeverity returns numeric severity for status comparison
func statusSeverity(s Status) int {
	switch s {
	case StatusOK:
		return 0
	case StatusWarning:
		return 1
	case StatusError:
		return 2
	default:
		return 0
	}
}

// SessionNotFoundError is returned when session doesn't exist
type SessionNotFoundError struct {
	Session string
}

func (e *SessionNotFoundError) Error() string {
	return "session '" + e.Session + "' not found"
}
