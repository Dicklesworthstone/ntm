package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// AMAdapter provides integration with Agent Mail MCP server
type AMAdapter struct {
	*BaseAdapter
	// serverURL and token pin the probe to one endpoint. Empty means
	// "resolve from configuration and environment at probe time", which is
	// what every production caller does; the fields exist so a test can aim
	// the adapter at an httptest server.
	serverURL string
	token     string
}

// NewAMAdapter creates a new Agent Mail adapter
func NewAMAdapter() *AMAdapter {
	return &AMAdapter{
		BaseAdapter: NewBaseAdapter(ToolAM, "mcp-agent-mail"),
	}
}

// SetServerURL pins the Agent Mail endpoint this adapter probes, overriding
// the configured one.
func (a *AMAdapter) SetServerURL(url string) {
	a.serverURL = strings.TrimSuffix(url, "/")
}

// SetToken pins the bearer token this adapter probes with.
func (a *AMAdapter) SetToken(token string) {
	a.token = token
}

// client builds the Agent Mail client this adapter probes with.
//
// It is the same client every other Agent Mail surface in ntm uses, reading
// the configured `[agent_mail] url`/`token` (and the AGENT_MAIL_URL /
// AGENT_MAIL_TOKEN overrides) and attaching the bearer. The adapter used to
// keep its own hard-coded 127.0.0.1:8765 base URL and send an unauthenticated
// GET, so `ntm doctor` reported a correctly auth-walled server as unhealthy
// while robot Mail talked to it happily (ntm#316).
func (a *AMAdapter) client() *agentmail.Client {
	if a.serverURL != "" {
		opts := []agentmail.Option{agentmail.WithBaseURL(a.serverURL)}
		if a.token != "" {
			opts = append(opts, agentmail.WithToken(a.token))
		}
		return agentmail.NewClient(opts...)
	}

	var baseURL, token string
	if wd, err := os.Getwd(); err == nil {
		if cfg, err := config.LoadMerged(wd, config.DefaultPath()); err == nil && cfg != nil {
			baseURL, token = cfg.AgentMail.URL, cfg.AgentMail.Token
		}
	}
	return agentmail.NewClient(agentmail.ConfigOptions(baseURL, token)...)
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

// isServerHealthy checks if the Agent Mail server is responding.
//
// Availability is decided by the shared client, which probes the cheap
// liveness endpoint with the bearer attached and falls back to the MCP
// health_check tool when that endpoint is missing or auth-walled. A server
// that requires authentication and gets it is therefore available here for
// exactly the same reason it is available to robot Mail (ntm#316).
func (a *AMAdapter) isServerHealthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return a.client().IsAvailableContext(ctx)
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
// It runs the MCP health_check tool through the shared client, so it reaches
// the configured endpoint with the bearer attached rather than a hard-coded
// loopback URL with no credentials (ntm#316).
func (a *AMAdapter) HealthCheck(ctx context.Context) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	status, err := a.client().HealthCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent mail health check failed: %w", err)
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		return nil, fmt.Errorf("encode agent mail health: %w", err)
	}
	return encoded, nil
}

// ServerURL returns the endpoint this adapter probes: the pinned override
// when one is set, otherwise the configured (or default) Agent Mail base URL.
func (a *AMAdapter) ServerURL() string {
	if a.serverURL != "" {
		return a.serverURL
	}
	return a.client().BaseURL()
}
