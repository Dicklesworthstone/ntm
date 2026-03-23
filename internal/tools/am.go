package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// AMAdapter provides integration with Agent Mail MCP server
type AMAdapter struct {
	*BaseAdapter
	serverURL string
}

// NewAMAdapter creates a new Agent Mail adapter
func NewAMAdapter() *AMAdapter {
	return &AMAdapter{
		BaseAdapter: NewBaseAdapter(ToolAM, "mcp-agent-mail"),
		serverURL:   "http://127.0.0.1:8765",
	}
}

// SetServerURL updates the Agent Mail server URL
func (a *AMAdapter) SetServerURL(url string) {
	a.serverURL = strings.TrimSuffix(url, "/")
}

// Detect checks if Agent Mail CLI is installed
func (a *AMAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the Agent Mail version
func (a *AMAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get am version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns Agent Mail capabilities
func (a *AMAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapMacros}

	// Check if server is responding
	if a.isServerHealthy(ctx) {
		caps = append(caps, "server_available")
	}

	return caps, nil
}

// Health checks if Agent Mail is functioning
func (a *AMAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	// Check CLI
	_, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "Agent Mail CLI not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Check server health
	if a.isServerHealthy(ctx) {
		return &HealthStatus{
			Healthy:     true,
			Message:     "Agent Mail server is healthy",
			LastChecked: time.Now(),
			Latency:     time.Since(start),
		}, nil
	}

	return &HealthStatus{
		Healthy:     false,
		Message:     "Agent Mail CLI installed but server not responding",
		LastChecked: time.Now(),
		Latency:     time.Since(start),
	}, nil
}

// isServerHealthy checks if the Agent Mail server is responding
func (a *AMAdapter) isServerHealthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", a.serverURL+"/health/liveness", nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// HasCapability checks if Agent Mail has a specific capability
func (a *AMAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete Agent Mail tool information
func (a *AMAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// AM-specific methods

// HealthCheck calls the server health endpoint
func (a *AMAdapter) HealthCheck(ctx context.Context) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", a.serverURL+"/health/liveness", nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent mail server not responding: %w", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	// Limit read to 1MB to prevent OOM
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, 1024*1024)); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// ServerURL returns the configured server URL
func (a *AMAdapter) ServerURL() string {
	return a.serverURL
}
