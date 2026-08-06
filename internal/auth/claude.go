package auth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AuthState represents the current state of authentication
type AuthState string

const (
	AuthInProgress     AuthState = "in_progress"
	AuthNeedsBrowser   AuthState = "needs_browser"
	AuthNeedsChallenge AuthState = "needs_challenge"
	AuthSuccess        AuthState = "success"
	AuthFailed         AuthState = "failed"
)

// AuthResult contains the result of an authentication attempt
type AuthResult struct {
	State AuthState
	Error error
	URL   string // For manual browser opening
}

// ClaudeAuthFlow handles the authentication process for Claude Code
type ClaudeAuthFlow struct {
	isRemote      bool
	sendKeys      func(string, string, bool) error
	pasteKeys     func(string, string, bool) error
	captureOutput func(string, int) (string, error)
	pollInterval  time.Duration
	sleep         func(time.Duration)
}

// NewClaudeAuthFlow creates a new Claude auth flow handler
func NewClaudeAuthFlow(isRemote bool) *ClaudeAuthFlow {
	return &ClaudeAuthFlow{
		isRemote:      isRemote,
		sendKeys:      tmux.SendKeys,
		pasteKeys:     tmux.PasteKeys,
		captureOutput: tmux.CapturePaneOutput,
		pollInterval:  time.Second,
		sleep:         time.Sleep,
	}
}

// InitiateAuth starts the authentication process
func (f *ClaudeAuthFlow) InitiateAuth(paneID string) error {
	return f.sendKeys(paneID, "/login", true)
}

// MonitorAuth watches the pane output for auth prompts and handles them
func (f *ClaudeAuthFlow) MonitorAuth(ctx context.Context, paneID string) (*AuthResult, error) {
	pollInterval := f.pollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			output, err := f.captureOutput(paneID, 30)
			if err != nil {
				return nil, fmt.Errorf("capture auth pane %q: %w", paneID, err)
			}

			// Check for success
			if f.DetectAuthSuccess(output) {
				return &AuthResult{State: AuthSuccess}, nil
			}

			// Check for failure
			if f.DetectAuthFailure(output) {
				return &AuthResult{State: AuthFailed, Error: fmt.Errorf("authentication failed")}, nil
			}

			// Check for challenge code (remote/SSH flow)
			if _, found := f.DetectChallengeCode(output); found {
				// Challenge handling would go here, or we return status to let caller handle it
				return &AuthResult{State: AuthNeedsChallenge}, nil
			}

			// Check for browser URL
			if url, found := f.DetectBrowserURL(output); found {
				if f.isRemote {
					// In remote mode, we return the URL for the user/caller to handle
					return &AuthResult{State: AuthNeedsBrowser, URL: url}, nil
				}
				// In local mode, Claude usually opens the browser automatically,
				// but we might need to confirm or detect that state.
				// For now, if we see a URL, we treat it as 'needs browser' if it's waiting.
				return &AuthResult{State: AuthNeedsBrowser, URL: url}, nil
			}
		}
	}
}

// SendContinuation sends a prompt to continue after auth is complete
func (f *ClaudeAuthFlow) SendContinuation(paneID, prompt string) error {
	// Wait briefly for prompt to be ready
	f.sleep(500 * time.Millisecond)

	// Send continuation prompt
	return f.pasteKeys(paneID, prompt, true)
}

// claudeLoginURLRegex matches the Claude login URL
var claudeLoginURLRegex = regexp.MustCompile(`https://claude\.ai/login\S+`)

// DetectBrowserURL finds the auth URL in the output
func (f *ClaudeAuthFlow) DetectBrowserURL(output string) (string, bool) {
	// Pattern: "Visit https://claude.ai/login?..." or "Open this URL: https://..."
	// We'll look for standard https links associated with claude/login
	match := claudeLoginURLRegex.FindString(output)
	if match != "" {
		return strings.TrimRight(match, ".,;:!?)]}\"'"), true
	}
	return "", false
}

// DetectChallengeCode finds the challenge code prompt
func (f *ClaudeAuthFlow) DetectChallengeCode(output string) (string, bool) {
	// Pattern: "Enter the code displayed in your browser" or similar
	// This might be context-dependent.
	// For now, we look for the prompt asking for a code.
	if strings.Contains(output, "Enter code:") || strings.Contains(output, "Enter the code") {
		return "", true
	}
	return "", false
}

// DetectAuthSuccess checks if authentication was successful
func (f *ClaudeAuthFlow) DetectAuthSuccess(output string) bool {
	return strings.Contains(output, "Successfully logged in") ||
		strings.Contains(output, "Login successful")
}

// DetectAuthFailure checks if authentication failed
func (f *ClaudeAuthFlow) DetectAuthFailure(output string) bool {
	return strings.Contains(output, "Login failed") ||
		strings.Contains(output, "Authentication failed") ||
		strings.Contains(output, "Error logging in")
}
