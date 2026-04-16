package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Pre-compiled regexes for performance
var (
	// ansiRegex matches common ANSI escape sequences:
	// 1. CSI sequences: \x1b[ ... [a-zA-Z]
	// 2. OSC sequences: \x1b] ... \a or \x1b\
	ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\a\x1b]*(\a|\x1b\\)`)

	errorPatterns = []struct {
		pattern  *regexp.Regexp
		severity Severity
		msg      string
	}{
		{regexp.MustCompile(`(?i)error:`), SeverityError, "Error detected in agent output"},
		{regexp.MustCompile(`(?i)fatal:`), SeverityCritical, "Fatal error in agent"},
		{regexp.MustCompile(`(?i)panic:`), SeverityCritical, "Panic in agent"},
		{regexp.MustCompile(`(?i)failed:`), SeverityWarning, "Operation failed in agent"},
		{regexp.MustCompile(`(?i)exception`), SeverityError, "Exception in agent"},
		{regexp.MustCompile(`(?i)traceback`), SeverityError, "Exception traceback detected"},
		{regexp.MustCompile(`(?i)permission denied`), SeverityError, "Permission denied error"},
		{regexp.MustCompile(`(?i)connection refused`), SeverityWarning, "Connection refused"},
		{regexp.MustCompile(`(?i)timeout`), SeverityWarning, "Timeout detected"},
	}

	rateLimitPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)rate.?limit`),
		regexp.MustCompile(`(?i)too many requests`),
		regexp.MustCompile(`(?i)429`),
		regexp.MustCompile(`(?i)quota exceeded`),
		regexp.MustCompile(`(?i)throttl`),
	}
)

// Generator creates alerts from system state analysis
type Generator struct {
	config Config
}

// NewGenerator creates a new alert generator with the given config
func NewGenerator(cfg Config) *Generator {
	return &Generator{config: cfg}
}

// GenerateAll analyzes the current system state and returns all detected alerts
// plus a list of sources that failed to check (to prevent false resolution).
func (g *Generator) GenerateAll() ([]Alert, []string) {
	if !g.config.Enabled {
		return nil, nil
	}

	var alerts []Alert
	var failed []string

	// Check agent states
	if agentAlerts, failedSources, err := g.checkAgentStates(); err != nil {
		failed = append(failed, "agents")
	} else {
		alerts = append(alerts, agentAlerts...)
		if len(failedSources) > 0 {
			failed = append(failed, failedSources...)
		}
	}

	// Check disk space
	if alert, err := g.checkDiskSpace(); err != nil {
		failed = append(failed, "disk")
	} else if alert != nil {
		alerts = append(alerts, *alert)
	}

	// Check bead state
	if beadAlerts, err := g.checkBeadState(); err != nil {
		failed = append(failed, "beads")
	} else {
		alerts = append(alerts, beadAlerts...)
	}

	return alerts, failed
}

// checkAgentStates analyzes tmux panes for stuck, crashed, or error states
func (g *Generator) checkAgentStates() ([]Alert, []string, error) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return nil, nil, err
	}

	alerts, failedSources := g.scanAgentSessions(sessions, tmux.GetPanes, tmux.CapturePaneOutput)
	return alerts, failedSources, nil
}

