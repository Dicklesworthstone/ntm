package agentmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// DefaultBaseURL is the default Agent Mail server URL.
	DefaultBaseURL = "http://127.0.0.1:8765/mcp/"

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 10 * time.Second

	// LongTimeout is used for operations that may take longer (search, summarize).
	LongTimeout = 30 * time.Second

	// HealthCheckPath is the path for health checks.
	HealthCheckPath = "health"

	// AvailabilityCacheTTL is how long to cache IsAvailable() results.
	AvailabilityCacheTTL = 30 * time.Second
)

// Client provides methods to interact with the Agent Mail API.
type Client struct {
	baseURL     string
	bearerToken string
	httpClient  *http.Client
	projectKey  string // Cached project path
	requestID   atomic.Int64

	// Availability cache (30s TTL)
	healthCheckMu      sync.Mutex
	availableCache     atomic.Bool
	availableCacheTime atomic.Int64 // Unix timestamp in seconds
}

// Option configures the Client.
type Option func(*Client)

// WithBaseURL sets the Agent Mail server base URL.
func WithBaseURL(url string) Option {
	return func(c *Client) {
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		c.baseURL = url
	}
}

// WithToken sets the bearer token for authentication.
func WithToken(token string) Option {
	return func(c *Client) {
		c.bearerToken = token
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithProjectKey sets the default project key (working directory path).
func WithProjectKey(key string) Option {
	return func(c *Client) {
		c.projectKey = key
	}
}

// WithTimeout sets the default timeout for HTTP requests.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if c.httpClient != nil {
			c.httpClient.Timeout = timeout
		}
	}
}

// NewClient creates a new Agent Mail client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}

	// Check environment variables
	if token := os.Getenv("AGENT_MAIL_TOKEN"); token != "" {
		c.bearerToken = token
	}
	if baseURL := os.Getenv("AGENT_MAIL_URL"); baseURL != "" {
		c.baseURL = baseURL
	}

	// Ensure base URL ends with /
	if !strings.HasSuffix(c.baseURL, "/") {
		c.baseURL += "/"
	}

	// Apply options
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// IsAvailable checks if the Agent Mail server is reachable.
// Results are cached for 30 seconds to avoid repeated health checks.
func (c *Client) IsAvailable() bool {
	// Optimistic check (lock-free)
	cacheTime := c.availableCacheTime.Load()
	if cacheTime > 0 && time.Now().Unix()-cacheTime < int64(AvailabilityCacheTTL.Seconds()) {
		return c.availableCache.Load()
	}

	// Acquire lock to prevent thundering herd
	c.healthCheckMu.Lock()
	defer c.healthCheckMu.Unlock()

	// Double-check after acquiring lock
	cacheTime = c.availableCacheTime.Load()
	if cacheTime > 0 && time.Now().Unix()-cacheTime < int64(AvailabilityCacheTTL.Seconds()) {
		return c.availableCache.Load()
	}

	// Perform health check
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.HealthCheck(ctx)
	available := err == nil

	// Cache the result
	c.availableCache.Store(available)
	c.availableCacheTime.Store(time.Now().Unix())

	return available
}

// InvalidateCache clears the availability cache, forcing the next IsAvailable() call
// to perform a fresh health check.
func (c *Client) InvalidateCache() {
	c.availableCacheTime.Store(0)
}

// DefaultArchivePath is the default location for the Agent Mail archive.
const DefaultArchivePath = ".mcp_agent_mail_git_mailbox_repo"

