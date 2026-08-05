// Package robot provides machine-readable output for AI agents.
// health_oauth.go implements the --robot-health-oauth command for per-agent
// OAuth and rate-limit status detection. It integrates with the ratelimit
// package's CodexThrottle for AIMD-aware throttle status. (bd-2plo3)
package robot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// OAuth and Rate Limit Status API (bd-2plo3)
// =============================================================================

// OAuthStatus represents OAuth authentication status for an agent.
type OAuthStatus string

const (
	OAuthValid   OAuthStatus = "valid"
	OAuthExpired OAuthStatus = "expired"
	OAuthError   OAuthStatus = "error"
	OAuthUnknown OAuthStatus = "unknown"
)

// RateLimitStatus represents rate limit status for an agent.
type RateLimitStatus string

const (
	RateLimitOK      RateLimitStatus = "ok"
	RateLimitWarning RateLimitStatus = "warning" // 3+ limits in 5 minutes
	RateLimitLimited RateLimitStatus = "limited"
)

// RateLimitWarningThreshold is the number of rate limit events within the
// observation window required to escalate from OK to warning.
const RateLimitWarningThreshold = 3

// AgentOAuthHealth contains OAuth and rate limit status for a single agent.
type AgentOAuthHealth struct {
	Pane              int             `json:"pane"`
	AgentType         string          `json:"agent_type"`
	Provider          string          `json:"provider"` // claude, openai, gemini
	OAuthStatus       OAuthStatus     `json:"oauth_status"`
	OAuthError        string          `json:"oauth_error,omitempty"`
	RateLimitStatus   RateLimitStatus `json:"rate_limit_status"`
	RateLimitCount    int             `json:"rate_limit_count"`   // limits in window
	CooldownRemaining int             `json:"cooldown_remaining"` // seconds
	LastActivitySec   int             `json:"last_activity_sec"`
	ErrorCount        int             `json:"error_count"` // errors in last 5 minutes
	// ThrottlePhase reports the AIMD throttle phase for Codex agents.
	// Empty for non-Codex agents or when no CodexThrottle is available.
	ThrottlePhase string `json:"throttle_phase,omitempty"`
	// ThrottleGuidance provides human-readable remediation for throttled agents.
	ThrottleGuidance string `json:"throttle_guidance,omitempty"`
}

// OAuthHealthOutput is the response format for --robot-health-oauth=SESSION.
type OAuthHealthOutput struct {
	RobotResponse
	Session   string                       `json:"session"`
	CheckedAt time.Time                    `json:"checked_at"`
	Agents    []AgentOAuthHealth           `json:"agents"`
	Pools     map[string]OAuthProviderPool `json:"pools"`
	Summary   OAuthHealthSummary           `json:"summary"`
	// Display is the human-readable status block for TUI or log output.
	Display string `json:"display,omitempty"`
}

// OAuthProviderPool summarizes the rate-limit decision for all panes using a
// provider. HealthyPanesRemaining is intentionally pane-scoped: this command
// cannot prove which account a pane uses, so it must not mislabel pane health
// as account availability.
type OAuthProviderPool struct {
	Provider              string `json:"provider"`
	Panes                 []int  `json:"panes"`
	RateLimitedCount      int    `json:"rate_limited_count"`
	AllRateLimited        bool   `json:"all_rate_limited"`
	HealthyPanesRemaining int    `json:"healthy_panes_remaining"`
	Recommendation        string `json:"recommendation"`
}

// OAuthHealthSummary contains aggregate OAuth/rate limit status.
type OAuthHealthSummary struct {
	Total        int `json:"total"`
	OAuthValid   int `json:"oauth_valid"`
	OAuthExpired int `json:"oauth_expired"`
	OAuthError   int `json:"oauth_error"`
	// OAuthUnknown counts agents whose OAuth status could not be determined
	// from scraped pane output (no auth error, no confirming activity signal).
	// It is surfaced explicitly so "unknown" is never conflated with a healthy
	// "valid" agent: OAuthValid counts only confirmed-good agents, and
	// Total == OAuthValid+OAuthExpired+OAuthError+OAuthUnknown holds (ntm#207).
	OAuthUnknown  int `json:"oauth_unknown"`
	RateLimitOK   int `json:"rate_limit_ok"`
	RateLimitWarn int `json:"rate_limit_warn"`
	RateLimited   int `json:"rate_limited"`
}

