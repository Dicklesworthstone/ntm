// Package gemini provides Gemini CLI-specific functionality for NTM.
package gemini

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SetupConfig configures Gemini post-spawn setup behavior.
type SetupConfig struct {
	// AutoSelectProModel automatically selects the Pro model after spawn.
	AutoSelectProModel bool

	// ReadyTimeout is how long to wait for Gemini to be ready.
	ReadyTimeout time.Duration

	// ModelSelectTimeout is how long to wait for model selection menu.
	ModelSelectTimeout time.Duration

	// PollInterval is how often to check pane output.
	PollInterval time.Duration

	// Verbose enables debug output.
	Verbose bool
}

// geminiPromptPattern matches the Gemini CLI prompt.
var geminiPromptPattern = regexp.MustCompile(`(?i)gemini>\s*$`)

// modelMenuPatterns indicate the model selection menu is visible.
// These patterns are used to detect when the /model command has shown its menu.
var modelMenuPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)select.*model`),
	regexp.MustCompile(`(?i)available.*models?`),
	regexp.MustCompile(`(?i)1\.\s*\w+.*\n.*2\.`), // numbered list like "1. Flash\n2. Pro"
	regexp.MustCompile(`(?i)flash|pro|ultra`),    // model names
	regexp.MustCompile(`(?i)\[\s*\d+\s*\]`),      // bracket selection [1] [2] etc
}

// proModelPatterns indicate Pro model is selected or active.
var proModelPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)pro.*selected`),
	regexp.MustCompile(`(?i)selected.*pro`),
	regexp.MustCompile(`(?i)using.*pro`),
	regexp.MustCompile(`(?i)model.*:\s*.*pro`),
	regexp.MustCompile(`(?i)gemini[- ]?2\.5[- ]?pro`),
	regexp.MustCompile(`(?i)gemini[- ]?3`), // Gemini 3 is Pro
}

// PostSpawnSetup performs post-spawn configuration for a Gemini agent.
// It waits for the agent to be ready, then optionally selects the Pro model.
func PostSpawnSetup(ctx context.Context, paneID string, cfg SetupConfig) error {
	if !cfg.AutoSelectProModel {
		return nil
	}

	// Step 1: Wait for Gemini to be ready (showing prompt)
	if err := WaitForReady(ctx, paneID, cfg.ReadyTimeout, cfg.PollInterval); err != nil {
		return fmt.Errorf("waiting for Gemini ready: %w", err)
	}

	// Step 2: Select Pro model
	if err := SelectProModel(ctx, paneID, cfg); err != nil {
		return fmt.Errorf("selecting Pro model: %w", err)
	}

	return nil
}

// WaitForReady waits until the Gemini CLI is ready and showing its prompt.
func WaitForReady(ctx context.Context, paneID string, timeout time.Duration, pollInterval time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastOutput string
	var lastErr error

	for {
		select {
		case <-ctx.Done():
			// Provide helpful context in timeout error
			errMsg := fmt.Sprintf("timeout after %v waiting for Gemini prompt", timeout)
			if lastErr != nil {
				errMsg += fmt.Sprintf(" (last capture error: %v)", lastErr)
			} else if lastOutput != "" {
				// Include last few chars of output for debugging
				snippet := lastOutput
				if len(snippet) > 100 {
					snippet = "..." + snippet[len(snippet)-100:]
				}
				errMsg += fmt.Sprintf(" (last output: %q)", snippet)
			}
			return fmt.Errorf("%s - consider increasing gemini_setup.ready_timeout_seconds in config", errMsg)
		case <-ticker.C:
			output, err := tmux.CapturePaneOutput(paneID, 20)
			if err != nil {
				lastErr = err
				continue
			}
			lastOutput = output
			lastErr = nil

			// Check for Gemini prompt
			if isGeminiReady(output) {
				return nil
			}
		}
	}
}

