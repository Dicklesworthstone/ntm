package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// Stage represents a step in the pipeline
type Stage struct {
	AgentType string
	Prompt    string
	Model     string // Optional
}

// Pipeline represents a sequence of stages
type Pipeline struct {
	Session string
	Stages  []Stage
}

// Execute runs the pipeline stages sequentially
func Execute(ctx context.Context, p Pipeline) error {
	if ctx == nil {
		return fmt.Errorf("pipeline execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var previousOutput string
	var previousDelivery AgentDeliveryState
	executor := NewExecutor(DefaultExecutorConfig(p.Session))

	for i, stage := range p.Stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.Printf("Stage %d/%d [%s]: %s", i+1, len(p.Stages), stage.AgentType, util.Truncate(stage.Prompt, 50))

		// 1. Find a suitable pane
		paneID, err := findPaneForStage(p.Session, stage.AgentType, stage.Model)
		if err != nil {
			return fmt.Errorf("stage %d failed: %w", i+1, err)
		}

		// Use the same ownership, readiness and lifetime evidence as file
		// workflows. The shorthand is not permission to paste into a draft or
		// to overlap another pipeline's capture/response window.
		stageCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		delivery, err := executor.executeAdHocStage(stageCtx, stage, paneID, previousOutput, previousDelivery)
		cancel()
		if err != nil {
			return fmt.Errorf("stage %d failed: %w", i+1, err)
		}
		previousOutput, previousDelivery = delivery.Output, delivery
	}

	return nil
}

// executeAdHocStage is intentionally not a retrying or resumable workflow.
// Once transport is attempted, an error stops the chain and leaves the pane
// for inspection. A later invocation is a new operator-requested execution.
func (e *Executor) executeAdHocStage(ctx context.Context, stage Stage, paneID, previousOutput string, previous AgentDeliveryState) (AgentDeliveryState, error) {
	agentType := normalizeAgentType(stage.AgentType)
	record := AgentDeliveryState{Session: e.config.Session, PaneID: paneID, Endpoint: e.agentDeliveryEndpoint(), AgentType: agentType}
	pane, err := e.agentDeliveryPane(ctx, paneID, 0, record.Endpoint)
	if err != nil {
		return record, err
	}
	record.PanePID = pane.PID
	release, err := e.acquirePaneLockCrossProcess(ctx, paneID)
	if err != nil {
		return record, err
	}
	defer release()

	// Waiting for the lock may outlive the selected process. Do not redirect
	// a queued stage to its replacement, even when the physical pane ID stays.
	pane, err = e.agentDeliveryPane(ctx, paneID, record.PanePID, record.Endpoint)
	if err != nil {
		return record, err
	}
	if normalizeAgentType(string(pane.Type)) != agentType || pane.IsServicePane() {
		return record, fmt.Errorf("selected pane %s no longer matches stage agent %s", paneID, stage.AgentType)
	}
	if err := e.tmuxClient().PrepareDelivery(ctx, pane, agentType); err != nil {
		return record, fmt.Errorf("stage not dispatched: %w", err)
	}
	before, err := e.captureAgentDelivery(ctx, record)
	if err != nil {
		return record, fmt.Errorf("capture before dispatch: %w", err)
	}
	prompt := stage.Prompt
	if previousOutput != "" {
		if previous.PaneID == paneID && previous.PanePID == record.PanePID && previous.Endpoint == record.Endpoint {
			prompt += "\n\n(See previous output above)"
		} else {
			prompt += "\n\nResult from previous stage:\n" + previousOutput
		}
	}
	// Capture can block or race an interactive UI transition. Refresh the
	// shared composer gate and process identity immediately before transport.
	if err := e.tmuxClient().PrepareDelivery(ctx, pane, agentType); err != nil {
		return record, fmt.Errorf("stage not dispatched: %w", err)
	}
	pane, err = e.agentDeliveryPane(ctx, paneID, record.PanePID, record.Endpoint)
	if err != nil {
		return record, err
	}
	if err := dispatch.RefuseDeadAgentPane(pane); err != nil {
		return record, err
	}
	if normalizeAgentType(string(pane.Type)) != agentType || pane.IsServicePane() {
		return record, fmt.Errorf("selected pane %s changed agent identity before dispatch", paneID)
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if client, ok := e.tmuxClient().(interface {
		PasteKeysContext(context.Context, string, string, bool) error
	}); ok {
		err = client.PasteKeysContext(ctx, paneID, prompt, true)
	} else {
		err = e.tmuxClient().PasteKeys(paneID, prompt, true)
	}
	if err != nil {
		return record, fmt.Errorf("sending stage prompt; inspect pane before retrying: %w", err)
	}
	if err := e.tmuxClient().VerifySubmission(ctx, paneID, prompt, agentType, pane.Width); err != nil {
		return record, fmt.Errorf("stage submission not confirmed; inspect pane before retrying: %w", err)
	}
	collect := func() (bool, error) {
		current, err := e.captureAgentDelivery(ctx, record)
		if err != nil {
			return false, err
		}
		delta, err := agentDeliveryNewOutput(before, current)
		if err != nil {
			return false, err
		}
		if len(record.Output)+len(delta) > agentDeliveryMaxOutputBytes {
			return false, fmt.Errorf("stage response exceeds the %d-byte evidence limit", agentDeliveryMaxOutputBytes)
		}
		before, record.Output = current, record.Output+delta
		response := normalizeAgentEcho(record.Output)
		return response != "" && response != normalizeAgentEcho(prompt), nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return record, ctx.Err()
	case <-timer.C:
	}
	if err := waitForIdle(ctx, e.detector, paneID, collect); err != nil {
		return record, fmt.Errorf("waiting for stage completion: %w", err)
	}
	// The last detector call may have outlived cancellation or pane respawn.
	// Require one more validated capture before exposing output downstream.
	if _, err := collect(); err != nil {
		return record, err
	}
	return record, ctx.Err()
}

// Shorthand stages must pass their deadline through the actual paste, not only
// the surrounding preflight. Older injected TmuxClients still use PasteKeys.
func (realTmuxClient) PasteKeysContext(ctx context.Context, target, content string, enter bool) error {
	return tmux.DefaultClient.PasteKeysContext(ctx, target, content, enter)
}

func findPaneForStage(session, agentType, model string) (string, error) {
	targetType := normalizeAgentType(agentType)
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return "", err
	}

	// First pass: look for exact match (type + model). Normalize the pane's
	// detected type through the same alias resolver as the target so
	// long-form detections ("claude") match short-form targets ("cc") and
	// vice versa (ntm#212).
	for _, p := range panes {
		if p.Dead || p.IsServicePane() || dispatch.RefuseDeadAgentPane(p) != nil {
			continue
		}
		if normalizeAgentType(string(p.Type)) == targetType {
			// Check model if specified
			if model != "" && p.Variant != model {
				continue
			}
			return p.ID, nil
		}
	}

	// Second pass: relaxed match (type only, ignore model mismatch if not found)
	// Only if model was specified but not found
	if model != "" {
		for _, p := range panes {
			if p.Dead || p.IsServicePane() || dispatch.RefuseDeadAgentPane(p) != nil {
				continue
			}
			if normalizeAgentType(string(p.Type)) == targetType {
				return p.ID, nil
			}
		}
	}

	return "", fmt.Errorf("no agent found for type %s (model %s)", agentType, model)
}

func normalizeAgentType(t string) string {
	switch agent.AgentType(t).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "cc"
	case agent.AgentTypeCodex:
		return "cod"
	case agent.AgentTypeGemini:
		return "gmi"
	case agent.AgentTypeAntigravity:
		return "agy"
	case agent.AgentTypeOmp:
		return "omp"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

func waitForIdle(ctx context.Context, detector status.Detector, paneID string, evidence ...func() (bool, error)) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(30 * time.Minute) // Max 30 min per stage default

	// Require a stable idle streak before declaring completion — a single
	// idle reading can be a transient TUI misclassification (ntm#213). Keep
	// in sync with Executor.waitForIdle.
	idleStreak := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for agent")
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			fresh := true
			for _, observe := range evidence {
				ready, err := observe()
				if err != nil {
					return err
				}
				fresh = fresh && ready
			}
			s, err := detector.Detect(paneID)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				idleStreak = 0
				continue
			}
			if s.State == status.StateIdle && fresh {
				idleStreak++
				if idleStreak >= idleStablePolls {
					return nil
				}
			} else {
				idleStreak = 0
			}
			// Optional: print progress indicator
		}
	}
}
