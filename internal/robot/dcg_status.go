package robot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// DCGStatusOutput represents the response from --robot-dcg-status
type DCGStatusOutput struct {
	RobotResponse
	DCG DCGStatus `json:"dcg"`
}

// DCGStatus contains DCG status information
type DCGStatus struct {
	Enabled    bool            `json:"enabled"`
	Available  bool            `json:"available"`
	Version    string          `json:"version,omitempty"`
	BinaryPath string          `json:"binary_path,omitempty"`
	Config     DCGConfigStatus `json:"config"`
	Stats      DCGStatsStatus  `json:"stats"`
}

// DCGConfigStatus contains DCG configuration information
type DCGConfigStatus struct {
	AuditLog             string `json:"audit_log,omitempty"`
	AllowOverride        bool   `json:"allow_override"`
	CustomBlocklistCount int    `json:"custom_blocklist_count"`
	CustomWhitelistCount int    `json:"custom_whitelist_count"`
}

// DCGStatsStatus contains DCG runtime statistics
type DCGStatsStatus struct {
	CommandsChecked int                 `json:"commands_checked"`
	CommandsBlocked int                 `json:"commands_blocked"`
	LastBlocked     *LastBlockedCommand `json:"last_blocked,omitempty"`
}

// LastBlockedCommand contains information about the most recently blocked command
type LastBlockedCommand struct {
	Command   string `json:"command"`
	Timestamp string `json:"timestamp"`
	Pane      string `json:"pane"`
}

// GetDCGStatus returns DCG status information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDCGStatus() (*DCGStatusOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewDCGAdapter()

	// Check DCG availability
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		return &DCGStatusOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to check DCG availability"),
			DCG: DCGStatus{
				Enabled:   false,
				Available: false,
			},
		}, nil
	}

	// Build status
	status := DCGStatus{
		Enabled:    availability.Available && availability.Compatible,
		Available:  availability.Available,
		BinaryPath: availability.Path,
	}

	if availability.Version.Major > 0 || availability.Version.Minor > 0 || availability.Version.Patch > 0 {
		status.Version = availability.Version.String()
	}

	// Get audit log stats
	stats := DCGStatsStatus{}
	auditLogPath := getDefaultAuditLogPath()

	// Try to read audit log for stats
	if auditLogPath != "" {
		blockedCount, lastBlocked := readAuditLogStats(auditLogPath)
		stats.CommandsBlocked = blockedCount
		stats.LastBlocked = lastBlocked
	}

	status.Config = DCGConfigStatus{
		AuditLog:             auditLogPath,
		AllowOverride:        false, // Default
		CustomBlocklistCount: 0,
		CustomWhitelistCount: 0,
	}
	status.Stats = stats

	return &DCGStatusOutput{
		RobotResponse: NewRobotResponse(true),
		DCG:           status,
	}, nil
}

