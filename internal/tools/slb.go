package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SLBAdapter provides integration with the Simultaneous Launch Button (slb) tool
type SLBAdapter struct {
	*BaseAdapter
	// ProjectDir pins request/status operations to their original project.
	// Empty retains the invoking process's working directory.
	ProjectDir string
}

// NewSLBAdapter creates a new SLB adapter
func NewSLBAdapter() *SLBAdapter {
	return &SLBAdapter{
		BaseAdapter: NewBaseAdapter(ToolSLB, "slb"),
	}
}

// Detect checks if slb is installed
func (a *SLBAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed slb version
func (a *SLBAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	// slb exposes version via the `version` SUBCOMMAND, not a `--version` flag.
	// `slb --version` errors with "unknown flag: --version" (exit 1) on current
	// releases, which previously made both `ntm doctor`'s version display AND its
	// health check fail for a perfectly healthy slb (issue #202).
	cmd := exec.CommandContext(ctx, a.BinaryName(), "version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get slb version: %w", err)
	}

	return parseSLBVersion(stdout.String())
}

// parseSLBVersion extracts version from slb --version output
// Format:
//
//	slb 0.1.0
//	  commit:  cc17518fe7d699363f4bcb48670ed4a3bbc71127
//	  built:   2025-12-25T03:35:46Z
//	  go:      go1.24.11
//	  config:  /home/ubuntu/.slb/config.toml
//	  db:      /data/projects/ntm/.slb/state.db
//	  project: /data/projects/ntm
func parseSLBVersion(output string) (Version, error) {
	output = strings.TrimSpace(output)

	// Get first line: "slb 0.1.0"
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return Version{Raw: output}, nil
	}

	firstLine := strings.TrimSpace(lines[0])

	// Extract version from "slb X.Y.Z"
	parts := strings.Fields(firstLine)
	if len(parts) < 2 {
		return Version{Raw: output}, nil
	}

	versionPart := parts[1]

	// Use the shared version regex from adapter.go
	matches := VersionRegex.FindStringSubmatch(versionPart)
	if len(matches) < 4 {
		return Version{Raw: output}, nil
	}

	var major, minor, patch int
	fmt.Sscanf(matches[1], "%d", &major)
	fmt.Sscanf(matches[2], "%d", &minor)
	fmt.Sscanf(matches[3], "%d", &patch)

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   output,
	}, nil
}

// Capabilities returns the list of slb capabilities
func (a *SLBAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{
		CapRobotMode,  // slb supports --json output
		CapDaemonMode, // slb can run as daemon (slb daemon)
		"request",     // slb request <command>
		"approve",     // slb approve <request-id>
		"reject",      // slb reject <request-id>
		"status",      // slb status
		"pending",     // slb pending
	}

	return caps, nil
}

// Health checks if slb is functioning correctly
func (a *SLBAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "slb not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check (fast)
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("slb at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "slb is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// Info returns complete slb tool information
func (a *SLBAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// SLB-specific methods

// SLBSession is an slb agent session, as printed by
// `slb session start --agent NAME --program PROG --model MODEL --json`.
// Requests are filed under a session; reviews are signed with its key, and slb
// refuses a review from the session that filed the request.
type SLBSession struct {
	ID  string
	Key string
}

// SLBSessionFromEnv reads the caller's session from SLB_SESSION_ID and
// SLB_SESSION_KEY, the variables slb itself uses.
func SLBSessionFromEnv() SLBSession {
	return SLBSession{
		ID:  strings.TrimSpace(os.Getenv("SLB_SESSION_ID")),
		Key: strings.TrimSpace(os.Getenv("SLB_SESSION_KEY")),
	}
}

// ErrSLBSessionRequired is returned when an slb call needs a session the caller
// has not provided.
var ErrSLBSessionRequired = errors.New("slb session required: start one with `slb session start --agent NAME --program PROG --model MODEL --json` and export its session_id and session_key as SLB_SESSION_ID and SLB_SESSION_KEY")

// Pending returns list of pending approval requests
func (a *SLBAdapter) Pending(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "pending", "--json")
}

// Request files an approval request for command under session; the response
// carries its request_id.
func (a *SLBAdapter) Request(ctx context.Context, session SLBSession, command, reason string) (json.RawMessage, error) {
	if session.ID == "" {
		return nil, ErrSLBSessionRequired
	}
	args := []string{"request", "--json", "--session-id", session.ID}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	return a.runCommand(ctx, append(args, "--", command)...)
}

// SLBRequestStatus is the part of `slb status <request-id> --json` ntm acts on.
type SLBRequestStatus struct {
	RequestID string      `json:"request_id"`
	Status    string      `json:"status"` // pending, approved, rejected, ...
	Reviews   []SLBReview `json:"reviews"`
}

// SLBReview is one reviewer's decision on a request.
type SLBReview struct {
	Reviewer string `json:"reviewer"`
	Decision string `json:"decision"` // approve, reject
	Comments string `json:"comments"`
}

// RequestStatus reports where an slb request stands and who reviewed it. slb
// resolves the request in the project of the working directory.
func (a *SLBAdapter) RequestStatus(ctx context.Context, requestID string) (*SLBRequestStatus, error) {
	raw, err := a.runCommand(ctx, "status", "--json", "--", requestID)
	if err != nil {
		return nil, err
	}
	var status SLBRequestStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("%w: slb status: %v", ErrSchemaValidation, err)
	}
	return &status, nil
}

// Approve records an approving review of requestID signed by session.
func (a *SLBAdapter) Approve(ctx context.Context, session SLBSession, requestID string) (json.RawMessage, error) {
	if session.ID == "" || session.Key == "" {
		return nil, ErrSLBSessionRequired
	}
	return a.runCommand(ctx, "approve", "--json", "--session-id", session.ID, "--session-key", session.Key, "--", requestID)
}

// Reject records a rejecting review of requestID signed by session; slb
// requires a reason.
func (a *SLBAdapter) Reject(ctx context.Context, session SLBSession, requestID, reason string) (json.RawMessage, error) {
	if session.ID == "" || session.Key == "" {
		return nil, ErrSLBSessionRequired
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("slb reject requires a reason")
	}
	return a.runCommand(ctx, "reject", "--json", "--session-id", session.ID, "--session-key", session.Key, "--reason", reason, "--", requestID)
}

// runCommand executes an slb command and returns raw JSON
func (a *SLBAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.Dir = a.ProjectDir
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if strings.Contains(err.Error(), ErrOutputLimitExceeded.Error()) {
			return nil, fmt.Errorf("slb output exceeded 10MB limit")
		}
		// Name only the subcommand: approve/reject args carry the session key.
		return nil, fmt.Errorf("slb %s failed: %w: %s", args[0], err, cliErrorLine(stderr.String()))
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from slb", ErrSchemaValidation)
	}

	return output, nil
}
