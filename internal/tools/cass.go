package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CASSAdapter provides integration with Cross-Agent Semantic Search
type CASSAdapter struct {
	*BaseAdapter
}

// NewCASSAdapter creates a new CASS adapter
func NewCASSAdapter() *CASSAdapter {
	return &CASSAdapter{
		BaseAdapter: NewBaseAdapter(ToolCASS, "cass"),
	}
}

// Detect checks if cass is installed
func (a *CASSAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed cass version
func (a *CASSAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get cass version: %w", err)
	}

	return parseVersion(stdout.String())
}

// Capabilities returns cass capabilities
func (a *CASSAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode, CapSearch}
	return caps, nil
}

// Health checks if cass is functioning
func (a *CASSAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "cass not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try health command
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "health")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("cass at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "cass is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if cass has a specific capability
func (a *CASSAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete cass tool information
func (a *CASSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// CASS-specific methods

// Search performs a semantic search across agent conversations
func (a *CASSAdapter) Search(ctx context.Context, query string, limit int) (json.RawMessage, error) {
	args := []string{"search", query, "--robot", fmt.Sprintf("--limit=%d", limit)}
	return a.runCommand(ctx, args...)
}

// GetCapabilities returns cass capabilities info
func (a *CASSAdapter) GetCapabilities(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "capabilities", "--json")
}

// runCommand executes a cass command and returns raw JSON
func (a *CASSAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("cass failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from cass", ErrSchemaValidation)
	}

	return output, nil
}

// enhanceQueryForContext enhances a query with context-specific terms
func (a *CASSAdapter) enhanceQueryForContext(query string) string {
	// Simple implementation - just return the original query
	// TODO: Add context-specific enhancement logic
	return query
}

// filterAndRankForContext post-processes search results for better context relevance
func (a *CASSAdapter) filterAndRankForContext(rawResults json.RawMessage, limit int) (json.RawMessage, error) {
	// Simple implementation - just return the raw results
	// TODO: Add filtering and ranking logic
	return rawResults, nil
}

// extractKeyConcepts extracts key concepts from a query for broader matching
func (a *CASSAdapter) extractKeyConcepts(query string) []string {
	// Simple implementation - split on spaces and return non-empty words
	words := strings.Fields(query)
	var concepts []string
	for _, word := range words {
		if len(word) > 2 { // Only include words longer than 2 characters
			concepts = append(concepts, word)
		}
	}
	return concepts
}

// buildRelatedSessionQuery constructs a query for finding related sessions
func (a *CASSAdapter) buildRelatedSessionQuery(concepts []string, sessionId string) string {
	// Simple implementation - join concepts with OR
	if len(concepts) == 0 {
		return ""
	}
	return strings.Join(concepts, " OR ")
}

// buildPatternQuery constructs a query for finding historical patterns
func (a *CASSAdapter) buildPatternQuery(concepts []string) string {
	// Simple implementation - join concepts with AND for pattern matching
	if len(concepts) == 0 {
		return ""
	}
	return strings.Join(concepts, " AND ")
}
