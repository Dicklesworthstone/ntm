package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
)

// CMAdapter provides integration with the CASS Memory (cm) tool
type CMAdapter struct {
	*BaseAdapter
	client     *cm.Client
	serverPort int
}

// NewCMAdapter creates a new CM adapter
func NewCMAdapter() *CMAdapter {
	return &CMAdapter{
		BaseAdapter: NewBaseAdapter(ToolCM, "cm"),
		serverPort:  8200,
	}
}

// Connect initializes the HTTP client by discovering the daemon port
func (a *CMAdapter) Connect(projectDir, sessionID string) error {
	client, err := cm.NewClient(projectDir, sessionID)
	if err != nil {
		return err
	}
	a.client = client
	if port := client.Port(); port > 0 {
		a.serverPort = port
	}
	return nil
}

// SetServerPort updates the CM server port
func (a *CMAdapter) SetServerPort(port int) {
	a.serverPort = port
}

// Detect checks if cm is installed
func (a *CMAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed cm version
func (a *CMAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get cm version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns cm capabilities
func (a *CMAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode}

	// Check for daemon mode
	if a.isDaemonRunning(ctx) {
		caps = append(caps, CapDaemonMode, "server_available")
	}

	return caps, nil
}

// Health checks if cm is functioning
func (a *CMAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "cm not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Check if we can run a basic command
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("cm at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	// Check daemon status
	if a.isDaemonRunning(ctx) {
		return &HealthStatus{
			Healthy:     true,
			Message:     "cm is healthy (daemon running)",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "cm is healthy (daemon not running)",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// isDaemonRunning checks if the cm daemon is responding
func (a *CMAdapter) isDaemonRunning(ctx context.Context) bool {
	// If connected to a session-specific daemon, probe that endpoint first.
	if a.client != nil {
		if func() bool {
			clientCtx, clientCancel := context.WithTimeout(ctx, 2*time.Second)
			defer clientCancel()
			return a.client.Health(clientCtx) == nil
		}() {
			return true
		}
	}

	directCtx, directCancel := context.WithTimeout(ctx, 2*time.Second)
	defer directCancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", a.serverPort)
	req, err := http.NewRequestWithContext(directCtx, "GET", url, nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// HasCapability checks if cm has a specific capability
func (a *CMAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete cm tool information
func (a *CMAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// CM-specific methods

// GetContext retrieves contextual information for a task
func (a *CMAdapter) GetContext(ctx context.Context, taskDescription string) (json.RawMessage, error) {
	if a.client != nil {
		res, err := a.client.GetContext(ctx, taskDescription)
		if err == nil {
			return json.Marshal(res)
		}
		// Fallback to CLI if HTTP fails
	}
	return a.runCommand(ctx, "context", taskDescription, "--json")
}

// OnboardStatus returns onboarding status
func (a *CMAdapter) OnboardStatus(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "onboard", "status", "--json")
}

// runCommand executes a cm command and returns raw JSON
func (a *CMAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	// Limit output to 10MB
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if strings.Contains(err.Error(), ErrOutputLimitExceeded.Error()) {
			return nil, fmt.Errorf("cm output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("cm failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from cm", ErrSchemaValidation)
	}

	return output, nil
}
