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

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
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
		serverURL:   "http://127.0.0.1:8765", // Base URL without /mcp/ path (appended per-request)
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

	assessment := CheckAgentMailServerHealth(ctx, a.serverURL)
	status := &HealthStatus{
		Healthy:     assessment.Healthy,
		Message:     assessment.Message,
		LastChecked: time.Now(),
		Latency:     time.Since(start),
		Error:       assessment.Error,
	}
	return status, nil
}

// isServerHealthy checks if the Agent Mail server is responding
func (a *AMAdapter) isServerHealthy(ctx context.Context) bool {
	return agentMailLivenessOK(ctx, a.serverURL)
}

// AgentMailServerHealth is the richer health verdict used by NTM doctor rows.
type AgentMailServerHealth struct {
	Healthy            bool
	DoctorStatus       string
	Message            string
	Error              string
	HealthStatus       string
	HealthLevel        string
	RecoveryMode       string
	RecoveryNextAction string
}

// CheckAgentMailServerHealth checks the MCP health_check tool, not just liveness.
func CheckAgentMailServerHealth(ctx context.Context, serverURL string) AgentMailServerHealth {
	return CheckAgentMailServerHealthWithToken(ctx, serverURL, "")
}

// CheckAgentMailServerHealthWithToken checks Agent Mail health using an explicit bearer token.
func CheckAgentMailServerHealthWithToken(ctx context.Context, serverURL, token string) AgentMailServerHealth {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	opts := []agentmail.Option{
		agentmail.WithBaseURL(agentMailMCPBaseURL(serverURL)),
		agentmail.WithTimeout(5 * time.Second),
	}
	if strings.TrimSpace(token) != "" {
		opts = append(opts, agentmail.WithToken(strings.TrimSpace(token)))
	}
	client := agentmail.NewClient(opts...)
	health, err := client.HealthCheck(ctx)
	if err != nil {
		errText := strings.TrimSpace(err.Error())
		if agentMailLivenessOK(ctx, serverURL) {
			return AgentMailServerHealth{
				Healthy:      false,
				DoctorStatus: "warning",
				Message:      "Agent Mail server listening but MCP health_check failed: " + errText,
				Error:        errText,
			}
		}
		return AgentMailServerHealth{
			Healthy:      false,
			DoctorStatus: "error",
			Message:      "Agent Mail server not responding",
			Error:        errText,
		}
	}

	return AssessAgentMailHealthStatus(health)
}

// AssessAgentMailHealthStatus converts the Agent Mail health payload into NTM status.
func AssessAgentMailHealthStatus(health *agentmail.HealthStatus) AgentMailServerHealth {
	if health == nil {
		return AgentMailServerHealth{
			Healthy:      false,
			DoctorStatus: "error",
			Message:      "Agent Mail health_check returned an empty payload",
		}
	}

	result := AgentMailServerHealth{
		Healthy:      true,
		DoctorStatus: "ok",
		HealthStatus: strings.TrimSpace(health.Status),
		HealthLevel:  strings.TrimSpace(health.HealthLevel),
	}
	if health.Recovery != nil {
		result.RecoveryMode = strings.TrimSpace(health.Recovery.Mode)
		result.RecoveryNextAction = strings.TrimSpace(health.Recovery.NextAction)
	}
	result.Message = agentMailHealthMessage(result)

	switch strings.ToLower(result.RecoveryMode) {
	case "corrupt":
		result.Healthy = false
		result.DoctorStatus = "error"
		return result
	}

	switch strings.ToLower(result.HealthStatus) {
	case "", "ok", "ready", "alive", "healthy":
	default:
		result.Healthy = false
		result.DoctorStatus = "warning"
		return result
	}

	switch strings.ToLower(result.HealthLevel) {
	case "", "green", "ok", "healthy":
	case "red", "error", "critical":
		result.Healthy = false
		result.DoctorStatus = "error"
	default:
		result.Healthy = false
		result.DoctorStatus = "warning"
	}

	return result
}

func agentMailMCPBaseURL(serverURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if trimmed == "" {
		return agentmail.DefaultBaseURL
	}
	if strings.HasSuffix(trimmed, "/mcp") {
		return trimmed + "/"
	}
	return trimmed + "/mcp/"
}

func agentMailLivenessOK(ctx context.Context, serverURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rootURL := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if strings.HasSuffix(rootURL, "/mcp") {
		rootURL = strings.TrimSuffix(rootURL, "/mcp")
	}
	if rootURL == "" {
		rootURL = "http://127.0.0.1:8765"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rootURL+"/health/liveness", nil)
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

func agentMailHealthMessage(health AgentMailServerHealth) string {
	parts := []string{"Agent Mail health_check"}
	if health.HealthStatus != "" {
		parts = append(parts, "status="+health.HealthStatus)
	}
	if health.HealthLevel != "" {
		parts = append(parts, "health_level="+health.HealthLevel)
	}
	if health.RecoveryMode != "" {
		parts = append(parts, "recovery_mode="+health.RecoveryMode)
	}
	if health.RecoveryNextAction != "" {
		parts = append(parts, "next_action="+health.RecoveryNextAction)
	}
	return strings.Join(parts, " ")
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