// PrintDCGStatus handles the --robot-dcg-status command.
// This is a thin wrapper around GetDCGStatus() for CLI output.
func PrintDCGStatus() error {
	output, err := GetDCGStatus()
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// DCGCheckOutput represents the response from --robot-dcg-check / --robot-guard.
type DCGCheckOutput struct {
	RobotResponse
	Command     string          `json:"command"`
	Context     string          `json:"context,omitempty"`
	CWD         string          `json:"cwd,omitempty"`
	Allowed     bool            `json:"allowed"`
	Severity    string          `json:"severity,omitempty"`
	Rationale   string          `json:"rationale,omitempty"`
	Suggestion  string          `json:"suggestion,omitempty"`
	RuleMatched string          `json:"rule_matched,omitempty"`
	DCGVersion  string          `json:"dcg_version,omitempty"`
	BinaryPath  string          `json:"binary_path,omitempty"`
	AgentHints  *DCGAgentHints  `json:"_agent_hints,omitempty"`

	// Deprecated: use Rationale instead (kept for backwards compatibility)
	Reason string `json:"reason,omitempty"`
}

// DCGAgentHints contains hints for AI agents processing blocked commands.
type DCGAgentHints struct {
	SaferAlternative     string `json:"safer_alternative,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

// DCGCheckOptions contains options for the DCG check operation.
type DCGCheckOptions struct {
	Command string // Required: command to check
	Context string // Optional: intent/context for the command
	CWD     string // Optional: working directory context
	Verbose bool   // Optional: include detailed analysis
}

// GetDCGCheck preflights an arbitrary shell command via DCG (no execution).
// Deprecated: use GetDCGCheckWithOptions for full functionality.
func GetDCGCheck(command string) (*DCGCheckOutput, error) {
	return GetDCGCheckWithOptions(DCGCheckOptions{Command: command})
}

// GetDCGCheckWithOptions preflights an arbitrary shell command via DCG (no execution).
// It accepts extended options for context, working directory, and verbose mode.
func GetDCGCheckWithOptions(opts DCGCheckOptions) (*DCGCheckOutput, error) {
	meta, finish := StartResponseMeta("robot-dcg-check")
	defer finish()

	command := strings.TrimSpace(opts.Command)
	if command == "" {
		output := &DCGCheckOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodeInvalidFlag,
				"Provide a command to check: ntm --robot-dcg-check --command='rm -rf /tmp'",
			),
			Command: command,
			Context: opts.Context,
			Allowed: false,
		}
		output.RobotResponse.Meta = meta.WithExitCode(1)
		return output, nil
	}

	// Determine working directory
	cwd := opts.CWD
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewDCGAdapter()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		output := &DCGCheckOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to check DCG availability"),
			Command:       command,
			Context:       opts.Context,
			CWD:           cwd,
			Allowed:       false,
		}
		output.RobotResponse.Meta = meta.WithExitCode(1)
		return output, nil
	}

	if availability == nil || !availability.Available || !availability.Compatible {
		err := fmt.Errorf("dcg not installed or incompatible")
		hint := "Install dcg to enable command preflight. Run: ntm --robot-dcg-status"
		if availability != nil && availability.Available && !availability.Compatible {
			hint = "Upgrade dcg to a compatible version. Run: ntm --robot-dcg-status"
		}

		output := &DCGCheckOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeDependencyMissing, hint),
			Command:       command,
			Context:       opts.Context,
			CWD:           cwd,
			Allowed:       false,
			BinaryPath:    "",
		}
		output.RobotResponse.Meta = meta.WithExitCode(2)
		if availability != nil {
			output.BinaryPath = availability.Path
			if availability.Version.Major > 0 || availability.Version.Minor > 0 || availability.Version.Patch > 0 {
				output.DCGVersion = availability.Version.String()
			}
		}
		return output, nil
	}

	output := &DCGCheckOutput{
		RobotResponse: NewRobotResponseWithMeta(true, meta.WithExitCode(0)),
		Command:       command,
		Context:       opts.Context,
		CWD:           cwd,
		Allowed:       false,
		BinaryPath:    availability.Path,
	}
	if availability.Version.Major > 0 || availability.Version.Minor > 0 || availability.Version.Patch > 0 {
		output.DCGVersion = availability.Version.String()
	}

	// Use extended check if context or cwd is provided
	checkResult, err := adapter.CheckCommandExtended(ctx, command, opts.Context, cwd)
	if err != nil {
		errCode := ErrCodeInternalError
		hint := "Check dcg installation and try again"
		if errors.Is(err, tools.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			errCode = ErrCodeTimeout
			hint = "Try again later or reduce dcg timeout"
		}
		output.RobotResponse = NewErrorResponse(err, errCode, hint)
		output.RobotResponse.Meta = meta.WithExitCode(1)
		return output, nil
	}

	if checkResult != nil && checkResult.Blocked {
		output.Allowed = false
		output.Rationale = checkResult.Reason
		output.Reason = checkResult.Reason // Backwards compatibility
		output.Severity = checkResult.Severity
		output.RuleMatched = checkResult.RuleMatched
		output.Suggestion = checkResult.Suggestion

		// Add agent hints if available
		if checkResult.SaferAlternative != "" || checkResult.Severity == "critical" || checkResult.Severity == "high" {
			output.AgentHints = &DCGAgentHints{
				SaferAlternative:     checkResult.SaferAlternative,
				RequiresConfirmation: checkResult.Severity == "critical" || checkResult.Severity == "high",
			}
		}
		return output, nil
	}

	output.Allowed = true
	output.Severity = "safe"
	output.Reason = "allowed" // Backwards compatibility
	output.Rationale = "Command passed DCG checks"
	return output, nil
}

// PrintDCGCheck handles the --robot-dcg-check / --robot-guard command.
// Deprecated: use PrintDCGCheckWithOptions for full functionality.
func PrintDCGCheck(command string) error {
	return PrintDCGCheckWithOptions(DCGCheckOptions{Command: command})
}

// PrintDCGCheckWithOptions handles the --robot-dcg-check command with extended options.
func PrintDCGCheckWithOptions(opts DCGCheckOptions) error {
	output, err := GetDCGCheckWithOptions(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// getDefaultAuditLogPath returns the default audit log path
func getDefaultAuditLogPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".local", "share", "ntm", "dcg-audit.jsonl")
}

// readAuditLogStats reads the audit log and returns statistics
func readAuditLogStats(logPath string) (int, *LastBlockedCommand) {
	file, err := os.Open(logPath)
	if err != nil {
		return 0, nil
	}
	defer file.Close()

	var count int
	var lastEntry *dcg.AuditEntry

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry dcg.AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Event == "command_blocked" {
			count++
			lastEntry = &entry
		}
	}

	if lastEntry == nil {
		return count, nil
	}

	return count, &LastBlockedCommand{
		Command:   lastEntry.Command,
		Timestamp: lastEntry.Timestamp,
		Pane:      lastEntry.Pane,
	}
}