// OAuthHealthOptions configures the health-oauth command.
type OAuthHealthOptions struct {
	Session string
	// CodexThrottle is an optional CodexThrottle to integrate AIMD status.
	// When nil, only output-based detection is used.
	CodexThrottle *ratelimit.CodexThrottle
}

// GetHealthOAuth collects per-agent OAuth and rate limit status for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetHealthOAuth(session string) (*OAuthHealthOutput, error) {
	return GetHealthOAuthWithOptions(OAuthHealthOptions{Session: session})
}

// GetHealthOAuthWithOptions collects per-agent OAuth and rate limit status
// using the provided options, including optional CodexThrottle integration.
func GetHealthOAuthWithOptions(opts OAuthHealthOptions) (*OAuthHealthOutput, error) {
	output := &OAuthHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CheckedAt:     time.Now().UTC(),
		Agents:        []AgentOAuthHealth{},
		Pools:         map[string]OAuthProviderPool{},
		Summary:       OAuthHealthSummary{},
	}

	// Check if session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		return output, nil
	}

	// Get panes in the session
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	// Check OAuth/rate limit status for each agent pane
	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" || agentType == "unknown" {
			continue // Skip non-agent panes
		}

		agentHealth := getAgentOAuthHealth(opts.Session, pane, agentType)

		// Integrate CodexThrottle status for Codex agents
		if opts.CodexThrottle != nil && (agentType == "cod" || agentType == "codex") {
			enrichWithThrottle(&agentHealth, opts.CodexThrottle)
		}

		output.Agents = append(output.Agents, agentHealth)

		// Update summary
		output.Summary.Total++
		switch agentHealth.OAuthStatus {
		case OAuthValid:
			output.Summary.OAuthValid++
		case OAuthExpired:
			output.Summary.OAuthExpired++
		case OAuthError:
			output.Summary.OAuthError++
		case OAuthUnknown:
			output.Summary.OAuthUnknown++
		}
		switch agentHealth.RateLimitStatus {
		case RateLimitOK:
			output.Summary.RateLimitOK++
		case RateLimitWarning:
			output.Summary.RateLimitWarn++
		case RateLimitLimited:
			output.Summary.RateLimited++
		}
	}
	output.Pools = summarizeOAuthProviderPools(output.Agents)

	// Generate display string
	output.Display = FormatOAuthHealthDisplay(output)

	return output, nil
}

func summarizeOAuthProviderPools(agents []AgentOAuthHealth) map[string]OAuthProviderPool {
	pools := make(map[string]OAuthProviderPool)
	for _, agentHealth := range agents {
		provider := canonicalRobotProvider(agentHealth.Provider)
		pool := pools[provider]
		if pool.Provider == "" {
			pool.Provider = provider
			pool.Panes = []int{}
		}
		pool.Panes = append(pool.Panes, agentHealth.Pane)
		if agentHealth.RateLimitStatus == RateLimitLimited {
			pool.RateLimitedCount++
		}
		if agentHealth.OAuthStatus == OAuthValid && agentHealth.RateLimitStatus != RateLimitLimited {
			pool.HealthyPanesRemaining++
		}
		pools[provider] = pool
	}

	for provider, pool := range pools {
		sort.Ints(pool.Panes)
		pool.AllRateLimited = len(pool.Panes) > 0 && pool.RateLimitedCount == len(pool.Panes)
		switch {
		case pool.AllRateLimited:
			pool.Recommendation = "rotate_provider"
		case pool.RateLimitedCount > 0:
			pool.Recommendation = "route_to_healthy_panes"
		default:
			pool.Recommendation = "continue"
		}
		pools[provider] = pool
	}
	return pools
}

// enrichWithThrottle merges CodexThrottle status into the agent health record.
func enrichWithThrottle(health *AgentOAuthHealth, ct *ratelimit.CodexThrottle) {
	if ct == nil {
		return
	}
	status := ct.Status()
	health.ThrottlePhase = string(status.Phase)
	health.ThrottleGuidance = status.Guidance

	// Escalate rate-limit status based on throttle phase.
	switch status.Phase {
	case ratelimit.ThrottlePaused:
		health.RateLimitStatus = RateLimitLimited
		remaining := int(status.CooldownRemaining.Seconds())
		if remaining > health.CooldownRemaining {
			health.CooldownRemaining = remaining
		}
	case ratelimit.ThrottleRecovering:
		if health.RateLimitStatus == RateLimitOK {
			health.RateLimitStatus = RateLimitWarning
		}
	}

	// Merge rate-limit count from throttle if higher.
	if status.RateLimitCount > health.RateLimitCount {
		health.RateLimitCount = status.RateLimitCount
	}
}