func (g *Generator) scanAgentSessions(
	sessions []tmux.Session,
	getPanes func(string) ([]tmux.Pane, error),
	capturePaneOutput func(string, int) (string, error),
) ([]Alert, []string) {
	var alerts []Alert
	failedSources := make([]string, 0)
	failedSet := make(map[string]bool)

	for _, sess := range sessions {
		// Filter by session if configured
		if g.config.SessionFilter != "" && sess.Name != g.config.SessionFilter {
			continue
		}

		panes, err := getPanes(sess.Name)
		if err != nil {
			source := agentAlertSource(sess.Name)
			if !failedSet[source] {
				failedSet[source] = true
				failedSources = append(failedSources, source)
			}
			slog.Warn("failed to list panes during alert generation", "session", sess.Name, "error", err)
			continue
		}

		for _, pane := range panes {
			// Capture pane output for analysis
			output, err := capturePaneOutput(pane.ID, 50)
			if err != nil {
				// If we can't capture, the pane may have crashed
				alerts = append(alerts, Alert{
					ID:         generateAlertID(AlertAgentCrashed, sess.Name, pane.ID),
					Type:       AlertAgentCrashed,
					Severity:   SeverityError,
					Source:     agentAlertSource(sess.Name),
					Message:    fmt.Sprintf("Cannot capture output from pane %s (may have crashed)", pane.ID),
					Session:    sess.Name,
					Pane:       pane.ID,
					CreatedAt:  time.Now(),
					LastSeenAt: time.Now(),
					Count:      1,
				})
				continue
			}

			// Strip ANSI and analyze
			cleanOutput := stripANSI(output)
			lines := strings.Split(cleanOutput, "\n")

			// Check for error patterns
			if alert := g.detectErrorState(sess.Name, pane, lines); alert != nil {
				alerts = append(alerts, *alert)
			}

			// Check for rate limiting
			if alert := g.detectRateLimit(sess.Name, pane, lines); alert != nil {
				alerts = append(alerts, *alert)
			}
		}
	}

	return alerts, failedSources
}

func agentAlertSource(session string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return "agents"
	}
	return "agents:" + session
}

// detectErrorState checks pane output for error patterns
func (g *Generator) detectErrorState(session string, pane tmux.Pane, lines []string) *Alert {
	// Check last N lines for patterns — return the highest-severity match
	checkLines := lines
	if len(checkLines) > 20 {
		checkLines = checkLines[len(checkLines)-20:]
	}

	var best *Alert
	bestRank := 0

	for _, line := range checkLines {
		for _, ep := range errorPatterns {
			if ep.pattern.MatchString(line) {
				rank := severityRank(ep.severity)
				if rank > bestRank {
					bestRank = rank
					best = &Alert{
						ID:         generateAlertID(AlertAgentError, session, pane.ID),
						Type:       AlertAgentError,
						Severity:   ep.severity,
						Source:     agentAlertSource(session),
						Message:    ep.msg,
						Session:    session,
						Pane:       pane.ID,
						Context:    map[string]interface{}{"matched_line": truncateString(line, 200)},
						CreatedAt:  time.Now(),
						LastSeenAt: time.Now(),
						Count:      1,
					}
				}
			}
		}
	}

	return best
}

// detectRateLimit checks for rate limiting patterns
func (g *Generator) detectRateLimit(session string, pane tmux.Pane, lines []string) *Alert {
	checkLines := lines
	if len(checkLines) > 20 {
		checkLines = checkLines[len(checkLines)-20:]
	}

	for _, line := range checkLines {
		for _, pattern := range rateLimitPatterns {
			if pattern.MatchString(line) {
				msg := "Rate limiting detected"
				guidance := ""

				// Codex-specific guidance (bd-3qoly)
				if string(pane.Type) == "cod" {
					msg = "Codex (OpenAI) rate limiting detected"
					guidance = "New cod launches are paused automatically. " +
						"Claude and Gemini agents continue unaffected. " +
						"Consider reducing concurrent Codex panes or waiting for quota reset. " +
						"Check throttle status with: ntm --robot-status"
				}

				ctx := map[string]interface{}{
					"matched_line": truncateString(line, 200),
					"agent_type":   string(pane.Type),
				}
				if guidance != "" {
					ctx["guidance"] = guidance
				}

				return &Alert{
					ID:         generateAlertID(AlertRateLimit, session, pane.ID),
					Type:       AlertRateLimit,
					Severity:   SeverityWarning,
					Source:     agentAlertSource(session),
					Message:    msg,
					Session:    session,
					Pane:       pane.ID,
					Context:    ctx,
					CreatedAt:  time.Now(),
					LastSeenAt: time.Now(),
					Count:      1,
				}
			}
		}
	}

	return nil
}

