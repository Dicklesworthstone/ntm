package cass

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// ContextSearchOptions requests a bounded candidate set, not final prompt
// formatting. A nonempty absolute Workspace adds a second, indexed-project
// search; global candidates remain eligible. Both scopes share one timeout.
type ContextSearchOptions struct {
	Query     string
	Workspace string
	Limit     int
	Since     string
	Agents    []string
}

// ContextSearchResult keeps degradation separate from successful empty results.
// TotalMatches is global when the global query succeeded; otherwise it is the
// number of retained candidates, not an invented global index count.
type ContextSearchResult struct {
	Hits                  []SearchHit
	TotalMatches          int
	GlobalError           error
	WorkspaceError        error
	WorkspaceQueried      bool
	RejectedWorkspaceHits int
	DuplicateHits         int
}

// SearchContext retrieves global and project candidates concurrently, with at
// most two searches and one shared client timeout. It joins both searches even
// on cancellation. Custom Executors used here must support concurrent Run calls.
// Missing tools/indices and malformed responses are errors, never fabricated hits.
func (c *Client) SearchContext(ctx context.Context, opts ContextSearchOptions) (*ContextSearchResult, error) {
	if ctx == nil {
		return nil, errors.New("cass context search requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Workspace != "" && !sameContextWorkspace(opts.Workspace, opts.Workspace) {
		return nil, errors.New("cass context workspace must be an absolute path without control characters")
	}
	if opts.Limit <= 0 {
		opts.Limit = 5
	}
	// Do not retain caller-owned slices in concurrent workers.
	opts.Agents = append([]string(nil), opts.Agents...)
	searchCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	type result struct {
		scoped bool
		data   *SearchResponse
		err    error
	}
	results := make(chan result, 2)
	run := func(workspace string) {
		data, err := c.search(searchCtx, SearchOptions{
			Query: opts.Query, Workspace: workspace, Limit: opts.Limit, Since: opts.Since,
		}, opts.Agents)
		// Preserve context injection's existing no-match exit convention, but
		// never reinterpret cancellation, overflow, or an uninitialized index.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && searchCtx.Err() == nil {
			data, err = &SearchResponse{Hits: []SearchHit{}}, nil
		}
		results <- result{scoped: workspace != "", data: data, err: err}
	}
	go run("")
	count := 1
	if opts.Workspace != "" {
		count++
		go run(filepath.Clean(opts.Workspace))
	}
	var global, local result
	for i := 0; i < count; i++ {
		r := <-results
		if r.scoped {
			local = r
		} else {
			global = r
		}
	}
	// An expired optional lookup may retain the other scope's success. An
	// expired/cancelled *operation* must discard everything instead.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := &ContextSearchResult{
		Hits: []SearchHit{}, GlobalError: global.err, WorkspaceError: local.err,
		WorkspaceQueried: opts.Workspace != "",
	}
	if global.err != nil && (opts.Workspace == "" || local.err != nil) {
		return out, errors.Join(global.err, local.err)
	}
	// Global order is retained, independently of which query finishes first.
	positions := make(map[contextHitKey]int)
	appendHit := func(hit SearchHit) {
		key, identifiable := contextHitIdentity(hit)
		if identifiable {
			if i, exists := positions[key]; exists {
				out.DuplicateHits++
				prior := &out.Hits[i]
				// Explicit disagreement is not permission to relabel global
				// history as local. The query's scope alone proves nothing.
				if prior.Workspace != "" && hit.Workspace != "" && !sameContextWorkspace(prior.Workspace, hit.Workspace) {
					out.RejectedWorkspaceHits++
					return
				}
				if prior.Workspace == "" {
					prior.Workspace = hit.Workspace
				}
				if prior.CreatedAt == nil || prior.CreatedAt.IsZero() {
					prior.CreatedAt = hit.CreatedAt
				}
				if strings.TrimSpace(prior.Content) == "" && strings.TrimSpace(hit.Content) != "" {
					prior.Content = hit.Content
				}
				if strings.TrimSpace(prior.Snippet) == "" {
					prior.Snippet = hit.Snippet
				}
				return
			}
			positions[key] = len(out.Hits)
		}
		out.Hits = append(out.Hits, hit)
	}
	if global.err == nil && global.data != nil {
		out.TotalMatches = global.data.TotalMatches
		for _, hit := range boundedContextHits(global.data.Hits, opts.Limit) {
			appendHit(hit)
		}
	}
	if local.err == nil && local.data != nil {
		for _, hit := range boundedContextHits(local.data.Hits, opts.Limit) {
			if !sameContextWorkspace(hit.Workspace, opts.Workspace) {
				out.RejectedWorkspaceHits++
				continue
			}
			appendHit(hit)
		}
	}
	if global.err != nil {
		out.TotalMatches = len(out.Hits)
	}
	return out, nil
}

type contextHitKey struct {
	path, agent string
	line        int
}

func contextHitIdentity(hit SearchHit) (contextHitKey, bool) {
	if hit.SourcePath == "" || hit.LineNumber == nil || *hit.LineNumber <= 0 {
		// Different messages in one archive must not collapse just because
		// neither result supplies its line. Unknown identity is not equality.
		return contextHitKey{}, false
	}
	return contextHitKey{hit.SourcePath, hit.Agent, *hit.LineNumber}, true
}

func sameContextWorkspace(a, b string) bool {
	valid := func(p string) bool {
		return filepath.IsAbs(p) && strings.IndexFunc(p, unicode.IsControl) < 0
	}
	return valid(a) && valid(b) && filepath.Clean(a) == filepath.Clean(b)
}

func boundedContextHits(hits []SearchHit, limit int) []SearchHit {
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

// Degradation reports which lookup was lost without including query text,
// subprocess stderr, or historical content in the diagnostic.
func (r *ContextSearchResult) Degradation() string {
	if r == nil {
		return ""
	}
	var parts []string
	if r.GlobalError != nil {
		parts = append(parts, "global search unavailable")
	}
	if r.WorkspaceError != nil {
		parts = append(parts, "workspace search unavailable")
	}
	if r.RejectedWorkspaceHits > 0 {
		parts = append(parts, fmt.Sprintf("%d workspace hits rejected for inconsistent indexed identity", r.RejectedWorkspaceHits))
	}
	return strings.Join(parts, "; ")
}
