package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Pre-compiled regexes for performance
var (
	ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`) // Corrected escape sequence for backslash

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
func (g *Generator) GenerateAll() []Alert {
	if !g.config.Enabled {
		return nil
	}

	var alerts []Alert

	// Check agent states
	alerts = append(alerts, g.checkAgentStates()...)

	// Check disk space
	if alert := g.checkDiskSpace(); alert != nil {
		alerts = append(alerts, *alert)
	}

	// Check bead state
	alerts = append(alerts, g.checkBeadState()...)

	return alerts
}

// checkAgentStates analyzes tmux panes for stuck, crashed, or error states
func (g *Generator) checkAgentStates() []Alert {
	var alerts []Alert

	sessions, err := tmux.ListSessions()
	if err != nil {
		return alerts
	}

	for _, sess := range sessions {
		panes, err := tmux.GetPanes(sess.Name)
		if err != nil {
			continue
		}

		for _, pane := range panes {
			// Capture pane output for analysis
			output, err := tmux.CapturePaneOutput(pane.ID, 50)
			if err != nil {
				// If we can't capture, the pane may have crashed
				alerts = append(alerts, Alert{
					ID:        generateAlertID(AlertAgentCrashed, sess.Name, pane.ID),
					Type:      AlertAgentCrashed,
					Severity:  SeverityError,
					Message:   fmt.Sprintf("Cannot capture output from pane %s (may have crashed)", pane.ID),
					Session:   sess.Name,
					Pane:      pane.ID,
					CreatedAt: time.Now(),
					LastSeenAt: time.Now(),
					Count:     1,
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

	return alerts
}

// detectErrorState checks pane output for error patterns
func (g *Generator) detectErrorState(session string, pane tmux.Pane, lines []string) *Alert {
	// Check last N lines for patterns
	checkLines := lines
	if len(checkLines) > 20 {
		checkLines = checkLines[len(checkLines)-20:]
	}

	for _, line := range checkLines {
		for _, ep := range errorPatterns {
			if ep.pattern.MatchString(line) {
				return &Alert{
					ID:        generateAlertID(AlertAgentError, session, pane.ID),
					Type:      AlertAgentError,
					Severity:  ep.severity,
					Message:   ep.msg,
					Session:   session,
					Pane:      pane.ID,
					Context:   map[string]interface{}{"matched_line": truncateString(line, 200)},
					CreatedAt: time.Now(),
					LastSeenAt: time.Now(),
					Count:     1,
				}
			}
		}
	}

	return nil
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
				return &Alert{
					ID:        generateAlertID(AlertRateLimit, session, pane.ID),
					Type:      AlertRateLimit,
					Severity:  SeverityWarning,
					Message:   "Rate limiting detected",
					Session:   session,
					Pane:      pane.ID,
					Context:   map[string]interface{}{"matched_line": truncateString(line, 200)},
					CreatedAt: time.Now(),
					LastSeenAt: time.Now(),
					Count:     1,
				}
			}
		}
	}

	return nil
}

// checkDiskSpace verifies available disk space
func (g *Generator) checkDiskSpace() *Alert {
	var stat syscall.Statfs_t

	// Check space on project directory if configured, otherwise root
	checkPath := "/"
	if g.config.ProjectsDir != "" {
		checkPath = g.config.ProjectsDir
	}

	err := syscall.Statfs(checkPath, &stat)
	if err != nil {
		// If project dir check fails (e.g. doesn't exist), fallback to root
		if checkPath != "/" {
			err = syscall.Statfs("/", &stat)
		}
		if err != nil {
			return nil
		}
	}

	// Calculate free space in GB
	freeGB := float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)

	if freeGB < g.config.DiskLowThresholdGB {
		severity := SeverityWarning
		if freeGB < 1.0 {
			severity = SeverityCritical
		}

		return &Alert{
			ID:       generateAlertID(AlertDiskLow, "", ""),
			Type:     AlertDiskLow,
			Severity: severity,
			Message:  fmt.Sprintf("Low disk space: %.1f GB remaining on %s", freeGB, checkPath),
			Context: map[string]interface{}{
				"free_gb":      freeGB,
				"threshold_gb": g.config.DiskLowThresholdGB,
				"path":         checkPath,
			},
			CreatedAt:  time.Now(),
			LastSeenAt: time.Now(),
			Count:      1,
		}
	}

	return nil
}

// checkBeadState analyzes beads for stale in-progress items and dependency cycles
func (g *Generator) checkBeadState() []Alert {
	var alerts []Alert

	// Check for stale in-progress beads
	alerts = append(alerts, g.checkStaleBeads()...)

	// Check for dependency cycles (use bv if available)
	if alert := g.checkDependencyCycles(); alert != nil {
		alerts = append(alerts, *alert)
	}

	return alerts
}

// checkStaleBeads finds in-progress beads that haven't been updated recently
func (g *Generator) checkStaleBeads() []Alert {
	var alerts []Alert

	// Run bd list --status=in_progress --json
	cmd := exec.Command("bd", "list", "--status=in_progress", "--json")
	output, err := cmd.Output()
	if err != nil {
		return alerts
	}

	var beads []struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		UpdatedAt time.Time `json:"updated_at"`
		Assignee  string    `json:"assignee"`
	}
	if err := json.Unmarshal(output, &beads); err != nil {
		return alerts
	}

	staleThreshold := time.Duration(g.config.BeadStaleHours) * time.Hour
	now := time.Now()

	for _, bead := range beads {
		if now.Sub(bead.UpdatedAt) > staleThreshold {
			alerts = append(alerts, Alert{
				ID:       generateAlertID(AlertBeadStale, "", bead.ID),
				Type:     AlertBeadStale,
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("Bead %s has been in_progress for >%d hours without update", bead.ID, g.config.BeadStaleHours),
				BeadID:   bead.ID,
				Context: map[string]interface{}{
					"title":           bead.Title,
					"assignee":        bead.Assignee,
					"last_updated":    bead.UpdatedAt.Format(time.RFC3339),
					"hours_since":     int(now.Sub(bead.UpdatedAt).Hours()),
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
	// Run bv --robot-insights and check for cycles
	cmd := exec.Command("bv", "--robot-insights")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var insights struct {
		Cycles []struct {
			Nodes []string `json:"nodes"`
		} `json:"Cycles"`
	}
	if err := json.Unmarshal(output, &insights); err != nil {
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

// truncateString truncates a string to maxLen chars with ellipsis
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}