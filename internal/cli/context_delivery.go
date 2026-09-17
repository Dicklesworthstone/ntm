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
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ContextPaneDelivery is a per-target receipt, not proof that an agent has
// finished processing the context. A failed send may have written some bytes:
// uncertain is deliberately distinct from not_attempted and is never retried.
type ContextPaneDelivery struct {
	Pane      int    `json:"pane"`
	Target    string `json:"target"`
	AgentType string `json:"agent_type"`
	PackID    string `json:"pack_id,omitempty"`
	Bytes     int    `json:"bytes"`
	Status    string `json:"status"` // planned | delivered | uncertain | not_attempted
	Error     string `json:"error,omitempty"`
}

type contextInjectionRequest struct {
	Pane    tmux.Pane
	Content string
	PackID  string
}

type contextPaneSender func(context.Context, tmux.Pane, string) error

func uniformContextRequests(panes []tmux.Pane, content string) []contextInjectionRequest {
	requests := make([]contextInjectionRequest, len(panes))
	for i, pane := range panes {
		requests[i] = contextInjectionRequest{Pane: pane, Content: content}
	}
	return requests
}

// sendContextToPane uses the provider's multiline and submit protocol rather
// than raw send-keys, which can submit each line as a separate agent prompt.
func sendContextToPane(ctx context.Context, pane tmux.Pane, content string) error {
	return tmux.DefaultClient.SendKeysForAgentContext(ctx, pane.ID, content, true, pane.Type)
}

// dispatchContextRequests validates the ENTIRE batch before the first write.
// Transport failures are best-effort across independent targets, but the batch
// returns an error unless every target succeeded. Cancellation stops the batch.
func dispatchContextRequests(ctx context.Context, session string, requests []contextInjectionRequest, dryRun bool, send contextPaneSender) ([]ContextPaneDelivery, error) {
	receipts := make([]ContextPaneDelivery, len(requests))
	for i, req := range requests {
		receipts[i] = ContextPaneDelivery{
			Pane: req.Pane.Index, Target: req.Pane.ID, AgentType: string(req.Pane.Type),
			PackID: req.PackID, Bytes: len(req.Content), Status: "not_attempted",
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
		if err := req.Pane.Type.ValidateAutomatedPromptDelivery(); err != nil {
			return receipts, fmt.Errorf("context injection for pane %d: %w", req.Pane.Index, err)
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
	if send == nil {
		return receipts, fmt.Errorf("context injection sender is required")
	}

	var failures []error
	delivered := make([]int, 0, len(requests))
	for i, req := range requests {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if err := send(ctx, req.Pane, req.Content); err != nil {
			receipts[i].Status = "uncertain"
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
	for _, receipt := range receipts {
		switch receipt.Status {
		case "delivered":
			result.PanesInjected = append(result.PanesInjected, receipt.Pane)
		case "planned":
			result.PanesPlanned = append(result.PanesPlanned, receipt.Pane)
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

type contextInjectOptions struct {
	Session, FilesArg, PackID, Task, BeadID string
	FilesSet, PackSet, Build, All, DryRun   bool
	Pane, MaxBytes                          int
}

type contextInjectDeps struct {
	resolve   func(context.Context, string) (string, error)
	project   func(context.Context, string) (string, error)
	panes     func(string) ([]tmux.Pane, error)
	build     func(context.Context, ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error)
	load      func(string) (*state.ContextPack, error)
	send      contextPaneSender
	includeMS bool
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
	hasPack := opts.PackSet || opts.PackID != ""
	hasFiles := opts.FilesSet || opts.FilesArg != ""
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
	if opts.Pane < -1 {
		return result, fmt.Errorf("pane index must be >= 0")
	}
	if opts.All && opts.Pane >= 0 {
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
		panes, err = selectContextInjectTargetPanes(panes, opts.Pane, opts.All, session)
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
	for _, pane := range panes {
		result.Deliveries = append(result.Deliveries, ContextPaneDelivery{
			Pane: pane.Index, Target: pane.ID, AgentType: string(pane.Type), Status: "not_attempted",
		})
	}
	if opts.Build || hasPack {
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
		built := make(map[agent.AgentType]*ntmctx.ContextPackFull)
		for i, pane := range panes {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			typ := agent.AgentType(pane.Type).Canonical()
			pack := stored
			if opts.Build {
				full, ok := built[typ]
				if !ok {
					full, err = deps.build(ctx, ntmctx.BuildOptions{
						AgentType: string(typ), ProjectDir: projectDir, SessionID: session,
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
					built[typ] = full
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
			if err := validateContextPackForPane(pack, pane, opts.MaxBytes); err != nil {
				return result, err
			}
			requests[i].Content, requests[i].PackID = pack.RenderedPrompt, pack.ID
		}
	}
	for _, req := range requests {
		// TotalBytes retains the uniform-message meaning for file injection;
		// mixed packs report the largest payload, with exact per-pane Bytes.
		if len(req.Content) > result.TotalBytes {
			result.TotalBytes = len(req.Content)
		}
	}
	receipts, err := dispatchContextRequests(ctx, session, requests, opts.DryRun, deps.send)
	delivery := contextInjectionResult(session, result.InjectedFiles, result.TotalBytes, result.Truncated, opts.DryRun, receipts, err)
	delivery.Mode, delivery.SelectedFiles, delivery.Warnings = result.Mode, result.SelectedFiles, result.Warnings
	return delivery, err
}

// Pack delivery must never infer "agent" from a nonzero pane index. Sessions
// created with --no-user legitimately have an agent at index zero.
func selectContextPackPanes(panes []tmux.Pane, index int, all bool, session string) ([]tmux.Pane, error) {
	selected := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		typ := agent.AgentType(pane.Type).Canonical()
		if index >= 0 {
			if pane.Index == index {
				selected = append(selected, pane)
			}
		} else if all || (typ.IsValid() && typ != agent.AgentTypeUser) {
			selected = append(selected, pane)
		}
	}
	if index >= 0 && len(selected) != 1 {
		return nil, fmt.Errorf("pane index %d in session %s matched %d targets; use an unambiguous session", index, session, len(selected))
	}
	for _, pane := range selected {
		typ := agent.AgentType(pane.Type).Canonical()
		if !typ.IsValid() || typ == agent.AgentTypeUser {
			return nil, fmt.Errorf("pane %d (%s) is not a known agent; refusing context pack delivery", pane.Index, pane.ID)
		}
	}
	return selected, nil
}

func validateContextPackForPane(pack *state.ContextPack, pane tmux.Pane, maxBytes int) error {
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
	tokens := bytes / 4
	if bytes%4 != 0 {
		tokens++
	}
	if tokens > ntmctx.GetTokenBudget(string(typ)) {
		return fmt.Errorf("pack %s exceeds the token budget for %s", pack.ID, typ)
	}
	if maxBytes > 0 && bytes > maxBytes {
		return fmt.Errorf("pack %s exceeds --max-bytes; refusing to truncate a rendered artifact", pack.ID)
	}
	return nil
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
