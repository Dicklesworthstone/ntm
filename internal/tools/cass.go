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
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get cass version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
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
	cmd.WaitDelay = time.Second
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
	args := []string{"search", "--robot", fmt.Sprintf("--limit=%d", limit), "--", query}
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
			return nil, fmt.Errorf("cass output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("cass failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from cass", ErrSchemaValidation)
	}

	return output, nil
}

// enhanceQueryForContext is a pass-through.  Query enhancement (synonym
// expansion, typo correction, context injection) is handled by the cass CLI
// itself via its --robot mode.  This adapter intentionally does not duplicate
// that logic; callers that need richer query rewriting should invoke cass
// with additional flags rather than pre-processing here.
func (a *CASSAdapter) enhanceQueryForContext(query string) string {
	return query
}

// filterAndRankForContext is a pass-through.  Result ranking and dedup are
// performed server-side by cass; re-ranking in the adapter would require
// access to embedding vectors that are not included in the JSON response.
func (a *CASSAdapter) filterAndRankForContext(rawResults json.RawMessage, limit int) (json.RawMessage, error) {
	return rawResults, nil
}

// extractKeyConcepts splits a query into significant words (> 2 chars) for
// use in secondary queries such as related-session or pattern lookups.
func (a *CASSAdapter) extractKeyConcepts(query string) []string {
	words := strings.Fields(query)
	concepts := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) > 2 {
			concepts = append(concepts, word)
		}
	}
	return concepts
}

// buildRelatedSessionQuery constructs a disjunctive (OR) query for finding
// sessions that share any of the given concepts.
func (a *CASSAdapter) buildRelatedSessionQuery(concepts []string, _ string) string {
	if len(concepts) == 0 {
		return ""
	}
	return strings.Join(concepts, " OR ")
}

// buildPatternQuery constructs a conjunctive (AND) query for finding
// historical patterns that match all of the given concepts.
func (a *CASSAdapter) buildPatternQuery(concepts []string) string {
	if len(concepts) == 0 {
		return ""
	}
	return strings.Join(concepts, " AND ")
}
