// Package scanner provides UBS integration including the beads bridge
// for automatic issue creation from scan findings.
package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// BeadPriority represents the priority level for beads issues.
type BeadPriority int

const (
	BeadPriorityP0 BeadPriority = 0 // Critical
	BeadPriorityP1 BeadPriority = 1 // Error/High
	BeadPriorityP2 BeadPriority = 2 // Warning/Medium
	BeadPriorityP3 BeadPriority = 3 // Info/Low
)

// BridgeConfig configures the UBS→Beads bridge.
type BridgeConfig struct {
	// MinSeverity is the minimum severity threshold for auto-creating beads.
	// Findings below this severity are ignored.
	MinSeverity Severity
	// DryRun if true, prints what would be created without actually creating.
	DryRun bool
	// Verbose enables detailed output.
	Verbose bool
}

// DefaultBridgeConfig returns sensible defaults for the bridge.
func DefaultBridgeConfig() BridgeConfig {
	return BridgeConfig{
		MinSeverity: SeverityWarning, // P2 and above by default
		DryRun:      false,
		Verbose:     false,
	}
}

// BridgeResult contains the results of a bridge operation.
type BridgeResult struct {
	Created    int      `json:"created"`
	Skipped    int      `json:"skipped"`    // Below severity threshold
	Duplicates int      `json:"duplicates"` // Already exists
	Errors     int      `json:"errors"`
	BeadIDs    []string `json:"bead_ids,omitempty"`
	Messages   []string `json:"messages,omitempty"`
}

// SeverityToPriority maps UBS severity levels to beads priority.
func SeverityToPriority(sev Severity) BeadPriority {
	switch sev {
	case SeverityCritical:
		return BeadPriorityP0
	case SeverityWarning:
		return BeadPriorityP1 // Map warning to P1 (higher than P2)
	case SeverityInfo:
		return BeadPriorityP3
	default:
		return BeadPriorityP2
	}
}

// SeverityMeetsTreshold returns true if sev meets or exceeds the threshold.
func SeverityMeetsThreshold(sev, threshold Severity) bool {
	severityOrder := map[Severity]int{
		SeverityCritical: 0,
		SeverityWarning:  1,
		SeverityInfo:     2,
	}
	return severityOrder[sev] <= severityOrder[threshold]
}

// FindingSignature generates a unique signature for a finding.
// Used for deduplication against existing beads.
func FindingSignature(f Finding) string {
	// Signature: file:line:rule_id OR file:line:category:message_hash
	key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.RuleID)
	if f.RuleID == "" {
		// Fall back to message hash if no rule ID
		msgHash := sha256.Sum256([]byte(f.Message))
		key = fmt.Sprintf("%s:%d:%s:%s", f.File, f.Line, f.Category, hex.EncodeToString(msgHash[:8]))
	}
	return key
}

// BeadFromFinding creates a bead create command args from a Finding.
func BeadFromFinding(f Finding, project string) BeadSpec {
	priority := SeverityToPriority(f.Severity)
	signature := FindingSignature(f)

	// Build title: [{severity}] {rule_id}: {message} in {file}
	title := fmt.Sprintf("[%s] ", strings.ToUpper(string(f.Severity)))
	if f.RuleID != "" {
		title += f.RuleID + ": "
	}
	title += truncateMessage(f.Message, 60)
	title += fmt.Sprintf(" in %s", shortenPath(f.File))

	// Build description with full context
	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("**File:** `%s:%d", f.File, f.Line))
	if f.Column > 0 {
		desc.WriteString(fmt.Sprintf(":%d", f.Column))
	}
	desc.WriteString("`\n\n")

	if f.RuleID != "" {
		desc.WriteString(fmt.Sprintf("**Rule:** `%s`", f.RuleID))
		if f.Category != "" {
			desc.WriteString(fmt.Sprintf(" (%s)", f.Category))
		}
		desc.WriteString("\n\n")
	} else if f.Category != "" {
		desc.WriteString(fmt.Sprintf("**Category:** %s\n\n", f.Category))
	}

	desc.WriteString(fmt.Sprintf("**Message:** %s\n\n", f.Message))

	if f.Suggestion != "" {
		desc.WriteString(fmt.Sprintf("**Suggested Fix:** %s\n\n", f.Suggestion))
	}

	// Embed a stable signature so future runs can deduplicate/close correctly.
	desc.WriteString(fmt.Sprintf("**Signature:** %s\n\n", signature))
	desc.WriteString(fmt.Sprintf("---\n*Auto-created by UBS scan at %s*", time.Now().Format(time.RFC3339)))

	return BeadSpec{
		Title:       title,
		Type:        "bug",
		Priority:    int(priority),
		Description: desc.String(),
		Labels:      []string{"ubs-scan", string(f.Severity)},
		Signature:   signature,
	}
}

// BeadSpec represents the specification for creating a bead.
type BeadSpec struct {
	Title       string
	Type        string
	Priority    int
	Description string
	Labels      []string
	Signature   string // For deduplication
}