// getAgentOAuthHealth determines OAuth and rate limit status for a single agent.
func getAgentOAuthHealth(session string, pane tmux.Pane, agentType string) AgentOAuthHealth {
	health := AgentOAuthHealth{
		Pane:            pane.Index,
		AgentType:       agentType,
		Provider:        agentTypeToProvider(agentType),
		OAuthStatus:     OAuthUnknown,
		RateLimitStatus: RateLimitOK,
	}

	// Get activity time
	activityTime, err := tmux.GetPaneActivity(pane.ID)
	if err == nil {
		health.LastActivitySec = int(time.Since(activityTime).Seconds())
	}

	// Capture recent output for OAuth/error detection
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := tmux.CapturePaneOutputContext(ctx, pane.ID, 50)
	if err != nil {
		return health
	}

	output = stripANSI(output)
	outputLower := strings.ToLower(output)

	// Detect OAuth status from output patterns
	health.OAuthStatus, health.OAuthError = detectOAuthStatus(outputLower)

	// Detect rate limit status
	health.RateLimitStatus, health.RateLimitCount = detectRateLimitStatusFromOutput(outputLower, agentType)

	// Check cooldown from backoff manager
	manager := GetBackoffManager(session)
	remaining := manager.GetBackoffRemaining(pane.ID)
	if remaining > 0 {
		health.CooldownRemaining = int(remaining.Seconds())
		health.RateLimitStatus = RateLimitLimited
	}

	// Count rate limits from backoff manager if we have data
	if backoff := manager.GetBackoff(pane.ID); backoff != nil {
		if backoff.TotalRateLimits > health.RateLimitCount {
			health.RateLimitCount = backoff.TotalRateLimits
		}
	}

	// Count errors in output
	health.ErrorCount = countErrorsInOutput(outputLower)

	// If rate limit count >= warning threshold, upgrade to warning
	if health.RateLimitStatus == RateLimitOK && health.RateLimitCount >= RateLimitWarningThreshold {
		health.RateLimitStatus = RateLimitWarning
	}

	return health
}

// agentTypeToProvider maps agent type to provider name.
func agentTypeToProvider(agentType string) string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "openai"
	case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
		// Antigravity (agy) authenticates through Google's OAuth, the same
		// provider as the legacy Gemini CLI.
		return "gemini"
	default:
		return "unknown"
	}
}

// detectOAuthStatus detects OAuth status from pane output.
func detectOAuthStatus(outputLower string) (OAuthStatus, string) {
	// Check for explicit authentication errors
	authErrorPatterns := []struct {
		pattern string
		status  OAuthStatus
		message string
	}{
		{"authentication failed", OAuthError, "authentication failed"},
		{"authentication error", OAuthError, "authentication error"},
		{"invalid api key", OAuthError, "invalid API key"},
		{"api key not found", OAuthError, "API key not found"},
		{"unauthorized", OAuthError, "unauthorized"},
		{"401", OAuthError, "HTTP 401"},
		{"token expired", OAuthExpired, "token expired"},
		{"session expired", OAuthExpired, "session expired"},
		{"please log in", OAuthExpired, "login required"},
		{"needs reauth", OAuthExpired, "needs reauthentication"},
		{"refresh token", OAuthExpired, "refresh token issue"},
	}

	for _, p := range authErrorPatterns {
		if strings.Contains(outputLower, p.pattern) {
			return p.status, p.message
		}
	}

	// If we see normal activity indicators, OAuth is likely valid
	validIndicators := []string{
		"thinking", "working", "reading", "writing",
		"searching", "executing", "analyzing",
	}
	for _, ind := range validIndicators {
		if strings.Contains(outputLower, ind) {
			return OAuthValid, ""
		}
	}

	return OAuthUnknown, ""
}

