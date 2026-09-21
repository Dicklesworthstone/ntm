package auth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
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

	baselineMu sync.RWMutex
	baselines  map[string]string // Pane output observed before sending /login.
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
	// Sending /login does not immediately erase scrollback. Capture the old
	// output first so a previous login cannot authorize this attempt's work.
	baseline, err := f.captureOutput(paneID, 30)
	if err != nil {
		return fmt.Errorf("capture auth baseline for pane %q: %w", paneID, err)
	}
	if err := f.sendKeys(paneID, "/login", true); err != nil {
		return err
	}
	f.baselineMu.Lock()
	if f.baselines == nil {
		f.baselines = make(map[string]string)
	}
	f.baselines[paneID] = baseline
	f.baselineMu.Unlock()
	return nil
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			output, err := f.captureOutput(paneID, 30)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				return nil, fmt.Errorf("capture auth pane %q: %w", paneID, err)
			}

			f.baselineMu.RLock()
			baseline := f.baselines[paneID]
			f.baselineMu.RUnlock()
			output = authOutputAfterBaseline(baseline, output)

			// Pending signals participate in ordering too: a newer browser URL
			// or code prompt invalidates an older terminal success or failure.
			switch f.latestAuthResult(output) {
			case AuthSuccess:
				return &AuthResult{State: AuthSuccess}, nil
			case AuthFailed:
				return &AuthResult{State: AuthFailed, Error: fmt.Errorf("authentication failed")}, nil
			case AuthNeedsChallenge:
				return &AuthResult{State: AuthNeedsChallenge}, nil
			case AuthNeedsBrowser:
				url, _ := f.DetectBrowserURL(output)
				return &AuthResult{State: AuthNeedsBrowser, URL: url}, nil
			}
		}
	}
}

// WaitForAuth waits for a terminal result, reporting changed pending prompts.
// Repeated prompts are normal while the operator works in the browser; neither
// a repeated prompt nor a browser-to-challenge transition is an auth failure.
func (f *ClaudeAuthFlow) WaitForAuth(ctx context.Context, paneID string, progress func(AuthResult)) error {
	var previous AuthResult
	for {
		result, err := f.MonitorAuth(ctx, paneID)
		if err != nil {
			return err
		}
		switch result.State {
		case AuthSuccess:
			return nil
		case AuthFailed:
			if result.Error != nil {
				return result.Error
			}
			return fmt.Errorf("authentication failed")
		case AuthNeedsBrowser, AuthNeedsChallenge:
			if progress != nil && (result.State != previous.State || result.URL != previous.URL) {
				progress(*result)
			}
			previous = *result
		default:
			return fmt.Errorf("unexpected authentication state %q", result.State)
		}
	}
}

// authOutputAfterBaseline removes unchanged leading lines already visible
// before /login. The viewport may scroll or replace its last prompt line, so
// match complete lines at each possible old viewport offset, not byte prefixes
// that could accidentally strip part of a new authentication signal.
func authOutputAfterBaseline(baseline, output string) string {
	baseline = strings.TrimRight(baseline, " \t\r\n")
	if baseline == "" {
		return output
	}
	before := strings.Split(baseline, "\n")
	after := strings.Split(strings.TrimRight(output, " \t\r\n"), "\n")
	matched := 0
	for start := range before {
		n := 0
		for start+n < len(before) && n < len(after) && before[start+n] == after[n] {
			n++
		}
		if n > matched {
			matched = n
		}
	}
	return strings.Join(after[matched:], "\n")
}

// SendContinuation sends a prompt to continue after auth is complete
func (f *ClaudeAuthFlow) SendContinuation(paneID, prompt string) error {
	// Wait briefly for prompt to be ready
	f.sleep(500 * time.Millisecond)

	// Send continuation prompt
	return f.pasteKeys(paneID, prompt, true)
}

// claudeLoginURLRegex matches the Claude login URL
var claudeLoginURLRegex = regexp.MustCompile(`https://claude\.ai/login\S*`)

// DetectBrowserURL finds the auth URL in the output
func (f *ClaudeAuthFlow) DetectBrowserURL(output string) (string, bool) {
	// Pattern: "Visit https://claude.ai/login?..." or "Open this URL: https://..."
	// We'll look for standard https links associated with claude/login
	matches := claudeLoginURLRegex.FindAllString(output, -1)
	if len(matches) > 0 {
		return strings.TrimRight(matches[len(matches)-1], ".,;:!?)]}\"'"), true
	}
	return "", false
}

func (f *ClaudeAuthFlow) latestAuthResult(output string) AuthState {
	state, latest := AuthInProgress, -1
	for _, candidate := range []struct {
		state AuthState
		index int
	}{
		{AuthSuccess, latestAuthSignal(output, "Successfully logged in", "Login successful")},
		{AuthFailed, latestAuthSignal(output, "Login failed", "Authentication failed", "Error logging in")},
		{AuthNeedsChallenge, latestAuthSignal(output, "Enter code:", "Enter the code")},
	} {
		if candidate.index > latest {
			state, latest = candidate.state, candidate.index
		}
	}
	urls := claudeLoginURLRegex.FindAllStringIndex(output, -1)
	if len(urls) > 0 && urls[len(urls)-1][0] > latest {
		state = AuthNeedsBrowser
	}
	return state
}

func latestAuthSignal(output string, signals ...string) int {
	latest := -1
	for _, signal := range signals {
		if index := strings.LastIndex(output, signal); index > latest {
			latest = index
		}
	}
	return latest
}