// CreateBeadsFromFindings creates beads from scan findings.
func CreateBeadsFromFindings(result *ScanResult, cfg BridgeConfig) (*BridgeResult, error) {
	br := &BridgeResult{
		BeadIDs:  make([]string, 0),
		Messages: make([]string, 0),
	}

	// Load existing beads for deduplication
	existing, err := loadExistingSignatures()
	if err != nil {
		br.Messages = append(br.Messages, fmt.Sprintf("Warning: could not load existing beads for dedup: %v", err))
		existing = make(map[string]bool)
	}

	for _, f := range result.Findings {
		// Check severity threshold
		if !SeverityMeetsThreshold(f.Severity, cfg.MinSeverity) {
			br.Skipped++
			continue
		}

		spec := BeadFromFinding(f, result.Project)

		// Check for duplicates
		if existing[spec.Signature] {
			br.Duplicates++
			if cfg.Verbose {
				br.Messages = append(br.Messages, fmt.Sprintf("Skipped duplicate: %s", spec.Title))
			}
			continue
		}

		// Create the bead
		if cfg.DryRun {
			br.Messages = append(br.Messages, fmt.Sprintf("Would create: %s", spec.Title))
			br.Created++
			continue
		}

		beadID, err := createBead(spec)
		if err != nil {
			br.Errors++
			br.Messages = append(br.Messages, fmt.Sprintf("Error creating bead: %v", err))
			continue
		}

		br.Created++
		br.BeadIDs = append(br.BeadIDs, beadID)
		existing[spec.Signature] = true // Mark as existing for this run

		if cfg.Verbose {
			br.Messages = append(br.Messages, fmt.Sprintf("Created %s: %s", beadID, spec.Title))
		}
	}

	return br, nil
}

// createBead calls bd create with the given spec.
func createBead(spec BeadSpec) (string, error) {
	args := []string{
		"create",
		"--json",
		"--type", spec.Type,
		"--priority", fmt.Sprintf("%d", spec.Priority),
		"--description", spec.Description,
		"--title", spec.Title,
	}

	if len(spec.Labels) > 0 {
		args = append(args, "--labels", strings.Join(spec.Labels, ","))
	}

	output, err := bv.RunBd("", args...)
	if err != nil {
		return "", fmt.Errorf("bd create failed: %w", err)
	}

	// Parse JSON output to get the bead ID
	var result []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("parsing bd output: %w", err)
	}

	if len(result) == 0 {
		return "", fmt.Errorf("no bead ID returned")
	}

	return result[0].ID, nil
}

// loadExistingSignatures loads signatures of existing UBS-created beads.
func loadExistingSignatures() (map[string]bool, error) {
	// Query beads with ubs-scan label that are open
	output, err := bv.RunBd("", "list", "--json", "--labels=ubs-scan", "--status=open,in_progress")
	if err != nil {
		return nil, fmt.Errorf("listing beads: %w", err)
	}

	var beads []struct {
		Description string `json:"description"`
	}
	if output == "" {
		return make(map[string]bool), nil
	}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		// Empty list is fine
		return nil, fmt.Errorf("parsing beads: %w", err)
	}

	// Extract signatures from descriptions
	// We store signature as part of description: "---\n*Signature: ...*"
	// For simplicity, we'll use the title+file combo as a rough dedup key
	// More sophisticated: parse the File: line from description
	signatures := make(map[string]bool)
	for _, b := range beads {
		// Extract file:line from description if present
		sig := extractSignatureFromDesc(b.Description)
		if sig != "" {
			signatures[sig] = true
		}
	}

	return signatures, nil
}

// extractSignatureFromDesc extracts the file:line signature from a bead description.
func extractSignatureFromDesc(desc string) string {
	// Prefer explicit signature line
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "**Signature:**") {
			return strings.TrimSpace(strings.TrimPrefix(line, "**Signature:**"))
		}
	}

	// Fallback: look for "**File:** `path:line" pattern (legacy format)
	lines := strings.Split(desc, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "**File:**") {
			// Extract the path:line part
			start := strings.Index(line, "`")
			end := strings.LastIndex(line, "`")
			if start != -1 && end > start {
				fileLine := line[start+1 : end]
				// Add placeholder for rule since we don't have it
				return fileLine + ":ubs"
			}
		}
	}
	return ""
}

// truncateMessage truncates a message to maxLen, adding ellipsis if needed.
func truncateMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen-3] + "..."
}

// shortenPath returns the last N path components for readability.
func shortenPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return path
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// UpdateBeadsFromFindings closes beads for findings that no longer appear.
func UpdateBeadsFromFindings(result *ScanResult, cfg BridgeConfig) (*BridgeResult, error) {
	br := &BridgeResult{
		Messages: make([]string, 0),
	}

	// Get current findings signatures
	currentSigs := make(map[string]bool)
	for _, f := range result.Findings {
		currentSigs[FindingSignature(f)] = true
	}

	// Get existing UBS beads
	output, err := bv.RunBd("", "list", "--json", "--labels=ubs-scan", "--status=open,in_progress")
	if err != nil {
		return nil, fmt.Errorf("listing beads: %w", err)
	}

	var beads []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	if output == "" {
		return br, nil // No beads to update
	}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		return nil, fmt.Errorf("parsing beads: %w", err)
	}

	// Close beads whose findings no longer exist
	for _, b := range beads {
		sig := extractSignatureFromDesc(b.Description)
		if sig == "" {
			continue // Can't determine if fixed
		}

		if !currentSigs[sig] {
			// Finding no longer exists - issue is fixed
			if cfg.DryRun {
				br.Messages = append(br.Messages, fmt.Sprintf("Would close: %s (fixed)", b.ID))
				br.Created++ // Reuse Created for closed count
				continue
			}

			if err := closeBead(b.ID); err != nil {
				br.Errors++
				br.Messages = append(br.Messages, fmt.Sprintf("Error closing %s: %v", b.ID, err))
				continue
			}

			br.Created++ // Reuse Created for closed count
			if cfg.Verbose {
				br.Messages = append(br.Messages, fmt.Sprintf("Closed %s (fixed)", b.ID))
			}
		}
	}

	return br, nil
}

// closeBead closes a bead by ID.
func closeBead(id string) error {
	_, err := bv.RunBd("", "close", id)
	if err != nil {
		return fmt.Errorf("bd close failed: %w", err)
	}
	return nil
}
