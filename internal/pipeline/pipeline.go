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
	var previousOutput string
	var lastPaneID string

	detector := status.NewDetector()

	for i, stage := range p.Stages {
		log.Printf("Stage %d/%d [%s]: %s", i+1, len(p.Stages), stage.AgentType, util.Truncate(stage.Prompt, 50))

		// 1. Find a suitable pane
		paneID, err := findPaneForStage(p.Session, stage.AgentType, stage.Model)
		if err != nil {
			return fmt.Errorf("stage %d failed: %w", i+1, err)
		}

		// Capture state BEFORE sending prompt to isolate the new response later
		beforeOutput, err := tmux.CapturePaneOutput(paneID, 2000)
		if err != nil {
			// Non-fatal, just means we might capture too much context
			beforeOutput = ""
		}

		// 2. Prepare prompt
		prompt := stage.Prompt
		if previousOutput != "" {
			if paneID == lastPaneID {
				// Same agent, context is already in the pane history.
				// Add a reference to the previous output instead of duplicating it.
				prompt = fmt.Sprintf("%s\n\n(See previous output above)", prompt)
			} else {
				// Different agent, inject the output from the previous stage
				prompt = fmt.Sprintf("%s\n\nResult from previous stage:\n%s", prompt, previousOutput)
			}
		}

		// 3. Send prompt
		if err := tmux.PasteKeys(paneID, prompt, true); err != nil {
			return fmt.Errorf("stage %d sending prompt: %w", i+1, err)
		}

		// 3b. Establish that the prompt actually left the composer before
		// treating pane idleness as completion (ntm#320). Without this an
		// agent TUI that swallowed the paste's Enter leaves the instruction
		// unsubmitted, the pane reads idle, and the stage advances having
		// executed nothing.
		if err := dispatch.VerifyAgentSubmission(ctx, paneID, prompt,
			tmux.AgentType(normalizeAgentType(stage.AgentType)), 0); err != nil {
			return fmt.Errorf("stage %d prompt was not submitted: %w", i+1, err)
		}

		// 4. Wait for working state (debounce)
		time.Sleep(2 * time.Second)

		// 5. Wait for idle state
		log.Printf("  Waiting for agent...")
		if err := waitForIdle(ctx, detector, paneID); err != nil {
			return fmt.Errorf("stage %d waiting for completion: %w", i+1, err)
		}
		log.Printf("  Done.")

		// 6. Capture output
		// We capture a larger buffer to ensure we get the full response.
		// 2000 lines should cover most responses without being excessive.
		afterOutput, err := tmux.CapturePaneOutput(paneID, 2000)
		if err != nil {
			return fmt.Errorf("stage %d capturing output: %w", i+1, err)
		}

		// Extract only the new content
		output := util.ExtractNewOutput(beforeOutput, afterOutput)
		previousOutput = output
		lastPaneID = paneID
	}

	return nil
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

func waitForIdle(ctx context.Context, detector status.Detector, paneID string) error {
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
			s, err := detector.Detect(paneID)
			if err != nil {
				idleStreak = 0
				continue
			}
			if s.State == status.StateIdle {
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
