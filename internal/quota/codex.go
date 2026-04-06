package quota

// Codex quota parsing — heuristic patterns, not validated against real CLI output.
// These parsers attempt best-effort extraction but may not match actual Codex CLI formats.
// Returns found=false when no patterns match, which callers should treat as "unknown" not "zero".

import (
	"regexp"
	"strconv"
	"strings"
)

var codexUsagePatterns = struct {
	// Usage patterns (to be refined after research)
	Usage   *regexp.Regexp
	Limit   *regexp.Regexp
	Limited *regexp.Regexp
}{
	Usage:   regexp.MustCompile(`(?i)usage[:\s]+(\d+(?:\.\d+)?)\s*%`),
	Limit:   regexp.MustCompile(`(?i)limit[:\s]+(\d+(?:\.\d+)?)\s*%`),
	Limited: regexp.MustCompile(`(?i)(?:rate\s*limit|limited|exceeded|quota)`),
}

var codexStatusPatterns = struct {
	Account *regexp.Regexp
	Org     *regexp.Regexp
}{
	Account: regexp.MustCompile(`(?i)(?:account|user)[:\s]+(\S+)`),
	Org:     regexp.MustCompile(`(?i)(?:organization|org|workspace)[:\s]+(.+?)(?:\n|$)`),
}

// parseCodexUsage parses Codex usage output
func parseCodexUsage(info *QuotaInfo, output string) (bool, error) {
	found := false

	// Parse usage percentage
	if match := codexUsagePatterns.Usage.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.SessionUsage = val
			found = true
		}
	}

	// Parse limit percentage
	if match := codexUsagePatterns.Limit.FindStringSubmatch(output); len(match) > 1 {
		if val, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.WeeklyUsage = val
			found = true
		}
	}

	// Check for rate limiting
	if codexUsagePatterns.Limited.MatchString(output) {
		info.IsLimited = true
		found = true
	}

	return found, nil
}

// parseCodexStatus parses Codex status output
func parseCodexStatus(info *QuotaInfo, output string) {
	// Parse account
	if match := codexStatusPatterns.Account.FindStringSubmatch(output); len(match) > 1 {
		info.AccountID = strings.TrimSpace(match[1])
	}

	// Parse organization
	if match := codexStatusPatterns.Org.FindStringSubmatch(output); len(match) > 1 {
		info.Organization = strings.TrimSpace(match[1])
	}
}
