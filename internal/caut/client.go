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

// limitedBuffer prevents unbounded memory growth when capturing output
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (n int, err error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("output exceeded limit of %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
}

// DefaultExecutor runs the caut binary via os/exec
type DefaultExecutor struct {
	BinaryPath string
}

// Run executes the caut command with the given arguments
func (e *DefaultExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.BinaryPath, args...)
	cmd.WaitDelay = 2 * time.Second

	// Limit stdout to 10MB to prevent OOM
	stdout := &limitedBuffer{limit: 10 * 1024 * 1024}
	var stderr bytes.Buffer

	cmd.Stdout = stdout
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
