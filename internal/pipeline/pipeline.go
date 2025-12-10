package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

	detector := status.NewDetector()

	for i, stage := range p.Stages {
		fmt.Printf("Stage %d/%d [%s]: %s\n", i+1, len(p.Stages), stage.AgentType, truncate(stage.Prompt, 50))

		// 1. Find a suitable pane
		paneID, err := findPaneForStage(p.Session, stage.AgentType, stage.Model)
		if err != nil {
			return fmt.Errorf("stage %d failed: %w", i+1, err)
		}

		// 2. Prepare prompt
		prompt := stage.Prompt
		if previousOutput != "" {
			prompt = fmt.Sprintf("%s\n\nResult from previous stage:\n%s", prompt, previousOutput)
		}

		// 3. Send prompt
		if err := tmux.SendKeys(paneID, prompt, true); err != nil {
			return fmt.Errorf("stage %d sending prompt: %w", i+1, err)
		}

		// 4. Wait for working state (debounce)
		time.Sleep(2 * time.Second)

		// 5. Wait for idle state
		fmt.Printf("  Waiting for agent...")
		if err := waitForIdle(ctx, detector, paneID); err != nil {
			return fmt.Errorf("stage %d waiting for completion: %w", i+1, err)
		}
		fmt.Println(" Done.")

		// 6. Capture output
		// We capture the whole buffer for now. Refining this to just the last response is hard without markers.
		// A simple heuristic is to take the last 50 lines.
		output, err := tmux.CapturePaneOutput(paneID, 50)
		if err != nil {
			return fmt.Errorf("stage %d capturing output: %w", i+1, err)
		}
		previousOutput = output
	}

	return nil
}

func findPaneForStage(session, agentType, model string) (string, error) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return "", err
	}

	// First pass: look for exact match (type + model)
	for _, p := range panes {
		if string(p.Type) == agentType {
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
			if string(p.Type) == agentType {
				return p.ID, nil
			}
		}
	}

	return "", fmt.Errorf("no agent found for type %s (model %s)", agentType, model)
}

func waitForIdle(ctx context.Context, detector status.Detector, paneID string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute) // Max 5 min per stage default

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for agent")
		case <-ticker.C:
			s, err := detector.Detect(paneID)
			if err != nil {
				continue
			}
			if s.State == status.StateIdle {
				return nil
			}
			// Optional: print progress indicator
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