// HasArchive checks if the Agent Mail archive directory exists.
// This provides a fallback detection method when the HTTP endpoint isn't available
// but Agent Mail is running via MCP stdio protocol.
func HasArchive() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	archivePath := filepath.Join(homeDir, DefaultArchivePath)
	info, err := os.Stat(archivePath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// HasArchiveForProject checks if the Agent Mail archive has data for a specific project.
func HasArchiveForProject(projectKey string) bool {
	if !HasArchive() {
		return false
	}
	homeDir, _ := os.UserHomeDir()
	slug := ProjectSlugFromPath(projectKey)
	if slug == "" {
		return false
	}
	projectPath := filepath.Join(homeDir, DefaultArchivePath, "projects", slug)
	info, err := os.Stat(projectPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// HealthCheck performs a health check against the Agent Mail server.
// This uses the MCP health_check tool via JSON-RPC.
func (c *Client) HealthCheck(ctx context.Context) (*HealthStatus, error) {
	result, err := c.callTool(ctx, "health_check", nil)
	if err != nil {
		return nil, err
	}

	var status HealthStatus
	if err := json.Unmarshal(result, &status); err != nil {
		return nil, NewAPIError("health_check", 0, err)
	}

	return &status, nil
}

// ProjectKey returns the configured project key.
func (c *Client) ProjectKey() string {
	return c.projectKey
}

// SetProjectKey sets the project key.
func (c *Client) SetProjectKey(key string) {
	c.projectKey = key
}

// BaseURL returns the configured Agent Mail base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// JSONRPCRequest represents a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// MCPToolResult represents the MCP tools/call result envelope.
// MCP wraps tool outputs in this structure rather than returning raw data.
type MCPToolResult struct {
	// Content is an array of content blocks (text, image, etc.)
	Content []MCPContentBlock `json:"content,omitempty"`
	// StructuredContent contains the parsed tool output (preferred)
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	// IsError indicates if the tool returned an error
	IsError bool `json:"isError,omitempty"`
}

// MCPContentBlock represents a single content block in an MCP response.
type MCPContentBlock struct {
	Type string `json:"type"` // "text", "image", etc.
	Text string `json:"text,omitempty"`
}

// extractMCPContent extracts the actual tool output from an MCP envelope.
// It prefers structuredContent (already parsed), falls back to content[0].text
// (JSON string), and finally returns the raw result if not an MCP envelope.
func extractMCPContent(result json.RawMessage) (json.RawMessage, error) {
	if len(result) == 0 {
		return result, nil
	}

	// Try to parse as MCP envelope
	var envelope MCPToolResult
	if err := json.Unmarshal(result, &envelope); err != nil {
		// Not an MCP envelope, return raw result (backward compatibility)
		return result, nil
	}

	// Check for tool-level error
	if envelope.IsError {
		// Extract error message from content if available
		if len(envelope.Content) > 0 && envelope.Content[0].Text != "" {
			return nil, fmt.Errorf("tool error: %s", envelope.Content[0].Text)
		}
		return nil, fmt.Errorf("tool returned error")
	}

	// Prefer structuredContent (already parsed JSON)
	if len(envelope.StructuredContent) > 0 {
		return envelope.StructuredContent, nil
	}

	// Fall back to content[0].text (JSON string that needs no re-parsing)
	if len(envelope.Content) > 0 && envelope.Content[0].Type == "text" && envelope.Content[0].Text != "" {
		// The text field contains JSON, return it as RawMessage
		return json.RawMessage(envelope.Content[0].Text), nil
	}

	// No structured content and no text content - might not be an MCP envelope
	// Check if it looks like raw tool output (has expected fields)
	// Return raw result for backward compatibility
	return result, nil
}

// ToolCallParams represents the params for a tools/call request.
type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// callTool makes a JSON-RPC call to the Agent Mail server.
func (c *Client) callTool(ctx context.Context, toolName string, args map[string]interface{}) (json.RawMessage, error) {
	reqID := c.requestID.Add(1)

	rpcReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  "tools/call",
		Params: ToolCallParams{
			Name:      toolName,
			Arguments: args,
		},
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, NewAPIError(toolName, 0, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, NewAPIError(toolName, 0, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, NewAPIError(toolName, 0, ErrTimeout)
		}
		return nil, NewAPIError(toolName, 0, ErrServerUnavailable)
	}
	defer resp.Body.Close()

	// Read response body (limit to 10MB to prevent DoS/OOM)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, NewAPIError(toolName, 0, err)
	}

	// Check HTTP status
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, NewAPIError(toolName, resp.StatusCode, ErrUnauthorized)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, NewAPIError(toolName, resp.StatusCode, fmt.Errorf("unexpected status: %s", resp.Status))
	}

	// Parse JSON-RPC response
	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, NewAPIError(toolName, 0, err)
	}

	// Check for JSON-RPC error
	if rpcResp.Error != nil {
		return nil, NewAPIError(toolName, 0, mapJSONRPCError(rpcResp.Error))
	}

	// Extract actual content from MCP envelope
	content, err := extractMCPContent(rpcResp.Result)
	if err != nil {
		return nil, NewAPIError(toolName, 0, err)
	}

	return content, nil
}

// callToolWithTimeout calls a tool with a specific timeout.
func (c *Client) callToolWithTimeout(ctx context.Context, toolName string, args map[string]interface{}, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.callTool(ctx, toolName, args)
}

// ReadResource reads a resource from the Agent Mail server.
func (c *Client) ReadResource(ctx context.Context, uri string) (json.RawMessage, error) {
	reqID := c.requestID.Add(1)

	rpcReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  "resources/read",
		Params: map[string]string{
			"uri": uri,
		},
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, NewAPIError("resources/read", 0, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, NewAPIError("resources/read", 0, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, NewAPIError("resources/read", 0, ErrTimeout)
		}
		return nil, NewAPIError("resources/read", 0, ErrServerUnavailable)
	}
	defer resp.Body.Close()

	// Read response body (limit to 10MB to prevent DoS/OOM)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, NewAPIError("resources/read", 0, err)
	}

	// Check HTTP status
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, NewAPIError("resources/read", resp.StatusCode, ErrUnauthorized)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, NewAPIError("resources/read", resp.StatusCode, fmt.Errorf("unexpected status: %s", resp.Status))
	}

	// Parse JSON-RPC response
	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, NewAPIError("resources/read", 0, err)
	}

	// Check for JSON-RPC error
	if rpcResp.Error != nil {
		return nil, NewAPIError("resources/read", 0, mapJSONRPCError(rpcResp.Error))
	}

	return rpcResp.Result, nil
}

// httpBaseURL returns the HTTP REST API base URL derived from the MCP base URL.
// The MCP endpoint is typically at /mcp/ while the HTTP endpoints are at the root.
// Example: "http://127.0.0.1:8765/mcp/" -> "http://127.0.0.1:8765"
func (c *Client) httpBaseURL() string {
	base := c.baseURL
	// Remove trailing /mcp/ or /mcp if present
	if strings.HasSuffix(base, "/mcp/") {
		return strings.TrimSuffix(base, "/mcp/")
	}
	if strings.HasSuffix(base, "/mcp") {
		return strings.TrimSuffix(base, "/mcp")
	}
	// Remove trailing slash
	return strings.TrimSuffix(base, "/")
}

// ProjectSlugFromPath derives a project slug from an absolute path.
// This matches the logic in the Agent Mail server.
// Example: "/Users/jemanuel/projects/ntm" -> "ntm"
func ProjectSlugFromPath(path string) string {
	if path == "" {
		return ""
	}
	// Get the last component using filepath.Base
	slug := filepath.Base(path)
	if slug == "." || slug == "/" {
		// Fallback for root or dot
		return "root"
	}

	// Lowercase and sanitize
	var sb strings.Builder
	for _, r := range slug {
		r = unicode.ToLower(r)
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else if r == ' ' {
			sb.WriteRune('_')
		}
		// Skip other characters
	}
	return sb.String()
}
