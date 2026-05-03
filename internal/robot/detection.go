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
	// MethodTmuxPane indicates detection from tmux pane metadata.
	MethodTmuxPane DetectionMethod = "tmux-pane"
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
	"claude":       "claude",
	"claude-code":  "claude",
	"codex":        "codex",
	"codex-cli":    "codex",
	"openai-codex": "codex",
	"gemini":       "gemini",
	"gemini-cli":   "gemini",
	"cursor":       "cursor",
	"windsurf":     "windsurf",
	"aider":        "aider",
	"aider-chat":   "aider",
	// `opencode` (https://opencode.ai) — note we deliberately do NOT add a
	// bare `oc` pattern here because `processPatterns` uses substring match
	// (`strings.Contains`), and `oc` collides with both the OpenShift CLI
	// (a real binary named `oc`) and with any command containing the
	// substring "oc" — e.g. `docker`, `localhost`, `procmon`. Detection
	// must key on the unambiguous binary name `opencode`. Pane titles still
	// use the short `oc` suffix because they are NTM-formatted (`__oc_N`)
	// and parsed by the title regex, not by substring match.
	"opencode":     "oc",
	"ollama":       "ollama",
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
	{
		agentType: "ollama",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?im)(^ollama>\s*$|\bollama\s+(run|chat|serve|pull)\b|^\s*ollama\s+cli\b)`),
		},
	},
}

// DetectAgentTypeEnhanced performs multi-method agent type detection.
// Priority: tmux pane metadata > Process > Content > Title > Unknown.
func DetectAgentTypeEnhanced(pane tmux.Pane, content string) AgentDetection {
	// Prefer tmux's parsed pane type when available; it is more reliable than
	// command/title/content heuristics for customized panes.
	if detection := detectFromPaneType(pane); detection.Type != "unknown" {
		return detection
	}

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
	if detection := DetectFromTitle(pane.Title); detection.Type != "unknown" {
		return detection
	}

	// Try NTM pane title convention (e.g., session__cc_1)
	if detection := DetectFromNTMTitle(pane.Title); detection.Type != "unknown" {
		return detection
	}

	// Unknown
	return AgentDetection{
		Type:       "unknown",
		Confidence: 0.0,
		Method:     MethodUnknown,
	}
}

func detectFromPaneType(pane tmux.Pane) AgentDetection {
	agentType := agentTypeString(pane.Type)
	if agentType == "unknown" || agentType == "user" {
		return AgentDetection{Type: "unknown", Confidence: 0.0, Method: MethodUnknown}
	}

	return AgentDetection{
		Type:       agentType,
		Confidence: 1.0,
		Method:     MethodTmuxPane,
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

// DetectFromTitle checks pane title for agent type keywords
func DetectFromTitle(title string) AgentDetection {
	title = strings.ToLower(title)

	agents := []string{"claude", "codex", "gemini", "cursor", "windsurf", "aider", "ollama"}
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

// DetectFromNTMTitle checks for NTM's pane title convention (session__type_n)
func DetectFromNTMTitle(title string) AgentDetection {
	// Check for encoded pane type markers in titles like "proj__cc_1" or
	// "proj__cursor_1". The helper enforces type-token boundaries to avoid
	// false positives from unrelated substrings.
	lower := strings.ToLower(title)
	switch {
	case containsShortForm(lower, "cc"):
		return AgentDetection{Type: "claude", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "cod"):
		return AgentDetection{Type: "codex", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "gmi"):
		return AgentDetection{Type: "gemini", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "cursor"):
		return AgentDetection{Type: "cursor", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "windsurf"), containsShortForm(lower, "ws"):
		return AgentDetection{Type: "windsurf", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "aider"):
		return AgentDetection{Type: "aider", Confidence: 0.9, Method: MethodTitle}
	case containsShortForm(lower, "ollama"):
		return AgentDetection{Type: "ollama", Confidence: 0.9, Method: MethodTitle}
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
