// Package robot provides machine-readable output for AI agents and automation.
package robot

import (
	"regexp"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// DetectionMethod describes how an agent type was detected
type DetectionMethod string

const (
	// MethodTitle indicates detection from pane title
	MethodTitle DetectionMethod = "title"
	// MethodProcess indicates detection from running process/command
	MethodProcess DetectionMethod = "process"
	// MethodContent indicates detection from pane content analysis
	MethodContent DetectionMethod = "content"
	// MethodUnknown indicates no reliable detection method succeeded
	MethodUnknown DetectionMethod = "unknown"
)

// AgentDetection represents the result of agent type detection
type AgentDetection struct {
	Type       string          `json:"type"`       // claude, codex, gemini, etc.
	Confidence float64         `json:"confidence"` // 0.0-1.0 confidence score
	Method     DetectionMethod `json:"method"`     // how the type was detected
}

// processPatterns maps process/command names to agent types
var processPatterns = map[string]string{
	"claude":         "claude",
	"claude-code":    "claude",
	"codex":          "codex",
	"codex-cli":      "codex",
	"openai-codex":   "codex",
	"gemini":         "gemini",
	"gemini-cli":     "gemini",
	"cursor":         "cursor",
	"windsurf":       "windsurf",
	"aider":          "aider",
	"aider-chat":     "aider",
}

// contentPatterns provides regex patterns for detecting agents from output
var contentPatterns = []struct {
	agentType string
	patterns  []*regexp.Regexp
}{
	{
		agentType: "claude",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)claude\s*(code|>|$)`),
			regexp.MustCompile(`(?i)anthropic`),
			regexp.MustCompile(`(?i)\[claude\]`),
		},
	},
	{
		agentType: "codex",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)codex\s*(>|cli|$)`),
			regexp.MustCompile(`(?i)openai\s+codex`),
		},
	},
	{
		agentType: "gemini",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)gemini\s*(>|cli|$)`),
			regexp.MustCompile(`(?i)google\s+ai`),
		},
	},
	{
		agentType: "cursor",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)cursor\s*(>|ai|$)`),
		},
	},
	{
		agentType: "windsurf",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)windsurf\s*(>|$)`),
			regexp.MustCompile(`(?i)codeium`),
		},
	},
	{
		agentType: "aider",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)aider\s*(>|$)`),
			regexp.MustCompile(`(?i)aider/`),
		},
	},
}

// DetectAgentTypeEnhanced performs multi-method agent type detection
// Priority: Process > Content > Title > Unknown
func DetectAgentTypeEnhanced(pane tmux.Pane, content string) AgentDetection {
	// Try process-based detection first (highest confidence)
	if detection := detectFromProcess(pane.Command); detection.Type != "unknown" {
		return detection
	}

	// Try content-based detection (medium-high confidence)
	if content != "" {
		if detection := detectFromContent(content); detection.Type != "unknown" {
			return detection
		}
	}

	// Try title-based detection (medium confidence)
	if detection := detectFromTitle(pane.Title); detection.Type != "unknown" {
		return detection
	}

	// Try NTM pane title convention (e.g., session__cc_1)
	if detection := detectFromNTMTitle(pane.Title); detection.Type != "unknown" {
		return detection
	}

	// Unknown
	return AgentDetection{
		Type:       "unknown",
		Confidence: 0.0,
		Method:     MethodUnknown,
	}
}

// detectFromProcess checks the running command for agent process names
func detectFromProcess(command string) AgentDetection {
	command = strings.ToLower(command)

	for pattern, agentType := range processPatterns {
		if strings.Contains(command, pattern) {
			return AgentDetection{
				Type:       agentType,
				Confidence: 0.95,
				Method:     MethodProcess,
			}
		}
	}

	return AgentDetection{Type: "unknown", Confidence: 0.0, Method: MethodUnknown}
}

// detectFromContent analyzes pane content for agent signatures
func detectFromContent(content string) AgentDetection {
	// Strip ANSI codes for cleaner matching
	content = status.StripANSI(content)

	for _, cp := range contentPatterns {
		for _, pattern := range cp.patterns {
			if pattern.MatchString(content) {
				return AgentDetection{
					Type:       cp.agentType,
					Confidence: 0.75,
					Method:     MethodContent,
				}
			}
		}
	}

	return AgentDetection{Type: "unknown", Confidence: 0.0, Method: MethodUnknown}
}

// detectFromTitle checks pane title for agent type keywords
func detectFromTitle(title string) AgentDetection {
	title = strings.ToLower(title)

	agents := []string{"claude", "codex", "gemini", "cursor", "windsurf", "aider"}
	for _, agent := range agents {
		if strings.Contains(title, agent) {
			return AgentDetection{
				Type:       agent,
				Confidence: 0.6,
				Method:     MethodTitle,
			}
		}
	}

	return AgentDetection{Type: "unknown", Confidence: 0.0, Method: MethodUnknown}
}

// detectFromNTMTitle checks for NTM's pane title convention (session__type_n)
func detectFromNTMTitle(title string) AgentDetection {
	// Check for __cc, __cod, __gmi suffixes
	switch {
	case strings.Contains(title, "__cc"):
		return AgentDetection{Type: "claude", Confidence: 0.9, Method: MethodTitle}
	case strings.Contains(title, "__cod"):
		return AgentDetection{Type: "codex", Confidence: 0.9, Method: MethodTitle}
	case strings.Contains(title, "__gmi"):
		return AgentDetection{Type: "gemini", Confidence: 0.9, Method: MethodTitle}
	}

	return AgentDetection{Type: "unknown", Confidence: 0.0, Method: MethodUnknown}
}

// DetectAllAgents detects agent types for all panes in a session
func DetectAllAgents(session string) (map[int]AgentDetection, error) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, err
	}

	results := make(map[int]AgentDetection)
	for _, pane := range panes {
		// Try to capture some content for detection
		content := ""
		if captured, err := tmux.CapturePaneOutput(pane.ID, 50); err == nil {
			content = captured
		}

		results[pane.Index] = DetectAgentTypeEnhanced(pane, content)
	}

	return results, nil
}
