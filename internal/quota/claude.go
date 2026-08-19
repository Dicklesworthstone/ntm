package quota

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude output parsing patterns
// Based on Claude Code CLI output format

var claudeUsagePatterns = struct {
	// Session usage: "Session: 45%" or "Session usage: 45%"
	Session *regexp.Regexp
	// Weekly usage: "Weekly: 72%" or "Weekly usage: 72%"
	Weekly *regexp.Regexp
	// Period/rolling usage: "Period: 30%" or "5-hour period: 30%"
	Period *regexp.Regexp
	// Sonnet-specific: "Sonnet: 38%" or "Sonnet weekly: 38%"
	Sonnet *regexp.Regexp
	// Reset time: "Resets: Mon 00:00 UTC" or "resets Monday at 00:00"
	Reset *regexp.Regexp
	// Rate limited indicator
	Limited *regexp.Regexp
	// Message counts: "(45/100 messages)"
	MessageCount *regexp.Regexp
}{
	Session:      regexp.MustCompile(`(?i)session[^:]*:\s*(\d+(?:\.\d+)?)\s*%`),
	Weekly:       regexp.MustCompile(`(?i)weekly[^:]*:\s*(\d+(?:\.\d+)?)\s*%`),
	Period:       regexp.MustCompile(`(?i)(?:period|5[- ]?hour)[^:]*:\s*(\d+(?:\.\d+)?)\s*%`),
	Sonnet:       regexp.MustCompile(`(?i)sonnet[^:]*:\s*(\d+(?:\.\d+)?)\s*%`),
	Reset:        regexp.MustCompile(`(?i)resets?[:\s]+(.+?)(?:\n|$)`),
	Limited:      regexp.MustCompile(`(?i)(?:rate\s*limit|limited|exceeded|wait|retry)`),
	MessageCount: regexp.MustCompile(`\((\d+)/(\d+)\s*(?:messages?)?\)`),
}

var claudeStatusPatterns = struct {
	// Email: "Logged in as: user@example.com" or "Account: user@example.com"
	Email *regexp.Regexp
	// Organization: "Organization: Personal" or "Org: Anthropic"
	Organization *regexp.Regexp
	// Login method: "Login method: Google OAuth" or "Auth: API Key"
	LoginMethod *regexp.Regexp
	// Plan: "Plan: Pro" or "Subscription: Free"
	Plan *regexp.Regexp
}{
	Email:        regexp.MustCompile(`(?i)(?:logged\s+in\s+as|account|email)[:\s]+(\S+@\S+)`),
	Organization: regexp.MustCompile(`(?i)(?:organization|org)[:\s]+(.+?)(?:\n|$)`),
	LoginMethod:  regexp.MustCompile(`(?i)(?:login\s+method|auth(?:entication)?)[:\s]+(.+?)(?:\n|$)`),
	Plan:         regexp.MustCompile(`(?i)(?:plan|subscription)[:\s]+(.+?)(?:\n|$)`),
}

// Pre-compiled patterns for reset time parsing
var (
	inHoursPattern = regexp.MustCompile(`in\s+(\d+)\s*(?:hours?|hrs?)`)
	inMinsPattern  = regexp.MustCompile(`in\s+(\d+)\s*(?:minutes?|mins?)`)
)

// parseClaudeUsage parses Claude's /usage command output
func parseClaudeUsage(info *QuotaInfo, output string) (bool, error) {
	found := false

	// Parse session usage
	if match := claudeUsagePatterns.Session.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.SessionUsage = val
			found = true
		}
	}

	// Parse weekly usage
	if match := claudeUsagePatterns.Weekly.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.WeeklyUsage = val
			found = true
		}
	}

	// Parse period/rolling usage
	if match := claudeUsagePatterns.Period.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.PeriodUsage = val
			found = true
		}
	}

	// Parse sonnet-specific usage
	if match := claudeUsagePatterns.Sonnet.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.SonnetUsage = val
			found = true
		}
	}

	// Parse reset time
	if match := claudeUsagePatterns.Reset.FindStringSubmatch(output); len(match) > 1 {
		info.ResetString = strings.TrimSpace(match[1])
		info.ResetTime = parseResetTime(info.ResetString)
		found = true
	}

	// Check for rate limiting
	if claudeUsagePatterns.Limited.MatchString(output) {
		info.IsLimited = true
		found = true
	}

	return found, nil
}

// parseClaudeStatus parses Claude's /status command output
func parseClaudeStatus(info *QuotaInfo, output string) {
	// Parse email/account ID
	if match := claudeStatusPatterns.Email.FindStringSubmatch(output); len(match) > 1 {
		info.AccountID = strings.TrimSpace(match[1])
	}

	// Parse organization
	if match := claudeStatusPatterns.Organization.FindStringSubmatch(output); len(match) > 1 {
		info.Organization = strings.TrimSpace(match[1])
	}

	// Parse login method
	if match := claudeStatusPatterns.LoginMethod.FindStringSubmatch(output); len(match) > 1 {
		info.LoginMethod = strings.TrimSpace(match[1])
	}
}

// parseResetTime attempts to parse a reset time string into a time.Time
func parseResetTime(resetStr string) time.Time {
	resetStr = strings.ToLower(strings.TrimSpace(resetStr))

	// Common patterns:
	// - "Mon 00:00 UTC"
	// - "Monday at 00:00"
	// - "in 2 hours"
	// - "tomorrow 00:00"

	now := time.Now().UTC()

	// Try "in X hours" pattern
	if match := inHoursPattern.FindStringSubmatch(resetStr); len(match) > 1 {
		if hours, err := strconv.Atoi(match[1]); err == nil {
			return now.Add(time.Duration(hours) * time.Hour)
		}
	}

	// Try "in X minutes" pattern
	if match := inMinsPattern.FindStringSubmatch(resetStr); len(match) > 1 {
		if mins, err := strconv.Atoi(match[1]); err == nil {
			return now.Add(time.Duration(mins) * time.Minute)
		}
	}

	// Try to parse day of week
	dayMap := map[string]time.Weekday{
		"sun": time.Sunday, "sunday": time.Sunday,
		"mon": time.Monday, "monday": time.Monday,
		"tue": time.Tuesday, "tuesday": time.Tuesday,
		"wed": time.Wednesday, "wednesday": time.Wednesday,
		"thu": time.Thursday, "thursday": time.Thursday,
		"fri": time.Friday, "friday": time.Friday,
		"sat": time.Saturday, "saturday": time.Saturday,
	}

	for dayStr, weekday := range dayMap {
		if strings.Contains(resetStr, dayStr) {
			// Find the next occurrence of this weekday
			daysUntil := int(weekday - now.Weekday())
			if daysUntil <= 0 {
				daysUntil += 7
			}
			return time.Date(now.Year(), now.Month(), now.Day()+daysUntil, 0, 0, 0, 0, time.UTC)
		}
	}

	// Tomorrow
	if strings.Contains(resetStr, "tomorrow") {
		return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}

	// Return zero time if unable to parse
	return time.Time{}
}

// ClaudeQuota is a convenience type for Claude-specific quota info
type ClaudeQuota struct {
	SessionUsage float64
	WeeklyUsage  float64
	PeriodUsage  float64
	SonnetUsage  float64
	ResetTime    string
	AccountEmail string
	Organization string
	LoginMethod  string
	IsLimited    bool
}
