package robot

// Shared prompt-context enrichment behind --with-cass and --with-memory.
//
// Robot sends and every assignment surface (`ntm assign`,
// `--robot-bulk-assign`, and the session coordinator's auto-assign) enrich
// the payload an agent receives through this one pipeline: relevant CASS
// history first (cass_inject.go), then CM project rules above it
// (cm_inject.go). Enrichment is never a gate: cass or cm being missing,
// disabled, or failing leaves the payload unchanged with a recorded skip.
//
// An assignment is enriched exactly once, before its durable intent is
// recorded, so the atomic coordinator hashes and persists the enriched
// prompt. Retries and recovery replay that recorded prompt and never query
// cass or cm again.

import (
	"context"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// PromptContextOptions carries the resolved --with-cass / --with-memory state
// and the config-derived engine parameters. The zero value disables both.
type PromptContextOptions struct {
	WithCASS     bool
	CASSConfig   *CASSConfig   // nil means DefaultCASSConfig
	FilterConfig *FilterConfig // nil means DefaultFilterConfig
	InjectConfig *InjectConfig // nil means DefaultInjectConfig

	WithMemory   bool
	MemoryInject *CMInjectConfig // nil means DefaultCMInjectConfig
}

// Enabled reports whether any enrichment was requested.
func (o PromptContextOptions) Enabled() bool {
	return o.WithCASS || o.WithMemory
}

// injectPromptContext prepends CASS history and then CM rules to message.
// query is the retrieval text both engines search with. format overrides the
// configured CASS block format when non-empty. scopeMemory binds the CM query
// to a verified project and returns a skip record when it cannot. Either
// returned info is nil when its enrichment was not requested.
func injectPromptContext(
	ctx context.Context,
	query, message string,
	format InjectionFormat,
	opts PromptContextOptions,
	scopeMemory func(CMInjectConfig) (CMInjectConfig, *CMInjectionInfo),
) (string, *CASSInjectionInfo, *CMInjectionInfo) {
	if ctx == nil || ctx.Err() != nil {
		return stoppedPromptContext(ctx, message, opts)
	}
	payload := message

	var cassInfo *CASSInjectionInfo
	if opts.WithCASS {
		queryConfig := DefaultCASSConfig()
		if opts.CASSConfig != nil {
			queryConfig = *opts.CASSConfig
		}
		filterConfig := DefaultFilterConfig()
		if opts.FilterConfig != nil {
			filterConfig = *opts.FilterConfig
		}
		injectConfig := DefaultInjectConfig()
		if opts.InjectConfig != nil {
			injectConfig = *opts.InjectConfig
		}
		if format != "" {
			injectConfig.Format = format
		}
		injectResult, queryResult, filterResult := InjectContextFromQuery(message, query, queryConfig, filterConfig, injectConfig, ctx)
		cassInfo = NewCASSInjectionInfo(injectResult, queryResult.Query, filterResult.Hits)
		if injectResult.Success && injectResult.ModifiedPrompt != "" {
			payload = injectResult.ModifiedPrompt
		}
	}
	if ctx.Err() != nil {
		return stoppedPromptContext(ctx, message, opts)
	}

	var memoryInfo *CMInjectionInfo
	if opts.WithMemory {
		memoryConfig := DefaultCMInjectConfig()
		if opts.MemoryInject != nil {
			memoryConfig = *opts.MemoryInject
		}
		memoryConfig, memoryInfo = scopeMemory(memoryConfig)
		if ctx.Err() != nil {
			return stoppedPromptContext(ctx, message, opts)
		}
		if memoryInfo == nil {
			payload, memoryInfo = InjectCMRules(ctx, query, payload, memoryConfig)
		}
	}
	if ctx.Err() != nil {
		return stoppedPromptContext(ctx, message, opts)
	}
	return payload, cassInfo, memoryInfo
}

// stoppedPromptContext discards partial enrichment rather than reporting rules
// or history as injected into an operation that stopped during retrieval. The
// dispatch/assignment owner still decides the operation's terminal outcome from
// its context; these receipts describe only why enrichment did not run to completion.
func stoppedPromptContext(ctx context.Context, message string, opts PromptContextOptions) (string, *CASSInjectionInfo, *CMInjectionInfo) {
	reason := "prompt context requires a non-nil operation context"
	if ctx != nil && ctx.Err() != nil {
		reason = "prompt context stopped: " + ctx.Err().Error()
	}
	var cassInfo *CASSInjectionInfo
	if opts.WithCASS {
		cassInfo = &CASSInjectionInfo{Enabled: true, SkippedReason: reason}
	}
	var memoryInfo *CMInjectionInfo
	if opts.WithMemory {
		memoryInfo = &CMInjectionInfo{Enabled: true, SkippedReason: reason}
	}
	return message, cassInfo, memoryInfo
}

// AssignmentPrompt is one freshly rendered assignment prompt and the bead and
// target it hands out.
type AssignmentPrompt struct {
	// Prompt is the rendered assignment payload to enrich.
	Prompt string
	// Title, Labels, and Description form the retrieval query, in that
	// order, so the bead's own terms lead the keyword budget.
	Title       string
	Labels      []string
	Description string
	// AgentType is the receiving agent; it selects the CASS block format.
	AgentType string
	// Session scopes CM daemon discovery.
	Session string
	// ProjectDir is the authoritative bead project: the CM workspace and the
	// CASS same-project preference.
	ProjectDir string
}

// retrievalQuery joins the bead's title, labels, and description. The bead ID
// is left out: tokens like "bd-123" carry no history signal. Credentials are
// redacted first because the extracted keywords are echoed back as
// cass_injection.query and handed to the cass and cm subprocesses.
func (p AssignmentPrompt) retrievalQuery() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{p.Title, strings.Join(p.Labels, " "), p.Description} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return redaction.ScanAndRedact(strings.Join(parts, "\n"), redaction.Config{Mode: redaction.ModeRedact}).Output
}

// EnrichAssignmentPrompt applies the send pipeline to one fresh assignment
// prompt. Callers run it before the durable assignment intent is recorded and
// never on recovery or replay of an intent that already exists. It returns
// the prompt unchanged with nil infos when opts requests nothing.
func EnrichAssignmentPrompt(ctx context.Context, p AssignmentPrompt, opts PromptContextOptions) (string, *CASSInjectionInfo, *CMInjectionInfo) {
	if !opts.Enabled() {
		return p.Prompt, nil, nil
	}
	if opts.WithCASS {
		filter := DefaultFilterConfig()
		if opts.FilterConfig != nil {
			filter = *opts.FilterConfig
		}
		// Assignment scope is authoritative. A reused caller configuration
		// must not rank another project's history above this task's own context.
		filter.CurrentWorkspace = p.ProjectDir
		opts.FilterConfig = &filter
	}
	return injectPromptContext(ctx, p.retrievalQuery(), p.Prompt, FormatForAgent(p.AgentType), opts,
		func(cfg CMInjectConfig) (CMInjectConfig, *CMInjectionInfo) {
			// The bead project is already authoritative, so no live-pane scope
			// resolution is needed; InjectCMRules records a skip when it is not
			// an absolute path or memory is disabled.
			cfg.ProjectDir, cfg.Workspace, cfg.SessionID = p.ProjectDir, p.ProjectDir, p.Session
			return cfg, nil
		})
}