// checkDiskSpace is implemented in platform-specific files:
// - generator_unix.go for Unix systems
// - (stub implementation returns nil on unsupported platforms)

// checkBeadState analyzes beads for stale in-progress items and dependency cycles
func (g *Generator) checkBeadState() ([]Alert, error) {
	var alerts []Alert

	// Check for stale in-progress beads
	alerts = append(alerts, g.checkStaleBeads()...)

	// Check for dependency cycles (use bv if available)
	if alert := g.checkDependencyCycles(); alert != nil {
		alerts = append(alerts, *alert)
	}

	return alerts, nil
}

// checkStaleBeads finds in-progress beads that haven't been updated recently
func (g *Generator) checkStaleBeads() []Alert {
	var alerts []Alert

	wd := g.config.ProjectsDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			slog.Warn("checkStaleBeads: failed to get working directory", "error", err)
			return nil
		}
	}
	// Get all in-progress beads (limit 100)
	beads := bv.GetInProgressList(wd, 100)

	staleThreshold := time.Duration(g.config.BeadStaleHours) * time.Hour
	now := time.Now()

	for _, bead := range beads {
		if now.Sub(bead.UpdatedAt) > staleThreshold {
			alerts = append(alerts, Alert{
				ID:       generateAlertID(AlertBeadStale, "", bead.ID),
				Type:     AlertBeadStale,
				Severity: SeverityWarning,
				Source:   "beads",
				Message:  fmt.Sprintf("Bead %s has been in_progress for >%d hours without update", bead.ID, g.config.BeadStaleHours),
				BeadID:   bead.ID,
				Context: map[string]interface{}{
					"title":        bead.Title,
					"assignee":     bead.Assignee,
					"last_updated": bead.UpdatedAt.Format(time.RFC3339),
					"hours_since":  int(now.Sub(bead.UpdatedAt).Hours()),
				},
				CreatedAt:  time.Now(),
				LastSeenAt: time.Now(),
				Count:      1,
			})
		}
	}

	return alerts
}

// checkDependencyCycles uses bv to detect cycles in the dependency graph
func (g *Generator) checkDependencyCycles() *Alert {
	wd := g.config.ProjectsDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			slog.Warn("checkDependencyCycles: failed to get working directory", "error", err)
			return nil
		}
	}
	// Run bv --robot-insights and check for cycles
	insights, err := bv.GetInsights(wd)
	if err != nil {
		// Silently skip when bv is not installed; only warn on real errors.
		if !errors.Is(err, bv.ErrNotInstalled) && !strings.Contains(err.Error(), "executable file not found") {
			slog.Warn("failed to check dependency cycles", "tool", "bv", "error", err)
		}
		return nil
	}

	if len(insights.Cycles) > 0 {
		cycleNodes := make([]string, 0)
		for _, cycle := range insights.Cycles {
			cycleNodes = append(cycleNodes, strings.Join(cycle.Nodes, " -> "))
		}

		return &Alert{
			ID:       generateAlertID(AlertDependencyCycle, "", ""),
			Type:     AlertDependencyCycle,
			Severity: SeverityError,
			Source:   "beads",
			Message:  fmt.Sprintf("Dependency cycle detected: %d cycle(s) found", len(insights.Cycles)),
			Context: map[string]interface{}{
				"cycle_count": len(insights.Cycles),
				"cycles":      cycleNodes,
			},
			CreatedAt:  time.Now(),
			LastSeenAt: time.Now(),
			Count:      1,
		}
	}

	return nil
}

// generateAlertID creates a deterministic ID for deduplication
func generateAlertID(alertType AlertType, session, pane string) string {
	data := fmt.Sprintf("%s:%s:%s", alertType, session, pane)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8])
}

// stripANSI removes ANSI escape sequences from text
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// truncateString truncates a string to maxLen bytes with ellipsis, respecting UTF-8 boundaries.
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	// Find the last rune boundary that allows for "..." suffix within maxLen bytes.
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts fit within targetLen
	return s[:prevI] + "..."
}
