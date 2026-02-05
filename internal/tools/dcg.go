package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DCGAdapter provides integration with the Destructive Command Guard (dcg) tool.
// DCG blocks dangerous commands like rm -rf, git reset --hard, DROP DATABASE, etc.
// and provides safety guardrails for agent operations.
type DCGAdapter struct {
	*BaseAdapter
}

// NewDCGAdapter creates a new DCG adapter
func NewDCGAdapter() *DCGAdapter {
	return &DCGAdapter{
		BaseAdapter: NewBaseAdapter(ToolDCG, "dcg"),
	}
}

// Detect checks if dcg is installed
func (a *DCGAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed dcg version
func (a *DCGAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get dcg version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of dcg capabilities
func (a *DCGAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	// Check if dcg has specific capabilities by examining help output
	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "help")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

	// Check for known capabilities
	if strings.Contains(output, "--json") || strings.Contains(output, "robot") {
		caps = append(caps, CapRobotMode)
	}

	return caps, nil
}

// Health checks if dcg is functioning correctly
func (a *DCGAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "dcg not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("dcg at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "dcg is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if dcg has a specific capability
func (a *DCGAdapter) HasCapability(ctx context.Context, cap Capability) bool {
	caps, err := a.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Info returns complete dcg tool information
func (a *DCGAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// DCG-specific methods

// DCGAvailability represents the availability and compatibility of dcg on PATH.
// Available indicates the binary is found; Compatible indicates version meets minimum requirements.
type DCGAvailability struct {
	Available   bool      `json:"available"`
	Compatible  bool      `json:"compatible"`
	Version     Version   `json:"version,omitempty"`
	Path        string    `json:"path,omitempty"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

var (
	dcgAvailabilityCache  DCGAvailability
	dcgAvailabilityExpiry time.Time
	dcgAvailabilityMutex  sync.RWMutex
	dcgAvailabilityTTL    = 5 * time.Minute
	dcgMinVersion         = Version{Major: 0, Minor: 1, Patch: 0}
	dcgLogger             = slog.Default().With("component", "tools.dcg")
)

// BlockedCommand represents a command that was blocked by DCG
type BlockedCommand struct {
	Command   string `json:"command"`
	Reason    string `json:"reason"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ExtendedCheckResult represents the result of an extended DCG check with full details.
type ExtendedCheckResult struct {
	Command          string `json:"command"`
	Blocked          bool   `json:"blocked"`
	Reason           string `json:"reason,omitempty"`
	Severity         string `json:"severity,omitempty"`         // critical, high, medium, low, safe
	RuleMatched      string `json:"rule_matched,omitempty"`     // e.g., RECURSIVE_DELETE_ROOT
	Suggestion       string `json:"suggestion,omitempty"`       // e.g., "Use trash-cli instead"
	SaferAlternative string `json:"safer_alternative,omitempty"` // e.g., "trash-put /data/backup"
}

// DCGStatus represents the current DCG configuration status
type DCGStatus struct {
	Enabled          bool     `json:"enabled"`
	BlockedPatterns  []string `json:"blocked_patterns,omitempty"`
	AllowedOverrides []string `json:"allowed_overrides,omitempty"`
}

// GetStatus returns the current DCG status
func (a *DCGAdapter) GetStatus(ctx context.Context) (*DCGStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status", "--json")
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// DCG might not have a status command - return default
		return &DCGStatus{Enabled: true}, nil
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		// Return default status if output is not valid JSON
		return &DCGStatus{Enabled: true}, nil
	}

	var status DCGStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse dcg status: %w", err)
	}

	return &status, nil
}

// GetAvailability returns whether dcg is available and compatible, with caching.
// It logs a warning if dcg is missing or incompatible, but does not return an error.
func (a *DCGAdapter) GetAvailability(ctx context.Context) (*DCGAvailability, error) {
	dcgAvailabilityMutex.RLock()
	if time.Now().Before(dcgAvailabilityExpiry) {
		availability := dcgAvailabilityCache
		dcgAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	dcgAvailabilityMutex.RUnlock()

	availability := a.fetchAvailability(ctx)

	dcgAvailabilityMutex.Lock()
	dcgAvailabilityCache = *availability
	dcgAvailabilityExpiry = time.Now().Add(dcgAvailabilityTTL)
	dcgAvailabilityMutex.Unlock()

	return availability, nil
}

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *DCGAdapter) InvalidateAvailabilityCache() {
	dcgAvailabilityMutex.Lock()
	dcgAvailabilityExpiry = time.Time{}
	dcgAvailabilityMutex.Unlock()
}

// IsAvailable returns true if dcg is installed and compatible.
func (a *DCGAdapter) IsAvailable(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.Available && availability.Compatible
}

func (a *DCGAdapter) fetchAvailability(ctx context.Context) *DCGAvailability {
	availability := &DCGAvailability{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		availability.Error = err.Error()
		dcgLogger.Warn("dcg binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		dcgLogger.Warn("dcg version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !dcgCompatible(version) {
		dcgLogger.Warn("dcg version incompatible", "path", path, "version", version.String(), "min_version", dcgMinVersion.String())
		return availability
	}

	availability.Compatible = true
	return availability
}

func dcgCompatible(version Version) bool {
	return version.AtLeast(dcgMinVersion)
}

// CheckCommand checks if a command would be blocked by DCG
func (a *DCGAdapter) CheckCommand(ctx context.Context, command string) (*BlockedCommand, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "check", "--json", command)
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// Non-zero exit may indicate command is blocked
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 1 {
			// Command was blocked
			output := stdout.Bytes()
			if json.Valid(output) {
				var blocked BlockedCommand
				if err := json.Unmarshal(output, &blocked); err == nil {
					return &blocked, nil
				}
			}
			// Return basic blocked info
			return &BlockedCommand{
				Command: command,
				Reason:  "blocked by dcg",
			}, nil
		}
		return nil, fmt.Errorf("dcg check failed: %w: %s", err, stderr.String())
	}

	// Exit code 0 means command is allowed
	return nil, nil
}

// CheckCommandExtended checks a command with extended options and returns detailed results.
// This method passes context/intent and working directory to DCG for better decision making.
func (a *DCGAdapter) CheckCommandExtended(ctx context.Context, command, context_, cwd string) (*ExtendedCheckResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	// Build command arguments
	args := []string{"check", "--json"}
	if context_ != "" {
		args = append(args, "--context", context_)
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	args = append(args, command)

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Parse output regardless of exit code
	output := stdout.Bytes()
	result := &ExtendedCheckResult{
		Command: command,
		Blocked: false,
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}

		// Non-zero exit may indicate command is blocked
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 1 {
			result.Blocked = true

			// Try to parse extended JSON output
			if json.Valid(output) {
				var parsed struct {
					Command          string `json:"command"`
					Reason           string `json:"reason"`
					Severity         string `json:"severity"`
					RuleMatched      string `json:"rule_matched"`
					Suggestion       string `json:"suggestion"`
					SaferAlternative string `json:"safer_alternative"`
				}
				if jsonErr := json.Unmarshal(output, &parsed); jsonErr == nil {
					result.Reason = parsed.Reason
					result.Severity = parsed.Severity
					result.RuleMatched = parsed.RuleMatched
					result.Suggestion = parsed.Suggestion
					result.SaferAlternative = parsed.SaferAlternative
					return result, nil
				}
			}

			// Fallback: infer severity from command pattern
			result.Reason = "blocked by dcg"
			result.Severity = inferSeverity(command)
			result.RuleMatched = inferRuleCode(command)
			return result, nil
		}

		return nil, fmt.Errorf("dcg check failed: %w: %s", err, stderr.String())
	}

	// Exit code 0 means command is allowed
	result.Severity = "safe"
	return result, nil
}

// inferSeverity guesses severity based on command patterns when DCG doesn't provide it.
func inferSeverity(command string) string {
	cmd := strings.ToLower(command)

	// Critical patterns
	if strings.Contains(cmd, "rm -rf /") && !strings.Contains(cmd, "rm -rf ./") {
		return "critical"
	}
	if strings.Contains(cmd, "dd if=/dev/zero of=/dev/") || strings.Contains(cmd, "dd if=/dev/urandom of=/dev/") {
		return "critical"
	}
	if strings.Contains(cmd, "drop database") || strings.Contains(cmd, "drop table") {
		return "critical"
	}

	// High patterns
	if strings.Contains(cmd, "git reset --hard") {
		return "high"
	}
	if strings.Contains(cmd, "git push --force") || strings.Contains(cmd, "git push -f") {
		return "high"
	}
	if strings.Contains(cmd, "chmod -r 777") || strings.Contains(cmd, "chmod 777 -r") {
		return "high"
	}

	// Medium patterns
	if strings.Contains(cmd, "rm -r") || strings.Contains(cmd, "rm -rf") {
		return "medium"
	}
	if strings.Contains(cmd, "git stash drop") {
		return "medium"
	}

	// Low patterns
	if strings.Contains(cmd, "rm ") && !strings.Contains(cmd, "rm -r") {
		return "low"
	}

	return "medium" // Default for blocked commands
}

// inferRuleCode guesses rule code based on command patterns when DCG doesn't provide it.
func inferRuleCode(command string) string {
	cmd := strings.ToLower(command)

	if strings.Contains(cmd, "rm -rf /") && !strings.Contains(cmd, "rm -rf ./") {
		if strings.Contains(cmd, "rm -rf /*") || cmd == "rm -rf /" {
			return "RECURSIVE_DELETE_ROOT"
		}
		return "RECURSIVE_DELETE_OUTSIDE_PROJECT"
	}
	if strings.Contains(cmd, "git reset --hard") {
		return "HARD_RESET"
	}
	if (strings.Contains(cmd, "git push --force") || strings.Contains(cmd, "git push -f")) &&
		(strings.Contains(cmd, "main") || strings.Contains(cmd, "master")) {
		return "FORCE_PUSH_PROTECTED"
	}
	if strings.Contains(cmd, "drop database") {
		return "DROP_DATABASE"
	}
	if strings.Contains(cmd, "drop table") {
		return "DROP_TABLE"
	}
	if strings.Contains(cmd, "dd") && strings.Contains(cmd, "of=/dev/") {
		return "DISK_OVERWRITE"
	}
	if strings.Contains(cmd, "chmod") && strings.Contains(cmd, "777") && (strings.Contains(cmd, "-r") || strings.Contains(cmd, "-R")) {
		return "CHMOD_RECURSIVE_777"
	}

	return "BLOCKED_COMMAND"
}
