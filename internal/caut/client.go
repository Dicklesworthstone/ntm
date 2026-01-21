package caut

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// ErrNotInstalled is returned when the caut binary is not found
var ErrNotInstalled = fmt.Errorf("caut is not installed")

// ErrNoData is returned when caut returns no data for a provider
var ErrNoData = fmt.Errorf("no data returned from caut")

// Executor interface allows mocking the caut binary execution
type Executor interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// DefaultExecutor runs the actual caut binary
type DefaultExecutor struct {
	BinaryPath string
}

// Run executes the caut command with the given arguments
func (e *DefaultExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.BinaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("caut execution failed: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Client interacts with the caut CLI
type Client struct {
	executor Executor
	timeout  time.Duration
}

// ClientOption configures the client
type ClientOption func(*Client)

// WithBinaryPath sets the path to the caut binary
func WithBinaryPath(path string) ClientOption {
	return func(c *Client) {
		if path == "" {
			return
		}
		if execImpl, ok := c.executor.(*DefaultExecutor); ok {
			execImpl.BinaryPath = path
		}
	}
}

// WithTimeout sets the command timeout
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = d
	}
}

// WithExecutor sets a custom executor (for testing)
func WithExecutor(e Executor) ClientOption {
	return func(c *Client) {
		c.executor = e
	}
}

// NewClient creates a new caut client
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		executor: &DefaultExecutor{BinaryPath: "caut"},
		timeout:  30 * time.Second,
	}

	for _, opt := range opts {
		opt(c)
	}
	return c
}

// IsInstalled checks if the caut binary is available
func (c *Client) IsInstalled() bool {
	if execImpl, ok := c.executor.(*DefaultExecutor); ok {
		path, err := exec.LookPath(execImpl.BinaryPath)
		return err == nil && path != ""
	}
	return true // Assume custom executor is working
}

// FetchUsage queries caut for provider usage
func (c *Client) FetchUsage(ctx context.Context, providers []string) (*UsageResult, error) {
	if !c.IsInstalled() {
		return nil, ErrNotInstalled
	}

	args := []string{"usage", "--format", "json"}
	for _, p := range providers {
		args = append(args, "--provider", p)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	output, err := c.executor.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("caut failed: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("parse caut output: %w", err)
	}

	return &UsageResult{
		SchemaVersion: resp.SchemaVersion,
		Payloads:      resp.Data.Payloads,
		Errors:        resp.Errors,
		FetchedAt:     time.Now(),
	}, nil
}

// GetProviderUsage fetches usage for a single provider
func (c *Client) GetProviderUsage(ctx context.Context, provider string) (*ProviderPayload, error) {
	result, err := c.FetchUsage(ctx, []string{provider})
	if err != nil {
		return nil, err
	}
	if len(result.Payloads) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoData, provider)
	}
	return &result.Payloads[0], nil
}

// GetAgentUsage fetches usage for an agent type (cc, cod, gmi)
func (c *Client) GetAgentUsage(ctx context.Context, agentType string) (*ProviderPayload, error) {
	provider := AgentTypeToProvider(agentType)
	if provider == "" {
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}
	return c.GetProviderUsage(ctx, provider)
}

// FetchAllSupportedUsage fetches usage for all NTM-supported providers
func (c *Client) FetchAllSupportedUsage(ctx context.Context) (*UsageResult, error) {
	return c.FetchUsage(ctx, SupportedProviders())
}

// CachedClient wraps Client with caching to avoid excessive caut calls
type CachedClient struct {
	client   *Client
	cache    map[string]*cachedResult
	cacheTTL time.Duration
	mu       sync.RWMutex
}

type cachedResult struct {
	payload   *ProviderPayload
	fetchedAt time.Time
}

// NewCachedClient creates a caut client with caching
func NewCachedClient(client *Client, cacheTTL time.Duration) *CachedClient {
	return &CachedClient{
		client:   client,
		cache:    make(map[string]*cachedResult),
		cacheTTL: cacheTTL,
	}
}

// IsInstalled checks if caut is available
func (c *CachedClient) IsInstalled() bool {
	return c.client.IsInstalled()
}

// GetProviderUsage fetches usage with caching
func (c *CachedClient) GetProviderUsage(ctx context.Context, provider string) (*ProviderPayload, error) {
	c.mu.RLock()
	if cached, ok := c.cache[provider]; ok {
		if time.Since(cached.fetchedAt) < c.cacheTTL {
			c.mu.RUnlock()
			return cached.payload, nil
		}
	}
	c.mu.RUnlock()

	// Cache miss or expired - fetch fresh
	payload, err := c.client.GetProviderUsage(ctx, provider)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[provider] = &cachedResult{
		payload:   payload,
		fetchedAt: time.Now(),
	}

	return payload, nil
}

// GetAgentUsage fetches usage for an agent type with caching
func (c *CachedClient) GetAgentUsage(ctx context.Context, agentType string) (*ProviderPayload, error) {
	provider := AgentTypeToProvider(agentType)
	if provider == "" {
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}
	return c.GetProviderUsage(ctx, provider)
}

// Invalidate clears cached data for a provider
func (c *CachedClient) Invalidate(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, provider)
}

// InvalidateAll clears all cached data
func (c *CachedClient) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*cachedResult)
}

// CacheStats returns statistics about the cache
func (c *CachedClient) CacheStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	validEntries := 0
	expiredEntries := 0
	providers := make([]string, 0, len(c.cache))

	for provider, cached := range c.cache {
		providers = append(providers, provider)
		if time.Since(cached.fetchedAt) < c.cacheTTL {
			validEntries++
		} else {
			expiredEntries++
		}
	}

	return map[string]interface{}{
		"valid_entries":   validEntries,
		"expired_entries": expiredEntries,
		"total_entries":   len(c.cache),
		"providers":       providers,
		"ttl_seconds":     c.cacheTTL.Seconds(),
	}
}
