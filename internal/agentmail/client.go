package agentmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
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
)

// Client provides methods to interact with the Agent Mail API.
type Client struct {
	baseURL     string
	bearerToken string
	httpClient  *http.Client
	projectKey  string // Cached project path
	requestID   atomic.Int64
}

// Option configures the Client.
type Option func(*Client)

// WithBaseURL sets the Agent Mail server base URL.
func WithBaseURL(url string) Option {
	return func(c *Client) {
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
		c.httpClient.Timeout = timeout
	}
}

// NewClient creates a new Agent Mail client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			Transport: &http.Transport{
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

	// Apply options
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// IsAvailable checks if the Agent Mail server is reachable.
func (c *Client) IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.HealthCheck(ctx)
	return err == nil
}

// HealthCheck performs a health check against the Agent Mail server.
func (c *Client) HealthCheck(ctx context.Context) (*HealthStatus, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+HealthCheckPath, nil)
	if err != nil {
		return nil, NewAPIError("health_check", 0, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAPIError("health_check", 0, ErrServerUnavailable)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, NewAPIError("health_check", resp.StatusCode, fmt.Errorf("unexpected status: %s", resp.Status))
	}

	var status HealthStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
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

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
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

	return rpcResp.Result, nil
}

// callToolWithTimeout calls a tool with a specific timeout.
func (c *Client) callToolWithTimeout(ctx context.Context, toolName string, args map[string]interface{}, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.callTool(ctx, toolName, args)
}