// isGeminiReady checks if the pane output indicates Gemini is ready.
func isGeminiReady(output string) bool {
	// Strip ANSI codes for cleaner matching
	clean := status.StripANSI(output)

	// Check last few lines for prompt
	lines := strings.Split(clean, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if geminiPromptPattern.MatchString(line) {
			return true
		}
		// Also check for "gemini>" anywhere in the line (robust)
		if strings.Contains(strings.ToLower(line), "gemini>") {
			return true
		}
	}
	return false
}

// SelectProModel sends the /model command and selects the Pro model.
func SelectProModel(ctx context.Context, paneID string, cfg SetupConfig) error {
	// Step 1: Send /model command
	if err := tmux.SendKeys(paneID, "/model", true); err != nil {
		return fmt.Errorf("sending /model command: %w", err)
	}

	// Step 2: Wait for model selection menu to appear
	if err := waitForModelMenu(ctx, paneID, cfg.ModelSelectTimeout, cfg.PollInterval); err != nil {
		// If menu doesn't appear, maybe /model isn't supported or already at Pro
		// Try to continue anyway - not a fatal error
		if cfg.Verbose {
			log.Printf("gemini setup: model menu not detected, attempting selection anyway")
		}
	}

	// Brief pause to let the menu render
	time.Sleep(200 * time.Millisecond)

	// Step 3: Send Down arrow to move to Pro option (assuming Flash is first, Pro is second)
	if err := sendDownArrow(paneID); err != nil {
		return fmt.Errorf("sending down arrow: %w", err)
	}

	// Brief pause between keystrokes
	time.Sleep(100 * time.Millisecond)

	// Step 4: Send Enter to confirm selection
	if err := tmux.SendKeys(paneID, "", true); err != nil {
		return fmt.Errorf("sending enter to confirm: %w", err)
	}

	// Step 5: Wait a moment then verify selection (optional, best-effort)
	time.Sleep(500 * time.Millisecond)

	// Check if Pro model is now selected (verification)
	output, err := tmux.CapturePaneOutput(paneID, 30)
	if err == nil && cfg.Verbose {
		if isProModelSelected(output) {
			log.Printf("gemini setup: Pro model selected successfully")
		} else {
			log.Printf("gemini setup: could not verify Pro model selection")
		}
	}

	return nil
}

// waitForModelMenu waits until the model selection menu is visible.
func waitForModelMenu(ctx context.Context, paneID string, timeout time.Duration, pollInterval time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastOutput string

	for {
		select {
		case <-ctx.Done():
			// Non-fatal timeout - model menu might not appear if /model command isn't supported
			errMsg := fmt.Sprintf("timeout after %v waiting for model menu", timeout)
			if lastOutput != "" {
				snippet := lastOutput
				if len(snippet) > 100 {
					snippet = "..." + snippet[len(snippet)-100:]
				}
				errMsg += fmt.Sprintf(" (last output: %q)", snippet)
			}
			return fmt.Errorf("%s", errMsg)
		case <-ticker.C:
			output, err := tmux.CapturePaneOutput(paneID, 30)
			if err != nil {
				continue
			}
			lastOutput = output

			if isModelMenuVisible(output) {
				return nil
			}
		}
	}
}

// isModelMenuVisible checks if the model selection menu is showing.
func isModelMenuVisible(output string) bool {
	clean := status.StripANSI(output)
	for _, pattern := range modelMenuPatterns {
		if pattern.MatchString(clean) {
			return true
		}
	}
	return false
}

// isProModelSelected checks if the Pro model appears to be selected.
func isProModelSelected(output string) bool {
	clean := status.StripANSI(output)
	for _, pattern := range proModelPatterns {
		if pattern.MatchString(clean) {
			return true
		}
	}
	return false
}

// sendDownArrow sends a down arrow key to the pane.
func sendDownArrow(paneID string) error {
	// tmux send-keys Down (or the escape sequence)
	return tmux.DefaultClient.RunSilent("send-keys", "-t", paneID, "Down")
}
