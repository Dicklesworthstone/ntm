package cass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Search performs a search query against CASS
func (c *Client) Search(ctx context.Context, opts SearchOptions) (*SearchResponse, error) {
	return c.search(ctx, opts, nil)
}

// search is shared by ordinary search and the two context-retrieval scopes.
// Keep argument construction, cancellation and the response decoder identical.
func (c *Client) search(ctx context.Context, opts SearchOptions, agents []string) (*SearchResponse, error) {
	if ctx == nil {
		return nil, errors.New("cass search requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.IsInstalled() {
		return nil, ErrNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Build arguments for: cass search --json [flags] -- <query>
	args := []string{"search", "--json"}

	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	}
	if opts.Offset > 0 {
		args = append(args, fmt.Sprintf("--offset=%d", opts.Offset))
	}
	if opts.Agent != "" {
		args = append(args, fmt.Sprintf("--agent=%s", opts.Agent))
	}
	for _, agent := range agents {
		args = append(args, "--agent="+agent)
	}
	if opts.Workspace != "" {
		args = append(args, fmt.Sprintf("--workspace=%s", opts.Workspace))
	}
	if opts.Since != "" {
		args = append(args, fmt.Sprintf("--since=%s", opts.Since))
	}
	if opts.Until != "" {
		args = append(args, fmt.Sprintf("--until=%s", opts.Until))
	}
	if opts.Cursor != "" {
		args = append(args, fmt.Sprintf("--cursor=%s", opts.Cursor))
	}
	if opts.Fields != "" {
		args = append(args, fmt.Sprintf("--fields=%s", opts.Fields))
	}
	if opts.MaxTokens > 0 {
		args = append(args, fmt.Sprintf("--max-tokens=%d", opts.MaxTokens))
	}
	if opts.Aggregate != "" {
		args = append(args, fmt.Sprintf("--aggregate=%s", opts.Aggregate))
	}
	if opts.Explain {
		args = append(args, "--explain")
	}
	if opts.Highlight {
		args = append(args, "--highlight")
	}

	// Add separator and query
	args = append(args, "--", opts.Query)

	output, err := c.executor.Run(ctx, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}

	var response SearchResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}

	return &response, nil
}

// SearchQuick performs a simple search with defaults
func (c *Client) SearchQuick(ctx context.Context, query string) (*SearchResponse, error) {
	return c.Search(ctx, SearchOptions{
		Query:  query,
		Limit:  10,
		Fields: "summary",
	})
}

// SearchForContext searches for relevant past context
func (c *Client) SearchForContext(ctx context.Context, query, workspace string) (*SearchResponse, error) {
	return c.Search(ctx, SearchOptions{
		Query:     query,
		Workspace: workspace,
		Since:     "30d",
		Limit:     5,
		Fields:    "summary",
	})
}
