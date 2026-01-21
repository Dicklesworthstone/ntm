// Package robot provides machine-readable output for AI agents.
// health_score.go implements health score calculation for --robot-agent-health.
package robot

import (
	"github.com/Dicklesworthstone/ntm/internal/caut"
)

// HealthRecommendation represents the recommended action based on health analysis.
type HealthRecommendation string

const (
	// RecommendHealthy indicates the agent is healthy and working normally.
	RecommendHealthy HealthRecommendation = "HEALTHY"

	// RecommendMonitor indicates minor issues that should be watched.
	RecommendMonitor HealthRecommendation = "MONITOR"

	// RecommendRestartRecommended indicates a restart would improve agent health.
	RecommendRestartRecommended HealthRecommendation = "RESTART_RECOMMENDED"

	// RecommendRestartUrgent indicates the agent should be restarted immediately.
	RecommendRestartUrgent HealthRecommendation = "RESTART_URGENT"

	// RecommendWaitForReset indicates the agent is rate-limited and should wait.
	RecommendWaitForReset HealthRecommendation = "WAIT_FOR_RESET"

	// RecommendSwitchAccount indicates the provider account is near capacity.
	RecommendSwitchAccount HealthRecommendation = "SWITCH_ACCOUNT"
)

// CalculateHealthScore computes a health score (0-100) from local state and provider usage.
// The algorithm applies deductions from a perfect score based on detected issues.
//
// Local state deductions:
//   - Rate limited: -50 (critical - agent blocked)
//   - In error state: -40 (serious - needs attention)
//   - Context low + idle: -25 (should restart soon)
//   - Context low + working: -10 (working but limited runway)
//   - Unknown agent type: -15 (uncertainty penalty)
//
// Provider usage deductions:
//   - Usage >= 95%: -30 (near cap)
//   - Usage >= 80%: -15 (approaching limit)
//   - Usage >= 60%: -5 (moderate usage)
//
// Confidence adjustment:
//   - Confidence < 0.5: -10 (low confidence in assessment)
func CalculateHealthScore(localState *PaneWorkStatus, providerUsage *caut.ProviderPayload) int {
	score := 100

	// Local state deductions
	if localState.IsRateLimited {
		score -= 50 // Critical - agent blocked
	}

	// Check for error recommendation
	if localState.Recommendation == "ERROR_STATE" {
		score -= 40 // Serious - needs attention
	}

	if localState.IsContextLow {
		if !localState.IsWorking {
			score -= 25 // Should restart soon
		} else {
			score -= 10 // Working but limited runway
		}
	}

	if localState.AgentType == "unknown" {
		score -= 15 // Uncertainty penalty
	}

	// Provider usage deductions (if available)
	if providerUsage != nil {
		if pct := providerUsage.UsedPercent(); pct != nil {
			switch {
			case *pct >= 95:
				score -= 30 // Near cap
			case *pct >= 80:
				score -= 15 // Approaching limit
			case *pct >= 60:
				score -= 5 // Moderate usage
			}
		}
	}

	// Confidence adjustment
	if localState.Confidence < 0.5 {
		score -= 10 // Low confidence in assessment
	}

	// Floor at 0
	if score < 0 {
		score = 0
	}

	return score
}

// HealthGrade converts a numeric health score to a letter grade.
//
//	A: score >= 90 (excellent)
//	B: score >= 80 (good)
//	C: score >= 70 (acceptable)
//	D: score >= 50 (poor)
//	F: score < 50 (failing)
func HealthGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 50:
		return "D"
	default:
		return "F"
	}
}

