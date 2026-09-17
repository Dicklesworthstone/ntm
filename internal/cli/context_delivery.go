package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
