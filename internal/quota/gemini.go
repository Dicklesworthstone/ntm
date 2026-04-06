package quota

// Gemini quota parsing — heuristic patterns, not validated against real CLI output.
// These parsers attempt best-effort extraction but may not match actual Gemini CLI formats.
// Returns found=false when no patterns match, which callers should treat as "unknown" not "zero".

import (
	"regexp"
	"strconv"
	"strings"
)

var geminiUsagePatterns = struct {
	// Usage patterns (to be refined after research)
	Usage   *regexp.Regexp
	Quota   *regexp.Regexp
	Limited *regexp.Regexp
}{
	Usage:   regexp.MustCompile(`(?i)usage[:\s]+(\d+(?:\.\d+)?)\s*%`),
	Quota:   regexp.MustCompile(`(?i)quota[:\s]+(\d+(?:\.\d+)?)\s*%`),
	Limited: regexp.MustCompile(`(?i)(?:rate\s*limit|limited|exceeded|quota\s*exceeded)`),
}

var geminiStatusPatterns = struct {
	Account *regexp.Regexp
	Project *regexp.Regexp
	Region  *regexp.Regexp
}{
	Account: regexp.MustCompile(`(?i)(?:account|email)[:\s]+(\S+@\S+)`),
	Project: regexp.MustCompile(`(?i)(?:project)[:\s]+(.+?)(?:\n|$)`),
	Region:  regexp.MustCompile(`(?i)(?:region)[:\s]+(.+?)(?:\n|$)`),
}

// parseGeminiUsage parses Gemini usage output
func parseGeminiUsage(info *QuotaInfo, output string) (bool, error) {
	found := false

	// Parse usage percentage
	if match := geminiUsagePatterns.Usage.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.SessionUsage = val
			found = true
		}
	}

	// Parse quota percentage
	if match := geminiUsagePatterns.Quota.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.WeeklyUsage = val
			found = true
		}
	}

	// Check for rate limiting
	if geminiUsagePatterns.Limited.MatchString(output) {
		info.IsLimited = true
		found = true
	}

	return found, nil
}

// parseGeminiStatus parses Gemini status output
func parseGeminiStatus(info *QuotaInfo, output string) {
	// Parse account/email
	if match := geminiStatusPatterns.Account.FindStringSubmatch(output); len(match) > 1 {
		info.AccountID = strings.TrimSpace(match[1])
	}

	// Parse project (use as organization)
	if match := geminiStatusPatterns.Project.FindStringSubmatch(output); len(match) > 1 {
		info.Organization = strings.TrimSpace(match[1])
	}
}
