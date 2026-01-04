package pipeline

import (
	"context"
	"fmt"
	"strings"
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
	var lastPaneID string

	detector := status.NewDetector()

	for i, stage := range p.Stages {
		fmt.Printf("Stage %d/%d [%s]: %s\n", i+1, len(p.Stages), stage.AgentType, truncate(stage.Prompt, 50))

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

		// 4. Wait for working state (debounce)
		time.Sleep(2 * time.Second)

		// 5. Wait for idle state
		fmt.Printf("  Waiting for agent...")
		if err := waitForIdle(ctx, detector, paneID); err != nil {
			return fmt.Errorf("stage %d waiting for completion: %w", i+1, err)
		}
		fmt.Println(" Done.")

		// 6. Capture output
		// We capture a larger buffer to ensure we get the full response.
		// 2000 lines should cover most responses without being excessive.
		afterOutput, err := tmux.CapturePaneOutput(paneID, 2000)
		if err != nil {
			return fmt.Errorf("stage %d capturing output: %w", i+1, err)
		}

		// Extract only the new content
		output := extractNewOutput(beforeOutput, afterOutput)
		previousOutput = output
		lastPaneID = paneID
	}

	return nil
}

// extractNewOutput isolates the text added to the pane after the before state.
func extractNewOutput(before, after string) string {
	if before == "" {
		return after
	}
	if after == "" {
		return ""
	}

	// Simple suffix check: if 'after' ends with 'before' (unlikely as new text is appended),
	// or 'after' starts with 'before' (likely).
	if len(after) >= len(before) && after[:len(before)] == before {
		return after[len(before):]
	}

	// If history scrolled (before is not a strict prefix), try to overlap.
	// Find the longest suffix of 'before' that matches a prefix of 'after'.
	const overlapSize = 100
	var tail string
	if len(before) > overlapSize {
		tail = before[len(before)-overlapSize:]
	} else {
		tail = before
	}

	// Search for tail in after and verify it matches the end of 'before'
	start := 0
	for {
		idx := strings.Index(after[start:], tail)
		if idx == -1 {
			break
		}
		actualIdx := start + idx
		matchEnd := actualIdx + len(tail)

		// Verify: is the prefix of 'after' up to this match a suffix of 'before'?
		candidate := after[:matchEnd]
		if strings.HasSuffix(before, candidate) {
			return after[matchEnd:]
		}

		// Continue searching
		start = actualIdx + 1
	}

	// Fallback: return everything if we can't match (better than nothing)
	return after
}

func findPaneForStage(session, agentType, model string) (string, error) {
	targetType := normalizeAgentType(agentType)
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return "", err
	}

	// First pass: look for exact match (type + model)
	for _, p := range panes {
		if string(p.Type) == targetType {
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
			if string(p.Type) == targetType {
				return p.ID, nil
			}
		}
	}

	return "", fmt.Errorf("no agent found for type %s (model %s)", agentType, model)
}

func normalizeAgentType(t string) string {
	switch strings.ToLower(t) {
	case "claude", "cc", "claude-code":
		return "cc"
	case "codex", "cod", "openai":
		return "cod"
	case "gemini", "gmi", "google":
		return "gmi"
	default:
		return t
	}
}

func waitForIdle(ctx context.Context, detector status.Detector, paneID string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(30 * time.Minute) // Max 30 min per stage default

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
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return "..."[:n]
	}
	// Find first rune boundary at or after n-3 bytes
	targetLen := n - 3
	for i := range s {
		if i >= targetLen {
			return s[:i] + "..."
		}
	}
	return s
}
