package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ContextPaneDelivery is a per-target receipt, not proof that an agent has
// finished processing the context. A failed send may have written some bytes:
// uncertain is deliberately distinct from not_attempted and is never retried.
type ContextPaneDelivery struct {
	Pane      int                        `json:"pane"`
	Target    string                     `json:"target"`
	AgentType string                     `json:"agent_type"`
	PackID    string                     `json:"pack_id,omitempty"`
	Bytes     int                        `json:"bytes"`
	Status    string                     `json:"status"` // planned | delivered | uncertain | not_attempted
	Error     string                     `json:"error,omitempty"`
	Capacity  *ContextPaneCapacity       `json:"capacity,omitempty"`
	Redaction *dispatch.RedactionReceipt `json:"redaction,omitempty"`
}

// ContextPaneCapacity distinguishes native accounting from an unverified
// fallback. Token budgets use the builder's approximate four-byte convention;
// measured occupancy never means that the payload was provider-tokenized.
type ContextPaneCapacity struct {
	Status          string     `json:"status"` // measured | stale | unknown
	BudgetSource    string     `json:"budget_source"`
	Model           string     `json:"model,omitempty"`
	ContextWindow   int        `json:"context_window,omitempty"`
	UsedTokens      *int       `json:"used_tokens,omitempty"`
	AvailableTokens *int       `json:"available_tokens,omitempty"`
	BudgetTokens    int        `json:"budget_tokens"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	Reason          string     `json:"reason,omitempty"`
}

type contextPackObservation struct {
	capacity ContextPaneCapacity
	usage    *ntmctx.TranscriptUsage
}

type contextPackBuildKey struct {
	agentType agent.AgentType
	model     string
	window    int
	budget    int
}

type contextInjectionRequest struct {
	Pane     tmux.Pane
	Content  string
	PackID   string
	Capacity *ContextPaneCapacity
	MaxBytes int
}

type contextPaneSender func(context.Context, tmux.Pane, string) error

func uniformContextRequests(panes []tmux.Pane, content string) []contextInjectionRequest {
	requests := make([]contextInjectionRequest, len(panes))
	for i, pane := range panes {
		requests[i] = contextInjectionRequest{Pane: pane, Content: content}
	}
	return requests
}

// sendContextToPane uses the shared live gates and submission protocol while
// preserving the pane identity observed before context preparation. The agent
// guard also covers composer rescue keys after the initial prompt was staged.
func sendContextToPane(ctx context.Context, pane tmux.Pane, content string) error {
	target := dispatch.Target{
		Pane: pane, Ref: pane.Ref(), Address: pane.Ref().Physical(), AgentType: pane.Type,
	}
	plan, err := (dispatch.DefaultProtocolPlanner{}).PlanDelivery(ctx, target, true)
	if err != nil {
		return dispatch.GuaranteeNoDeliveryActuation(err)
	}
	delivery := dispatch.Delivery{
		Target: target, Message: content, Protocol: plan.Protocol,
		EnterDelay: plan.EnterDelay, SecondEnterDelay: plan.SecondEnterDelay,
	}
	if plan.Protocol == dispatch.ProtocolDoubleEnter {
		guarded, err := tmux.WithPaneMutationGuard(ctx, pane.ID, pane.PID)
		if err != nil {
			return dispatch.GuaranteeNoDeliveryActuation(err)
		}
		return (dispatch.TMUXDeliverer{}).Deliver(guarded, delivery)
	}
	// Explicit user/unknown targets keep their single-Enter, PID-bound
	// transport. The agent-only mutation guard would reject their shell.
	if _, err := dispatch.PrepareTMUXDelivery(ctx, delivery); err != nil {
		return err
	}
	return tmux.DefaultClient.SendKeysForAgentIfCurrentContext(ctx, pane.ID, content, true, pane.Type, pane.PID, false)
}

// dispatchContextRequests validates the ENTIRE batch before the first write.
// Transport failures are best-effort across independent targets, but the batch
// returns an error unless every target succeeded. Cancellation stops the batch.
func dispatchContextRequests(ctx context.Context, session string, requests []contextInjectionRequest, dryRun bool, send contextPaneSender) ([]ContextPaneDelivery, error) {
	receipts := make([]ContextPaneDelivery, len(requests))
	for i, req := range requests {
		receipts[i] = ContextPaneDelivery{
			Pane: req.Pane.Index, Target: req.Pane.ID, AgentType: string(req.Pane.Type),
			PackID: req.PackID, Bytes: len(req.Content), Status: "not_attempted", Capacity: req.Capacity,
		}
	}
	if err := ctx.Err(); err != nil {
		return receipts, err
	}
	if len(requests) == 0 {
		return receipts, fmt.Errorf("no target panes for context injection in session %s", session)
	}
	seen := make(map[string]bool, len(requests))
	for _, req := range requests {
		if strings.TrimSpace(req.Pane.ID) == "" {
			return receipts, fmt.Errorf("context injection for pane %d requires an explicit target ID", req.Pane.Index)
		}
		if seen[req.Pane.ID] {
			return receipts, fmt.Errorf("duplicate context injection target %s", req.Pane.ID)
		}
		seen[req.Pane.ID] = true
		if strings.TrimSpace(req.Content) == "" {
			return receipts, fmt.Errorf("context injection for pane %d has empty content", req.Pane.Index)
		}
		if req.MaxBytes < 0 {
			return receipts, fmt.Errorf("context injection maxBytes must be >= 0")
		}
		if err := req.Pane.Type.ValidateAutomatedPromptDelivery(); err != nil {
			return receipts, fmt.Errorf("context injection for pane %d: %w", req.Pane.Index, err)
		}
	}
	if !dryRun && send == nil {
		return receipts, fmt.Errorf("context injection sender is required")
	}
	service, err := dispatch.NewService(dispatch.Ports{
		Redactor: shellFinalMessageRedactor(activeShellDispatchRedactionConfig().DeepCopy()),
		Deliverer: dispatch.DelivererFunc(func(ctx context.Context, delivery dispatch.Delivery) error {
			return send(ctx, delivery.Target.Pane, delivery.Message)
		}),
	})
	if err != nil {
		return receipts, err
	}
	// Preflight every final, per-pane payload before dispatching any of them.
	// Keeping the prepared intents also ensures the bytes scanned by policy are
	// exactly those sent, without modifying a cached or persisted context pack.
	prepared := make([]*dispatch.Prepared, len(requests))
	for i, req := range requests {
		prepared[i], err = service.Prepare(ctx, dispatch.Request{
			Session: session, Panes: []tmux.Pane{req.Pane}, Selectors: []string{req.Pane.ID},
			Message: req.Content, Submit: true, DryRun: dryRun,
		})
		if prepared[i] != nil {
			preflight := prepared[i].PreflightResult()
			if len(preflight.Receipts) == 1 {
				summary := preflight.Receipts[0].Redaction
				receipts[i].Redaction = &summary
			}
		}
		if err != nil {
			receipts[i].Error = err.Error()
			return receipts, err
		}
		message, messageErr := prepared[i].FinalMessageForSingleTarget()
		if messageErr != nil {
			return receipts, messageErr
		}
		receipts[i].Bytes = len(message)
		// Placeholders can be longer than the matched secret. A successful raw
		// budget check must never authorize an oversized redacted message.
		if req.MaxBytes > 0 && len(message) > req.MaxBytes {
			err = fmt.Errorf("final context for pane %d (%s) exceeds --max-bytes after redaction; refusing to truncate", req.Pane.Index, req.Pane.ID)
		} else if req.Capacity != nil && (req.Capacity.BudgetTokens <= 0 || contextPayloadTokens(message) > req.Capacity.BudgetTokens) {
			err = fmt.Errorf("final context for pane %d (%s) exceeds context budget of %d estimated tokens after redaction", req.Pane.Index, req.Pane.ID, req.Capacity.BudgetTokens)
		}
		if err != nil {
			receipts[i].Error = err.Error()
			return receipts, err
		}
	}
	if err := ctx.Err(); err != nil {
		return receipts, err
	}
	if dryRun {
		for i := range receipts {
			receipts[i].Status = "planned"
		}
		return receipts, nil
	}
	var failures []error
	delivered := make([]int, 0, len(requests))
	for i, req := range requests {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		outcome, err := service.Dispatch(ctx, prepared[i])
		if err != nil {
			receipts[i].Status = "uncertain"
			if dispatch.IsGuaranteedNoDeliveryActuation(err) || (len(outcome.Receipts) == 1 && outcome.Receipts[0].Status == dispatch.ReceiptSkipped) {
				receipts[i].Status = "not_attempted"
			}
			receipts[i].Error = err.Error()
			failures = append(failures, fmt.Errorf("pane %d (%s): %w", req.Pane.Index, req.Pane.ID, err))
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			continue
		}
		receipts[i].Status = "delivered"
		delivered = append(delivered, req.Pane.Index)
	}
	if len(failures) != 0 {
		return receipts, fmt.Errorf("context injection incomplete in session %s; delivered panes %v; inspect uncertain targets before retrying: %w", session, delivered, errors.Join(failures...))
	}
	return receipts, nil
}

func contextInjectionResult(session string, files []string, size int, truncated, dryRun bool, receipts []ContextPaneDelivery, err error) ContextInjectResult {
	result := ContextInjectResult{
		Success: err == nil, Session: session, InjectedFiles: append([]string{}, files...),
		TotalBytes: size, Truncated: truncated, DryRun: dryRun,
		PanesInjected: []int{}, Deliveries: receipts,
	}
	if len(receipts) > 0 {
		result.TotalBytes = 0
	}
	for _, receipt := range receipts {
		// Uniform file injections retain their per-message size; mixed packs
		// report the largest final payload, with exact per-pane byte counts.
		if receipt.Bytes > result.TotalBytes {
			result.TotalBytes = receipt.Bytes
		}
		if summary := receipt.Redaction; summary != nil && summary.Findings > 0 {
			action := "potential secrets detected in context"
			if summary.Mode == "redact" {
				action = "redacted potential secrets in context"
			} else if summary.Blocked {
				action = "blocked potential secrets in context"
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("pane %d (%s): %s (redaction mode: %s; %s)",
				receipt.Pane, receipt.Target, action, summary.Mode, formatRedactionCategoryCounts(summary.Categories)))
		}
		switch receipt.Status {
		case "delivered":
			result.PanesInjected = append(result.PanesInjected, receipt.Pane)
		case "planned":
			result.PanesPlanned = append(result.PanesPlanned, receipt.Pane)
		}
	}
	if err != nil {
		result.Error = err.Error()
		result.ErrorCode, result.Hint = classifyRobotContextInjectError(err)
	}
	return result
}

type contextInjectOptions struct {
	Session, FilesArg, PackID, Task, BeadID, Pane  string
	FilesSet, PackSet, PaneSet, Build, All, DryRun bool
	MaxBytes                                       int
}

type contextInjectDeps struct {
	resolve   func(context.Context, string) (string, error)
	project   func(context.Context, string) (string, error)
	panes     func(string) ([]tmux.Pane, error)
	build     func(context.Context, ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error)
	load      func(string) (*state.ContextPack, error)
	usage     func(context.Context, tmux.Pane) (*ntmctx.TranscriptUsage, bool)
	send      contextPaneSender
	includeMS bool
	remote    bool
}

// runContextInjection is the command's preparation-to-delivery transaction.
// All pack builds and validation finish before dispatch, even for mixed-agent
// batches. It neither persists generated packs nor retries uncertain sends.
func runContextInjection(ctx context.Context, opts contextInjectOptions, deps contextInjectDeps) (result ContextInjectResult, err error) {
	result = contextInjectionResult(opts.Session, nil, 0, false, opts.DryRun, nil, nil)
	result.Mode = "files"
	defer func() {
		if result.InjectedFiles == nil {
			result.InjectedFiles = []string{}
		}
		if result.PanesInjected == nil {
			result.PanesInjected = []int{}
		}
		if err != nil {
			result.Success = false
			result.Error = err.Error()
		}
	}()
	opts.PackID = strings.TrimSpace(opts.PackID)
	opts.Pane = strings.TrimSpace(opts.Pane)
	hasPack := opts.PackSet || opts.PackID != ""
	hasFiles := opts.FilesSet || opts.FilesArg != ""
	hasPane := opts.PaneSet || opts.Pane != ""
	if hasPack {
		result.Mode = "pack"
	} else if opts.Build {
		result.Mode = "build"
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(opts.Session) == "" {
		return result, fmt.Errorf("session name is required")
	}
	if opts.MaxBytes < 0 {
		return result, fmt.Errorf("maxBytes must be >= 0, got %d", opts.MaxBytes)
	}
	if hasPane {
		if _, err := tmux.ParsePaneSelector(opts.Pane); err != nil {
			return result, err
		}
	}
	if opts.All && hasPane {
		return result, fmt.Errorf("--all and --pane are mutually exclusive")
	}
	if hasPack && opts.PackID == "" {
		return result, fmt.Errorf("--pack requires a non-empty pack ID")
	}
	if hasPack && (opts.Build || hasFiles || opts.Task != "" || opts.BeadID != "") {
		return result, fmt.Errorf("--pack cannot be combined with --build, --files, --task, or --bead")
	}
	if !opts.Build && (opts.Task != "" || opts.BeadID != "") {
		return result, fmt.Errorf("--task and --bead require --build")
	}
	files := defaultContextFiles()
	if hasFiles {
		files = strings.Split(opts.FilesArg, ",")
		for i := range files {
			files[i] = strings.TrimSpace(files[i])
			if files[i] == "" {
				return result, fmt.Errorf("--files contains an empty path")
			}
		}
	}
	if deps.remote && !hasPack {
		return result, ntmctx.ErrRemoteContextPreparation
	}
	if deps.resolve == nil || deps.panes == nil {
		return result, fmt.Errorf("context injection session dependencies are required")
	}
	session, err := deps.resolve(ctx, opts.Session)
	if err != nil {
		return result, err
	}
	result.Session = session
	if err := ctx.Err(); err != nil {
		return result, err
	}

	var projectDir, content string
	if !hasPack {
		if deps.project == nil {
			return result, fmt.Errorf("context injection project resolver is required")
		}
		projectDir, err = deps.project(ctx, session)
		if err != nil {
			return result, err
		}
		if strings.TrimSpace(projectDir) == "" {
			return result, fmt.Errorf("context injection project directory is empty")
		}
		if opts.Build && !hasFiles {
			// Default documentation files are optional; explicit selections are
			// not. The builder still performs root-confined reads for every file.
			present := make([]string, 0, len(files))
			for _, file := range files {
				if _, statErr := os.Stat(filepath.Join(projectDir, file)); statErr == nil {
					present = append(present, file)
				} else if !os.IsNotExist(statErr) {
					return result, statErr
				}
			}
			files = present
		}
		if !opts.Build {
			content, result.InjectedFiles, result.Truncated, err = formatContextInjectContent(projectDir, files, opts.MaxBytes)
			if err != nil {
				return result, err
			}
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if content == "" {
				return result, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	panes, err := deps.panes(session)
	if err != nil {
		return result, fmt.Errorf("get panes: %w", err)
	}
	if opts.Build || hasPack {
		panes, err = selectContextPackPanes(panes, opts.Pane, opts.All, session)
	} else {
		panes, err = selectContextInjectionPanes(panes, opts.Pane, opts.All, session)
	}
	if err != nil {
		return result, err
	}
	if len(panes) == 0 {
		return result, fmt.Errorf("no target panes for context injection in session %s", session)
	}
	requests := uniformContextRequests(panes, content)
	// Retain unattempted targets when preparation fails. No partially built
	// batch can accidentally reach dispatch.
	for i, pane := range panes {
		requests[i].MaxBytes = opts.MaxBytes
		result.Deliveries = append(result.Deliveries, ContextPaneDelivery{
			Pane: pane.Index, Target: pane.ID, AgentType: string(pane.Type), Status: "not_attempted",
		})
	}
	if opts.Build || hasPack {
		observations, observeErr := observeContextPackTargets(ctx, panes, deps)
		for i := range observations {
			requests[i].Capacity = &observations[i].capacity
			result.Deliveries[i].Capacity = requests[i].Capacity
		}
		if observeErr != nil {
			return result, observeErr
		}
		var stored *state.ContextPack
		if hasPack {
			if deps.load == nil {
				return result, fmt.Errorf("context pack loader is required")
			}
			if err := ctx.Err(); err != nil {
				return result, err
			}
			stored, err = deps.load(opts.PackID)
			if err != nil {
				return result, fmt.Errorf("get context pack: %w", err)
			}
			if stored == nil {
				return result, fmt.Errorf("context pack not found: %s", opts.PackID)
			}
			if stored.ID != opts.PackID {
				return result, fmt.Errorf("context pack ID does not match requested artifact")
			}
		} else {
			if deps.build == nil {
				return result, fmt.Errorf("context pack builder is required")
			}
			result.SelectedFiles = append([]string{}, files...)
		}
		built := make(map[contextPackBuildKey]*ntmctx.ContextPackFull)
		for i, pane := range panes {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			typ := agent.AgentType(pane.Type).Canonical()
			capacity := observations[i].capacity
			key := contextPackBuildKey{agentType: typ, budget: capacity.BudgetTokens}
			if capacity.Status == "measured" {
				key.model, key.window = capacity.Model, capacity.ContextWindow
			}
			pack := stored
			if opts.Build {
				full, ok := built[key]
				if !ok {
					full, err = deps.build(ctx, ntmctx.BuildOptions{
						AgentType: string(typ), ProjectDir: projectDir, SessionID: session,
						Model: key.model, ContextWindow: key.window, MaxTokens: key.budget,
						Remote:  deps.remote,
						RepoRev: getRepoRev(projectDir), Files: append([]string{}, files...),
						Task: opts.Task, BeadID: opts.BeadID, IncludeMSSkills: deps.includeMS,
					})
					if err != nil {
						return result, fmt.Errorf("build %s context: %w", typ, err)
					}
					if full == nil {
						return result, fmt.Errorf("build %s context returned no pack", typ)
					}
					if len(files) > 0 {
						source := full.Components["s2p"]
						if source != nil && source.Error != "" {
							return result, fmt.Errorf("build %s source context: %s", typ, source.Error)
						}
						var sourceText string
						if source == nil || json.Unmarshal(source.Data, &sourceText) != nil || strings.TrimSpace(sourceText) == "" {
							return result, fmt.Errorf("build %s context: selected source files were not prepared", typ)
						}
					}
					built[key] = full
					keys := make([]string, 0, len(full.Components))
					for name := range full.Components {
						keys = append(keys, name)
					}
					sort.Strings(keys)
					for _, name := range keys {
						comp := full.Components[name]
						if comp == nil {
							continue
						}
						if comp.Error != "" {
							result.Warnings = append(result.Warnings, fmt.Sprintf("%s %s: %s", typ, name, comp.Error))
						}
						result.Truncated = result.Truncated || comp.Truncated
					}
				}
				pack = &full.ContextPack
			}
			if err := validateContextPackForPane(pack, pane, opts.MaxBytes, key.budget); err != nil {
				return result, err
			}
			requests[i].Content, requests[i].PackID = pack.RenderedPrompt, pack.ID
			result.Deliveries[i].PackID, result.Deliveries[i].Bytes = pack.ID, len(pack.RenderedPrompt)
		}
		latest, recheckErr := recheckContextPackTargets(ctx, session, requests, observations, deps)
		for i := range latest {
			requests[i].Capacity = &latest[i].capacity
			result.Deliveries[i].Capacity = requests[i].Capacity
			if latest[i].capacity.Status != "measured" {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"pane %d (%s): %s; using an unverified agent-default pack limit of %d estimated tokens",
					panes[i].Index, panes[i].ID, latest[i].capacity.Reason, latest[i].capacity.BudgetTokens))
			}
		}
		if recheckErr != nil {
			return result, recheckErr
		}
	}
	receipts, err := dispatchContextRequests(ctx, session, requests, opts.DryRun, deps.send)
	delivery := contextInjectionResult(session, result.InjectedFiles, result.TotalBytes, result.Truncated, opts.DryRun, receipts, err)
	delivery.Mode, delivery.SelectedFiles = result.Mode, result.SelectedFiles
	delivery.Warnings = append(result.Warnings, delivery.Warnings...)
	return delivery, err
}

// Pack delivery must never infer "agent" from a nonzero pane index. Sessions
// created with --no-user legitimately have an agent at index zero.
func selectContextPackPanes(panes []tmux.Pane, selector string, all bool, session string) ([]tmux.Pane, error) {
	selected, err := selectContextInjectionPanes(panes, selector, all, session)
	if err != nil {
		return nil, err
	}
	for _, pane := range selected {
		typ := agent.AgentType(pane.Type).Canonical()
		if !typ.IsValid() || typ == agent.AgentTypeUser {
			return nil, fmt.Errorf("pane %d (%s) is not a known agent; refusing context pack delivery", pane.Index, pane.ID)
		}
	}
	return selected, nil
}

func validateContextPackForPane(pack *state.ContextPack, pane tmux.Pane, maxBytes, budget int) error {
	if pack == nil || strings.TrimSpace(pack.ID) == "" || strings.TrimSpace(pack.RenderedPrompt) == "" {
		return fmt.Errorf("context pack is empty or invalid")
	}
	typ := agent.AgentType(pane.Type).Canonical()
	packType := agent.AgentType(pack.AgentType).Canonical()
	if !typ.IsValid() || typ == agent.AgentTypeUser || packType != typ {
		return fmt.Errorf("pack %s is for %s, not pane %d (%s); use --build or select a matching --pane", pack.ID, packType, pane.Index, typ)
	}
	if !utf8.ValidString(pack.RenderedPrompt) || strings.ContainsRune(pack.RenderedPrompt, 0) {
		return fmt.Errorf("pack %s contains invalid text", pack.ID)
	}
	// Measure the actual payload, not the stored TokenCount. This deliberately
	// uses the same approximate four-byte convention as the context builder.
	bytes := len(pack.RenderedPrompt)
	if budget <= 0 || contextPayloadTokens(pack.RenderedPrompt) > budget {
		return fmt.Errorf("pack %s exceeds pane %d (%s) context budget of %d estimated tokens", pack.ID, pane.Index, pane.ID, budget)
	}
	if maxBytes > 0 && bytes > maxBytes {
		return fmt.Errorf("pack %s exceeds --max-bytes; refusing to truncate a rendered artifact", pack.ID)
	}
	return nil
}

// observeContextPackTargets bounds a whole observation phase, not each pane.
// Only process-bound transcripts may supply occupancy; remote PIDs must never
// be inspected locally. Classify timestamps after all probes have completed.
func observeContextPackTargets(ctx context.Context, panes []tmux.Pane, deps contextInjectDeps) ([]contextPackObservation, error) {
	observations := make([]contextPackObservation, len(panes))
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for i, pane := range panes {
		if err := ctx.Err(); err != nil {
			return observations, err
		}
		if pane.Dead || pane.IsServicePane() || pane.RotationToken != "" {
			return observations, fmt.Errorf("pane %d (%s) is not available for context delivery", pane.Index, pane.ID)
		}
		if deps.remote || deps.usage == nil || probeCtx.Err() != nil {
			continue
		}
		if usage, ok := deps.usage(probeCtx, pane); ok && usage != nil && probeCtx.Err() == nil {
			copy := *usage
			observations[i].usage = &copy
		}
	}
	if err := ctx.Err(); err != nil {
		return observations, err
	}
	now := time.Now()
	var failures []error
	for i, pane := range panes {
		capacity, err := contextCapacityForPane(pane, observations[i].usage, deps.remote, now)
		observations[i].capacity = capacity
		if err == nil && capacity.BudgetTokens <= 0 {
			err = fmt.Errorf("pane %d (%s) has no usable context pack budget", pane.Index, pane.ID)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return observations, errors.Join(failures...)
}

func contextCapacityForPane(pane tmux.Pane, usage *ntmctx.TranscriptUsage, remote bool, now time.Time) (ContextPaneCapacity, error) {
	typ := string(agent.AgentType(pane.Type).Canonical())
	capacity := ContextPaneCapacity{
		Status: "unknown", BudgetSource: "agent_default",
		BudgetTokens: ntmctx.GetTokenBudget(typ),
		Reason:       "fresh process-bound context accounting is unavailable",
	}
	if remote {
		capacity.Reason = "remote context occupancy is unavailable; local process evidence was not used"
		return capacity, nil
	}
	if usage == nil || usage.Path == "" {
		return capacity, nil
	}
	if !usage.UpdatedAt.IsZero() {
		updated := usage.UpdatedAt
		capacity.UpdatedAt = &updated
	}
	if usage.Tokens < 0 || usage.ContextWindow < 0 {
		capacity.Reason = "native context accounting contains invalid counters"
		return capacity, nil
	}
	if ntmctx.TranscriptConfidence(usage.UpdatedAt, now) != "high" {
		capacity.Status = "stale"
		capacity.Reason = "native context accounting is not current"
		return capacity, nil
	}
	model := strings.ToLower(strings.TrimSpace(usage.Model))
	window := usage.ContextWindow
	if window == 0 {
		if !models.KnownModel(model) {
			capacity.Reason = "native accounting has no recognized model or reported context window"
			return capacity, nil
		}
		window = models.GetContextLimit(model)
	}
	capacity.Status = "measured"
	capacity.BudgetSource = "remaining_context"
	capacity.Model, capacity.ContextWindow = model, window
	used, available := usage.Tokens, max(0, window-usage.Tokens)
	capacity.UsedTokens, capacity.AvailableTokens = &used, &available
	capacity.Reason = ""
	capacity.BudgetTokens = 0
	if available == 0 {
		return capacity, fmt.Errorf("pane %d (%s) has no remaining context capacity", pane.Index, pane.ID)
	}
	// Apply the pack policy to remaining space. Subtracting occupancy from
	// the full-window pack budget would wrongly reject a Gemini session
	// after 10% occupancy, because its 10% policy limits one pack.
	budget, err := models.GetTokenBudgetForModel(typ, model, available)
	if err != nil {
		return capacity, fmt.Errorf("pane %d (%s) remaining context budget: %w", pane.Index, pane.ID, err)
	}
	capacity.BudgetTokens = budget
	return capacity, nil
}

// recheckContextPackTargets refreshes the entire batch before the first send.
// Losing evidence used to prepare a pack is an admission failure, not license
// to fall back to an assumed empty session. Newly available evidence also
// constrains a pack that was prepared with an unverified default.
func recheckContextPackTargets(ctx context.Context, session string, requests []contextInjectionRequest, initial []contextPackObservation, deps contextInjectDeps) ([]contextPackObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := deps.panes(session)
	if err != nil {
		return nil, fmt.Errorf("refresh context targets: %w", err)
	}
	byID := make(map[string]tmux.Pane, len(current))
	for _, pane := range current {
		if _, exists := byID[pane.ID]; exists {
			return nil, fmt.Errorf("duplicate physical pane %s in context target refresh", pane.ID)
		}
		byID[pane.ID] = pane
	}
	panes := make([]tmux.Pane, len(requests))
	for i, req := range requests {
		pane, exists := byID[req.Pane.ID]
		if !exists || pane.PID != req.Pane.PID ||
			agent.AgentType(pane.Type).Canonical() != agent.AgentType(req.Pane.Type).Canonical() ||
			pane.Dead || pane.IsServicePane() || pane.RotationToken != "" {
			return nil, fmt.Errorf("context target %s changed or became unavailable during preparation", req.Pane.ID)
		}
		panes[i] = pane
	}
	latest, err := observeContextPackTargets(ctx, panes, deps)
	if err != nil {
		return latest, err
	}
	for i, req := range requests {
		before, after := initial[i], latest[i]
		if before.capacity.Status == "measured" {
			if after.capacity.Status != "measured" {
				return latest, fmt.Errorf("pane %d (%s) lost fresh context capacity evidence before delivery", req.Pane.Index, req.Pane.ID)
			}
			if before.usage.Path != after.usage.Path || before.usage.ProviderSessionID != after.usage.ProviderSessionID ||
				before.capacity.Model != after.capacity.Model || before.capacity.ContextWindow != after.capacity.ContextWindow ||
				after.usage.UpdatedAt.Before(before.usage.UpdatedAt) {
				return latest, fmt.Errorf("pane %d (%s) context session or model changed during preparation", req.Pane.Index, req.Pane.ID)
			}
		}
		if contextPayloadTokens(req.Content) > after.capacity.BudgetTokens {
			return latest, fmt.Errorf("pack %s exceeds pane %d (%s) context budget of %d estimated tokens before delivery", req.PackID, req.Pane.Index, req.Pane.ID, after.capacity.BudgetTokens)
		}
	}
	return latest, nil
}

func contextPayloadTokens(content string) int {
	tokens := len(content) / 4
	if len(content)%4 != 0 {
		tokens++
	}
	return tokens
}

type contextPackStore interface {
	GetContextPack(string) (*state.ContextPack, error)
	CreateContextPack(*state.ContextPack) error
}

// persistContextPack makes context build's printed ID reusable by --pack.
// Repeated cache hits are idempotent, even across distinct state stores, but
// an existing ID with different content is never overwritten.
func persistContextPack(ctx context.Context, store contextPackStore, pack *state.ContextPack) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || pack == nil || strings.TrimSpace(pack.ID) == "" {
		return fmt.Errorf("context pack persistence requires a store and valid pack")
	}
	copy := *pack
	read := func() (*state.ContextPack, error) {
		existing, err := store.GetContextPack(copy.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return existing, err
	}
	same := func(existing *state.ContextPack) bool {
		return existing != nil && existing.ID == copy.ID && existing.BeadID == copy.BeadID &&
			existing.AgentType == copy.AgentType && existing.RepoRev == copy.RepoRev &&
			existing.CorrelationID == copy.CorrelationID && existing.CreatedAt.Equal(copy.CreatedAt) &&
			existing.TokenCount == copy.TokenCount && existing.RenderedPrompt == copy.RenderedPrompt
	}
	existing, err := read()
	if err != nil {
		return fmt.Errorf("read context pack before persistence: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if existing != nil {
		if same(existing) {
			return nil
		}
		return fmt.Errorf("context pack %s already exists with different content; refusing to overwrite", copy.ID)
	}
	if err := store.CreateContextPack(&copy); err != nil {
		// A concurrent process may have inserted the identical artifact after
		// our read. Only verified equality can turn that error into success.
		existing, readErr := read()
		if readErr == nil && same(existing) {
			return nil
		}
		return fmt.Errorf("persist context pack %s: %w", copy.ID, errors.Join(err, readErr))
	}
	return nil
}
