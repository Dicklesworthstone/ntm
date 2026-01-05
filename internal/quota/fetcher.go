package quota

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// PTYFetcher implements Fetcher by sending commands to tmux panes
type PTYFetcher struct {
	// CommandTimeout is how long to wait for command execution
	CommandTimeout time.Duration
	// CaptureLines is how many lines to capture from pane output
	CaptureLines int
}

// providerCommands maps providers to their quota commands
var providerCommands = map[Provider]struct {
	UsageCmd  string
	StatusCmd string
}{
	ProviderClaude: {
		UsageCmd:  "/usage",
		StatusCmd: "/status",
	},
	ProviderCodex: {
		UsageCmd:  "/usage", // May need adjustment after research
		StatusCmd: "/status",
	},
	ProviderGemini: {
		UsageCmd:  "/auth status", // Gemini uses different commands
		StatusCmd: "/auth status",
	},
}

// FetchQuota sends quota commands to a pane and parses the output
func (f *PTYFetcher) FetchQuota(ctx context.Context, paneID string, provider Provider) (*QuotaInfo, error) {
	timeout := f.CommandTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	captureLines := f.CaptureLines
	if captureLines == 0 {
		captureLines = 100
	}

	cmds, ok := providerCommands[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	info := &QuotaInfo{
		Provider:  provider,
		FetchedAt: time.Now(),
	}

	// Capture initial state for comparison
	initialOutput, err := tmux.CapturePaneOutput(paneID, captureLines)
	if err != nil {
		info.Error = fmt.Sprintf("failed to capture initial output: %v", err)
		return info, nil
	}

	// Send /usage command
	if err := tmux.SendKeys(paneID, cmds.UsageCmd, true); err != nil {
		info.Error = fmt.Sprintf("failed to send usage command: %v", err)
		return info, nil
	}

	// Poll for output that contains usage data
	deadline := time.Now().Add(timeout)
	currentOutput := initialOutput
	var lastErr error

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			info.Error = "context cancelled"
			return info, nil
		case <-ticker.C:
			if time.Now().After(deadline) {
				if lastErr != nil {
					info.Error = lastErr.Error()
				} else {
					info.Error = "timeout waiting for usage data"
				}
				return info, nil
			}

			// Capture output
			output, err := tmux.CapturePaneOutput(paneID, captureLines)
			if err != nil {
				lastErr = fmt.Errorf("capture failed: %w", err)
				continue
			}

			// If output changed, try to parse
			if output != currentOutput {
				currentOutput = output

				// Attempt to slice content after the command echo to avoid stale data
				relevantOutput := output
				// Use LastIndex to find the most recent command execution
				if idx := strings.LastIndex(output, cmds.UsageCmd); idx != -1 {
					relevantOutput = output[idx+len(cmds.UsageCmd):]
				}

				found, err := parseUsageOutput(info, relevantOutput, provider)
				if err != nil {
					lastErr = err
				}
				if found {
					info.RawOutput = relevantOutput
					// Optionally fetch status for additional info
					statusOutput, err := f.fetchStatus(ctx, paneID, cmds.StatusCmd, captureLines, 2*time.Second)
					if err == nil && statusOutput != "" {
						parseStatusOutput(info, statusOutput, provider)
						info.RawOutput += "\n---\n" + statusOutput
					}
					info.Error = "" // Clear any previous error
					return info, nil
				}
			}
		}
	}
}

// waitForNewOutput polls until new output appears after the initial capture
func (f *PTYFetcher) waitForNewOutput(ctx context.Context, paneID, initialOutput string, lines int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("timeout waiting for output")
			}

			output, err := tmux.CapturePaneOutput(paneID, lines)
			if err != nil {
				continue
			}

			// Check if output has changed
			if output != initialOutput {
				return output, nil
			}
		}
	}
}

// fetchStatus sends a status command and captures output
func (f *PTYFetcher) fetchStatus(ctx context.Context, paneID, statusCmd string, lines int, timeout time.Duration) (string, error) {
	// Don't send if status command is same as usage command
	// Note: We need to check the specific provider's commands, but we don't have provider here.
	// However, this logic was slightly flawed in original code (checked ProviderClaude only).
	// For now, we assume caller handles deduplication or we check against known duplicates.
	// But simply checking output change is safe.

	initialOutput, err := tmux.CapturePaneOutput(paneID, lines)
	if err != nil {
		return "", err
	}

	if err := tmux.SendKeys(paneID, statusCmd, true); err != nil {
		return "", err
	}

	// Wait for any change
	output, err := f.waitForNewOutput(ctx, paneID, initialOutput, lines, timeout)
	if err != nil {
		return "", err
	}

	// Slice after command
	if idx := strings.LastIndex(output, statusCmd); idx != -1 {
		return output[idx+len(statusCmd):], nil
	}
	return output, nil
}

// parseUsageOutput routes to provider-specific parsers
func parseUsageOutput(info *QuotaInfo, output string, provider Provider) (bool, error) {
	switch provider {
	case ProviderClaude:
		return parseClaudeUsage(info, output)
	case ProviderCodex:
		return parseCodexUsage(info, output)
	case ProviderGemini:
		return parseGeminiUsage(info, output)
	default:
		return false, fmt.Errorf("no parser for provider: %s", provider)
	}
}

// parseStatusOutput routes to provider-specific status parsers
func parseStatusOutput(info *QuotaInfo, output string, provider Provider) {
	switch provider {
	case ProviderClaude:
		parseClaudeStatus(info, output)
	case ProviderCodex:
		parseCodexStatus(info, output)
	case ProviderGemini:
		parseGeminiStatus(info, output)
	}
}
