package rotation

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// Provider defines the interface for an AI agent authentication provider
type Provider interface {
	Name() string
	LoginCommand() string
	ExitCommand() string
	AuthSuccessPatterns() []string
	ContinuationPrompt() string
	SupportsReauth() bool
}

// GetProvider returns the provider implementation for the given agent type
func GetProvider(agentType string) Provider {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return &ClaudeProvider{}
	case agent.AgentTypeCodex:
		return &CodexProvider{}
	case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
		// Antigravity (agy) is the Gemini CLI's successor and shares Google's
		// auth/quota path, so it reuses the Gemini rotation provider.
		return &GeminiProvider{}
	default:
		if strings.EqualFold(strings.TrimSpace(agentType), "anthropic") {
			return &ClaudeProvider{}
		}
		return nil
	}
}

// ClaudeProvider implementation
type ClaudeProvider struct{}

func (p *ClaudeProvider) Name() string         { return "Claude" }
func (p *ClaudeProvider) LoginCommand() string { return "/login" }
func (p *ClaudeProvider) ExitCommand() string  { return "/exit" }
func (p *ClaudeProvider) AuthSuccessPatterns() []string {
	return []string{
		"successfully logged in",
		"authenticated as",
	}
}
func (p *ClaudeProvider) ContinuationPrompt() string { return "continue. Use ultrathink" }
func (p *ClaudeProvider) SupportsReauth() bool       { return true }

// CodexProvider implementation
type CodexProvider struct{}

func (p *CodexProvider) Name() string                  { return "Codex" }
func (p *CodexProvider) LoginCommand() string          { return "/logout" } // Codex needs restart
func (p *CodexProvider) ExitCommand() string           { return "/exit" }
func (p *CodexProvider) AuthSuccessPatterns() []string { return nil } // Restart strategy doesn't use in-pane auth
func (p *CodexProvider) ContinuationPrompt() string    { return "" }
func (p *CodexProvider) SupportsReauth() bool          { return false }

// GeminiProvider implementation
type GeminiProvider struct{}

func (p *GeminiProvider) Name() string         { return "Gemini" }
func (p *GeminiProvider) LoginCommand() string { return "/auth" } // Guessing based on epic description
func (p *GeminiProvider) ExitCommand() string  { return "/exit" }
func (p *GeminiProvider) AuthSuccessPatterns() []string {
	return []string{"authenticated", "logged in"}
}
func (p *GeminiProvider) ContinuationPrompt() string { return "continue" }
func (p *GeminiProvider) SupportsReauth() bool       { return false } // Assuming restart for safety initially

// MatchesAuthSuccess checks whether captured terminal output contains any of
// the provider's authentication success markers (case-insensitive).
func MatchesAuthSuccess(p Provider, output string) (string, bool) {
	if p == nil || strings.TrimSpace(output) == "" {
		return "", false
	}
	lower := strings.ToLower(output)
	for _, pattern := range p.AuthSuccessPatterns() {
		trimmed := strings.TrimSpace(pattern)
		if trimmed != "" && strings.Contains(lower, strings.ToLower(trimmed)) {
			return trimmed, true
		}
	}
	return "", false
}