// CollectIssues gathers a list of actionable issues based on state and usage.
func CollectIssues(localState *PaneWorkStatus, providerUsage *caut.ProviderPayload) []string {
	issues := []string{}

	// Local state issues
	if localState.IsRateLimited {
		issues = append(issues, "Rate limited - agent cannot continue")
	}

	if localState.Recommendation == "ERROR_STATE" {
		issues = append(issues, "Agent in error state - needs attention")
	}

	if localState.IsContextLow {
		if localState.ContextRemaining != nil {
			issues = append(issues, formatIssue("Context remaining below 20%% threshold (%.0f%%)", *localState.ContextRemaining))
		} else {
			issues = append(issues, "Context remaining below threshold")
		}
	}

	if localState.IsIdle && !localState.IsRateLimited {
		issues = append(issues, "Agent is idle - may need new task")
	}

	if localState.AgentType == "unknown" {
		issues = append(issues, "Could not determine agent type")
	}

	if localState.Confidence < 0.5 {
		issues = append(issues, "Low confidence in agent state assessment")
	}

	// Provider usage issues
	if providerUsage != nil {
		if pct := providerUsage.UsedPercent(); pct != nil {
			if *pct >= 95 {
				issues = append(issues, formatIssue("Provider at %.0f%% usage, near cap", *pct))
			} else if *pct >= 80 {
				issues = append(issues, formatIssue("Provider at %.0f%% usage, approaching limit", *pct))
			}
		}

		if !providerUsage.IsOperational() {
			issues = append(issues, "Provider reports non-operational status")
		}
	}

	return issues
}

// formatIssue is a helper for formatting issue strings with values.
func formatIssue(format string, args ...interface{}) string {
	return sprintf(format, args...)
}

// sprintf is a local alias to avoid import in signature.
var sprintf = func() func(string, ...interface{}) string {
	return func(format string, args ...interface{}) string {
		// Manual implementation to avoid fmt import in hot path
		// For simple percentage formatting
		if len(args) == 1 {
			if pct, ok := args[0].(float64); ok {
				// Simple percentage substitution
				result := ""
				for i := 0; i < len(format); i++ {
					if format[i] == '%' && i+1 < len(format) {
						if format[i+1] == '%' {
							result += "%"
							i++
						} else if format[i+1] == '.' && i+3 < len(format) && format[i+2] == '0' && format[i+3] == 'f' {
							result += formatFloat(pct)
							i += 3
						}
					} else {
						result += string(format[i])
					}
				}
				return result
			}
		}
		return format
	}
}()

// formatFloat converts a float to a string without decimal places.
func formatFloat(f float64) string {
	n := int(f + 0.5) // Round
	if n < 0 {
		return "-" + formatInt(-n)
	}
	return formatInt(n)
}

// formatInt converts an integer to a string.
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// DeriveHealthRecommendation determines the recommended action based on health analysis.
func DeriveHealthRecommendation(localState *PaneWorkStatus, providerUsage *caut.ProviderPayload, healthScore int) (HealthRecommendation, string) {
	// Check for rate limit - highest priority
	if localState.IsRateLimited {
		reason := "Rate limited"
		if providerUsage != nil {
			if resetTime := providerUsage.GetResetTime(); resetTime != nil {
				reason = "Rate limited, resets at " + resetTime.Format("2006-01-02T15:04:05Z")
			}
		}
		return RecommendWaitForReset, reason
	}

	// Check for error state
	if localState.Recommendation == "ERROR_STATE" {
		return RecommendRestartUrgent, "Agent in error state, requires immediate attention"
	}

	// Check for provider near capacity
	if providerUsage != nil {
		if pct := providerUsage.UsedPercent(); pct != nil && *pct >= 90 {
			return RecommendSwitchAccount, formatIssue("Provider at %.0f%% usage, consider account switch", *pct)
		}
	}

	// Check for low context when idle
	if localState.IsContextLow && localState.IsIdle {
		reason := "Low context"
		if localState.ContextRemaining != nil {
			reason = formatIssue("Low context (%.0f%%)", *localState.ContextRemaining)
		}
		return RecommendRestartRecommended, reason + ", restart will restore capacity"
	}

	// Check for stuck/unknown agent
	if localState.AgentType == "unknown" && localState.Confidence < 0.3 {
		return RecommendRestartUrgent, "Unable to determine agent state, may be stuck"
	}

	// Health score based decisions
	if healthScore >= 70 {
		if localState.IsWorking {
			return RecommendHealthy, "Agent working normally, account has capacity"
		}
		return RecommendHealthy, "Agent ready for work, account has capacity"
	}

	if healthScore >= 50 {
		return RecommendMonitor, "Minor issues detected, monitoring recommended"
	}

	// Low health score but no specific critical issue
	return RecommendRestartRecommended, "Multiple issues affecting agent health"
}
