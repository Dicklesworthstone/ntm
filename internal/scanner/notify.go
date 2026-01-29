package scanner

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assign "github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// NotifyScanResults sends scan results to relevant agents via Agent Mail.
func NotifyScanResults(ctx context.Context, result *ScanResult, projectKey string) error {
	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !client.IsAvailable() {
		return nil
	}

	// Ensure scanner identity is registered
	if err := ensureScannerRegistered(ctx, client, projectKey); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to register scanner agent: %v\n", err)
		// Continue anyway, maybe it exists?
	}

	// 1. Fetch active file reservations to target notifications
	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		// Log error but continue with summary
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch reservations: %v\n", err)
	}

	// Group findings by file
	findingsByFile := make(map[string][]Finding)
	for _, f := range result.Findings {
		findingsByFile[f.File] = append(findingsByFile[f.File], f)
	}

	// 2. Send targeted alerts to agents holding locks
	notifiedAgents := make(map[string]bool)
	matchedFindings := make(map[string]bool)

	assignmentMatches, assignmentErr := collectAssignmentMatches(projectKey, result.Findings)
	if assignmentErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load assignment store: %v\n", assignmentErr)
	}

	for agentName, items := range assignmentMatches {
		if len(items) == 0 {
			continue
		}
		msg := buildAssignmentMessage(items)
		subject := fmt.Sprintf("[Scan] %d issues in assigned files", len(items))
		_, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
			ProjectKey: projectKey,
			SenderName: "ntm_scanner",
			To:         []string{agentName},
			Subject:    subject,
			BodyMD:     msg,
			Importance: "high",
		})
		if err == nil {
			notifiedAgents[agentName] = true
			for _, item := range items {
				matchedFindings[FindingSignature(item.Finding)] = true
			}
		}
	}

	for _, res := range reservations {
		var relevantFindings []Finding

		// Check all files with findings against reservation pattern
		for file, findings := range findingsByFile {
			if matchAssignmentPattern(res.PathPattern, file) {
				for _, f := range findings {
					if matchedFindings[FindingSignature(f)] {
						continue
					}
					relevantFindings = append(relevantFindings, f)
				}
			}
		}

		if len(relevantFindings) > 0 {
			// Send targeted message
			msg := buildTargetedMessage(relevantFindings, res.PathPattern)
			_, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
				ProjectKey: projectKey,
				SenderName: "ntm_scanner",
				To:         []string{res.AgentName},
				Subject:    fmt.Sprintf("[Scan] %d issues in %s", len(relevantFindings), res.PathPattern),
				BodyMD:     msg,
				Importance: "high",
			})
			if err == nil {
				notifiedAgents[res.AgentName] = true
			}
		}
	}

	// 3. Send summary to all other registered agents (optional, maybe too noisy?)
	// Task says: "Scan Summary - After each scan, send digest to all active agents"
	// But we don't want to spam. Maybe only if critical issues found?

	if result.HasCritical() || result.HasWarning() {
		agents, err := client.ListProjectAgents(ctx, projectKey)
		if err == nil {
			var broadcastTo []string
			for _, a := range agents {
				// Don't send duplicate if already notified via targeted alert
				if !notifiedAgents[a.Name] && a.Name != "ntm_scanner" && a.Name != "HumanOverseer" {
					broadcastTo = append(broadcastTo, a.Name)
				}
			}

			if len(broadcastTo) > 0 {
				summaryMsg := buildSummaryMessage(result)
				_, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
					ProjectKey: projectKey,
					SenderName: "ntm_scanner",
					To:         broadcastTo,
					Subject:    fmt.Sprintf("[Scan] Summary: %d critical, %d warnings", result.Totals.Critical, result.Totals.Warning),
					BodyMD:     summaryMsg,
					Importance: "normal",
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to send scan summary: %v\n", err)
				}
			}
		}
	}

	return nil
}

type assignmentFinding struct {
	Assignment *assignment.Assignment
	Finding    Finding
}

func collectAssignmentMatches(projectKey string, findings []Finding) (map[string][]assignmentFinding, error) {
	sessionName := sessionFromProjectKey(projectKey)
	if sessionName == "" {
		return nil, nil
	}

	store, err := assignment.LoadStore(sessionName)
	if err != nil {
		return nil, err
	}

	assignments := store.ListActive()
	if len(assignments) == 0 {
		return nil, nil
	}

	sort.Slice(assignments, func(i, j int) bool {
		return assignments[i].BeadID < assignments[j].BeadID
	})

	patternsByBead := make(map[string][]string, len(assignments))
	for _, a := range assignments {
		if a.AgentName == "" {
			continue
		}
		paths := assign.ExtractFilePaths(a.BeadTitle, a.PromptSent)
		if len(paths) == 0 {
			continue
		}
		patternsByBead[a.BeadID] = paths
	}

	if len(patternsByBead) == 0 {
		return nil, nil
	}

	matches := make(map[string][]assignmentFinding)
	seen := make(map[string]bool)

	for _, f := range findings {
		sig := FindingSignature(f)
		if seen[sig] {
			continue
		}
		for _, a := range assignments {
			if a.AgentName == "" {
				continue
			}
			patterns := patternsByBead[a.BeadID]
			if len(patterns) == 0 {
				continue
			}
			if matchesAnyPattern(patterns, f.File) {
				matches[a.AgentName] = append(matches[a.AgentName], assignmentFinding{
					Assignment: a,
					Finding:    f,
				})
				seen[sig] = true
				break
			}
		}
	}

	if len(matches) == 0 {
		return nil, nil
	}
	return matches, nil
}