// detectRateLimitStatusFromOutput detects rate limit status from output.
func detectRateLimitStatusFromOutput(output string, agentType string) (RateLimitStatus, int) {
	rateLimitPatterns := []string{
		"rate limit", "ratelimit", "rate-limit",
		"429", "too many requests", "quota exceeded",
		"try again", "retry after", "backoff",
	}
	if agent.AgentType(agentType).Canonical() == agent.AgentTypeCodex {
		rateLimitPatterns = append(rateLimitPatterns,
			"insufficient_quota",
			"billing limit",
			"usage cap",
			"tokens per min",
			"requests per min",
		)
	}

	count := 0
	for _, p := range rateLimitPatterns {
		if strings.Contains(output, p) {
			count++
		}
	}
	if count == 0 && ratelimit.DetectRateLimitForAgent(output, agentType).RateLimited {
		count = 1
	}

	if count >= 3 {
		return RateLimitLimited, count
	}
	if count >= 1 {
		return RateLimitWarning, count
	}
	return RateLimitOK, 0
}

// countErrorsInOutput counts error patterns in output.
func countErrorsInOutput(outputLower string) int {
	errorPatterns := []string{
		"error", "failed", "exception", "panic",
		"timeout", "connection refused",
	}

	count := 0
	for _, p := range errorPatterns {
		if strings.Contains(outputLower, p) {
			count++
		}
	}
	return count
}

// FormatOAuthHealthDisplay formats per-agent OAuth/rate-limit status
// for human-readable display (TUI or log output).
func FormatOAuthHealthDisplay(output *OAuthHealthOutput) string {
	if output == nil || len(output.Agents) == 0 {
		return "OAuth/Rate Status: (no agents)"
	}

	var b strings.Builder
	b.WriteString("OAuth/Rate Status:\n")
	for _, ag := range output.Agents {
		oauthIcon := oauthIcon(ag.OAuthStatus)
		rateLabel := rateLabel(ag.RateLimitStatus)
		lastAgo := formatLastActivity(ag.LastActivitySec)
		line := fmt.Sprintf("  %s-%d: OAuth=%s Rate=%-7s Last=%s",
			ag.AgentType, ag.Pane, oauthIcon, rateLabel, lastAgo)
		if ag.RateLimitStatus == RateLimitWarning || ag.RateLimitStatus == RateLimitLimited {
			line += fmt.Sprintf(" (%d limits in 5m)", ag.RateLimitCount)
		}
		if ag.ThrottlePhase != "" && ag.ThrottlePhase != string(ratelimit.ThrottleNormal) {
			line += fmt.Sprintf(" [throttle=%s]", ag.ThrottlePhase)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// oauthIcon returns a compact icon string for the OAuth status.
func oauthIcon(s OAuthStatus) string {
	switch s {
	case OAuthValid:
		return "\u2713" // checkmark
	case OAuthExpired:
		return "EXP"
	case OAuthError:
		return "ERR"
	default:
		return "?"
	}
}

// rateLabel returns a compact label for the rate-limit status.
func rateLabel(s RateLimitStatus) string {
	switch s {
	case RateLimitOK:
		return "OK"
	case RateLimitWarning:
		return "WARN"
	case RateLimitLimited:
		return "LIMITED"
	default:
		return "?"
	}
}

// formatLastActivity converts seconds-ago into a human-readable duration.
func formatLastActivity(sec int) string {
	if sec < 0 {
		return "now"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds ago", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm ago", sec/60)
	}
	return fmt.Sprintf("%dh ago", sec/3600)
}

// PrintHealthOAuth outputs per-agent OAuth and rate limit status for a session
// and returns the process exit code (0 on success, nonzero when the result
// reports success:false — see ExitCodeForResponse and ntm#207). The failure
// envelope is always emitted as JSON before returning so callers still receive
// the machine-readable payload.
func PrintHealthOAuth(session string) int {
	output, err := GetHealthOAuth(session)
	if err != nil {
		// GetHealthOAuth folds operational failures into the envelope and
		// returns a nil error; a non-nil error is an unexpected internal fault.
		response := NewErrorResponse(err, ErrCodeInternalError, "")
		return printLegacyRobotOutput(&response, response, 1, "robot OAuth health failed")
	}
	return printLegacyRobotOutput(output, output.RobotResponse, ExitCodeForResponse(output.RobotResponse), "robot OAuth health failed")
}
