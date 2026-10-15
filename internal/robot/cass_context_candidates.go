package robot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/ntm/internal/cass"
)

// injectCASSContextCandidates makes project preference a retrieval policy, not
// merely a bonus on an already-truncated global result set. Both scopes use the
// maintained CASS client/decoder; final ranking, redaction, framing, and prompt
// budgets remain in the existing injection engine.
func injectCASSContextCandidates(ctx context.Context, prompt, query string, cfg CASSConfig, filter FilterConfig, inject InjectConfig) (InjectionResult, CASSQueryResult, FilterResult) {
	if !cfg.Enabled {
		return InjectContextFromQuery(prompt, query, cfg, filter, inject, ctx)
	}
	queryResult := CASSQueryResult{Hits: []CASSHit{}}
	failure := func(err error) (InjectionResult, CASSQueryResult, FilterResult) {
		queryResult.Success = false
		queryResult.Error = safeCASSContextError(err)
		return InjectionResult{
			ModifiedPrompt: prompt, Error: "CASS query failed: " + queryResult.Error,
			Metadata: InjectionMetadata{Enabled: true, FormatUsed: inject.Format, SkippedReason: queryResult.Error},
		}, queryResult, FilterResult{}
	}
	if ctx == nil {
		return failure(fmt.Errorf("cass context retrieval requires an operation context"))
	}
	if err := ctx.Err(); err != nil {
		return failure(err)
	}
	// A full context window must not pay for searches whose results cannot
	// be injected. Preserve the engine's existing skipped-result contract.
	if inject.CurrentContextPct > 0 && inject.CurrentContextPct >= inject.SkipThreshold {
		queryResult.Success = true
		return InjectContext(prompt, nil, inject), queryResult, FilterResult{}
	}
	queryResult.Keywords = ExtractKeywords(redactCASSHitContent(query))
	queryResult.Query = strings.Join(queryResult.Keywords, " ")
	if queryResult.Query == "" {
		queryResult.Success = true
		return InjectContext(prompt, nil, inject), queryResult, FilterResult{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultCASSTimeout
	}
	opts := cass.ContextSearchOptions{
		Query: queryResult.Query, Limit: cfg.MaxResults, Agents: cfg.AgentFilter,
	}
	if cfg.MaxAgeDays > 0 {
		opts.Since = fmt.Sprintf("%dd", cfg.MaxAgeDays)
	}
	if cfg.PreferSameProject && filter.PreferSameProject && sameCASSWorkspace(filter.CurrentWorkspace, filter.CurrentWorkspace) && strings.IndexFunc(filter.CurrentWorkspace, unicode.IsControl) < 0 {
		opts.Workspace = filter.CurrentWorkspace
	}
	client := cass.NewClient(cass.WithBinaryPath(cfg.BinaryPath), cass.WithTimeout(timeout))
	start := time.Now()
	candidates, err := client.SearchContext(ctx, opts)
	queryResult.QueryTime = time.Since(start)
	if err != nil {
		return failure(err)
	}
	if reason := candidates.Degradation(); reason != "" {
		// Do not log subprocess stderr, task text, or historical content.
		slog.WarnContext(ctx, "CASS context retrieval degraded", "reason", reason)
	}
	queryResult.TotalMatches = candidates.TotalMatches
	for _, hit := range candidates.Hits {
		content := hit.Content
		if strings.TrimSpace(content) == "" {
			content = hit.Snippet
		}
		converted := CASSHit{
			SourcePath: hit.SourcePath, Agent: hit.Agent, Content: content,
			Score: hit.Score, Workspace: hit.Workspace,
		}
		if hit.LineNumber != nil {
			converted.LineNumber = *hit.LineNumber
		}
		if hit.CreatedAt != nil && !hit.CreatedAt.IsZero() {
			created := hit.CreatedAt.Time
			converted.CreatedAt = &created
		}
		queryResult.Hits = append(queryResult.Hits, converted)
	}
	queryResult.Success = true
	filtered := FilterResults(queryResult.Hits, filter)
	result := InjectContext(prompt, filtered.Hits, inject)
	result.Metadata.ItemsFound = filtered.OriginalCount
	result.Metadata.ItemsFiltered = filtered.RemovedByScore + filtered.RemovedByAge + filtered.RemovedByTopic
	if err := ctx.Err(); err != nil {
		queryResult.Success, queryResult.Hits = false, []CASSHit{}
		return failure(err)
	}
	return result, queryResult, filtered
}

// A child process can print task text or credentials in stderr. Optional
// enrichment reports only stable failure classes, never that raw diagnostic.
func safeCASSContextError(err error) string {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, cass.ErrNotInstalled, cass.ErrNotInitialized, cass.ErrOutputLimit} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("cass search exited with code %d", exit.ExitCode())
	}
	return "cass context search failed or returned an invalid response"
}