func matchesAnyPattern(patterns []string, file string) bool {
	if file == "" {
		return false
	}
	for _, pattern := range patterns {
		if matchAssignmentPattern(pattern, file) {
			return true
		}
	}
	return false
}

func sessionFromProjectKey(projectKey string) string {
	if projectKey == "" {
		return ""
	}
	cleaned := filepath.Clean(projectKey)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return ""
	}
	return filepath.Base(cleaned)
}

func matchAssignmentPattern(pattern, file string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	pattern = filepath.ToSlash(strings.TrimPrefix(pattern, "./"))
	file = filepath.ToSlash(strings.TrimPrefix(file, "./"))

	if !strings.Contains(pattern, "/") {
		return matchSegment(pattern, path.Base(file))
	}

	return matchPatternSegments(splitPathSegments(pattern), splitPathSegments(file))
}

func splitPathSegments(value string) []string {
	parts := strings.Split(value, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func matchPatternSegments(patternSegs, fileSegs []string) bool {
	if len(patternSegs) == 0 {
		return len(fileSegs) == 0
	}

	if patternSegs[0] == "**" {
		for i := 0; i <= len(fileSegs); i++ {
			if matchPatternSegments(patternSegs[1:], fileSegs[i:]) {
				return true
			}
		}
		return false
	}

	if len(fileSegs) == 0 {
		return false
	}

	if !matchSegment(patternSegs[0], fileSegs[0]) {
		return false
	}

	return matchPatternSegments(patternSegs[1:], fileSegs[1:])
}

func matchSegment(pattern, segment string) bool {
	matched, err := filepath.Match(pattern, segment)
	if err != nil {
		return false
	}
	return matched
}

func buildAssignmentMessage(items []assignmentFinding) string {
	if len(items) == 0 {
		return "No matching findings."
	}

	byBead := make(map[string][]Finding)
	beadTitles := make(map[string]string)
	for _, item := range items {
		beadID := item.Assignment.BeadID
		byBead[beadID] = append(byBead[beadID], item.Finding)
		if beadTitles[beadID] == "" {
			beadTitles[beadID] = item.Assignment.BeadTitle
		}
	}

	beadIDs := make([]string, 0, len(byBead))
	for beadID := range byBead {
		beadIDs = append(beadIDs, beadID)
	}
	sort.Strings(beadIDs)

	var sb strings.Builder
	sb.WriteString("Found UBS issues in files tied to your assigned beads:\n\n")
	for _, beadID := range beadIDs {
		title := beadTitles[beadID]
		if title != "" {
			sb.WriteString(fmt.Sprintf("### %s — %s\n", beadID, title))
		} else {
			sb.WriteString(fmt.Sprintf("### %s\n", beadID))
		}
		findings := byBead[beadID]
		for i, f := range findings {
			if i >= 10 {
				sb.WriteString(fmt.Sprintf("\n...and %d more\n\n", len(findings)-10))
				break
			}
			icon := "⚠"
			if f.Severity == SeverityCritical {
				icon = "❌"
			}
			sb.WriteString(fmt.Sprintf("- %s **%s**: %s (`%s:%d`)\n", icon, f.RuleID, f.Message, f.File, f.Line))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Please review and fix these issues.")
	return sb.String()
}

func buildTargetedMessage(findings []Finding, pattern string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d issues in files matching `%s`:\n\n", len(findings), pattern))

	for i, f := range findings {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("\n...and %d more\n", len(findings)-10))
			break
		}
		icon := "⚠"
		if f.Severity == SeverityCritical {
			icon = "❌"
		}
		sb.WriteString(fmt.Sprintf("- %s **%s**: %s (`%s:%d`)\n", icon, f.RuleID, f.Message, f.File, f.Line))
	}

	sb.WriteString("Please review and fix these issues.")
	return sb.String()
}

func buildSummaryMessage(result *ScanResult) string {
	var sb strings.Builder
	sb.WriteString("## Scan Summary\n\n")
	sb.WriteString(fmt.Sprintf("- **Critical**: %d\n", result.Totals.Critical))
	sb.WriteString(fmt.Sprintf("- **Warnings**: %d\n", result.Totals.Warning))
	sb.WriteString(fmt.Sprintf("- **Info**: %d\n", result.Totals.Info))
	sb.WriteString(fmt.Sprintf("- **Files**: %d\n\n", result.Totals.Files))
	if len(result.Findings) > 0 {
		sb.WriteString("## Top Issues\n\n")
		// Show top 5 critical/warning
		count := 0
		for _, f := range result.Findings {
			if count >= 5 {
				break
			}
			if f.Severity == SeverityCritical || f.Severity == SeverityWarning {
				icon := "⚠"
				if f.Severity == SeverityCritical {
					icon = "❌"
				}
				sb.WriteString(fmt.Sprintf("- %s %s: %s (`%s`)\n", icon, f.RuleID, f.Message, f.File))
				count++
			}
		}
	}

	return sb.String()
}

func ensureScannerRegistered(ctx context.Context, client *agentmail.Client, projectKey string) error {
	_, err := client.RegisterAgent(ctx, agentmail.RegisterAgentOptions{
		ProjectKey:      projectKey,
		Name:            "ntm_scanner",
		Program:         "ntm",
		Model:           "scanner",
		TaskDescription: "Automated vulnerability scanner",
	})
	return err
}
